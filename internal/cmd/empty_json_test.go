package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// --- Filtered-path empty results. These sites short-circuit on a filter that
// matches nothing (--domain/--label/--failing) or a scan/replace with zero
// hits, ahead of the same function's flagJSON-aware render. Each must emit "[]"
// under --json rather than the filter's human hint.

// TestEmptyResultJSON_EntLs_DomainFilter covers ent.go's `--domain` branch: a
// domain with zero matching entities must not print domainNotFoundHint under
// --json.
func TestEmptyResultJSON_EntLs_DomainFilter(t *testing.T) {
	ts := startCmdServer(t, nil, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)
	oldDomain, oldPattern, oldArea, oldLabel, oldRestored := flagEntDomain, flagEntPattern, flagEntArea, flagEntLabel, flagEntRestored
	flagEntDomain, flagEntPattern, flagEntArea, flagEntLabel, flagEntRestored = "sensor", "", "", "", false
	defer func() {
		flagEntDomain, flagEntPattern, flagEntArea, flagEntLabel, flagEntRestored = oldDomain, oldPattern, oldArea, oldLabel, oldRestored
	}()

	var buf bytes.Buffer
	if err := runEntLs(context.Background(), &buf); err != nil {
		t.Fatalf("runEntLs failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

// TestEmptyResultJSON_EntLs_LabelFilter covers ent.go's `--label` branch: a
// label absent from the registry must not print labelNotFoundHint under --json.
func TestEmptyResultJSON_EntLs_LabelFilter(t *testing.T) {
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
	oldDomain, oldPattern, oldArea, oldLabel, oldRestored := flagEntDomain, flagEntPattern, flagEntArea, flagEntLabel, flagEntRestored
	flagEntDomain, flagEntPattern, flagEntArea, flagEntLabel, flagEntRestored = "", "", "", "nonexistent", false
	defer func() {
		flagEntDomain, flagEntPattern, flagEntArea, flagEntLabel, flagEntRestored = oldDomain, oldPattern, oldArea, oldLabel, oldRestored
	}()

	var buf bytes.Buffer
	if err := runEntLs(context.Background(), &buf); err != nil {
		t.Fatalf("runEntLs failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

// TestEmptyResultJSON_AutoLs_Failing covers auto.go's `--failing` branch: a
// registry with no failing automations must not print failingEmptyHint under
// --json.
func TestEmptyResultJSON_AutoLs_Failing(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []any{},
		"config/area_registry/list":   []any{},
		"config/label_registry/list":  []any{},
		"config/floor_registry/list":  []any{},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// One healthy automation: a non-empty listing with zero failing rows.
			_, _ = fmt.Fprint(w, `[{"entity_id":"automation.healthy","state":"on","attributes":{}}]`)
		},
	})
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)
	oldFailing, oldPattern, oldLabel, oldRestored, oldSince := flagAutoFailing, flagAutoPattern, flagAutoLabel, flagAutoRestored, flagSince
	flagAutoFailing, flagAutoPattern, flagAutoLabel, flagAutoRestored, flagSince = true, "", "", false, "24h"
	defer func() {
		flagAutoFailing, flagAutoPattern, flagAutoLabel, flagAutoRestored, flagSince = oldFailing, oldPattern, oldLabel, oldRestored, oldSince
	}()

	var buf bytes.Buffer
	if err := runAutoLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAutoLs failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

// TestEmptyResultJSON_RefScan covers ref.go's `ref scan` empty branch: a target
// referenced nowhere must emit "[]" under --json, not the "not referenced" note.
func TestEmptyResultJSON_RefScan(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"target":"sensor.absent","hits":[]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          dashboardConfigWith("sensor.other"),
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)

	var buf bytes.Buffer
	if err := runRefScan(context.Background(), &buf, "sensor.absent"); err != nil {
		t.Fatalf("runRefScan failed: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

// TestEmptyResultJSON_RefReplace covers ref.go's `ref replace` empty branch: an
// old value found nowhere must emit "[]" under --json, not the "not found" note.
func TestEmptyResultJSON_RefReplace(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"dry_run","changes":[]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/info":            map[string]any{"mode": "storage"},
		"lovelace/config":          dashboardConfigWith("sensor.other"),
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)
	withRefConfirm(t, false)

	var buf bytes.Buffer
	if err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new"); err != nil {
		t.Fatalf("runRefReplace failed: %v", err)
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
