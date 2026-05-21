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

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var healthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show Home Assistant health overview",
	Long:  "Display HA version, recorder status, and error count.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHealth(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
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
	companionStatus, companionVersion := discoverCompanion(ctx, cfg)
	hr.CompanionStatus = companionStatus
	hr.CompanionVersion = companionVersion

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
		_, _ = fmt.Fprintf(w, "⚠ SAFE MODE ACTIVE\n")
	}

	// Companion status line
	if companionStatus != "" {
		if companionVersion != "" {
			_, _ = fmt.Fprintf(w, "companion=%s  version=%s\n", companionStatus, companionVersion)
		} else {
			_, _ = fmt.Fprintf(w, "companion=%s\n", companionStatus)
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

// discoverCompanion attempts to find and health-check the companion.
// Returns (status, version). Non-fatal: returns ("not found", "") if unavailable.
func discoverCompanion(ctx context.Context, cfg *config.Config) (string, string) {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	var wsConnected bool
	if err := ws.Connect(ctx); err == nil {
		wsConnected = true
		defer func() { _ = ws.Close() }()
	}

	var wsClient *haapi.WSClient
	if wsConnected {
		wsClient = ws
	}

	companionURL, err := companion.Discover(ctx, cfg, wsClient)
	if err != nil {
		slog.Debug("companion discovery failed", "error", err)
		var de *companion.DiscoveryError
		if errors.As(err, &de) {
			return "not found (" + string(de.Reason) + ")", ""
		}
		return "not found", ""
	}

	// Health check
	cc := companion.New(companionURL, cfg.CompanionToken)
	if wsClient != nil {
		cc = cc.WithSigner(wsClient)
	}
	health, err := cc.Health(ctx)
	if err != nil {
		slog.Debug("companion health check failed", "error", err)
		return "unreachable", ""
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

	return status, ver
}

// checkVersionCompat compares hactl and companion major versions.
// Returns a warning string if major versions differ by more than 2, empty otherwise.
func checkVersionCompat(hactlVersion, companionVersion string) string {
	hMajor := parseMajor(hactlVersion)
	cMajor := parseMajor(companionVersion)
	if hMajor < 0 || cMajor < 0 {
		return ""
	}
	diff := hMajor - cMajor
	if diff < 0 {
		diff = -diff
	}
	if diff > 2 {
		return fmt.Sprintf("companion version %s may be incompatible with hactl %s (major version diff: %d)", companionVersion, hactlVersion, diff)
	}
	return ""
}

// parseMajor extracts the major version number from a semver-like string.
// Returns -1 if the version cannot be parsed.
func parseMajor(v string) int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 2)
	if len(parts) == 0 {
		return -1
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return -1
	}
	return n
}
