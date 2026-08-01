package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/companion"
)

// --- #36: the two summaries of one run must reconcile (H-11) ---

// TestDanglingSummaryIsOneSentenceInBothPlaces pins the count contract that
// `ref validate --exit-code` broke: its one-line summary said "318 dangling
// reference(s) found" while the same run's report body said "429 dangling
// reference(s) to 318 entity(ies)" (measured on a real instance, 2026-07-31).
// 318 is the DEDUPLICATED ENTITY count — `&danglingRefsError{len(uniq)}` — so
// the number was right and the noun was the other measurement's.
//
// The fixture is deliberately 3 references to 2 entities: a count that divides
// evenly hides which of the two a label names (WP7's version of the fencepost
// lesson).
func TestDanglingSummaryIsOneSentenceInBothPlaces(t *testing.T) {
	companionSrv := refEntitiesServer(t, `{"entities":[
		{"location":"automations.yaml","path":"[0].trigger[0].entity_id","key":"entity_id","matched_value":"sensor.gone"},
		{"location":"automations.yaml","path":"[1].action[0].entity_id","key":"entity_id","matched_value":"sensor.gone"},
		{"location":"scripts.yaml","path":"morning.sequence[0].entity_id","key":"entity_id","matched_value":"light.gone"}
	]}`)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":    []any{},
		"lovelace/config":             map[string]any{"views": []any{}},
		"config/entity_registry/list": []any{map[string]any{"entity_id": "sensor.real"}},
	}, statesHandler(`[{"entity_id":"sensor.real","state":"21.5"}]`))
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withRefFlag(t, &flagRefExitCode)

	var buf bytes.Buffer
	err := runRefValidate(context.Background(), &buf)
	if err == nil {
		t.Fatal("--exit-code with dangling references must return a verdict error")
	}

	const want = "3 dangling reference(s) to 2 entity(ies)"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the verdict must name what it counted, want %q, got %q", want, err.Error())
	}
	if !strings.Contains(buf.String(), want) {
		t.Errorf("report body missing %q\n%s", want, buf.String())
	}
}

// --- #34: a partial scan is stated where the answer goes ---

// refScanServer stubs GET /v1/ref/scan with a canned body, or fails the request
// with status when body is empty.
func refScanServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ref/scan" {
			t.Errorf("unexpected companion path: %s", r.URL.Path)
		}
		if body == "" {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, `{"error":"companion token rejected"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// scanFixture wires a fake HA holding one dashboard that references
// sensor.dash_only, plus the companion server the caller supplies.
func scanFixture(t *testing.T, companionSrv *httptest.Server, lovelace any) {
	t.Helper()
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          lovelace,
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
}

// TestRunRefScan_AnUnreadConfigHalfIsStatedInTheBody is #34 in plain text. The
// config half is 21 of the 24 references on the reference instance, and its
// failure reached the caller only as a slog.Warn — a stream HACTL_LOG_LEVEL
// routinely hides, and the same channel D-7 had to undo one function at a time.
func TestRunRefScan_AnUnreadConfigHalfIsStatedInTheBody(t *testing.T) {
	companionSrv := refScanServer(t, http.StatusUnauthorized, "")
	scanFixture(t, companionSrv, dashboardConfigWith("sensor.dash_only"))

	var buf bytes.Buffer
	if err := runRefScan(context.Background(), &buf, "sensor.dash_only"); err != nil {
		t.Fatalf("a search command still answers in plain text: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"partial sweep", "config files could not be scanned", "views[0].cards[0].entity"} {
		if !strings.Contains(out, want) {
			t.Errorf("scan output missing %q\n%s", want, out)
		}
	}
}

// TestRunRefScan_PartialRefusesUnderJSONUnlessAllowPartial is the half H-10
// exempted search commands from, and the exemption is what #34 walked through:
// under --json the answer is a bare array a scope note cannot ride on, so on the
// reference instance `ref scan <id> --json` returned 3 of 24 hits at exit 0 with
// no in-band signal at all. When the medium cannot carry the caveat the answer
// refuses, exactly as `ref validate` does — --allow-partial is the acknowledgement.
func TestRunRefScan_PartialRefusesUnderJSONUnlessAllowPartial(t *testing.T) {
	for _, tc := range []struct {
		name        string
		allowPartia bool
		wantRefusal bool
	}{
		{name: "json_refuses", wantRefusal: true},
		{name: "json_with_allow_partial_answers", allowPartia: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			companionSrv := refScanServer(t, http.StatusUnauthorized, "")
			scanFixture(t, companionSrv, dashboardConfigWith("sensor.dash_only"))
			withRefFlag(t, &flagJSON)
			if tc.allowPartia {
				withRefFlag(t, &flagRefAllowPartial)
			}

			var buf bytes.Buffer
			err := runRefScan(context.Background(), &buf, "sensor.dash_only")
			if tc.wantRefusal {
				if err == nil {
					t.Fatalf("a partial scan must refuse under --json; got output:\n%s", buf.String())
				}
				for _, want := range []string{"config files", "--allow-partial"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal missing %q: %v", want, err)
					}
				}
				if buf.String() != "" {
					t.Errorf("a refusal must write no document, got:\n%s", buf.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("--allow-partial must answer: %v", err)
			}
			if !strings.Contains(buf.String(), "views[0].cards[0].entity") {
				t.Errorf("--allow-partial must still report what could be read\n%s", buf.String())
			}
		})
	}
}

// TestRunRefScan_APartialScanNeverClaimsNotReferenced is the sharpest form of
// #34, and it is the one no report contained: on the reference instance
// `ref scan input_boolean.pg_core_flag_a --timeout 2s` printed "not referenced
// as an id in any config file or dashboard" at exit 0 for an id with two config
// references. The message exists to satisfy D-10 — name the contract the query
// tested instead of claiming a verified negative — and a query that never ran
// tested nothing.
func TestRunRefScan_APartialScanNeverClaimsNotReferenced(t *testing.T) {
	companionSrv := refScanServer(t, http.StatusUnauthorized, "")
	scanFixture(t, companionSrv, dashboardConfigWith("sensor.something_else"))
	withRefFlag(t, &flagRefAllowPartial)

	var buf bytes.Buffer
	if err := runRefScan(context.Background(), &buf, "sensor.dash_only"); err != nil {
		t.Fatalf("--allow-partial must answer: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "not referenced") {
		t.Errorf("a scan that could not read the config half must not claim a verified negative:\n%s", out)
	}
	for _, want := range []string{"partial sweep", "config files could not be scanned"} {
		if !strings.Contains(out, want) {
			t.Errorf("the empty answer must name what emptied it, missing %q\n%s", want, out)
		}
	}
}

// TestRunRefScan_SkippedConfigFilesMakeTheAnswerPartial closes the limit D-7
// recorded in writing: the config half is ONE wire call over N files, and the
// companion names the files it could not read in `skipped` (a renamed !include
// target, a file the path guard refuses). hactl has decoded that array since the
// v2026.7.9 spec sync and no command read it — a field decoded by everything and
// consumed by nothing, which is the same shape as documented-by-12-and-decoded-
// by-none one layer along. A 200 response is not a complete answer.
func TestRunRefScan_SkippedConfigFilesMakeTheAnswerPartial(t *testing.T) {
	companionSrv := refScanServer(t, http.StatusOK, `{"target":"sensor.dash_only","hits":[],
		"skipped":[{"location":"packages/heating.yaml","reason":"unreadable"}]}`)
	scanFixture(t, companionSrv, dashboardConfigWith("sensor.dash_only"))

	var buf bytes.Buffer
	if err := runRefScan(context.Background(), &buf, "sensor.dash_only"); err != nil {
		t.Fatalf("a search command still answers in plain text: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"partial sweep", "packages/heating.yaml", "unreadable"} {
		if !strings.Contains(out, want) {
			t.Errorf("a skipped config file must reach the report, missing %q\n%s", want, out)
		}
	}
}

// TestRunRefValidate_SkippedConfigFilesTakeTheGate is the same fact on the
// command whose whole job is to certify: a file the companion could not read
// holds unknown references, so a run that certifies the tree over the rest is
// the vacuous gate D-7 exists to prevent. The 200 makes it look complete, which
// is exactly why it needs the gate rather than the error path.
func TestRunRefValidate_SkippedConfigFilesTakeTheGate(t *testing.T) {
	companionSrv := refEntitiesServer(t, `{"entities":[],
		"skipped":[{"location":"packages/heating.yaml","reason":"unreadable"}]}`)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":    []any{},
		"lovelace/config":             map[string]any{"views": []any{}},
		"config/entity_registry/list": []any{map[string]any{"entity_id": "sensor.real"}},
	}, statesHandler(`[{"entity_id":"sensor.real","state":"21.5"}]`))
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withRefFlag(t, &flagRefExitCode)

	var buf bytes.Buffer
	err := runRefValidate(context.Background(), &buf)
	assertPartialSweepRefused(t, err, buf.String(), "--allow-partial", "config file", "packages/heating.yaml")
}

// TestRefReplaceRefusesWhenTheCompanionSkippedAFile is the write half. A rename
// reported as done while an unread file keeps the old id leaves a dangling
// pointer behind a success message — the companion's own docstring says so, and
// hactl was throwing the array away.
func TestRefReplaceRefusesWhenTheCompanionSkippedAFile(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ref/replace" {
			t.Errorf("unexpected companion path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"dry_run","changes":[
			{"location":"automations.yaml","path":"[0].trigger[0].entity_id","before":"sensor.old","after":"sensor.new"}],
			"skipped":[{"location":"packages/heating.yaml","reason":"unreadable"}]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          map[string]any{"views": []any{}},
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	err := refReplaceWithOptions(context.Background(), &buf, "sensor.old", "sensor.new", false, false)
	if err == nil {
		t.Fatalf("a rename over a config half with an unread file must refuse:\n%s", buf.String())
	}
	for _, want := range []string{"packages/heating.yaml", "--allow-partial"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
}

// TestRunDashGrep_PartialIsStatedAndRefusesUnderJSON is the sibling site. The
// rule is not "ref scan states its scope"; it is "a search command states its
// scope", and `dash grep` reads the same walk through the same warn-only path.
// Fixing one and not the other is the half-fix dev/surfaces exists to catch.
func TestRunDashGrep_PartialIsStatedAndRefusesUnderJSON(t *testing.T) {
	unreadable := wsErrorResponse{Code: "error", Message: "boom"}

	t.Run("plain_text_states_the_scope", func(t *testing.T) {
		ts := startCmdServer(t, map[string]any{
			"lovelace/dashboards/list": []any{},
			"lovelace/config":          unreadable,
		}, nil)
		withFlagDir(t, ts.dir)

		var buf bytes.Buffer
		if err := runDashGrep(context.Background(), &buf, "sensor.x"); err != nil {
			t.Fatalf("a search command still answers in plain text: %v", err)
		}
		if !strings.Contains(buf.String(), "partial sweep") {
			t.Errorf("dash grep must state its scope in the body\n%s", buf.String())
		}
		if strings.Contains(buf.String(), "not referenced") {
			t.Errorf("a partial grep must not claim a verified negative\n%s", buf.String())
		}
	})

	t.Run("json_refuses", func(t *testing.T) {
		ts := startCmdServer(t, map[string]any{
			"lovelace/dashboards/list": []any{},
			"lovelace/config":          unreadable,
		}, nil)
		withFlagDir(t, ts.dir)
		withRefFlag(t, &flagJSON)

		var buf bytes.Buffer
		err := runDashGrep(context.Background(), &buf, "sensor.x")
		if err == nil {
			t.Fatalf("a partial grep must refuse under --json; got:\n%s", buf.String())
		}
		if !strings.Contains(err.Error(), "--allow-partial") {
			t.Errorf("refusal must name the escape hatch: %v", err)
		}
	})
}

// TestAMissingIncludeTargetIsNotAPartialSweep is the boundary the E2E tier found
// the moment this rule shipped: the companion reports a `!include_dir_*` naming a
// directory that is not there as skipped/"missing", and the test instance's own
// `themes: !include_dir_merge_named themes/` made every rename refuse.
//
// Home Assistant globs that directory and yields nothing when it does not exist
// (annotatedyaml `_find_files`, read out of the stable image), so it is zero
// entries to HA and zero references here — a complete answer about a file that
// holds nothing, the D-23 shape one source over. Treating it as partial is the
// cry-wolf direction: that include line is in stock configurations, and
// `ref validate --exit-code` would have refused to certify anything, forever.
func TestAMissingIncludeTargetIsNotAPartialSweep(t *testing.T) {
	companionSrv := refScanServer(t, http.StatusOK, `{"target":"sensor.dash_only",
		"hits":[{"location":"automations.yaml","path":"[0].trigger[0].entity_id","matched_value":"sensor.dash_only"}],
		"skipped":[{"location":"themes","reason":"missing"}]}`)
	scanFixture(t, companionSrv, dashboardConfigWith("sensor.dash_only"))

	var buf bytes.Buffer
	if err := runRefScan(context.Background(), &buf, "sensor.dash_only"); err != nil {
		t.Fatalf("a missing include target must not make the scan partial: %v", err)
	}
	if strings.Contains(buf.String(), "partial") {
		t.Errorf("a target that is not there is not a target that went unread:\n%s", buf.String())
	}
	// And the machine mode must not refuse either, which is where the harm was.
	withRefFlag(t, &flagJSON)
	var doc bytes.Buffer
	if err := runRefScan(context.Background(), &doc, "sensor.dash_only"); err != nil {
		t.Fatalf("--json must answer over a missing include target: %v", err)
	}
}

// TestEntRenameRefusesWhenTheConfigHalfSkippedAFile is the third consumer of
// the same read. `ent rename` already refused on an unscannable dashboard —
// and counted the hits of a config scan that had skipped files as if the count
// were whole, which is the number a caller reads before deciding to --confirm.
func TestEntRenameRefusesWhenTheConfigHalfSkippedAFile(t *testing.T) {
	dir := entRenameStub(t, `{"target":"sensor.old","hits":[{"location":"automations.yaml",
		"path":"[0].trigger[0].entity_id","matched_value":"sensor.old"}],
		"skipped":[{"location":"packages/heating.yaml","reason":"unreadable"}]}`)
	withFlagDir(t, dir)

	var buf bytes.Buffer
	err := runEntRename(context.Background(), &buf, "sensor.old", "sensor.new")
	if err == nil {
		t.Fatalf("a rename over a config half with an unread file must refuse:\n%s", buf.String())
	}
	for _, want := range []string{"config file(s)", "packages/heating.yaml", "--allow-partial", "nothing was renamed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
}

// --- #37: the matcher's own precondition, enforced where the target enters ---

// TestRefScanRefusesADegenerateTargetAsAnErrorNotAPartialAnswer keeps the two
// refusals apart: a target the matcher cannot honour is the caller's mistake and
// ends the command, and must never be absorbed into "the config half could not
// be read, here are the dashboard hits" — which is what the partial path would
// do with any other companion-side error. The rule itself lives at the client
// and is proven there (TestRefRoutesRefuseATargetThatIsNotAWholeToken).
func TestRefScanRefusesADegenerateTargetAsAnErrorNotAPartialAnswer(t *testing.T) {
	companionSrv := refScanServer(t, http.StatusOK, `{"target":".","hits":[]}`)
	scanFixture(t, companionSrv, dashboardConfigWith("sensor.dash_only"))

	var buf bytes.Buffer
	err := runRefScan(context.Background(), &buf, ".")
	if err == nil {
		t.Fatalf("a degenerate target must end the command, got:\n%s", buf.String())
	}
	if !errors.As(err, new(*companion.InvalidRefTargetError)) {
		t.Errorf("a bad target is the caller's mistake, not a partial sweep: %v", err)
	}
	if buf.String() != "" {
		t.Errorf("nothing may be printed for a refused target, got:\n%s", buf.String())
	}
}
