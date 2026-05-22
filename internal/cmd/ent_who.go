package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var entWhoCmd = &cobra.Command{
	Use:   "who <entity_id>",
	Short: "Show who/what changed an entity, with counts",
	Long: `Attribute an entity's recent changes to the user, automation, script,
or device that triggered them. Aggregates over --since (default 24h)
and emits a per-event table plus a counts summary.

Resolving user UUIDs to names requires an admin long-lived token; when
the token lacks admin scope, raw UUIDs are shown and the rest of the
attribution (automations/scripts/devices) still works.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntWho(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	entCmd.AddCommand(entWhoCmd)
}

// entWhoJSON is the structured shape emitted by `hactl ent who --json`.
type entWhoJSON struct {
	Events  []entWhoEventJSON   `json:"events"`
	Summary []entWhoSummaryJSON `json:"summary"`
	Window  entWhoWindowJSON    `json:"window"`
}

type entWhoEventJSON struct {
	When                string `json:"when"`
	EntityID            string `json:"entity_id"`
	State               string `json:"state"`
	ChangedBy           string `json:"changed_by"`
	ContextID           string `json:"context_id,omitempty"`
	ContextUserID       string `json:"context_user_id,omitempty"`
	ContextEventType    string `json:"context_event_type,omitempty"`
	ContextName         string `json:"context_name,omitempty"`
	ContextEntityID     string `json:"context_entity_id,omitempty"`
	ContextEntityIDName string `json:"context_entity_id_name,omitempty"`
	ContextSource       string `json:"context_source,omitempty"`
}

type entWhoSummaryJSON struct {
	Trigger string `json:"trigger"`
	Count   int    `json:"count"`
}

type entWhoWindowJSON struct {
	Since string `json:"since"`
	Until string `json:"until"`
}

func runEntWho(ctx context.Context, w io.Writer, entityID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	sinceDur, err := parseSince(flagSince)
	if err != nil {
		return err
	}
	now := time.Now()
	start := now.Add(-sinceDur)

	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetLogbookFiltered(ctx,
		start.Format(time.RFC3339),
		now.Format(time.RFC3339),
		entityID)
	if err != nil {
		return fmt.Errorf("fetching logbook: %w", err)
	}

	var entries []logbookEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parsing logbook: %w", err)
	}

	if len(entries) == 0 && !flagJSON {
		_, _ = fmt.Fprintf(w, "no changes for %s in the last %s\n", entityID, flagSince)
		return nil
	}

	// Pull users once for attribution. Graceful-degrades when not admin.
	var users map[string]haapi.UserEntry
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if wsErr := ws.Connect(ctx); wsErr == nil {
		users = loadUsers(ctx, ws)
		_ = ws.Close()
	}

	// Resolve labels once and tally counts.
	labels := make([]string, len(entries))
	counts := make(map[string]int, len(entries))
	for i, e := range entries {
		l := triggerLabel(e, users)
		labels[i] = l
		counts[l]++
	}

	summary := make([]entWhoSummaryJSON, 0, len(counts))
	for trigger, n := range counts {
		summary = append(summary, entWhoSummaryJSON{Trigger: trigger, Count: n})
	}
	sort.Slice(summary, func(i, j int) bool {
		if summary[i].Count != summary[j].Count {
			return summary[i].Count > summary[j].Count
		}
		return summary[i].Trigger < summary[j].Trigger
	})

	if flagJSON {
		events := make([]entWhoEventJSON, len(entries))
		for i, e := range entries {
			events[i] = entWhoEventJSON{
				When:                e.When,
				EntityID:            e.EntityID,
				State:               e.State,
				ChangedBy:           labels[i],
				ContextID:           e.ContextID,
				ContextUserID:       e.ContextUserID,
				ContextEventType:    e.ContextEventType,
				ContextName:         e.ContextName,
				ContextEntityID:     e.ContextEntityID,
				ContextEntityIDName: e.ContextEntityIDName,
				ContextSource:       e.ContextSource,
			}
		}
		out := entWhoJSON{
			Events:  events,
			Summary: summary,
			Window: entWhoWindowJSON{
				Since: start.Format(time.RFC3339),
				Until: now.Format(time.RFC3339),
			},
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Per-event table.
	tbl := &format.Table{
		Headers: []string{"time", "state", "changed_by"},
		Rows:    make([][]string, len(entries)),
	}
	for i, e := range entries {
		tbl.Rows[i] = []string{
			formatShortTime(e.When),
			e.State,
			labels[i],
		}
	}
	if err := tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		Compact: true,
	}); err != nil {
		return err
	}

	// Summary table.
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "summary (%s):\n", flagSince)
	sumTbl := &format.Table{
		Headers: []string{"changed_by", "count"},
		Rows:    make([][]string, len(summary)),
	}
	for i, s := range summary {
		sumTbl.Rows[i] = []string{s.Trigger, strconv.Itoa(s.Count)}
	}
	return sumTbl.Render(w, format.RenderOpts{Full: true, Compact: true})
}
