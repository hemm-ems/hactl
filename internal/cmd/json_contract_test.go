package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/pkg/ids"
)

// ---------------------------------------------------------------------------
// H-10 — "--json is a machine contract: it parses, it is complete, and it is
// never silently truncated" (see INVARIANTS.md).
//
// This file walks the LIVE cobra command tree (rootCmd.Commands(),
// recursively) rather than a hand-kept list, so a newly added command is
// covered automatically — "N commands silently ignore --json" becomes a
// build failure here, not something a human has to notice against a real HA.
// ---------------------------------------------------------------------------

// leafCommands returns every *runnable* command in the tree — anything with
// a Run/RunE, including a command that is both runnable itself AND has
// subcommands (e.g. bare `hactl log` vs `hactl log show`; cobra allows both).
// Pure grouping commands (no Run/RunE, only there to hold subcommands) are
// walked into but never themselves returned.
func leafCommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() {
			out = append(out, c)
		}
		for _, ch := range c.Commands() {
			walk(ch)
		}
	}
	for _, c := range root.Commands() {
		walk(c)
	}
	return out
}

// cmdArgsOf returns a command's path with the root's own name stripped, e.g.
// "hactl ent ls" -> []string{"ent", "ls"}.
func cmdArgsOf(c *cobra.Command) []string {
	full := strings.Fields(c.CommandPath())
	if len(full) > 0 {
		full = full[1:]
	}
	return full
}

// isMutating reports whether a command writes to HA. Every write-family
// command in hactl is dry-run-by-default and gated behind --confirm (H-2 in
// INVARIANTS.md), so a registered --confirm flag is the generic,
// self-maintaining signal: a new write command is excluded automatically as
// long as it follows H-2, with no per-command list to keep in sync.
func isMutating(c *cobra.Command) bool {
	return c.Flags().Lookup("confirm") != nil
}

// metaCommands are cobra/administrative commands with no HA data contract of
// their own: the four named in this test's brief (setup, mcp, completion,
// rtfm), plus cobra's own built-in "help" command, which is the same kind of
// meta machinery and prints the same content --help does. Matched against the
// command's TOP-LEVEL ancestor name, so e.g. "completion bash" is excluded
// via "completion" without listing every shell.
var metaCommands = map[string]bool{
	"setup":      true,
	"mcp":        true,
	"completion": true,
	"rtfm":       true,
	"help":       true,
}

// verbatimByDesign holds leaf command names (the LAST path segment) whose
// entire point is to emit content verbatim for piping/round-tripping, not a
// structured data query:
//   - `cat`/`diff` exist for YAML round-tripping — see the "pipe-friendly,
//     round-trippable" comment on script.go's runScriptCat, and the identical
//     pattern on auto/helper/tpl cat and auto/script diff.
//   - `eval`'s result IS a rendered template string (tpl eval).
//   - `file`/`block` print a raw config file or block verbatim (config file,
//     config block).
//
// Forcing --json on these would fight their actual, documented contract, so
// they are excluded from the JSON sweep on design grounds — not because they
// currently fail.
var verbatimByDesign = map[string]bool{
	"cat":   true,
	"diff":  true,
	"eval":  true,
	"file":  true,
	"block": true,
}

// actionOnly holds full command paths (minus "hactl ") for commands that
// perform a local action and report a short confirmation line, not a data
// query — the same category as verbatimByDesign, just not name-based.
// Neither has a --confirm flag (they don't write to HA — the cache is purely
// local — so isMutating doesn't catch them), and neither returns list/object
// data: `cache refresh` prints "traces refreshed"/"logs refreshed", `cache
// clear` prints "cache cleared". Forcing --json here would mean inventing a
// payload shape these commands were never designed to have.
var actionOnly = map[string]bool{
	"cache refresh": true,
	"cache clear":   true,
}

// companionRequired lists full command paths that need a working companion
// connection (internal/companion.Discover, then the companion's own HTTP
// routes) just to run at all — not merely to produce a richer answer, but to
// avoid a hard connection error (confirmed by reading each: helper ls/show
// call connectCompanion directly; config files does too; ref scan/validate
// call connectRefSources, which returns an error the moment companion
// discovery fails; companion status/logs and companion wireguard status are
// companion commands by definition).
//
// startCmdServer (ws_cmd_test.go) mocks only the core HA HTTP+WS surface, not
// the companion side-car; standing up a second, companion-shaped mock server
// is out of proportion for this sweep. This is the "cannot be exercised
// without a live HA[-equivalent stand-in]" skip list the task brief allows
// for — kept to exactly the commands that need it, and printed by
// TestJSONContract so the gap is visible rather than silent.
//
// Commands that merely USE the companion but degrade gracefully when it is
// unavailable (`ent related`, `health`) are NOT on this list — they are
// exercised for real, companion-less, exactly as a real offline-companion
// instance would.
var companionRequired = map[string]bool{
	"helper ls":                  true,
	"helper show":                true,
	"config files":               true,
	"ref scan":                   true,
	"ref validate":               true,
	"companion status":           true,
	"companion logs":             true,
	"companion wireguard status": true,
}

type contractCategory int

const (
	catEnforced contractCategory = iota
	catMutating
	catMeta
	catVerbatim
	catActionOnly
	catCompanionRequired
)

func classifyCommand(leaf *cobra.Command, path string) contractCategory {
	switch {
	case isMutating(leaf):
		return catMutating
	case metaCommands[topCommandName(leaf)]:
		return catMeta
	case verbatimByDesign[leaf.Name()]:
		return catVerbatim
	case actionOnly[path]:
		return catActionOnly
	case companionRequired[path]:
		return catCompanionRequired
	default:
		return catEnforced
	}
}

// contractFixture is the shared HA+dir fixture every enforced command in
// TestJSONContract runs against, plus the one piece of state a caller cannot
// know ahead of time (the stable `log:` id `log show` needs as input).
type contractFixture struct {
	dir       string
	logShowID string
}

// buildContractFixture stands up ONE shared fake HA (startCmdServer) with
// enough data — states, registries, dashboards, logbook, history, config
// entries, a flow, an issue, and a trace — to exercise every enforced
// command in one pass. Commands are read-only here (mutating ones are
// excluded before we ever get this far), so sharing one server/dir across
// all of them is safe.
func buildContractFixture(t *testing.T) *contractFixture {
	t.Helper()

	states := []map[string]any{
		{
			"entity_id": "light.kitchen", "state": "on",
			"last_changed": "2026-01-01T09:00:00+00:00", "last_updated": "2026-01-01T09:00:00+00:00",
			"attributes": map[string]any{"friendly_name": "Kitchen Light"},
		},
		{
			"entity_id": "sensor.temp", "state": "21.5",
			"last_changed": "2026-01-01T10:00:00+00:00", "last_updated": "2026-01-01T10:00:00+00:00",
			"attributes": map[string]any{"friendly_name": "Temp Sensor"},
		},
		{
			"entity_id": "binary_sensor.door", "state": "off",
			"last_changed": "2026-01-01T08:00:00+00:00", "last_updated": "2026-01-01T08:00:00+00:00",
			"attributes": map[string]any{},
		},
		{
			"entity_id": "automation.morning", "state": "on",
			"last_changed": "2026-01-01T08:00:00+00:00", "last_updated": "2026-01-01T08:00:00+00:00",
			"attributes": map[string]any{
				"friendly_name": "Morning Routine", "mode": "single", "current": 0,
				"id": "1700000000000", "last_triggered": "2026-01-01T08:00:00+00:00",
			},
		},
		{
			"entity_id": "script.wakeup", "state": "on",
			"last_changed": "2026-01-01T07:00:00+00:00", "last_updated": "2026-01-01T07:00:00+00:00",
			"attributes": map[string]any{
				"friendly_name": "Wake Up", "mode": "single", "current": 0,
				"last_triggered": "2026-01-01T07:00:00+00:00",
			},
		},
		{
			"entity_id": "group.livingroom", "state": "on",
			"last_changed": "2026-01-01T06:00:00+00:00", "last_updated": "2026-01-01T06:00:00+00:00",
			"attributes": map[string]any{"entity_id": []string{"light.kitchen"}, "friendly_name": "Living Room"},
		},
		{
			"entity_id": "update.mydomain", "state": "off",
			"last_changed": "2026-01-01T05:00:00+00:00", "last_updated": "2026-01-01T05:00:00+00:00",
			"attributes": map[string]any{"title": "My Domain", "installed_version": "1.2.3"},
		},
	}

	const historyJSON = `[[
		{"entity_id":"sensor.temp","state":"21.5","last_changed":"2026-01-01T10:00:00+00:00","attributes":{}},
		{"entity_id":"sensor.temp","state":"22.1","last_changed":"2026-01-01T11:00:00+00:00","attributes":{}},
		{"entity_id":"sensor.temp","state":"45.0","last_changed":"2026-01-01T12:00:00+00:00","attributes":{}}
	]]`

	const logbookJSON = `[
		{"entity_id":"light.kitchen","name":"Kitchen Light","state":"on","when":"2026-01-01T10:00:00+00:00","context_user_id":"user1"},
		{"entity_id":"light.kitchen","name":"Kitchen Light","state":"off","when":"2026-01-01T09:00:00+00:00","context_event_type":"automation_triggered","context_name":"Morning Routine"}
	]`

	httpHandlers := map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(states)
		},
		"/api/states/": func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimPrefix(r.URL.Path, "/api/states/")
			for _, s := range states {
				if s["entity_id"] == id {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(s)
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)
		},
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, historyJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, logbookJSON)
		},
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, "2026-01-01 05:00:00.000 ERROR (MainThread) [mydomain.sensor] boom\n")
		},
		"/api/config": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"version":"2026.7.0","location_name":"Home","state":"RUNNING","time_zone":"UTC","components":["recorder"],"safe_mode":false}`)
		},
		"/api/config/config_entries/entry": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[{"entry_id":"entry1","domain":"mydomain","title":"My Domain","state":"loaded","source":"user","supports_options":true,"supports_reconfigure":false}]`)
		},
		"/api/diagnostics/config_entry/entry1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"data":{"options":{"foo":"bar"}}}`)
		},
		"/api/config/config_entries/flow/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"flow_id":"flow123","type":"form","step_id":"init","handler":"mydomain","data_schema":[]}`)
		},
	}

	wsResponses := map[string]any{
		"config/area_registry/list":   []map[string]any{{"area_id": "kitchen_id", "name": "Kitchen"}},
		"config/floor_registry/list":  []map[string]any{{"floor_id": "ground", "name": "Ground", "level": 0}},
		"config/label_registry/list":  []map[string]any{{"label_id": "energy", "name": "Energy"}},
		"config/device_registry/list": []map[string]any{{"id": "dev1", "name": "My Device", "manufacturer": "Acme", "model": "X1", "area_id": "kitchen_id"}},
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "light.kitchen", "device_id": "dev1", "area_id": "kitchen_id", "labels": []string{"energy"}},
			{"entity_id": "sensor.temp", "device_id": "dev1", "area_id": "kitchen_id"},
			{"entity_id": "automation.morning", "area_id": "kitchen_id"},
			{"entity_id": "script.wakeup"},
		},
		// HA's trace/list returns a FLAT array of trace summaries; haapi's
		// WSClient.TraceList groups them into the "domain.item_id" map
		// client-side, so the fixture must mimic the real wire shape (a flat
		// array), not the already-grouped result type.
		"trace/list": []map[string]any{
			{
				"run_id": "run1", "domain": "automation", "item_id": "1700000000000",
				"last_step": "action/0", "state": "stopped", "script_execution": "finished",
				"trigger": "time", "error": "",
				"timestamp": map[string]any{"start": "2026-01-01T08:00:00+00:00", "finish": "2026-01-01T08:00:01+00:00"},
			},
			{
				"run_id": "run2", "domain": "script", "item_id": "wakeup",
				"last_step": "action/0", "state": "stopped", "script_execution": "finished",
				"trigger": "", "error": "",
				"timestamp": map[string]any{"start": "2026-01-01T07:00:00+00:00", "finish": "2026-01-01T07:00:01+00:00"},
			},
		},
		"trace/get": map[string]any{
			"run_id": "run2", "domain": "script", "item_id": "wakeup", "last_step": "action/0",
			"state": "stopped", "script_execution": "finished",
			"timestamp": map[string]any{"start": "2026-01-01T07:00:00+00:00", "finish": "2026-01-01T07:00:01+00:00"},
			"trace":     map[string]any{"action/0": []map[string]any{{"path": "action/0", "timestamp": "2026-01-01T07:00:00+00:00"}}},
		},
		"repairs/list_issues": map[string]any{
			"issues": []map[string]any{
				{"domain": "mydomain", "issue_id": "issue1", "severity": "warning", "translation_key": "tk", "is_fixable": false, "ignored": false, "breaks_in_ha_version": ""},
			},
		},
		"lovelace/dashboards/list": []map[string]any{{"id": "dash1", "url_path": "main", "title": "Main", "mode": "storage"}},
		"lovelace/config": map[string]any{
			"views": []map[string]any{
				{"title": "Main", "path": "main", "type": "masonry", "cards": []map[string]any{
					{"type": "entities", "entities": []string{"light.kitchen"}},
				}},
			},
		},
		"lovelace/resources": []map[string]any{{"id": "1", "type": "module", "url": "/local/custom.js"}},
		// cc ls/show discover components from loaded integration manifests
		// (WS manifest/list); a matching update.* entity only ENRICHES an
		// already-confirmed domain's version, it never adds one on its own.
		"manifest/list": []map[string]any{
			{"domain": "mydomain", "name": "My Domain", "version": "1.2.3", "is_built_in": false},
		},
	}

	ts := startCmdServer(t, wsResponses, httpHandlers)

	// `log show <id>` resolves a previously-registered `log:` short id from
	// the local ids registry — it never re-fetches logs — so seed one here
	// the way `hactl log` itself would have.
	reg := ids.NewRegistry(filepath.Join(ts.dir, "cache", "ids.json"))
	logShowID := reg.GetOrCreate("log", "2026-01-01 05:00:00.000|mydomain.sensor|boom")
	if err := reg.Save(); err != nil {
		t.Fatalf("seeding ids registry: %v", err)
	}

	return &contractFixture{dir: ts.dir, logShowID: logShowID}
}

// contractPosArgs maps a full command path (minus "hactl ") to the
// positional arguments it needs against buildContractFixture's data. Every
// command classified catEnforced by classifyCommand MUST have an entry here
// (asserted by TestJSONContract) so a command with nowhere to route falls
// out as a loud test failure, not a silent gap.
func contractPosArgs(f *contractFixture) map[string][]string {
	return map[string][]string{
		"version":             nil,
		"area ls":             nil,
		"auto ls":             nil,
		"auto show":           {"morning"},
		"cache status":        nil,
		"changes":             nil,
		"cc ls":               nil,
		"cc show":             {"mydomain"},
		"cc logs":             {"mydomain"},
		"device ls":           nil,
		"device show":         {"dev1"},
		"ent ls":              nil,
		"ent show":            {"light.kitchen"},
		"ent hist":            {"light.kitchen"},
		"ent anomalies":       {"light.kitchen"},
		"ent related":         {"light.kitchen"},
		"ent who":             {"light.kitchen"},
		"floor ls":            nil,
		"health":              nil,
		"config entries":      nil,
		"config show":         {"entry1"},
		"config flow-inspect": {"flow123"},
		"issues":              nil,
		"label ls":            nil,
		"log":                 nil,
		"script ls":           nil,
		"script show":         {"wakeup"},
		"trace show":          {"script.wakeup/run2"},
		"dash ls":             nil,
		"dash show":           nil,
		"dash resources":      nil,
		"dash grep":           {"light.kitchen"},
		"log show":            {f.logShowID},
	}
}

// TestJSONContract walks the live cobra command tree and asserts the --json
// contract (H-10) on every read command it can exercise. New commands are
// covered automatically the moment they're added to the tree — the whole
// point is that "N commands silently ignore --json" becomes a test failure
// here, not something a human has to notice.
func TestJSONContract(t *testing.T) {
	// Force cobra's lazily-added builtin commands into the tree so they are
	// seen (and correctly excluded, see metaCommands) regardless of what ran
	// before this test in the same process.
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()

	fixture := buildContractFixture(t)
	posArgs := contractPosArgs(fixture)

	leaves := leafCommands(rootCmd)
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].CommandPath() < leaves[j].CommandPath() })

	var tested, skippedCompanion, excludedAction []string

	for _, leaf := range leaves {
		args := cmdArgsOf(leaf)
		path := strings.Join(args, " ")

		switch classifyCommand(leaf, path) {
		case catMutating, catMeta, catVerbatim:
			// Out of scope by definition (write command / cobra machinery /
			// intentional verbatim passthrough) — not asserted, not logged
			// individually to keep signal high; the categories themselves are
			// documented above.
			continue
		case catActionOnly:
			excludedAction = append(excludedAction, path)
			continue
		case catCompanionRequired:
			skippedCompanion = append(skippedCompanion, path)
			continue
		}

		extra, ok := posArgs[path]
		if !ok {
			t.Errorf("H-10 sweep: leaf command %q has no fixture registered in contractPosArgs — "+
				"either add one, or classify it (mutating/meta/verbatim/actionOnly/companionRequired) "+
				"so the gap is not silent", path)
			continue
		}

		tested = append(tested, path)
		t.Run(strings.ReplaceAll(path, " ", "_"), func(t *testing.T) {
			assertJSONContract(t, fixture.dir, args, extra)
		})
	}

	sort.Strings(skippedCompanion)
	sort.Strings(excludedAction)
	t.Logf("H-10 sweep: asserted the --json contract on %d read command(s): %s",
		len(tested), strings.Join(tested, ", "))
	t.Logf("H-10 sweep: excluded %d local-action command(s) (no structured payload by design): %s",
		len(excludedAction), strings.Join(excludedAction, ", "))
	t.Logf("H-10 sweep: SKIPPED %d command(s) — require a working companion connection "+
		"(internal/companion.Discover + the companion's own HTTP routes); startCmdServer mocks only "+
		"the core HA HTTP+WS surface, and standing up a second companion-shaped mock is out of "+
		"proportion for this sweep: %s", len(skippedCompanion), strings.Join(skippedCompanion, ", "))
}

// assertJSONContract runs cmdArgs+extra with --json and --top 1 against the
// shared fixture at dir, and checks the three H-10 properties: (1) strict
// JSON, (3) no leading human header line, and — only when the output is
// array-shaped, see below — (2) a second run with --top 1000 reports the
// same element count.
func assertJSONContract(t *testing.T, dir string, cmdArgs, extra []string) {
	t.Helper()

	run := func(top int) string {
		t.Helper()
		args := make([]string, 0, 1+len(cmdArgs)+len(extra)+5)
		args = append(args, "hactl")
		args = append(args, cmdArgs...)
		args = append(args, extra...)
		args = append(args, "--dir", dir, "--json", "--top", strconv.Itoa(top))
		var buf bytes.Buffer
		if err := RunWithOutput(args, &buf); err != nil {
			t.Fatalf("command failed: %v\nargs: %v\noutput: %s", err, args[1:], buf.String())
		}
		return buf.String()
	}

	small := run(1)

	// (1) stdout parses strictly as JSON.
	var parsedSmall any
	if err := json.Unmarshal([]byte(small), &parsedSmall); err != nil {
		t.Fatalf("--json output does not parse as JSON: %v\noutput:\n%s", err, small)
	}

	// (3) no human header line before the JSON — the first non-whitespace
	// byte must open a JSON value.
	trimmed := strings.TrimLeft(small, " \t\r\n")
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		t.Fatalf("--json output does not start with JSON (a human header line precedes it):\n%s", small)
	}

	// (2) --top must not change the number of elements in JSON output
	// (defeats defect A generically). This only means something for
	// array-shaped output — a single-object result (script show, version,
	// health, cc show, ...) has no "elements" for --top to truncate, and
	// some object-shaped commands legitimately report live, --top-unrelated
	// state (e.g. `cache status`'s on-disk db sizes, which grow simply from
	// being opened) that a byte/deep-equality check across two separate
	// process runs would flag as spuriously "changed". So: skip the second
	// call entirely unless the first result is a JSON array.
	smallArr, ok := parsedSmall.([]any)
	if !ok {
		return
	}

	large := run(1000)
	var parsedLarge any
	if err := json.Unmarshal([]byte(large), &parsedLarge); err != nil {
		t.Fatalf("--json output (--top 1000) does not parse as JSON: %v\noutput:\n%s", err, large)
	}
	largeArr, ok := parsedLarge.([]any)
	if !ok {
		t.Fatalf("--json output shape changed between --top values: --top 1 was an array, --top 1000 was not\n--top 1:\n%s\n--top 1000:\n%s",
			small, large)
	}
	if len(smallArr) != len(largeArr) {
		t.Fatalf("--top changed the number of JSON elements (defect A): --top 1 -> %d element(s), --top 1000 -> %d element(s)\n--top 1:\n%s\n--top 1000:\n%s",
			len(smallArr), len(largeArr), small, large)
	}
}

// TestRootHelp_NeverTokenTruncated is the direct regression test for defect C:
// cobra's help writer used to go through the same --tokensmax cap as normal
// output, so `hactl --help` (2000+ bytes; --tokensmax defaults to 500 tokens
// ~= 2000 bytes) was cut off mid-word. It goes through the real
// Execute()-shaped path (cmd.RunWithOutput -> RunWithOutputContext ->
// applyTokenPolicy), unlike cmd_test.go's TestRootCommandHelp, which calls
// rootCmd.Execute() directly and so never went through applyTokenPolicy at
// all — which is exactly why that test never caught this.
func TestRootHelp_NeverTokenTruncated(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "--help"}, &buf); err != nil {
		t.Fatalf("--help failed: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "output capped at") {
		t.Fatalf("--help output was token-capped (defect C):\n%s", out)
	}
	if !strings.Contains(out, issuesURL) {
		t.Errorf("--help output looks truncated: missing trailing content (%q)\noutput:\n%s", issuesURL, out)
	}
	// Sanity: the default --tokensmax=500 (~2000 bytes) would have triggered
	// truncation had help not been exempted, so this test cannot pass
	// vacuously against a short help screen.
	if len(out) < 2000 {
		t.Fatalf("help output unexpectedly short (%d bytes) — cannot prove the --tokensmax exemption "+
			"actually did anything; did the root command's command list shrink, or is --tokensmax no "+
			"longer 500 by default?", len(out))
	}
}

// TestVersionJSON_Shape checks `hactl version --json` beyond the generic
// contract: it must carry the same information the text form does.
func TestVersionJSON_Shape(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "version", "--json"}, &buf); err != nil {
		t.Fatalf("version --json failed: %v", err)
	}
	var got versionInfo
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("version --json did not parse: %v\n%s", err, buf.String())
	}
	if got.Version == "" {
		t.Errorf("version --json: empty version, got %+v", got)
	}
	if got.Project != projectURL || got.Issues != issuesURL {
		t.Errorf("version --json: project/issues URLs missing or wrong: %+v", got)
	}
}

// TestScriptShowJSON_Shape checks `hactl script show --json` beyond the
// generic contract: per the task brief, it must carry the script id, state,
// mode, and the trace list keyed by `trc:` ids.
func TestScriptShowJSON_Shape(t *testing.T) {
	fixture := buildContractFixture(t)

	var buf bytes.Buffer
	args := []string{"hactl", "script", "show", "wakeup", "--dir", fixture.dir, "--json"}
	if err := RunWithOutput(args, &buf); err != nil {
		t.Fatalf("script show --json failed: %v\n%s", err, buf.String())
	}

	var got scriptShowResult
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("script show --json did not parse: %v\n%s", err, buf.String())
	}
	if got.ID != "wakeup" {
		t.Errorf("id = %q, want %q", got.ID, "wakeup")
	}
	if got.State == "" {
		t.Error("state missing")
	}
	if got.Mode == "" {
		t.Error("mode missing")
	}
	if len(got.Traces) == 0 {
		t.Fatalf("traces missing: %+v", got)
	}
	if !strings.HasPrefix(got.Traces[0].ID, "trc:") {
		t.Errorf("trace id should be a trc: short id, got %q", got.Traces[0].ID)
	}
}
