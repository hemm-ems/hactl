package cache

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestTSStore_StoreSamples(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	ts, err := OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = ts.Close() }()

	times := []time.Time{
		time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	values := []float64{21.5, 22.0, 21.8}

	if storeErr := ts.StoreSamples(ctx, "sensor.temperature", times, values); storeErr != nil {
		t.Fatalf("store: %v", storeErr)
	}

	count, err := ts.SampleCount(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestTSStore_GetSamples(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	ts, err := OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = ts.Close() }()

	times := []time.Time{
		time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	values := []float64{21.5, 22.0, 21.8}

	if storeErr := ts.StoreSamples(ctx, "sensor.temperature", times, values); storeErr != nil {
		t.Fatalf("store: %v", storeErr)
	}

	since := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	until := time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)
	gotTimes, gotValues, err := ts.GetSamples(ctx, "sensor.temperature", since, until)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if len(gotTimes) != 2 {
		t.Fatalf("got %d samples, want 2", len(gotTimes))
	}
	if gotValues[0] != 22.0 {
		t.Errorf("first value = %.1f, want 22.0", gotValues[0])
	}
}

func TestTSStore_LatestSample(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	ts, err := OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = ts.Close() }()

	// No samples yet
	latest, err := ts.LatestSample(ctx, "sensor.x")
	if err != nil {
		t.Fatalf("latest (empty): %v", err)
	}
	if !latest.IsZero() {
		t.Errorf("expected zero time, got %v", latest)
	}

	times := []time.Time{
		time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	values := []float64{1.0, 2.0}

	if storeErr := ts.StoreSamples(ctx, "sensor.x", times, values); storeErr != nil {
		t.Fatalf("store: %v", storeErr)
	}

	latest, err = ts.LatestSample(ctx, "sensor.x")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if !latest.Equal(times[1]) {
		t.Errorf("latest = %v, want %v", latest, times[1])
	}
}

func TestTSStore_ClearEntity(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	ts, err := OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = ts.Close() }()

	times := []time.Time{time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)}
	values := []float64{1.0}

	_ = ts.StoreSamples(ctx, "sensor.a", times, values)
	_ = ts.StoreSamples(ctx, "sensor.b", times, values)

	if err := ts.ClearEntity(ctx, "sensor.a"); err != nil {
		t.Fatalf("clear entity: %v", err)
	}

	count, _ := ts.SampleCount(ctx)
	if count != 1 {
		t.Errorf("count = %d, want 1 (only sensor.b)", count)
	}
}

func TestTSStore_Clear(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	ts, err := OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = ts.Close() }()

	times := []time.Time{time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)}
	values := []float64{1.0}

	_ = ts.StoreSamples(ctx, "sensor.a", times, values)
	_ = ts.StoreSamples(ctx, "sensor.b", times, values)

	if err := ts.Clear(ctx); err != nil {
		t.Fatalf("clear: %v", err)
	}

	count, _ := ts.SampleCount(ctx)
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestTSStore_GetStatus(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	ts, err := OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = ts.Close() }()

	status, err := ts.GetStatus(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.SampleCount != 0 {
		t.Errorf("count = %d, want 0", status.SampleCount)
	}
}

func TestTSStore_DuplicateInsert(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	ts, err := OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = ts.Close() }()

	times := []time.Time{time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)}
	values := []float64{1.0}

	_ = ts.StoreSamples(ctx, "sensor.a", times, values)
	// Insert again — should be ignored (INSERT OR IGNORE)
	if storeErr := ts.StoreSamples(ctx, "sensor.a", times, values); storeErr != nil {
		t.Fatalf("duplicate store: %v", storeErr)
	}

	count, _ := ts.SampleCount(ctx)
	if count != 1 {
		t.Errorf("count = %d, want 1 (duplicates ignored)", count)
	}
}

func TestTSStore_MismatchedLengths(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	ts, err := OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = ts.Close() }()

	times := []time.Time{time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)}
	values := []float64{1.0, 2.0} // mismatched

	if storeErr := ts.StoreSamples(ctx, "sensor.a", times, values); storeErr == nil {
		t.Fatal("expected error for mismatched lengths")
	}
}

func TestOpenTS_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	ts, err := OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = ts.Close() }()

	// Check cache dir exists
	if _, statErr := os.Stat(dir + "/cache"); statErr != nil {
		t.Errorf("cache dir not created: %v", statErr)
	}
}
