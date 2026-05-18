package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
		return runCCLogs(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var flagCCLogsUnique bool

func init() {
	ccLogsCmd.Flags().BoolVar(&flagCCLogsUnique, "unique", false, "deduplicate identical log messages")
	ccCmd.AddCommand(ccLsCmd, ccShowCmd, ccLogsCmd)
	rootCmd.AddCommand(ccCmd)
}

// ccInfo holds info about a custom component.
type ccInfo struct {
	Domain       string
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
		_, _ = fmt.Fprintln(w, "no custom components")
		return nil
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

	_, _ = fmt.Fprintf(w, "domain:  %s\n", found.Domain)
	_, _ = fmt.Fprintf(w, "version: %s\n", found.Version)

	// Show related entities
	states, statesErr := client.GetStates(ctx)
	if statesErr == nil {
		var allStates []entityState
		if jsonErr := json.Unmarshal(states, &allStates); jsonErr == nil {
			count := 0
			for _, s := range allStates {
				if strings.HasPrefix(s.EntityID, found.Domain+".") {
					count++
				}
			}
			if count > 0 {
				_, _ = fmt.Fprintf(w, "entities: %d\n", count)
			}
		}
	}

	return nil
}

func runCCLogs(ctx context.Context, w io.Writer, name string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	entries, err := fetchLogEntries(ctx, cfg)
	if err != nil {
		return fmt.Errorf("fetching logs: %w", err)
	}

	entries = analyze.FilterByComponent(entries, name)

	if len(entries) == 0 {
		_, _ = fmt.Fprintf(w, "no log entries for %s\n", name)
		return nil
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

// fetchCustomComponents gets custom_components from HA config.
func fetchCustomComponents(ctx context.Context, cfg *config.Config, client *haapi.Client) ([]ccInfo, error) {
	// Method 1: HACS update.* entities (provides version info)
	statesData, err := client.GetStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching states: %w", err)
	}

	var states []struct {
		Attributes map[string]any `json:"attributes"`
		EntityID   string         `json:"entity_id"`
		State      string         `json:"state"`
	}
	if err := json.Unmarshal(statesData, &states); err != nil {
		return nil, fmt.Errorf("parsing states: %w", err)
	}

	var components []ccInfo
	seen := make(map[string]bool)
	for _, s := range states {
		if !strings.HasPrefix(s.EntityID, "update.") {
			continue
		}
		title, _ := s.Attributes["title"].(string)
		installedVersion, _ := s.Attributes["installed_version"].(string)
		if title == "" || installedVersion == "" {
			continue
		}
		domain := strings.TrimPrefix(s.EntityID, "update.")
		if seen[domain] {
			continue
		}
		seen[domain] = true
		components = append(components, ccInfo{
			Domain:  domain,
			Version: installedVersion,
		})
	}

	// Method 2: WS manifest/list to find non-HACS custom components.
	// Custom integrations loaded from custom_components/ (e.g. via volume mount)
	// have is_built_in=false in their manifest but may lack HACS update entities.
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if wsErr := ws.Connect(ctx); wsErr == nil {
		manifests, mErr := ws.IntegrationManifestList(ctx)
		_ = ws.Close()
		if mErr == nil {
			for _, m := range manifests {
				if m.IsBuiltIn || seen[m.Domain] {
					continue
				}
				seen[m.Domain] = true
				v := m.Version
				if v == "" {
					v = "n/a"
				}
				components = append(components, ccInfo{
					Domain:  m.Domain,
					Version: v,
				})
			}
		}
	}

	return components, nil
}
