package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

// pipedFile is a path that reads like `-f /dev/stdin` with content piped in: a
// pipe whose writer is closed, so the first read drains it and every later
// read of the path is empty.
func pipedFile(t *testing.T, content string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return fmt.Sprintf("/dev/fd/%d", r.Fd())
}

// TestAutoApplyReadsItsCandidateOnce: `auto apply -f /dev/stdin` diffs, checks
// and writes the bytes piped in — one read. Before, the diff drained the pipe,
// so under any profile the plan read an empty file and refused it as an
// automation without a config id, and with no profile the confirmed write sent
// the companion an empty body while the preview had shown the right diff.
func TestAutoApplyReadsItsCandidateOnce(t *testing.T) {
	const candidate = "id: heating\nalias: Heating (eco)\ntriggers: []\nactions: []\n"
	var mu sync.Mutex
	var bodies []string
	ts := startCmdServer(t, map[string]any{
		"validate_config": map[string]any{
			"triggers": map[string]any{"valid": true, "error": nil}, "conditions": map[string]any{"valid": true, "error": nil},
			"actions": map[string]any{"valid": true, "error": nil},
		},
		"config/entity_registry/list": []any{},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			typedJSON(w, http.StatusOK, []map[string]any{
				{"entity_id": "automation.heating", "state": "on", "attributes": map[string]any{"id": "heating"}},
			})
		},
		"/v1/config/automation": func(w http.ResponseWriter, req *http.Request) {
			switch req.Method {
			case http.MethodGet:
				typedJSON(w, http.StatusOK, map[string]string{"id": "heating", "content": typedHeatingEntry})
			case http.MethodPut:
				raw, _ := io.ReadAll(req.Body)
				mu.Lock()
				bodies = append(bodies, req.URL.Query().Get("dry_run")+":"+string(raw))
				mu.Unlock()
				typedDryRunOr(w, req)
			}
		},
	})
	pointAtCompanion(t, ts)

	for name, policyBody := range map[string]string{"no policy": "", "allow-all profile": policyAllowEverything} {
		t.Run(name, func(t *testing.T) {
			if policyBody != "" {
				writePolicyFile(t, ts.dir, policyBody)
				defer func() { _ = os.Remove(ts.dir + "/policy.yaml") }()
			}
			mu.Lock()
			bodies = nil
			mu.Unlock()
			out, err := runPolicyCLI(t, ts.dir, "auto", "apply", "heating", "-f", pipedFile(t, candidate))
			if err != nil {
				t.Fatalf("preview of a piped candidate: %v\n%s", err, out)
			}
			if !strings.Contains(out, "+alias: Heating (eco)") {
				t.Errorf("the preview does not diff the piped candidate:\n%s", out)
			}
			if policyBody != "" && !strings.Contains(out, "policy: 1 checked, allowed (profile agent)") {
				t.Errorf("the plan did not check the piped candidate's own id:\n%s", out)
			}
			out, err = runPolicyCLI(t, ts.dir, "auto", "apply", "heating", "-f", pipedFile(t, candidate), "--confirm")
			if err != nil {
				t.Fatalf("confirmed apply of a piped candidate: %v\n%s", err, out)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) == 0 {
				t.Fatal("nothing was written")
			}
			for _, b := range bodies {
				if _, body, _ := strings.Cut(b, ":"); body != candidate {
					t.Errorf("PUT %q sent %q, want the piped candidate", b, body)
				}
			}
		})
	}
}
