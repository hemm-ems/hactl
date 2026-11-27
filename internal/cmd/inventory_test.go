package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// inventoryStates: a light on a named device, a template sensor with no
// device, and a state-only entity with no registry row at all.
const inventoryStates = `[
 {"entity_id":"light.flur","state":"on","last_changed":"2026-11-27T06:00:00Z","attributes":{}},
 {"entity_id":"sensor.tpl","state":"3","last_changed":"2026-11-27T06:00:00Z","attributes":{}},
 {"entity_id":"sensor.orphan","state":"1","last_changed":"2026-11-27T06:00:00Z","attributes":{}},
 {"entity_id":"binary_sensor.hidden","state":"off","last_changed":"2026-11-27T06:00:00Z","attributes":{}}
]`

func inventoryWS(withDevices bool) map[string]any {
	ws := map[string]any{
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "light.flur", "platform": "hue", "device_id": "dev1", "config_entry_id": "entry_hue"},
			{"entity_id": "sensor.tpl", "platform": "template"},
			// Its device row is absent, as when the policy profile filters it
			// out of config/device_registry/list (reqmap FilterDeviceRegistry).
			{"entity_id": "binary_sensor.hidden", "platform": "zha", "device_id": "dev_hidden", "config_entry_id": "entry_zha"},
		},
		"config/area_registry/list":  []any{},
		"config/label_registry/list": []any{},
		"config/floor_registry/list": []any{},
	}
	if withDevices {
		ws["config/device_registry/list"] = []map[string]any{
			{"id": "dev1", "name": "Hue Bulb", "name_by_user": "Flur Licht", "config_entries": []string{"entry_hue"}},
		}
	}
	return ws
}

func inventoryServer(t *testing.T, ws map[string]any) *cmdTestServer {
	t.Helper()
	return startCmdServer(t, ws, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(inventoryStates))
		},
		"/api/config/config_entries/entry": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
			 {"entry_id":"entry_hue","domain":"hue","title":"Hue","state":"loaded","source":"user"},
			 {"entry_id":"entry_dead","domain":"tibber","title":"Tibber","state":"setup_error","source":"user"}]`))
		},
	})
}

func resetEntLsFilters(t *testing.T) {
	t.Helper()
	for _, p := range []*string{&flagEntDomain, &flagEntPattern, &flagEntArea, &flagEntLabel, &flagEntPlatform, &flagEntDevice} {
		setFlagForTest(t, p, "")
	}
	setFlagForTest(t, &flagEntRestored, false)
}

func entLsJSON(t *testing.T) (map[string]map[string]any, error) {
	t.Helper()
	withFlagJSON(t, true)
	var buf bytes.Buffer
	if err := runEntLs(listingCmd(context.Background(), "ent", "ls"), &buf); err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("--json: %v\n%s", err, buf.String())
	}
	out := map[string]map[string]any{}
	for _, r := range rows {
		id, _ := r["entity_id"].(string)
		out[id] = r
	}
	return out, nil
}

// TestEntLsCarriesOwnership: platform and device in the table, the two ids
// as JSON keys, and empty (never omitted) where the registry has nothing.
func TestEntLsCarriesOwnership(t *testing.T) {
	ts := inventoryServer(t, inventoryWS(true))
	withFlagDir(t, ts.dir)
	resetEntLsFilters(t)

	var text bytes.Buffer
	if err := runEntLs(listingCmd(context.Background(), "ent", "ls"), &text); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"platform", "device", "hue", "Flur Licht", "template"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, text.String())
		}
	}
	if strings.Contains(text.String(), "entry_hue") || strings.Contains(text.String(), "dev1") {
		t.Errorf("the ids belong to JSON only (D-236):\n%s", text.String())
	}

	rows, err := entLsJSON(t)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][4]string{
		"light.flur":    {"hue", "Flur Licht", "dev1", "entry_hue"},
		"sensor.tpl":    {"template", "", "", ""},
		"sensor.orphan": {"", "", "", ""},
		// The registry row names a device the answer did not include: nothing
		// about that device is printed, not even its id (D-236).
		"binary_sensor.hidden": {"zha", "", "", "entry_zha"},
	}
	for id, w := range want {
		r := rows[id]
		for i, key := range []string{"platform", "device", "device_id", "config_entry_id"} {
			v, present := r[key]
			if !present || v != w[i] {
				t.Errorf("%s %s = %#v (present=%v), want %q", id, key, v, present, w[i])
			}
		}
	}
}

func TestEntLsPlatformAndDeviceFilters(t *testing.T) {
	ts := inventoryServer(t, inventoryWS(true))
	withFlagDir(t, ts.dir)
	for _, tc := range []struct {
		flag  *string
		value string
		want  string
	}{
		{&flagEntPlatform, "HUE", "light.flur"},
		{&flagEntPlatform, "templ", ""}, // exact, not substring
		{&flagEntDevice, "flur", "light.flur"},
		{&flagEntDevice, "hue bulb", "light.flur"},
		{&flagEntDevice, "DEV1", "light.flur"},
		{&flagEntDevice, "dev", ""}, // the id matches exactly, not as a substring
	} {
		resetEntLsFilters(t)
		setFlagForTest(t, tc.flag, tc.value)
		rows, err := entLsJSON(t)
		if err != nil {
			t.Fatalf("%q: %v", tc.value, err)
		}
		got := make([]string, 0, len(rows))
		for id := range rows {
			got = append(got, id)
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("filter %q kept %v, want %q", tc.value, got, tc.want)
		}
	}
}

// TestEntLsRegistryFiltersFailWithoutRegistry: a registry-bound filter used
// to be skipped when the registry could not be read, so the narrowed
// question came back as every entity (H-7, D-236).
func TestEntLsRegistryFiltersFailWithoutRegistry(t *testing.T) {
	noRegistry := map[string]any{} // every WS command fails
	for _, tc := range []struct {
		name string
		ws   map[string]any
		flag *string
		want string
	}{
		{"area", noRegistry, &flagEntArea, "--area needs the entity registry"},
		{"label", noRegistry, &flagEntLabel, "--label needs the entity registry"},
		{"platform", noRegistry, &flagEntPlatform, "--platform needs the entity registry"},
		{"device", inventoryWS(false), &flagEntDevice, "--device needs the device registry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := inventoryServer(t, tc.ws)
			withFlagDir(t, ts.dir)
			resetEntLsFilters(t)
			setFlagForTest(t, tc.flag, "x")
			var buf bytes.Buffer
			err := runEntLs(listingCmd(context.Background(), "ent", "ls"), &buf)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q; output:\n%s", err, tc.want, buf.String())
			}
		})
	}

	// Without a filter the listing still renders: the columns are an addition.
	ts := inventoryServer(t, noRegistry)
	withFlagDir(t, ts.dir)
	resetEntLsFilters(t)
	if rows, err := entLsJSON(t); err != nil || len(rows) != 4 {
		t.Fatalf("unfiltered listing without registry: rows=%d err=%v", len(rows), err)
	}
}

func configEntriesByID(t *testing.T) map[string]map[string]any {
	t.Helper()
	setFlagForTest(t, &flagConfigDomain, "")
	withFlagJSON(t, true)
	var buf bytes.Buffer
	if err := runConfigEntries(listingCmd(context.Background(), "config", "entries"), &buf); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("--json: %v\n%s", err, buf.String())
	}
	out := map[string]map[string]any{}
	for _, r := range rows {
		id, _ := r["entry_id"].(string)
		out[id] = r
	}
	return out
}

func TestConfigEntriesCountsOwnedRows(t *testing.T) {
	ts := inventoryServer(t, inventoryWS(true))
	withFlagDir(t, ts.dir)
	rows := configEntriesByID(t)
	for id, want := range map[string][2]float64{"entry_hue": {1, 1}, "entry_dead": {0, 0}} {
		if rows[id]["entities"] != want[0] || rows[id]["devices"] != want[1] {
			t.Errorf("%s: entities=%v devices=%v, want %v", id, rows[id]["entities"], rows[id]["devices"], want)
		}
	}
	if n := ts.cmdCounts["config/entity_registry/list"] + ts.cmdCounts["config/device_registry/list"]; n != 2 {
		t.Errorf("registry reads = %d, want 2 (one per registry, never per entry)", n)
	}
}

// TestConfigEntriesCountsUnknownWhenRegistryFails: no answer is `-` / null,
// never 0 — a 0 would read as "safe to delete" (D-237).
func TestConfigEntriesCountsUnknownWhenRegistryFails(t *testing.T) {
	ts := inventoryServer(t, inventoryWS(false)) // device registry fails
	withFlagDir(t, ts.dir)
	rows := configEntriesByID(t)
	r := rows["entry_hue"]
	if r["entities"] != float64(1) {
		t.Errorf("entities = %v, want 1 (its registry was readable)", r["entities"])
	}
	if v, present := r["devices"]; !present || v != nil {
		t.Errorf("devices = %#v (present=%v), want null", v, present)
	}

	withFlagJSON(t, false)
	var text bytes.Buffer
	if err := runConfigEntries(listingCmd(context.Background(), "config", "entries"), &text); err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(text.String(), "\n") {
		if strings.HasPrefix(line, "entry_hue") && !strings.HasSuffix(strings.TrimSpace(line), " -") {
			t.Errorf("unknown device count must render as '-': %q", line)
		}
	}
}
