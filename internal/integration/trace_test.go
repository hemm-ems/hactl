//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// triggerAutomation fires an automation via the HA REST API so that traces exist.
func triggerAutomation(t *testing.T, automationID string) {
	t.Helper()
	cfg := loadConfig(t)
	client := haapi.New(cfg.URL, cfg.Token)
	ctx := context.Background()
	err := client.CallService(ctx, "automation", "trigger", map[string]any{
		"entity_id": "automation." + automationID,
	})
	if err != nil {
		t.Fatalf("trigger automation %s: %v", automationID, err)
	}
}

// extractTraceID finds the first trc:XX stable ID in hactl output.
func extractTraceID(out string) string {
	for l := range strings.SplitSeq(out, "\n") {
		if !strings.Contains(l, "trc:") {
			continue
		}
		for f := range strings.FieldsSeq(l) {
			if strings.HasPrefix(f, "trc:") {
				return f
			}
		}
	}
	return ""
}

// getFirstAutoEntityID returns the entity_id of the first automation from auto ls --json.
// Returns empty string if none available.
func getFirstAutoEntityID(t *testing.T) string {
	t.Helper()
	out := runHactl(t, "auto", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) == 0 {
		return ""
	}
	return entries[0]["id"]
}

// waitForTrace triggers an automation then polls `auto show` until a trace ID appears (up to 5s).
func waitForTrace(t *testing.T, autoID string) string {
	t.Helper()
	triggerAutomation(t, autoID)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := runHactlErr(t, "auto", "show", autoID)
		if err == nil {
			if id := extractTraceID(out); id != "" {
				return id
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("no trace appeared for %s within 5s after trigger", autoID)
	return ""
}

func TestTraceShowCondensed(t *testing.T) {
	autoID := getFirstAutoEntityID(t)
	if autoID == "" {
		t.Skip("no automations available")
	}

	traceID := waitForTrace(t, autoID)

	// Now test trace show with the condensed format
	traceOut := runHactl(t, "trace", "show", traceID)
	assertNotContains(t, traceOut, "panic")
	// Condensed trace should have step-like output (trigger, action, etc.)
	if len(traceOut) == 0 {
		t.Error("trace show returned empty output")
	}
}

func TestTraceShowFull(t *testing.T) {
	autoID := getFirstAutoEntityID(t)
	if autoID == "" {
		t.Skip("no automations available")
	}

	traceID := waitForTrace(t, autoID)

	traceOut := runHactl(t, "trace", "show", traceID, "--full", "--tokensmax=0")
	// Full output should be valid JSON
	trimmed := strings.TrimSpace(stripTokenHeader(traceOut))
	if !strings.HasPrefix(trimmed, "{") {
		t.Errorf("trace show --full expected JSON object, got: %.100s", trimmed)
	}
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		t.Errorf("trace show --full returned invalid JSON: %v", err)
	}
}

func TestTraceShowInvalidID(t *testing.T) {
	_, err := runHactlErr(t, "trace", "show", "trc:nonexistent99")
	if err == nil {
		t.Error("trace show with invalid ID expected error, got nil")
	}
}

func TestTraceShowBadFormat(t *testing.T) {
	_, err := runHactlErr(t, "trace", "show", "not-a-trace-id")
	if err == nil {
		t.Error("trace show with bad format expected error, got nil")
	}
}
