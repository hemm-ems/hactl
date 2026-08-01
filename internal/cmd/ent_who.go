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
	Args: takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntWho(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	entCmd.AddCommand(entWhoCmd)
}

// entWhoJSON is the structured shape emitted by `hactl ent who --json`.
//
// H-10 + D-4: `source` and `logbook_excluded` are the machine half of "every
// answer names its source". A caller distinguishes three states by fields,
// never by emptiness alone: events non-empty (the logbook answered), events
// empty + logbook_excluded false (answered quiet — a verified zero), events
// empty + logbook_excluded true (the logbook structurally cannot answer for
// this entity; `changed_by` then carries the shared state-context fallback
// for the most recent change, the same answer `ent show` gives).
type entWhoJSON struct {
	Events  []entWhoEventJSON   `json:"events"`
	Summary []entWhoSummaryJSON `json:"summary"`
	Window  entWhoWindowJSON    `json:"window"`
	Source  string              `json:"source"`
	// LogbookExcluded says HA's logbook excludes this entity (continuous
	// sensor, or a never-logged domain) — the only case where Source is
	// "state context".
	LogbookExcluded bool `json:"logbook_excluded"`
	// ChangedBy is the shared fallback answer for the most recent change,
	// present only when LogbookExcluded (the events list can say nothing).
	ChangedBy string `json:"changed_by,omitempty"`
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

	sinceDur, err := parseSince(sinceWindow())
	if err != nil {
		return err
	}
	now := time.Now()
	start := now.Add(-sinceDur)

	client := haapi.New(cfg.URL, cfg.Token)
	entries, err := fetchLogbookEntries(ctx, client, start, now, entityID)
	if err != nil {
		return err
	}

	// Pull users once for attribution. Graceful-degrades when not admin.
	var users map[string]haapi.UserEntry
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if wsErr := ws.Connect(ctx); wsErr == nil {
		users = loadUsers(ctx, ws)
		_ = ws.Close()
	}

	if len(entries) == 0 {
		// The logbook is empty for a typo, for a quiet entity, AND for an
		// entity HA's logbook excludes (D70). Only the last two are answers,
		// and they are different answers: a nonexistent entity_id must fail, a
		// quiet covered entity is the logbook's verified zero, and an excluded
		// entity gets the shared state-context fallback plus an explicit
		// statement of the exclusion — never a bare "no changes" that
		// contradicts `ent show`'s changed_by line for the same entity.
		st, stErr := fetchEntityState(ctx, client, entityID)
		if stErr != nil {
			if unknownErr := errUnknownEntity(ctx, client, entityID); unknownErr != nil {
				return unknownErr
			}
			return stErr
		}
		ans := resolveActor(nil, st, users)
		window := entWhoWindowJSON{
			Since: start.Format(time.RFC3339),
			Until: now.Format(time.RFC3339),
		}
		if ans.LogbookExcluded {
			if flagJSON {
				return writeEntWhoJSON(w, entWhoJSON{
					Events:          []entWhoEventJSON{},
					Summary:         []entWhoSummaryJSON{},
					Window:          window,
					Source:          ans.Source,
					LogbookExcluded: true,
					ChangedBy:       ans.ChangedBy,
				})
			}
			_, _ = fmt.Fprintf(w, "no logbook entries for %s: HA's logbook excludes it (%s)\n",
				entityID, ans.ExclusionReason)
			_, _ = fmt.Fprintf(w, "most recent change: %s\n", ans.Label())
			return nil
		}
		if flagJSON {
			return writeEntWhoJSON(w, entWhoJSON{
				Events:  []entWhoEventJSON{},
				Summary: []entWhoSummaryJSON{},
				Window:  window,
				Source:  actorSourceLogbook,
			})
		}
		_, _ = fmt.Fprintf(w, "no changes for %s in the last %s (source: %s)\n",
			entityID, sinceWindow(), actorSourceLogbook)
		return nil
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
		return writeEntWhoJSON(w, entWhoJSON{
			Events:  events,
			Summary: summary,
			Window: entWhoWindowJSON{
				Since: start.Format(time.RFC3339),
				Until: now.Format(time.RFC3339),
			},
			Source: actorSourceLogbook,
		})
	}

	// D-4: every answer names its source — this whole table and summary is the
	// logbook's answer.
	_, _ = fmt.Fprintf(w, "source: %s\n", actorSourceLogbook)

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
	_, _ = fmt.Fprintf(w, "summary (%s):\n", sinceWindow())
	sumTbl := &format.Table{
		Headers: []string{"changed_by", "count"},
		Rows:    make([][]string, len(summary)),
	}
	for i, s := range summary {
		sumTbl.Rows[i] = []string{s.Trigger, strconv.Itoa(s.Count)}
	}
	return sumTbl.Render(w, format.RenderOpts{Full: true, Compact: true})
}

// writeEntWhoJSON encodes the one `ent who --json` shape, every branch of the
// command going through it so the source/exclusion fields cannot be forgotten
// on one path (H-10).
func writeEntWhoJSON(w io.Writer, out entWhoJSON) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
