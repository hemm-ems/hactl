package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEntAnomaliesMintsNoIdentifier is the standing gate for D-5
// (docs/decisions.md): `ent anomalies` prints no identifier, in any format.
//
// The command used to mint stable `anom:` short ids into cache/ids.json and
// print them in an "id" column, while no command accepted one — `log show`
// explicitly rejects them (`TestRunLogShow_RejectsForeignNamespace`), and the
// key shape collision with log keys made a resolved anom: id print an
// unrelated record's fields (D7). An identifier without a consumer does not
// get printed (H-11); the minting was deleted (D69), and D-5 rules that
// re-minting must arrive in the same PR as its consumer.
//
// The gate therefore pins, for both renderers (the numeric DetectAll path and
// the non-numeric state-duration fallback) and both formats:
//
//   - no `anom:` token anywhere in the output;
//   - the JSON row shape is exactly {type, time, detail} — a new column here
//     is a deliberate act, and if it is an identifier, D-5 requires its
//     consumer in the same PR;
//   - cache/ids.json gains no `anom:` entries as a side effect (the old
//     minting grew the id registry on every run).
func TestEntAnomaliesMintsNoIdentifier(t *testing.T) {
	// Numeric path: three flat points then a change — the 2h15m hole is a
	// gap anomaly (>1h), the 2h45m run at 20.0 is a stuck anomaly (>=2h).
	numericHist := `[[
		{"entity_id":"sensor.power","state":"20.0","last_changed":"2026-01-01T00:00:00+00:00"},
		{"entity_id":"sensor.power","state":"20.0","last_changed":"2026-01-01T00:30:00+00:00"},
		{"entity_id":"sensor.power","state":"20.0","last_changed":"2026-01-01T02:45:00+00:00"},
		{"entity_id":"sensor.power","state":"21.0","last_changed":"2026-01-01T02:50:00+00:00"}
	]]`
	// State-duration path: a non-numeric entity stuck "on" since 2020
	// (>=24h), forcing renderStateAnomalies.
	stateHist := `[[
		{"entity_id":"binary_sensor.door","state":"on","last_changed":"2020-01-01T00:00:00+00:00"}
	]]`

	cases := []struct {
		name     string
		entityID string
		hist     string
	}{
		{"numeric", "sensor.power", numericHist},
		{"state_duration", "binary_sensor.door", stateHist},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
				"/api/history/period/": func(w http.ResponseWriter, _ *http.Request) {
					_, _ = fmt.Fprint(w, tc.hist)
				},
			})
			withFlagDir(t, ts.dir)

			oldSince, oldJSON := flagSince, flagJSON
			flagSince = "3000d" // comfortably covers the fixture timestamps
			defer func() { flagSince, flagJSON = oldSince, oldJSON }()

			flagJSON = false
			textOut := runAnomaliesForGate(t, tc.entityID, "text")
			if !strings.Contains(textOut, "anomalies") {
				t.Fatalf("fixture produced no anomaly rows — the gate asserted nothing:\n%s", textOut)
			}
			if strings.Contains(textOut, "anom:") {
				t.Errorf("ent anomalies (text) printed an anom: identifier — no command consumes one (D-5):\n%s", textOut)
			}

			flagJSON = true
			jsonOut := runAnomaliesForGate(t, tc.entityID, "json")
			if strings.Contains(jsonOut, "anom:") {
				t.Errorf("ent anomalies --json printed an anom: identifier — no command consumes one (D-5):\n%s", jsonOut)
			}
			assertAnomalyRowShape(t, jsonOut)
			assertNoAnomRegistryEntries(t, ts.dir)
		})
	}
}

// runAnomaliesForGate runs `ent anomalies` for entityID under the current
// flags and hands back its output.
func runAnomaliesForGate(t *testing.T, entityID, mode string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := runEntAnomalies(context.Background(), &buf, entityID); err != nil {
		t.Fatalf("runEntAnomalies (%s): %v", mode, err)
	}
	return buf.String()
}

// assertAnomalyRowShape pins the `--json` row shape to exactly
// {type, time, detail}: a grown column is a deliberate act, and if it is an
// identifier, D-5 requires its consumer in the same PR.
func assertAnomalyRowShape(t *testing.T, jsonOut string) {
	t.Helper()
	var rows []map[string]string
	if err := json.Unmarshal([]byte(jsonOut), &rows); err != nil {
		t.Fatalf("ent anomalies --json output not valid JSON: %v\n%s", err, jsonOut)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one anomaly row from the fixture")
	}
	for _, row := range rows {
		for key := range row {
			switch key {
			case "type", "time", "detail":
			default:
				t.Errorf("ent anomalies --json row grew a %q column — if it is an identifier, D-5 requires its consumer in the same PR; row: %v", key, row)
			}
		}
	}
}

// assertNoAnomRegistryEntries checks the side effect: the old minting
// persisted anom: entries into cache/ids.json on every run. The registry
// file must not have grown any.
func assertNoAnomRegistryEntries(t *testing.T, dir string) {
	t.Helper()
	idsPath := filepath.Join(dir, "cache", "ids.json")
	data, err := os.ReadFile(filepath.Clean(idsPath))
	if os.IsNotExist(err) {
		return // no registry written at all — nothing minted
	}
	if err != nil {
		t.Fatalf("reading %s: %v", idsPath, err)
	}
	if strings.Contains(string(data), "anom:") {
		t.Errorf("ent anomalies wrote anom: entries into %s:\n%s", idsPath, data)
	}
}
