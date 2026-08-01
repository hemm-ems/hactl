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

// rigEmptyStateSeries is the shape behind finding #38: a history series in
// which the `state` key is present on every record and empty on some of them.
//
// The rig could not express it before. Every fixture entity holds a value, so
// the degeneracy guard's premise — that an empty state means the payload was
// empty, not the entity — was never contradicted here, and `ent hist` stayed
// green on the rig while exiting 1 with no stdout against a real house. The
// reference instance served 62 of 407 records this way over 400 days, on two
// unrelated entities, so it is a property of Home Assistant and not of one
// sensor.
//
// The values are categorical rather than numeric on purpose: this is what a
// template or pushed sensor looks like when it has not computed a category
// yet, which is where the shape was found. Consecutive samples never repeat —
// including across the blanks — because HA's significant_changes_only filter
// drops a repeat, and a dropped blank is the one sample the case needs.
var rigEmptyStateSeries = struct {
	EntityID string
	States   []string
	Step     time.Duration
}{
	EntityID: "sensor.sweep_category",
	States:   []string{"normal", "", "guenstig", "", "teuer", "", "normal"},
	Step:     10 * time.Minute,
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
	// The run-level collateral bracket. Every case can pass while the RUN as a
	// whole has reformatted automations.yaml or left an empty-id area behind —
	// both happened on 2026-07-30, and neither showed up in any individual
	// command's output, because collateral is a property of the instance rather
	// than of a command. So it is measured on the instance, once before the
	// first case and once after the last.
	before := censusOrExit(binDir)

	exit := m.Run()

	if problems := collateral(before); len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintf(os.Stderr, "COLLATERAL: %s\n", problem)
		}
		fmt.Fprintln(os.Stderr, "the sweep changed the instance outside the pg_ playground — see above")
		exit = 1
	}

	rigHA.Stop()
	// os.Exit skips deferred calls, so the build directory is removed here.
	_ = os.RemoveAll(binDir)
	os.Exit(exit)
}

// censusOrExit takes the pre-run census of every profile that is configured.
//
// A census that cannot be read is fatal rather than skipped. The whole value of
// the bracket is that "nothing moved" is a claim somebody checked; a run that
// quietly lost the ability to look would report a clean instance precisely when
// it could no longer see one — the shape H-14 exists to refuse, applied to the
// harness itself.
func censusOrExit(binDir string) map[Profile]Integrity {
	out := map[Profile]Integrity{}
	for profile, tgt := range censusTargets() {
		census, err := Census(tgt)
		if err != nil {
			rigHA.Stop()
			_ = os.RemoveAll(binDir)
			fmt.Fprintf(os.Stderr, "taking the %s census before the sweep: %v\n", profile, err)
			os.Exit(1)
		}
		out[profile] = census
	}
	return out
}

// collateral re-reads each census and reports what moved outside pg_*.
func collateral(before map[Profile]Integrity) []string {
	var problems []string
	for profile, tgt := range censusTargets() {
		start, taken := before[profile]
		if !taken {
			continue
		}
		after, err := Census(tgt)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: the census could not be re-read after the sweep: %v", profile, err))
			continue
		}
		for _, problem := range CompareIntegrity(start, after) {
			problems = append(problems, string(profile)+": "+problem)
		}
	}
	return problems
}

// censusTargets is the profiles the bracket covers.
//
// The rig is included as well as the live instance, and deliberately: the rig
// is where the destructive cases run, so it is the profile most likely to be
// damaged — and unlike the live profile it has no pg_ guard standing between a
// case and the whole instance.
func censusTargets() map[Profile]Target {
	out := map[Profile]Target{
		Rig: {Profile: Rig, Dir: rigHA.Dir(), Bin: hactlBin},
	}
	if dir := os.Getenv("HACTL_LIVEFIRE_DIR"); dir != "" {
		out[Live] = Target{Profile: Live, Dir: dir, Bin: hactlBin}
	}
	return out
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

	// The empty-state series shares this backfill so the container stops once.
	// It is anchored to the same end instant, far enough back that both series
	// sit inside any window a case asks for.
	blanks := make([]hatest.Sample, 0, len(rigEmptyStateSeries.States))
	for i, state := range rigEmptyStateSeries.States {
		at := end.Add(-time.Duration(len(rigEmptyStateSeries.States)-i) * rigEmptyStateSeries.Step)
		blanks = append(blanks, hatest.Sample{
			At:    at,
			State: state,
			Attrs: map[string]any{"friendly_name": "Sweep Category"},
		})
	}

	if err := inst.Backfill(ctx,
		hatest.Series{EntityID: rigHistory.EntityID, Samples: samples},
		hatest.Series{EntityID: rigEmptyStateSeries.EntityID, Samples: blanks},
	); err != nil {
		return fmt.Errorf("backfilling %s and %s: %w",
			rigHistory.EntityID, rigEmptyStateSeries.EntityID, err)
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
