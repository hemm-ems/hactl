package cmd

// Blind spec tests for the pre-merge fixes that hold with or without a policy
// (PLAN-policy-prelaunch-fixes §3.2), written from the contract only —
// docs/decisions.md D-115 (`auto ls --json` config_id, B-9c), D-122 (`svc call
// --data` objects only, B-17), D-123 (`helper delete` always names its domain,
// B-18) and the manual's Automations, Services and Helpers prose — without
// reading auto.go, svc.go or helper.go's write paths.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// --- B-9c: auto ls --json carries config_id (D-115) ------------------------------

// TestAutoLsJSONCarriesTheConfigID — the manual (Automations): "`auto ls
// --json` carries `config_id` on every row — `""` for an automation without
// one, a restored ghost included — while the text table keeps its columns".
func TestAutoLsJSONCarriesTheConfigID(t *testing.T) {
	states := `[
	 {"entity_id":"automation.pg_v9_verify_auto","state":"on","attributes":{"id":"pg_v9_auto_1","friendly_name":"PG v9 verify auto","last_triggered":null},"last_changed":"2026-11-01T08:00:00+00:00","last_updated":"2026-11-01T08:00:00+00:00"},
	 {"entity_id":"automation.yaml_without_id","state":"on","attributes":{"friendly_name":"YAML without id","last_triggered":null},"last_changed":"2026-11-01T08:00:00+00:00","last_updated":"2026-11-01T08:00:00+00:00"},
	 {"entity_id":"automation.ghost","state":"unavailable","attributes":{"restored":true,"friendly_name":"Ghost"},"last_changed":"2026-11-01T08:00:00+00:00","last_updated":"2026-11-01T08:00:00+00:00"}
	]`
	ts := startCmdServer(t, map[string]any{"trace/list": map[string]any{}}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, states)
		},
	})
	listing := func(args ...string) []map[string]any {
		t.Helper()
		out, err := runPolicyCLI(t, ts.dir, append([]string{"auto", "ls", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("auto ls --json %v: %v\n%s", args, err, out)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("auto ls --json is not a JSON array: %v\n%s", err, out)
		}
		return rows
	}
	check := func(rows []map[string]any, want map[string]string, mustList bool) {
		t.Helper()
		for _, r := range rows {
			id, _ := r["id"].(string)
			w, ok := want[id]
			if !ok {
				continue
			}
			delete(want, id)
			got, has := r["config_id"]
			if !has {
				t.Errorf("row %s has no config_id key: %v", id, r)
				continue
			}
			if got != w {
				t.Errorf("row %s: config_id %v, want %q", id, got, w)
			}
		}
		for id := range want {
			if mustList {
				t.Errorf("auto ls --json lists no row %s: %v", id, rows)
			}
		}
	}
	check(listing(), map[string]string{"pg_v9_verify_auto": "pg_v9_auto_1", "yaml_without_id": ""}, true)
	// The ghost, where the restored listing shows it.
	check(listing("--restored"), map[string]string{"ghost": ""}, false)

	text, err := runPolicyCLI(t, ts.dir, "auto", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if header := strings.SplitN(text, "\n", 2)[0]; strings.Contains(header, "config_id") {
		t.Errorf("the text table gained a config_id column (a JSON key only): %q", header)
	}
}

// --- B-17: svc call --data must be a JSON object (D-122) -------------------------

// TestSvcCallRefusesDataThatIsNotAnObject — D-122, without a policy: any JSON
// value but an object is refused in the dry run already — exit 1, nothing on
// stdout, nothing sent; `{}` and an absent --data still preview.
func TestSvcCallRefusesDataThatIsNotAnObject(t *testing.T) {
	for _, tc := range []struct{ data, got string }{
		{"null", "null"},
		{`["light.a"]`, "a list"},
		{`"light.a"`, "a string"},
		{"1", "a number"},
		{"true", "a boolean"},
	} {
		t.Run(tc.data, func(t *testing.T) {
			rig := startSvcRig(t, reachOf("light.a"))
			for _, extra := range [][]string{nil, {"--confirm"}} {
				args := append([]string{"svc", "call", "light.turn_on", "--data", tc.data}, extra...)
				out, err := runPolicyCLI(t, rig.dir, args...)
				if cliExitCode(err) != 1 || out != "" {
					t.Errorf("%v: exit %d, stdout %q; want 1 and nothing", args, cliExitCode(err), out)
					continue
				}
				if want := "invalid --data: want a JSON object ({…}), got " + tc.got; !strings.Contains(err.Error(), want) {
					t.Errorf("%v: error %q lacks %q", args, err, want)
				}
			}
			if n := rig.posted("light/turn_on"); n != 0 {
				t.Errorf("a refused --data reached Home Assistant %d times", n)
			}
		})
	}
	rig := startSvcRig(t, reachOf())
	for _, args := range [][]string{
		{"svc", "call", "light.turn_on", "--data", "{}"},
		{"svc", "call", "light.turn_on"},
	} {
		if out, err := runPolicyCLI(t, rig.dir, args...); err != nil {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
	}
	out, err := runPolicyCLI(t, rig.dir, "svc", "call", "light.turn_on", "--data", "{nope")
	if err == nil || !strings.Contains(err.Error(), "invalid --data JSON: ") {
		t.Errorf("malformed --data: %v\n%s", err, out)
	}
}

// --- B-18: helper delete always names its domain (D-123) -------------------------

// TestHelperDeleteNamesItsDomainWithoutAPolicy — D-123: the confirmed delete
// sends `&domain=<the domain the companion's GET resolved>` with or without a
// policy, so it deletes exactly the helper its preview named.
func TestHelperDeleteNamesItsDomainWithoutAPolicy(t *testing.T) {
	for name, body := range map[string]string{"no file": "", "implicit allow-all": policyImplicitAllow} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HACTL_MANUAL_MODE", "off")
			r := newTypedRig(t)
			if body != "" {
				writePolicyFile(t, r.ts.dir, body)
			}
			if out, err := runPolicyCLI(t, r.ts.dir, "helper", "delete", "guest_mode", "--confirm"); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			var deletes []string
			for _, l := range r.requests() {
				if strings.HasPrefix(l, "DELETE /v1/config/helper") {
					deletes = append(deletes, l)
				}
			}
			if !slices.Equal(deletes, []string{"DELETE /v1/config/helper?domain=input_boolean&id=guest_mode"}) {
				t.Errorf("helper deletes sent %q, want exactly one naming domain=input_boolean", deletes)
			}
		})
	}
}
