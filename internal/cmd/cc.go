package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var ccCmd = family(&cobra.Command{
	Use:   "cc",
	Short: "Inspect custom components",
	Long:  "List and inspect custom (third-party) components installed in HA.",
})

var ccLsCmd = &cobra.Command{
	Use:   "ls",
	Args:  takesNone(),
	Short: "List custom components",
	Long:  "Show installed custom components with version and domain.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCCLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var ccShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show custom component details",
	Long:  "Display details for a specific custom component.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCCShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var ccLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show logs for a custom component",
	Long:  "Display error log entries related to a specific custom component.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCCLogs(cmd.Context(), cmd.OutOrStdout(), args[0], cmd.Flags().Changed("since"))
	},
}

var flagCCLogsUnique bool

func init() {
	ccLogsCmd.Flags().BoolVar(&flagCCLogsUnique, "unique", false, "deduplicate identical log messages")
	ccCmd.AddCommand(ccLsCmd, ccShowCmd, ccLogsCmd)
	rootCmd.AddCommand(ccCmd)
}

// ccInfo holds info about a custom component.
//
// NOTE: HA's manifest/list WS response also carries documentation,
// dependencies, iot_class, codeowners, and issue_tracker for each
// integration, but haapi.IntegrationManifest (internal/haapi/websocket.go)
// does not currently decode them, so `cc show` cannot report those fields
// honestly yet. That struct is outside this fix's file set — extending it is
// a follow-up, not something faked here.
type ccInfo struct {
	Domain       string
	Name         string
	Version      string
	Requirements []string
}

func runCCLs(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	components, err := fetchCustomComponents(ctx, cfg, client)
	if err != nil {
		return err
	}

	if len(components) == 0 {
		return emitEmptyList(w, "no custom components")
	}

	tbl := &format.Table{
		Headers: []string{"domain", "version"},
		Rows:    make([][]string, len(components)),
	}
	for i, cc := range components {
		tbl.Rows[i] = []string{
			cc.Domain,
			cc.Version,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func runCCShow(ctx context.Context, w io.Writer, name string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	components, err := fetchCustomComponents(ctx, cfg, client)
	if err != nil {
		return err
	}

	found, err := findCustomComponent(components, name)
	if err != nil {
		return err
	}

	owned, err := componentEntityIDs(ctx, cfg, client, found.Domain)
	if err != nil {
		return err
	}

	if flagJSON {
		out := map[string]any{
			"domain":              found.Domain,
			"name":                found.Name,
			"version":             found.Version,
			"is_built_in":         false,
			"entity_count":        len(owned.Live),
			"entity_ids":          owned.Live,
			"disabled_count":      len(owned.Disabled),
			"disabled_entity_ids": owned.Disabled,
			"registry_count":      owned.Registry,
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	_, _ = fmt.Fprintf(w, "domain:   %s\n", found.Domain)
	if found.Name != "" {
		_, _ = fmt.Fprintf(w, "name:     %s\n", found.Name)
	}
	_, _ = fmt.Fprintf(w, "version:  %s\n", found.Version)
	// The registry total is stated only when it disagrees with the live count,
	// because when they agree the number already reconciles and a second one
	// would be noise. When they disagree, the difference is the whole point:
	// `homematicip_local` owns 402 entities and 159 of them have a state.
	if owned.Registry == len(owned.Live) {
		_, _ = fmt.Fprintf(w, "entities: %d\n", len(owned.Live))
	} else {
		_, _ = fmt.Fprintf(w, "entities: %d (registry: %d, of which %d disabled)\n",
			len(owned.Live), owned.Registry, len(owned.Disabled))
	}

	if flagFull && len(owned.Live) > 0 {
		_, _ = fmt.Fprintln(w, "entity_ids:")
		for _, id := range owned.Live {
			_, _ = fmt.Fprintf(w, "  %s\n", id)
		}
	}
	if flagFull && len(owned.Disabled) > 0 {
		_, _ = fmt.Fprintln(w, "disabled_entity_ids:")
		for _, id := range owned.Disabled {
			_, _ = fmt.Fprintf(w, "  %s\n", id)
		}
	}

	return nil
}

func runCCLogs(ctx context.Context, w io.Writer, name string, sinceSet bool) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	// The name is resolved before the log is read, not used as a bare filter
	// string. `cc logs totally_bogus_xyz` answered "no log entries for
	// totally_bogus_xyz" at exit 0 — byte for byte the answer a real, installed,
	// error-free component gets — while the sibling `cc show` refuses the same
	// name at exit 1. A typo was indistinguishable from a quiet component
	// (finding #18), which is H-22's rule at the boundary and D-19 in
	// docs/decisions.md: within a family that resolves a name, every member
	// resolves it.
	client := haapi.New(cfg.URL, cfg.Token)
	components, err := fetchCustomComponents(ctx, cfg, client)
	if err != nil {
		return err
	}
	found, err := findCustomComponent(components, name)
	if err != nil {
		return err
	}

	entries, err := fetchLogEntries(ctx, cfg)
	if err != nil {
		return fmt.Errorf("fetching logs: %w", err)
	}

	entries = analyze.FilterByComponent(entries, found.Domain)

	if entries, err = applyLogSince(entries, sinceSet); err != nil {
		return err
	}

	if len(entries) == 0 {
		return emitEmptyList(w, "no log entries for "+found.Domain)
	}

	if flagCCLogsUnique {
		return renderDedupedLogs(w, cfg, entries)
	}

	// The same renderer `log` uses, not a third one. `cc logs <name>` had its
	// own table with its own schema — no `id` column, and the full logger name
	// where both siblings show its last segment — so the default view was the
	// one place in the family where a shortened message could not be traced
	// back to `log show <id>`, the route the manual prescribes (finding #17).
	// Three renderers for one record type is the condition; a fourth would
	// inherit it.
	return renderLogEntries(w, cfg, entries)
}

// findCustomComponent resolves a component name against what HA reports.
//
// One function for both `cc show` and `cc logs`, because two of them is how the
// family came to have two answers for an unknown name. The message names the
// command that lists the valid ones (D-18: an error names what the reader needs
// to do next); the instance is appended by the root error printer.
func findCustomComponent(components []ccInfo, name string) (*ccInfo, error) {
	for i := range components {
		if components[i].Domain == name {
			return &components[i], nil
		}
	}
	return nil, fmt.Errorf("custom component %q not found among the %d installed "+
		"(hactl cc ls lists them)", name, len(components))
}

// fetchCustomComponents returns the custom (non-built-in) integrations HA
// itself reports via manifest/list — the only source that carries
// is_built_in, and therefore the only honest definition of "custom
// component" (defect #4 / H-11).
//
// Previously, "Method 1" treated ANY update.* entity carrying a title and
// installed_version as a custom component with no is_built_in check at all.
// HA's own built-in integrations (`demo` among them) ship update.* entities
// shaped exactly like a HACS component update, so that heuristic fabricated
// rows for entirely built-in integrations — on a real install, update.*
// covers HA Core, the OS, and every add-on.
//
// The update.* entity data is still useful — HACS keeps its installed_version
// fresher than the static manifest.json version — but now it can only ENRICH
// a domain manifest/list has already confirmed non-built-in; it can never
// nominate one on its own.
func fetchCustomComponents(ctx context.Context, cfg *config.Config, client *haapi.Client) ([]ccInfo, error) {
	var manifests []haapi.IntegrationManifest
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if wsErr := ws.Connect(ctx); wsErr == nil {
		m, mErr := ws.IntegrationManifestList(ctx)
		_ = ws.Close()
		if mErr == nil {
			manifests = m
		} else {
			slog.Debug("manifest/list unavailable", "error", mErr)
		}
	} else {
		slog.Debug("websocket unavailable for manifest/list", "error", wsErr)
	}

	components := make(map[string]*ccInfo)
	var order []string
	for _, m := range manifests {
		if m.IsBuiltIn {
			continue
		}
		if _, dup := components[m.Domain]; dup {
			continue
		}
		v := m.Version
		if v == "" {
			v = "n/a"
		}
		components[m.Domain] = &ccInfo{Domain: m.Domain, Name: m.Name, Version: v}
		order = append(order, m.Domain)
	}

	if len(components) > 0 {
		if err := enrichVersionsFromUpdateEntities(ctx, client, components); err != nil {
			return nil, err
		}
	}

	sort.Strings(order)
	result := make([]ccInfo, 0, len(order))
	for _, d := range order {
		result = append(result, *components[d])
	}
	return result, nil
}

// enrichVersionsFromUpdateEntities overwrites a component's version with the
// installed_version of its matching update.* entity, when HA reports one. It
// can only adjust a domain manifest/list already confirmed — never add one.
func enrichVersionsFromUpdateEntities(
	ctx context.Context, client *haapi.Client, components map[string]*ccInfo,
) error {
	statesData, err := client.GetStates(ctx)
	if err != nil {
		return fmt.Errorf("fetching states: %w", err)
	}
	// entityState, not an anonymous struct: an anonymous type cannot carry an
	// Identity, so a /api/states that stopped answering entity_id here would
	// silently enrich nothing and report every version as unchanged.
	var states []entityState
	if err := json.Unmarshal(statesData, &states); err != nil {
		return fmt.Errorf("parsing states: %w", err)
	}
	if err := degeneracy.Check("/api/states", &states); err != nil {
		return err
	}
	for _, s := range states {
		if !strings.HasPrefix(s.EntityID, "update.") {
			continue
		}
		info, ok := components[strings.TrimPrefix(s.EntityID, "update.")]
		if !ok {
			continue
		}
		if v, _ := s.Attributes["installed_version"].(string); v != "" {
			info.Version = v
		}
	}
	return nil
}

// componentEntities is what the registry attributes to one integration, split
// by whether Home Assistant currently holds a state for it.
//
// The split is reported rather than applied, because it is the difference
// between two true answers to different questions and a caller cannot tell
// which one they got from a single number (H-11: a count reconciles with the
// count its source reported).
type componentEntities struct {
	Live     []string // in the registry AND in /api/states
	Disabled []string // in the registry, disabled, so never in /api/states
	Registry int      // every row the registry attributes to the domain
}

// componentEntityIDs returns the entities an integration owns.
//
// The join is the entity registry's `platform` field, which names the
// integration that created the entity. It used to be a prefix match on the
// entity_id — `strings.HasPrefix(id, domain+".")` — which reads plausibly and
// is wrong for almost every integration there is: an entity_id's first segment
// is its ENTITY domain (`sensor`, `light`, `binary_sensor`), not the
// integration that supplied it. `powercalc` publishes `sensor.*`, `pyscript`
// publishes `pyscript.*` only for its own service entities, and so on, so the
// count came back 0 for virtually every real component on a live instance
// (live-fire 2026-07-30, P2 #16) while the component plainly had entities.
//
// A registry lookup is authoritative for attribution and is the only source
// that carries it; `/api/states` does not. Entities absent from the registry
// (a platform that never registers a unique_id) therefore cannot be attributed
// at all, and are not guessed at — an undercount that names its reason beats a
// count assembled from a rule that does not hold.
//
// The live-state filter used to be applied silently, with the reason given as
// "so a stale registry row for a removed device does not inflate the answer".
// Running the fix against a real instance is what showed that is not what it
// does: of 5524 registry rows there, every single row without a live state is
// a DISABLED one — an entity the integration owns and somebody turned off, not
// a leftover. It cost `homematicip_local` 243 of its 402 entities and
// `dwd_weather` 56 of 75, with nothing in the output naming the gap. Both
// numbers are now reported.
func componentEntityIDs(ctx context.Context, cfg *config.Config, client *haapi.Client, domain string) (componentEntities, error) {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return componentEntities{}, fmt.Errorf("entity attribution needs the entity registry: %w", err)
	}
	entries, err := ws.EntityRegistryList(ctx)
	_ = ws.Close()
	if err != nil {
		return componentEntities{}, fmt.Errorf("listing the entity registry: %w", err)
	}

	live := map[string]bool{}
	if states, statesErr := client.GetStates(ctx); statesErr == nil {
		var allStates []entityState
		if jsonErr := json.Unmarshal(states, &allStates); jsonErr == nil {
			if degErr := degeneracy.Check("/api/states", &allStates); degErr != nil {
				return componentEntities{}, degErr
			}
			for _, s := range allStates {
				live[s.EntityID] = true
			}
		}
	}

	var owned componentEntities
	for _, e := range entries {
		if e.Platform != domain {
			continue
		}
		owned.Registry++
		switch {
		case len(live) == 0 || live[e.EntityID]:
			// An empty state set means /api/states could not be read at all;
			// every registry row then counts as live rather than none, which is
			// the same posture the filter has always had.
			owned.Live = append(owned.Live, e.EntityID)
		case e.DisabledBy != "":
			owned.Disabled = append(owned.Disabled, e.EntityID)
		}
		// A row that is neither live nor disabled falls through deliberately:
		// it is counted in Registry and appears in no list, so `entity_count +
		// disabled_count < registry_count` is how a stale row — the thing the
		// filter was originally described as removing — becomes visible instead
		// of being quietly subtracted.
	}
	sort.Strings(owned.Live)
	sort.Strings(owned.Disabled)
	return owned, nil
}
