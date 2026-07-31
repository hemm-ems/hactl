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
//
// The bucket width is derived here — the caller asked for a POINT COUNT and
// does not care how wide a bucket has to be to produce it. ResampleDuration is
// the other half of the pair, where the caller names the width instead; the
// two must not be confused, and confusing them is exactly what went wrong (see
// there).
func Resample(points []DataPoint, targetPoints int) []DataPoint {
	if len(points) <= targetPoints || targetPoints <= 0 {
		return points
	}

	start := points[0].Time
	span := points[len(points)-1].Time.Sub(start)
	if span <= 0 {
		return points[:1]
	}

	return bucketize(points, start, span/time.Duration(targetPoints), targetPoints)
}

// ResampleDuration averages points into buckets of exactly bucketDur.
//
// It used to compute `target := int(span / bucketDur)` and hand that to
// Resample, which then divided the span into that many equal buckets — so the
// width the caller asked for was never applied. Two errors compounded, both
// visible in one command:
//
//	hactl ent hist sensor.x --since 60m --resample 10m
//	→ 5 points, 12 minutes apart
//
// Integer division floors, so the final partial bucket was dropped and the
// count came out one short of the window; then span/count widened every
// remaining bucket to cover the ground the missing one had held. The requested
// resolution was silently coarsened every single time, and nothing said so.
// Live-fire finding #39.
//
// Buckets now start at the first point and are bucketDur wide, and there are
// as many as it takes to cover the span — ceiling, not floor. A bucket with no
// points in it produces no row: a gap in the history is a gap, and inventing a
// value to fill it would be a worse answer than a shorter series.
func ResampleDuration(points []DataPoint, bucketDur time.Duration) []DataPoint {
	if len(points) == 0 || bucketDur <= 0 {
		return points
	}

	start := points[0].Time
	span := points[len(points)-1].Time.Sub(start)
	buckets := int(span / bucketDur)
	if span%bucketDur != 0 {
		buckets++
	}
	if buckets <= 0 {
		buckets = 1
	}

	return bucketize(points, start, bucketDur, buckets)
}

// bucketize averages points into consecutive windows of bucketDur beginning at
// start, and stamps each result at its bucket's midpoint.
//
// It is shared by both entry points above so that "which samples belong to a
// bucket" has one answer. The two callers differ only in where bucketDur comes
// from, and while each had its own loop they also had their own idea of how
// many buckets there should be.
func bucketize(points []DataPoint, start time.Time, bucketDur time.Duration, buckets int) []DataPoint {
	result := make([]DataPoint, 0, buckets)

	pi := 0
	for b := range buckets {
		bStart := start.Add(time.Duration(b) * bucketDur)
		bEnd := bStart.Add(bucketDur)
		// The last bucket is closed at its upper end. Buckets are otherwise
		// half-open, and the series' final timestamp is by construction a
		// bucket boundary whenever the span divides evenly, so a half-open last
		// bucket would drop the newest reading from every such series. The last
		// bucket therefore takes everything that is left.
		last := b == buckets-1

		sum := 0.0
		count := 0
		for pi < len(points) && (last || points[pi].Time.Before(bEnd)) {
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
