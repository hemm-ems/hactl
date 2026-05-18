package analyze

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// AnomalyType classifies the kind of anomaly detected.
type AnomalyType string

// Anomaly type constants.
const (
	AnomalyGap   AnomalyType = "gap"
	AnomalyStuck AnomalyType = "stuck"
	AnomalySpike AnomalyType = "spike"
)

// Anomaly describes a detected anomaly in a time series.
type Anomaly struct {
	Start    time.Time   `json:"start"`
	End      time.Time   `json:"end"`
	Type     AnomalyType `json:"type"`
	Detail   string      `json:"detail"`
	Duration time.Duration
	Value    float64
}

// DetectGaps finds time gaps exceeding threshold between consecutive data points.
func DetectGaps(points []DataPoint, threshold time.Duration) []Anomaly {
	var anomalies []Anomaly
	for i := 1; i < len(points); i++ {
		gap := points[i].Time.Sub(points[i-1].Time)
		if gap > threshold {
			anomalies = append(anomalies, Anomaly{
				Type:     AnomalyGap,
				Start:    points[i-1].Time,
				End:      points[i].Time,
				Duration: gap,
				Detail:   fmt.Sprintf("no data for %s", gap.Truncate(time.Second)),
			})
		}
	}
	return anomalies
}

// DetectStuck finds periods where the value doesn't change for longer than threshold.
func DetectStuck(points []DataPoint, threshold time.Duration) []Anomaly {
	if len(points) < 2 {
		return nil
	}

	var anomalies []Anomaly
	stuckStart := 0

	for i := 1; i < len(points); i++ {
		if points[i].Value != points[stuckStart].Value {
			dur := points[i-1].Time.Sub(points[stuckStart].Time)
			if dur >= threshold {
				anomalies = append(anomalies, Anomaly{
					Type:     AnomalyStuck,
					Start:    points[stuckStart].Time,
					End:      points[i-1].Time,
					Duration: dur,
					Value:    points[stuckStart].Value,
					Detail:   fmt.Sprintf("stuck at %.2f for %s", points[stuckStart].Value, dur.Truncate(time.Second)),
				})
			}
			stuckStart = i
		}
	}

	// Check trailing stuck period
	dur := points[len(points)-1].Time.Sub(points[stuckStart].Time)
	if dur >= threshold {
		anomalies = append(anomalies, Anomaly{
			Type:     AnomalyStuck,
			Start:    points[stuckStart].Time,
			End:      points[len(points)-1].Time,
			Duration: dur,
			Value:    points[stuckStart].Value,
			Detail:   fmt.Sprintf("stuck at %.2f for %s", points[stuckStart].Value, dur.Truncate(time.Second)),
		})
	}

	return anomalies
}

// DetectSpikes finds points where the value deviates by more than zThreshold
// standard deviations from the mean.
func DetectSpikes(points []DataPoint, zThreshold float64) []Anomaly {
	if len(points) < 3 {
		return nil
	}

	sum := 0.0
	for _, p := range points {
		sum += p.Value
	}
	mean := sum / float64(len(points))

	sumSq := 0.0
	for _, p := range points {
		d := p.Value - mean
		sumSq += d * d
	}
	stddev := math.Sqrt(sumSq / float64(len(points)))

	if stddev == 0 {
		return nil
	}

	var anomalies []Anomaly
	for _, p := range points {
		z := math.Abs(p.Value-mean) / stddev
		if z > zThreshold {
			anomalies = append(anomalies, Anomaly{
				Type:   AnomalySpike,
				Start:  p.Time,
				End:    p.Time,
				Value:  p.Value,
				Detail: fmt.Sprintf("value=%.2f z=%.1f (mean=%.2f stddev=%.2f)", p.Value, z, mean, stddev),
			})
		}
	}

	return anomalies
}

// DetectAll runs all anomaly detectors and returns results sorted by time.
func DetectAll(points []DataPoint, gapThreshold, stuckThreshold time.Duration, spikeZ float64) []Anomaly {
	all := make([]Anomaly, 0, len(points))
	all = append(all, DetectGaps(points, gapThreshold)...)
	all = append(all, DetectStuck(points, stuckThreshold)...)
	all = append(all, DetectSpikes(points, spikeZ)...)

	sort.Slice(all, func(i, j int) bool {
		return all[i].Start.Before(all[j].Start)
	})
	return all
}
