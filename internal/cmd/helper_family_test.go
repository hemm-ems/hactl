package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The helper family on a normally-configured instance, from the 2026-07-30
// live-fire run:
//
//   - every helper was storage-backed (created in the HA UI), `helper ls`
//     listed all 220 of them, and `helper show`/`helper cat` answered 404 for
//     every single one;
//   - `input_boolean:` was written inline in configuration.yaml, so every
//     `helper create --confirm` was a structural 400 — while every dry run
//     printed "would create", 8 domains out of 8.
//
// The first is fixed in the companion (its lookup now resolves storage
// helpers); what lands here is the CLI's half: surfacing the source, refusing a
// delete the confirmed run cannot perform, and asking the layout question
// before promising a create.

// helperFamilyEnv wires the CLI to a companion stub whose routes the caller
// supplies, and to an HA stub that serves an empty state list.
func helperFamilyEnv(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) string {
	t.Helper()
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := routes[r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.Error(w, `{"error":{"code":404,"message":"not found"}}`, http.StatusNotFound)
	}))
	t.Cleanup(companionSrv.Close)
	haSrv := httptest.NewServer(helperStatesHandler(`[]`))
	t.Cleanup(haSrv.Close)

	dir := t.TempDir()
	env := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func jsonRoute(body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

const storageHelperBody = `{"id":"input_boolean.anwesenheit_flur","domain":"input_boolean",` +
	`"content":"# source: storage — created in the Home Assistant UI, not in a YAML file.\n` +
	`anwesenheit_flur:\n  name: Anwesenheit Flur\n","source":"storage"}`

const yamlHelperBody = `{"id":"guest_mode","domain":"input_boolean",` +
	`"content":"guest_mode:\n  name: Guest Mode\n","source":"yaml"}`

// TestHelperShowStatesWhichSourceItRead: `helper ls` distinguishes yaml from
// storage in a column, and until now `helper show` on the same helper said
// nothing — so the one command that prints a helper's definition gave no hint
// that create/set/delete cannot touch it.
func TestHelperShowStatesWhichSourceItRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"storage", storageHelperBody, "source: storage"},
		{"yaml", yamlHelperBody, "source: yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := helperFamilyEnv(t, map[string]func(http.ResponseWriter, *http.Request){
				"/v1/config/helper": jsonRoute(tc.body),
			})
			withFlagDir(t, dir)

			var buf bytes.Buffer
			if err := runHelperShow(context.Background(), &buf, "anwesenheit_flur"); err != nil {
				t.Fatalf("helper show: %v", err)
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("helper show output does not state the source (%q):\n%s", tc.want, buf.String())
			}
		})
	}
}

// TestHelperShowJSONCarriesTheSource: an agent asking for JSON must be able to
// tell an editable helper from a read-only one without parsing prose.
func TestHelperShowJSONCarriesTheSource(t *testing.T) {
	dir := helperFamilyEnv(t, map[string]func(http.ResponseWriter, *http.Request){
		"/v1/config/helper": jsonRoute(storageHelperBody),
	})
	withFlagDir(t, dir)
	old := flagJSON
	flagJSON = true
	defer func() { flagJSON = old }()

	var buf bytes.Buffer
	if err := runHelperShow(context.Background(), &buf, "input_boolean.anwesenheit_flur"); err != nil {
		t.Fatalf("helper show --json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("helper show --json did not parse: %v\n%s", err, buf.String())
	}
	if got["source"] != "storage" {
		t.Errorf("--json dropped the source: %v", got)
	}
}

// TestHelperCatPassesTheStorageMarkerThrough: `cat` is verbatim by contract, so
// the marker has to come from the companion inside the content — and `cat` must
// not swallow it.
func TestHelperCatPassesTheStorageMarkerThrough(t *testing.T) {
	dir := helperFamilyEnv(t, map[string]func(http.ResponseWriter, *http.Request){
		"/v1/config/helper": jsonRoute(storageHelperBody),
	})
	withFlagDir(t, dir)

	var buf bytes.Buffer
	if err := runHelperCat(context.Background(), &buf, "input_boolean.anwesenheit_flur"); err != nil {
		t.Fatalf("helper cat: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "# source: storage") {
		t.Errorf("helper cat lost the read-only marker:\n%s", buf.String())
	}
}

// TestHelperDeleteRefusesAStorageHelperInBothModes is the half a fix like this
// forgets. `helper delete` resolves its target through GET before printing a
// plan, and GET now *finds* storage helpers — so a preview that only checked
// "did the lookup succeed" would print a confident plan for a delete whose
// --confirm is a 409. H-2 asserted as equality: preview and confirm agree.
func TestHelperDeleteRefusesAStorageHelperInBothModes(t *testing.T) {
	dir := helperFamilyEnv(t, map[string]func(http.ResponseWriter, *http.Request){
		"/v1/config/helper": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":{"code":409,"message":"Helper is storage-backed"}}`)
				return
			}
			jsonRoute(storageHelperBody)(w, r)
		},
	})
	withFlagDir(t, dir)

	verdicts := map[bool]error{}
	for _, confirm := range []bool{false, true} {
		old := flagHelperConfirm
		flagHelperConfirm = confirm
		var buf bytes.Buffer
		verdicts[confirm] = runHelperDelete(context.Background(), &buf, "input_boolean.anwesenheit_flur")
		flagHelperConfirm = old
	}

	if (verdicts[false] == nil) != (verdicts[true] == nil) {
		t.Fatalf("preview and confirm disagree: dry-run err=%v, --confirm err=%v", verdicts[false], verdicts[true])
	}
	if verdicts[false] == nil {
		t.Fatal("dry run planned a delete of a storage-backed helper that cannot be deleted")
	}
	if !strings.Contains(verdicts[false].Error(), "storage") {
		t.Errorf("refusal does not name the reason: %v", verdicts[false])
	}
}

// TestHelperCreatePreviewAgreesWithConfirmOnEveryLayout is the defect itself,
// asserted as an equality rather than as two hand-written expectations: for the
// same instance, the dry run must fail exactly when --confirm does.
func TestHelperCreatePreviewAgreesWithConfirmOnEveryLayout(t *testing.T) {
	const refusal = "Home Assistant reads input_boolean config, but not from a file this route can extend: " +
		"'input_boolean:' is defined inline, and appending to an inline mapping is not safe."

	for _, tc := range []struct {
		name    string
		verdict string
		create  func(w http.ResponseWriter, r *http.Request)
	}{
		{
			name:    "included",
			verdict: `{"domain":"input_boolean","wired":true,"file":"input_boolean.yaml"}`,
			create: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"status":"created","id":"probe","entity_id":"input_boolean.probe",`+
					`"reloaded":true,"entity_created":true}`)
			},
		},
		{
			name:    "inline",
			verdict: fmt.Sprintf(`{"domain":"input_boolean","wired":false,"reason":%q}`, refusal),
			create: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": 400, "message": refusal}})
				_, _ = w.Write(body)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := helperFamilyEnv(t, map[string]func(http.ResponseWriter, *http.Request){
				"/v1/config/wiring": jsonRoute(tc.verdict),
				"/v1/config/helper": tc.create,
			})
			withFlagDir(t, dir)

			file := filepath.Join(dir, "probe.yaml")
			if err := os.WriteFile(file, []byte("probe:\n  name: Probe\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			oldFile := flagHelperFile
			flagHelperFile = file
			defer func() { flagHelperFile = oldFile }()

			verdicts := map[bool]error{}
			for _, confirm := range []bool{false, true} {
				old := flagHelperConfirm
				flagHelperConfirm = confirm
				var buf bytes.Buffer
				verdicts[confirm] = runHelperCreate(context.Background(), &buf, "input_boolean")
				flagHelperConfirm = old
			}

			if (verdicts[false] == nil) != (verdicts[true] == nil) {
				t.Fatalf("preview and confirm disagree on the %s layout: dry-run err=%v, --confirm err=%v",
					tc.name, verdicts[false], verdicts[true])
			}
			if verdicts[false] == nil {
				return
			}
			// Same verdict is not enough: a preview that refuses for its own
			// reason sends the operator somewhere else than the run it predicts.
			if !strings.Contains(verdicts[false].Error(), refusal) {
				t.Errorf("preview refused with a different explanation than the companion's:\n%v", verdicts[false])
			}
		})
	}
}
