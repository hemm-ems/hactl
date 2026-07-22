package cmd

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/config"
)

// captureDefaultLogger redirects slog's default logger to a buffer at the
// DEFAULT level (Info) — a Debug-only message is invisible here, which is the
// whole point: `ent related` must not hide a companion failure below the
// threshold the user actually runs at.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// Companion discovery failing means the config/YAML half of the relation graph
// was never consulted. `ent related` feeds delete decisions, so a partial graph
// must be announced at the default log level, not whispered at Debug.
func TestFindCompanionRelations_WarnsWhenDiscoveryFails(t *testing.T) {
	logBuf := captureDefaultLogger(t)

	// No COMPANION_URL and no WS client: discovery cannot succeed.
	cfg := &config.Config{URL: "http://localhost:9999", Token: "tok"}

	related, staleRefs := findCompanionRelations(context.Background(), cfg, nil, "sensor.temperature", false)
	if len(related) != 0 || len(staleRefs) != 0 {
		t.Fatalf("precondition: expected no results, got %d related / %d stale", len(related), len(staleRefs))
	}
	assertIncompleteGraphWarning(t, logBuf.String())
}

// Same contract for a companion that is reachable but whose related-graph call
// fails: the answer is incomplete and must say so.
func TestFindCompanionRelations_WarnsWhenGraphCallFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	logBuf := captureDefaultLogger(t)
	cfg := &config.Config{URL: "http://localhost:9999", Token: "tok", CompanionURL: srv.URL}

	related, staleRefs := findCompanionRelations(context.Background(), cfg, nil, "sensor.temperature", false)
	if len(related) != 0 || len(staleRefs) != 0 {
		t.Fatalf("precondition: expected no results, got %d related / %d stale", len(related), len(staleRefs))
	}
	assertIncompleteGraphWarning(t, logBuf.String())
}

// assertIncompleteGraphWarning requires a WARN line that says the config scan
// did not happen. A bare WARN is not enough — the companion HTTP client already
// logs "retrying companion request" warnings that say nothing about the answer
// being incomplete.
func assertIncompleteGraphWarning(t *testing.T, log string) {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "not scanned") {
			return
		}
	}
	t.Errorf("no WARN line reporting that config files were not scanned; log was:\n%s", log)
}
