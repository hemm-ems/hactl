//go:build livefire

package livefire

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// WP7 — the ref family's partial-scan honesty, plus the two classes the
// reproductions turned out to belong to: what an error may do to an answer the
// command already rendered, and what a word-bounded matcher may be asked.
//
// Findings #34 #36 #37. Read-only on both profiles: every case here either
// reads, refuses before contacting anything, or runs a dry run.

// TestSweepRefScanRefusesATargetItCannotMatch — finding #37.
//
// `ref scan .` returned 2747 config hits on the reference instance, and
// `ref replace . X` planned 2747 rewrites of real config files, because the
// companion matches `\b` + target + `\b` and a target that starts and ends on a
// non-word character makes those boundaries bind to the neighbouring text.
//
// The refusal happens before hactl connects to anything, which is why this case
// runs on BOTH profiles even though the ref family is companion-routed: a rig
// with no companion refuses these targets for the right reason, and the control
// below proves it — a well-formed target gets past the check and fails, if it
// fails, for a different one.
func TestSweepRefScanRefusesATargetItCannotMatch(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, target := range []string{".", "-", "..", "'sensor.x'", ".turn_on"} {
			t.Run("scan_"+target, func(t *testing.T) {
				out, err := tgt.Read(t, "ref", "scan", target, "--tokensmax", "0")
				if err == nil {
					t.Fatalf("`ref scan %q` must refuse; it answered:\n%s", target, truncate(out))
				}
				if strings.TrimSpace(out) != "" {
					t.Errorf("a refused target may print nothing, got:\n%s", truncate(out))
				}
				stderr, _ := tgt.ReadDiagnostic(t, "ref", "scan", target)
				if !strings.Contains(stderr, "whole token") {
					t.Errorf("`ref scan %q` must say why it refused:\n%s", target, truncate(stderr))
				}
			})
			t.Run("replace_"+target, func(t *testing.T) {
				// Dry run: the P1-shaped half. Nothing is written on either
				// profile, and with the fix nothing is even contacted.
				out, err := tgt.Read(t, "ref", "replace", target, "pg_w7_never_written", "--tokensmax", "0")
				if err == nil {
					t.Fatalf("`ref replace %q` must refuse; it planned:\n%s", target, truncate(out))
				}
				if strings.Contains(out, "would rename") {
					t.Errorf("a refused target may not become a plan:\n%s", truncate(out))
				}
			})
		}
		// The control: a target that IS a whole token gets past the check. On a
		// profile with no companion it still fails — at the transport, which is
		// a different message and the point of the assertion.
		stderr, err := tgt.ReadDiagnostic(t, "ref", "scan", "sensor.sweep_series")
		if err != nil && strings.Contains(stderr, "whole token") {
			t.Errorf("a well-formed target must not be refused by the shape check:\n%s", truncate(stderr))
		}
	})
}

// TestSweepRefValidateExitCodeAnswersOnStdout — finding #36, and the defect
// under it that no report contained.
//
// `ref validate --exit-code` renders the dangling-reference table and then
// returns a sentinel error carrying the exit code. Execute() buffered cobra's
// writer and flushed it only on the success path, so on the reference instance
// the command printed ZERO bytes of stdout and one line of stderr — and that
// line said "318 dangling reference(s) found" for what the report body calls
// 429 references to 318 entities. The whole answer was the mislabeled summary.
//
// Both halves are asserted here because they are one output: the report reaches
// stdout, and the numbers in the two places are the same sentence.
func TestSweepRefValidateExitCodeAnswersOnStdout(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		requireCompanion(t, tgt)

		body := tgt.MustRead(t, "ref", "validate", "--tokensmax", "0")
		refs, entities, ok := parseDanglingSummary(body)
		if !ok {
			// A clean tree is a legitimate answer, and it has its own contract.
			if !strings.Contains(body, "no dangling references found") {
				t.Fatalf("`ref validate` said neither a summary nor a clean bill:\n%s", truncate(body))
			}
			out, err := tgt.Read(t, "ref", "validate", "--exit-code", "--tokensmax", "0")
			if err != nil {
				t.Fatalf("--exit-code on a clean tree must exit 0: %v\n%s", err, truncate(out))
			}
			return
		}

		out, err := tgt.Read(t, "ref", "validate", "--exit-code", "--tokensmax", "0")
		if code := ExitCode(err); code != 1 {
			t.Fatalf("--exit-code with %d dangling reference(s) must exit 1, got %d", refs, code)
		}
		if strings.TrimSpace(out) == "" {
			t.Fatalf("--exit-code printed no answer at all: the report it rendered never reached stdout")
		}
		gotRefs, gotEntities, ok := parseDanglingSummary(out)
		if !ok {
			t.Fatalf("--exit-code's report carries no summary line:\n%s", truncate(out))
		}
		if gotRefs != refs || gotEntities != entities {
			t.Errorf("--exit-code reports %d reference(s) to %d entity(ies); the plain run reports %d to %d",
				gotRefs, gotEntities, refs, entities)
		}
		// The verdict on stderr is the same sentence, so a CI log and a report
		// cannot disagree about which number is which (H-11).
		stderr, _ := tgt.ReadDiagnostic(t, "ref", "validate", "--exit-code", "--tokensmax", "0")
		if !strings.Contains(stderr, danglingSummaryText(refs, entities)) {
			t.Errorf("the one-line verdict must name what it counted (%q):\n%s",
				danglingSummaryText(refs, entities), truncate(stderr))
		}
	})
}

// TestSweepAPartialScanSaysSoOrRefuses — finding #34, on both profiles.
//
// The condition is produced rather than waited for: the case points hactl at a
// companion that answers 500 while HA stays real, which is exactly the shape a
// slow or restarting add-on produces (on the reference instance a 2-second
// timeout was enough). What the answer may not be is a short list at exit 0.
//
// It runs on the rig too, and deliberately: the rig has no companion (R11), but
// a companion that FAILS is the thing under test, and a stub can fail without
// being one. The dashboards half is real on both profiles.
func TestSweepAPartialScanSaysSoOrRefuses(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		broken := withStubCompanion(t, tgt, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":"stub companion refused"}`)
		})

		// Plain text: the answer stands and the scope is in the body, where a
		// person reads it — not on the stderr stream HACTL_LOG_LEVEL hides.
		out := broken.MustRead(t, "ref", "scan", "sensor.sweep_series", "--tokensmax", "0")
		if !strings.Contains(out, "partial sweep") {
			t.Errorf("a scan that could not read the config half must say so in the body:\n%s", truncate(out))
		}
		if strings.Contains(out, "not referenced as an id") {
			t.Errorf("a partial scan may not claim a verified negative:\n%s", truncate(out))
		}

		// --json: the document is a bare array with nowhere to say it is short,
		// so it refuses and prints nothing.
		doc, err := broken.Read(t, "ref", "scan", "sensor.sweep_series", "--json", "--tokensmax", "0")
		if err == nil {
			t.Errorf("a partial scan must refuse under --json, got:\n%s", truncate(doc))
		}
		if strings.TrimSpace(doc) != "" {
			t.Errorf("a refusal may not emit a document, got:\n%s", truncate(doc))
		}
		// --allow-partial is the acknowledgement, and then the array is legal.
		doc = broken.MustRead(t, "ref", "scan", "sensor.sweep_series", "--json", "--allow-partial", "--tokensmax", "0")
		if !strings.HasPrefix(strings.TrimSpace(doc), "[") {
			t.Errorf("--allow-partial must answer with the array, got:\n%s", truncate(doc))
		}
	})
}

// TestSweepAScanThatSkippedFilesIsPartialToo is the same law for the shape that
// looks complete: HTTP 200, hits, and a `skipped` list naming files the walk
// could not read. hactl decoded that field on three response types and no
// command read it — the limit D-7 recorded in writing and this closes.
//
// The second half is the boundary, and it is here rather than only in the unit
// tier because it is what a stock Home Assistant config produces: a `!include_dir_*`
// naming a directory that is not there is skipped as "missing", HA's own loader
// globs it and yields nothing, and treating that as partial made every rename on
// the E2E instance refuse. A file that is not there holds no references.
func TestSweepAScanThatSkippedFilesIsPartialToo(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, tc := range []struct {
			name        string
			reason      string
			wantPartial bool
		}{
			{name: "unreadable_is_partial", reason: "unreadable", wantPartial: true},
			{name: "missing_is_a_complete_zero", reason: "missing"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cc := withStubCompanion(t, tgt, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"target":"sensor.sweep_series","hits":[],
						"skipped":[{"location":"packages/heating.yaml","reason":%q}]}`, tc.reason)
				})

				out := cc.MustRead(t, "ref", "scan", "sensor.sweep_series", "--tokensmax", "0")
				_, jsonErr := cc.Read(t, "ref", "scan", "sensor.sweep_series", "--json", "--tokensmax", "0")

				if !tc.wantPartial {
					if strings.Contains(out, "partial") {
						t.Errorf("a target that is not there is not a target that went unread:\n%s", truncate(out))
					}
					if jsonErr != nil {
						t.Errorf("--json must answer over a missing include target: %v", jsonErr)
					}
					return
				}
				for _, want := range []string{"partial sweep", "packages/heating.yaml"} {
					if !strings.Contains(out, want) {
						t.Errorf("a 200 that skipped a file is a partial answer; missing %q:\n%s", want, truncate(out))
					}
				}
				if jsonErr == nil {
					t.Error("a scan whose config walk skipped a file must refuse under --json")
				}
			})
		}
	})
}

// withStubCompanion returns a Target pointed at the profile's own Home
// Assistant and at a companion this case controls.
//
// The .env is copied rather than edited in place: the live profile's directory
// is somebody's real instance, and a case that rewrote it would be a write to
// the one thing this tier promises not to touch.
func withStubCompanion(t *testing.T, tgt Target, handler http.HandlerFunc) Target {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	env, err := os.ReadFile(filepath.Join(tgt.Dir, ".env"))
	if err != nil {
		t.Fatalf("reading %s/.env: %v", tgt.Dir, err)
	}
	kept := make([]string, 0, strings.Count(string(env), "\n")+3)
	for line := range strings.SplitSeq(string(env), "\n") {
		key, _, _ := strings.Cut(line, "=")
		switch strings.TrimSpace(key) {
		case "COMPANION_URL", "COMPANION_TOKEN", "":
			continue
		}
		kept = append(kept, line)
	}
	kept = append(kept, "COMPANION_URL="+srv.URL, "COMPANION_TOKEN=stub", "")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(strings.Join(kept, "\n")), 0o600); err != nil {
		t.Fatalf("writing stub .env: %v", err)
	}
	return Target{Profile: tgt.Profile, Dir: dir, Bin: tgt.Bin}
}

// danglingSummaryRe reads the summary both outputs of `ref validate` share.
var danglingSummaryRe = regexp.MustCompile(`(\d+) dangling reference\(s\) to (\d+) entity\(ies\)`)

func parseDanglingSummary(s string) (refs, entities int, ok bool) {
	m := danglingSummaryRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	refs, _ = strconv.Atoi(m[1])
	entities, _ = strconv.Atoi(m[2])
	return refs, entities, true
}

func danglingSummaryText(refs, entities int) string {
	return fmt.Sprintf("%d dangling reference(s) to %d entity(ies)", refs, entities)
}
