package cmd

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/manual"
)

// What a write command prints from Home Assistant's answer to the write
// itself — a service call's changed states and its response — follows the
// read rules like a read command's output (D-182); the write is unchanged.

// shownAnswerRig is the service-call rig whose service answer reports a
// bystander lock changing beside the light the call targets.
func shownAnswerRig(t *testing.T) *svcRig {
	t.Helper()
	rig := &svcRig{posts: map[string]int{}, puts: map[string]int{}}
	ws, handlers := rig.fixtures(reachOf("light.a"))
	handlers["/api/services/"] = func(w http.ResponseWriter, r *http.Request) {
		rig.mu.Lock()
		rig.posts[strings.TrimPrefix(r.URL.Path, "/api/services/")]++
		rig.mu.Unlock()
		changed := `[{"entity_id":"light.a","state":"on","attributes":{"brightness":9}},{"entity_id":"lock.door","state":"unlocked","attributes":{"code":"1234"}}]`
		if r.URL.Query().Get("return_response") != "" {
			_, _ = io.WriteString(w, `{"changed_states":`+changed+`,"service_response":{"lock.door":{"code":"1234"},"light.a":{"ok":true}}}`)
			return
		}
		_, _ = io.WriteString(w, changed)
	}
	rig.cmdTestServer = startCmdServer(t, ws, handlers)
	pointAtCompanion(t, rig.cmdTestServer)
	return rig
}

const shownAnswerPolicy = "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entity:lock.*\": [read]\n"

func TestSvcCallShowsOnlyWhatItMayRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
	}{
		{"prose", nil},
		{"json", []string{"--json"}},
		{"return", []string{"--return"}},
		{"return json", []string{"--return", "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := shownAnswerRig(t)
			writePolicyFile(t, rig.dir, shownAnswerPolicy)
			args := append([]string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"light.a"}`}, tc.extra...)
			if out, err := runPolicyCLI(t, rig.dir, args...); err != nil {
				t.Fatalf("preview: %v\n%s", err, out)
			}
			recordWitness(stateCacheDir(rig.dir), manual.SessionKey(), "hactl svc call", []string{"light.turn_on"}, time.Now())
			out, err := runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
			if err != nil {
				t.Fatalf("--confirm: %v\n%s", err, out)
			}
			if rig.posted("light/turn_on") != 1 {
				t.Errorf("the service was called %d times, want 1", rig.posted("light/turn_on"))
			}
			if strings.Contains(out, "lock.door") || strings.Contains(out, "1234") {
				t.Errorf("a read-denied entity shown in the write's output:\n%s", out)
			}
			if !strings.Contains(out, "light.a") {
				t.Errorf("the readable target is missing:\n%s", out)
			}
			// Under --json the trailer goes to stderr (D-139), which the
			// harness does not capture.
			if !strings.Contains(tc.name, "json") && !strings.Contains(out, "hidden by policy: 1") {
				t.Errorf("no trailer for what was hidden:\n%s", out)
			}
		})
	}
}

// TestSvcCallAnswerWithoutAProfileIsUnchanged — D-58: without a profile the
// bystander is shown as Home Assistant reported it.
func TestSvcCallAnswerWithoutAProfileIsUnchanged(t *testing.T) {
	rig := shownAnswerRig(t)
	args := []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"light.a"}`}
	if out, err := runPolicyCLI(t, rig.dir, args...); err != nil {
		t.Fatalf("preview: %v\n%s", err, out)
	}
	recordWitness(stateCacheDir(rig.dir), manual.SessionKey(), "hactl svc call", []string{"light.turn_on"}, time.Now())
	out, err := runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
	if err != nil {
		t.Fatalf("--confirm: %v\n%s", err, out)
	}
	if !strings.Contains(out, "lock.door") || strings.Contains(out, "hidden by policy") {
		t.Errorf("output without a profile changed:\n%s", out)
	}
}

// TestLogShowFromTheIDCacheFollowsTheReadRules — an entry `log show` prints
// from cache/ids.json reaches no request, so it is decided as `read
// system:logs` on its own (D-182).
func TestLogShowFromTheIDCacheFollowsTheReadRules(t *testing.T) {
	dir := t.TempDir()
	writeRefEnv(t, dir, "http://127.0.0.1:1", "http://127.0.0.1:1")
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o750); err != nil {
		t.Fatal(err)
	}
	idsJSON := `{"2026-11-01 09:00:00|homeassistant.core|the secret message":"log:abc123"}`
	if err := os.WriteFile(filepath.Join(dir, "cache", "ids.json"), []byte(idsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPolicyCLI(t, dir, "log", "show", "log:abc123")
	if err != nil || !strings.Contains(out, "the secret message") {
		t.Fatalf("without a profile: %v\n%s", err, out)
	}
	writePolicyFile(t, dir, "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"system:logs\": [read]\n")
	out, err = runPolicyCLI(t, dir, "log", "show", "log:abc123")
	if err == nil || strings.Contains(out, "the secret message") || !strings.Contains(out, "system:logs") {
		t.Errorf("under a profile denying the log: %v\n%s", err, out)
	}
}
