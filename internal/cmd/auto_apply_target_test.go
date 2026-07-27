package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// autoApplyRig stands up a fake HA holding one automation whose config id and
// entity object id differ, the way every UI-authored automation is: HA mints a
// millisecond timestamp for `id:` and derives the entity_id from the alias.
func autoApplyRig(t *testing.T) (localFile string) {
	t.Helper()
	const configID = "1712345678901"
	remoteJSON := `{"id":"1712345678901","alias":"Climate Schedule","trigger":[],"condition":[],"action":[]}`
	states := `[{"entity_id":"automation.climate_schedule","state":"on","attributes":{"id":"1712345678901","friendly_name":"Climate Schedule"}}]`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/automation/config/" + configID: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, remoteJSON)
		},
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, states)
		},
	})
	withFlagDir(t, ts.dir)

	localFile = filepath.Join(ts.dir, "new.yaml")
	if err := os.WriteFile(localFile, []byte("alias: Climate Schedule\ntrigger: []\ncondition: []\naction:\n  - delay: '00:00:05'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return localFile
}

func withAutoApplyFlags(t *testing.T, file string) {
	t.Helper()
	oldFile, oldConfirm, oldJSON := flagAutoFile, flagAutoConfirm, flagJSON
	flagAutoFile, flagAutoConfirm, flagJSON = file, false, false
	t.Cleanup(func() { flagAutoFile, flagAutoConfirm, flagJSON = oldFile, oldConfirm, oldJSON })
}

// TestAutoApplyDryRunRefusesUnresolvableTarget is issue #94's first defect.
//
// The remote fetch was the only call that touched the target, and its 404 went
// to slog.Warn. On the dry-run path writer.Apply returns before the backup
// fetch, so nothing downstream could fail: a fabricated id produced
// "validation: ok (HA validate_config)" and an invitation to --confirm, at exit
// 0. POST to that endpoint is create-or-update, so an agent that believed the
// plan would have created a new automation instead of updating one.
func TestAutoApplyDryRunRefusesUnresolvableTarget(t *testing.T) {
	localFile := autoApplyRig(t)
	withAutoApplyFlags(t, localFile)

	var buf bytes.Buffer
	err := runAutoApply(context.Background(), &buf, "totally_bogus_automation_xyz")
	if err == nil {
		t.Fatalf("dry-run planned an apply against an id that names no automation; output:\n%s", buf.String())
	}
	if out := buf.String(); strings.Contains(out, "use --confirm") || strings.Contains(out, "validation: ok") {
		t.Errorf("a refused preview must print no plan and no validation verdict, got:\n%s", out)
	}
}

// TestAutoDiffAndApplyAcceptTheIdentifierAutoLsPrints is issue #94's second
// defect (H-17). `auto ls` prints the entity object id in a column headed `id`;
// `show` and `cat` accepted it while `diff` and `apply` 404'd on it.
func TestAutoDiffAndApplyAcceptTheIdentifierAutoLsPrints(t *testing.T) {
	localFile := autoApplyRig(t)

	// Every identifier hactl prints for this automation.
	for _, ref := range []string{
		"1712345678901",               // config id — `auto cat`/`auto show`
		"climate_schedule",            // entity object id — the `id` column of `auto ls`
		"automation.climate_schedule", // full entity_id — `auto show`
		"Climate Schedule",            // alias — friendly_name
	} {
		t.Run(ref, func(t *testing.T) {
			withAutoApplyFlags(t, localFile)

			var diffBuf bytes.Buffer
			if err := runAutoDiff(context.Background(), &diffBuf, ref); err != nil {
				t.Errorf("auto diff %q: %v", ref, err)
			}
			var applyBuf bytes.Buffer
			if err := runAutoApply(context.Background(), &applyBuf, ref); err != nil {
				t.Errorf("auto apply %q: %v", ref, err)
			}
			if out := applyBuf.String(); !strings.Contains(out, "dry-run") {
				t.Errorf("auto apply %q previewed nothing: %q", ref, out)
			}
		})
	}
}

// TestAutoApplyPreviewIsMachineReadable — H-2's second half. The preview used
// to Fprintf "validation: ok" to the same writer ahead of the JSON object, so
// stdout did not parse on a successful command.
func TestAutoApplyPreviewIsMachineReadable(t *testing.T) {
	localFile := autoApplyRig(t)
	withAutoApplyFlags(t, localFile)
	flagJSON = true

	var buf bytes.Buffer
	if err := runAutoApply(context.Background(), &buf, "climate_schedule"); err != nil {
		t.Fatalf("runAutoApply: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("--json output does not parse: %v\n%s", err, buf.String())
	}
	if got["dry_run"] != true {
		t.Errorf("preview object must state dry_run:true, got %v", got["dry_run"])
	}
}
