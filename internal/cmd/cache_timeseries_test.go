package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/cache"
)

// seedTimeseriesCache creates an instance dir with an .env and a timeseries
// cache holding n samples — the state `hactl ent hist` leaves behind.
func seedTimeseriesCache(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	ts, err := cache.OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("OpenTS: %v", err)
	}
	times := make([]time.Time, n)
	values := make([]float64, n)
	base := time.Now().Add(-time.Duration(n) * time.Minute)
	for i := range n {
		times[i] = base.Add(time.Duration(i) * time.Minute)
		values[i] = float64(i)
	}
	if err := ts.StoreSamples(ctx, "sensor.power", times, values); err != nil {
		t.Fatalf("StoreSamples: %v", err)
	}
	if err := ts.Close(); err != nil {
		t.Fatalf("closing timeseries cache: %v", err)
	}
	return dir
}

// timeseriesSampleCount reopens the timeseries cache and counts what survived.
func timeseriesSampleCount(t *testing.T, dir string) int {
	t.Helper()
	ctx := context.Background()
	ts, err := cache.OpenTS(ctx, dir)
	if err != nil {
		t.Fatalf("OpenTS: %v", err)
	}
	defer func() { _ = ts.Close() }()
	count, err := ts.SampleCount(ctx)
	if err != nil {
		t.Fatalf("SampleCount: %v", err)
	}
	return count
}

// `cache clear` must empty timeseries.db too — it is a separate file from
// traces.db and the only one that grows during normal use.
func TestRunCacheClear_ClearsTimeseriesCache(t *testing.T) {
	dir := seedTimeseriesCache(t, 5)
	withFlagDir(t, dir)

	if got := timeseriesSampleCount(t, dir); got != 5 {
		t.Fatalf("precondition: seeded sample count = %d, want 5", got)
	}

	var buf bytes.Buffer
	if err := runCacheClear(context.Background(), &buf); err != nil {
		t.Fatalf("runCacheClear failed: %v", err)
	}
	if !strings.Contains(buf.String(), "cache cleared") {
		t.Errorf("output = %q, want 'cache cleared'", buf.String())
	}
	if got := timeseriesSampleCount(t, dir); got != 0 {
		t.Errorf("after 'cache cleared', timeseries.db still holds %d samples; the timeseries cache was not cleared", got)
	}
}

// `cache status` must account for timeseries.db; otherwise the only cache that
// grows is invisible.
func TestRunCacheStatus_ReportsTimeseriesCache(t *testing.T) {
	dir := seedTimeseriesCache(t, 5)
	withFlagDir(t, dir)

	var buf bytes.Buffer
	if err := runCacheStatus(context.Background(), &buf); err != nil {
		t.Fatalf("runCacheStatus failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "timeseries") {
		t.Errorf("cache status never mentions the timeseries cache:\n%s", out)
	}
	if !strings.Contains(out, "5 samples") {
		t.Errorf("cache status does not report the 5 cached samples:\n%s", out)
	}
}

func TestRunCacheStatus_JSONReportsTimeseriesCache(t *testing.T) {
	dir := seedTimeseriesCache(t, 5)
	withFlagDir(t, dir)

	old := flagJSON
	flagJSON = true
	defer func() { flagJSON = old }()

	var buf bytes.Buffer
	if err := runCacheStatus(context.Background(), &buf); err != nil {
		t.Fatalf("runCacheStatus --json failed: %v", err)
	}
	var parsed struct {
		SampleCount     *int   `json:"timeseries_sample_count"`
		TimeseriesBytes *int64 `json:"timeseries_db_size"`
	}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\n%s", err, buf.String())
	}
	if parsed.SampleCount == nil || *parsed.SampleCount != 5 {
		t.Errorf("timeseries_sample_count missing or wrong in --json:\n%s", buf.String())
	}
	if parsed.TimeseriesBytes == nil || *parsed.TimeseriesBytes <= 0 {
		t.Errorf("timeseries_db_size missing or zero in --json:\n%s", buf.String())
	}
}
