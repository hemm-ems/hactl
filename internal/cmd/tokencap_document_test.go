package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// H-10's truncation clause, on output that is not --json.
//
// The token cap chops at a byte boundary, walking back only far enough to keep
// the bytes valid UTF-8, and appends a plain-English notice. On prose that is a
// shortened answer. On a DOCUMENT it is a syntax error delivered at exit 0:
//
//   - `dash show lovelace-dev --raw` returned 2 096 bytes of a 91 541-byte
//     config; `python3 -m json.tool` on it fails. `--raw`'s own help says it
//     exists "for LLM round-trip editing".
//   - `dash show <d> --yaml` failed yaml.safe_load with "could not find
//     expected :"; `--view <v>` emits JSON unconditionally and was cut the same
//     way.
//   - `auto cat <a> > backup.yaml` on a 579-token automation truncated inside a
//     quoted scalar, so the backup a caller had just taken did not parse.
//   - `hactl completion bash > /etc/bash_completion.d/hactl`, the line the
//     command's own --help prints, produced a script `bash -n` rejects at
//     line 60.
//
// The pole is the one --json already sets and docs/manual.md already states:
// output whose contract is "this parses" is not capped, and the caller narrows
// it with filters. applyTokenPolicy now exempts anything a command declared
// with markStructuredOutput, and these tests hold that exemption to reaching
// the bytes.
//
// --tokensmax 1 is the instrument: it caps anything over ~4 bytes, so a
// document that survives it survives any cap, and no large fixture is needed to
// pose the question.
// ---------------------------------------------------------------------------

// capNotice is the marker applyTokenPolicy appends when it truncates. Its
// presence in a document is the defect itself — it is prose, inside the stream.
const capNotice = "output capped at"

// TestDocumentOutputIsNeverTokenCapped drives every command whose output is a
// document under --tokensmax 1 and requires the whole document back, parsing.
//
// The cases are grouped by the parser that has to accept the result, because
// "was not truncated" and "still parses" are different claims and the second is
// the one a caller depends on.
func TestDocumentOutputIsNeverTokenCapped(t *testing.T) {
	f := buildDocumentFixture(t)

	for _, tc := range []struct {
		name  string
		args  []string
		parse func(t *testing.T, out string)
	}{
		{
			name:  "dash show --raw",
			args:  []string{"dash", "show", "main", "--raw"},
			parse: parsesAsJSON,
		},
		{
			name:  "dash show --yaml",
			args:  []string{"dash", "show", "main", "--yaml"},
			parse: parsesAsYAML,
		},
		{
			// --view emits JSON whether or not --json was passed, so it is a
			// document by what it writes, not by which flag asked for it.
			name:  "dash show --view",
			args:  []string{"dash", "show", "main", "--view", "overview"},
			parse: parsesAsJSON,
		},
		{
			name:  "auto cat",
			args:  []string{"auto", "cat", "morning"},
			parse: parsesAsYAML,
		},
		{
			name:  "script cat",
			args:  []string{"script", "cat", "wakeup"},
			parse: parsesAsYAML,
		},
		{
			name:  "helper cat",
			args:  []string{"helper", "cat", "guest_mode"},
			parse: parsesAsYAML,
		},
		{
			name:  "tpl cat",
			args:  []string{"tpl", "cat", "room_temp"},
			parse: parsesAsYAML,
		},
		{
			name:  "config file",
			args:  []string{"config", "file", "configuration.yaml"},
			parse: parsesAsYAML,
		},
		{
			name:  "config block",
			args:  []string{"config", "block", "automations.yaml", "morning"},
			parse: parsesAsYAML,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runCapped(t, f.dir, tc.args...)
			if strings.Contains(out, capNotice) {
				t.Fatalf("--tokensmax 1 truncated a document and said so inside it:\n%s", out)
			}
			tc.parse(t, out)
		})
	}
}

// TestCompletionScriptIsNeverTokenCapped is the same clause for cobra's own
// generated output, which this package cannot mark from inside the command
// body because the body is cobra's.
//
// Every shell is driven, not one: the generators are four separate templates,
// and the truncation cut a different construct in each.
func TestCompletionScriptIsNeverTokenCapped(t *testing.T) {
	// cobra captures the writer ONCE, when InitDefaultCompletionCmd builds the
	// command (completions.go: `out := c.OutOrStdout()`), so a completion
	// command initialised by an earlier test in this package is bound to
	// os.Stdout forever and this test would read an empty buffer while the
	// script scrolled past on the terminal. Drop it and let ExecuteC rebuild it
	// inside the captured-output window, which is the order the real binary
	// runs in: Execute() calls SetOut before ExecuteC.
	dropCompletionCmd := func() {
		for _, c := range rootCmd.Commands() {
			if c.Name() == "completion" {
				rootCmd.RemoveCommand(c)
			}
		}
	}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			dropCompletionCmd()
			var buf bytes.Buffer
			if err := RunWithOutput([]string{"hactl", "completion", shell, "--tokensmax", "1"}, &buf); err != nil {
				t.Fatalf("completion %s failed: %v", shell, err)
			}
			out := buf.String()
			if strings.Contains(out, capNotice) {
				t.Fatalf("completion %s was token-capped mid-script:\n%s", shell, out)
			}
			// A generated completion script is thousands of bytes; the cap
			// would have landed around 4. Assert on size too, so a generator
			// that started emitting nothing cannot pass this vacuously.
			if len(out) < 1000 {
				t.Fatalf("completion %s produced %d bytes — too short to prove the exemption did anything", shell, len(out))
			}
		})
	}
}

// TestProseOutputIsStillTokenCapped is the control.
//
// The exemption must not have become "nothing is ever capped": the cap is a
// context-budget tool an agent relies on, and a fix that quietly disabled it
// everywhere would look identical to the fix above on every test that only
// checks documents. A table listing is prose, and it is capped.
func TestProseOutputIsStillTokenCapped(t *testing.T) {
	f := buildDocumentFixture(t)
	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "ent", "ls", "--dir", f.dir, "--tokensmax", "1"}, &buf); err != nil {
		t.Fatalf("ent ls failed: %v", err)
	}
	if !strings.Contains(buf.String(), capNotice) {
		t.Errorf("--tokensmax 1 no longer caps an ordinary text listing — the document exemption is too wide:\n%s", buf.String())
	}
}

// TestVerbatimLeavesAreDispositionedForTheTokenCap is the closure clause.
//
// `verbatimByDesign` is derived from the live cobra tree by leaf name, so a new
// `cat`-shaped command joins it automatically — and would join it with no
// statement about whether the cap may corrupt it. This walks that set and
// requires every leaf to be either driven above or recorded here with a reason,
// so the gap cannot be silent.
func TestVerbatimLeavesAreDispositionedForTheTokenCap(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()

	// The paths TestDocumentOutputIsNeverTokenCapped drives, keyed the same way
	// classifyCommand keys the tree.
	driven := map[string]bool{
		"auto cat": true, "script cat": true, "helper cat": true, "tpl cat": true,
		"config file": true, "config block": true,
	}
	// Verbatim leaves the cap MAY truncate, with the reason.
	capAllowed := map[string]string{
		"auto diff": "a diff is a human reading of a change, not a document anything parses; " +
			"compactDiffContext exists precisely so the changed hunks survive the default cap, " +
			"and removing the cap here would undo that",
		"script diff": "same shape and same reasoning as auto diff — the renderer is built around " +
			"the cap rather than against it",
		"tpl eval": "the result is one rendered scalar; HA renders a template to a string and " +
			"hactl echoes it, so there is no document grammar a cut could break, and --json " +
			"already wraps it in an envelope that is exempt",
	}

	var undecided []string
	for _, leaf := range leafCommands(rootCmd) {
		path := strings.Join(cmdArgsOf(leaf), " ")
		if classifyCommand(leaf, path) != catVerbatim {
			continue
		}
		if driven[path] {
			continue
		}
		if reason, ok := capAllowed[path]; ok {
			if len(reason) < 40 {
				t.Errorf("%q is exempted from the document rule with a reason too thin to be one: %q", path, reason)
			}
			continue
		}
		undecided = append(undecided, path)
	}
	sort.Strings(undecided)
	for _, path := range undecided {
		t.Errorf("verbatim command %q says nothing about the token cap — either drive it in "+
			"TestDocumentOutputIsNeverTokenCapped (and call markStructuredOutput in its "+
			"implementation), or record in capAllowed why a truncated answer is still an answer", path)
	}
}

func parsesAsJSON(t *testing.T, out string) {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
}

func parsesAsYAML(t *testing.T, out string) {
	t.Helper()
	var v any
	if err := yaml.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not valid YAML: %v\n%s", err, out)
	}
	if v == nil {
		t.Fatalf("output parsed to nothing — the fixture returned an empty document:\n%q", out)
	}
}

// runCapped runs a command with the smallest cap that still means something,
// so any un-exempted command is truncated.
func runCapped(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"hactl"}, args...)
	full = append(full, "--dir", dir, "--tokensmax", "1")
	var buf bytes.Buffer
	if err := RunWithOutput(full, &buf); err != nil {
		t.Fatalf("%v failed: %v\n%s", args, err, buf.String())
	}
	return buf.String()
}

// documentFixture is a fake HA plus companion whose payloads are all
// comfortably larger than one token, so the cap has something to cut.
type documentFixture struct{ dir string }

func buildDocumentFixture(t *testing.T) *documentFixture {
	t.Helper()

	// A dashboard config big enough that a 1-token cap lands deep inside it.
	views := make([]map[string]any, 0, 4)
	for i := range 4 {
		cards := make([]map[string]any, 0, 6)
		for j := range 6 {
			cards = append(cards, map[string]any{
				"type":     "entities",
				"title":    fmt.Sprintf("Card %d-%d with a title long enough to matter", i, j),
				"entities": []string{"light.kitchen", "sensor.temp", "binary_sensor.door"},
			})
		}
		views = append(views, map[string]any{
			"title": fmt.Sprintf("View %d", i), "path": fmt.Sprintf("view-%d", i),
			"type": "masonry", "cards": cards,
		})
	}
	views[0]["path"] = "overview"

	states := []map[string]any{
		{
			"entity_id": "light.kitchen", "state": "on",
			"last_changed": "2026-01-01T09:00:00+00:00", "last_updated": "2026-01-01T09:00:00+00:00",
			"attributes": map[string]any{"friendly_name": "Kitchen Light"},
		},
		{
			"entity_id": "automation.morning", "state": "on",
			"last_changed": "2026-01-01T08:00:00+00:00", "last_updated": "2026-01-01T08:00:00+00:00",
			"attributes": map[string]any{
				"friendly_name": "Morning Routine", "mode": "single", "current": 0, "id": "morning",
			},
		},
		{
			"entity_id": "script.wakeup", "state": "on",
			"last_changed": "2026-01-01T07:00:00+00:00", "last_updated": "2026-01-01T07:00:00+00:00",
			"attributes": map[string]any{"friendly_name": "Wake Up", "mode": "single", "current": 0},
		},
	}

	ts := startCmdServer(t,
		map[string]any{
			"lovelace/dashboards/list": []map[string]any{{"id": "d1", "url_path": "main", "title": "Main", "mode": "storage"}},
			"lovelace/config":          map[string]any{"views": views},
		},
		map[string]http.HandlerFunc{
			"/api/states": func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(states)
			},
		})

	// A YAML body long enough that the cap would land in the middle of a
	// quoted scalar — the shape that made `auto cat > backup.yaml` produce an
	// unparseable backup.
	const longYAML = "id: morning\n" +
		"alias: Morning Routine\n" +
		"description: >-\n" +
		"  A description long enough that a one-token cap lands well inside it, which is the\n" +
		"  situation a real automation of a few hundred tokens is in under the default cap.\n" +
		"triggers:\n" +
		"  - trigger: time\n" +
		"    at: \"07:00:00\"\n" +
		"actions:\n" +
		"  - action: light.turn_on\n" +
		"    target:\n" +
		"      entity_id: light.kitchen\n" +
		"    data:\n" +
		"      message: 'a quoted scalar the cap used to cut in half, leaving no closing quote'\n"

	cc := startDocumentCompanion(t, longYAML)
	appendCompanionURL(t, ts.dir, cc)
	return &documentFixture{dir: ts.dir}
}

// startDocumentCompanion answers the read routes the verbatim `cat` family and
// `config file`/`config block` call, each with a body long enough for the cap
// to have cut it.
func startDocumentCompanion(t *testing.T, longYAML string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	yamlBody := func(w http.ResponseWriter, content string) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{
			"status": "ok", "id": "morning", "unique_id": "room_temp",
			"domain": "input_boolean", "path": "configuration.yaml", "content": content,
		})
		_, _ = w.Write(body)
	}
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok","version":"2026.7.8"}`)
	})
	for _, route := range []string{
		"/v1/config/automation", "/v1/config/script", "/v1/config/helper",
		"/v1/config/template", "/v1/config/file", "/v1/config/block",
	} {
		mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) { yamlBody(w, longYAML) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// appendCompanionURL wires a companion stub into an existing fixture .env.
func appendCompanionURL(t *testing.T, dir string, srv *httptest.Server) {
	t.Helper()
	envPath := filepath.Join(dir, ".env")
	env, err := os.ReadFile(envPath) //nolint:gosec // path from t.TempDir(), not user input
	if err != nil {
		t.Fatalf("reading fixture .env: %v", err)
	}
	env = fmt.Appendf(env, "COMPANION_URL=%s\nCOMPANION_TOKEN=test-token\n", srv.URL)
	if err := os.WriteFile(envPath, env, 0o600); err != nil { //nolint:gosec // fixture dir from t.TempDir()
		t.Fatalf("wiring COMPANION_URL into fixture .env: %v", err)
	}
}
