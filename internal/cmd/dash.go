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

// defaultDashboardState is the answer to "what is HA's default dashboard right
// now?" — the question `lovelace/info` cannot answer (it emits only
// resource_mode, which describes frontend resources; there is no `mode` field
// on that wire at all — captured from HA 2026.7.4 in both states,
// internal/integration/lovelace_oracle_test.go).
type defaultDashboardState int

const (
	// defaultDashStored: `lovelace/config` returned a document. Either a
	// config was saved for the default (storage), or configuration.yaml pins
	// it to YAML mode — in both cases the document is real and readable.
	defaultDashStored defaultDashboardState = iota
	// defaultDashAutoGenerated: HA holds no config and builds the dashboard
	// at view time ({"code":"config_not_found","message":"No config found."}).
	defaultDashAutoGenerated
	// defaultDashUnclassifiable: `lovelace/config` failed some other way, so
	// hactl does not know which state the default is in.
	defaultDashUnclassifiable
)

// classifyDefaultDashboard classifies HA's default Lovelace dashboard by
// attempting `lovelace/config` (D-6) — the only call that answers the
// question. raw carries the config for defaultDashStored; err carries the
// failure for defaultDashUnclassifiable.
func classifyDefaultDashboard(ctx context.Context, ws *haapi.WSClient) (state defaultDashboardState, raw json.RawMessage, err error) {
	raw, err = ws.DashboardConfigRaw(ctx, "")
	if err == nil {
		return defaultDashStored, raw, nil
	}
	var apiErr *haapi.APIError
	if errors.As(err, &apiErr) && apiErr.Code == haapi.WSErrCodeLovelaceConfigNotFound {
		return defaultDashAutoGenerated, nil, nil
	}
	return defaultDashUnclassifiable, nil, err
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

	var raw json.RawMessage
	if urlPath == "" {
		// The default dashboard has a state a named dashboard cannot have:
		// auto-generated, where HA holds no config at all (D-3/D67). Classify
		// it honestly instead of failing with HA's bare error.
		state, classified, classifyErr := classifyDefaultDashboard(ctx, ws)
		switch state {
		case defaultDashUnclassifiable:
			return fmt.Errorf("fetching dashboard config: %w", classifyErr)
		case defaultDashAutoGenerated:
			return reportAutoGeneratedDefault(w)
		case defaultDashStored:
			raw = classified
		}
	} else {
		var rawErr error
		raw, rawErr = ws.DashboardConfigRaw(ctx, urlPath)
		if rawErr != nil {
			return fmt.Errorf("fetching dashboard config: %w", rawErr)
		}
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

	if len(cfg.Views) == 0 {
		_, _ = fmt.Fprintln(w, "no views")
		return nil
	}

	// If --view is set, find and display that specific view
	if flagDashView != "" {
		return showSingleView(w, cfg)
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

// autoGeneratedDefaultReport is `dash show --json`'s answer for the
// auto-generated default dashboard. The `state` key is the machine
// discriminator (H-10): a stored answer is the config document itself, which
// never carries a top-level `state`, so a caller can tell the two apart by
// looking at the object rather than by remembering what it asked.
type autoGeneratedDefaultReport struct {
	URLPath string `json:"url_path"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
}

// reportAutoGeneratedDefault answers `dash show` (no argument) when HA holds
// no stored config for the default dashboard: say exactly that and point to
// `dash ls` — never fabricate a render of what HA would generate (D-3).
func reportAutoGeneratedDefault(w io.Writer) error {
	if flagDashRaw || flagDashYAML {
		return errors.New("the default dashboard is auto-generated: Home Assistant builds it at view time " +
			"and holds no stored config to output; `hactl dash ls` lists the dashboards that have one " +
			"(`hactl dash save --confirm` stores a config for the default and makes it editable)")
	}
	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(autoGeneratedDefaultReport{
			URLPath: dashDisplayPath(""),
			State:   "auto-generated",
			Detail: "Home Assistant builds the default dashboard at view time; no stored config exists. " +
				"Use `hactl dash ls` to list stored dashboards.",
		})
	}
	_, _ = fmt.Fprintln(w, "default dashboard: auto-generated (no stored config)")
	_, _ = fmt.Fprintln(w, "Home Assistant builds this dashboard at view time from your areas and devices; "+
		"there is no stored config for hactl to show.")
	_, _ = fmt.Fprintln(w, "use `hactl dash ls` to list stored dashboards, or `hactl dash save --confirm` "+
		"to store a config for the default")
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
// under when configuration.yaml pins it to YAML mode. No user-created
// dashboard can take this slug: `lovelace/dashboards/create` requires a
// hyphen in the url_path, and "lovelace" has none. In the storage and
// auto-generated states the default is never listed at all. (Captured from
// HA 2026.7.4: internal/integration/lovelace_oracle_test.go.)
const defaultDashboardURLPath = "lovelace"

// defaultDashboardIsListed reports whether dashboards/list carries the
// default dashboard itself — which happens exactly when it is in YAML mode.
func defaultDashboardIsListed(dashboards []haapi.LovelaceDashboard) bool {
	for _, d := range dashboards {
		if d.URLPath == defaultDashboardURLPath {
			return true
		}
	}
	return false
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
// The default dashboard is classified with classifyDefaultDashboard (D-6/D-7)
// rather than fetched blindly: the auto-generated state holds no config, so
// zero references there is the whole truth about that dashboard — a complete
// answer, not a gap. Any other fetch or parse failure, on the default or on a
// listed dashboard, lands in scope.unscanned naming the dashboard and the
// reason.
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
		var raw json.RawMessage
		if t.urlPath == "" {
			state, classified, classifyErr := classifyDefaultDashboard(ctx, ws)
			switch state {
			case defaultDashAutoGenerated:
				// Nothing stored, so nothing to read: this dashboard is fully
				// accounted for and contributes nothing.
				scope.scanned++
				continue
			case defaultDashUnclassifiable:
				scope.unscanned = append(scope.unscanned, fmt.Sprintf("%s: %v", t.label, classifyErr))
				continue
			case defaultDashStored:
				raw = classified
			}
		} else {
			var rawErr error
			raw, rawErr = ws.DashboardConfigRaw(ctx, t.urlPath)
			if rawErr != nil {
				scope.unscanned = append(scope.unscanned,
					fmt.Sprintf("%s: fetching dashboard config: %v", t.label, rawErr))
				continue
			}
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
	raw, err := ws.DashboardConfigRaw(ctx, urlPath)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching dashboard config: %w", err)
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
	if target.Mode != "storage" {
		return fmt.Errorf("dashboard %q is %s-mode (defined in YAML configuration); "+
			"remove it from configuration.yaml instead", urlPath, target.Mode)
	}

	if !flagDashConfirm {
		return dryRun("delete dashboard").
			with("url_path", urlPath).
			with("title", target.Title).
			render(w)
	}

	if err := ws.DashboardDelete(ctx, target.ID); err != nil {
		return fmt.Errorf("deleting dashboard: %w", err)
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
// proceeded would print a plan --confirm can only fail (H-2). The empty
// urlPath means the default dashboard, which is YAML-mode exactly when it
// appears in the list under the reserved slug "lovelace".
func refuseYamlModeDashboardWrite(dashboards []haapi.LovelaceDashboard, urlPath string) error {
	if urlPath == "" {
		if defaultDashboardIsListed(dashboards) {
			return errors.New("the default dashboard is YAML-mode (configuration.yaml pins `lovelace: mode: yaml`); " +
				"Home Assistant does not accept dashboard writes for it — edit its YAML file instead")
		}
		return nil
	}
	for _, d := range dashboards {
		if d.URLPath == urlPath && d.Mode != "storage" {
			return fmt.Errorf("dashboard %q is %s-mode; Home Assistant does not accept dashboard writes for it — "+
				"edit its YAML file instead", urlPath, d.Mode)
		}
	}
	return nil
}

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
