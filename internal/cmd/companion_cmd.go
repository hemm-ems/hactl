package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// companionStatusResult holds structured companion status data for JSON output.
type companionStatusResult struct {
	ConfigURL           string `json:"config_url,omitempty"`
	Source              string `json:"source,omitempty"`
	WSConnect           string `json:"ws_connect"`
	WSError             string `json:"ws_error,omitempty"`
	Discovery           string `json:"discovery"`
	DiscoveryReason     string `json:"discovery_reason,omitempty"`
	DiscoveryHint       string `json:"discovery_hint,omitempty"`
	URL                 string `json:"url,omitempty"`
	Health              string `json:"health,omitempty"`
	HealthError         string `json:"health_error,omitempty"`
	Version             string `json:"version,omitempty"`
	SupervisorReachable *bool  `json:"supervisor_reachable,omitempty"`
	HasHACLI            *bool  `json:"ha_cli,omitempty"`
	IngressActive       *bool  `json:"ingress_active,omitempty"`
	AuthMode            string `json:"auth_mode,omitempty"`
}

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

	res := companionStatusResult{}

	if cfg.CompanionURL != "" {
		res.ConfigURL = cfg.CompanionURL
		res.Source = ".env (COMPANION_URL)"
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	var wsClient *haapi.WSClient
	if connErr := ws.Connect(ctx); connErr != nil {
		res.WSConnect = "failed"
		res.WSError = connErr.Error()
	} else {
		defer func() { _ = ws.Close() }()
		wsClient = ws
		res.WSConnect = "ok"
	}

	companionURL, discoverErr := companion.Discover(ctx, cfg, wsClient)
	if discoverErr != nil {
		var de *companion.DiscoveryError
		errors.As(discoverErr, &de)
		res.Discovery = "failed"
		if de != nil {
			res.DiscoveryReason = string(de.Reason)
		} else {
			res.DiscoveryReason = "unreachable"
		}
		res.DiscoveryHint = discoverErr.Error()
		return writeCompanionStatus(w, res)
	}

	res.Discovery = "ok"
	res.URL = companionURL

	cc := companion.New(companionURL, cfg.CompanionToken)
	if wsClient != nil {
		cc = cc.WithIngressAuth(wsClient)
	}
	health, healthErr := cc.Health(ctx)
	if healthErr != nil {
		res.Health = "failed"
		res.HealthError = healthErr.Error()
		return writeCompanionStatus(w, res)
	}
	res.Health = health.Status
	res.Version = health.Version

	if status, statusErr := cc.Status(ctx); statusErr == nil {
		sr := status.SupervisorReachable
		hc := status.HasHACLI
		ia := status.IngressActive
		res.SupervisorReachable = &sr
		res.HasHACLI = &hc
		res.IngressActive = &ia
		res.AuthMode = status.AuthMode
	}

	return writeCompanionStatus(w, res)
}

func writeCompanionStatus(w io.Writer, res companionStatusResult) error {
	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	_, _ = fmt.Fprintln(w, "companion status")

	if res.ConfigURL != "" {
		_, _ = fmt.Fprintf(w, "  config URL:  %s\n", res.ConfigURL)
		_, _ = fmt.Fprintf(w, "  source:      %s\n", res.Source)
	} else {
		_, _ = fmt.Fprintln(w, "  config URL:  (not set — will enumerate /addons via Supervisor WS proxy)")
	}

	if res.WSConnect == "ok" {
		_, _ = fmt.Fprintln(w, "  WS connect:  ok")
	} else {
		_, _ = fmt.Fprintf(w, "  WS connect:  failed (%s)\n", res.WSError)
	}

	if res.Discovery == "failed" {
		_, _ = fmt.Fprintf(w, "  discovery:   failed (%s)\n", res.DiscoveryReason)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, res.DiscoveryHint)
		return nil
	}

	_, _ = fmt.Fprintf(w, "  URL:         %s\n", res.URL)

	if res.Health == "" {
		return nil
	}
	if res.HealthError != "" {
		_, _ = fmt.Fprintf(w, "  health:      failed (%s)\n", res.HealthError)
		return nil
	}
	_, _ = fmt.Fprintf(w, "  health:      %s\n", res.Health)
	_, _ = fmt.Fprintf(w, "  version:     %s\n", res.Version)

	if res.SupervisorReachable != nil {
		_, _ = fmt.Fprintf(w, "  supervisor:  %v\n", *res.SupervisorReachable)
		_, _ = fmt.Fprintf(w, "  ha cli:      %v\n", *res.HasHACLI)
		_, _ = fmt.Fprintf(w, "  ingress:     %v\n", *res.IngressActive)
		_, _ = fmt.Fprintf(w, "  auth mode:   %s\n", res.AuthMode)
	}

	return nil
}
