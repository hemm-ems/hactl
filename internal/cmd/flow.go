package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var configCmd = family(&cobra.Command{
	Use:        "config",
	SuggestFor: []string{"integrations", "integration", "entries"},
	Short:      "Manage config entries and flows",
	Long:       "List config entries and start, step through, and inspect config entry options flows and config flows.",
})

var flagConfigDomain string

var configEntriesCmd = &cobra.Command{
	Use:   "entries",
	Args:  takesNone(),
	Short: "List config entries",
	Long:  "List all config entries. Use --domain to filter by integration domain.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigEntries(cmd, cmd.OutOrStdout())
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
	Args: takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var configOptionsCmd = &cobra.Command{
	Use:   "options <entry_id>",
	Short: "Start an options flow for a config entry (dry-run by default)",
	Long:  "Start an options flow for an existing config entry. Returns the flow ID and initial step schema. Dry-run by default: previews the intent without starting the flow; use --confirm to start.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigOptions(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var flagConfigConfirm bool

var configDeleteCmd = &cobra.Command{
	Use:   "delete <entry_id>",
	Short: "Delete a config entry (dry-run by default)",
	Long:  "Delete a config entry by ID. Dry-run by default — use --confirm to apply.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var configFlowStartCmd = &cobra.Command{
	Use:   "flow-start <domain>",
	Short: "Start a config flow for an integration (dry-run by default)",
	Long:  "Start a new config flow for a domain/integration. Returns the flow ID and initial step schema. Dry-run by default: previews the intent without starting the flow; use --confirm to start.",
	Args:  takes(1),
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
	Args: takes(1),
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
	Args: takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigFlowInspect(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var flagConfigFileRaw bool

var configFilesCmd = &cobra.Command{
	Use:   "files",
	Args:  takesNone(),
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
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigFile(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var configBlockCmd = &cobra.Command{
	Use:   "block <path> <id>",
	Short: "Print a single keyed config block as YAML",
	Long: "Print a single block from a config file: matched by 'id:' or 'alias:' on the direct items " +
		"of a top-level list (automations.yaml), or by top-level key (scripts.yaml). template.yaml " +
		"blocks carry neither — read those with 'tpl cat <unique_id>'.",
	Args:  takes(2),
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

// filterConfigEntriesByDomain keeps the entries of exactly one integration
// domain, matched without regard to case (D-2).
//
// It compared with `==` and lived inline in runConfigEntries, where nothing
// could probe it: `config entries --domain MQTT` answered "no config entries"
// on an instance running mqtt. Extracted so the case gate drives the filter the
// command itself calls rather than a reimplementation of it.
func filterConfigEntriesByDomain(entries []configEntry, domain string) []configEntry {
	out := make([]configEntry, 0, len(entries))
	for _, e := range entries {
		if strings.EqualFold(e.Domain, domain) {
			out = append(out, e)
		}
	}
	return out
}

func runConfigEntries(cmd *cobra.Command, w io.Writer) error {
	ctx := cmd.Context()
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
	if err := degeneracy.Check("/api/config/config_entries/entry", &entries); err != nil {
		return err
	}

	// Before the filter runs — see emptyListing.
	total := len(entries)

	if flagConfigDomain != "" {
		entries = filterConfigEntriesByDomain(entries, flagConfigDomain)
	}

	if len(entries) == 0 {
		return emptyListing(cmd, w, "config entries", total)
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
		// Both cells above are renderings for a person; the machine gets what
		// HA sent (finding #22). `config show --json` already emits the raw
		// field for disabled_by, and these two were the pair that disagreed.
		tbl.SetMachine(i, "options", e.SupportsOptions)
		tbl.SetMachine(i, "disabled_by", e.DisabledBy)
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
	if err := degeneracy.Check("/api/config/config_entries/entry", &entries); err != nil {
		return err
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

// resolveConfigEntry fetches HA's config entries and returns the one with
// entryID, or an error naming the miss. Used by the write commands so their
// dry runs fail exactly where the confirmed run would.
func resolveConfigEntry(ctx context.Context, client *haapi.Client, entryID string) (*configEntry, error) {
	data, err := client.GetConfigEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching config entries: %w", err)
	}
	var entries []configEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing config entries: %w", err)
	}
	if err := degeneracy.Check("/api/config/config_entries/entry", &entries); err != nil {
		return nil, err
	}
	entry, ok := findConfigEntry(entries, entryID)
	if !ok {
		return nil, fmt.Errorf("unknown config entry %q (list them with 'hactl config entries')", entryID)
	}
	return entry, nil
}

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

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func runConfigDelete(ctx context.Context, w io.Writer, entryID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}
	client := haapi.New(cfg.URL, cfg.Token)

	// Resolve before planning: deleting a config entry removes every entity
	// it owns, so a preview naming an entry HA does not have is a plan for a
	// removal that cannot be reviewed.
	entry, err := resolveConfigEntry(ctx, client, entryID)
	if err != nil {
		return err
	}

	if !flagConfigConfirm {
		return dryRun("delete config entry").
			with("entry_id", entry.EntryID).
			with("domain", entry.Domain).
			with("title", entry.Title).
			render(w)
	}

	data, err := client.DeleteConfigEntry(ctx, entryID)
	if err != nil {
		return fmt.Errorf("deleting config entry: %w", err)
	}

	// HA's own answer is carried, not echoed. Echoing it made this the one
	// write whose --json document had no `dry_run`, no `action` and no `ok`:
	// valid JSON that a caller still could not tell from a preview, from an
	// error, or from another command's output. `require_restart` is the one
	// field HA sends and it is the one thing the caller has to act on, so it
	// keeps its name inside the result.
	res := done("delete config entry").
		with("entry_id", entry.EntryID).
		with("domain", entry.Domain).
		with("title", entry.Title).
		text("deleted config entry %q", entryID)
	var haAnswer struct {
		RequireRestart bool `json:"require_restart"`
	}
	if json.Unmarshal(data, &haAnswer) == nil {
		res = res.with("require_restart", haAnswer.RequireRestart)
	}
	return res.render(w)
}

func runConfigOptions(ctx context.Context, w io.Writer, entryID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)

	// Resolve before planning. `supports_options` is part of it: HA rejects an
	// options flow for an entry that has none, and the preview should say so
	// rather than promise a flow that cannot start.
	entry, err := resolveConfigEntry(ctx, client, entryID)
	if err != nil {
		return err
	}
	if !entry.SupportsOptions {
		return fmt.Errorf("config entry %q (%s) has no options flow", entryID, entry.Domain)
	}

	if !flagConfigConfirm {
		return dryRun("start an options flow for config entry").
			with("entry_id", entry.EntryID).
			with("domain", entry.Domain).
			with("title", entry.Title).
			withHint("use --confirm to start").
			render(w)
	}

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

	// Resolve before planning: a domain that has no config flow is the whole
	// failure mode here, and the preview must refuse exactly what --confirm
	// would. HA's flow-handler list is the authority — it reports every
	// installable integration that exposes a config flow, whether or not it is
	// currently loaded, so a not-yet-configured integration (the very thing you
	// start a flow for) previews instead of being wrongly rejected.
	//
	// manifest/list, used before, reports only *loaded* integrations, so it
	// rejected every unconfigured domain as "no loaded integration" while a
	// confirmed StartConfigFlow lazily loaded it and succeeded — the dry run
	// failed exactly where the confirmed run worked, the inverse of the H-2
	// contract, and it broke the command's whole reason for existing.
	if !flagConfigConfirm {
		if handlerErr := ensureConfigFlowHandler(ctx, client, domain); handlerErr != nil {
			return handlerErr
		}
		return dryRun("start a config flow for integration").
			with("domain", domain).
			withHint("use --confirm to start").
			render(w)
	}
	data, err := client.StartConfigFlowOnce(ctx, domain)
	if err != nil {
		return fmt.Errorf("integration %q failed to load — check HA logs for import errors: %w", domain, err)
	}
	return renderFlowResult(w, data)
}

// ensureConfigFlowHandler fails when the domain exposes no config flow, so the
// dry run refuses exactly what a confirmed StartConfigFlow would 404 on
// ("Invalid handler specified"). HA's flow_handlers list is the authority: it
// includes installable-but-unloaded integrations, which manifest/list omits.
func ensureConfigFlowHandler(ctx context.Context, client *haapi.Client, domain string) error {
	handlers, err := client.ConfigFlowHandlers(ctx)
	if err != nil {
		return fmt.Errorf("fetching config flow handlers: %w", err)
	}
	if slices.Contains(handlers, domain) {
		return nil
	}
	return fmt.Errorf("no config flow for domain %q "+
		"(the domain must be an installed integration that provides a config flow)", domain)
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
		// Resolve before planning: a flow id is transient, and one that has
		// expired or was never started reads exactly like a live one in a
		// preview that never asks HA about it.
		if _, inspectErr := client.InspectFlow(ctx, flowID, flagFlowOptions); inspectErr != nil {
			return flowLookupError(flowID, flagFlowOptions, inspectErr)
		}
		return dryRun("submit data to advance the flow").
			with("flow_id", flowID).
			with("endpoint", endpoint).
			with("data", flagFlowData).
			withHint("use --confirm to submit (a step may complete the flow and create a config entry)").
			render(w)
	}

	data, err := client.StepFlow(ctx, flowID, flagFlowOptions, rawData)
	if err != nil {
		return flowLookupError(flowID, flagFlowOptions, fmt.Errorf("stepping flow: %w", err))
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
		return flowLookupError(flowID, flagFlowOptions, fmt.Errorf("inspecting flow: %w", err))
	}
	return renderFlowResult(w, data)
}

// flowLookupError explains a flow id Home Assistant does not know.
//
// It exists because the explanation was a sentence inside ONE branch of ONE
// command. `config flow-step <bad-id>` without --confirm said "flows expire;
// start one with 'config flow-start' or 'config options'"; `config flow-inspect
// <the same bad id>` said `inspecting flow: GET …: 404 Not Found:
// {"message":"Invalid flow specified"}`, and `flow-step --confirm` said
// `stepping flow: …` — three commands, one condition, and the help attached to
// whichever one somebody was looking at when they wrote it (#84).
//
// The three causes HA answers identically are named, because a 404 alone does
// not distinguish them and the reader's next action differs for each: the id
// never existed, the flow expired or was aborted, or the id belongs to the
// other endpoint and --options is missing or extra. The last is the one a
// caller cannot guess, so it names the flag and the direction to move it.
//
// Anything other than a 404 passes through untouched: a 500 from HA is not a
// caller mistake and telling them to check their flag would be a guess.
func flowLookupError(flowID string, options bool, err error) error {
	if status, ok := haapi.HTTPStatus(err); !ok || status != http.StatusNotFound {
		return err
	}
	endpoint, other, flag := "config flow", "options flow", "add --options"
	if options {
		endpoint, other, flag = "options flow", "config flow", "drop --options"
	}
	return fmt.Errorf("no in-progress %s with id %q — it never existed, it expired or was aborted, "+
		"or it belongs to the %s (%s): %w", endpoint, flowID, other, flag, err)
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
	markStructuredOutput()
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
	markStructuredOutput()
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	resp, err := cc.ReadConfigBlock(ctx, path, id)
	if err != nil {
		if redirect := templateRedirect(ctx, cc, path, id, err); redirect != nil {
			return redirect
		}
		return fmt.Errorf("reading config block: %w", err)
	}
	_, _ = fmt.Fprint(w, resp.Content)
	return nil
}

// templateRedirect turns a block-not-found into the referral `config block`'s
// own --help already promises, or returns nil when there is nothing to refer to.
//
// The help says: "template.yaml blocks carry neither [id: nor alias:] — read
// those with 'tpl cat <unique_id>'". Nothing implemented that sentence, so
// `config block template.yaml posclock_jan` — a real unique_id in a real file —
// answered `Block not found: posclock_jan`, word for word what a typo gets
// (finding #24). The command that knows the id is addressable said nothing
// about it.
//
// Whether the id IS addressable is asked, not assumed from the filename: the
// companion is the only thing that knows which unique_ids exist, and a rule
// keyed on `path == "template.yaml"` would miss a template split into its own
// file and would fire on a typo inside template.yaml. A 404 from the template
// route means no — the caller keeps the original error, which is the true one.
func templateRedirect(ctx context.Context, cc *companion.Client, path, id string, blockErr error) error {
	if status, ok := haapi.HTTPStatus(blockErr); !ok || status != http.StatusNotFound {
		return nil
	}
	if _, err := cc.GetTemplate(ctx, id); err != nil {
		// Deliberately swallowed: this call is a QUESTION ("is that id a
		// template?"), not work the caller asked for. Its failure — a 404, an
		// unreachable companion, anything — is the answer "no", and the error
		// the caller gets is the block read's own, which is the true one.
		return nil //nolint:nilerr // the probe's failure is data, not this command's failure
	}
	return fmt.Errorf("no block with id or alias %q in %s, but a template entity has that unique_id — "+
		"read it with 'hactl tpl cat %s'", id, path, id)
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

	// Errors, in field order. HA reports one message per failed field, and a
	// step can fail several at once — ranging the map directly rendered them
	// in an order that changed between two runs of the same command (H-16).
	if len(flow.Errors) > 0 {
		_, _ = fmt.Fprintf(w, "\nErrors:\n")
		for _, field := range sortedKeys(flow.Errors) {
			_, _ = fmt.Fprintf(w, "  %s: %s\n", field, flow.Errors[field])
		}
	}

	// Menu step: choices instead of fields. Without this branch a menu step
	// rendered the five header lines and nothing else, while the choices the
	// caller must submit were silently dropped at decode (#112).
	if flow.Type == "menu" {
		renderMenuOptions(w, flow)
		return nil
	}

	// Schema fields table
	if len(flow.DataSchema) > 0 {
		return renderSchemaTable(w, flow)
	}

	// Result payload for create_entry / abort
	if flow.Type == "create_entry" || flow.Type == "abort" {
		if len(flow.Result) > 0 {
			_, _ = fmt.Fprintf(w, "\nResult: %s\n", string(flow.Result))
		}
	}

	return nil
}

// renderMenuOptions lists a menu step's choices plus the flow-step submit
// hint. Order is the parse's: wire order for the list form, sorted ids for
// the map form (H-16, canonicalized in parseMenuOptions). A menu whose
// options did not decode says so — silence here is the exact #112 shape.
func renderMenuOptions(w io.Writer, flow *haapi.FlowResult) {
	if len(flow.MenuOptions) == 0 {
		_, _ = fmt.Fprintf(w, "\nmenu step, but no options decoded from the flow response — inspect the raw form with --json\n")
		return
	}
	_, _ = fmt.Fprintf(w, "\nMenu options:\n")
	for _, opt := range flow.MenuOptions {
		if opt.Label != "" && opt.Label != opt.ID {
			_, _ = fmt.Fprintf(w, "  %s  (%s)\n", opt.ID, opt.Label)
		} else {
			_, _ = fmt.Fprintf(w, "  %s\n", opt.ID)
		}
	}
	_, _ = fmt.Fprintf(w, "\nchoose with: config flow-step %s --data '{\"next_step_id\": \"<option>\"}' (--options for an options flow)\n", flow.FlowID)
}

// renderSchemaTable prints a form step's fields, each select's submittable
// values, and the expandable-section nesting hints.
func renderSchemaTable(w io.Writer, flow *haapi.FlowResult) error {
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
	// Full is deliberately hardcoded, not flagFull: honouring the flag would
	// truncate schema tables by default and hide required fields behind a
	// flag nobody knows to pass (#112 kept this on purpose).
	if err := tbl.Render(w, format.RenderOpts{Full: true}); err != nil {
		return err
	}
	// A select's submittable values, after the table like the expandable
	// hint below — they used to be dropped at decode, leaving a next_step_id
	// select with no visible choices (#112).
	for _, f := range flow.DataSchema {
		printSelectOptions(w, f, "")
	}
	// Hint how to submit expandable sections, which must be nested under
	// their section name in --data (HA rejects flat keys with a 400).
	for _, s := range sections {
		parts := make([]string, len(s.Schema))
		for i, sub := range s.Schema {
			parts[i] = fmt.Sprintf("%q: <%s>", sub.Name, schemaFieldType(sub))
		}
		_, _ = fmt.Fprintf(w, "\n%q is an expandable section — nest its fields in --data:\n", s.Name)
		_, _ = fmt.Fprintf(w, "  {%q: {%s}}\n", s.Name, strings.Join(parts, ", "))
	}
	return nil
}

// printSelectOptions prints one line per field that carries options, with the
// dotted path appendSchemaRows uses, recursing into expandable sections.
func printSelectOptions(w io.Writer, f haapi.SchemaField, prefix string) {
	name := f.Name
	if prefix != "" {
		name = prefix + "." + f.Name
	}
	if len(f.Options) > 0 {
		_, _ = fmt.Fprintf(w, "\n%q options: %s\n", name, strings.Join(f.Options, ", "))
	}
	for _, sub := range f.Schema {
		printSelectOptions(w, sub, name)
	}
}

// schemaFieldType is the word the Type column carries.
//
// A modern HA schema types its fields with a SELECTOR and leaves `type` empty,
// so the fallback "string" was the answer for every one of them: a number, a
// 28-value enum, an entity picker and a device picker all rendered identically
// while --json carried the difference (#82). The selector kind is the type when
// there is one; `type` is preferred when both are present, because that is the
// field's own declaration; "string" remains the answer when HA said neither,
// which is what an unadorned voluptuous string field is.
func schemaFieldType(f haapi.SchemaField) string {
	switch {
	case f.Type != "":
		return f.Type
	case f.Selector != "":
		return f.Selector
	default:
		return "string"
	}
}

// schemaFieldDefault is the value the Default column carries.
//
// HA sends two different things and they mean different things: `default` is
// what the field falls back to, and `description.suggested_value` is what HA
// PROPOSES — for an options flow, the entry's current configuration. The second
// was decoded nowhere, so `flow-inspect --options` on a template helper showed
// an empty Default beside a `state` field whose current value `{{ true }}` was
// in the same response (#83). The suggestion wins where both exist: it is the
// more specific statement, and it is the one a caller re-submitting a form
// wants to see.
func schemaFieldDefault(f haapi.SchemaField) string {
	if f.Suggested != nil {
		return fmt.Sprintf("%v", f.Suggested)
	}
	if f.Default != nil {
		return fmt.Sprintf("%v", f.Default)
	}
	return ""
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
	tbl.Rows = append(tbl.Rows, []string{name, schemaFieldType(f), req, schemaFieldDefault(f)})
	for _, sub := range f.Schema {
		appendSchemaRows(tbl, sub, name)
	}
}
