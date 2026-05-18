package cache

import (
	"context"
	"os"
	"path/filepath"
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
