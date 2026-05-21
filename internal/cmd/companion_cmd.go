package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var companionCmd = &cobra.Command{
	Use:   "companion",
	Short: "Diagnose hactl-companion connectivity",
}

var companionStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show companion discovery result and capabilities",
	Long:  "Run through companion discovery paths and print a one-screen diagnostic.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCompanionStatus(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	companionCmd.AddCommand(companionStatusCmd)
	rootCmd.AddCommand(companionCmd)
}

// formatCompanionStatusLine returns a one-line companion status string for
// the health command and tests. status is the top-level status (e.g. "ok",
// "not found", "unreachable"). reason is the DiscoveryReason string (may be empty).
func formatCompanionStatusLine(status, reason string) string {
	if reason == "" {
		return "companion=" + status
	}
	switch companion.DiscoveryReason(reason) {
	case companion.ReasonAuthDenied:
		return fmt.Sprintf("companion=%s  (token lacks hassio_admin — create token as HA owner or set COMPANION_URL)", status)
	case companion.ReasonAddonMissing:
		return fmt.Sprintf("companion=%s  (add-on not installed — HA → Settings → Add-ons)", status)
	case companion.ReasonProtocolMismatch:
		return fmt.Sprintf("companion=%s  (HA Container has no Supervisor — set COMPANION_URL)", status)
	default:
		return fmt.Sprintf("companion=%s  (%s)", status, reason)
	}
}

func runCompanionStatus(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(w, "companion status")

	// Show COMPANION_URL from config if set
	if cfg.CompanionURL != "" {
		_, _ = fmt.Fprintf(w, "  config URL:  %s\n", cfg.CompanionURL)
		_, _ = fmt.Fprintln(w, "  source:      .env (COMPANION_URL)")
	} else {
		_, _ = fmt.Fprintln(w, "  config URL:  (not set — will enumerate /addons via Supervisor WS proxy)")
	}

	// Try WS connect
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	var wsClient *haapi.WSClient
	if connErr := ws.Connect(ctx); connErr != nil {
		_, _ = fmt.Fprintf(w, "  WS connect:  failed (%v)\n", connErr)
	} else {
		defer func() { _ = ws.Close() }()
		wsClient = ws
		_, _ = fmt.Fprintln(w, "  WS connect:  ok")
	}

	// Companion discovery
	companionURL, discoverErr := companion.Discover(ctx, cfg, wsClient)
	if discoverErr != nil {
		var de *companion.DiscoveryError
		errors.As(discoverErr, &de)
		reason := "unreachable"
		if de != nil {
			reason = string(de.Reason)
		}
		_, _ = fmt.Fprintf(w, "  discovery:   failed (%s)\n", reason)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, discoverErr.Error())
		return nil
	}

	_, _ = fmt.Fprintf(w, "  URL:         %s\n", companionURL)

	// Health check
	cc := companion.New(companionURL, cfg.CompanionToken)
	health, healthErr := cc.Health(ctx)
	if healthErr != nil {
		_, _ = fmt.Fprintf(w, "  health:      failed (%v)\n", healthErr)
		return nil
	}
	_, _ = fmt.Fprintf(w, "  health:      %s\n", health.Status)
	_, _ = fmt.Fprintf(w, "  version:     %s\n", health.Version)

	// Status check (best-effort — companion may not have /v1/status yet)
	status, statusErr := cc.Status(ctx)
	if statusErr == nil {
		_, _ = fmt.Fprintf(w, "  supervisor:  %v\n", status.SupervisorReachable)
		_, _ = fmt.Fprintf(w, "  ha cli:      %v\n", status.HasHACLI)
		_, _ = fmt.Fprintf(w, "  ingress:     %v\n", status.IngressActive)
		_, _ = fmt.Fprintf(w, "  auth mode:   %s\n", status.AuthMode)
	}

	return nil
}
