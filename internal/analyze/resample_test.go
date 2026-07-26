package analyze

import (
	"math"
	"testing"
	"time"
)

func TestResample_NoChange(t *testing.T) {
	points := makePoints(5, time.Minute, 1.0)
	result := Resample(points, 10)
	if len(result) != 5 {
		t.Fatalf("expected 5 points, got %d", len(result))
	}
}

func TestResample_EmptyInput(t *testing.T) {
	result := Resample(nil, 10)
	if len(result) != 0 {
		t.Fatalf("expected 0 points, got %d", len(result))
	}
}

func TestResample_ZeroTarget(t *testing.T) {
	points := makePoints(10, time.Minute, 1.0)
	result := Resample(points, 0)
	if len(result) != 10 {
		t.Fatalf("expected 10 unchanged, got %d", len(result))
	}
}

func TestResample_Reduces(t *testing.T) {
	points := makePoints(100, time.Minute, 1.0)
	result := Resample(points, 10)
	if len(result) > 10 {
		t.Fatalf("expected <= 10 points, got %d", len(result))
	}
	if len(result) == 0 {
		t.Fatal("expected at least 1 point")
	}
}

func TestResample_AveragesValues(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := make([]DataPoint, 10)
	for i := range 10 {
		points[i] = DataPoint{
			Time:  start.Add(time.Duration(i) * time.Minute),
			Value: float64(i),
		}
	}

	result := Resample(points, 2)
	if len(result) != 2 {
		t.Fatalf("expected 2 points, got %d", len(result))
	}

	// The means are exact, not approximate: this test's tolerance used to be
	// 0.5, which is wide enough to accept a second bucket that has silently
	// lost a sample. It did: the comment said "values 5-9 → mean 7.0" while the
	// code returned 6.5, and the test passed anyway.
	const eps = 1e-9
	// First bucket: values 0-4 → mean 2.0
	if math.Abs(result[0].Value-2.0) > eps {
		t.Errorf("first bucket value = %.4f, want exactly 2.0 (mean of values 0-4)", result[0].Value)
	}
	// Second bucket: values 5-9 → mean 7.0
	if math.Abs(result[1].Value-7.0) > eps {
		t.Errorf("second bucket value = %.4f, want exactly 7.0 (mean of values 5-9); "+
			"6.5 means the newest sample was dropped", result[1].Value)
	}
}

// TestResample_KeepsNewestSample pins the property the half-open final bucket
// violated. `end` is by construction the timestamp of the last point, so a
// final bucket that excluded its own upper bound dropped the newest reading
// from every series Resample was ever handed, and integer division of the span
// could leave further samples stranded past the last bucket's end.
func TestResample_KeepsNewestSample(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Nine samples, one minute apart, flat at 0.0 except for the newest, which
	// is the only sample that can move an average away from zero.
	points := make([]DataPoint, 9)
	for i := range points {
		points[i] = DataPoint{Time: start.Add(time.Duration(i) * time.Minute)}
	}
	points[len(points)-1].Value = 30.0

	result := Resample(points, 3)
	if len(result) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(result))
	}

	// The final bucket spans minutes 6, 7 and 8, so the newest sample is one of
	// three: 30/3 = 10. A final bucket that stops short of `end` reports 0.0 and
	// the newest reading never appears in the answer at all.
	last := result[len(result)-1].Value
	if math.Abs(last-10.0) > 1e-9 {
		t.Errorf("final bucket = %.4f, want exactly 10.0 (mean of 0, 0, 30); "+
			"0.0 means the newest sample was never bucketed", last)
	}
}

// TestResample_KeepsEveryNewestSample is the same defect at its worst. Home
// Assistant's history can hold several rows sharing one timestamp, and when
// that timestamp is the newest one, a half-open final bucket discarded all of
// them at once rather than just the single boundary sample.
func TestResample_KeepsEveryNewestSample(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := make([]DataPoint, 0, 9)
	for i := range 6 {
		points = append(points, DataPoint{Time: start.Add(time.Duration(i) * time.Minute), Value: 1.0})
	}
	newest := start.Add(6 * time.Minute)
	for _, v := range []float64{10.0, 20.0, 30.0} {
		points = append(points, DataPoint{Time: newest, Value: v})
	}

	result := Resample(points, 2)
	if len(result) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(result))
	}
	// Final bucket: minutes 3, 4, 5 at 1.0 plus the three newest → 63/6 = 10.5.
	// Dropping the shared final timestamp reports 1.0 instead.
	if math.Abs(result[1].Value-10.5) > 1e-9 {
		t.Errorf("final bucket = %.4f, want exactly 10.5 — every sample sharing the "+
			"newest timestamp must be averaged in, not dropped", result[1].Value)
	}
}

func TestResample_SinglePoint(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{{Time: start, Value: 42.0}}
	result := Resample(points, 50)
	if len(result) != 1 {
		t.Fatalf("expected 1 point, got %d", len(result))
	}
	if result[0].Value != 42.0 {
		t.Errorf("value = %.2f, want 42.0", result[0].Value)
	}
}

func TestResample_SameTimestamp(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{
		{Time: start, Value: 1.0},
		{Time: start, Value: 2.0},
		{Time: start, Value: 3.0},
	}
	result := Resample(points, 1)
	if len(result) != 1 {
		t.Fatalf("expected 1 point, got %d", len(result))
	}
}

func TestResampleDuration_Empty(t *testing.T) {
	result := ResampleDuration(nil, 5*time.Minute)
	if len(result) != 0 {
		t.Fatalf("expected 0, got %d", len(result))
	}
}

func TestResampleDuration_FiveMinBuckets(t *testing.T) {
	// 60 points at 1-minute intervals → 5min buckets → ~12 buckets
	points := makePoints(60, time.Minute, 1.0)
	result := ResampleDuration(points, 5*time.Minute)
	if len(result) < 10 || len(result) > 14 {
		t.Fatalf("expected ~12 points, got %d", len(result))
	}
}

func TestResampleDuration_ZeroDuration(t *testing.T) {
	points := makePoints(10, time.Minute, 1.0)
	result := ResampleDuration(points, 0)
	if len(result) != 10 {
		t.Fatalf("expected 10 unchanged, got %d", len(result))
	}
}

func makePoints(n int, interval time.Duration, value float64) []DataPoint { //nolint:unparam // interval varies in other test files
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := make([]DataPoint, n)
	for i := range n {
		points[i] = DataPoint{
			Time:  start.Add(time.Duration(i) * interval),
			Value: value,
		}
	}
	return points
}
