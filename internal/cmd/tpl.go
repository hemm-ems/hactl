package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// tplEntityDomains are the entity-domain keys a modern `template:` block can
// declare. Their presence marks the input as a full block rather than a bare
// entity item. Mirrors the companion's _ENTITY_DOMAINS.
var tplEntityDomains = []string{
	"sensor", "binary_sensor", "number", "select", "button", "image", "weather",
	"light", "switch", "lock", "cover", "fan", "device_tracker", "event",
	"alarm_control_panel", "update", "vacuum",
}

// tplBlockKeys are block-level keys (modern plural + legacy singular) that make
// a block trigger-based; they must never appear inside an entity item.
var tplBlockKeys = []string{
	"trigger", "triggers", "action", "actions", "condition", "conditions",
}

// tplKind classifies a create input as either a full block or a bare entity item.
type tplKind struct {
	isBlock      bool
	triggerBased bool
	domains      []string
	strayKey     string // a block-level key found inside a bare item (a corruption trap)
}

// classifyTemplate inspects create input to decide whether it is a full block
// (has an entity-domain key) or a bare entity item, and flags a bare item that
// carries a block-level trigger/action/condition key.
func classifyTemplate(content string) (tplKind, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(content), &m); err != nil {
		return tplKind{}, fmt.Errorf("parsing template YAML (expected a single entity item or block mapping): %w", err)
	}
	if m == nil {
		return tplKind{}, errors.New("template file is empty")
	}
	var k tplKind
	for _, d := range tplEntityDomains {
		if _, ok := m[d]; ok {
			k.isBlock = true
			k.domains = append(k.domains, d)
		}
	}
	for _, t := range tplBlockKeys {
		if _, ok := m[t]; ok {
			k.triggerBased = true
			if !k.isBlock && k.strayKey == "" {
				k.strayKey = t
			}
		}
	}
	return k, nil
}

var flagTplFile string
var flagTplConfirm bool
var flagTplDomain string

var tplCmd = &cobra.Command{
	Use:        "tpl",
	SuggestFor: []string{"template", "templates"},
	Short:      "Manage templates (eval, create, delete)",
	Long:  "Evaluate Jinja2 templates and manage template sensor definitions.",
}

var tplEvalCmd = &cobra.Command{
	Use:   "eval [template]",
	Short: "Evaluate a template",
	Long:  "Evaluate an inline template string or a template from a file (-f).",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTplEval(cmd.Context(), cmd.OutOrStdout(), args)
	},
}

var tplCatCmd = &cobra.Command{
	Use:   "cat <unique_id>",
	Short: "Print a template sensor's remote config as YAML",
	Long:  "Fetch and print the current remote YAML definition of a template sensor (via the companion).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTplCat(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	tplEvalCmd.Flags().StringVarP(&flagTplFile, "file", "f", "", "read template from file")
	tplCreateCmd.Flags().StringVarP(&flagTplFile, "file", "f", "", "YAML file for the new template (an entity item or a full block)")
	tplCreateCmd.Flags().StringVar(&flagTplDomain, "domain", "sensor", "domain for a bare entity item (sensor or binary_sensor); ignored for a full block")
	tplCreateCmd.Flags().BoolVar(&flagTplConfirm, "confirm", false, "actually create (default is dry-run)")
	tplDeleteCmd.Flags().BoolVar(&flagTplConfirm, "confirm", false, "actually delete (default is dry-run)")
	tplCmd.AddCommand(tplEvalCmd, tplCatCmd, tplCreateCmd, tplDeleteCmd)
	rootCmd.AddCommand(tplCmd)
}

// runTplCat prints a template sensor's remote YAML config verbatim. The
// companion returns the definition as YAML text in resp.Content.
func runTplCat(ctx context.Context, w io.Writer, uniqueID string) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	resp, err := cc.GetTemplate(ctx, uniqueID)
	if err != nil {
		return fmt.Errorf("fetching template: %w", err)
	}
	_, _ = fmt.Fprint(w, resp.Content)
	return nil
}

var tplCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new template entry (dry-run by default)",
	Long: `Create a new template entry from a YAML file via the companion. Use --confirm to apply.

The file may be either:

  * a bare entity item (state-based), placed into a block for --domain:

      unique_id: room_temp
      name: Room Temp
      state: "{{ states('sensor.x') | float }}"

  * a full block, used for trigger-based or multi-domain entries — the
    trigger/action/condition lives at the block level, never inside the entity:

      triggers:
        - trigger: state
          entity_id: sensor.source
      sensor:
        - name: Sampled
          unique_id: sampled
          state: "{{ trigger.to_state.state }}"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTplCreate(cmd.Context(), cmd.OutOrStdout())
	},
}

var tplDeleteCmd = &cobra.Command{
	Use:   "delete <unique_id>",
	Short: "Delete a template sensor (dry-run by default)",
	Long:  "Delete a template sensor from HA via the companion. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTplDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func runTplEval(ctx context.Context, w io.Writer, args []string) error {
	tpl, err := resolveTemplate(args)
	if err != nil {
		return err
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	result, err := client.RenderTemplate(ctx, tpl)
	if err != nil {
		return fmt.Errorf("rendering template: %w", err)
	}

	_, _ = fmt.Fprintln(w, result)
	return nil
}

func resolveTemplate(args []string) (string, error) {
	if flagTplFile != "" {
		data, err := os.ReadFile(flagTplFile) //nolint:gosec // file path provided by user via CLI flag
		if err != nil {
			return "", fmt.Errorf("reading template file: %w", err)
		}
		return string(data), nil
	}
	if len(args) == 0 {
		return "", errors.New("provide a template string or use -f <file>")
	}
	return args[0], nil
}

func runTplCreate(ctx context.Context, w io.Writer) error {
	if flagTplFile == "" {
		return errors.New("--file / -f is required for create")
	}

	data, err := os.ReadFile(flagTplFile) //nolint:gosec // file path provided by user via CLI flag
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}
	content := string(data)

	kind, err := classifyTemplate(content)
	if err != nil {
		return err
	}
	// Fail fast on the classic corruption trap: a trigger nested inside an
	// entity item. HA rejects it and the companion would too — catch it here
	// with actionable guidance before anything is written.
	if kind.strayKey != "" {
		return fmt.Errorf(
			"%q belongs at the block level, not inside an entity item; supply a full block "+
				"(e.g. `triggers: [...]` alongside `sensor: [ {...} ]`)", kind.strayKey)
	}

	if !flagTplConfirm {
		if _, connErr := connectCompanion(ctx); connErr != nil {
			return connErr
		}
		_, _ = fmt.Fprintln(w, "dry-run: would create template")
		_, _ = fmt.Fprintf(w, "  file:   %s\n", flagTplFile)
		if kind.isBlock {
			shape := "state-based block"
			if kind.triggerBased {
				shape = "trigger-based block"
			}
			_, _ = fmt.Fprintf(w, "  shape:  %s\n", shape)
			_, _ = fmt.Fprintf(w, "  domains: %s\n", strings.Join(kind.domains, ", "))
		} else {
			_, _ = fmt.Fprintln(w, "  shape:  entity item")
			_, _ = fmt.Fprintf(w, "  domain: %s\n", flagTplDomain)
		}
		_, _ = fmt.Fprintf(w, "  size:   %d bytes\n", len(data))
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.CreateTemplate(ctx, content, flagTplDomain)
	if err != nil {
		return fmt.Errorf("creating template: %w", err)
	}

	switch {
	case kind.triggerBased:
		_, _ = fmt.Fprintf(w, "created trigger-based template %q\n", resp.UniqueID)
	case kind.isBlock:
		_, _ = fmt.Fprintf(w, "created template %q\n", resp.UniqueID)
	default:
		_, _ = fmt.Fprintf(w, "created template %q (domain=%s)\n", resp.UniqueID, flagTplDomain)
	}
	return nil
}

func runTplDelete(ctx context.Context, w io.Writer, uniqueID string) error {
	if !flagTplConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete template sensor")
		_, _ = fmt.Fprintf(w, "  unique_id: %s\n", uniqueID)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	if _, err := cc.DeleteTemplate(ctx, uniqueID); err != nil {
		return fmt.Errorf("deleting template: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted template %q\n", uniqueID)
	return nil
}
