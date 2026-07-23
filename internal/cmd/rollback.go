package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/writer"
)

var flagRollbackConfirm bool

// rollbackDeprecationMsg returns the one-line deprecation notice for hactl rollback.
func rollbackDeprecationMsg() string {
	return "deprecated: use 'hactl auto rollback' instead (hactl rollback will be removed in a future release)"
}

var autoRollbackCmd = &cobra.Command{
	Use:   "rollback [automation-id]",
	Short: "Restore the most recent automation backup (dry-run by default)",
	Long:  "Rollback to the last backed-up automation config. Optionally specify an automation ID. Dry-run by default: previews which backup would be restored; use --confirm to apply.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		autoID := ""
		if len(args) > 0 {
			autoID = args[0]
		}
		return runRollback(cmd.Context(), cmd.OutOrStdout(), autoID)
	},
}

// rollbackCmd is a deprecated alias for 'hactl auto rollback'.
var rollbackCmd = &cobra.Command{
	Use:   "rollback [automation-id]",
	Short: "Deprecated: use 'hactl auto rollback' instead",
	Long:  "Rollback to the last backed-up automation config (dry-run by default; use --confirm to apply). Use 'hactl auto rollback' instead.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _ = fmt.Fprintln(os.Stderr, rollbackDeprecationMsg())
		autoID := ""
		if len(args) > 0 {
			autoID = args[0]
		}
		return runRollback(cmd.Context(), cmd.OutOrStdout(), autoID)
	},
}

func init() {
	// Canonical: hactl auto rollback
	autoRollbackCmd.Flags().BoolVar(&flagRollbackConfirm, "confirm", false, "actually restore + reload (default is dry-run)")
	autoCmd.AddCommand(autoRollbackCmd)
	// Deprecated alias: hactl rollback
	rollbackCmd.Flags().BoolVar(&flagRollbackConfirm, "confirm", false, "actually restore + reload (default is dry-run)")
	rootCmd.AddCommand(rollbackCmd)
}

func runRollback(ctx context.Context, w io.Writer, automationID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	backupDir := filepath.Join(cfg.Dir, "backups")

	if !flagRollbackConfirm {
		plan, planErr := writer.New(client, nil, backupDir).PlanRollback(automationID)
		if planErr != nil {
			return planErr
		}
		return dryRun("roll back automation").
			with("automation", plan.AutomationID).
			with("from_backup", plan.BackupPath).
			render(w)
	}

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
