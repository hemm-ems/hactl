package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// ============================================================================
// The silent half of the /api/states defect (SPEC §2a, acceptance criterion 7).
//
// `resolveAutomation` used to discard fetchAutomations' error and return
// (automationEntity{}, false) — its own doc comment stated the conflation: "if
// no live automation matches OR the states fetch fails". On an instance whose
// /api/states cannot be read, every automation reference therefore resolved as
// UNKNOWN, with no error shown anywhere: `auto show` fell back to the
// "automation." + id guess and 404'd as if the caller had typed a bad name,
// `auto delete` forwarded the raw object id to the companion (the exact H-17
// failure), `auto cat`/`auto diff`/`auto apply`/`rollback` skipped the config-id
// path, and `trace show` passed the reference through unrewritten so HA's own
// error read as a caller typo.
//
// That is H-7 exactly: an unavailable source rendering as a confident negative
// answer. It stays necessary after the H-21 ordering fix — the fetch can still
// fail on network, on auth, or on a genuinely degenerate payload, and none of
// those may read as "no such automation".
// ============================================================================

// brokenStatesRig stands up one server playing fake HA and fake companion in
// which /api/states cannot be decoded, while the automation the caller names
// genuinely exists on the companion side. Any command that reports "unknown
// reference" here is reporting a fact it does not have.
func brokenStatesRig(t *testing.T) string {
	t.Helper()
	const configID = "1712345678901"

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		// The states fetch fails. Which way it fails is not the point — this
		// stands in for a network error, an expired token, or an /api/states
		// hactl can no longer decode.
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"message":"not a states array"}`)
		},
		"/v1/config/automation": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("id") != configID {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"error":"automation not found"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":%q,"content":"alias: Climate Schedule\n"}`, configID)
		},
	})
	env := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=test-token\nCOMPANION_URL=%s\n", ts.srv.URL, ts.srv.URL)
	if err := os.WriteFile(filepath.Join(ts.dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, ts.dir)
	return ts.dir
}

// TestAutomationReferenceCommandsReportAFailedStatesFetch is acceptance
// criterion 7, over every command that resolves an automation reference.
//
// The reference used, `climate_schedule`, is the entity object id — the string
// `auto ls` prints in its `id` column, which is exactly the form that NEEDS
// resolution (D-1, H-17). With /api/states unreadable the resolution cannot
// happen, and the only honest answer is to say so.
func TestAutomationReferenceCommandsReportAFailedStatesFetch(t *testing.T) {
	const ref = "climate_schedule"

	// Every flag these entrypoints read is pinned once, so each case below is
	// just the command under test.
	withAutoFile(t, "alias: Climate Schedule\n")
	withAutoConfirm(t, false)
	withRollbackConfirm(t, false)
	withTraceFlags(t)

	cases := map[string]func(ctx context.Context, w *bytes.Buffer) error{
		"auto cat":   func(ctx context.Context, w *bytes.Buffer) error { return runAutoCat(ctx, w, ref) },
		"auto show":  func(ctx context.Context, w *bytes.Buffer) error { return runAutoShow(ctx, w, ref) },
		"auto diff":  func(ctx context.Context, w *bytes.Buffer) error { return runAutoDiff(ctx, w, ref) },
		"auto apply": func(ctx context.Context, w *bytes.Buffer) error { return runAutoApply(ctx, w, ref) },
		"auto delete": func(ctx context.Context, w *bytes.Buffer) error {
			return runAutoDelete(ctx, w, ref)
		},
		"rollback": func(ctx context.Context, w *bytes.Buffer) error { return runRollback(ctx, w, ref) },
		"trace show": func(ctx context.Context, w *bytes.Buffer) error {
			return runTraceShow(ctx, w, "automation."+ref+"/01J0RUN")
		},
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			brokenStatesRig(t)
			var buf bytes.Buffer
			err := run(context.Background(), &buf)
			if err == nil {
				t.Fatalf("%s succeeded with an unreadable /api/states — output:\n%s\n"+
					"the automation reference could not be resolved, so whatever this printed is "+
					"an answer about an instance hactl could not read", name, buf.String())
			}
			if !strings.Contains(err.Error(), "parsing states") {
				t.Errorf("%s reported %v\nwant an error naming the states fetch. Reporting anything "+
					"else here conflates \"this reference matches no automation\" with \"hactl could "+
					"not read the instance\" — an unavailable source rendered as a confident negative "+
					"answer (H-7, SPEC §2a).", name, err)
			}
		})
	}
}

// TestResolveAutomationDistinguishesNoMatchFromNoAnswer pins the resolver's own
// contract, which is what every call site above depends on: a readable instance
// with no matching automation is (false, nil); an unreadable one is an error.
// Collapsing the two is the defect.
func TestResolveAutomationDistinguishesNoMatchFromNoAnswer(t *testing.T) {
	t.Run("readable instance, no match", func(t *testing.T) {
		srv := statesOnlyServer(t, []byte(`[{"entity_id":"automation.other","state":"on","attributes":{"id":"1"}}]`))
		_, ok, err := resolveAutomation(context.Background(), haapi.New(srv, "tok"), "climate_schedule")
		if err != nil {
			t.Fatalf("resolveAutomation errored on a perfectly readable payload: %v", err)
		}
		if ok {
			t.Error("resolveAutomation matched an automation that is not in the payload")
		}
	})

	t.Run("unreadable instance", func(t *testing.T) {
		srv := statesOnlyServer(t, []byte(`{"message":"not a states array"}`))
		_, ok, err := resolveAutomation(context.Background(), haapi.New(srv, "tok"), "climate_schedule")
		if err == nil {
			t.Fatal("resolveAutomation swallowed the states failure — the caller cannot tell " +
				"\"no automation matches\" from \"hactl could not read the instance\"")
		}
		if ok {
			t.Error("resolveAutomation reported a match alongside an error")
		}
	})
}

// withAutoFile writes a temp automation YAML and points --file at it.
func withAutoFile(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auto.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := flagAutoFile
	flagAutoFile = path
	t.Cleanup(func() { flagAutoFile = old })
}

func withAutoConfirm(t *testing.T, v bool) {
	t.Helper()
	old := flagAutoConfirm
	flagAutoConfirm = v
	t.Cleanup(func() { flagAutoConfirm = old })
}

func withRollbackConfirm(t *testing.T, v bool) {
	t.Helper()
	old := flagRollbackConfirm
	flagRollbackConfirm = v
	t.Cleanup(func() { flagRollbackConfirm = old })
}

func withTraceFlags(t *testing.T) {
	t.Helper()
	oldFull, oldJSON := flagFull, flagJSON
	flagFull, flagJSON = false, false
	t.Cleanup(func() { flagFull, flagJSON = oldFull, oldJSON })
}
