package analyze

import (
	"time"
)

// DataPoint represents a single time series data point.
type DataPoint struct {
	Time  time.Time
	Value float64
}

// StateChange represents a state transition for non-numeric entities.
type StateChange struct {
	Time     time.Time
	State    string
	Duration time.Duration
}

// Resample reduces a time series to approximately targetPoints by averaging
// values in equal-width time buckets. Points must be sorted chronologically.
func Resample(points []DataPoint, targetPoints int) []DataPoint {
	if len(points) <= targetPoints || targetPoints <= 0 {
		return points
	}

	start := points[0].Time
	end := points[len(points)-1].Time
	span := end.Sub(start)
	if span <= 0 {
		return points[:1]
	}

	bucketDur := span / time.Duration(targetPoints)
	result := make([]DataPoint, 0, targetPoints)

	pi := 0
	for b := range targetPoints {
		bStart := start.Add(time.Duration(b) * bucketDur)
		bEnd := bStart.Add(bucketDur)

		sum := 0.0
		count := 0
		for pi < len(points) && points[pi].Time.Before(bEnd) {
			sum += points[pi].Value
			count++
			pi++
		}

		if count > 0 {
			result = append(result, DataPoint{
				Time:  bStart.Add(bucketDur / 2),
				Value: sum / float64(count),
			})
		}
	}

	return result
}

// ResampleDuration reduces points by averaging within fixed-duration buckets.
func ResampleDuration(points []DataPoint, bucketDur time.Duration) []DataPoint {
	if len(points) == 0 || bucketDur <= 0 {
		return points
	}

	start := points[0].Time
	end := points[len(points)-1].Time
	span := end.Sub(start)
	target := int(span / bucketDur)
	if target <= 0 {
		target = 1
	}

	return Resample(points, target)
}
