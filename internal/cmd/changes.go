package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var changesCmd = &cobra.Command{
	Use:   "changes",
	Short: "Show recent state changes",
	Long:  "Display recent logbook entries (state changes, automations fired, etc.).",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runChanges(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	rootCmd.AddCommand(changesCmd)
}

// logbookEntry holds one entry from the HA logbook API.
//
// Context_* fields come from HA's logbook ContextAugmenter
// (homeassistant/components/logbook/processor.py, HA 2026.4.4) and identify
// the trigger of this event: a user (ContextUserID), an automation/script
// (ContextEventType + ContextName + ContextEntityID), or a parent entity.
type logbookEntry struct {
	EntityID            string `json:"entity_id"`
	Name                string `json:"name"`
	State               string `json:"state"`
	When                string `json:"when"`
	Domain              string `json:"domain"`
	Message             string `json:"message"`
	ContextID           string `json:"context_id"`
	ContextUserID       string `json:"context_user_id"`
	ContextEventType    string `json:"context_event_type"`
	ContextName         string `json:"context_name"`
	ContextEntityID     string `json:"context_entity_id"`
	ContextEntityIDName string `json:"context_entity_id_name"`
	ContextSource       string `json:"context_source"`
}

func runChanges(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	sinceDur, err := parseSince(flagSince)
	if err != nil {
		return err
	}

	now := time.Now()
	startTime := now.Add(-sinceDur)

	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetLogbook(ctx,
		startTime.Format(time.RFC3339),
		now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("fetching logbook: %w", err)
	}

	var entries []logbookEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parsing logbook: %w", err)
	}

	if len(entries) == 0 {
		_, _ = fmt.Fprintln(w, "no changes in the last "+flagSince)
		return nil
	}

	// JSON mode emits the raw entries (including all context_* fields) so
	// LLM consumers get the full structured data, not just the table cells.
	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	// Pull users once for changed_by attribution. Graceful-degrades to UUIDs
	// when the LL token isn't admin.
	var users map[string]haapi.UserEntry
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if wsErr := ws.Connect(ctx); wsErr == nil {
		users = loadUsers(ctx, ws)
		_ = ws.Close()
	}

	tbl := &format.Table{
		Headers: []string{"time", "entity_id", "state", "who", "message"},
		Rows:    make([][]string, len(entries)),
	}
	for i, e := range entries {
		msg := e.Message
		if msg == "" {
			msg = e.Name
		}
		if len(msg) > 50 {
			msg = msg[:47] + "..."
		}
		tbl.Rows[i] = []string{
			formatShortTime(e.When),
			e.EntityID,
			e.State,
			triggerLabel(e, users),
			msg,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		Compact: true,
	})
}
