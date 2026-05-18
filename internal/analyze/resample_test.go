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

	// First bucket: values 0-4 → mean 2.0
	if math.Abs(result[0].Value-2.0) > 0.5 {
		t.Errorf("first bucket value = %.2f, want ~2.0", result[0].Value)
	}
	// Second bucket: values 5-9 → mean 7.0
	if math.Abs(result[1].Value-7.0) > 0.5 {
		t.Errorf("second bucket value = %.2f, want ~7.0", result[1].Value)
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
