package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_OpenClose(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// cache/ directory should exist
	cacheDir := filepath.Join(dir, "cache")
	if _, statErr := os.Stat(cacheDir); os.IsNotExist(statErr) {
		t.Fatal("cache directory was not created")
	}

	// traces.db should exist
	dbPath := filepath.Join(cacheDir, "traces.db")
	if _, statErr := os.Stat(dbPath); os.IsNotExist(statErr) {
		t.Fatal("traces.db was not created")
	}
}

func TestStore_TraceRoundtrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	if storeErr := store.StoreTrace(ctx, "run1", "automation", "test_auto", "2026-04-16T09:42:00Z", "finished", "", "action/0", "time", []byte(`{"test": true}`)); storeErr != nil {
		t.Fatalf("StoreTrace failed: %v", storeErr)
	}

	raw, getErr := store.GetTrace(ctx, "run1")
	if getErr != nil {
		t.Fatalf("GetTrace failed: %v", getErr)
	}
	if string(raw) != `{"test": true}` {
		t.Errorf("GetTrace = %q, want %q", string(raw), `{"test": true}`)
	}
}

func TestStore_TraceCount(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	count, countErr := store.TraceCount(ctx)
	if countErr != nil {
		t.Fatalf("TraceCount failed: %v", countErr)
	}
	if count != 0 {
		t.Errorf("initial count = %d, want 0", count)
	}

	_ = store.StoreTrace(ctx, "r1", "automation", "a", "2026-01-01T00:00:00Z", "finished", "", "", "", []byte("{}"))
	_ = store.StoreTrace(ctx, "r2", "automation", "b", "2026-01-01T00:00:00Z", "error", "err", "", "", []byte("{}"))

	count, countErr = store.TraceCount(ctx)
	if countErr != nil {
		t.Fatalf("TraceCount failed: %v", countErr)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestStore_BatchTraces(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	records := []TraceRecord{
		{RunID: "r1", Domain: "automation", ItemID: "a", StartTime: "2026-01-01T00:00:00Z", Execution: "finished", RawJSON: "{}"},
		{RunID: "r2", Domain: "automation", ItemID: "a", StartTime: "2026-01-02T00:00:00Z", Execution: "error", ErrorMsg: "fail", RawJSON: "{}"},
		{RunID: "r3", Domain: "automation", ItemID: "b", StartTime: "2026-01-03T00:00:00Z", Execution: "finished", RawJSON: "{}"},
	}

	if storeErr := store.StoreTraces(ctx, records); storeErr != nil {
		t.Fatalf("StoreTraces failed: %v", storeErr)
	}

	count, _ := store.TraceCount(ctx)
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}

	// Query for item "a"
	results, queryErr := store.GetTracesForItem(ctx, "automation", "a", 10)
	if queryErr != nil {
		t.Fatalf("GetTracesForItem failed: %v", queryErr)
	}
	if len(results) != 2 {
		t.Errorf("results for 'a' = %d, want 2", len(results))
	}
	// Should be ordered by start_time desc
	if len(results) >= 2 && results[0].RunID != "r2" {
		t.Errorf("first result = %q, want r2 (most recent)", results[0].RunID)
	}
}

func TestStore_ClearTraces(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.StoreTrace(ctx, "r1", "automation", "a", "2026-01-01T00:00:00Z", "finished", "", "", "", []byte("{}"))

	if clearErr := store.ClearTraces(ctx); clearErr != nil {
		t.Fatalf("ClearTraces failed: %v", clearErr)
	}
	count, _ := store.TraceCount(ctx)
	if count != 0 {
		t.Errorf("count after clear = %d, want 0", count)
	}
}

func TestStore_MetaRoundtrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	if setErr := store.SetMeta(ctx, "test_key", "test_value"); setErr != nil {
		t.Fatalf("SetMeta failed: %v", setErr)
	}
	val, getErr := store.GetMeta(ctx, "test_key")
	if getErr != nil {
		t.Fatalf("GetMeta failed: %v", getErr)
	}
	if val != "test_value" {
		t.Errorf("GetMeta = %q, want %q", val, "test_value")
	}
}

func TestStore_GetMetaNotFound(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	val, getErr := store.GetMeta(ctx, "nonexistent")
	if getErr != nil {
		t.Fatalf("GetMeta failed: %v", getErr)
	}
	if val != "" {
		t.Errorf("GetMeta for nonexistent = %q, want empty", val)
	}
}

func TestStore_LogsRoundtrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	logText := "2026-04-16 09:42:00 ERROR (Main) [comp] test error\n"
	if refreshErr := store.RefreshLogs(ctx, logText); refreshErr != nil {
		t.Fatalf("RefreshLogs failed: %v", refreshErr)
	}

	data, readErr := store.ReadLogs()
	if readErr != nil {
		t.Fatalf("ReadLogs failed: %v", readErr)
	}
	if data != logText {
		t.Errorf("ReadLogs = %q, want %q", data, logText)
	}
}

func TestStore_ClearAll(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.StoreTrace(ctx, "r1", "automation", "a", "2026-01-01T00:00:00Z", "finished", "", "", "", []byte("{}"))
	_ = store.SetMeta(ctx, "key", "val")
	_ = store.RefreshLogs(ctx, "log data")

	if clearErr := store.Clear(ctx); clearErr != nil {
		t.Fatalf("Clear failed: %v", clearErr)
	}

	count, _ := store.TraceCount(ctx)
	if count != 0 {
		t.Errorf("traces after clear = %d, want 0", count)
	}
	val, _ := store.GetMeta(ctx, "key")
	if val != "" {
		t.Errorf("meta after clear = %q, want empty", val)
	}
	logs, _ := store.ReadLogs()
	if logs != "" {
		t.Errorf("logs after clear = %q, want empty", logs)
	}
}

func TestStore_Status(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	status, statusErr := store.GetStatus(ctx)
	if statusErr != nil {
		t.Fatalf("GetStatus failed: %v", statusErr)
	}
	if status.TraceCount != 0 {
		t.Errorf("initial trace count = %d, want 0", status.TraceCount)
	}
	if status.TracesSync != "" {
		t.Errorf("initial traces sync = %q, want empty", status.TracesSync)
	}
}

func TestStore_Dir(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	got := store.Dir()
	if got == "" {
		t.Fatal("Dir() returned empty string")
	}
	// Dir should contain the temp dir path
	if !filepath.IsAbs(got) {
		t.Errorf("Dir() = %q is not absolute", got)
	}
}

func TestStore_AppendAndReadLogs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	entries := []json.RawMessage{
		json.RawMessage(`{"level":"ERROR","msg":"test error"}`),
		json.RawMessage(`{"level":"WARNING","msg":"test warning"}`),
	}

	if appendErr := store.AppendLogs(entries); appendErr != nil {
		t.Fatalf("AppendLogs failed: %v", appendErr)
	}

	data, readErr := store.ReadLogs()
	if readErr != nil {
		t.Fatalf("ReadLogs failed: %v", readErr)
	}
	if !strings.Contains(data, "test error") {
		t.Errorf("ReadLogs missing first entry: %q", data)
	}
	if !strings.Contains(data, "test warning") {
		t.Errorf("ReadLogs missing second entry: %q", data)
	}
}

func TestStore_AppendLogsMultipleCalls(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.AppendLogs([]json.RawMessage{json.RawMessage(`{"n":1}`)})
	_ = store.AppendLogs([]json.RawMessage{json.RawMessage(`{"n":2}`)})

	data, _ := store.ReadLogs()
	if !strings.Contains(data, `{"n":1}`) || !strings.Contains(data, `{"n":2}`) {
		t.Errorf("ReadLogs missing entries after multiple AppendLogs calls: %q", data)
	}
}

func TestStore_TrimLogs_BelowMax(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Write 3 lines — well below maxLogLines
	_ = store.RefreshLogs(ctx, "line1\nline2\nline3\n")

	if trimErr := store.TrimLogs(); trimErr != nil {
		t.Fatalf("TrimLogs failed: %v", trimErr)
	}

	data, _ := store.ReadLogs()
	if !strings.Contains(data, "line1") || !strings.Contains(data, "line3") {
		t.Errorf("TrimLogs removed lines unexpectedly: %q", data)
	}
}

func TestStore_TrimLogs_AboveMax(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Build more than maxLogLines (10000) lines
	var sb strings.Builder
	for i := range 10010 {
		_, _ = fmt.Fprintf(&sb, "line%d\n", i)
	}
	_ = store.RefreshLogs(ctx, sb.String())

	if trimErr := store.TrimLogs(); trimErr != nil {
		t.Fatalf("TrimLogs failed: %v", trimErr)
	}

	data, _ := store.ReadLogs()
	lines := strings.Split(strings.TrimRight(data, "\n"), "\n")
	if len(lines) != maxLogLines {
		t.Errorf("after trim, line count = %d, want %d", len(lines), maxLogLines)
	}
	// Should keep the LAST maxLogLines lines (lines 10 through 10009)
	if !strings.Contains(data, "line10010") || strings.Contains(data, "line0\n") {
		// line10010 may not exist (only 0-10009), check oldest trimmed
		if strings.HasPrefix(data, "line0\n") {
			t.Error("TrimLogs did not remove the oldest entries")
		}
	}
}

func TestStore_TrimLogs_NoFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// No log file written — should be a no-op
	if trimErr := store.TrimLogs(); trimErr != nil {
		t.Fatalf("TrimLogs on non-existent file failed: %v", trimErr)
	}
}

func TestStore_RefreshTraces(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Pre-populate with one trace
	_ = store.StoreTrace(ctx, "old1", "automation", "old_auto", "2025-01-01T00:00:00Z", "finished", "", "", "", []byte("{}"))

	// RefreshTraces should replace all traces and set sync time
	records := []TraceRecord{
		{RunID: "new1", Domain: "automation", ItemID: "climate", StartTime: "2026-01-01T00:00:00Z", Execution: "finished", RawJSON: `{"test":1}`},
		{RunID: "new2", Domain: "automation", ItemID: "alarm", StartTime: "2026-01-02T00:00:00Z", Execution: "error", ErrorMsg: "template fail", RawJSON: `{"test":2}`},
	}

	if refreshErr := store.RefreshTraces(ctx, records); refreshErr != nil {
		t.Fatalf("RefreshTraces failed: %v", refreshErr)
	}

	count, _ := store.TraceCount(ctx)
	if count != 2 {
		t.Errorf("count after RefreshTraces = %d, want 2", count)
	}

	// Old trace should be gone
	_, oldErr := store.GetTrace(ctx, "old1")
	if oldErr == nil {
		t.Error("expected error fetching deleted trace, got nil")
	}

	// Sync time should be set
	syncTime, _ := store.GetMeta(ctx, "traces_sync")
	if syncTime == "" {
		t.Error("RefreshTraces did not set traces_sync meta")
	}
}

func TestStore_LogSize(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// No log file → size 0
	size, sizeErr := store.LogSize()
	if sizeErr != nil {
		t.Fatalf("LogSize on missing file failed: %v", sizeErr)
	}
	if size != 0 {
		t.Errorf("LogSize with no file = %d, want 0", size)
	}

	// After writing logs, size should be > 0
	_ = store.RefreshLogs(ctx, "some log data\n")
	size, sizeErr = store.LogSize()
	if sizeErr != nil {
		t.Fatalf("LogSize failed: %v", sizeErr)
	}
	if size == 0 {
		t.Error("LogSize after writing = 0, want > 0")
	}
}

func TestStore_ClearLogs_NonExistent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Clearing non-existent log file should not error
	if clearErr := store.ClearLogs(); clearErr != nil {
		t.Fatalf("ClearLogs on non-existent file failed: %v", clearErr)
	}
}

func TestStore_GetStatus_WithData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.StoreTrace(ctx, "r1", "automation", "a", "2026-01-01T00:00:00Z", "finished", "", "", "", []byte("{}"))
	_ = store.RefreshLogs(ctx, "log line\n")
	_ = store.SetMeta(ctx, "traces_sync", "2026-01-01T12:00:00Z")

	status, statusErr := store.GetStatus(ctx)
	if statusErr != nil {
		t.Fatalf("GetStatus failed: %v", statusErr)
	}
	if status.TraceCount != 1 {
		t.Errorf("trace count = %d, want 1", status.TraceCount)
	}
	if status.LogSize == 0 {
		t.Error("log size = 0, want > 0")
	}
	if status.TracesSync != "2026-01-01T12:00:00Z" {
		t.Errorf("traces_sync = %q, want 2026-01-01T12:00:00Z", status.TracesSync)
	}
}
