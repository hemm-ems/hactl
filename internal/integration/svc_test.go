//go:build integration

package integration

import (
	"os"
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

func TestSvcCallCheckConfig(t *testing.T) {
	// homeassistant.check_config is a safe, read-like service
	out := runHactl(t, "svc", "call", "homeassistant.check_config")
	assertContains(t, out, "called homeassistant.check_config")
}

func TestSvcCallGroupSet(t *testing.T) {
	// Call persistent_notification.create with --data to test service calls with JSON data
	out := runHactl(t, "svc", "call", "persistent_notification.create",
		"--data", `{"title":"Test","message":"hello from hactl"}`)
	assertContains(t, out, "called persistent_notification.create")
}

func TestSvcCallGroupSetFull(t *testing.T) {
	// Call persistent_notification.create with complex data to verify JSON payload handling
	out := runHactl(t, "svc", "call", "persistent_notification.create",
		"--data", `{"title":"Full Test","message":"complex payload","notification_id":"hactl_test"}`)
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

	out := runHactl(t, "svc", "call", "persistent_notification.create", "--data", "@"+dataFile)
	assertContains(t, out, "called persistent_notification.create")
}

func TestSvcCallDataFromFileMissing(t *testing.T) {
	_, err := runHactlErr(t, "svc", "call", "group.set", "--data", "@/nonexistent/file.json")
	if err == nil {
		t.Error("svc call with missing @file should fail")
	}
}
