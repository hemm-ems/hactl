package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// The companion side of the H-10 read sweep.
//
// Eight commands need a companion to run at all (helper ls/show and config
// files call connectCompanion; ref scan/validate call connectRefSources;
// `companion status`/`logs`/`wireguard status` are companion commands by
// definition). They used to be listed in a `companionRequired` map that
// TestJSONContract printed and never asserted on — which is where `helper
// show`'s missing --json branch hid for a release, and which the invariant
// manifest recorded as H-10's remaining read-half debt.
//
// A companion test double is not out of proportion: this package already
// stands one up in cat_test.go, helper_test.go, ref_test.go, script_test.go,
// auto_create_validation_test.go and half a dozen more. This file is the same
// pattern, sized to the whole set of routes those eight commands call, so the
// eight can be asserted like every other read command instead of skipped.
//
// The payloads are deliberately realistic, not minimal: internal/degeneracy
// poisons a record that decoded without its identity, so a stub answering `{}`
// makes the command fail for a reason that has nothing to do with --json —
// and a stub answering a single row makes H-10's --top clause vacuous, because
// a truncation to one row is invisible when there was only one row. Every list
// route below therefore answers with at least two fully-populated records.
// ---------------------------------------------------------------------------

// contractCompanion is the companion-shaped stub buildContractFixture wires
// into the fixture's .env via COMPANION_URL. It records every path it was
// asked for that it does not implement, so an unstubbed route becomes a test
// failure (assertRoutesComplete) rather than a command that quietly degrades.
type contractCompanion struct {
	srv *httptest.Server

	mu         sync.Mutex
	unexpected map[string]int
}

// contractHelpers are the YAML helpers the stub's companion "manages" — two of
// them, in two domains, so `helper ls --json --top 1` has something to truncate
// and `helper show` has a real definition to return.
var contractHelpers = map[string]struct {
	Name    string
	Domain  string
	Icon    string
	Content string
}{
	"guest_mode": {
		Name: "Guest Mode", Domain: "input_boolean", Icon: "mdi:account-multiple",
		Content: "guest_mode:\n  name: Guest Mode\n  icon: mdi:account-multiple\n",
	},
	"laundry_timer": {
		Name: "Laundry Timer", Domain: "timer", Icon: "mdi:washing-machine",
		Content: "laundry_timer:\n  name: Laundry Timer\n  duration: \"01:30:00\"\n",
	},
}

// startContractCompanion starts the stub and returns it. Every route below is
// one an H-10-swept read command actually calls, found by reading the
// implementations: /v1/health + /v1/status (companion status, health),
// /v1/config/helpers + /v1/config/helper (helper ls, helper show),
// /v1/config/files (config files), /v1/ref/scan (ref scan), /v1/ref/entities
// (ref validate), /v1/logs (companion logs), /v1/wireguard/status (companion
// wireguard status), /v1/related/entity (ent related — already enforced, and
// no longer forced down its companion-less fallback now that a companion
// answers).
// filterContractLogs keeps the records whose `field` contains needle, folding
// case. It stands in for the add-on's own filtering, which is all this stub
// owes: the point is that a narrowed request can come back empty.
func filterContractLogs(entries []map[string]any, field, needle string) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if v, ok := e[field].(string); ok && strings.Contains(strings.ToLower(v), strings.ToLower(needle)) {
			out = append(out, e)
		}
	}
	return out
}

func startContractCompanion(t *testing.T) *contractCompanion {
	t.Helper()

	cs := &contractCompanion{unexpected: map[string]int{}}
	mux := http.NewServeMux()

	// GET /v1/health — companion status, health.
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		contractJSON(w, map[string]any{"status": "ok", "version": "2026.7.8"})
	})

	// GET /v1/status — companion status (the capability block under health).
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		contractJSON(w, map[string]any{
			"version":              "2026.7.8",
			"supervisor_reachable": true,
			"has_ha_cli":           true,
			"config_writable":      true,
			"ingress_active":       false,
			"auth_mode":            "bearer",
		})
	})

	// GET /v1/config/files — config files.
	mux.HandleFunc("/v1/config/files", func(w http.ResponseWriter, _ *http.Request) {
		contractJSON(w, map[string]any{
			"files": []string{"configuration.yaml", "automations.yaml", "scripts.yaml", "template.yaml"},
		})
	})

	// GET /v1/config/helpers[?domain=] — helper ls.
	mux.HandleFunc("/v1/config/helpers", func(w http.ResponseWriter, r *http.Request) {
		domain := r.URL.Query().Get("domain")
		helpers := make([]map[string]any, 0, len(contractHelpers))
		for _, id := range sortedHelperIDs() {
			h := contractHelpers[id]
			if domain != "" && h.Domain != domain {
				continue
			}
			helpers = append(helpers, map[string]any{
				"id": id, "name": h.Name, "domain": h.Domain, "icon": h.Icon,
			})
		}
		contractJSON(w, map[string]any{"helpers": helpers})
	})

	// GET /v1/config/helper?id= — helper show. Unknown ids 404 exactly as the
	// real companion does, so the fixture cannot certify a lookup that would
	// fail against a real instance.
	mux.HandleFunc("/v1/config/helper", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		h, ok := contractHelpers[id]
		if !ok {
			http.Error(w, "helper not found: "+id, http.StatusNotFound)
			return
		}
		contractJSON(w, map[string]any{"id": id, "domain": h.Domain, "content": h.Content})
	})

	// GET /v1/ref/scan?target= — ref scan. Two hits in two files, so the
	// merged table has rows to lose if --top ever reaches JSON output.
	mux.HandleFunc("/v1/ref/scan", func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("target")
		contractJSON(w, map[string]any{
			"target": target,
			"hits": []map[string]any{
				{"location": "automations.yaml", "path": "[0].trigger[0].entity_id", "matched_value": target},
				{"location": "scripts.yaml", "path": "wakeup.sequence[0].target.entity_id", "matched_value": target},
			},
		})
	})

	// GET /v1/ref/entities — ref validate. Two entity_id-keyed values that are
	// absent from the fixture's live set (so two dangling rows are reported),
	// one that is live (so the live-set union is exercised), and one under a
	// `service:` key (so the key filter is exercised).
	mux.HandleFunc("/v1/ref/entities", func(w http.ResponseWriter, _ *http.Request) {
		contractJSON(w, map[string]any{
			"entities": []map[string]any{
				{"location": "automations.yaml", "path": "[0].trigger[0].entity_id", "key": "entity_id", "matched_value": "sensor.gone"},
				{"location": "scripts.yaml", "path": "wakeup.sequence[0].target.entity_id", "key": "entity_id", "matched_value": "light.removed"},
				{"location": "automations.yaml", "path": "[0].action[0].target.entity_id", "key": "entity_id", "matched_value": "light.kitchen"},
				{"location": "automations.yaml", "path": "[0].action[0].service", "key": "service", "matched_value": "light.turn_on"},
			},
		})
	})

	// GET /v1/logs — companion logs. The real route honours ?limit=, which
	// `hactl companion logs` fills from --top, so the stub honours it too: a
	// stub that ignored it would hide that this command's --top reaches its
	// source rather than only its text table.
	//
	// ?component= and ?level= are honoured for the same reason one step on.
	// This command's narrowing happens server side, so a stub that answered the
	// full buffer whatever it was asked made an empty answer unreachable — and
	// the empty answer is where `companion logs` used to print "(no log
	// entries)" for a component nothing logs under (live-fire #28's class).
	mux.HandleFunc("/v1/logs", func(w http.ResponseWriter, r *http.Request) {
		entries := []map[string]any{
			{"ts": 1767256200.25, "level": "INFO", "name": "companion.wireguard", "message": "tunnel wg0 up, 1 peer"},
			{"ts": 1767256260.5, "level": "WARN", "name": "companion.config", "message": "automations.yaml reloaded with 2 warnings"},
			{"ts": 1767256320.75, "level": "ERROR", "name": "companion.api", "message": "GET /v1/config/helper?id=ghost -> 404"},
		}
		if component := r.URL.Query().Get("component"); component != "" {
			entries = filterContractLogs(entries, "name", component)
		}
		if level := r.URL.Query().Get("level"); level != "" {
			entries = filterContractLogs(entries, "level", level)
		}
		if limit, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && limit > 0 && limit < len(entries) {
			entries = entries[:limit]
		}
		contractJSON(w, map[string]any{"entries": entries})
	})

	// GET /v1/wireguard/status?tunnel= — companion wireguard status. Answered
	// as an ACTIVE tunnel with an interface, a peer and a running dyndns
	// monitor: an inactive tunnel takes an early return in
	// writeWireguardStatus and would leave most of the payload unrendered.
	mux.HandleFunc("/v1/wireguard/status", func(w http.ResponseWriter, r *http.Request) {
		tunnel := r.URL.Query().Get("tunnel")
		if tunnel == "" {
			tunnel = "wg0"
		}
		hs := 42
		lastCheck := 17
		lastReresolve := 903
		contractJSON(w, map[string]any{
			"tunnel": tunnel,
			"state":  "active",
			"interface": map[string]any{
				"public_key": "kQ1nQ9m8vT3wYcJ2pR7sL0aZbXeF4gH6iJ8kL0mN2oQ=", "listening_port": 51820,
			},
			"peers": []map[string]any{{
				"public_key":            "aB3cD4eF5gH6iJ7kL8mN9oP0qR1sT2uV3wX4yZ5aB6c=",
				"endpoint":              "vpn.example.net:51820",
				"allowed_ips":           "10.6.0.0/24",
				"latest_handshake":      "42s ago",
				"latest_handshake_secs": &hs,
				"transfer_rx":           "12.4 MiB",
				"transfer_tx":           "3.1 MiB",
				"transfer_rx_bytes":     13002342,
				"transfer_tx_bytes":     3250688,
			}},
			"monitor": map[string]any{
				"running":                 true,
				"healthy":                 true,
				"hostnames":               []string{"vpn.example.net"},
				"resolved":                map[string]string{"vpn.example.net": "203.0.113.17"},
				"last_check_secs_ago":     &lastCheck,
				"last_reresolve_secs_ago": &lastReresolve,
				"attempt":                 1,
			},
		})
	})

	// GET /v1/related/entity?entity_id= — `ent related`. Already enforced, but
	// until now it ran with no companion at all and logged a WARN; with one
	// present the config-file half of its answer is exercised too.
	mux.HandleFunc("/v1/related/entity", func(w http.ResponseWriter, r *http.Request) {
		entityID := r.URL.Query().Get("entity_id")
		contractJSON(w, map[string]any{
			"entity_id": entityID,
			"stale":     false,
			"related": []map[string]any{
				{"entity_id": "automation.morning", "relationship": "config", "detail": "automations.yaml [0].action[0]"},
				{"entity_id": "script.wakeup", "relationship": "config", "detail": "scripts.yaml wakeup.sequence[0]"},
			},
			"stale_refs": []map[string]any{},
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cs.mu.Lock()
		cs.unexpected[r.Method+" "+r.URL.Path]++
		cs.mu.Unlock()
		http.Error(w, "unstubbed companion route: "+r.URL.Path, http.StatusNotFound)
	})

	cs.srv = httptest.NewServer(mux)
	t.Cleanup(cs.srv.Close)
	return cs
}

func sortedHelperIDs() []string {
	ids := make([]string, 0, len(contractHelpers))
	for id := range contractHelpers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// assertRoutesComplete fails when a swept command asked the companion for a
// route the stub does not implement. Without it, adding a companion call to a
// read command would silently put that command back on the degraded path the
// old skip list institutionalised — the failure mode this whole file exists to
// remove.
func (cs *contractCompanion) assertRoutesComplete(t *testing.T) {
	t.Helper()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.unexpected) == 0 {
		return
	}
	missing := make([]string, 0, len(cs.unexpected))
	for route, n := range cs.unexpected {
		missing = append(missing, route+" ("+strconv.Itoa(n)+"x)")
	}
	sort.Strings(missing)
	t.Errorf("H-10 sweep: a swept command called %d companion route(s) the contract stub does not answer, "+
		"so it ran on a degraded path instead of against a companion: %s — add them to "+
		"startContractCompanion", len(missing), strings.Join(missing, ", "))
}

func contractJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
