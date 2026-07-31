//go:build livefire

package livefire

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/hatest"
)

var (
	rigHA    *hatest.Instance
	hactlBin string
)

// backfilledSeries is the history written into the rig's recorder before any
// case runs, and the ground truth those cases measure against.
//
// A freshly booted container holds minutes of history, so every command that
// reasons over a window — `ent hist --resample`, `ent anomalies` — could only
// ever be asked questions whose answer was "not enough data". That is the
// completed half of rig capability R5: the live profile can be asked about a
// house with years of history and the rig could not be asked at all, which is
// the asymmetry that let finding #39 live in a resampler for as long as it did.
type backfilledSeries struct {
	EntityID string
	Step     time.Duration // the interval between samples
	Span     time.Duration // first sample to last
}

// rigHistory describes what TestMain writes: a moving numeric series on a
// one-minute grid, dense enough that a ten-minute bucket holds ten samples and
// no bucket is empty.
//
// The span is deliberately NOT a whole number of ten-minute buckets. Two hours
// was the first choice and it made the resample case pass against the very
// defect it exists for: `int(span/bucket)` and `ceil(span/bucket)` agree
// exactly when the span divides evenly, and span/count then reproduces the
// requested width by arithmetic accident. Two hours and five minutes is 12.5
// buckets, so the floor loses one and the survivors widen to 10m25s — the
// shape finding #39 was reported as.
var rigHistory = backfilledSeries{
	EntityID: "sensor.sweep_series",
	Step:     time.Minute,
	Span:     2*time.Hour + 5*time.Minute,
}

func TestMain(m *testing.M) {
	// The rig profile always runs. The live profile joins it only when
	// HACTL_LIVEFIRE_DIR names a configured instance, so `go test` can never
	// wander onto somebody's house by accident.
	var code int
	opts := []hatest.Option{hatest.WithFixture(rigFixture)}
	if img := os.Getenv("HACTL_HA_IMAGE"); img != "" {
		opts = append(opts, hatest.WithImage(img))
	}
	binDir, err := os.MkdirTemp("", "hactl-livefire-*")
	if err != nil {
		panic(err)
	}
	if hactlBin, err = BuildHactl(binDir); err != nil {
		_ = os.RemoveAll(binDir)
		panic(err)
	}

	rigHA, code = hatest.StartMain(m, opts...)
	if code != 0 {
		_ = os.RemoveAll(binDir)
		os.Exit(code)
	}
	// Before any case runs, so that no case has to reason about a container
	// that restarts underneath it: Backfill stops HA to write into the recorder
	// database and Docker re-assigns the published port on the way back up.
	if err := backfillRigHistory(rigHA); err != nil {
		rigHA.Stop()
		_ = os.RemoveAll(binDir)
		panic(err)
	}
	exit := m.Run()
	rigHA.Stop()
	// os.Exit skips deferred calls, so the build directory is removed here.
	_ = os.RemoveAll(binDir)
	os.Exit(exit)
}

// backfillRigHistory writes rigHistory into the rig's recorder.
//
// The values move — a ramp with a wobble on it — because a flat series
// averages to itself, and a resampler that assigned samples to the wrong
// buckets would pass every value check there is against one. Consecutive
// samples never repeat a value either, which is what makes them survive HA's
// significant_changes_only history filter.
func backfillRigHistory(inst *hatest.Instance) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Anchored to a whole minute so the bucket arithmetic a case asserts is
	// exact rather than exact-to-within-a-jitter.
	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-rigHistory.Span)
	count := int(rigHistory.Span/rigHistory.Step) + 1

	samples := make([]hatest.Sample, 0, count)
	for i := range count {
		at := start.Add(time.Duration(i) * rigHistory.Step)
		// Two decimals of a ramp with a wobble: monotone enough to read, never
		// twice the same value.
		value := 100 + float64(i)/3 + float64(i%7)
		samples = append(samples, hatest.Sample{
			At:    at,
			State: fmt.Sprintf("%.2f", value),
			Attrs: map[string]any{
				"unit_of_measurement": "W",
				"device_class":        "power",
				"state_class":         "measurement",
				"friendly_name":       "Sweep Series",
			},
		})
	}

	if err := inst.Backfill(ctx, hatest.Series{EntityID: rigHistory.EntityID, Samples: samples}); err != nil {
		return fmt.Errorf("backfilling %s: %w", rigHistory.EntityID, err)
	}
	return nil
}

// eachProfile runs a case against the rig and, when configured, the real
// instance — one body, two instances. That is the whole point of the tier: a
// claim proved against HA and kept honest by the rig cannot drift apart,
// because there is only one assertion.
func eachProfile(t *testing.T, run func(t *testing.T, tgt Target)) {
	t.Helper()

	t.Run(string(Rig), func(t *testing.T) {
		run(t, Target{Profile: Rig, Dir: rigHA.Dir(), Bin: hactlBin})
	})

	t.Run(string(Live), func(t *testing.T) {
		tgt, ok := LiveTarget(t, hactlBin)
		if !ok {
			t.Skip("set HACTL_LIVEFIRE_DIR to a configured instance to run the live profile")
		}
		run(t, tgt)
	})
}
