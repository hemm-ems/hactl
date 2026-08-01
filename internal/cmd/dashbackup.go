package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hemm-ems/hactl/internal/backupfile"
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
	state, prev, err := classifyDashboardConfig(ctx, ws, urlPath)
	switch state {
	case dashConfigAbsent:
		// The first save for a dashboard that has none. There is nothing to
		// snapshot and nothing to warn about: warning "could not fetch current
		// dashboard config" here reported an absence as a failure, which is
		// finding #3's class one command over.
		slog.Debug("no stored dashboard config to snapshot", "url_path", urlPath)
		return
	case dashConfigUnclassifiable:
		slog.Warn("could not fetch current dashboard config; overwriting without a snapshot",
			"url_path", urlPath, "error", err)
		return
	case dashConfigStored:
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
	// url_path comes from HA's dashboard list, but it still flows into a file
	// path, so reduce it to a single safe filename component and then verify the
	// result stays inside the snapshot dir (defense in depth against traversal).
	name := sanitizeSnapshotName(urlPath)
	// Checked BEFORE anything is created, on the name rather than on the
	// finished path: a containment check that runs after the write has already
	// happened is not a check, it is a report.
	if !strings.HasPrefix(filepath.Join(dir, name), dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to write snapshot outside %s", dir)
	}
	// Named through backupfile, which will not write over a name that is
	// already taken (H-26). The stamp used to be `20060102T150405` in UTC, so
	// two dashboard writes inside one second left one snapshot where the caller
	// had been told there were two — the same defect as the automation and
	// script backups, in the third of the three places hactl names a file from
	// a clock.
	return backupfile.Write(dir, 0o600, raw, func(stamp string) string {
		return name + "." + stamp + ".json"
	})
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
