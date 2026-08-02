package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var flagHealthCheckConfig bool

var healthCmd = &cobra.Command{
	Use:   "health",
	Args:  takesNone(),
	Short: "Show Home Assistant health overview",
	Long:  "Display HA version, recorder status, and error count.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHealth(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	healthCmd.Flags().BoolVar(&flagHealthCheckConfig, "check-config", false, "validate the on-disk HA config via the companion (runs a full config check; slow)")
	rootCmd.AddCommand(healthCmd)
}

// healthResult holds structured health data for JSON output.
type healthResult struct {
	Version          string `json:"version"`
	State            string `json:"state"`
	RecorderStatus   string `json:"recorder"`
	LocationName     string `json:"location"`
	TimeZone         string `json:"timezone"`
	ErrorCount       int    `json:"errors"`
	SafeMode         bool   `json:"safe_mode,omitempty"`
	CompanionVersion string `json:"companion_version,omitempty"`
	CompanionStatus  string `json:"companion_status,omitempty"`
	HAConfigValid    *bool  `json:"ha_config_valid,omitempty"`
	HAConfigErrors   string `json:"ha_config_errors,omitempty"`
}

// haConfig holds the subset of /api/config we care about.
type haConfig struct {
	UnitSystem      any      `json:"unit_system"`
	Version         string   `json:"version"`
	LocationName    string   `json:"location_name"`
	State           string   `json:"state"`
	ExternalURL     string   `json:"external_url"`
	InternalURL     string   `json:"internal_url"`
	Currency        string   `json:"currency"`
	TimeZone        string   `json:"time_zone"`
	ConfigDir       string   `json:"config_dir"`
	Components      []string `json:"components"`
	AllowlistExtURL []string `json:"allowlist_external_urls"`
	SafeMode        bool     `json:"safe_mode"`
}

func runHealth(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)

	// Fetch config (version, state, components)
	configData, err := client.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("fetching HA config: %w", err)
	}

	var haCfg haConfig
	if unmarshalErr := json.Unmarshal(configData, &haCfg); unmarshalErr != nil {
		return fmt.Errorf("parsing HA config: %w", unmarshalErr)
	}
	if degErr := degeneracy.Check("/api/config", &haCfg); degErr != nil {
		return degErr
	}

	// Check recorder
	recorderStatus := "not loaded"
	if slices.Contains(haCfg.Components, "recorder") {
		recorderStatus = "ok"
	}

	// Fetch error log entries (WS system_log/list, REST /api/error_log fallback).
	// Non-fatal: some HA setups disable system_log and newer HA dropped /api/error_log.
	errorCount := -1
	entries, err := fetchLogEntries(ctx, cfg)
	if err != nil {
		slog.Warn("could not fetch error log", "error", err)
	} else {
		errorCount = countErrorEntries(entries)
	}

	// Output
	hr := healthResult{
		Version:        haCfg.Version,
		State:          haCfg.State,
		RecorderStatus: recorderStatus,
		ErrorCount:     errorCount,
		LocationName:   haCfg.LocationName,
		TimeZone:       haCfg.TimeZone,
		SafeMode:       haCfg.SafeMode,
	}

	// Companion discovery and health check (non-fatal)
	comp := discoverCompanion(ctx, cfg, flagHealthCheckConfig)
	companionStatus, companionVersion := comp.status, comp.version
	configValid, configErrors := comp.configValid, comp.configErrors
	hr.CompanionStatus = companionStatus
	hr.CompanionVersion = companionVersion
	hr.HAConfigValid = configValid
	hr.HAConfigErrors = configErrors

	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(hr)
	}

	if errorCount >= 0 {
		_, _ = fmt.Fprintf(w, "HA %s  state=%s  recorder=%s  errors=%d\n", haCfg.Version, haCfg.State, recorderStatus, errorCount)
	} else {
		_, _ = fmt.Fprintf(w, "HA %s  state=%s  recorder=%s  errors=n/a\n", haCfg.Version, haCfg.State, recorderStatus)
	}
	_, _ = fmt.Fprintf(w, "location=%s  tz=%s\n", haCfg.LocationName, haCfg.TimeZone)
	if haCfg.SafeMode {
		// No decoration (docs/manual.md "no emojis, no color"): this was the
		// only glyph in the product, on its most-called command, in the one
		// condition a caller most needs to match on reliably.
		_, _ = fmt.Fprintf(w, "WARNING: SAFE MODE ACTIVE\n")
	}

	// Companion status line. The machine contract keeps the reason code
	// (`companion_status: "not found (auth_invalid)"`); the human line names the
	// next step, which is what formatCompanionStatusLine is for.
	if companionStatus != "" {
		if companionVersion != "" {
			_, _ = fmt.Fprintf(w, "companion=%s  version=%s\n", companionStatus, companionVersion)
		} else {
			_, _ = fmt.Fprintln(w, formatCompanionStatusLine(comp.headline, string(comp.reason)))
		}
	}

	if flagHealthCheckConfig {
		switch {
		case configValid == nil:
			_, _ = fmt.Fprintf(w, "config_check=failed: %s\n", configErrors)
		case *configValid:
			_, _ = fmt.Fprintf(w, "config_check=valid\n")
		default:
			_, _ = fmt.Fprintf(w, "config_check=INVALID: %s\n", configErrors)
		}
	}

	return nil
}

// countErrorEntries counts entries logged at ERROR level.
func countErrorEntries(entries []analyze.LogEntry) int {
	count := 0
	for _, e := range entries {
		if e.Level == "ERROR" {
			count++
		}
	}
	return count
}

// companionProbe is what `health` learned about the companion.
//
// headline and reason are the two halves of status kept apart, because they
// answer different readers: `companion_status` in --json is the machine's
// contract and stays "not found (auth_invalid)", while the text line renders
// the reason as a next step (formatCompanionStatusLine). Returning them already
// joined is what left `health` printing a bare reason code while the function
// written to explain it was called by nothing.
type companionProbe struct {
	status       string // the joined form: "ok", "not found (auth_invalid)", "unreachable"
	headline     string // "ok" / "not found" / "unreachable"
	reason       companion.DiscoveryReason
	version      string
	configValid  *bool
	configErrors string
}

// discoverCompanion attempts to find and health-check the companion.
//
// Non-fatal: an unavailable companion is a field of the health report, not a
// failure of it. When checkConfig is set and the companion is reachable, it also
// validates the on-disk HA config; configValid is nil when the check was not
// requested or could not run (reason in configErrors). The check happens here,
// not in the caller, because the ingress auth session is tied to the WS client
// closed on return.
func discoverCompanion(ctx context.Context, cfg *config.Config, checkConfig bool) companionProbe {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err == nil {
		defer func() { _ = ws.Close() }()
	}

	notFoundErrors := ""
	if checkConfig {
		notFoundErrors = "companion not found"
	}

	// Handed over connected or not: a failed Connect carries why it failed, and
	// that is the discovery reason (#75).
	companionURL, err := companion.Discover(ctx, cfg, ws)
	if err != nil {
		slog.Debug("companion discovery failed", "error", err)
		var de *companion.DiscoveryError
		if errors.As(err, &de) {
			return companionProbe{
				status:       "not found (" + string(de.Reason) + ")",
				headline:     "not found",
				reason:       de.Reason,
				configErrors: notFoundErrors,
			}
		}
		return companionProbe{status: "not found", headline: "not found", configErrors: notFoundErrors}
	}

	// Health check
	cc := companion.New(companionURL, cfg.CompanionToken)
	if ws.Connected() {
		cc = cc.WithIngressAuth(ws)
	}
	health, err := cc.Health(ctx)
	if err != nil {
		slog.Debug("companion health check failed", "error", err)
		if checkConfig {
			notFoundErrors = "companion unreachable"
		}
		return companionProbe{status: "unreachable", headline: "unreachable", configErrors: notFoundErrors}
	}

	status := health.Status
	ver := health.Version

	// Version compatibility check: warn if major version diff > 2
	if ver != "" {
		if warn := checkVersionCompat(version, ver); warn != "" {
			slog.Warn(warn)
			status += " (version mismatch)"
		}
	}

	var configValid *bool
	configErrors := ""
	if checkConfig {
		valid, errs, err := cc.WithTimeout(90 * time.Second).CheckConfig(ctx)
		if err != nil {
			configErrors = err.Error()
		} else {
			configValid = &valid
			configErrors = errs
		}
	}

	return companionProbe{
		status:       status,
		headline:     status,
		version:      ver,
		configValid:  configValid,
		configErrors: configErrors,
	}
}

// compatWindowMonths is how far the CLI and the companion may drift apart
// before `hactl health` says so. The two ship in lockstep every month
// (companion first, then hactl), so a month of skew is routine — a pair that
// has not been completed yet. Beyond that a release was skipped outright, and
// the CLI is likely calling routes the installed companion does not serve.
const compatWindowMonths = 2

// checkVersionCompat compares the hactl and companion versions and returns a
// warning when they have drifted too far apart, empty otherwise.
//
// Both sides version as CalVer YYYY.M.PATCH, mirroring Home Assistant. That
// makes the leading field the *year*, so comparing leading fields answers a
// question nobody asked: hactl 2026.8 against companion 2026.1 is seven months
// of API drift and zero years apart. The distance that matters here is months,
// so months is what this measures.
//
// A version that is not CalVer goes unjudged — a `dev` build, or a companion
// reporting some other scheme. Silence is the honest answer when the two
// numbers are not on the same scale.
func checkVersionCompat(hactlVersion, companionVersion string) string {
	hMonth, hOK := calverMonth(hactlVersion)
	cMonth, cOK := calverMonth(companionVersion)
	if !hOK || !cOK {
		return ""
	}
	diff := hMonth - cMonth
	if diff < 0 {
		diff = -diff
	}
	if diff > compatWindowMonths {
		return fmt.Sprintf("companion version %s may be incompatible with hactl %s (%d months apart)", companionVersion, hactlVersion, diff)
	}
	return ""
}

// calverMonth converts a CalVer version to an absolute month count, so that two
// versions can be subtracted. Reports false when v is not YYYY.M[.PATCH]: the
// year floor and the month range together reject a semver-shaped string, which
// would otherwise be read as year 1 and yield a distance of two thousand years.
func calverMonth(v string) (int, bool) {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) < 2 {
		return 0, false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil || year < 2000 {
		return 0, false
	}
	month, err := strconv.Atoi(parts[1])
	if err != nil || month < 1 || month > 12 {
		return 0, false
	}
	return year*12 + month, true
}
