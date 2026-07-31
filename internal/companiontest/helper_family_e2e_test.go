//go:build companion

package companiontest

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ============================================================================
// The helper family end to end, against a real HA and a real companion.
//
// Two defects from the 2026-07-30 live-fire run, neither reachable from a
// stock test instance:
//
//   - every helper on a UI-managed instance is storage-backed, and
//     `helper show`/`helper cat` answered 404 for all 220 of them while
//     `helper ls` listed them;
//   - `input_boolean:` was written inline in configuration.yaml, so every
//     `helper create --confirm` was a structural 400 — and every dry run
//     printed "would create" anyway, 8 domains out of 8.
//
// Both are asserted here through the CLI binary, because that is the only
// surface a user has. The parity halves are written as *equalities* between
// the dry run's verdict and `--confirm`'s: two hand-written expectations can
// drift apart one release later, an equality cannot.
// ============================================================================

// haWSCommand runs one command against HA's WebSocket API and returns its
// result. Collections (`input_boolean/create` and friends) have no REST form,
// so this is the only way to create a helper the way HA's own UI does — which
// is the entire point: the fixture must come from HA, not from us.
func haWSCommand(t *testing.T, msgType string, payload map[string]any) json.RawMessage {
	t.Helper()
	u, err := url.Parse(haURL)
	if err != nil {
		t.Fatalf("parsing HA URL: %v", err)
	}
	u.Scheme = "ws"
	u.Path = "/api/websocket"

	// Not t.Context(): this also runs from t.Cleanup, where that context is
	// already cancelled — the WebSocket delete would then fail as "operation
	// was canceled" and fail the test after its body had passed.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil) //nolint:bodyclose
	if err != nil {
		t.Fatalf("ws connect: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("reading auth_required: %v", err)
	}
	if err := conn.WriteJSON(map[string]string{"type": "auth", "access_token": haToken}); err != nil {
		t.Fatalf("sending auth: %v", err)
	}
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("reading auth_ok: %v", err)
	}
	if msg["type"] != "auth_ok" {
		t.Fatalf("expected auth_ok, got %v", msg["type"])
	}

	cmd := map[string]any{"id": 1, "type": msgType}
	maps.Copy(cmd, payload)
	if err := conn.WriteJSON(cmd); err != nil {
		t.Fatalf("sending %s: %v", msgType, err)
	}
	var resp struct {
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
		Success bool            `json:"success"`
	}
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("reading %s response: %v", msgType, err)
	}
	if !resp.Success {
		t.Fatalf("%s failed: %s", msgType, resp.Error)
	}
	return resp.Result
}

// createUIHelper creates a helper the way HA's UI does and returns its
// collection id, registering the WebSocket delete as cleanup.
func createUIHelper(t *testing.T, domain string, payload map[string]any) string {
	t.Helper()
	var item struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(haWSCommand(t, domain+"/create", payload), &item); err != nil {
		t.Fatalf("decoding %s/create result: %v", domain, err)
	}
	if item.ID == "" {
		t.Fatalf("%s/create returned no id", domain)
	}
	t.Cleanup(func() {
		haWSCommand(t, domain+"/delete", map[string]any{domain + "_id": item.ID})
	})
	return item.ID
}

// waitForHelperShow polls `hactl helper show` until it succeeds. Home Assistant
// persists a collection change to `.storage` on a delay, so a helper created a
// moment ago is genuinely not on disk yet — the wait is for HA, not for the
// lookup.
func waitForHelperShow(t *testing.T, entityID string) string {
	t.Helper()
	var out string
	var err error
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if out, err = runHactlE2E(t, "helper", "show", entityID); err == nil {
			return out
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("helper show %s never succeeded (last err %v):\n%s", entityID, err, out)
	return ""
}

// TestE2EHelperShowReadsAUICreatedHelperCLI: a helper made the normal way must
// be readable by the command whose whole job is to read helpers.
func TestE2EHelperShowReadsAUICreatedHelperCLI(t *testing.T) {
	id := createUIHelper(t, "input_boolean", map[string]any{
		"name": "E2E UI Helper",
		"icon": "mdi:toggle-switch",
	})
	entityID := "input_boolean." + id

	out := waitForHelperShow(t, entityID)
	for _, want := range []string{"source: storage", "domain: input_boolean", "E2E UI Helper"} {
		if !strings.Contains(out, want) {
			t.Errorf("helper show does not report %q:\n%s", want, out)
		}
	}

	// `ls` and `show` must agree about the same helper: an identifier hactl
	// prints is an identifier hactl accepts (H-17).
	listing, err := runHactlE2E(t, "helper", "ls", "--pattern", id, "--tokensmax", "0")
	if err != nil {
		t.Fatalf("helper ls failed (exit %v):\n%s", err, listing)
	}
	if !strings.Contains(listing, entityID) || !strings.Contains(listing, "storage") {
		t.Errorf("helper ls does not list %s as storage-backed:\n%s", entityID, listing)
	}

	cat, err := runHactlE2E(t, "helper", "cat", entityID)
	if err != nil {
		t.Fatalf("helper cat failed (exit %v):\n%s", err, cat)
	}
	if !strings.HasPrefix(cat, "# source: storage") {
		t.Errorf("helper cat lost the read-only marker:\n%s", cat)
	}
}

// TestE2EHelperDeleteAgreesWithItselfOnAUIHelperCLI: GET now resolves storage
// helpers, so "the lookup succeeded" no longer means "the delete can happen".
// Preview and confirm must reach the same verdict — this is the direction a fix
// to the read path can silently break.
func TestE2EHelperDeleteAgreesWithItselfOnAUIHelperCLI(t *testing.T) {
	id := createUIHelper(t, "input_boolean", map[string]any{"name": "E2E Delete Refusal"})
	entityID := "input_boolean." + id
	waitForHelperShow(t, entityID)

	preview, previewErr := runHactlE2E(t, "helper", "delete", entityID)
	confirm, confirmErr := runHactlE2E(t, "helper", "delete", entityID, "--confirm")

	if (previewErr == nil) != (confirmErr == nil) {
		t.Fatalf("dry run and --confirm disagree on a storage-backed helper:\ndry-run (err=%v):\n%s\n--confirm (err=%v):\n%s",
			previewErr, preview, confirmErr, confirm)
	}
	if previewErr == nil {
		t.Fatalf("dry run planned a delete of a helper that cannot be deleted:\n%s", preview)
	}
	if !strings.Contains(preview, "storage") {
		t.Errorf("the refusal does not name the reason:\n%s", preview)
	}

	// The refused delete must not have removed anything.
	if _, err := runHactlE2E(t, "helper", "show", entityID); err != nil {
		t.Errorf("the helper is gone after two refused deletes")
	}
}

// helperCreateRefusal extracts the companion's refusal text from CLI output, so
// the preview's explanation can be compared with the confirmed run's rather
// than merely their exit codes.
// Bounded by a newline and by the double quote that ends the JSON string the
// confirmed run's error carries its body in — the message itself uses single
// quotes, so the two runs' reasons compare as the same text.
var helperCreateRefusal = regexp.MustCompile(`Home Assistant reads [^\n"]*`)

// readConfigFileE2E / writeConfigFileE2E own their own context so the test body
// carries none: `runHactlE2E` spawns the CLI under the test's context, and a
// context in scope there makes contextcheck ask for one it must not receive.
func readConfigFileE2E(t *testing.T, path string) string {
	t.Helper()
	resp, err := testClient.ReadConfigFileRaw(context.Background(), path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return resp.Content
}

func writeConfigFileE2E(t *testing.T, path, content string) {
	t.Helper()
	if _, err := testClient.WriteConfigFile(context.Background(), path, content, false); err != nil {
		t.Fatalf("writing %s: %v — if HA rejected it, this is not a layout a real instance can have", path, err)
	}
}

// TestE2EHelperCreatePreviewMatchesConfirmOnEveryLayoutCLI is the defect
// itself: on an instance whose helper domain is written inline in
// configuration.yaml, every create is a 400 and every preview used to promise
// one anyway. Asserted as an equality across both layouts.
func TestE2EHelperCreatePreviewMatchesConfirmOnEveryLayoutCLI(t *testing.T) {
	original := readConfigFileE2E(t, "configuration.yaml")
	stripped := stripDomainKeys(original, "input_boolean")
	t.Cleanup(func() { writeConfigFileE2E(t, "configuration.yaml", original) })
	writeConfigFileE2E(t, "input_boolean.yaml", "# e2e parity probe\n")

	for _, tc := range []struct {
		name       string
		config     string
		helperID   string
		mustRefuse bool
	}{
		{
			name:       "inline",
			config:     stripped + "\ninput_boolean:\n  e2e_inline_flag:\n    name: E2E Inline\n",
			helperID:   "e2e_parity_inline",
			mustRefuse: true,
		},
		{
			name:     "included",
			config:   stripped + "\ninput_boolean: !include input_boolean.yaml\n",
			helperID: "e2e_parity_included",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeConfigFileE2E(t, "configuration.yaml", tc.config)
			file := writeTempYAML(t, "parity.yaml", tc.helperID+":\n  name: E2E Parity Probe\n")

			preview, previewErr := runHactlE2E(t, "helper", "create", "input_boolean", "-f", file)
			confirm, confirmErr := runHactlE2E(t, "helper", "create", "input_boolean", "-f", file, "--confirm")
			if confirmErr == nil {
				t.Cleanup(func() { _, _ = runHactlE2E(t, "helper", "delete", tc.helperID, "--confirm") })
			}

			if (previewErr == nil) != (confirmErr == nil) {
				t.Fatalf("preview and --confirm disagree on the %s layout:\ndry-run (err=%v):\n%s\n--confirm (err=%v):\n%s",
					tc.name, previewErr, preview, confirmErr, confirm)
			}
			if tc.mustRefuse == (previewErr == nil) {
				t.Fatalf("the %s layout should refuse=%v, got dry-run err=%v:\n%s", tc.name, tc.mustRefuse, previewErr, preview)
			}
			if previewErr == nil {
				return
			}
			// Same verdict is not enough: a preview that refuses for its own
			// reason sends the operator somewhere else than the run it predicts.
			previewReason := helperCreateRefusal.FindString(preview)
			confirmReason := helperCreateRefusal.FindString(confirm)
			if previewReason == "" || previewReason != confirmReason {
				t.Errorf("preview and --confirm explain the refusal differently:\npreview: %q\nconfirm: %q",
					previewReason, confirmReason)
			}
		})
	}
}

// stripDomainKeys removes every top-level `<domain>:` / `<domain> <label>:`
// line, mirroring HA's own extract_domain_configs matching so the result is a
// config HA genuinely does not read the domain's file from.
func stripDomainKeys(config, domain string) string {
	key := regexp.MustCompile(`^` + regexp.QuoteMeta(domain) + `(| .+):`)
	var kept []string
	for line := range strings.SplitSeq(config, "\n") {
		if !key.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
