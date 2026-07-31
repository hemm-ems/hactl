//go:build livefire

package livefire

import (
	"encoding/json"
	"testing"
	"time"
)

// Finding #39: `ent hist --resample <d>` never applied the bucket duration it
// was given.
//
//	hactl ent hist sensor.gesamtleistung_verbrauch --since 60m --resample 10m
//	→ 5 points at 11:41 11:53 12:05 12:17 12:29 — twelve minutes apart
//
// The resampler turned the requested width into a point COUNT
// (`int(span/bucketDur)`, a floor) and then divided the span into that many
// equal buckets, so the last partial bucket was dropped and every remaining
// bucket widened to cover the ground it had held. The requested resolution was
// silently coarsened on every call.
//
// The assertion is on the SPACING, not the count. A count is the one thing a
// wrong bucket width can still get right — the case that was supposed to cover
// this asserted "~12 buckets, accept 10 to 14" and passed on 11 buckets of
// 5m21s each.
//
// The rig's series is the one TestMain backfills; the live profile is asked
// about a real household power meter over a day, which is where the finding
// came from. The live window is a day rather than the reported hour because an
// hour of a quiet meter can legitimately hold two readings, and a case that
// bails out on a thin series is a case that reports "nothing to see" on
// exactly the days it matters.
//
// sweepResampleBucket is the width asked of the RIG. It is a package-level
// constant because the backfilled span has to be a NON-multiple of it, which
// rigshapes_test.go asserts rather than leaving to the next person to notice.
const sweepResampleBucket = 10 * time.Minute

func TestSweepResampleUsesTheBucketItWasGiven(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		entity, window, bucket := rigHistory.EntityID, "3h", sweepResampleBucket
		if tgt.Profile == Live {
			entity, window, bucket = "sensor.gesamtleistung_verbrauch", "24h", time.Hour
		}

		out := tgt.MustRead(t, "ent", "hist", entity, "--since", window,
			"--resample", bucket.String(), "--json")
		var points []struct {
			Time  string `json:"time"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal([]byte(out), &points); err != nil {
			t.Fatalf("ent hist --json: %v\n%s", err, truncate(out))
		}
		// Three points is the smallest series in which "every gap is one
		// bucket" is a claim rather than an accident of two endpoints.
		if len(points) < 3 {
			t.Fatalf("%s holds %d resampled points over %s — the case cannot fail here, which is "+
				"not the same as passing:\n%s", entity, len(points), window, truncate(out))
		}

		var prev time.Time
		for i, p := range points {
			at, err := time.Parse(time.RFC3339, p.Time)
			if err != nil {
				t.Fatalf("point %d has an unparseable time %q: %v — H-10 requires the full "+
					"instant in JSON", i, p.Time, err)
			}
			if i > 0 {
				// A bucket with no samples in it produces no row, so a gap in
				// the history legitimately shows up as a multiple of the bucket
				// width. What may never happen is a gap that is not a multiple:
				// that is the resampler using a width nobody asked for.
				gap := at.Sub(prev)
				if gap%bucket != 0 || gap == 0 {
					t.Errorf("%s: points %d and %d are %s apart, which is not a whole number of "+
						"%s buckets — the requested resolution was silently coarsened (finding #39)",
						entity, i-1, i, gap, bucket)
				}
			}
			prev = at
		}
	})
}
