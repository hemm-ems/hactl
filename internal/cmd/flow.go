package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var configCmd = &cobra.Command{
	Use:        "config",
	SuggestFor: []string{"integrations", "integration", "entries"},
	Short:      "Manage config entries and flows",
	Long:       "List config entries and start, step through, and inspect config entry options flows and config flows.",
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

var flagConfigProbeOptions bool

var configShowCmd = &cobra.Command{
	Use:   "show <entry_id>",
	Short: "Show a config entry's setup and current configuration",
	Long: "Show what an integration is set up as (domain, title, state, source, " +
		"options/reconfigure support, disabled/failure reason) and how it is " +
		"configured. The configuration is read from the integration's diagnostics " +
		"dump (secrets redacted by the integration). When the integration ships no " +
		"diagnostics platform, pass --probe-options-flow to read current values " +
		"from a transient options flow (started and immediately aborted); without " +
		"the flag no options flow is started. Read-only; requires an admin token.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var configOptionsCmd = &cobra.Command{
	Use:   "options <entry_id>",
	Short: "Start an options flow for a config entry (dry-run by default)",
	Long:  "Start an options flow for an existing config entry. Returns the flow ID and initial step schema. Dry-run by default: previews the intent without starting the flow; use --confirm to start.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigOptions(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var flagConfigConfirm bool

var configDeleteCmd = &cobra.Command{
	Use:   "delete <entry_id>",
	Short: "Delete a config entry (dry-run by default)",
	Long:  "Delete a config entry by ID. Dry-run by default — use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var configFlowStartCmd = &cobra.Command{
	Use:   "flow-start <domain>",
	Short: "Start a config flow for an integration (dry-run by default)",
	Long:  "Start a new config flow for a domain/integration. Returns the flow ID and initial step schema. Dry-run by default: previews the intent without starting the flow; use --confirm to start.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigFlowStart(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var flagFlowData string
var flagFlowOptions bool

var configFlowStepCmd = &cobra.Command{
	Use:   "flow-step <flow_id>",
	Short: "Submit data to advance a flow (dry-run by default)",
	Long: `Submit data to advance a config/options flow to the next step.

Use --options when stepping through an options flow (started via 'config options <entry_id>').
Without --options, the step is sent to the config flow endpoint
(/api/config/config_entries/flow/) instead of the options flow endpoint
(/api/config/config_entries/options/flow/).

Dry-run by default: previews the data that would be submitted (a step may complete
the flow and create a config entry); use --confirm to submit.`,
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

var flagConfigFileRaw bool

var configFilesCmd = &cobra.Command{
	Use:   "files",
	Short: "List config files",
	Long:  "List configuration.yaml and its !include'd files (via the companion).",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigFiles(cmd.Context(), cmd.OutOrStdout())
	},
}

var configFileCmd = &cobra.Command{
	Use:   "file <path>",
	Short: "Print a config file as YAML",
	Long:  "Print the contents of a config file. Use --raw to leave !include directives unresolved.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigFile(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var configBlockCmd = &cobra.Command{
	Use:   "block <path> <id>",
	Short: "Print a single keyed config block as YAML",
	Long:  "Print a single block (matched by id/unique_id/key) from a config file.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigBlock(cmd.Context(), cmd.OutOrStdout(), args[0], args[1])
	},
}

func init() {
	configEntriesCmd.Flags().StringVar(&flagConfigDomain, "domain", "", "filter entries by integration domain")
	configShowCmd.Flags().BoolVar(&flagConfigProbeOptions, "probe-options-flow", false,
		"when no diagnostics platform exists, probe a transient options flow to read current values (starts then immediately aborts a flow; requires the entry to support options)")
	configDeleteCmd.Flags().BoolVar(&flagConfigConfirm, "confirm", false, "actually delete (default is dry-run)")
	configOptionsCmd.Flags().BoolVar(&flagConfigConfirm, "confirm", false, "actually start the options flow (default is dry-run)")
	configFlowStartCmd.Flags().BoolVar(&flagConfigConfirm, "confirm", false, "actually start the config flow (default is dry-run)")
	configFlowStepCmd.Flags().StringVar(&flagFlowData, "data", "{}", "JSON data to submit to the flow step")
	configFlowStepCmd.Flags().BoolVar(&flagFlowOptions, "options", false, "use options flow endpoint (for existing config entries)")
	configFlowStepCmd.Flags().BoolVar(&flagConfigConfirm, "confirm", false, "actually submit the step (default is dry-run)")
	configFlowInspectCmd.Flags().BoolVar(&flagFlowOptions, "options", false, "use options flow endpoint (for existing config entries)")
	configFileCmd.Flags().BoolVar(&flagConfigFileRaw, "raw", false, "leave !include directives unresolved")
	configCmd.AddCommand(configEntriesCmd, configShowCmd, configDeleteCmd, configOptionsCmd, configFlowStartCmd, configFlowStepCmd, configFlowInspectCmd, configFilesCmd, configFileCmd, configBlockCmd)
	rootCmd.AddCommand(configCmd)
}

// configEntry is the subset of a config entry we display. Every field must
// correspond to a key HA actually emits on /api/config/config_entries/entry —
// a field HA never sends serialises a fabricated zero value into --json output
// (there is no `version` key on that endpoint, hence none here).
type configEntry struct {
	EntryID            string `json:"entry_id"`
	Domain             string `json:"domain"`
	Title              string `json:"title"`
	State              string `json:"state"`
	Source             string `json:"source"`
	SupportsOptions    bool   `json:"supports_options"`
	SupportsReconfig   bool   `json:"supports_reconfigure"`
	DisabledBy         string `json:"disabled_by"`
	Reason             string `json:"reason"`
	ReasonTranslateKey string `json:"error_reason_translation_key"`
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
		return emitEmptyList(w, "no config entries")
	}

	tbl := &format.Table{
		Headers: []string{"entry_id", "domain", "title", "state", "source", "options", "disabled_by"},
		Rows:    make([][]string, len(entries)),
	}
	for i, e := range entries {
		tbl.Rows[i] = []string{
			e.EntryID,
			e.Domain,
			e.Title,
			e.State,
			e.Source,
			yesNo(e.SupportsOptions),
			dashIfEmpty(e.DisabledBy),
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

// configShowResult is the structured form of `config show`, used verbatim for
// --json output.
type configShowResult struct {
	Entry        *configEntry    `json:"entry"`
	ConfigSource string          `json:"config_source"`     // "diagnostics" | "options_flow" | "unavailable"
	Config       json.RawMessage `json:"config,omitempty"`  // diagnostics: integration-redacted dump
	Options      map[string]any  `json:"options,omitempty"` // options_flow: current field values
	Warning      string          `json:"warning,omitempty"` // side-effect the probe must not hide
	Note         string          `json:"note,omitempty"`
}

func runConfigShow(ctx context.Context, w io.Writer, entryID string) error {
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
	entry, ok := findConfigEntry(entries, entryID)
	if !ok {
		return fmt.Errorf("unknown config entry %q (list them with 'hactl config entries')", entryID)
	}

	result := &configShowResult{Entry: entry, ConfigSource: "unavailable"}

	// Primary source: the integration's diagnostics dump (secrets redacted by
	// the integration).
	diag, diagErr := client.GetConfigEntryDiagnostics(ctx, entryID)
	if diagErr == nil {
		result.ConfigSource = "diagnostics"
		result.Config = diagnosticsConfigData(diag)
	} else {
		reason := configShowDiagReason(diagErr)
		// The options-flow fallback POSTs to the same endpoint the gated
		// `config options` write command uses, so from this read-classified
		// command it runs only behind an explicit --probe-options-flow, and
		// only when it is both safe and meaningful: the diagnostics platform is
		// genuinely absent (a TYPED 404 — not a 401/403/5xx, and not an error
		// whose body merely contains "404") and the entry advertises options.
		status, _ := haapi.HTTPStatus(diagErr)
		canProbe := status == http.StatusNotFound && entry.SupportsOptions
		switch {
		case canProbe && flagConfigProbeOptions:
			opts, warning, note := readOptionsFlowValues(ctx, client, entryID)
			if opts != nil {
				result.ConfigSource = "options_flow"
				result.Options = opts
			}
			result.Warning = warning
			result.Note = joinNote(reason, note)
		case canProbe:
			result.Note = reason + "; pass --probe-options-flow to read current values from a transient options flow"
		default:
			result.Note = reason
		}
	}

	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	return renderConfigShow(w, result)
}

// findConfigEntry returns the entry whose entry_id matches (case-sensitive).
func findConfigEntry(entries []configEntry, entryID string) (*configEntry, bool) {
	for i := range entries {
		if entries[i].EntryID == entryID {
			return &entries[i], true
		}
	}
	return nil, false
}

// diagnosticsConfigData extracts the integration's own diagnostics payload (the
// top-level "data" key of the download-diagnostics envelope) — the part that
// describes how the entry is configured. Falls back to the whole dump if the
// envelope shape is unexpected.
func diagnosticsConfigData(raw []byte) json.RawMessage {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Data) > 0 {
		return envelope.Data
	}
	return json.RawMessage(raw)
}

// optionsFlowAbortTimeout bounds the best-effort cleanup DELETE so a detached
// abort cannot hang the command on a slow HA.
const optionsFlowAbortTimeout = 5 * time.Second

// readOptionsFlowValues probes a transient options flow to read the current
// value of each schema field, then aborts the flow so nothing dangles. It runs
// only behind --probe-options-flow (see runConfigShow). Returns:
//   - values: the current values ({} when the form carries none, nil when there
//     is no readable form);
//   - warning: a prominent, non-benign side-effect the caller must surface;
//   - note: a human explanation of the outcome.
func readOptionsFlowValues(ctx context.Context, client *haapi.Client, entryID string) (values map[string]any, warning, note string) {
	// Single-shot POST: retrying would risk starting several flows while only
	// one gets aborted below.
	raw, err := client.StartOptionsFlowOnce(ctx, entryID)
	if err != nil {
		return nil, "", "options flow unavailable: " + err.Error()
	}

	// Always attempt cleanup whenever a flow id is present — independent of
	// whether the full parse below succeeds, since a parse failure does not
	// prove no flow was created. Detach from the caller's context
	// (WithoutCancel) with a short timeout so the abort still runs even if the
	// caller's context was cancelled.
	if flowID := flowIDOf(raw); flowID != "" {
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), optionsFlowAbortTimeout)
		if abortErr := client.AbortOptionsFlow(abortCtx, flowID); abortErr != nil {
			slog.Debug("aborting options flow failed", "flow_id", flowID, "error", abortErr)
		}
		cancel()
	}

	flow, parseErr := haapi.ParseFlowResult(raw)
	if parseErr != nil {
		return nil, "", "could not parse options flow: " + parseErr.Error()
	}

	// A create_entry means HA accepted a submission and finished the flow: the
	// entry's options were PERSISTED and the config entry reloaded. That is a
	// mutation, not a benign read — never hide it behind a note.
	if flow.Type == "create_entry" {
		return nil, "options-flow probe PERSISTED options and reloaded the config entry: HA finished the flow " +
			"with create_entry instead of returning a form, so the entry may have been rewritten with its current values", ""
	}

	if flow.Type != "form" {
		return nil, "", "integration exposes no readable options form (flow type: " + flow.Type + ")"
	}
	vals := optionsFlowCurrentValues(raw)
	if len(vals) == 0 {
		return map[string]any{}, "", "options form has no pre-filled current values"
	}
	return vals, "", ""
}

// flowIDOf extracts just the flow_id from a raw flow response, independent of
// the fuller ParseFlowResult, so cleanup can proceed even when the rest of the
// response is unparseable.
func flowIDOf(rawFlow []byte) string {
	var v struct {
		FlowID string `json:"flow_id"`
	}
	_ = json.Unmarshal(rawFlow, &v)
	return v.FlowID
}

// optionsFlowCurrentValues extracts each schema field's current value from a
// raw options-flow response. HA seeds an options form with the entry's current
// values as either a field "default" or a "description.suggested_value".
func optionsFlowCurrentValues(rawFlow []byte) map[string]any {
	var raw struct {
		DataSchema []json.RawMessage `json:"data_schema"`
	}
	if err := json.Unmarshal(rawFlow, &raw); err != nil {
		return nil
	}
	values := make(map[string]any)
	for _, fieldRaw := range raw.DataSchema {
		var field struct {
			Name        string `json:"name"`
			Default     any    `json:"default"`
			Description struct {
				SuggestedValue any `json:"suggested_value"`
			} `json:"description"`
		}
		if err := json.Unmarshal(fieldRaw, &field); err != nil || field.Name == "" {
			continue
		}
		switch {
		case field.Default != nil:
			values[field.Name] = field.Default
		case field.Description.SuggestedValue != nil:
			values[field.Name] = field.Description.SuggestedValue
		}
	}
	return values
}

// configShowDiagReason explains why the diagnostics dump was unavailable,
// branching on the TYPED HTTP status (never the message text, which can embed up
// to 500 bytes of response body).
func configShowDiagReason(diagErr error) string {
	switch status, _ := haapi.HTTPStatus(diagErr); status {
	case http.StatusNotFound:
		return "integration ships no diagnostics platform"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "diagnostics requires an admin token"
	default:
		return "diagnostics unavailable: " + diagErr.Error()
	}
}

// joinNote appends a sub-note to a reason with "; ", omitting an empty sub-note.
func joinNote(reason, note string) string {
	if note == "" {
		return reason
	}
	return reason + "; " + note
}

func renderConfigShow(w io.Writer, r *configShowResult) error {
	// A probe side-effect (e.g. options persisted by a create_entry) must be
	// impossible to miss, so lead with it.
	if r.Warning != "" {
		_, _ = fmt.Fprintf(w, "WARNING: %s\n\n", r.Warning)
	}
	e := r.Entry
	_, _ = fmt.Fprintf(w, "entry_id:     %s\n", e.EntryID)
	_, _ = fmt.Fprintf(w, "domain:       %s\n", e.Domain)
	_, _ = fmt.Fprintf(w, "title:        %s\n", e.Title)
	_, _ = fmt.Fprintf(w, "state:        %s\n", e.State)
	_, _ = fmt.Fprintf(w, "source:       %s\n", e.Source)
	_, _ = fmt.Fprintf(w, "options:      %s\n", yesNo(e.SupportsOptions))
	_, _ = fmt.Fprintf(w, "reconfigure:  %s\n", yesNo(e.SupportsReconfig))
	if e.DisabledBy != "" {
		_, _ = fmt.Fprintf(w, "disabled_by:  %s\n", e.DisabledBy)
	}
	if reason := e.Reason; reason != "" {
		_, _ = fmt.Fprintf(w, "reason:       %s\n", reason)
	} else if e.ReasonTranslateKey != "" {
		_, _ = fmt.Fprintf(w, "reason:       %s\n", e.ReasonTranslateKey)
	}

	_, _ = fmt.Fprintf(w, "\nconfiguration (source: %s):\n", r.ConfigSource)
	switch r.ConfigSource {
	case "diagnostics":
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, r.Config, "  ", "  "); err == nil {
			_, _ = fmt.Fprintf(w, "  %s\n", pretty.String())
		} else {
			_, _ = fmt.Fprintf(w, "  %s\n", string(r.Config))
		}
	case "options_flow":
		if len(r.Options) == 0 {
			_, _ = fmt.Fprintln(w, "  (no pre-filled current values in the options form)")
		}
		for _, k := range sortedKeys(r.Options) {
			_, _ = fmt.Fprintf(w, "  %s: %v\n", k, r.Options[k])
		}
	default:
		_, _ = fmt.Fprintln(w, "  (not available)")
	}
	if r.Note != "" {
		_, _ = fmt.Fprintf(w, "\nnote: %s\n", r.Note)
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func runConfigDelete(ctx context.Context, w io.Writer, entryID string) error {
	if !flagConfigConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete config entry")
		_, _ = fmt.Fprintf(w, "  entry_id: %s\n", entryID)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}
	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.DeleteConfigEntry(ctx, entryID)
	if err != nil {
		return fmt.Errorf("deleting config entry: %w", err)
	}

	if flagJSON {
		_, err = w.Write(data)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w)
		return err
	}

	_, _ = fmt.Fprintf(w, "deleted config entry %q\n", entryID)
	return nil
}

func runConfigOptions(ctx context.Context, w io.Writer, entryID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	if !flagConfigConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would start an options flow for config entry")
		_, _ = fmt.Fprintf(w, "  entry_id: %s\n", entryID)
		_, _ = fmt.Fprintln(w, "use --confirm to start")
		return nil
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

	if !flagConfigConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would start a config flow for integration")
		_, _ = fmt.Fprintf(w, "  domain: %s\n", domain)
		_, _ = fmt.Fprintln(w, "use --confirm to start")
		return nil
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

	if !flagConfigConfirm {
		endpoint := "config flow"
		if flagFlowOptions {
			endpoint = "options flow"
		}
		_, _ = fmt.Fprintln(w, "dry-run: would submit data to advance the flow")
		_, _ = fmt.Fprintf(w, "  flow_id:  %s\n", flowID)
		_, _ = fmt.Fprintf(w, "  endpoint: %s\n", endpoint)
		_, _ = fmt.Fprintf(w, "  data:     %s\n", flagFlowData)
		_, _ = fmt.Fprintln(w, "use --confirm to submit (a step may complete the flow and create a config entry)")
		return nil
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

func runConfigFiles(ctx context.Context, w io.Writer) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	resp, err := cc.ListConfigFiles(ctx)
	if err != nil {
		return fmt.Errorf("listing config files: %w", err)
	}
	if len(resp.Files) == 0 {
		return emitEmptyList(w, "no config files")
	}
	tbl := &format.Table{
		Headers: []string{"path"},
		Rows:    make([][]string, len(resp.Files)),
	}
	for i, f := range resp.Files {
		tbl.Rows[i] = []string{f}
	}
	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

// runConfigFile prints a config file's contents verbatim. With --raw the
// companion leaves !include directives unresolved; otherwise they are inlined.
func runConfigFile(ctx context.Context, w io.Writer, path string) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	var content string
	if flagConfigFileRaw {
		resp, readErr := cc.ReadConfigFileRaw(ctx, path)
		if readErr != nil {
			return fmt.Errorf("reading config file: %w", readErr)
		}
		content = resp.Content
	} else {
		resp, readErr := cc.ReadConfigFile(ctx, path)
		if readErr != nil {
			return fmt.Errorf("reading config file: %w", readErr)
		}
		content = resp.Content
	}
	_, _ = fmt.Fprint(w, content)
	return nil
}

// runConfigBlock prints a single keyed block from a config file as YAML.
func runConfigBlock(ctx context.Context, w io.Writer, path, id string) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	resp, err := cc.ReadConfigBlock(ctx, path, id)
	if err != nil {
		return fmt.Errorf("reading config block: %w", err)
	}
	_, _ = fmt.Fprint(w, resp.Content)
	return nil
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
		var sections []haapi.SchemaField
		for _, f := range flow.DataSchema {
			appendSchemaRows(tbl, f, "")
			if len(f.Schema) > 0 {
				sections = append(sections, f)
			}
		}
		if err := tbl.Render(w, format.RenderOpts{Full: true}); err != nil {
			return err
		}
		// Hint how to submit expandable sections, which must be nested under
		// their section name in --data (HA rejects flat keys with a 400).
		for _, s := range sections {
			parts := make([]string, len(s.Schema))
			for i, sub := range s.Schema {
				typ := sub.Type
				if typ == "" {
					typ = "string"
				}
				parts[i] = fmt.Sprintf("%q: <%s>", sub.Name, typ)
			}
			_, _ = fmt.Fprintf(w, "\n%q is an expandable section — nest its fields in --data:\n", s.Name)
			_, _ = fmt.Fprintf(w, "  {%q: {%s}}\n", s.Name, strings.Join(parts, ", "))
		}
		return nil
	}

	// Result payload for create_entry / abort
	if flow.Type == "create_entry" || flow.Type == "abort" {
		if len(flow.Result) > 0 {
			_, _ = fmt.Fprintf(w, "\nResult: %s\n", string(flow.Result))
		}
	}

	return nil
}

// appendSchemaRows adds a schema field (and, for expandable sections, its
// nested sub-fields) to the table. Sub-fields are shown with a dotted path
// (e.g. "advanced.framerate") so the nesting is visible at a glance.
func appendSchemaRows(tbl *format.Table, f haapi.SchemaField, prefix string) {
	name := f.Name
	if prefix != "" {
		name = prefix + "." + f.Name
	}
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
	tbl.Rows = append(tbl.Rows, []string{name, typ, req, def})
	for _, sub := range f.Schema {
		appendSchemaRows(tbl, sub, name)
	}
}
