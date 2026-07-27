//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSvcCallHelp(t *testing.T) {
	out := runHactl(t, "svc", "call", "--help")
	assertContains(t, out, "domain")
	assertContains(t, out, "--data")
}

func TestSvcCallInvalidFormat(t *testing.T) {
	_, err := runHactlErr(t, "svc", "call", "badformat")
	if err == nil {
		t.Error("svc call badformat should fail")
	}
}

func TestSvcCallDryRunDefault(t *testing.T) {
	// Without --confirm nothing is executed; the planned call is printed in the
	// shared preview shape, which is the only one that honours --json.
	out := runHactl(t, "svc", "call", "homeassistant.check_config")
	assertContains(t, out, "dry-run: would call homeassistant.check_config")
	assertContains(t, out, "re-run with --confirm")
}

// TestSvcCallDryRunRefusesAServiceHADoesNotHave — H-2 against the live
// registry.
//
// The preview used to check only that the argument contained a dot: it never
// loaded the instance config, never contacted HA, and printed "would call:
// light.turn_onn" as the artifact a human approves before --confirm. The
// manual routes "turn X on" through this preview as the verification step.
func TestSvcCallDryRunRefusesAServiceHADoesNotHave(t *testing.T) {
	out, err := runHactlErr(t, "svc", "call", "homeassistant.check_configg")
	if err == nil {
		t.Fatalf("previewed a service Home Assistant does not register:\n%s", out)
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Errorf("the refusal must name the reason, got: %v", err)
	}
	if strings.Contains(out, "would call") {
		t.Errorf("a refused preview must print no plan on stdout:\n%s", out)
	}
}

// TestSvcCallPreviewIsMachineReadable — the other half of H-2. This preview was
// prose under --json, and it is one of the two an MCP caller reaches for most.
func TestSvcCallPreviewIsMachineReadable(t *testing.T) {
	out := runHactl(t, "svc", "call", "homeassistant.check_config", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json preview does not parse: %v\n%s", err, out)
	}
	if got["dry_run"] != true {
		t.Errorf("preview object must state dry_run:true, got %v", got["dry_run"])
	}
}

func TestSvcCallCheckConfig(t *testing.T) {
	// homeassistant.check_config is a safe, read-like service
	out := runHactl(t, "svc", "call", "homeassistant.check_config", "--confirm")
	assertContains(t, out, "called homeassistant.check_config")
}

func TestSvcCallGroupSet(t *testing.T) {
	// Call persistent_notification.create with --data to test service calls with JSON data
	out := runHactl(t, "svc", "call", "persistent_notification.create",
		"--data", `{"title":"Test","message":"hello from hactl"}`, "--confirm")
	assertContains(t, out, "called persistent_notification.create")
}

func TestSvcCallGroupSetFull(t *testing.T) {
	// Call persistent_notification.create with complex data to verify JSON payload handling
	out := runHactl(t, "svc", "call", "persistent_notification.create",
		"--data", `{"title":"Full Test","message":"complex payload","notification_id":"hactl_test"}`, "--confirm")
	assertContains(t, out, "called persistent_notification.create")
}

func TestSvcCallInvalidJSON(t *testing.T) {
	_, err := runHactlErr(t, "svc", "call", "test.service", "--data", "{invalid}")
	if err == nil {
		t.Error("svc call with invalid JSON should fail")
	}
}

func TestSvcCallNoArgs(t *testing.T) {
	_, err := runHactlErr(t, "svc", "call")
	if err == nil {
		t.Error("svc call without arguments should fail")
	}
}

func TestSvcCallDataFromFile(t *testing.T) {
	// Write JSON to a temp file and use @file syntax
	dir := t.TempDir()
	dataFile := dir + "/notification_data.json"
	if err := os.WriteFile(dataFile, []byte(`{"title":"File Test","message":"from file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	out := runHactl(t, "svc", "call", "persistent_notification.create", "--data", "@"+dataFile, "--confirm")
	assertContains(t, out, "called persistent_notification.create")
}

func TestSvcCallDataFromFileMissing(t *testing.T) {
	_, err := runHactlErr(t, "svc", "call", "group.set", "--data", "@/nonexistent/file.json")
	if err == nil {
		t.Error("svc call with missing @file should fail")
	}
}
