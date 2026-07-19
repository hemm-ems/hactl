package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// helperDomains lists every HA domain a helper entity can live in — the
// superset used to discover storage-backed (UI-created) helpers via
// /api/states. This is wider than the companion's YAML-managed domain set
// (see hactl-companion routes/helpers.py ALLOWED_DOMAINS): input_button has
// no YAML equivalent, so it only ever shows up here, sourced from storage.
var helperDomains = []string{
	"input_boolean", "input_number", "input_select", "input_text",
	"input_datetime", "input_button", "counter", "timer", "schedule",
}

// helperRow is one row in the merged helper listing: either a YAML helper
// (companion-managed, editable via `helper create`/`set`/`delete`) or a
// storage-backed helper entity discovered live in HA's .storage — not
// editable through hactl's helper CRUD. See issue #71.
type helperRow struct {
	ID     string
	Name   string
	Domain string
	Icon   string
	Source string // "yaml" or "storage"
}

var flagHelperDomain string
var flagHelperFile string
var flagHelperConfirm bool

var helperCmd = &cobra.Command{
	Use:        "helper",
	SuggestFor: []string{"helpers", "input_boolean", "input_number"},
	Short:      "Manage HA helpers (input_boolean, counter, timer, etc.)",
	Long:  "List, create, and delete Home Assistant helper entities via the companion.",
}

var helperLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List helpers",
	Long: "List all helpers, optionally filtered by domain. Unions YAML helpers (companion-managed) " +
		"with storage-backed helpers created in the HA UI (discovered live via the entity states), " +
		"distinguished by a source column — only the yaml ones are editable via create/set/delete.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var helperShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show helper details",
	Long:  "Show the YAML definition of a helper entity.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var helperCatCmd = &cobra.Command{
	Use:   "cat <id>",
	Short: "Print a helper's remote config as YAML",
	Long:  "Fetch and print the current remote YAML definition of a helper, with no header (pipe-friendly).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperCat(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var helperCreateCmd = &cobra.Command{
	Use:   "create <domain>",
	Short: "Create a new helper (dry-run by default)",
	Long: `Create a new helper from a YAML file via the companion.
Supported domains: input_boolean, input_number, input_select, input_text,
input_datetime, counter, timer, schedule.
Use --confirm to apply.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperCreate(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var helperDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a helper (dry-run by default)",
	Long:  "Delete a helper entity via the companion. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHelperDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	helperLsCmd.Flags().StringVar(&flagHelperDomain, "domain", "", "filter by domain (e.g. input_boolean)")
	helperCreateCmd.Flags().StringVarP(&flagHelperFile, "file", "f", "", "YAML file for the new helper")
	helperCreateCmd.Flags().BoolVar(&flagHelperConfirm, "confirm", false, "actually create (default is dry-run)")
	helperDeleteCmd.Flags().BoolVar(&flagHelperConfirm, "confirm", false, "actually delete (default is dry-run)")
	helperCmd.AddCommand(helperLsCmd, helperShowCmd, helperCatCmd, helperCreateCmd, helperDeleteCmd)
	rootCmd.AddCommand(helperCmd)
}

// runHelperCat prints a helper's remote YAML config verbatim, without the
// id/domain header that `helper show` prints — pipe-friendly and consistent
// with `auto cat` / `script cat` / `tpl cat`.
func runHelperCat(ctx context.Context, w io.Writer, helperID string) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	resp, err := cc.GetHelper(ctx, helperID)
	if err != nil {
		return fmt.Errorf("fetching helper: %w", err)
	}
	_, _ = fmt.Fprint(w, resp.Content)
	return nil
}

// runHelperLs lists the union of YAML-sourced helpers (companion's per-domain
// files) and storage-backed helper entities (created in the HA UI, living in
// .storage — invisible to the companion, which only ever reads/writes YAML).
// Most real instances create helpers in the UI, so listing YAML alone reports
// "no helpers" while dozens exist live. See issue #71.
func runHelperLs(ctx context.Context, w io.Writer) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	// Fetch unfiltered: --domain is applied below, after storage helpers are
	// merged in, so it also reaches storage-only domains (e.g. input_button)
	// the companion's YAML CRUD doesn't manage.
	resp, err := cc.ListHelpers(ctx, "")
	if err != nil {
		return fmt.Errorf("listing helpers: %w", err)
	}

	rows := make([]helperRow, 0, len(resp.Helpers))
	yamlEntityIDs := make(map[string]bool, len(resp.Helpers))
	for _, h := range resp.Helpers {
		rows = append(rows, helperRow{ID: h.ID, Name: h.Name, Domain: h.Domain, Icon: h.Icon, Source: "yaml"})
		yamlEntityIDs[h.Domain+"."+h.ID] = true
	}
	rows = append(rows, fetchStorageHelpers(ctx, yamlEntityIDs)...)

	if flagHelperDomain != "" {
		rows = filterHelperRowsByDomain(rows, flagHelperDomain)
	}

	if len(rows) == 0 {
		return emitEmptyList(w, "no helpers")
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Domain != rows[j].Domain {
			return rows[i].Domain < rows[j].Domain
		}
		return rows[i].ID < rows[j].ID
	})

	tbl := &format.Table{
		Headers: []string{"id", "name", "domain", "icon", "source"},
		Rows:    make([][]string, len(rows)),
	}
	for i, r := range rows {
		tbl.Rows[i] = []string{r.ID, r.Name, r.Domain, r.Icon, r.Source}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

// fetchStorageHelpers discovers UI-created helper entities by scanning
// /api/states for entities in a helper domain (helperDomains) that the
// companion's YAML listing didn't already report (skip, keyed by entity_id).
// The id shown is the full entity_id, not a bare slug: unlike YAML helpers,
// there is no `helper show`/`cat` lookup for these — use `ent show` instead.
// Failures here are logged and treated as "no storage helpers found" rather
// than failing the whole command: an unreachable HA shouldn't hide the
// YAML-sourced list that already answered above.
func fetchStorageHelpers(ctx context.Context, skip map[string]bool) []helperRow {
	cfg, err := config.Load(flagDir)
	if err != nil {
		slog.Warn("could not load config for storage helper discovery", "error", err)
		return nil
	}

	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetStates(ctx)
	if err != nil {
		slog.Warn("could not fetch states for storage helper discovery", "error", err)
		return nil
	}

	var states []entityState
	if unmarshalErr := json.Unmarshal(data, &states); unmarshalErr != nil {
		slog.Warn("could not parse states for storage helper discovery", "error", unmarshalErr)
		return nil
	}

	domains := make(map[string]bool, len(helperDomains))
	for _, d := range helperDomains {
		domains[d] = true
	}

	var rows []helperRow
	for _, s := range states {
		domain := parseEntityDomain(s.EntityID)
		if !domains[domain] || skip[s.EntityID] {
			continue
		}
		name, _ := s.Attributes["friendly_name"].(string)
		if name == "" {
			name = s.EntityID
		}
		icon, _ := s.Attributes["icon"].(string)
		rows = append(rows, helperRow{ID: s.EntityID, Name: name, Domain: domain, Icon: icon, Source: "storage"})
	}
	return rows
}

func filterHelperRowsByDomain(rows []helperRow, domain string) []helperRow {
	out := make([]helperRow, 0, len(rows))
	for _, r := range rows {
		if r.Domain == domain {
			out = append(out, r)
		}
	}
	return out
}

func runHelperShow(ctx context.Context, w io.Writer, helperID string) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.GetHelper(ctx, helperID)
	if err != nil {
		return fmt.Errorf("fetching helper: %w", err)
	}

	_, _ = fmt.Fprintf(w, "id:     %s\n", resp.ID)
	_, _ = fmt.Fprintf(w, "domain: %s\n", resp.Domain)
	_, _ = fmt.Fprintf(w, "---\n%s", resp.Content)
	return nil
}

func runHelperCreate(ctx context.Context, w io.Writer, domain string) error {
	if flagHelperFile == "" {
		return errors.New("--file / -f is required for create")
	}

	data, err := os.ReadFile(flagHelperFile) //nolint:gosec // file path provided by user via CLI flag
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}
	content := string(data)

	if !flagHelperConfirm {
		if _, connErr := connectCompanion(ctx); connErr != nil {
			return connErr
		}
		_, _ = fmt.Fprintln(w, "dry-run: would create helper")
		_, _ = fmt.Fprintf(w, "  domain: %s\n", domain)
		_, _ = fmt.Fprintf(w, "  file:   %s\n", flagHelperFile)
		_, _ = fmt.Fprintf(w, "  size:   %d bytes\n", len(data))
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.CreateHelper(ctx, content, domain)
	if err != nil {
		return fmt.Errorf("creating helper: %w", err)
	}

	_, _ = fmt.Fprintf(w, "created helper %q (domain=%s)\n", resp.ID, domain)
	switch {
	case !resp.Reloaded:
		_, _ = fmt.Fprintln(w, "warning: helper written but HA did not confirm reload")
	case !resp.EntityCreated:
		_, _ = fmt.Fprintf(w, "warning: helper reloaded but entity %q was not found in HA's live state\n", resp.EntityID)
	default:
		_, _ = fmt.Fprintf(w, "entity_id: %s\n", resp.EntityID)
	}
	return nil
}

func runHelperDelete(ctx context.Context, w io.Writer, helperID string) error {
	if !flagHelperConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete helper")
		_, _ = fmt.Fprintf(w, "  id: %s\n", helperID)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	if _, err := cc.DeleteHelper(ctx, helperID); err != nil {
		return fmt.Errorf("deleting helper: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted helper %q\n", helperID)
	return nil
}
