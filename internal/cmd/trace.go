package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/pkg/ids"
)

var traceCmd = &cobra.Command{
	Use:   "trace",
	Short: "Inspect automation traces",
	Long:  "View condensed or full trace details for automation and script runs.",
}

var traceShowCmd = &cobra.Command{
	Use:   "show <trace-id>",
	Short: "Show trace details",
	Long:  "Display a condensed or full trace. Use stable IDs (e.g. trc:a7) or run IDs.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTraceShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	traceCmd.AddCommand(traceShowCmd)
	rootCmd.AddCommand(traceCmd)
}

func runTraceShow(ctx context.Context, w io.Writer, traceID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	// Resolve stable ID to domain/item_id/run_id
	idsPath := filepath.Join(cfg.Dir, "cache", "ids.json")
	reg := ids.NewRegistry(idsPath)
	if loadErr := reg.Load(); loadErr != nil {
		slog.Warn("could not load ids registry", "error", loadErr)
	}

	domain, itemID, runID, resolveErr := resolveTraceID(reg, traceID)
	if resolveErr != nil {
		return resolveErr
	}

	// H-9: HA keys automation traces by the CONFIG id, but the only automation
	// identifier the CLI ever displays is the entity object id, so a
	// hand-written `automation.<object_id>/<run_id>` reference — the exact form
	// this command's usage string documents — would otherwise never resolve.
	// Translate object id -> config id before asking HA. Scripts have no such
	// split and are left untouched.
	if domain == "automation" {
		if resolved, ok := automationConfigIDFor(ctx, cfg, itemID); ok {
			itemID = resolved
		}
	}

	// Fetch full trace via WebSocket
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connectErr := ws.Connect(ctx); connectErr != nil {
		return fmt.Errorf("websocket connect: %w", connectErr)
	}
	defer func() { _ = ws.Close() }()

	rawJSON, err := ws.TraceGet(ctx, domain, itemID, runID)
	if err != nil {
		return fmt.Errorf("fetching trace: %w", err)
	}

	if flagFull {
		// Full: pretty-print the raw JSON
		var pretty json.RawMessage
		if jsonErr := json.Unmarshal(rawJSON, &pretty); jsonErr != nil {
			// Fallback: write raw
			_, _ = w.Write(rawJSON)
			_, _ = fmt.Fprintln(w)
			return nil
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(pretty)
	}

	// Condensed: parse and render
	var raw analyze.RawTrace
	if jsonErr := json.Unmarshal(rawJSON, &raw); jsonErr != nil {
		return fmt.Errorf("parsing trace: %w", jsonErr)
	}

	condensed := analyze.Condense(&raw)

	// analyze.Condense records the identity HA reported, which for an
	// automation is "<domain>.<config id>". That string is correct as a record
	// of the trace but is NOT an address: feeding it to `ent show` or
	// `auto show` fails, because every other command speaks entity_id. A
	// command must not display an identifier it cannot itself consume, so
	// translate to the entity_id for display when one exists.
	if domain == "automation" {
		if entityID, ok := automationEntityIDFor(ctx, cfg, itemID); ok {
			condensed.AutoID = entityID
		}
	}

	// H-10: `--json` on the condensed view emits the same steps the text view
	// renders, structured. `--full` keeps its documented meaning of dumping
	// HA's raw trace verbatim, so `--full` wins when both are given.
	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(condensed)
	}

	_, _ = fmt.Fprint(w, analyze.FormatCondensed(condensed))
	return nil
}

// resolveTraceID resolves a stable ID (trc:a7) or composite key to domain, item_id, run_id.
func resolveTraceID(reg *ids.Registry, traceID string) (domain, itemID, runID string, err error) {
	// Try as stable ID first (e.g. "trc:a7")
	if strings.HasPrefix(traceID, "trc:") {
		key, ok := reg.Resolve(traceID)
		if !ok {
			return "", "", "", fmt.Errorf("unknown trace ID: %s (not in ids registry)", traceID)
		}
		// key format: "automation.item_id/run_id"
		return parseTraceKey(key)
	}

	// Try as direct key: "automation.item_id/run_id"
	if strings.Contains(traceID, "/") {
		return parseTraceKey(traceID)
	}

	return "", "", "", fmt.Errorf("invalid trace ID format: %s (expected trc:<hash> or domain.item_id/run_id)", traceID)
}

// automationConfigIDFor maps an automation reference (object id, entity_id,
// config id or alias) to the config id HA files its traces under. Returns
// (ref, false) when no live automation matches — a genuinely unknown reference
// is passed through unchanged so HA's own error surfaces rather than a
// silently-rewritten lookup.
func automationConfigIDFor(ctx context.Context, cfg *config.Config, ref string) (string, bool) {
	client := haapi.New(cfg.URL, cfg.Token)
	a, ok := resolveAutomation(ctx, client, ref)
	if !ok || a.Attributes.ID == "" {
		return ref, false
	}
	return a.Attributes.ID, true
}

// automationEntityIDFor is automationConfigIDFor's inverse: it maps the config
// id HA files traces under back to the entity_id every other command speaks.
func automationEntityIDFor(ctx context.Context, cfg *config.Config, ref string) (string, bool) {
	client := haapi.New(cfg.URL, cfg.Token)
	a, ok := resolveAutomation(ctx, client, ref)
	if !ok || a.EntityID == "" {
		return ref, false
	}
	return a.EntityID, true
}

func parseTraceKey(key string) (string, string, string, error) {
	entityID, runID, found := strings.Cut(key, "/")
	if !found {
		return "", "", "", fmt.Errorf("invalid trace key: %s (expected domain.item_id/run_id)", key)
	}

	domain, itemID, found := strings.Cut(entityID, ".")
	if !found {
		return "", "", "", fmt.Errorf("invalid entity ID in trace key: %s", entityID)
	}

	return domain, itemID, runID, nil
}
