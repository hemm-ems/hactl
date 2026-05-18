package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/writer"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback [automation-id]",
	Short: "Restore the most recent automation backup",
	Long:  "Rollback to the last backed-up automation config. Optionally specify an automation ID.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		autoID := ""
		if len(args) > 0 {
			autoID = args[0]
		}
		return runRollback(cmd.Context(), cmd.OutOrStdout(), autoID)
	},
}

func init() {
	rootCmd.AddCommand(rollbackCmd)
}

func runRollback(ctx context.Context, w io.Writer, automationID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	backupDir := filepath.Join(cfg.Dir, "backups")

	// Connect WebSocket for reload (optional)
	var wsClient *haapi.WSClient
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connectErr := ws.Connect(ctx); connectErr != nil {
		slog.Warn("could not connect WebSocket", "error", connectErr)
	} else {
		wsClient = ws
		defer func() { _ = ws.Close() }()
	}

	wr := writer.New(client, wsClient, backupDir)

	result, err := wr.Rollback(ctx, automationID)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "rolled back: %s\n", result.AutomationID)
	_, _ = fmt.Fprintf(w, "from backup: %s\n", result.BackupPath)
	if result.Reloaded {
		_, _ = fmt.Fprintf(w, "reload:      ok\n")
	}
	return nil
}
