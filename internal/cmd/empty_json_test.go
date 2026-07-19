package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// withFlagJSON sets flagJSON for the duration of a test and restores it
// afterward, mirroring withFlagDir above.
func withFlagJSON(t *testing.T, on bool) {
	t.Helper()
	old := flagJSON
	flagJSON = on
	t.Cleanup(func() { flagJSON = old })
}

// assertValidJSON fails the test if out does not parse as JSON, and returns
// the decoded value for further assertions.
func assertValidJSON(t *testing.T, out string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %q", err, out)
	}
	return v
}

// assertEmptyJSONArray fails unless out is exactly a JSON array with zero
// elements — the shape every affected command must emit instead of prose.
func assertEmptyJSONArray(t *testing.T, out string) {
	t.Helper()
	v := assertValidJSON(t, out)
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("output is not a JSON array: %q", out)
	}
	if len(arr) != 0 {
		t.Fatalf("expected an empty array, got %d element(s): %q", len(arr), out)
	}
}

// --- Issue #71 (H3): --json must never fall through to bare prose on an
// empty result. Each test below hits a command's empty-result path with
// --json active and asserts stdout is valid (empty) JSON, not prose like
// "no areas found".

func TestEmptyResultJSON_AreaLs(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/list": []any{},
	}, nil)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)

	var buf bytes.Buffer
	if err := runAreaLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAreaLs failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

func TestEmptyResultJSON_LabelLs(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/label_registry/list": []any{},
	}, nil)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)

	var buf bytes.Buffer
	if err := runLabelLs(context.Background(), &buf); err != nil {
		t.Fatalf("runLabelLs failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

func TestEmptyResultJSON_FloorLs(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/floor_registry/list": []any{},
	}, nil)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)

	var buf bytes.Buffer
	if err := runFloorLs(context.Background(), &buf); err != nil {
		t.Fatalf("runFloorLs failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

func TestEmptyResultJSON_DeviceLs(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/device_registry/list": []any{},
		"config/entity_registry/list": []any{},
		"config/area_registry/list":   []any{},
		"config/label_registry/list":  []any{},
	}, nil)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)
	oldPattern, oldName, oldArea, oldLabel := flagDevicePattern, flagDeviceName, flagDeviceArea, flagDeviceLabel
	flagDevicePattern, flagDeviceName, flagDeviceArea, flagDeviceLabel = "", "", "", ""
	defer func() {
		flagDevicePattern, flagDeviceName, flagDeviceArea, flagDeviceLabel = oldPattern, oldName, oldArea, oldLabel
	}()

	var buf bytes.Buffer
	if err := runDeviceLs(context.Background(), &buf); err != nil {
		t.Fatalf("runDeviceLs failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

func TestEmptyResultJSON_ConfigEntries(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/config_entries/entry": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)
	old := flagConfigDomain
	flagConfigDomain = ""
	defer func() { flagConfigDomain = old }()

	var buf bytes.Buffer
	if err := runConfigEntries(context.Background(), &buf); err != nil {
		t.Fatalf("runConfigEntries failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

func TestEmptyResultJSON_Issues_EmptyArray(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/repairs/issues": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"issues":[]}`)
		},
	})
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)

	var buf bytes.Buffer
	if err := runIssues(context.Background(), &buf); err != nil {
		t.Fatalf("runIssues failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

// TestEmptyResultJSON_Issues_404 covers the other empty-result branch: HA
// versions without the repairs API return 404, which runIssues treats as a
// valid empty result rather than an error.
func TestEmptyResultJSON_Issues_404(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/repairs/issues": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
	})
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)

	var buf bytes.Buffer
	if err := runIssues(context.Background(), &buf); err != nil {
		t.Fatalf("runIssues failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

func TestEmptyResultJSON_Changes(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)
	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()

	var buf bytes.Buffer
	if err := runChanges(context.Background(), &buf); err != nil {
		t.Fatalf("runChanges failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

// TestEmptyResultJSON_EntRelated covers ent.go's two-message empty branch
// (known vs. stale/deleted entity), which — unlike the other sites — calls
// writeEmptyJSONArray directly instead of going through emitEmptyList.
func TestEmptyResultJSON_EntRelated(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []any{},
		"config/area_registry/list":   []any{},
		"config/label_registry/list":  []any{},
		"config/floor_registry/list":  []any{},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)
	old := flagEntStale
	flagEntStale = false
	defer func() { flagEntStale = old }()

	var buf bytes.Buffer
	if err := runEntRelated(context.Background(), &buf, "sensor.gone"); err != nil {
		t.Fatalf("runEntRelated failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

// TestEmptyResultJSON_NonJSONStillProse is a control: the same empty area
// listing without --json must still print human prose, not JSON.
func TestEmptyResultJSON_NonJSONStillProse(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/list": []any{},
	}, nil)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, false)

	var buf bytes.Buffer
	if err := runAreaLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAreaLs failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no areas") {
		t.Errorf("non-json empty result should still say 'no areas': %q", buf.String())
	}
}
