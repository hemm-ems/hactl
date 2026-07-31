package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/jsonwalk"
)

var flagDashView string
var flagDashRaw bool
var flagDashYAML bool
var flagDashFile string
var flagDashConfirm bool
var flagDashTitle string
var flagDashURLPath string
var flagDashIcon string
var flagDashSidebar bool
var flagDashAdmin bool

var dashCmd = family(&cobra.Command{
	Use:        "dash",
	SuggestFor: []string{"dashboard", "dashboards", "lovelace"},
	Short:      "Manage Lovelace dashboards",
	Long:       "List, inspect, create, and modify Home Assistant Lovelace dashboards.",
})

var dashLsCmd = &cobra.Command{
	Use:   "ls",
	Args:  takesNone(),
	Short: "List dashboards",
	Long:  "Show all Lovelace dashboards registered in Home Assistant.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var dashShowCmd = &cobra.Command{
	Use:   "show [url_path]",
	Short: "Show dashboard config",
	Long:  "Display dashboard views summary, or the full config as raw JSON (--raw/--json) or YAML (--yaml). Omit url_path for the default dashboard.",
	Args:  takesAtMost(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		urlPath := ""
		if len(args) > 0 {
			urlPath = args[0]
		}
		// Two flags naming one format is a question with no answer, and
		// answering it silently is what `dash show x --raw --yaml` did
		// (finding #60). It gates here rather than inside runDashShow because
		// only cobra knows which flags the caller actually passed.
		if err := checkOutputFormatFlags(cmd); err != nil {
			return err
		}
		return runDashShow(cmd.Context(), cmd.OutOrStdout(), urlPath)
	},
}

var dashSaveCmd = &cobra.Command{
	Use:   "save [url_path]",
	Short: "Save dashboard config (dry-run by default)",
	Long:  "Write a full dashboard config from JSON file or stdin. Use --confirm to apply.",
	Args:  takesAtMost(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		urlPath := ""
		if len(args) > 0 {
			urlPath = args[0]
		}
		return runDashSave(cmd.Context(), cmd.OutOrStdout(), urlPath)
	},
}

var dashCreateCmd = &cobra.Command{
	Use:   "create",
	Args:  takesNone(),
	Short: "Create a new dashboard (dry-run by default)",
	Long:  "Create a new storage-mode Lovelace dashboard. Use --confirm to apply.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashCreate(cmd.Context(), cmd.OutOrStdout())
	},
}

var dashDeleteCmd = &cobra.Command{
	Use:   "delete <url_path>",
	Short: "Delete a dashboard (dry-run by default)",
	Long:  "Delete a Lovelace dashboard by url_path. Use --confirm to apply.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var dashResourcesCmd = &cobra.Command{
	Use:   "resources",
	Args:  takesNone(),
	Short: "List registered resources",
	Long:  "Show custom card/CSS resources registered in Lovelace.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashResources(cmd.Context(), cmd.OutOrStdout())
	},
}

var dashGrepCmd = &cobra.Command{
	Use:   "grep <value>",
	Short: "Find where a value (typically an entity_id) is used across dashboards",
	Long: "Scan every dashboard (default + storage) for string values equal to <value> and report the " +
		"dashboard and path of each hit. The match is whole-value and position-independent: a card's " +
		"entity matches, and so does a markdown card whose content or a view whose title is exactly " +
		"that string. A mention inside a longer string is not a hit; map keys are never matched.",
	Args: takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDashGrep(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var dashReplaceCmd = &cobra.Command{
	Use:   "replace <old> <new> [url_path]",
	Short: "Rename a value within a dashboard (dry-run by default)",
	Long: "Replace every string value equal to <old> with <new> in one dashboard's config — the same " +
		"whole-value match `dash grep` reports, so it rewrites card entities, titles and markdown " +
		"content alike, and never rewrites map keys. Omit url_path for the default dashboard. Use " +
		"--confirm to save; `hactl ref replace` covers config files and dashboards in one pass.",
	Args: takesBetween(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		urlPath := ""
		if len(args) > 2 {
			urlPath = args[2]
		}
		return runDashReplace(cmd.Context(), cmd.OutOrStdout(), args[0], args[1], urlPath)
	},
}

func init() {
	dashShowCmd.Flags().StringVar(&flagDashView, "view", "", "show only the view with this path")
	dashShowCmd.Flags().BoolVar(&flagDashRaw, "raw", false, "output raw HA JSON (for LLM round-trip editing)")
	dashShowCmd.Flags().BoolVar(&flagDashYAML, "yaml", false, "output the dashboard config as YAML")
	dashReplaceCmd.Flags().BoolVar(&flagDashConfirm, "confirm", false, "actually save (default is dry-run)")
	dashSaveCmd.Flags().StringVarP(&flagDashFile, "file", "f", "", "JSON config file (default: read from stdin)")
	dashSaveCmd.Flags().BoolVar(&flagDashConfirm, "confirm", false, "actually save (default is dry-run)")
	dashCreateCmd.Flags().StringVar(&flagDashURLPath, "url-path", "", "dashboard URL path (must contain a hyphen)")
	dashCreateCmd.Flags().StringVar(&flagDashTitle, "title", "", "dashboard title")
	dashCreateCmd.Flags().StringVar(&flagDashIcon, "icon", "", "dashboard icon (e.g. mdi:view-dashboard)")
	dashCreateCmd.Flags().BoolVar(&flagDashSidebar, "sidebar", true, "show in sidebar")
	dashCreateCmd.Flags().BoolVar(&flagDashAdmin, "admin", false, "require admin access")
	dashCreateCmd.Flags().BoolVar(&flagDashConfirm, "confirm", false, "actually create (default is dry-run)")
	dashDeleteCmd.Flags().BoolVar(&flagDashConfirm, "confirm", false, "actually delete (default is dry-run)")

	_ = dashCreateCmd.MarkFlagRequired("url-path")
	_ = dashCreateCmd.MarkFlagRequired("title")

	dashCmd.AddCommand(dashLsCmd, dashShowCmd, dashSaveCmd, dashCreateCmd, dashDeleteCmd, dashResourcesCmd, dashGrepCmd, dashReplaceCmd)
	rootCmd.AddCommand(dashCmd)
}

func connectWS(ctx context.Context) (*haapi.WSClient, error) {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return nil, err
	}
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connecting to HA: %w", err)
	}
	return ws, nil
}

// dashboardConfigState is the answer to "what does Home Assistant hold for
// this url_path?" — the question `lovelace/info` cannot answer (it emits only
// resource_mode, which describes frontend resources; there is no `mode` field
// on that wire at all — captured from HA 2026.7.4 in both states,
// internal/integration/lovelace_oracle_test.go).
//
// It is asked of ANY dashboard, and that generality is the fix for finding #3.
// The state used to be modelled as a property of the default alone, with the
// premise written into a comment here: "the default dashboard has a state a
// named dashboard cannot have: auto-generated, where HA holds no config at
// all". Every dashboard is in that state between `dash create` and its first
// `dash save` — the family's own documented next step — and a named one got
// HA's bare wire error at exit 1 for it.
type dashboardConfigState int

const (
	// dashConfigStored: `lovelace/config` returned a document. Either a config
	// was saved for this dashboard, or configuration.yaml pins the default to
	// YAML mode — in both cases the document is real and readable.
	dashConfigStored dashboardConfigState = iota
	// dashConfigAbsent: HA holds no config for this dashboard
	// ({"code":"config_not_found","message":"No config found."}). For the
	// default that means HA builds it at view time; for a named dashboard it
	// means nothing has been saved yet.
	dashConfigAbsent
	// dashConfigUnclassifiable: `lovelace/config` failed some other way, so
	// hactl does not know which state the dashboard is in.
	dashConfigUnclassifiable
)

// classifyDashboardConfig classifies one Lovelace dashboard by attempting
// `lovelace/config` (D-6) — the only call that answers the question. raw
// carries the config for dashConfigStored; err carries the failure for
// dashConfigUnclassifiable.
//
// The caller must have resolved urlPath already. HA answers `config_not_found`
// to BOTH "this dashboard holds no config" ("No config found.") and "there is
// no such dashboard" ("Unknown config specified: x") — same code, two
// conditions, distinguishable on the wire only by an English message
// (lovelace/websocket.py, HA 2026.7.4; measured on the reference instance
// 2026-07-31). Classifying on the code alone would turn a typo into a serene
// "nothing is stored here", which is why every caller resolves first: the empty
// urlPath is the default and needs none, and every other target comes from
// `lovelace/dashboards/list`.
func classifyDashboardConfig(ctx context.Context, ws *haapi.WSClient, urlPath string) (state dashboardConfigState, raw json.RawMessage, err error) {
	raw, err = ws.DashboardConfigRaw(ctx, urlPath)
	if err == nil {
		return dashConfigStored, raw, nil
	}
	var apiErr *haapi.APIError
	if errors.As(err, &apiErr) && apiErr.Code == haapi.WSErrCodeLovelaceConfigNotFound {
		return dashConfigAbsent, nil, nil
	}
	return dashConfigUnclassifiable, nil, err
}

func runDashLs(ctx context.Context, w io.Writer) error {
	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	dashboards, err := ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}

	if len(dashboards) == 0 {
		return emitEmptyList(w, "no dashboards")
	}

	tbl := &format.Table{
		Headers: []string{"url_path", "title", "mode", "icon", "sidebar", "admin"},
		Rows:    make([][]string, len(dashboards)),
	}
	for i, d := range dashboards {
		tbl.Rows[i] = []string{
			d.URLPath,
			d.Title,
			d.Mode,
			d.Icon,
			strconv.FormatBool(d.ShowInSidebar),
			strconv.FormatBool(d.RequireAdmin),
		}
		// The cells above are a text table's rendering of two booleans; the
		// machine gets the booleans (finding #59). `"admin": "false"` is a
		// non-empty string, so the obvious `if row["admin"]` read every
		// dashboard on the reference instance as admin-only.
		tbl.SetMachine(i, "sidebar", d.ShowInSidebar)
		tbl.SetMachine(i, "admin", d.RequireAdmin)
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func runDashShow(ctx context.Context, w io.Writer, urlPath string) error {
	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	// Resolve before classifying. `config_not_found` covers both "nothing is
	// stored for this dashboard" and "there is no such dashboard" — see
	// classifyDashboardConfig — so without this the second would be reported as
	// the first, and `dash show <typo>` would exit 0.
	if urlPath != "" {
		if _, findErr := findDashboard(ctx, ws, urlPath); findErr != nil {
			return findErr
		}
	}

	state, raw, classifyErr := classifyDashboardConfig(ctx, ws, urlPath)
	switch state {
	case dashConfigUnclassifiable:
		return fmt.Errorf("fetching dashboard config: %w", classifyErr)
	case dashConfigAbsent:
		return reportNoStoredConfig(w, urlPath)
	case dashConfigStored:
	}

	// Raw / JSON / YAML mode: output the machine-readable config.
	//
	// All three write a DOCUMENT, so none of them may be token-capped: the cap
	// chops at a byte boundary, and a 91 541-byte dashboard came back as 2 096
	// bytes of invalid JSON with a plain-English notice appended, at exit 0.
	// --json was already exempt; --raw and --yaml were not, although --raw's own
	// help says it exists "for LLM round-trip editing".
	if flagDashRaw || flagJSON || flagDashYAML {
		markStructuredOutput()
		if flagDashView != "" {
			var viewErr error
			raw, viewErr = selectDashboardViewRaw(raw, flagDashView)
			if viewErr != nil {
				return viewErr
			}
		}
		if flagDashRaw {
			_, writeErr := w.Write(append(raw, '\n'))
			return writeErr
		}
		if flagDashYAML {
			var v any
			if unmarshalErr := json.Unmarshal(raw, &v); unmarshalErr != nil {
				return fmt.Errorf("parsing dashboard config: %w", unmarshalErr)
			}
			out, marshalErr := yaml.Marshal(v)
			if marshalErr != nil {
				return fmt.Errorf("marshaling dashboard config to YAML: %w", marshalErr)
			}
			_, writeErr := w.Write(out)
			return writeErr
		}
		// --json: pretty-print
		var buf json.RawMessage
		if unmarshalErr := json.Unmarshal(raw, &buf); unmarshalErr != nil {
			_, writeErr := w.Write(append(raw, '\n'))
			return writeErr
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(buf)
	}

	cfg, err := haapi.ParseLovelaceConfig(raw)
	if err != nil {
		return err
	}

	// --view is consulted BEFORE the no-views branch. It used to come after,
	// so `dash show map --view anything` printed "no views" at exit 0 while the
	// same missing view on a dashboard that has some exits 1 — one command, one
	// question ("does this view exist?"), two answers decided by an internal
	// shape the caller cannot see (finding #8). Under --raw the order was
	// already this way, which is how the same command disagreed with itself.
	if flagDashView != "" {
		return showSingleView(w, cfg)
	}

	if len(cfg.Views) == 0 {
		// Zero views is true here and "no views" alone is not the whole truth:
		// a strategy dashboard's views are built by the frontend at view time
		// from the strategy named in its config — Home Assistant's own `map`
		// dashboard is one. Reporting the emptiness without naming what fills
		// it is the same fabrication-by-omission D-3 forbids for the
		// auto-generated default.
		if strategy := haapi.LovelaceStrategyType(raw); strategy != "" {
			_, _ = fmt.Fprintf(w, "no stored views: this dashboard is strategy-generated (strategy: %s)\n", strategy)
			_, _ = fmt.Fprintln(w, "Home Assistant builds its views in the frontend at view time; "+
				"`hactl dash show <url_path> --raw` shows the stored document")
			return nil
		}
		_, _ = fmt.Fprintln(w, "no views")
		return nil
	}

	tbl := &format.Table{
		Headers: []string{"#", "title", "path", "type", "cards", "sections", "badges"},
		Rows:    make([][]string, len(cfg.Views)),
	}
	for i, raw := range cfg.Views {
		s := haapi.ParseViewSummary(raw)
		tbl.Rows[i] = []string{
			strconv.Itoa(i),
			s.Title,
			s.Path,
			viewType(s.Type),
			strconv.Itoa(s.Cards),
			strconv.Itoa(s.Sections),
			strconv.Itoa(s.Badges),
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Full: true,
	})
}

// noStoredConfigReport is `dash show --json`'s answer for a dashboard Home
// Assistant holds no config for. The `state` key is the machine discriminator
// (H-10): a stored answer is the config document itself, which never carries a
// top-level `state`, so a caller can tell the two apart by looking at the
// object rather than by remembering what it asked.
type noStoredConfigReport struct {
	URLPath string `json:"url_path"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
}

// noStoredConfigAnswer is what "Home Assistant holds no config" MEANS for a
// given dashboard, and the two meanings are different facts rather than two
// wordings of one.
//
// For the default, HA builds the dashboard at view time from areas and devices
// — the state is permanent-until-saved and the dashboard is fully usable, which
// is why it has always been reported at exit 0. For a named dashboard, the
// registration exists and nothing has been stored under it yet: hactl knows
// that HA has nothing, and deliberately claims nothing about what the frontend
// renders instead, because it has not asked the frontend.
// headline is what a person reads; state is what a machine branches on. They
// are returned together so the two audiences cannot drift apart, and they are
// deliberately not the same string — "no-stored-config" is a discriminator, not
// a sentence.
func noStoredConfigAnswer(urlPath string) (state, headline, detail string) {
	if urlPath == "" {
		return "auto-generated",
			"default dashboard: auto-generated (no stored config)",
			"Home Assistant builds the default dashboard at view time; " +
				"no stored config exists. Use `hactl dash ls` to list stored dashboards."
	}
	return "no-stored-config",
		"dashboard " + urlPath + ": no stored config",
		"The dashboard is registered and Home Assistant holds no config for it — " +
			"the state a dashboard is in between `dash create` and its first `dash save`. " +
			"Nothing is stored, so nothing here references anything."
}

// reportNoStoredConfig answers `dash show` when HA holds no config for the
// dashboard: say exactly that, and never fabricate a render of what HA or the
// frontend would generate (D-3).
func reportNoStoredConfig(w io.Writer, urlPath string) error {
	state, headline, detail := noStoredConfigAnswer(urlPath)
	label := dashDisplayPath(urlPath)
	if flagDashRaw || flagDashYAML {
		// A document was asked for and there is none. Refusing is the only
		// honest answer: emitting `{}` or `null` would be a config that says
		// "this dashboard is empty", which is a different claim.
		return fmt.Errorf("dashboard %s holds no stored config (%s), so there is no document to output; "+
			"`hactl dash ls` lists the dashboards on this instance and "+
			"`hactl dash save %s --confirm` stores a config for this one", label, state, label)
	}
	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(noStoredConfigReport{URLPath: label, State: state, Detail: detail})
	}
	_, _ = fmt.Fprintln(w, headline)
	_, _ = fmt.Fprintln(w, detail)
	_, _ = fmt.Fprintf(w, "use `hactl dash ls` to list stored dashboards, or `hactl dash save %s --confirm` "+
		"to store a config for this one\n", label)
	return nil
}

// showSingleView writes one view's config. It emits JSON whether or not --json
// was passed — that is what `dash show --view` has always meant — so it is a
// document by the same rule the raw/yaml branch is, and it was truncated into
// invalid JSON by the default cap for exactly that reason.
func showSingleView(w io.Writer, cfg *haapi.LovelaceConfig) error {
	markStructuredOutput()
	for _, raw := range cfg.Views {
		s := haapi.ParseViewSummary(raw)
		if s.Path == flagDashView || s.Title == flagDashView {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			var v any
			_ = json.Unmarshal(raw, &v)
			return enc.Encode(v)
		}
	}
	return fmt.Errorf("view %q not found", flagDashView)
}

func selectDashboardViewRaw(raw json.RawMessage, view string) (json.RawMessage, error) {
	var cfg haapi.LovelaceConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing dashboard config: %w", err)
	}
	for _, candidate := range cfg.Views {
		s := haapi.ParseViewSummary(candidate)
		if s.Path == view {
			return candidate, nil
		}
	}
	for _, candidate := range cfg.Views {
		s := haapi.ParseViewSummary(candidate)
		if s.Title == view {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("view %q not found", view)
}

func viewType(t string) string {
	if t == "" {
		return "masonry"
	}
	return t
}

func runDashSave(ctx context.Context, w io.Writer, urlPath string) error {
	// Read config JSON from file or stdin
	var data []byte
	var err error
	if flagDashFile != "" {
		data, err = os.ReadFile(filepath.Clean(flagDashFile))
		if err != nil {
			return fmt.Errorf("reading config file: %w", err)
		}
	} else {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
	}

	// Validate JSON
	if !json.Valid(data) {
		return errors.New("invalid JSON in config input")
	}

	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	// Resolve before planning: `dash save` overwrites the WHOLE config, so a
	// preview against a url_path HA does not have is not a harmless typo — it
	// is a full-replacement plan that names the wrong target.
	//
	// The empty url_path is the exception, and a real one: it means HA's
	// default Lovelace storage, which `lovelace/dashboards/list` does not
	// report unless the default is pinned to YAML mode, and which is otherwise
	// always a valid save target — saving to it is how an auto-generated
	// default becomes storage-mode in the first place.
	dashboards, err := ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}
	if gateErr := refuseYamlModeDashboardWrite(dashboards, urlPath); gateErr != nil {
		return gateErr
	}
	title := "(default)"
	if urlPath != "" {
		target, findErr := findDashboardIn(dashboards, urlPath)
		if findErr != nil {
			return findErr
		}
		title = target.Title
	}

	if !flagDashConfirm {
		return dryRun("save dashboard config").
			with("url_path", dashDisplayPath(urlPath)).
			with("title", title).
			with("config_bytes", len(data)).
			render(w)
	}

	snapshotDashboardBeforeSave(ctx, ws, urlPath)
	if err := ws.DashboardConfigSave(ctx, urlPath, data); err != nil {
		return fmt.Errorf("saving dashboard config: %w", err)
	}

	return done("save dashboard config").
		with("url_path", dashDisplayPath(urlPath)).
		with("title", title).
		with("config_bytes", len(data)).
		text("saved dashboard config for %s", dashDisplayPath(urlPath)).
		render(w)
}

// dashScanTarget is a dashboard to scan: its display label and WS url_path.
type dashScanTarget struct {
	label   string
	urlPath string
}

// defaultDashboardURLPath is the url_path HA lists the default dashboard
// under when it is listed at all. No user-created dashboard can take this
// slug: `lovelace/dashboards/create` requires a hyphen in the url_path, and
// "lovelace" has none.
const defaultDashboardURLPath = "lovelace"

// defaultDashboardListing returns the list entry for the default dashboard, and
// whether it is listed at all.
//
// This used to be a bool named defaultDashboardIsListed, and one predicate was
// answering two questions. Its DEDUP use is correct and unchanged — a listed
// default is the same dashboard as the "" pseudo-target, so scanning both would
// report every hit twice, and HA's own websocket layer says so: with no
// url_path it serves `dashboards["lovelace"] or dashboards[None]`
// (lovelace/websocket.py, HA 2026.7.4). Its MODE use was false. It read "listed"
// as "YAML-mode", because the only state that had ever produced a listed
// default was the YAML one — and since HA 2026.x `_async_migrate_default_config`
// moves a stored default into the dashboards collection at boot, so the
// reference instance lists it with mode `storage`. `dash save` and `dash replace`
// refused every write to that default, citing a `lovelace: mode: yaml` line its
// configuration.yaml does not contain. The mode is on the entry; read it there.
func defaultDashboardListing(dashboards []haapi.LovelaceDashboard) (haapi.LovelaceDashboard, bool) {
	for _, d := range dashboards {
		if d.URLPath == defaultDashboardURLPath {
			return d, true
		}
	}
	return haapi.LovelaceDashboard{}, false
}

// defaultDashboardIsListed reports whether dashboards/list carries the default
// dashboard itself, which is the question a DEDUP has to ask.
func defaultDashboardIsListed(dashboards []haapi.LovelaceDashboard) bool {
	_, listed := defaultDashboardListing(dashboards)
	return listed
}

// dashboardScanTargets returns the default dashboard plus every listed dashboard.
//
// When the default is in YAML mode it appears IN the list (url_path
// "lovelace"), and `lovelace/config` returns the same document with and
// without that url_path — so the "" pseudo-target is skipped then, or every
// scan would report the same dashboard twice.
func dashboardScanTargets(dashboards []haapi.LovelaceDashboard) []dashScanTarget {
	targets := make([]dashScanTarget, 0, 1+len(dashboards))
	if !defaultDashboardIsListed(dashboards) {
		targets = append(targets, dashScanTarget{label: "(default)", urlPath: ""})
	}
	for _, d := range dashboards {
		targets = append(targets, dashScanTarget{label: d.URLPath, urlPath: d.URLPath})
	}
	return targets
}

// dashHit is one dashboard reference: the dashboard label and the path within it.
type dashHit struct{ dashboard, path string }

// dashboardScanScope is what a dashboard walk actually covered: how many
// dashboards were read, and one "<label>: <reason>" entry for each one that
// could not be. The two reconcile to the number of targets (H-11), so a caller
// can state its scope instead of implying a complete answer.
type dashboardScanScope struct {
	scanned   int
	unscanned []string
}

// total is the number of dashboards the walk was asked about.
func (s dashboardScanScope) total() int { return s.scanned + len(s.unscanned) }

// partial reports whether at least one dashboard could not be read, which is
// the only condition any caller branches on.
func (s dashboardScanScope) partial() bool { return len(s.unscanned) > 0 }

// reason joins the per-dashboard skip reasons as an error, or nil when the walk
// was complete — the shape a gate wants to wrap.
func (s dashboardScanScope) reason() error {
	if !s.partial() {
		return nil
	}
	return errors.New(strings.Join(s.unscanned, "; "))
}

// walkDashboardConfigs is the ONE place hactl reads a set of dashboards. It
// fetches and parses each target's config and hands the decoded document to
// visit, and it returns the scope it covered.
//
// Every target is classified with classifyDashboardConfig (D-6/D-7) rather
// than fetched blindly: a dashboard HA holds no config for holds no
// references either, so zero references there is the whole truth about that
// dashboard — a complete answer, not a gap. Any other fetch or parse failure
// lands in scope.unscanned naming the dashboard and the reason.
//
// Every caller must do something visible with a partial scope. A search command
// ("where is X?") reports it and still answers; a gate ("is the tree clean?")
// refuses, because a skipped dashboard makes the certificate vacuous. Dropping
// it at slog.Debug — which both of the walks this function replaces used to do
// — is the one thing no caller may do (D-7).
func walkDashboardConfigs(ctx context.Context, ws *haapi.WSClient, targets []dashScanTarget,
	visit func(t dashScanTarget, root any)) dashboardScanScope {
	var scope dashboardScanScope
	for _, t := range targets {
		state, raw, classifyErr := classifyDashboardConfig(ctx, ws, t.urlPath)
		switch state {
		case dashConfigAbsent:
			// Nothing stored, so nothing to read: this dashboard is fully
			// accounted for and contributes nothing. Only the DEFAULT used to
			// reach this branch, so one `dash create` that had not been saved
			// yet made every walk partial — `dash grep` warned its answer was
			// incomplete and `ref validate`, a gate, refused to certify
			// anything at all (finding #3). Every target here comes from
			// `lovelace/dashboards/list`, so `config_not_found` cannot mean
			// "no such dashboard" (see classifyDashboardConfig).
			scope.scanned++
			continue
		case dashConfigUnclassifiable:
			scope.unscanned = append(scope.unscanned,
				fmt.Sprintf("%s: fetching dashboard config: %v", t.label, classifyErr))
			continue
		case dashConfigStored:
		}
		var root any
		if unmarshalErr := json.Unmarshal(raw, &root); unmarshalErr != nil {
			scope.unscanned = append(scope.unscanned,
				fmt.Sprintf("%s: parsing dashboard config: %v", t.label, unmarshalErr))
			continue
		}
		scope.scanned++
		visit(t, root)
	}
	return scope
}

// scanDashboards walks each target dashboard for exact string leaves equal to
// target, returning every hit plus the scope the walk covered. Shared by
// `dash grep` and `ref scan`.
func scanDashboards(ctx context.Context, ws *haapi.WSClient, targets []dashScanTarget, target string) ([]dashHit, dashboardScanScope) {
	var hits []dashHit
	scope := walkDashboardConfigs(ctx, ws, targets, func(t dashScanTarget, root any) {
		jsonwalk.FindString(root, target, func(p jsonwalk.Path) {
			hits = append(hits, dashHit{t.label, p.String()})
		})
	})
	return hits, scope
}

// warnPartialDashboardScan is how a *search* command reports a dashboard it
// could not read: loudly enough to be seen (slog.Warn, never slog.Debug — the
// D-7 defect), but without failing and without touching stdout, because
// `dash grep`/`ref scan` answer "where is X?" rather than "is the tree clean?"
// and their --json shape is a contract (H-10).
func warnPartialDashboardScan(scope dashboardScanScope) {
	if !scope.partial() {
		return
	}
	slog.Warn("some dashboards could not be scanned; this answer is partial",
		"scanned", scope.scanned, "of", scope.total(), "unscanned", scope.unscanned)
}

// dashReplaceOne fetches one dashboard's raw config and returns a deep copy with
// every exact occurrence of oldVal rewritten to newVal, along with the changed
// paths. It never saves. A nil error with no changed paths means no match.
// Shared by `dash replace` and `ref replace`.
func dashReplaceOne(ctx context.Context, ws *haapi.WSClient, urlPath, oldVal, newVal string) (result any, changed []jsonwalk.Path, err error) {
	// A dashboard with no stored config contains no occurrences of anything, so
	// the answer is "nothing matched" rather than a wire error (finding #3).
	// Callers must have resolved urlPath — see classifyDashboardConfig.
	state, raw, classifyErr := classifyDashboardConfig(ctx, ws, urlPath)
	switch state {
	case dashConfigAbsent:
		return nil, nil, nil
	case dashConfigUnclassifiable:
		return nil, nil, fmt.Errorf("fetching dashboard config: %w", classifyErr)
	case dashConfigStored:
	}
	var root any
	if unmarshalErr := json.Unmarshal(raw, &root); unmarshalErr != nil {
		return nil, nil, fmt.Errorf("parsing dashboard config: %w", unmarshalErr)
	}
	result, changed = jsonwalk.Replace(root, oldVal, newVal)
	return result, changed, nil
}

// runDashGrep scans every dashboard for an exact entity_id reference.
func runDashGrep(ctx context.Context, w io.Writer, target string) error {
	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	dashboards, err := ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}

	hits, scope := scanDashboards(ctx, ws, dashboardScanTargets(dashboards), target)
	warnPartialDashboardScan(scope)

	if len(hits) == 0 {
		// The miss must only claim what the query tested (D-10): matching is
		// whole-value, so for a substring intent "not referenced" alone would
		// be a wrong answer under the manual's stop-at-the-first-miss rule.
		return emitEmptyList(w, target+": not referenced as a whole value in any dashboard "+
			"(grep matches complete string values, never substrings — for term discovery: "+
			"hactl ent ls --pattern '*"+target+"*')")
	}

	tbl := &format.Table{
		Headers: []string{"dashboard", "path"},
		Rows:    make([][]string, len(hits)),
	}
	for i, h := range hits {
		tbl.Rows[i] = []string{h.dashboard, h.path}
	}
	return tbl.Render(w, format.RenderOpts{
		Top:  flagTop,
		Full: true,
		JSON: flagJSON,
	})
}

// runDashReplace renames every exact occurrence of oldVal to newVal within one
// dashboard, gated by --confirm with a path-level dry-run diff.
func runDashReplace(ctx context.Context, w io.Writer, oldVal, newVal, urlPath string) error {
	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	// A YAML-mode target (including a YAML-mode default) is readable but not
	// savable, so the preview must refuse where --confirm would (H-2).
	dashboards, err := ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}
	if gateErr := refuseYamlModeDashboardWrite(dashboards, urlPath); gateErr != nil {
		return gateErr
	}
	// Resolve the target before reading it. `dash save` already did; this one
	// leaned on the fetch failing, which stopped being an error the moment a
	// dashboard with no stored config became an answer instead of a failure —
	// without this, `dash replace a b <typo>` would report "not found in
	// dashboard <typo>" at exit 0 (confirm.manifest's dash replace [target] row).
	if urlPath != "" {
		if _, findErr := findDashboardIn(dashboards, urlPath); findErr != nil {
			return findErr
		}
	}

	result, changed, err := dashReplaceOne(ctx, ws, urlPath, oldVal, newVal)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		// "nothing matched" is an outcome, not an absence of one: under --json
		// this printed prose and exit 0, which a caller cannot tell from a
		// command that produced no output at all.
		return done("replace in dashboard "+dashDisplayPath(urlPath)).
			with("dashboard", dashDisplayPath(urlPath)).
			with("from", oldVal).
			with("to", newVal).
			with("occurrences", 0).
			text("%q not found in dashboard %s", oldVal, dashDisplayPath(urlPath)).
			asPreview(!flagDashConfirm).
			render(w)
	}

	if !flagDashConfirm {
		sites := make([]string, 0, len(changed))
		for _, p := range changed {
			sites = append(sites, p.String())
		}
		return dryRun("replace in dashboard "+dashDisplayPath(urlPath)).
			with("dashboard", dashDisplayPath(urlPath)).
			with("from", oldVal).
			with("to", newVal).
			with("occurrences", len(changed)).
			with("paths", sites).
			render(w)
	}

	out, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encoding dashboard config: %w", err)
	}
	snapshotDashboardBeforeSave(ctx, ws, urlPath)
	if err := ws.DashboardConfigSave(ctx, urlPath, out); err != nil {
		return fmt.Errorf("saving dashboard config: %w", err)
	}
	sites := make([]string, 0, len(changed))
	for _, p := range changed {
		sites = append(sites, p.String())
	}
	return done("replace in dashboard "+dashDisplayPath(urlPath)).
		with("dashboard", dashDisplayPath(urlPath)).
		with("from", oldVal).
		with("to", newVal).
		with("occurrences", len(changed)).
		with("paths", sites).
		text("replaced %q → %q in dashboard %s (%d occurrence(s))", oldVal, newVal, dashDisplayPath(urlPath), len(changed)).
		render(w)
}

func runDashCreate(ctx context.Context, w io.Writer) error {
	// Validate the input before reporting a plan for it (H-2). HA's lovelace
	// url_slug validator requires a hyphen and refuses a path already in use,
	// and the flag's own help text says so — but the preview checked neither
	// and contacted nothing, so it printed a confident plan for a path
	// --confirm would reject, without even an instance configured.
	if flagDashURLPath == "" {
		return errors.New("--url-path is required")
	}
	if !strings.Contains(flagDashURLPath, "-") {
		return fmt.Errorf("--url-path %q must contain a hyphen (Home Assistant's url_slug rule)", flagDashURLPath)
	}

	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	if existing, findErr := findDashboard(ctx, ws, flagDashURLPath); findErr == nil {
		return fmt.Errorf("dashboard %q already exists (id: %s)", existing.URLPath, existing.ID)
	}

	if !flagDashConfirm {
		return dryRun("create dashboard").
			with("url_path", flagDashURLPath).
			with("title", flagDashTitle).
			with("icon", flagDashIcon).
			with("sidebar", flagDashSidebar).
			with("admin", flagDashAdmin).
			render(w)
	}

	d, err := ws.DashboardCreate(ctx, haapi.DashboardCreateParams{
		URLPath:       flagDashURLPath,
		Title:         flagDashTitle,
		Icon:          flagDashIcon,
		ShowInSidebar: flagDashSidebar,
		RequireAdmin:  flagDashAdmin,
	})
	if err != nil {
		return fmt.Errorf("creating dashboard: %w", err)
	}

	return done("create dashboard").
		with("url_path", d.URLPath).
		with("id", d.ID).
		with("title", flagDashTitle).
		with("icon", flagDashIcon).
		with("sidebar", flagDashSidebar).
		with("admin", flagDashAdmin).
		text("created dashboard %q (id: %s)", d.URLPath, d.ID).
		render(w)
}

func runDashDelete(ctx context.Context, w io.Writer, urlPath string) error {
	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	target, err := findDashboard(ctx, ws, urlPath)
	if err != nil {
		return err
	}
	// A YAML-mode dashboard exists in configuration.yaml, not in HA's storage
	// collection — there is nothing the delete API could remove (its list
	// entry does not even carry an id). Refuse in preview and confirm alike.
	if target.Mode != dashboardStorageMode {
		return fmt.Errorf("dashboard %q is %s-mode (defined in YAML configuration); "+
			"remove it from configuration.yaml instead", urlPath, target.Mode)
	}

	if !flagDashConfirm {
		return dryRun("delete dashboard").
			with("url_path", urlPath).
			with("title", target.Title).
			render(w)
	}

	if delErr := ws.DashboardDelete(ctx, target.ID); delErr != nil {
		// HA reported a failure. Whether the dashboard is still there is a
		// separate question, and the only way to answer it is to ask (H-12).
		//
		// Finding #57: on a url_path long enough that
		// `.storage/lovelace.<id>` exceeds the filesystem's filename limit, HA
		// removes the item from its collection and THEN fails unlinking the
		// file, from a listener that runs after the removal — so the websocket
		// answers "Unknown error" about a dashboard that is already gone
		// (OSError [Errno 36], traceback captured on the reference instance
		// 2026-07-31). Reporting that as a plain failure tells a caller the
		// object still exists, and a retry then fails with "not found".
		if _, findErr := findDashboard(ctx, ws, urlPath); findErr == nil {
			return fmt.Errorf("deleting dashboard: %w", delErr)
		}
		return done("delete dashboard").
			with("url_path", urlPath).
			with("title", target.Title).
			text("deleted dashboard %q", urlPath).
			warn("Home Assistant answered this delete with an error (%v) and the dashboard is no "+
				"longer registered, so the delete took effect", delErr).
			render(w)
	}

	return done("delete dashboard").
		with("url_path", urlPath).
		with("title", target.Title).
		text("deleted dashboard %q", urlPath).
		render(w)
}

// findDashboard resolves a url_path against HA's own dashboard list.
func findDashboard(ctx context.Context, ws *haapi.WSClient, urlPath string) (haapi.LovelaceDashboard, error) {
	dashboards, err := ws.DashboardList(ctx)
	if err != nil {
		return haapi.LovelaceDashboard{}, fmt.Errorf("listing dashboards: %w", err)
	}
	return findDashboardIn(dashboards, urlPath)
}

// findDashboardIn resolves a url_path within an already-fetched dashboard list.
func findDashboardIn(dashboards []haapi.LovelaceDashboard, urlPath string) (haapi.LovelaceDashboard, error) {
	for _, d := range dashboards {
		if d.URLPath == urlPath {
			return d, nil
		}
	}
	return haapi.LovelaceDashboard{}, fmt.Errorf(
		"dashboard %q not found (use 'dash ls' to see available dashboards)", dashDisplayPath(urlPath))
}

// refuseYamlModeDashboardWrite refuses a write aimed at a YAML-mode dashboard
// before any plan is printed. HA answers `lovelace/config/save` for those with
// "Not supported" (HA 2026.7.4, lovelace_oracle_test.go), so a preview that
// proceeded would print a plan --confirm can only fail (H-2).
//
// The empty urlPath means the default dashboard. Its mode comes from its own
// list entry, never from the fact that it has one: an unlisted default is
// storage-mode (nothing else produces an unlisted default), and a listed one
// carries the answer in `mode` — `yaml` when configuration.yaml pins it,
// `storage` when HA's own migration moved a stored default into the
// collection. See defaultDashboardListing.
func refuseYamlModeDashboardWrite(dashboards []haapi.LovelaceDashboard, urlPath string) error {
	if urlPath == "" {
		listing, listed := defaultDashboardListing(dashboards)
		if listed && listing.Mode != dashboardStorageMode {
			return fmt.Errorf("the default dashboard is %s-mode (configuration.yaml pins `lovelace: mode: %s`); "+
				"Home Assistant does not accept dashboard writes for it — edit its YAML file instead",
				strings.ToUpper(listing.Mode), listing.Mode)
		}
		return nil
	}
	for _, d := range dashboards {
		if d.URLPath == urlPath && d.Mode != dashboardStorageMode {
			return fmt.Errorf("dashboard %q is %s-mode; Home Assistant does not accept dashboard writes for it — "+
				"edit its YAML file instead", urlPath, d.Mode)
		}
	}
	return nil
}

// dashboardStorageMode is the one mode Home Assistant accepts dashboard writes
// for (lovelace/const.py: `MODE_STORAGE`, and STORAGE_DASHBOARD_CREATE_FIELDS
// pins every created dashboard to it).
const dashboardStorageMode = "storage"

func runDashResources(ctx context.Context, w io.Writer) error {
	ws, err := connectWS(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()

	resources, err := ws.ResourceList(ctx)
	if err != nil {
		return fmt.Errorf("listing resources: %w", err)
	}

	if len(resources) == 0 {
		return emitEmptyList(w, "no resources")
	}

	tbl := &format.Table{
		Headers: []string{"id", "type", "url"},
		Rows:    make([][]string, len(resources)),
	}
	for i, r := range resources {
		tbl.Rows[i] = []string{r.ID, r.Type, r.URL}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func dashDisplayPath(urlPath string) string {
	if urlPath == "" {
		return "(default)"
	}
	return urlPath
}
