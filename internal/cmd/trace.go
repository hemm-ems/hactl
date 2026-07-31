package cmd

import (
	"context"
	"encoding/json"
	"errors"
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

var traceCmd = family(&cobra.Command{
	Use:   "trace",
	Short: "Inspect automation traces",
	Long:  "View condensed or full trace details for automation and script runs.",
})

var traceShowCmd = &cobra.Command{
	Use:   "show <trace-id|automation>",
	Short: "Show trace details",
	Long: "Display a condensed or full trace.\n\n" +
		"Takes a stable trace ID (trc:a7), a composite run key\n" +
		"(automation.<item_id>/<run_id>), or ANY identifier that names an automation —\n" +
		"its config id, alias, entity_id or object id — in which case the automation's\n" +
		"most recent stored run is shown.",
	Args: takes(1),
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
	if resolveErr != nil && !errors.Is(resolveErr, errNotATraceReference) {
		return resolveErr
	}

	// H-9: HA keys automation traces by the CONFIG id, but the only automation
	// identifier the CLI ever displays is the entity object id, so a
	// hand-written `automation.<object_id>/<run_id>` reference — the exact form
	// this command's usage string documents — would otherwise never resolve.
	// Translate object id -> config id before asking HA. Scripts have no such
	// split and are left untouched.
	if domain == "automation" {
		resolved, ok, configErr := automationConfigIDFor(ctx, cfg, itemID)
		if configErr != nil {
			return configErr
		}
		if ok {
			itemID = resolved
		}
	}

	// Fetch full trace via WebSocket
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connectErr := ws.Connect(ctx); connectErr != nil {
		return fmt.Errorf("websocket connect: %w", connectErr)
	}
	defer func() { _ = ws.Close() }()

	// Not a trace address at all: the argument names an AUTOMATION, and the
	// answer is its most recent stored run (D-1, H-17).
	if resolveErr != nil {
		domain, itemID, runID, err = latestAutomationRun(ctx, cfg, ws, traceID)
		if err != nil {
			return err
		}
	}

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
		entityID, ok, resolveErr := automationEntityIDFor(ctx, cfg, itemID)
		if resolveErr != nil {
			return resolveErr
		}
		if ok {
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

// errNotATraceReference says the argument addresses no particular RUN.
//
// It is a sentinel rather than a message because the caller acts on it: a
// reference that is not a trace address is an automation reference, and D-1
// (INVARIANTS.md H-17) says every command taking an automation accepts every
// identifier the family prints. `trace show` is named in the manual's own
// statement of that contract and refused all four forms — the config id, the
// alias, the entity_id and the object id — with "invalid trace ID format"
// (#66). "Invalid" was the wrong word: the forms are valid, they address an
// automation rather than one of its runs.
var errNotATraceReference = errors.New("not a trace address")

// resolveTraceID resolves a stable ID (trc:a7) or composite key to domain, item_id, run_id.
//
// A reference in neither form returns errNotATraceReference, which the caller
// resolves as an automation instead. A trc: id that IS in trace form and simply
// unknown is still a hard error: it named a run, and that run is not there.
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

	return "", "", "", errNotATraceReference
}

// latestAutomationRun turns an automation reference into the address of its most
// recent stored run.
//
// It resolves through resolveAutomation, the one function that accepts every
// identifier the automation family prints, so this command inherits the contract
// rather than re-deriving a narrower version of it — which is how `auto diff`
// and `auto apply` came to refuse ids `auto ls` printed while H-17 asserted they
// did not.
//
// Two failures are distinguished, because the fix differs: a reference that
// names no automation is the caller's mistake, and an automation Home Assistant
// holds no trace for is an instance fact (HA stores a bounded number of traces
// per automation and forgets the rest, and an automation that has never run has
// none at all).
func latestAutomationRun(ctx context.Context, cfg *config.Config, ws *haapi.WSClient, ref string) (domain, itemID, runID string, err error) {
	client := haapi.New(cfg.URL, cfg.Token)
	auto, ok, err := resolveAutomation(ctx, client, ref)
	if err != nil {
		return "", "", "", err
	}
	if !ok {
		return "", "", "", fmt.Errorf(
			"%q is neither a trace address (trc:<hash>, domain.item_id/run_id) nor an automation on this instance — `auto ls` lists the automations, `auto show <id>` their recent runs", ref)
	}

	traces, err := ws.TraceList(ctx, "automation")
	if err != nil {
		return "", "", "", fmt.Errorf("listing traces: %w", err)
	}
	runs := traces[automationTraceKey(auto)]
	if len(runs) == 0 {
		return "", "", "", fmt.Errorf(
			"no stored trace for %s — Home Assistant keeps a bounded number per automation, and this one has not run since the ones it holds were stored", displayAutomationRef(auto, ref))
	}

	// HA returns a bounded per-automation list oldest-first; the newest run is
	// the one a caller asking "what happened?" means. Picked by timestamp rather
	// than by position, because a position is an assumption about the wire and a
	// timestamp is the wire's own answer.
	newest := runs[0]
	for _, r := range runs[1:] {
		if r.Timestamp.Start > newest.Timestamp.Start {
			newest = r
		}
	}
	return newest.Domain, newest.ItemID, newest.RunID, nil
}

// displayAutomationRef names an automation the way the rest of the CLI does —
// by entity_id — falling back to what the caller typed when the entity carries
// none.
func displayAutomationRef(auto automationEntity, ref string) string {
	if auto.EntityID != "" {
		return auto.EntityID
	}
	return ref
}

// automationConfigIDFor maps an automation reference (object id, entity_id,
// config id or alias) to the config id HA files its traces under. Returns
// (ref, false, nil) when no live automation matches — a genuinely unknown
// reference is passed through unchanged so HA's own error surfaces rather than
// a silently-rewritten lookup.
//
// A states fetch that FAILED is returned as an error instead (H-7, SPEC §2a).
// Passing the reference through then would send HA an object id it does not
// file traces under, and HA's "no such trace" would read as a caller typo about
// an instance hactl never managed to read.
func automationConfigIDFor(ctx context.Context, cfg *config.Config, ref string) (string, bool, error) {
	client := haapi.New(cfg.URL, cfg.Token)
	a, ok, err := resolveAutomation(ctx, client, ref)
	if err != nil {
		return "", false, err
	}
	if !ok || a.Attributes.ID == "" {
		return ref, false, nil
	}
	return a.Attributes.ID, true, nil
}

// automationEntityIDFor is automationConfigIDFor's inverse: it maps the config
// id HA files traces under back to the entity_id every other command speaks.
func automationEntityIDFor(ctx context.Context, cfg *config.Config, ref string) (string, bool, error) {
	client := haapi.New(cfg.URL, cfg.Token)
	a, ok, err := resolveAutomation(ctx, client, ref)
	if err != nil {
		return "", false, err
	}
	if !ok || a.EntityID == "" {
		return ref, false, nil
	}
	return a.EntityID, true, nil
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
