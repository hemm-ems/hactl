package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/jsonwalk"
)

var (
	flagRefConfirm      bool
	flagRefExitCode     bool
	flagRefAllowPartial bool
)

var refCmd = &cobra.Command{
	Use:   "ref",
	Short: "Find and rename entity references across config files and dashboards",
	Long: "Scan and rewrite literal entity_id references everywhere they appear — YAML config files " +
		"(via the companion, following !include) and Lovelace dashboards (via the WebSocket API) — in one pass.",
}

var refScanCmd = &cobra.Command{
	Use:   "scan <target>",
	Short: "Find every reference to a value across config and dashboards",
	Long: "Scan all YAML config files (following !include) and every dashboard for an exact string " +
		"(typically an entity_id) and report the source, location, and path of each reference.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRefScan(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var refReplaceCmd = &cobra.Command{
	Use:   "replace <old> <new>",
	Short: "Rename an entity reference everywhere (dry-run by default)",
	Long: "Replace every exact occurrence of <old> with <new> across all YAML config files and every " +
		"writable dashboard in one pass — including the default dashboard whenever it has a stored " +
		"config. Dry-run by default; use --confirm to apply. References in YAML-mode dashboards " +
		"cannot be rewritten over the API, and a dashboard that cannot be scanned leaves the rename " +
		"unverifiable — both refuse loudly unless --allow-partial is given.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRefReplace(cmd.Context(), cmd.OutOrStdout(), args[0], args[1])
	},
}

var refValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Find dangling entity references — pointers to entities that no longer exist",
	Long: "Sweep every YAML config file (via the companion, following !include) and every dashboard for " +
		"entity_id references, then report the ones that no longer map to a live Home Assistant entity. " +
		"The live set is the union of the entity registry and the current states, so state-only entities " +
		"(sun.sun, zone.home, weather.*, template sensors) are not falsely flagged.\n\n" +
		"Conservative by design: only values in known entity-holding positions are checked " +
		"(entity_id/entity in config; entity/entities/badges/camera_image in dashboards), so service " +
		"names like `light.turn_on` are never mistaken for entities. Two blind spots are the accepted " +
		"trade for zero false positives: entities embedded in templates (`{{ states('sensor.x') }}`) and " +
		"entities under non-standard custom-card keys are not detected. validate reports; it does not fix " +
		"— rename each dangling id with `hactl ref replace <old> <new>`.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRefValidate(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	refReplaceCmd.Flags().BoolVar(&flagRefConfirm, "confirm", false, "actually apply changes (default is dry-run)")
	refReplaceCmd.Flags().BoolVar(&flagRefAllowPartial, "allow-partial", false,
		"rename in whatever can be scanned and written even when some dashboards cannot be "+
			"(references in those dashboards are left unchanged)")
	refValidateCmd.Flags().BoolVar(&flagRefExitCode, "exit-code", false, "exit 1 if any dangling reference is found (for CI/pre-commit gating)")
	refValidateCmd.Flags().BoolVar(&flagRefAllowPartial, "allow-partial", false,
		"validate even when live states are unavailable and only the entity registry can be read "+
			"(higher false-positive risk: state-only entities are omitted from the registry)")
	refCmd.AddCommand(refScanCmd, refReplaceCmd, refValidateCmd)
	rootCmd.AddCommand(refCmd)
}

// refSources bundles the connections a ref command needs: a live WS client for
// dashboards (and the entity registry), a companion client for config files, and
// a REST client for /api/states (used by `ref validate` to build the live set).
type refSources struct {
	ws   *haapi.WSClient
	cc   *companion.Client
	rest *haapi.Client
}

func (s *refSources) close() {
	if s.ws != nil {
		_ = s.ws.Close()
	}
}

// connectRefSources connects to HA over WebSocket and discovers the companion.
func connectRefSources(ctx context.Context) (*refSources, error) {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return nil, err
	}
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return nil, fmt.Errorf("connecting to HA: %w", connErr)
	}
	companionURL, err := companion.Discover(ctx, cfg, ws)
	if err != nil {
		_ = ws.Close()
		return nil, fmt.Errorf("companion discovery: %w", err)
	}
	cc := companion.New(companionURL, cfg.CompanionToken).WithIngressAuth(ws)
	rest := haapi.New(cfg.URL, cfg.Token)
	return &refSources{ws: ws, cc: cc, rest: rest}, nil
}

// refRow is one merged reference across sources for `ref scan`.
type refRow struct {
	source   string // "config" | "dashboard"
	location string
	path     string
}

func runRefScan(ctx context.Context, w io.Writer, target string) error {
	src, err := connectRefSources(ctx)
	if err != nil {
		return err
	}
	defer src.close()

	var rows []refRow

	// Config files (companion). A companion failure is surfaced as a warning
	// but does not hide dashboard hits — better a partial answer than none.
	if scan, scanErr := src.cc.RefScan(ctx, target); scanErr != nil {
		slog.Warn("companion config scan failed; config files were not scanned", "error", scanErr)
	} else {
		for _, h := range scan.Hits {
			rows = append(rows, refRow{"config", h.Location, h.Path})
		}
	}

	// Dashboards (WS).
	dashboards, err := src.ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}
	for _, h := range scanDashboards(ctx, src.ws, dashboardScanTargets(dashboards), target) {
		rows = append(rows, refRow{"dashboard", h.dashboard, h.path})
	}

	if len(rows) == 0 {
		return emitEmptyList(w, target+": not referenced in any config file or dashboard")
	}

	tbl := &format.Table{
		Headers: []string{"source", "location", "path"},
		Rows:    make([][]string, len(rows)),
	}
	for i, r := range rows {
		tbl.Rows[i] = []string{r.source, r.location, r.path}
	}
	return tbl.Render(w, format.RenderOpts{Top: flagTop, Full: true, JSON: flagJSON})
}

// dashReplacePlan is one dashboard's pending rewrite for `ref replace`.
type dashReplacePlan struct {
	label    string
	urlPath  string
	result   any
	changed  []jsonwalk.Path
	writable bool
}

func runRefReplace(ctx context.Context, w io.Writer, oldVal, newVal string) error {
	src, err := connectRefSources(ctx)
	if err != nil {
		return err
	}
	defer src.close()

	confirm := flagRefConfirm

	// --- Dashboard side first: read-only planning (D-6) ---
	// The whole dashboard plan is computed before the companion writes
	// anything, so a partial-scan or yaml-mode refusal aborts with nothing
	// written anywhere.
	dashboards, err := src.ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}
	plans, unscanned := planDashboardReplacements(ctx, src.ws, dashboards, oldVal, newVal)

	// A dashboard that could not be scanned means the rename cannot claim to
	// cover every reference — a silent skip here is exactly the failure this
	// command exists to prevent (mirrors `ref validate`'s asymmetric-failure
	// design: partial is refused unless explicitly allowed).
	if len(unscanned) > 0 && !flagRefAllowPartial {
		return fmt.Errorf("%d dashboard(s) could not be scanned (%s), so this rename cannot claim to cover "+
			"every reference; nothing was renamed. Re-run with --allow-partial to rename in what could be read",
			len(unscanned), strings.Join(unscanned, "; "))
	}
	if len(unscanned) > 0 {
		slog.Warn("renaming with a partial dashboard scan (--allow-partial); references in unscanned dashboards are left unchanged",
			"unscanned", unscanned)
	}

	// References found in YAML-mode dashboards cannot be rewritten over the
	// API. A confirmed run that proceeded anyway would leave them dangling, so
	// it refuses up front — before the companion writes the config half.
	skippedHits, skippedLabels := unwritableHits(plans)
	if confirm && skippedHits > 0 && !flagRefAllowPartial {
		return yamlHitsError(skippedHits, skippedLabels, oldVal)
	}

	// --- Config side (companion) ---
	// A write that silently skips config files is exactly the failure this tool
	// exists to prevent, so a companion error aborts before anything is written.
	cfgResp, err := src.cc.RefReplace(ctx, oldVal, newVal, !confirm)
	if err != nil {
		return fmt.Errorf("companion ref replace (use `hactl dash replace` for dashboard-only renames): %w", err)
	}

	total := len(cfgResp.Changes)
	for _, p := range plans {
		total += len(p.changed)
	}
	if total == 0 {
		return emitEmptyList(w, fmt.Sprintf("%q not found in any config file or dashboard", oldVal))
	}

	if !flagJSON {
		if confirm {
			_, _ = fmt.Fprintf(w, "renamed %q → %q (%d occurrence(s))\n", oldVal, newVal, total)
		} else {
			_, _ = fmt.Fprintf(w, "dry-run: would rename %q → %q (%d occurrence(s))\n", oldVal, newVal, total)
		}
	}

	rows, saveErrs := buildReplaceRows(ctx, src.ws, cfgResp.Changes, plans, confirm)

	if err := renderReplaceReport(w, rows, oldVal, newVal, total, confirm); err != nil {
		return err
	}

	// The preview fails exactly where the confirmed run would (H-2): yaml-mode
	// hits made the confirmed run refuse above, so the dry run reports the
	// same refusal — after rendering the plan, so the caller sees what is stuck.
	if !confirm && skippedHits > 0 && !flagRefAllowPartial {
		return yamlHitsError(skippedHits, skippedLabels, oldVal)
	}
	// A confirmed run whose dashboard saves failed is a partial rename: the
	// config half is written, so say so loudly. Re-running is idempotent (the
	// literal is gone from config, so only dashboards get rewritten on retry).
	if confirm && len(saveErrs) > 0 {
		return fmt.Errorf("config files were updated, but %d dashboard(s) failed to save (%s); re-run "+
			"`hactl ref replace %q %q --confirm` (idempotent) once HA is reachable",
			len(saveErrs), strings.Join(saveErrs, "; "), oldVal, newVal)
	}
	return nil
}

// renderReplaceReport renders the merged rows as a table, a dry-run plan
// object under --json, or the plain table for a confirmed run.
func renderReplaceReport(w io.Writer, rows [][]string, oldVal, newVal string, total int, confirm bool) error {
	// Under --json a preview and a completed rename used to return structurally
	// identical documents, differing only in a `status` cell reading "pending"
	// rather than "applied". A caller must be able to tell a plan from a result
	// by looking at the object, not by remembering which flags it passed
	// (H-2), so the preview renders as a plan whose details carry the rows.
	if !confirm && flagJSON {
		return dryRun(fmt.Sprintf("rename %q to %q", oldVal, newVal)).
			with("from", oldVal).
			with("to", newVal).
			with("occurrences", total).
			with("rows", rows).
			render(w)
	}

	tbl := &format.Table{
		Headers: []string{"source", "location", "path", "status"},
		Rows:    rows,
	}
	if err := tbl.Render(w, format.RenderOpts{Full: true, JSON: flagJSON}); err != nil {
		return err
	}

	if !confirm && !flagJSON {
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
	}
	return nil
}

// unwritableHits counts the pending changes that sit in dashboards hactl
// cannot save back, and names those dashboards.
func unwritableHits(plans []dashReplacePlan) (int, []string) {
	var n int
	var labels []string
	for _, p := range plans {
		if !p.writable {
			n += len(p.changed)
			labels = append(labels, p.label)
		}
	}
	return n, labels
}

// yamlHitsError is the loud half of D-6: references in YAML-mode dashboards
// would survive a confirmed rename as dangling pointers, so the run refuses
// unless the caller explicitly accepts a partial rename.
func yamlHitsError(n int, labels []string, oldVal string) error {
	return fmt.Errorf("%d reference(s) to %q sit in YAML-mode dashboard(s) hactl cannot write (%s); a "+
		"confirmed rename would leave them dangling, so nothing was renamed. Rename them in the "+
		"dashboards' YAML files, or re-run with --allow-partial to rename everywhere else",
		n, oldVal, strings.Join(labels, ", "))
}

// planDashboardReplacements computes the pending rewrite for every dashboard,
// recording which are writable.
//
// The default dashboard is classified by attempting `lovelace/config` (D-6):
// a stored config joins the plan as writable; the auto-generated state holds
// no config and therefore no references — a complete answer of zero hits, not
// a failure; any other failure means the scan is partial and is returned in
// unscanned for the caller to surface. A listed dashboard that cannot be
// fetched is partial the same way. A YAML-mode default appears in the listed
// set itself (url_path "lovelace", see dashboardScanTargets) with
// mode "yaml", so it is scanned once and never writable.
func planDashboardReplacements(ctx context.Context, ws *haapi.WSClient, dashboards []haapi.LovelaceDashboard, oldVal, newVal string) (plans []dashReplacePlan, unscanned []string) {
	modeByPath := make(map[string]string, len(dashboards))
	for _, d := range dashboards {
		modeByPath[d.URLPath] = d.Mode
	}

	for _, t := range dashboardScanTargets(dashboards) {
		writable := modeByPath[t.urlPath] == "storage"
		if t.urlPath == "" {
			state, _, classifyErr := classifyDefaultDashboard(ctx, ws)
			switch state {
			case defaultDashAutoGenerated:
				// Nothing stored, so nothing to scan or rewrite: zero hits
				// here is the whole truth about this dashboard.
				continue
			case defaultDashUnclassifiable:
				unscanned = append(unscanned, fmt.Sprintf("%s: %v", t.label, classifyErr))
				continue
			case defaultDashStored:
				writable = true
			}
		}
		result, changed, replErr := dashReplaceOne(ctx, ws, t.urlPath, oldVal, newVal)
		if replErr != nil {
			unscanned = append(unscanned, fmt.Sprintf("%s: %v", t.label, replErr))
			continue
		}
		if len(changed) == 0 {
			continue
		}
		plans = append(plans, dashReplacePlan{
			label:    t.label,
			urlPath:  t.urlPath,
			result:   result,
			changed:  changed,
			writable: writable,
		})
	}
	return plans, unscanned
}

// buildReplaceRows renders the merged config+dashboard change rows, applying the
// dashboard writes when confirm is set. Config changes are already applied (or
// dry-run reported) by the companion before this is called. saveErrs carries
// the confirmed saves that failed, one entry per dashboard.
func buildReplaceRows(ctx context.Context, ws *haapi.WSClient, configChanges []companion.RefChange, plans []dashReplacePlan, confirm bool) (rows [][]string, saveErrs []string) {
	configStatus := "pending"
	if confirm {
		configStatus = "applied" // the companion already wrote the files
	}
	for _, c := range configChanges {
		rows = append(rows, []string{"config", c.Location, c.Path, configStatus})
	}
	for _, p := range plans {
		status := dashboardStatus(ctx, ws, p, confirm)
		if confirm && strings.HasPrefix(status, "error: ") {
			saveErrs = append(saveErrs, p.label+": "+strings.TrimPrefix(status, "error: "))
		}
		for _, path := range p.changed {
			rows = append(rows, []string{"dashboard", p.label, path.String(), status})
		}
	}
	return rows, saveErrs
}

// dashboardStatus applies a single dashboard's rewrite when confirmed and
// writable, returning the per-dashboard status string for the report.
func dashboardStatus(ctx context.Context, ws *haapi.WSClient, p dashReplacePlan, confirm bool) string {
	if !p.writable {
		// Reaching here without --allow-partial only happens on a dry run —
		// the confirmed run refuses these hits before writing anything.
		return "skipped: yaml-mode (rename it in the dashboard's YAML file)"
	}
	if !confirm {
		return "pending"
	}
	out, err := json.Marshal(p.result)
	if err != nil {
		return "error: " + err.Error()
	}
	// Snapshot the current config before overwriting (asymmetric with the YAML
	// side otherwise, which always backs up first).
	snapshotDashboardBeforeSave(ctx, ws, p.urlPath)
	if err := ws.DashboardConfigSave(ctx, p.urlPath, out); err != nil {
		return "error: " + err.Error()
	}
	return "saved"
}

// --- ref validate ---

// entityIDShapeRe matches the entity_id shape domain.object_id. It is
// deliberately identical to the companion's _ENTITY_ID_RE so both reference
// sources classify leaves the same way. Note the shape also matches service
// names (light.turn_on) — the key allowlists below, not this regex, exclude those.
var entityIDShapeRe = regexp.MustCompile(`^[a-z_]+\.[a-z0-9_]+$`)

func isEntityIDShaped(s string) bool { return entityIDShapeRe.MatchString(s) }

// configEntityKeys / dashEntityKeys are ALLOWLISTS of the mapping keys whose
// entity_id-shaped values are real entity references. An allowlist (not a
// denylist) is deliberate: for a safety sweep, false positives erode trust worse
// than a missed exotic position, so favor precision. Services drop out for free
// — `service`/`action`/`service_template`/`tap_action.service` are simply absent.
// These are conservative by design and miss template-embedded entities and
// non-standard custom-card keys; extend them as real configs reveal gaps.
var (
	configEntityKeys = map[string]bool{"entity_id": true, "entity": true}
	dashEntityKeys   = map[string]bool{
		"entity": true, "entity_id": true, "camera_image": true, "entities": true, "badges": true,
	}
)

// danglingRef is one entity reference found in a known entity position whose
// value is not in the live set.
type danglingRef struct {
	source   string // "config" | "dashboard"
	location string
	path     string
	entity   string
}

// danglingRefsError makes `ref validate --exit-code` exit non-zero when
// references are found, without treating the finding as a command failure
// (the report is still printed normally before this is returned).
type danglingRefsError struct{ n int }

func (e *danglingRefsError) Error() string {
	return fmt.Sprintf("%d dangling reference(s) found", e.n)
}
func (e *danglingRefsError) ExitCode() int { return 1 }

// configScanGateError decides whether a failed config-file scan must fail the
// command. Interactively a partial validate (dashboards only) still has value,
// so the failure is a warning. But under --exit-code (CI/pre-commit gating) a
// silently-skipped config half makes the gate vacuous — a companion outage would
// let dangling config references pass green — so it is fatal unless the caller
// opts into a partial gate with --allow-partial (mirroring the live-set flag).
func configScanGateError(entErr error, exitCode, allowPartial bool) error {
	if entErr == nil || !exitCode || allowPartial {
		return nil
	}
	return fmt.Errorf("config files could not be scanned, so --exit-code cannot certify they are free of "+
		"dangling references: %w; re-run with --allow-partial to gate on dashboards alone", entErr)
}

func runRefValidate(ctx context.Context, w io.Writer) error {
	src, err := connectRefSources(ctx)
	if err != nil {
		return err
	}
	defer src.close()

	live, err := liveEntitySet(ctx, src)
	if err != nil {
		return err
	}

	var refs []danglingRef

	// Config files (companion). A companion failure is a warning, not fatal —
	// a partial validate over dashboards alone still has value.
	if resp, entErr := src.cc.RefEntities(ctx); entErr != nil {
		if gateErr := configScanGateError(entErr, flagRefExitCode, flagRefAllowPartial); gateErr != nil {
			return gateErr
		}
		slog.Warn("companion config entity scan failed; config files were not validated", "error", entErr)
	} else {
		for _, e := range resp.Entities {
			if configEntityKeys[e.Key] {
				refs = append(refs, danglingRef{"config", e.Location, e.Path, e.MatchedValue})
			}
		}
	}

	// Dashboards (WS).
	dashboards, err := src.ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}
	refs = append(refs, collectDashboardEntityRefs(ctx, src.ws, dashboardScanTargets(dashboards))...)

	var dangling []danglingRef
	for _, r := range refs {
		if !live[r.entity] {
			dangling = append(dangling, r)
		}
	}
	dangling = dedupeSortRefs(dangling)

	if len(dangling) == 0 {
		if flagJSON {
			return renderValidateTable(w, nil)
		}
		_, _ = fmt.Fprintln(w, "no dangling references found")
		_, _ = fmt.Fprintln(w, "(note: entities embedded in templates like \"{{ states('sensor.x') }}\" are not checked)")
		return nil
	}

	if err := renderValidateTable(w, dangling); err != nil {
		return err
	}

	uniq := uniqueDanglingEntities(dangling)
	if !flagJSON {
		_, _ = fmt.Fprintf(w, "\n%d dangling reference(s) to %d entity(ies): %s\n",
			len(dangling), len(uniq), strings.Join(uniq, ", "))
		_, _ = fmt.Fprintln(w, "rename each with `hactl ref replace <old> <new>`")
	}

	if flagRefExitCode {
		return &danglingRefsError{len(uniq)}
	}
	return nil
}

// liveEntitySet builds the set of entity_ids that currently exist, as the union
// of the entity registry (catches disabled and currently-unloaded entities) and
// /api/states (catches state-only entities with no registry entry — sun.sun,
// zone.home, weather.*, template sensors). The two half-failures are NOT
// symmetric: states alone is a near-complete live set, but the registry alone
// omits every state-only entity and would flag them all as dangling — so a
// registry-only fallback is refused unless --allow-partial is set.
func liveEntitySet(ctx context.Context, src *refSources) (map[string]bool, error) {
	live := make(map[string]bool)

	reg, regErr := src.ws.EntityRegistryList(ctx)
	if regErr == nil {
		for _, e := range reg {
			if e.EntityID != "" {
				live[e.EntityID] = true
			}
		}
	}
	states, statesErr := fetchStateEntityIDs(ctx, src.rest)
	if statesErr == nil {
		for _, id := range states {
			live[id] = true
		}
	}

	switch {
	case regErr != nil && statesErr != nil:
		return nil, fmt.Errorf("cannot validate: no live entity set available: %w",
			errors.Join(fmt.Errorf("registry: %w", regErr), fmt.Errorf("states: %w", statesErr)))
	case statesErr != nil:
		if !flagRefAllowPartial {
			return nil, fmt.Errorf("cannot fetch live states: %w; the entity registry alone omits state-only "+
				"entities (sun.sun, zone.home, weather.*, template sensors) and would report them all as "+
				"dangling. Re-run with --allow-partial to validate against the registry anyway", statesErr)
		}
		slog.Warn("validating against the entity registry only; state-only entities may be falsely reported as dangling", "error", statesErr)
	case regErr != nil:
		slog.Warn("entity registry unavailable; validating against live states only", "error", regErr)
	}
	return live, nil
}

// fetchStateEntityIDs pulls every entity_id from /api/states via REST.
func fetchStateEntityIDs(ctx context.Context, rest *haapi.Client) ([]string, error) {
	data, err := rest.GetStates(ctx)
	if err != nil {
		return nil, err
	}
	var states []entityState
	if err := json.Unmarshal(data, &states); err != nil {
		return nil, fmt.Errorf("parsing states: %w", err)
	}
	if err := degeneracy.Check("/api/states", &states); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(states))
	for _, s := range states {
		ids = append(ids, s.EntityID)
	}
	return ids, nil
}

// collectDashboardEntityRefs walks each dashboard for entity_id-shaped string
// leaves in a known entity position (per dashEntityKeys). Unlike scanDashboards
// it matches by shape+position rather than an exact target. Dashboards that
// cannot be fetched or parsed are skipped rather than aborting the sweep.
func collectDashboardEntityRefs(ctx context.Context, ws *haapi.WSClient, targets []dashScanTarget) []danglingRef {
	var refs []danglingRef
	for _, t := range targets {
		raw, rawErr := ws.DashboardConfigRaw(ctx, t.urlPath)
		if rawErr != nil {
			slog.Debug("could not fetch dashboard config", "dashboard", t.label, "error", rawErr)
			continue
		}
		var root any
		if json.Unmarshal(raw, &root) != nil {
			continue
		}
		jsonwalk.Walk(root, func(p jsonwalk.Path, v any) {
			s, ok := v.(string)
			if !ok || !isEntityIDShaped(s) || !dashEntityKeys[p.TerminalKey()] {
				return
			}
			refs = append(refs, danglingRef{"dashboard", t.label, p.String(), s})
		})
	}
	return refs
}

// dedupeSortRefs removes exact duplicate rows and sorts deterministically.
func dedupeSortRefs(refs []danglingRef) []danglingRef {
	seen := make(map[danglingRef]bool, len(refs))
	out := make([]danglingRef, 0, len(refs))
	for _, r := range refs {
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.source != b.source:
			return a.source < b.source
		case a.location != b.location:
			return a.location < b.location
		case a.path != b.path:
			return a.path < b.path
		default:
			return a.entity < b.entity
		}
	})
	return out
}

// uniqueDanglingEntities returns the distinct dangling entity_ids, sorted — the
// list a user renames one at a time with `ref replace`.
func uniqueDanglingEntities(refs []danglingRef) []string {
	seen := make(map[string]bool, len(refs))
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if !seen[r.entity] {
			seen[r.entity] = true
			out = append(out, r.entity)
		}
	}
	sort.Strings(out)
	return out
}

// renderValidateTable renders the dangling-reference rows (or an empty table in
// JSON mode) with the same options as the other ref commands.
func renderValidateTable(w io.Writer, refs []danglingRef) error {
	tbl := &format.Table{
		Headers: []string{"source", "location", "path", "entity"},
		Rows:    make([][]string, len(refs)),
	}
	for i, r := range refs {
		tbl.Rows[i] = []string{r.source, r.location, r.path, r.entity}
	}
	return tbl.Render(w, format.RenderOpts{Top: flagTop, Full: true, JSON: flagJSON})
}
