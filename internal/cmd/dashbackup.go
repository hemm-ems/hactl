package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// snapshotDashboardBeforeSave writes a storage-mode dashboard's current on-disk
// config to the instance backup dir before it is overwritten, so a bad rewrite
// can be restored (and to enable a future `dash undo`). This mirrors the YAML
// side, which already backs up before every write.
//
// Best-effort: a snapshot failure is logged, never fatal. Refusing to save
// because we couldn't back up would be worse than the asymmetry it fixes.
func snapshotDashboardBeforeSave(ctx context.Context, ws *haapi.WSClient, urlPath string) {
	prev, err := ws.DashboardConfigRaw(ctx, urlPath)
	if err != nil {
		slog.Warn("could not fetch current dashboard config; overwriting without a snapshot",
			"url_path", urlPath, "error", err)
		return
	}
	path, err := writeDashboardSnapshot(flagDir, urlPath, prev)
	if err != nil {
		slog.Warn("could not write dashboard snapshot; overwriting anyway", "url_path", urlPath, "error", err)
		return
	}
	slog.Debug("dashboard snapshot written", "url_path", urlPath, "path", path)
}

// writeDashboardSnapshot writes raw to <instance>/backups/dashboards/<name>.<ts>.json
// and returns the path. Split out from the WS fetch so it is unit-testable.
func writeDashboardSnapshot(dirFlag, urlPath string, raw []byte) (string, error) {
	base := config.BestEffortDir(dirFlag)
	if base == "" {
		return "", errors.New("could not resolve hactl instance directory")
	}
	dir := filepath.Join(base, "backups", "dashboards")
	if err := os.MkdirAll(filepath.Clean(dir), 0o750); err != nil {
		return "", fmt.Errorf("creating snapshot dir: %w", err)
	}
	name := urlPath
	if name == "" {
		name = "default"
	}
	name = strings.ReplaceAll(name, "/", "_")
	ts := time.Now().UTC().Format("20060102T150405")
	path := filepath.Join(dir, name+"."+ts+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("writing snapshot: %w", err)
	}
	return path, nil
}
