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
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var ccCmd = &cobra.Command{
	Use:   "cc",
	Short: "Inspect custom components",
	Long:  "List and inspect custom (third-party) components installed in HA.",
}

var ccLsCmd = &cobra.Command{
	Use:   "ls",
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
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCCShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var ccLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Show logs for a custom component",
	Long:  "Display error log entries related to a specific custom component.",
	Args:  cobra.ExactArgs(1),
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

	var found *ccInfo
	for i, cc := range components {
		if cc.Domain == name {
			found = &components[i]
			break
		}
	}

	if found == nil {
		return fmt.Errorf("custom component %q not found", name)
	}

	// Honest entity count + (with --full) the actual entity_ids — states-based
	// so it counts everything live, not just what the entity registry knows.
	var entityIDs []string
	states, statesErr := client.GetStates(ctx)
	if statesErr == nil {
		var allStates []entityState
		if jsonErr := json.Unmarshal(states, &allStates); jsonErr == nil {
			for _, s := range allStates {
				if strings.HasPrefix(s.EntityID, found.Domain+".") {
					entityIDs = append(entityIDs, s.EntityID)
				}
			}
		}
	}
	sort.Strings(entityIDs)

	if flagJSON {
		out := map[string]any{
			"domain":       found.Domain,
			"name":         found.Name,
			"version":      found.Version,
			"is_built_in":  false,
			"entity_count": len(entityIDs),
			"entity_ids":   entityIDs,
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
	_, _ = fmt.Fprintf(w, "entities: %d\n", len(entityIDs))

	if flagFull && len(entityIDs) > 0 {
		_, _ = fmt.Fprintln(w, "entity_ids:")
		for _, id := range entityIDs {
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

	entries, err := fetchLogEntries(ctx, cfg)
	if err != nil {
		return fmt.Errorf("fetching logs: %w", err)
	}

	entries = analyze.FilterByComponent(entries, name)

	if entries, err = applyLogSince(entries, sinceSet); err != nil {
		return err
	}

	if len(entries) == 0 {
		return emitEmptyList(w, "no log entries for "+name)
	}

	if flagCCLogsUnique {
		return renderDedupedLogs(w, entries)
	}

	return renderLogEntriesSimple(w, entries)
}

func renderLogEntriesSimple(w io.Writer, entries []analyze.LogEntry) error {
	tbl := &format.Table{
		Headers: []string{"time", "level", "component", "message"},
		Rows:    make([][]string, len(entries)),
	}
	for i, e := range entries {
		msg := e.Message
		if len(msg) > 60 {
			msg = msg[:57] + "..."
		}
		tbl.Rows[i] = []string{
			analyze.FormatShortTimestamp(e.Timestamp),
			e.Level,
			e.Component,
			msg,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
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

	// Enrich version from a matching update.* entity's installed_version, when
	// present. Can only adjust a domain already confirmed above — never add one.
	if len(components) > 0 {
		statesData, err := client.GetStates(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetching states: %w", err)
		}
		var states []struct {
			Attributes map[string]any `json:"attributes"`
			EntityID   string         `json:"entity_id"`
		}
		if err := json.Unmarshal(statesData, &states); err != nil {
			return nil, fmt.Errorf("parsing states: %w", err)
		}
		for _, s := range states {
			if !strings.HasPrefix(s.EntityID, "update.") {
				continue
			}
			domain := strings.TrimPrefix(s.EntityID, "update.")
			info, ok := components[domain]
			if !ok {
				continue
			}
			if v, _ := s.Attributes["installed_version"].(string); v != "" {
				info.Version = v
			}
		}
	}

	sort.Strings(order)
	result := make([]ccInfo, 0, len(order))
	for _, d := range order {
		result = append(result, *components[d])
	}
	return result, nil
}
