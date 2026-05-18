package analyze

import (
	"testing"
	"time"
)

func TestDetectGaps_NoGaps(t *testing.T) {
	points := makePoints(10, time.Minute, 1.0)
	anomalies := DetectGaps(points, time.Hour)
	if len(anomalies) != 0 {
		t.Fatalf("expected 0 gaps, got %d", len(anomalies))
	}
}

func TestDetectGaps_SingleGap(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{
		{Time: start, Value: 1.0},
		{Time: start.Add(30 * time.Minute), Value: 2.0},
		{Time: start.Add(3 * time.Hour), Value: 3.0}, // 2.5h gap
		{Time: start.Add(4 * time.Hour), Value: 4.0},
	}

	anomalies := DetectGaps(points, time.Hour)
	if len(anomalies) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(anomalies))
	}
	if anomalies[0].Type != AnomalyGap {
		t.Errorf("type = %q, want %q", anomalies[0].Type, AnomalyGap)
	}
	if anomalies[0].Duration < 2*time.Hour {
		t.Errorf("duration = %v, want >= 2h", anomalies[0].Duration)
	}
}

func TestDetectGaps_MultipleGaps(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{
		{Time: start, Value: 1.0},
		{Time: start.Add(2 * time.Hour), Value: 2.0},
		{Time: start.Add(3 * time.Hour), Value: 3.0},
		{Time: start.Add(6 * time.Hour), Value: 4.0},
	}

	anomalies := DetectGaps(points, time.Hour)
	if len(anomalies) != 2 {
		t.Fatalf("expected 2 gaps, got %d", len(anomalies))
	}
}

func TestDetectGaps_Empty(t *testing.T) {
	anomalies := DetectGaps(nil, time.Hour)
	if len(anomalies) != 0 {
		t.Fatalf("expected 0, got %d", len(anomalies))
	}
}

func TestDetectStuck_NoStuck(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{
		{Time: start, Value: 1.0},
		{Time: start.Add(30 * time.Minute), Value: 2.0},
		{Time: start.Add(60 * time.Minute), Value: 3.0},
	}
	anomalies := DetectStuck(points, time.Hour)
	if len(anomalies) != 0 {
		t.Fatalf("expected 0 stuck, got %d", len(anomalies))
	}
}

func TestDetectStuck_StuckPeriod(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{
		{Time: start, Value: 5.0},
		{Time: start.Add(1 * time.Hour), Value: 5.0},
		{Time: start.Add(3 * time.Hour), Value: 5.0},
		{Time: start.Add(4 * time.Hour), Value: 6.0},
	}
	anomalies := DetectStuck(points, 2*time.Hour)
	if len(anomalies) != 1 {
		t.Fatalf("expected 1 stuck, got %d", len(anomalies))
	}
	if anomalies[0].Value != 5.0 {
		t.Errorf("stuck value = %.2f, want 5.0", anomalies[0].Value)
	}
}

func TestDetectStuck_TrailingStuck(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{
		{Time: start, Value: 1.0},
		{Time: start.Add(1 * time.Hour), Value: 5.0},
		{Time: start.Add(4 * time.Hour), Value: 5.0},
	}
	anomalies := DetectStuck(points, 2*time.Hour)
	if len(anomalies) != 1 {
		t.Fatalf("expected 1 stuck, got %d", len(anomalies))
	}
}

func TestDetectStuck_SinglePoint(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{{Time: start, Value: 1.0}}
	anomalies := DetectStuck(points, time.Hour)
	if len(anomalies) != 0 {
		t.Fatalf("expected 0, got %d", len(anomalies))
	}
}

func TestDetectSpikes_NoSpikes(t *testing.T) {
	points := makePoints(20, time.Minute, 1.0) // all same value → stddev=0
	anomalies := DetectSpikes(points, 3.0)
	if len(anomalies) != 0 {
		t.Fatalf("expected 0 spikes, got %d", len(anomalies))
	}
}

func TestDetectSpikes_WithSpike(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := make([]DataPoint, 20)
	for i := range 20 {
		points[i] = DataPoint{
			Time:  start.Add(time.Duration(i) * time.Minute),
			Value: 20.0,
		}
	}
	// Add a spike
	points[10].Value = 100.0

	anomalies := DetectSpikes(points, 3.0)
	if len(anomalies) != 1 {
		t.Fatalf("expected 1 spike, got %d", len(anomalies))
	}
	if anomalies[0].Value != 100.0 {
		t.Errorf("spike value = %.2f, want 100.0", anomalies[0].Value)
	}
}

func TestDetectSpikes_TooFewPoints(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{
		{Time: start, Value: 1.0},
		{Time: start.Add(time.Minute), Value: 100.0},
	}
	anomalies := DetectSpikes(points, 3.0)
	if len(anomalies) != 0 {
		t.Fatalf("expected 0 spikes for < 3 points, got %d", len(anomalies))
	}
}

func TestDetectAll_Combined(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	points := []DataPoint{
		{Time: start, Value: 20.0},
		{Time: start.Add(30 * time.Minute), Value: 20.0},
		// 3h gap
		{Time: start.Add(4 * time.Hour), Value: 20.0},
		{Time: start.Add(5 * time.Hour), Value: 20.0},
		{Time: start.Add(6 * time.Hour), Value: 20.0},
		{Time: start.Add(7 * time.Hour), Value: 20.0},
		{Time: start.Add(8 * time.Hour), Value: 20.0},
		{Time: start.Add(9 * time.Hour), Value: 20.0},
		{Time: start.Add(10 * time.Hour), Value: 20.0},
		{Time: start.Add(11 * time.Hour), Value: 20.0},
		{Time: start.Add(12 * time.Hour), Value: 20.0},
		{Time: start.Add(13 * time.Hour), Value: 20.0},
		{Time: start.Add(14 * time.Hour), Value: 200.0}, // spike
	}

	anomalies := DetectAll(points, time.Hour, 3*time.Hour, 3.0)
	if len(anomalies) == 0 {
		t.Fatal("expected at least 1 anomaly")
	}

	// Should have at least a gap
	hasGap := false
	for _, a := range anomalies {
		if a.Type == AnomalyGap {
			hasGap = true
		}
	}
	if !hasGap {
		t.Error("expected at least one gap anomaly")
	}
}
