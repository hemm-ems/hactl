package cmd

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeURLEnv writes a minimal .env pointing at baseURL.
func writeURLEnv(t *testing.T, dir, baseURL string) {
	t.Helper()
	content := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=test-token\n", baseURL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSvcCall_ReturnFlag verifies that --return prints the service response JSON.
func TestSvcCall_ReturnFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"response":{"result":"valid"}}]`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeURLEnv(t, dir, srv.URL)

	oldDir := flagDir
	flagDir = dir
	defer func() { flagDir = oldDir }()
	oldReturn := flagSvcReturn
	flagSvcReturn = true
	defer func() { flagSvcReturn = oldReturn }()

	var out bytes.Buffer
	if err := runSvcCall(t.Context(), &out, "homeassistant.check_config"); err != nil {
		t.Fatalf("runSvcCall --return: %v", err)
	}
	if !strings.Contains(out.String(), "result") {
		t.Errorf("--return output should contain response JSON, got: %q", out.String())
	}
}

// TestSvcCall_NoReturn verifies that without --return, only "called X.Y" is printed.
func TestSvcCall_NoReturn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{}]`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeURLEnv(t, dir, srv.URL)

	oldDir := flagDir
	flagDir = dir
	defer func() { flagDir = oldDir }()
	oldReturn := flagSvcReturn
	flagSvcReturn = false
	defer func() { flagSvcReturn = oldReturn }()

	var out bytes.Buffer
	if err := runSvcCall(t.Context(), &out, "homeassistant.check_config"); err != nil {
		t.Fatalf("runSvcCall (no --return): %v", err)
	}
	if !strings.Contains(out.String(), "called homeassistant.check_config") {
		t.Errorf("without --return, expect 'called X.Y', got: %q", out.String())
	}
	if strings.Contains(out.String(), "result") {
		t.Errorf("without --return, response body must not appear, got: %q", out.String())
	}
}

// TestAutoLs_Failing_EmptyHint verifies that failingEmptyHint returns a useful string.
func TestAutoLs_Failing_EmptyHint(t *testing.T) {
	hint := failingEmptyHint()
	if !strings.Contains(hint, "hactl log") {
		t.Errorf("failing empty hint should mention 'hactl log', got: %q", hint)
	}
}

// TestEntLs_LabelNotFound verifies labelNotFoundHint returns an actionable message.
func TestEntLs_LabelNotFound(t *testing.T) {
	msg := labelNotFoundHint("heat_pump")
	if !strings.Contains(msg, "heat_pump") {
		t.Errorf("hint should include the label name, got: %q", msg)
	}
	if !strings.Contains(msg, "label ls") {
		t.Errorf("hint should suggest 'label ls', got: %q", msg)
	}
}

// TestLabelCreate_NoConfirm_DryRun verifies label create without --confirm prints a summary.
func TestLabelCreate_NoConfirm_DryRun(t *testing.T) {
	oldConfirm := flagLabelConfirm
	flagLabelConfirm = false
	defer func() { flagLabelConfirm = oldConfirm }()

	out := dryRunLabelSummary("Energy", "mdi:flash", "red", "Power consumers")
	if !strings.Contains(out, "would create label") {
		t.Errorf("dry-run should say 'would create label', got: %q", out)
	}
	if !strings.Contains(out, "Energy") {
		t.Errorf("dry-run should mention label name, got: %q", out)
	}
}

// TestRollback_AliasMsg verifies rollbackDeprecationMsg mentions auto rollback.
func TestRollback_AliasMsg(t *testing.T) {
	msg := rollbackDeprecationMsg()
	if !strings.Contains(msg, "auto rollback") {
		t.Errorf("deprecation message should mention 'auto rollback', got: %q", msg)
	}
}

// TestAutoRollbackRegistered verifies hactl auto rollback is a registered command.
func TestAutoRollbackRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"auto", "rollback"})
	if err != nil || cmd == nil || cmd.Name() != "rollback" {
		t.Fatalf("'hactl auto rollback' not registered: cmd=%v err=%v", cmd, err)
	}
}
