package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// ============================================================================
// H-21 — a listing decodes only the entities it lists.
//
// `auto ls` and `script ls` used to decode ALL of /api/states into their own
// domain-typed struct and filter to `automation.`/`script.` afterwards, so an
// entity they discard could fail them. A live instance (HA 2026.7.4) reported
// exactly that:
//
//	parsing states: json: cannot unmarshal number -1.7525 into Go struct field
//	automationAttributes.attributes.current of type int
//
// That instance is a third party's and is NOT reachable from this project. The
// entity that emitted -1.7525 there is therefore UNKNOWN, and nothing in this
// file claims to know it. `sensor.hactl_synthetic_collider` is named so it
// cannot be mistaken for an observation: it is built by reflection over the
// command's OWN attribute struct, so it carries every key that struct decodes
// at a type the corresponding Go field cannot hold. That is a deliberate
// SUPERSET of the real culprit, and a stronger case than the real answer would
// have been — whatever single key that instance's entity carries, this one
// carries it too, along with every other key the struct can be broken by.
//
// The collision is not exotic either. `TestOracleStatesCarriesOneKeyAtTwoJSONTypes`
// (internal/integration/domaindecode_oracle_test.go) establishes that stock HA
// already emits `max` as an integer on `automation.*`/`script.*` and as a
// fraction on `number.*` in one response — `automationAttributes` is one
// `Max int` field away from breaking on an out-of-the-box instance.
// ============================================================================

// colliderEntityID is the synthetic entity every test here discards. Synthetic
// and named as such: see the file header.
const colliderEntityID = "sensor.hactl_synthetic_collider"

// colliderAttributes builds an attribute map carrying EVERY key `attrs`
// decodes, each at a JSON type the corresponding Go field cannot hold.
//
// The key set is DERIVED from the struct by reflection rather than written out,
// which is the point: `automationAttributes` and `scriptAttributes` are
// different key sets (six vs four — no `id`, no `restored` on scripts), so one
// hand-copied list used twice would assert about fields that do not exist on
// one of them. Derivation also makes the case close over the future: a key
// added to either struct tomorrow is covered by this fixture the same day,
// with no test edit.
func colliderAttributes(t *testing.T, attrs any) map[string]any {
	t.Helper()
	rt := reflect.TypeOf(attrs)
	if rt.Kind() != reflect.Struct {
		t.Fatalf("colliderAttributes wants a struct, got %s", rt.Kind())
	}
	out := map[string]any{}
	for f := range rt.Fields() {
		key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		switch f.Type.Kind() {
		case reflect.String:
			// A number where a string is expected. -1.7525 is the value the
			// live report carried; reused here so the reproduction and the
			// report speak about the same wire value.
			out[key] = -1.7525
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			out[key] = -1.7525
		case reflect.Bool:
			out[key] = "not-a-bool"
		default:
			t.Fatalf("%s.%s is a %s — colliderAttributes has no colliding value for that kind, so "+
				"this key would be silently uncovered", rt.Name(), f.Name, f.Type.Kind())
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s yielded no json-tagged keys — the collider would assert nothing", rt.Name())
	}
	return out
}

// colliderKeys is colliderAttributes' key set, sorted, for diagnostics.
func colliderKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// statesWithCollider renders a /api/states payload holding one real entity of
// the listed domain plus the synthetic collider the listing must discard.
func statesWithCollider(t *testing.T, listed map[string]any, attrs any) []byte {
	t.Helper()
	payload := []map[string]any{
		listed,
		{
			"entity_id":  colliderEntityID,
			"state":      "ok",
			"attributes": colliderAttributes(t, attrs),
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling states fixture: %v", err)
	}
	return data
}

// statesServer answers /api/states with body and the logbook with an empty list.
func statesServer(t *testing.T, body []byte) *cmdTestServer {
	t.Helper()
	return startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		},
		"/api/logbook/": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
}

// TestAutoLsIgnoresAttributesOfEntitiesItDiscards is the H-21 reproduction for
// `auto ls`: one `sensor.*` the command never renders, carrying every key
// `automationAttributes` decodes at a colliding type, must not stop the
// automations from being listed.
func TestAutoLsIgnoresAttributesOfEntitiesItDiscards(t *testing.T) {
	withAutoLsFlags(t, "")
	attrs := colliderAttributes(t, automationAttributes{})
	t.Logf("collider covers %d automationAttributes keys: %v", len(attrs), colliderKeys(attrs))

	body := statesWithCollider(t, map[string]any{
		"entity_id": "automation.climate_schedule",
		"state":     "on",
		"attributes": map[string]any{
			"id": "1700000000002", "friendly_name": "Climate schedule",
			"mode": "single", "current": 0, "last_triggered": "2026-07-28T09:00:00Z",
		},
	}, automationAttributes{})

	ts := statesServer(t, body)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runAutoLs(listingCmd(context.Background(), "auto", "ls"), &buf); err != nil {
		t.Fatalf("auto ls failed because of an entity it does not list: %v\n"+
			"H-21: the set whose attributes a listing decodes into a domain-specific schema must be "+
			"a subset of the set it renders. %s is a sensor; `auto ls` discards it and must never "+
			"have decoded it.", err, colliderEntityID)
	}
	if !strings.Contains(buf.String(), "climate_schedule") {
		t.Errorf("auto ls did not list the automation:\n%s", buf.String())
	}
}

// TestScriptLsIgnoresAttributesOfEntitiesItDiscards is the same reproduction for
// `script ls`, against `scriptAttributes`' own (smaller) key set.
func TestScriptLsIgnoresAttributesOfEntitiesItDiscards(t *testing.T) {
	withScriptLsFlags(t)
	attrs := colliderAttributes(t, scriptAttributes{})
	t.Logf("collider covers %d scriptAttributes keys: %v", len(attrs), colliderKeys(attrs))

	body := statesWithCollider(t, map[string]any{
		"entity_id": "script.morning_routine",
		"state":     "off",
		"attributes": map[string]any{
			"friendly_name": "Morning routine", "mode": "single",
			"current": 0, "last_triggered": "2026-07-28T06:00:00Z",
		},
	}, scriptAttributes{})

	ts := statesServer(t, body)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runScriptLs(listingCmd(context.Background(), "script", "ls"), &buf); err != nil {
		t.Fatalf("script ls failed because of an entity it does not list: %v\n"+
			"H-21: %s is a sensor; `script ls` discards it and must never have decoded it.",
			err, colliderEntityID)
	}
	if !strings.Contains(buf.String(), "morning_routine") {
		t.Errorf("script ls did not list the script:\n%s", buf.String())
	}
}

// TestColliderCoversEachStructsOwnKeys pins the derivation itself. The two
// attribute structs are NOT the same key set, and a fixture that used one list
// for both would assert about `id`/`restored` on scripts, which have no such
// fields — the "test inherits the fix's blind spot" pattern from issue #94,
// pointed the other way.
func TestColliderCoversEachStructsOwnKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		attrs any
	}{
		{"automationAttributes", automationAttributes{}},
		{"scriptAttributes", scriptAttributes{}},
	} {
		got := colliderKeys(colliderAttributes(t, tc.attrs))
		want := jsonKeysOf(reflect.TypeOf(tc.attrs))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: collider covers %v, struct declares %v — a key the struct decodes but the "+
				"fixture omits is a key H-21 is not tested on", tc.name, got, want)
		}
		t.Logf("%s declares %d keys: %v", tc.name, len(want), want)
	}
}

// jsonKeysOf reads a struct's json key set straight off its tags — the
// independent second derivation TestColliderCoversEachStructsOwnKeys compares
// against.
func jsonKeysOf(rt reflect.Type) []string {
	var keys []string
	for f := range rt.Fields() {
		if key, _, _ := strings.Cut(f.Tag.Get("json"), ","); key != "" && key != "-" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// TestStatesDecodeErrorNamesTheEntityAndKey is acceptance criterion 8 (spec §9,
// layer 0). The entity that breaks the decode is one the listing DOES render —
// so failing is correct — but the failure must name the record and the key.
//
// `encoding/json` cannot name a slice element: decoding the whole payload at
// once produced "cannot unmarshal number -1.7525 into Go struct field
// automationAttributes.attributes.current of type int", which names the Go type
// and not the entity, and cost a source-reading session against an instance
// nobody here can reach. Decoding per entity means the loop knows the id.
func TestStatesDecodeErrorNamesTheEntityAndKey(t *testing.T) {
	body, err := json.Marshal([]map[string]any{{
		"entity_id": "automation.climate_schedule",
		"state":     "on",
		"attributes": map[string]any{
			"id": "1700000000002", "friendly_name": "Climate schedule",
			"mode": "single", "current": -1.7525,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	srv := statesOnlyServer(t, body)

	_, fetchErr := fetchAutomations(context.Background(), haapi.New(srv, "tok"))
	if fetchErr == nil {
		t.Fatal("an automation whose own `current` is fractional decoded without error — the listing " +
			"renders this entity, so the decode failure is correct and must not be swallowed")
	}
	for _, want := range []string{
		"parsing states:",
		"entity automation.climate_schedule:",
		"attributes.current:",
		"cannot unmarshal number -1.7525 into Go value of type int",
	} {
		if !strings.Contains(fetchErr.Error(), want) {
			t.Errorf("decode error does not contain %q:\n  %v\n"+
				"A wire-decode failure names the record that caused it; without the entity id a "+
				"report from an unreachable instance cannot be diagnosed from its own output.",
				want, fetchErr)
		}
	}
}

// TestStatesWithoutEntityIDStillPoisonsTheListing is acceptance criterion 3.
// H-14 quantifies over the WHOLE payload, and filtering earlier must not narrow
// a payload that lost `entity_id` into a quiet "no automations found": with no
// entity_id, no record matches the `automation.` prefix, so the naive
// filter-first shape answers "nothing to list" at exit 0 — the exact silent
// failure H-14 exists to stop.
func TestStatesWithoutEntityIDStillPoisonsTheListing(t *testing.T) {
	body, err := json.Marshal([]map[string]any{
		{"state": "on", "attributes": map[string]any{"id": "1700000000002"}},
		{"state": "off", "attributes": map[string]any{"id": "1700000000003"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := statesOnlyServer(t, body)

	for name, fetch := range map[string]func(context.Context, *haapi.Client) error{
		"auto ls": func(ctx context.Context, c *haapi.Client) error {
			_, e := fetchAutomations(ctx, c)
			return e
		},
		"script ls": func(ctx context.Context, c *haapi.Client) error {
			_, e := fetchScripts(ctx, c)
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			gotErr := fetch(context.Background(), haapi.New(srv, "tok"))
			if gotErr == nil {
				t.Fatal("a /api/states payload in which every record lost `entity_id` was accepted — " +
					"H-14 must poison it, not let the domain filter turn it into an empty listing")
			}
			if !errors.Is(gotErr, degeneracy.ErrDegenerate) {
				t.Errorf("error is not degeneracy.ErrDegenerate: %v", gotErr)
			}
			if !strings.Contains(gotErr.Error(), degeneracy.Marker) {
				t.Errorf("error does not carry %q, so the integration harness scan cannot see it: %v",
					degeneracy.Marker, gotErr)
			}
		})
	}
}

// statesOnlyServer serves one /api/states body and nothing else, and returns
// its URL. Used by the tests that call the fetchers directly.
func statesOnlyServer(t *testing.T, body []byte) string {
	t.Helper()
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		},
	})
	return ts.srv.URL
}

// withScriptLsFlags pins the `script ls` filter flags for one test.
func withScriptLsFlags(t *testing.T) {
	t.Helper()
	oldPattern, oldLabel, oldFailing, oldSince := flagScriptPattern, flagScriptLabel, flagScriptFailing, flagSince
	flagScriptPattern, flagScriptLabel, flagScriptFailing, flagSince = "", "", false, "24h"
	t.Cleanup(func() {
		flagScriptPattern, flagScriptLabel, flagScriptFailing, flagSince = oldPattern, oldLabel, oldFailing, oldSince
	})
}
