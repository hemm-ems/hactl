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

var companionCmd = family(&cobra.Command{
	Use:   "companion",
	Short: "Diagnose hactl-companion connectivity",
})

var companionStatusCmd = &cobra.Command{
	Use:   "status",
	Args:  takesNone(),
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

// formatCompanionStatusLine returns the one-line companion status `health`
// prints. status is the top-level status (e.g. "ok", "not found",
// "unreachable"). reason is the DiscoveryReason string (may be empty).
//
// It says what to DO, which is the whole reason it exists: a reason code names
// a category, and the reader of a one-line overview needs the next step. It had
// drifted into being called by nothing but its own tests while `health` printed
// the bare code beside it — the divergence #75 is about, one command over.
func formatCompanionStatusLine(status, reason string) string {
	if reason == "" {
		return "companion=" + status
	}
	switch companion.DiscoveryReason(reason) {
	case companion.ReasonAuthDenied:
		return fmt.Sprintf("companion=%s  (token lacks hassio_admin — create token as HA owner or set COMPANION_URL)", status)
	case companion.ReasonAuthInvalid:
		return fmt.Sprintf("companion=%s  (HA rejected the token — replace HA_TOKEN in .env)", status)
	case companion.ReasonAddonMissing:
		return fmt.Sprintf("companion=%s  (add-on not installed — HA → Settings → Add-ons)", status)
	case companion.ReasonRedirected:
		return fmt.Sprintf("companion=%s  (HA_URL redirects elsewhere — point it at the origin that answers)", status)
	case companion.ReasonProtocolMismatch:
		return fmt.Sprintf("companion=%s  (HA Container has no Supervisor — set COMPANION_URL)", status)
	case companion.ReasonUnreachable:
		return fmt.Sprintf("companion=%s  (nothing answered at HA_URL — check the URL and the network)", status)
	default:
		// A reason with no line of its own reaches here and prints its own code,
		// which is the least a reader can act on. TestEveryDiscoveryReasonHasAStatusLine
		// quantifies over companion.DiscoveryReasons() so nothing arrives here.
		return fmt.Sprintf("companion=%s  (%s)", status, reason)
	}
}

// companionUnreachableError is `companion status`'s verdict when the answer it
// just printed is a failure.
//
// The command used to exit 0 in every failure mode there is — a rejected token,
// a refused port, an i/o timeout — while its own body said "WS connect: failed",
// "discovery: failed (unreachable)" and "companion not found (unreachable)".
// `hactl companion status && proceed` proceeded (#74). A diagnostic that cannot
// be gated on is not a diagnostic; the machine-readable half already carried the
// verdict in `ws_connect`/`discovery`, and the exit code is the same statement
// in the one channel every caller reads.
//
// The report is rendered BEFORE this is returned and reaches stdout, because an
// error ends a command and does not erase what it printed (D-33).
type companionUnreachableError struct{ reason string }

func (e *companionUnreachableError) Error() string {
	return "companion not usable: " + e.reason
}
func (e *companionUnreachableError) ExitCode() int { return 1 }

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

	// The client is handed to Discover whether or not it connected: a failed
	// Connect carries the reason it failed, and that reason IS the discovery
	// reason. Passing nil here was what turned an authentication failure into
	// "check Ingress / network" (#75).
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		res.WSConnect = "failed"
		res.WSError = connErr.Error()
	} else {
		defer func() { _ = ws.Close() }()
		res.WSConnect = "ok"
	}

	companionURL, discoverErr := companion.Discover(ctx, cfg, ws)
	if discoverErr != nil {
		var de *companion.DiscoveryError
		errors.As(discoverErr, &de)
		res.Discovery = "failed"
		if de != nil {
			res.DiscoveryReason = string(de.Reason)
		} else {
			res.DiscoveryReason = string(companion.ReasonUnreachable)
		}
		res.DiscoveryHint = discoverErr.Error()
		if writeErr := writeCompanionStatus(w, res); writeErr != nil {
			return writeErr
		}
		return &companionUnreachableError{reason: "discovery failed (" + res.DiscoveryReason + ")"}
	}

	res.Discovery = "ok"
	res.URL = companionURL

	cc := companion.New(companionURL, cfg.CompanionToken)
	if ws.Connected() {
		cc = cc.WithIngressAuth(ws)
	}
	health, healthErr := cc.Health(ctx)
	if healthErr != nil {
		res.Health = "failed"
		res.HealthError = healthErr.Error()
		if writeErr := writeCompanionStatus(w, res); writeErr != nil {
			return writeErr
		}
		return &companionUnreachableError{reason: "health check failed"}
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
