package cmd

// D-4 (docs/decisions.md): `ent show`'s changed_by and `ent who` answer "who
// changed this entity" through ONE shared resolution — the logbook's answer
// when the logbook has one, the state's own context otherwise — and every
// answer names which source produced it. The defect this converges (D70/R20):
// the two commands read two different HA sources, and HA's logbook EXCLUDES
// continuous sensors, so for those entities `ent show` reported a real recent
// change while `ent who` reported nothing — same question, two silently
// different answers, each individually right.
//
// The fixtures here are the divergence made concrete:
//   - a logbook-excluded entity (a continuous sensor: unit_of_measurement +
//     state_class) whose state context names a user — under the old behavior
//     `ent show` said "User Jan" and `ent who` said "no changes", with nothing
//     naming a source or the exclusion;
//   - a logbook-covered entity whose newest logbook entry names an automation
//     while the state context carries only the propagated user id — under the
//     old behavior `ent show` answered from the poorer source.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// excludedSensorFixture serves a continuous sensor (sensor.total_energy, has
// unit_of_measurement and state_class → HA's logbook excludes it), an empty
// logbook — exactly what HA answers for an excluded entity — and a user list
// resolving janUUID.
func excludedSensorFixture(t *testing.T) *cmdTestServer {
	t.Helper()
	body := `{"entity_id":"sensor.total_energy","state":"15.5",` +
		`"attributes":{"friendly_name":"Total Energy","unit_of_measurement":"kWh","state_class":"total_increasing"},` +
		`"last_changed":"2026-05-21T10:00:00+00:00","last_updated":"2026-05-21T10:00:00+00:00",` +
		`"context":{"id":"01HXYZ","parent_id":null,"user_id":"` + janUUID + `"}}`
	return startCmdServer(t, map[string]any{
		"config/entity_registry/list": []any{},
		"config/area_registry/list":   []any{},
		"config/label_registry/list":  []any{},
		"config/floor_registry/list":  []any{},
		"config/auth/list":            []map[string]any{{"id": janUUID, "name": "Jan"}},
	}, map[string]http.HandlerFunc{
		"/api/states/sensor.total_energy": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		},
		"/api/logbook/": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[]`))
		},
	})
}

// coveredEntityFixture serves light.kitchen — a logbook-covered entity — whose
// newest logbook entry attributes the change to an automation, while the state
// context carries only the (propagated) user id. The logbook rows arrive
// newest-first, as one of HA's two orderings, so a resolver that grabs a
// positional row instead of the newest `when` fails here.
func coveredEntityFixture(t *testing.T) *cmdTestServer {
	t.Helper()
	logbook := `[
		{"when":"2026-05-21T10:00:00+00:00","name":"Kitchen Light","state":"on","entity_id":"light.kitchen","domain":"light","context_event_type":"automation_triggered","context_name":"Sunset Lights","context_user_id":"` + janUUID + `"},
		{"when":"2026-05-21T09:00:00+00:00","name":"Kitchen Light","state":"off","entity_id":"light.kitchen","domain":"light","context_user_id":"` + janUUID + `"}
	]`
	return startCmdServer(t, map[string]any{
		"config/entity_registry/list": []any{},
		"config/area_registry/list":   []any{},
		"config/label_registry/list":  []any{},
		"config/floor_registry/list":  []any{},
		"config/auth/list":            []map[string]any{{"id": janUUID, "name": "Jan"}},
	}, map[string]http.HandlerFunc{
		"/api/states/light.kitchen": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(stateJSON(janUUID)))
		},
		"/api/logbook/": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(logbook))
		},
	})
}

// TestActorConverges_LogbookExcludedEntity is the direct D70 regression: for a
// logbook-excluded entity both commands must give the SAME answer from the
// SAME named source, and `ent who` must say explicitly that the logbook
// excludes the entity — not report a bare "no changes" that contradicts
// `ent show`'s changed_by line.
func TestActorConverges_LogbookExcludedEntity(t *testing.T) {
	ts := excludedSensorFixture(t)
	withFlagDir(t, ts.dir)

	var showBuf bytes.Buffer
	if err := runEntShow(context.Background(), &showBuf, "sensor.total_energy"); err != nil {
		t.Fatalf("runEntShow: %v", err)
	}
	show := showBuf.String()

	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()
	var whoBuf bytes.Buffer
	if err := runEntWho(context.Background(), &whoBuf, "sensor.total_energy"); err != nil {
		t.Fatalf("runEntWho: %v", err)
	}
	who := whoBuf.String()

	// One shared answer, one shared source label, on both commands.
	const want = "User Jan (source: state context"
	if !strings.Contains(show, want) {
		t.Errorf("ent show must name the actor AND its source, want %q in:\n%s", want, show)
	}
	if !strings.Contains(who, want) {
		t.Errorf("ent who must fall back to the same shared answer, want %q in:\n%s", want, who)
	}

	// Both must say WHY the logbook has nothing: HA excludes the entity.
	if !strings.Contains(show, "excluded from logbook") {
		t.Errorf("ent show must state the logbook exclusion, got:\n%s", show)
	}
	if !strings.Contains(who, "logbook excludes") {
		t.Errorf("ent who must state explicitly that the logbook excludes this entity, got:\n%s", who)
	}
	// The old contradiction: a bare "no changes" against ent show's real answer.
	if strings.Contains(who, "no changes for") {
		t.Errorf("ent who must not report an excluded entity as merely quiet:\n%s", who)
	}
}

// TestEntShow_ChangedBy_PrefersLogbookAnswer pins the resolution order the
// decision fixes: logbook first. The state context carries only the propagated
// user id; the logbook knows the proximate cause was an automation. One shared
// resolution means ent show now gives the logbook's richer answer — labelled.
func TestEntShow_ChangedBy_PrefersLogbookAnswer(t *testing.T) {
	ts := coveredEntityFixture(t)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Automation: Sunset Lights (source: logbook)") {
		t.Errorf("ent show must answer from the logbook (newest entry) and say so, got:\n%s", out)
	}
	if strings.Contains(out, "User Jan") {
		t.Errorf("ent show must not fall back to the propagated state-context user when the logbook answered:\n%s", out)
	}
}

// TestEntWho_QuietIsNotExcluded is the distinguishability half (H-10): an
// entity the logbook covers but that was quiet in the window is an answered
// zero from the logbook — never spelled like the excluded case.
func TestEntWho_QuietIsNotExcluded(t *testing.T) {
	ts := entWhoFixture(t, []map[string]any{}, `[]`)
	withFlagDir(t, ts.dir)

	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	if err := runEntWho(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntWho: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no changes for light.kitchen in the last 24h (source: logbook)") {
		t.Errorf("a quiet covered entity is the logbook's answered zero, and names its source:\n%s", out)
	}
	if strings.Contains(out, "excludes") {
		t.Errorf("a quiet covered entity must not be reported as logbook-excluded:\n%s", out)
	}
}

// TestEntWho_JSON_ExcludedCarriesSourceAndExclusion: the machine contract.
// `--json` must let a caller distinguish excluded from quiet by FIELDS —
// source + logbook_excluded — and must carry the shared fallback answer.
func TestEntWho_JSON_ExcludedCarriesSourceAndExclusion(t *testing.T) {
	ts := excludedSensorFixture(t)
	withFlagDir(t, ts.dir)

	oldSince, oldJSON := flagSince, flagJSON
	flagSince, flagJSON = "24h", true
	defer func() { flagSince, flagJSON = oldSince, oldJSON }()

	var buf bytes.Buffer
	if err := runEntWho(context.Background(), &buf, "sensor.total_energy"); err != nil {
		t.Fatalf("runEntWho --json: %v", err)
	}
	var got struct {
		Events          []map[string]any `json:"events"`
		Source          string           `json:"source"`
		LogbookExcluded *bool            `json:"logbook_excluded"`
		ChangedBy       string           `json:"changed_by"`
	}
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("ent who --json invalid: %v\n%s", err, buf.String())
	}
	if got.Source != "state context" {
		t.Errorf("source = %q, want 'state context'", got.Source)
	}
	if got.LogbookExcluded == nil || !*got.LogbookExcluded {
		t.Errorf("logbook_excluded must be present and true, got %v\n%s", got.LogbookExcluded, buf.String())
	}
	if got.ChangedBy != "User Jan" {
		t.Errorf("changed_by = %q, want the shared fallback answer 'User Jan'", got.ChangedBy)
	}
	if len(got.Events) != 0 {
		t.Errorf("events must be empty for an excluded entity, got %v", got.Events)
	}
}

// TestEntWho_JSON_QuietCarriesLogbookSource: the other side of the field-level
// distinction — quiet is source "logbook", logbook_excluded false.
func TestEntWho_JSON_QuietCarriesLogbookSource(t *testing.T) {
	ts := entWhoFixture(t, []map[string]any{}, `[]`)
	withFlagDir(t, ts.dir)

	oldSince, oldJSON := flagSince, flagJSON
	flagSince, flagJSON = "24h", true
	defer func() { flagSince, flagJSON = oldSince, oldJSON }()

	var buf bytes.Buffer
	if err := runEntWho(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntWho --json: %v", err)
	}
	var got struct {
		Source          string `json:"source"`
		LogbookExcluded *bool  `json:"logbook_excluded"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("ent who --json invalid: %v\n%s", err, buf.String())
	}
	if got.Source != "logbook" {
		t.Errorf("source = %q, want 'logbook' (the logbook answered: nothing)", got.Source)
	}
	if got.LogbookExcluded == nil || *got.LogbookExcluded {
		t.Errorf("logbook_excluded must be present and false for a quiet covered entity, got %v", got.LogbookExcluded)
	}
}

// TestEntShow_JSON_CarriesSourceFields: ent show's machine contract gains the
// same two fields (changed_by_source, logbook_excluded), for both branches.
func TestEntShow_JSON_CarriesSourceFields(t *testing.T) {
	oldJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = oldJSON }()

	t.Run("logbook answer", func(t *testing.T) {
		ts := coveredEntityFixture(t)
		withFlagDir(t, ts.dir)
		var buf bytes.Buffer
		if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
			t.Fatalf("runEntShow --json: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
		}
		if got["changed_by"] != "Automation: Sunset Lights" {
			t.Errorf("changed_by = %v, want the logbook's answer", got["changed_by"])
		}
		if got["changed_by_source"] != "logbook" {
			t.Errorf("changed_by_source = %v, want 'logbook'", got["changed_by_source"])
		}
		if got["logbook_excluded"] != false {
			t.Errorf("logbook_excluded = %v, want false", got["logbook_excluded"])
		}
	})

	t.Run("excluded fallback", func(t *testing.T) {
		ts := excludedSensorFixture(t)
		withFlagDir(t, ts.dir)
		var buf bytes.Buffer
		if err := runEntShow(context.Background(), &buf, "sensor.total_energy"); err != nil {
			t.Fatalf("runEntShow --json: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
		}
		if got["changed_by"] != "User Jan" {
			t.Errorf("changed_by = %v, want the state-context fallback 'User Jan'", got["changed_by"])
		}
		if got["changed_by_source"] != "state context" {
			t.Errorf("changed_by_source = %v, want 'state context'", got["changed_by_source"])
		}
		if got["logbook_excluded"] != true {
			t.Errorf("logbook_excluded = %v, want true", got["logbook_excluded"])
		}
	})
}
