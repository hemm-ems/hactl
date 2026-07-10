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
	// url_path comes from HA's dashboard list, but it still flows into a file
	// path, so reduce it to a single safe filename component and then verify the
	// result stays inside the snapshot dir (defense in depth against traversal).
	name := sanitizeSnapshotName(urlPath)
	ts := time.Now().UTC().Format("20060102T150405")
	path := filepath.Join(dir, name+"."+ts+".json")
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to write snapshot outside %s", dir)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("writing snapshot: %w", err)
	}
	return path, nil
}

// sanitizeSnapshotName reduces an arbitrary dashboard url_path to a single safe
// path component: every byte outside [A-Za-z0-9._-] becomes "_", and leading/
// trailing dots are stripped so the result can never be "", ".", ".." or any
// traversal sequence.
func sanitizeSnapshotName(urlPath string) string {
	b := make([]byte, 0, len(urlPath))
	for i := range len(urlPath) {
		c := urlPath[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	name := strings.Trim(string(b), ".")
	if name == "" {
		return "default"
	}
	return name
}
