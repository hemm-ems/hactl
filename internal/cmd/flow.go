package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage config entries and flows",
	Long:  "List config entries and start, step through, and inspect config entry options flows and config flows.",
}

var flagConfigDomain string

var configEntriesCmd = &cobra.Command{
	Use:   "entries",
	Short: "List config entries",
	Long:  "List all config entries. Use --domain to filter by integration domain.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigEntries(cmd.Context(), cmd.OutOrStdout())
	},
}

var configOptionsCmd = &cobra.Command{
	Use:   "options <entry_id>",
	Short: "Start an options flow for a config entry",
	Long:  "Start an options flow for an existing config entry. Returns the flow ID and initial step schema.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigOptions(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var configFlowStartCmd = &cobra.Command{
	Use:   "flow-start <domain>",
	Short: "Start a config flow for an integration",
	Long:  "Start a new config flow for a domain/integration. Returns the flow ID and initial step schema.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigFlowStart(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var flagFlowData string
var flagFlowOptions bool

var configFlowStepCmd = &cobra.Command{
	Use:   "flow-step <flow_id>",
	Short: "Submit data to advance a flow",
	Long: `Submit data to advance a config/options flow to the next step.

Use --options when stepping through an options flow (started via 'config options <entry_id>').
Without --options, the step is sent to the config flow endpoint
(/api/config/config_entries/flow/) instead of the options flow endpoint
(/api/config/config_entries/options/flow/).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigFlowStep(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var configFlowInspectCmd = &cobra.Command{
	Use:   "flow-inspect <flow_id>",
	Short: "Inspect current flow state",
	Long: `Show the current step, expected schema fields, and any errors for a flow.

Use --options when inspecting an options flow (started via 'config options <entry_id>').
Without --options, the inspect reads from the config flow endpoint instead of the options flow endpoint.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigFlowInspect(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	configEntriesCmd.Flags().StringVar(&flagConfigDomain, "domain", "", "filter entries by integration domain")
	configFlowStepCmd.Flags().StringVar(&flagFlowData, "data", "{}", "JSON data to submit to the flow step")
	configFlowStepCmd.Flags().BoolVar(&flagFlowOptions, "options", false, "use options flow endpoint (for existing config entries)")
	configFlowInspectCmd.Flags().BoolVar(&flagFlowOptions, "options", false, "use options flow endpoint (for existing config entries)")
	configCmd.AddCommand(configEntriesCmd, configOptionsCmd, configFlowStartCmd, configFlowStepCmd, configFlowInspectCmd)
	rootCmd.AddCommand(configCmd)
}

// configEntry is the subset of a config entry we display.
type configEntry struct {
	EntryID string `json:"entry_id"`
	Domain  string `json:"domain"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Version int    `json:"version"`
}

func runConfigEntries(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}
	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetConfigEntries(ctx)
	if err != nil {
		return fmt.Errorf("fetching config entries: %w", err)
	}

	var entries []configEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parsing config entries: %w", err)
	}

	// Filter by domain if requested
	if flagConfigDomain != "" {
		var filtered []configEntry
		for _, e := range entries {
			if e.Domain == flagConfigDomain {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	if len(entries) == 0 {
		_, _ = fmt.Fprintln(w, "no config entries")
		return nil
	}

	tbl := &format.Table{
		Headers: []string{"entry_id", "domain", "title", "state", "version"},
		Rows:    make([][]string, len(entries)),
	}
	for i, e := range entries {
		tbl.Rows[i] = []string{
			e.EntryID,
			e.Domain,
			e.Title,
			e.State,
			strconv.Itoa(e.Version),
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func runConfigOptions(ctx context.Context, w io.Writer, entryID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}
	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.StartOptionsFlow(ctx, entryID)
	if err != nil {
		return fmt.Errorf("starting options flow: %w", err)
	}
	return renderFlowResult(w, data)
}

func runConfigFlowStart(ctx context.Context, w io.Writer, domain string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}
	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.StartConfigFlowOnce(ctx, domain)
	if err != nil {
		return fmt.Errorf("integration %q failed to load — check HA logs for import errors: %w", domain, err)
	}
	return renderFlowResult(w, data)
}

func runConfigFlowStep(ctx context.Context, w io.Writer, flowID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}
	client := haapi.New(cfg.URL, cfg.Token)

	var rawData json.RawMessage
	if jsonErr := json.Unmarshal([]byte(flagFlowData), &rawData); jsonErr != nil {
		return fmt.Errorf("invalid --data JSON: %w", jsonErr)
	}

	data, err := client.StepFlow(ctx, flowID, flagFlowOptions, rawData)
	if err != nil {
		return fmt.Errorf("stepping flow: %w", err)
	}
	return renderFlowResult(w, data)
}

func runConfigFlowInspect(ctx context.Context, w io.Writer, flowID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}
	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.InspectFlow(ctx, flowID, flagFlowOptions)
	if err != nil {
		return fmt.Errorf("inspecting flow: %w", err)
	}
	return renderFlowResult(w, data)
}

func renderFlowResult(w io.Writer, data []byte) error {
	if flagJSON {
		_, err := w.Write(data)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w)
		return err
	}

	flow, err := haapi.ParseFlowResult(data)
	if err != nil {
		return err
	}

	// Header info
	_, _ = fmt.Fprintf(w, "flow_id: %s\n", flow.FlowID)
	_, _ = fmt.Fprintf(w, "type:    %s\n", flow.Type)
	_, _ = fmt.Fprintf(w, "step:    %s\n", flow.StepID)
	if flow.Handler != "" {
		_, _ = fmt.Fprintf(w, "handler: %s\n", flow.Handler)
	}
	if flow.Title != "" {
		_, _ = fmt.Fprintf(w, "title:   %s\n", flow.Title)
	}

	// Errors
	if len(flow.Errors) > 0 {
		_, _ = fmt.Fprintf(w, "\nErrors:\n")
		for field, msg := range flow.Errors {
			_, _ = fmt.Fprintf(w, "  %s: %s\n", field, msg)
		}
	}

	// Schema fields table
	if len(flow.DataSchema) > 0 {
		_, _ = fmt.Fprintf(w, "\n")
		tbl := &format.Table{
			Headers: []string{"Field", "Type", "Required", "Default"},
		}
		for _, f := range flow.DataSchema {
			req := "no"
			if f.Required {
				req = "yes"
			}
			def := ""
			if f.Default != nil {
				def = fmt.Sprintf("%v", f.Default)
			}
			typ := f.Type
			if typ == "" {
				typ = "string"
			}
			tbl.Rows = append(tbl.Rows, []string{f.Name, typ, req, def})
		}
		return tbl.Render(w, format.RenderOpts{Full: true})
	}

	// Result payload for create_entry / abort
	if flow.Type == "create_entry" || flow.Type == "abort" {
		if len(flow.Result) > 0 {
			_, _ = fmt.Fprintf(w, "\nResult: %s\n", string(flow.Result))
		}
	}

	return nil
}
