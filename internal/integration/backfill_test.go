//go:build integration

package integration

// Recorder-backfill tests: the oracle for `ent anomalies` and long-window
// `ent hist` (H-15).
//
// Before this file, every anomalies test accepted `[]` as success. That is two
// escape mechanisms in one assertion: M1 (silent degradation — empty spelled
// "success") and M3 (liveness-only — "it ran" instead of "it is right"). A
// detector that finds nothing and a detector that is broken produced the same
// green. The cause was fixture-shaped, not laziness: a freshly booted HA has
// minutes of history, so no gap, stuck run or spike could exist to be found.
//
// hatest.Instance.Backfill writes real recorder rows for a history we author, so
// the input is known in advance. The assertions then have two independent legs:
//
//	Leg 1 (rig ⇄ HA) — the rig_lands_in_has_own_history subtest reads every
//	  backfilled series back out of *HA's own history API*, over plain HTTP with
//	  hactl nowhere in the path, and reconciles it against what we wrote. Until
//	  that passes, nothing below means anything: it is what makes the input a
//	  fact rather than an intention.
//
//	Leg 2 (hactl ⇄ known input) — the anomaly assertions. Their expected values
//	  are hand-stated, and that is legitimate here for exactly one reason: we
//	  authored the input, and leg 1 proved HA agrees with what we authored. The
//	  provenance is the plan below, not hactl's model of it. Deliberately NOT
//	  used as the expectation: anything computed by re-running hactl's own gap /
//	  stuck / z-score logic over the series, which would confirm the detector
//	  against itself.
//
// Long-window `ent hist` gets the same treatment, and its expectations are
// derived from HA's raw series at test time (count, min, max, span) rather than
// stated: bucketing is a pure function of data hactl does not own.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ---------------------------------------------------------------------------
// The authored input
// ---------------------------------------------------------------------------

const (
	// The series spans 26h at a 5-minute cadence: long enough to contain a 3h
	// gap and a 6h stuck run, and — at 312 samples — long enough that `ent hist`
	// must actually resample to reach its documented ~50 points.
	backfillSpan = 26 * time.Hour
	backfillStep = 5 * time.Minute
	backfillN    = int(backfillSpan / backfillStep) // 312

	// Queried window. Wider than the series on purpose: with no state before the
	// window start, HA's include_start_time_state has nothing to synthesise, so
	// what HA returns is exactly what we wrote and nothing else.
	backfillSince = "30h"

	// hactl's documented default (docs/manual.md: "~50 resampled datapoints").
	histResampleTarget = 50

	entClean    = "sensor.t7_clean"
	entGap      = "sensor.t7_gap"
	entStuck    = "sensor.t7_stuck"
	entSpike    = "sensor.t7_spike"
	entAttrOnly = "sensor.t7_attr_only"
)

// backfillPlan is the ground truth we author. Every field is a property of the
// rows we write, decided before HA or hactl sees them.
type backfillPlan struct {
	Now   time.Time
	Start time.Time

	// The 3h hole: no rows between these two samples, which are themselves the
	// last before and first after.
	GapFrom, GapTo time.Time

	// The 6h stuck run: first and last sample carrying StuckValue.
	StuckFrom, StuckTo time.Time
	StuckValue         float64

	// The single outlier.
	SpikeAt    time.Time
	SpikeValue float64

	Series []hatest.Series
}

var numericAttrs = map[string]any{
	"unit_of_measurement": "W",
	"device_class":        "power",
	"state_class":         "measurement",
}

// baseValue is a smooth 24h sine with a 3.7h wobble on top, range ~[86, 114].
// The wobble is what keeps consecutive samples distinct: a pure 24h sine flattens
// near its turning points, and a run of equal values there would plant a stuck
// anomaly in the negative control.
func baseValue(hoursIn float64) float64 {
	v := 100 + 12*math.Sin(2*math.Pi*hoursIn/24) + 2*math.Sin(2*math.Pi*hoursIn/3.7)
	return math.Round(v*100) / 100
}

func fmtVal(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// changingFmt renders sample values so that no two consecutive samples of a
// series carry the same state string.
//
// This is not cosmetic. HA never records an unchanged value as a state change,
// and its history API filters attribute-only rows out by default — so a series
// containing accidental repeats does not round-trip through HA, and the first
// run of these tests proved it: the smooth base curve happened to round to the
// same two-decimal value six times at its turning points, and HA returned 306 of
// the 312 rows written. A negative control that quietly loses samples is exactly
// the kind of fixture nobody notices is wrong.
type changingFmt struct{ prev string }

// next renders v, nudging it by hundredths until it differs from the previous
// sample and from every string in avoid.
func (f *changingFmt) next(v float64, avoid ...string) string {
	s := fmtVal(v)
	for {
		clash := s == f.prev
		for _, a := range avoid {
			clash = clash || s == a
		}
		if !clash {
			break
		}
		v += 0.01
		s = fmtVal(math.Round(v*100) / 100)
	}
	f.prev = s
	return s
}

// emit records a value chosen elsewhere, keeping the anti-repeat state honest.
func (f *changingFmt) emit(s string) string { f.prev = s; return s }

// buildBackfillPlan authors four series with known-in-advance properties, plus a
// fifth that probes an HA behaviour the others depend on.
//
//	t7_clean     NEGATIVE CONTROL — dense, always moving, no hole, no outlier.
//	             The detector must report nothing, and must do so about ~312 real
//	             samples rather than about an empty result.
//	t7_gap       the same shape with one 3h hole.
//	t7_stuck     the same shape, but for 6h the sensor's integration keeps
//	             dropping out: it alternates the SAME value with `unavailable`.
//	             That is the shape a real HA produces — every row is a genuine
//	             state change, so every row survives HA's significant-changes
//	             filter — and once hactl drops the non-numeric samples what is
//	             left is a value that has not moved in 6h. Writing 72 identical
//	             numeric rows instead would be fiction: HA never records an
//	             unchanged value as a change.
//	t7_spike     flat ~200 with one sample at 2000.
//	t7_attr_only constant value, attributes changing each row. HA's history API
//	             filters these by default; this series is what proves it, and is
//	             the reason t7_stuck is shaped the way it is.
func buildBackfillPlan(now time.Time) backfillPlan {
	now = now.UTC().Truncate(time.Second)
	start := now.Add(-backfillSpan)
	at := func(i int) time.Time { return start.Add(time.Duration(i) * backfillStep) }
	hoursIn := func(i int) float64 { return float64(i) * backfillStep.Hours() }

	p := backfillPlan{Now: now, Start: start}

	// --- negative control ---------------------------------------------------
	cleanFmt := &changingFmt{}
	clean := make([]hatest.Sample, 0, backfillN)
	for i := range backfillN {
		clean = append(clean, hatest.Sample{At: at(i), State: cleanFmt.next(baseValue(hoursIn(i))), Attrs: numericAttrs})
	}

	// --- one 3h hole --------------------------------------------------------
	// Indices are exclusive of the endpoints, so the surviving neighbours are
	// exactly 36 steps = 3h apart.
	const gapFromIdx, gapSteps = 120, 36
	gapFmt := &changingFmt{}
	gap := make([]hatest.Sample, 0, backfillN)
	for i := range backfillN {
		if i > gapFromIdx && i < gapFromIdx+gapSteps {
			continue
		}
		gap = append(gap, hatest.Sample{At: at(i), State: gapFmt.next(baseValue(hoursIn(i))), Attrs: numericAttrs})
	}
	p.GapFrom, p.GapTo = at(gapFromIdx), at(gapFromIdx+gapSteps)

	// --- 6h stuck at a value close to the series mean ------------------------
	// 88.88 sits well inside the baseline's range, so the stuck run cannot also
	// register as a spike: the two detectors must be independently satisfiable.
	// The baseline is kept away from that exact value so the run has crisp edges
	// — a base sample that happened to render as 88.88 would silently extend it.
	const stuckFromIdx, stuckSteps = 96, 72
	p.StuckValue = 88.88
	stuckStr := fmtVal(p.StuckValue)
	stuckFmt := &changingFmt{}
	stuck := make([]hatest.Sample, 0, backfillN)
	for i := range backfillN {
		s := hatest.Sample{At: at(i), Attrs: numericAttrs}
		switch {
		case i < stuckFromIdx || i > stuckFromIdx+stuckSteps:
			s.State = stuckFmt.next(baseValue(hoursIn(i)), stuckStr)
		case (i-stuckFromIdx)%2 == 0:
			s.State = stuckFmt.emit(stuckStr)
		default:
			s.State = stuckFmt.emit("unavailable")
			s.Attrs = map[string]any{"device_class": "power"}
		}
		stuck = append(stuck, s)
	}
	p.StuckFrom, p.StuckTo = at(stuckFromIdx), at(stuckFromIdx+stuckSteps)

	// --- one outlier --------------------------------------------------------
	const spikeIdx = 240
	p.SpikeValue = 2000
	spikeFmt := &changingFmt{}
	spike := make([]hatest.Sample, 0, backfillN)
	for i := range backfillN {
		v := math.Round((200+2*math.Sin(2*math.Pi*hoursIn(i)/5))*100) / 100
		state := spikeFmt.next(v)
		if i == spikeIdx {
			state = spikeFmt.emit(fmtVal(p.SpikeValue))
		}
		spike = append(spike, hatest.Sample{At: at(i), State: state, Attrs: numericAttrs})
	}
	p.SpikeAt = at(spikeIdx)

	// --- attribute-only updates ---------------------------------------------
	attrOnly := make([]hatest.Sample, 0, 12)
	for i := range 12 {
		attrOnly = append(attrOnly, hatest.Sample{
			At:    now.Add(-2*time.Hour + time.Duration(i)*backfillStep),
			State: "77.7",
			Attrs: map[string]any{"unit_of_measurement": "W", "cycle": i},
		})
	}

	p.Series = []hatest.Series{
		{EntityID: entClean, Samples: clean},
		{EntityID: entGap, Samples: gap},
		{EntityID: entStuck, Samples: stuck},
		{EntityID: entSpike, Samples: spike},
		{EntityID: entAttrOnly, Samples: attrOnly},
	}
	return p
}

// assertPlanIsDistinguishing refuses to run the suite against a fixture that has
// stopped containing what the assertions are about (TC-4).
//
// This checks the authored input against itself — it is not the oracle, and none
// of its results are used as an expected value. Its job is to stop a later edit
// (a changed cadence, a moved index, a value collision) from turning these tests
// green for the wrong reason. Without it the constants below are load-bearing and
// unguarded, which is how a fixture silently stops distinguishing.
func assertPlanIsDistinguishing(t *testing.T, plan backfillPlan) {
	t.Helper()
	byID := map[string]hatest.Series{}
	for _, s := range plan.Series {
		byID[s.EntityID] = s
	}
	for _, id := range []string{entClean, entGap, entStuck, entSpike} {
		assertSamplesAlwaysChange(t, id, byID[id])
	}
	assertPlanHasOneHole(t, plan, byID[entGap])
	assertPlanHasOneStuckRun(t, plan, byID[entStuck])
	assertPlanHasOneOutlier(t, plan, byID[entSpike])
}

// assertSamplesAlwaysChange enforces the property every series in this file
// depends on: HA does not record an unchanged value as a change, so a repeat
// would not survive the round trip through its history API.
func assertSamplesAlwaysChange(t *testing.T, id string, s hatest.Series) {
	t.Helper()
	for i := 1; i < len(s.Samples); i++ {
		if s.Samples[i].State == s.Samples[i-1].State {
			t.Fatalf("plan %s: samples %d and %d both read %q — HA does not record an unchanged value "+
				"as a change, so this row would not survive the round trip", id, i-1, i, s.Samples[i].State)
		}
		if !s.Samples[i].At.After(s.Samples[i-1].At) {
			t.Fatalf("plan %s: sample %d is not after %d", id, i, i-1)
		}
	}
}

// assertPlanHasOneHole — the gap series must contain exactly one hole, of
// exactly the size the gap assertion names.
func assertPlanHasOneHole(t *testing.T, plan backfillPlan, s hatest.Series) {
	t.Helper()
	holes := 0
	for i := 1; i < len(s.Samples); i++ {
		d := s.Samples[i].At.Sub(s.Samples[i-1].At)
		if d <= backfillStep {
			continue
		}
		holes++
		if d != 3*time.Hour || !s.Samples[i-1].At.Equal(plan.GapFrom) || !s.Samples[i].At.Equal(plan.GapTo) {
			t.Fatalf("plan %s: hole %s → %s lasts %s, want the planned %s → %s of 3h",
				entGap, s.Samples[i-1].At, s.Samples[i].At, d, plan.GapFrom, plan.GapTo)
		}
	}
	if holes != 1 {
		t.Fatalf("plan %s: %d holes, want exactly 1", entGap, holes)
	}
}

// assertPlanHasOneStuckRun — the stuck value must appear only inside the planned
// run, and must span it.
func assertPlanHasOneStuckRun(t *testing.T, plan backfillPlan, s hatest.Series) {
	t.Helper()
	want := fmtVal(plan.StuckValue)
	var first, last time.Time
	for _, sample := range s.Samples {
		if sample.State != want {
			continue
		}
		if first.IsZero() {
			first = sample.At
		}
		last = sample.At
	}
	if !first.Equal(plan.StuckFrom) || !last.Equal(plan.StuckTo) || last.Sub(first) != 6*time.Hour {
		t.Fatalf("plan %s: value %s runs %s → %s (%s), want the planned %s → %s of 6h",
			entStuck, want, first, last, last.Sub(first), plan.StuckFrom, plan.StuckTo)
	}
}

// assertPlanHasOneOutlier — exactly one outlier, and it is the one the spike
// assertion names.
func assertPlanHasOneOutlier(t *testing.T, plan backfillPlan, s hatest.Series) {
	t.Helper()
	outliers := 0
	for _, sample := range s.Samples {
		v, err := strconv.ParseFloat(sample.State, 64)
		if err != nil || v <= 1000 {
			continue
		}
		outliers++
		if !sample.At.Equal(plan.SpikeAt) {
			t.Fatalf("plan %s: outlier at %s, want %s", entSpike, sample.At, plan.SpikeAt)
		}
	}
	if outliers != 1 {
		t.Fatalf("plan %s: %d outliers, want exactly 1", entSpike, outliers)
	}
}

// TestRecorderBackfill owns the whole rig: one dedicated HA container, one
// backfill, then every assertion as a subtest.
//
// The container is dedicated because Backfill stops and restarts it, and Docker
// re-assigns the published port on restart — an instance other tests hold a URL
// for cannot be used. It is a plain hatest.Start rather than one of the package's
// sync.Once-shared instances so that its t.Cleanup frees it the moment these
// subtests finish, instead of holding ~400 MB until TestMain returns: the Docker
// VM is the scarce resource, four other HA containers may already be up, and a
// prior session hit repeated startup timeouts at five.
//
// Subtest order matters. rig_lands_in_has_own_history runs first because every
// assertion after it is conditional on the backfill being real.
func TestRecorderBackfill(t *testing.T) {
	plan := buildBackfillPlan(time.Now())
	assertPlanIsDistinguishing(t, plan)

	inst := hatest.Start(t, hatest.WithFixture("basic"))
	if err := inst.Backfill(context.Background(), plan.Series...); err != nil {
		t.Fatalf("recorder backfill: %v", err)
	}

	t.Run("rig_lands_in_has_own_history", func(t *testing.T) { assertBackfillLanded(t, inst, plan) })
	t.Run("anomalies_finds_injected_gap", func(t *testing.T) { assertInjectedGapFound(t, inst, plan) })
	t.Run("anomalies_finds_injected_stuck_run", func(t *testing.T) { assertInjectedStuckRunFound(t, inst, plan) })
	t.Run("anomalies_finds_injected_spike", func(t *testing.T) { assertInjectedSpikeFound(t, inst, plan) })
	t.Run("anomalies_negative_control_is_quiet", func(t *testing.T) { assertNegativeControlIsQuiet(t, inst, plan) })
	t.Run("hist_long_window_buckets", func(t *testing.T) { assertLongWindowBucketing(t, inst, plan) })
	t.Run("hist_long_window_drops_empty_buckets", func(t *testing.T) { assertLongWindowDropsEmptyBuckets(t, inst, plan) })
}

// ---------------------------------------------------------------------------
// Leg 1 — the rig must be real: HA's own history has to agree with what we wrote
// ---------------------------------------------------------------------------

// haStateRow is one record as HA's history API renders it.
type haStateRow struct {
	EntityID    string `json:"entity_id"`
	State       string `json:"state"`
	LastChanged string `json:"last_changed"`
	LastUpdated string `json:"last_updated"`
}

// haHistory asks HA directly. Deliberately plain net/http rather than
// internal/haapi: this is the ground truth hactl is measured against, so hactl's
// own request construction and decoding must not be in the path.
func haHistory(t *testing.T, inst *hatest.Instance, entityID string, start, end time.Time) []haStateRow {
	t.Helper()
	q := url.Values{}
	q.Set("filter_entity_id", entityID)
	q.Set("end_time", end.Format(time.RFC3339))
	endpoint := inst.URL() + "/api/history/period/" + url.PathEscape(start.Format(time.RFC3339)) + "?" + q.Encode()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("building history request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+inst.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET history for %s: %v", entityID, err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading history body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET history for %s: HTTP %d: %s", entityID, resp.StatusCode, body)
	}
	var outer [][]haStateRow
	if err := json.Unmarshal(body, &outer); err != nil {
		t.Fatalf("decoding history for %s: %v\n%s", entityID, err, body)
	}
	if len(outer) == 0 {
		return nil
	}
	return outer[0]
}

func (r haStateRow) changedAt(t *testing.T) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, r.LastChanged)
	if err != nil {
		t.Fatalf("history row has unparseable last_changed %q: %v", r.LastChanged, err)
	}
	return ts.UTC()
}

// assertBackfillLanded backs TestRecorderBackfill/rig_lands_in_has_own_history,
// the precondition for every other subtest in this file. It proves the rig writes rows HA accepts as its own: same
// count, same timestamps, same states, read back through HA's history API.
//
// It also pins the one HA behaviour the rig's row shape depends on — that
// attribute-only rows are filtered out of history by default — because if that
// ever stops being true, the stuck series is shaped for a filter that no longer
// exists and someone should find out from a failing test, not from a wrong answer.
func assertBackfillLanded(t *testing.T, inst *hatest.Instance, plan backfillPlan) {
	t.Helper()
	winStart := plan.Now.Add(-30 * time.Hour)
	winEnd := plan.Now.Add(time.Hour)

	for _, s := range plan.Series {
		if s.EntityID == entAttrOnly {
			continue // asserted separately below: HA filters it by design
		}
		rows := haHistory(t, inst, s.EntityID, winStart, winEnd)
		if len(rows) != len(s.Samples) {
			t.Errorf("%s: HA's history returned %d rows, we wrote %d — the backfill did not land as written",
				s.EntityID, len(rows), len(s.Samples))
			continue
		}
		for i, want := range s.Samples {
			got := rows[i]
			if got.State != want.State {
				t.Errorf("%s row %d: HA reports state %q, we wrote %q", s.EntityID, i, got.State, want.State)
			}
			if delta := got.changedAt(t).Sub(want.At); delta > time.Millisecond || delta < -time.Millisecond {
				t.Errorf("%s row %d: HA reports last_changed %s, we wrote %s (delta %s)",
					s.EntityID, i, got.changedAt(t), want.At, delta)
			}
		}
	}

	// The attribute-only series: 12 rows written, one state value throughout.
	// HA's history defaults to significant_changes_only, whose SQL keeps a row
	// only when last_changed_ts is NULL or equal to last_updated_ts — true for a
	// real state change, false for an attributes-only update. So HA must return
	// the first row and drop the other eleven.
	attrRows := haHistory(t, inst, entAttrOnly, winStart, winEnd)
	if len(attrRows) != 1 {
		t.Errorf("%s: HA returned %d rows for 12 attribute-only updates, want 1 — "+
			"HA's significant-changes filter no longer behaves as the rig assumes, "+
			"which invalidates the row shape of the stuck series", entAttrOnly, len(attrRows))
	}
}

// ---------------------------------------------------------------------------
// Leg 2 — the detector, against history whose anomalies are known in advance
// ---------------------------------------------------------------------------

type anomalyRow struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Time   string `json:"time"`
	Detail string `json:"detail"`
}

func entAnomalies(t *testing.T, inst *hatest.Instance, entityID string) []anomalyRow {
	t.Helper()
	raw := runHactlDir(t, inst.Dir(), "ent", "anomalies", entityID, "--since", backfillSince, "--json")
	var rows []anomalyRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("ent anomalies %s --json did not parse: %v\n%s", entityID, err, raw)
	}
	return rows
}

// shortTimeForms renders a timestamp the two ways hactl's table can show it.
// Which one it picks depends on whether the sample falls on today's date, a
// decision hactl makes against the wall clock at render time; accepting either
// keeps a midnight rollover between hactl's call and this assertion from being a
// flake, while still pinning the timestamp to the minute.
func shortTimeForms(ts time.Time) []string {
	return []string{ts.Format("15:04"), ts.Format("01-02 15:04")}
}

func assertRenderedTime(t *testing.T, what string, got string, want time.Time) {
	t.Helper()
	if slices.Contains(shortTimeForms(want), got) {
		return
	}
	t.Errorf("%s: reported at %q, want %s (rendered %q or %q)",
		what, got, want.Format(time.RFC3339), shortTimeForms(want)[0], shortTimeForms(want)[1])
}

func rowsOfType(rows []anomalyRow, typ string) []anomalyRow {
	out := make([]anomalyRow, 0, len(rows))
	for _, r := range rows {
		if r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

// assertInjectedGapFound backs TestRecorderBackfill/anomalies_finds_injected_gap:
// 3h with no rows, between two samples we named. Expected boundaries come from the plan, not from re-running a
// gap-finder over the series.
func assertInjectedGapFound(t *testing.T, inst *hatest.Instance, plan backfillPlan) {
	t.Helper()
	rows := entAnomalies(t, inst, entGap)

	gaps := rowsOfType(rows, "gap")
	if len(gaps) != 1 {
		t.Fatalf("ent anomalies %s: found %d gaps, want exactly 1 (a single 3h hole was injected into an "+
			"otherwise 5-minute-dense 26h series)\nall rows: %+v", entGap, len(gaps), rows)
	}
	assertRenderedTime(t, "injected gap", gaps[0].Time, plan.GapFrom)
	if want := "no data for 3h0m0s"; !strings.Contains(gaps[0].Detail, want) {
		t.Errorf("gap detail = %q, want it to report %q (the hole runs %s → %s)",
			gaps[0].Detail, want, plan.GapFrom.Format(time.RFC3339), plan.GapTo.Format(time.RFC3339))
	}
	// The hole is the only thing wrong with this series.
	for _, r := range rows {
		if r.Type != "gap" {
			t.Errorf("ent anomalies %s reported an unexpected %s anomaly: %+v", entGap, r.Type, r)
		}
	}
}

// assertInjectedStuckRunFound backs
// TestRecorderBackfill/anomalies_finds_injected_stuck_run: 6h during which the
// sensor's value never moves. See buildBackfillPlan for why the run is written as a value
// alternating with `unavailable` rather than as repeated identical rows.
func assertInjectedStuckRunFound(t *testing.T, inst *hatest.Instance, plan backfillPlan) {
	t.Helper()
	rows := entAnomalies(t, inst, entStuck)

	stuck := rowsOfType(rows, "stuck")
	if len(stuck) != 1 {
		t.Fatalf("ent anomalies %s: found %d stuck runs, want exactly 1 (the value is frozen at %.2f "+
			"from %s to %s and moves every 5 minutes otherwise)\nall rows: %+v",
			entStuck, len(stuck), plan.StuckValue,
			plan.StuckFrom.Format(time.RFC3339), plan.StuckTo.Format(time.RFC3339), rows)
	}
	assertRenderedTime(t, "injected stuck run", stuck[0].Time, plan.StuckFrom)
	want := fmt.Sprintf("stuck at %.2f for 6h0m0s", plan.StuckValue)
	if !strings.Contains(stuck[0].Detail, want) {
		t.Errorf("stuck detail = %q, want it to contain %q", stuck[0].Detail, want)
	}
	for _, r := range rows {
		if r.Type != "stuck" {
			t.Errorf("ent anomalies %s reported an unexpected %s anomaly: %+v", entStuck, r.Type, r)
		}
	}
}

// assertInjectedSpikeFound backs
// TestRecorderBackfill/anomalies_finds_injected_spike: one sample at 2000 in a
// series that otherwise sits at 200±2.
func assertInjectedSpikeFound(t *testing.T, inst *hatest.Instance, plan backfillPlan) {
	t.Helper()
	rows := entAnomalies(t, inst, entSpike)

	spikes := rowsOfType(rows, "spike")
	if len(spikes) != 1 {
		t.Fatalf("ent anomalies %s: found %d spikes, want exactly 1 (a single %.0f sample at %s in a "+
			"series that otherwise stays within 200±2)\nall rows: %+v",
			entSpike, len(spikes), plan.SpikeValue, plan.SpikeAt.Format(time.RFC3339), rows)
	}
	assertRenderedTime(t, "injected spike", spikes[0].Time, plan.SpikeAt)
	if want := fmt.Sprintf("value=%.2f", plan.SpikeValue); !strings.Contains(spikes[0].Detail, want) {
		t.Errorf("spike detail = %q, want it to contain %q", spikes[0].Detail, want)
	}
	for _, r := range rows {
		if r.Type != "spike" {
			t.Errorf("ent anomalies %s reported an unexpected %s anomaly: %+v", entSpike, r.Type, r)
		}
	}
}

// assertNegativeControlIsQuiet backs
// TestRecorderBackfill/anomalies_negative_control_is_quiet, the half that makes
// the three subtests above mean something. A detector that flags everything passes them all; this one it
// cannot pass. And an empty answer only counts as a negative when there was
// something to look at, so the emptiness is asserted together with the size of
// the series it is about — both taken from HA, not from hactl.
func assertNegativeControlIsQuiet(t *testing.T, inst *hatest.Instance, plan backfillPlan) {
	t.Helper()
	haRows := haHistory(t, inst, entClean, plan.Now.Add(-30*time.Hour), plan.Now.Add(time.Hour))
	if len(haRows) < backfillN {
		t.Fatalf("precondition: HA holds only %d rows for the control entity %s, want %d — "+
			"an empty anomaly result would be vacuous", len(haRows), entClean, backfillN)
	}

	rows := entAnomalies(t, inst, entClean)
	if len(rows) != 0 {
		t.Errorf("ent anomalies %s reported %d anomalies over %d well-behaved samples, want none:\n%+v",
			entClean, len(rows), len(haRows), rows)
	}
}

// ---------------------------------------------------------------------------
// Long-window `ent hist`
// ---------------------------------------------------------------------------

type histRow struct {
	Time  string `json:"time"`
	Value string `json:"value"`
}

func entHist(t *testing.T, inst *hatest.Instance, entityID string, extra ...string) []histRow {
	t.Helper()
	args := append([]string{"ent", "hist", entityID, "--since", backfillSince}, extra...)
	raw := runHactlDir(t, inst.Dir(), append(args, "--json")...)
	var rows []histRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("ent hist %s --json did not parse: %v\n%s", entityID, err, raw)
	}
	return rows
}

func (r histRow) value(t *testing.T) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(r.Value, 64)
	if err != nil {
		t.Fatalf("ent hist rendered a non-numeric value %q: %v", r.Value, err)
	}
	return v
}

// numericStats are the facts about HA's own series that a resampler cannot
// change, and from which every `ent hist` expectation in this file is derived.
type numericStats struct {
	N           int
	Min         float64
	Max         float64
	Mean        float64
	First, Last time.Time
}

// haNumericStats reduces HA's raw answer to those facts.
func haNumericStats(t *testing.T, rows []haStateRow) numericStats {
	t.Helper()
	st := numericStats{Min: math.Inf(1), Max: math.Inf(-1)}
	sum := 0.0
	for _, r := range rows {
		v, err := strconv.ParseFloat(r.State, 64)
		if err != nil {
			continue // `unavailable` and friends are not samples
		}
		ts := r.changedAt(t)
		if st.N == 0 {
			st.First = ts
		}
		st.Last = ts
		st.N++
		sum += v
		st.Min = math.Min(st.Min, v)
		st.Max = math.Max(st.Max, v)
	}
	if st.N > 0 {
		st.Mean = sum / float64(st.N)
	}
	return st
}

// assertLongWindowBucketing backs TestRecorderBackfill/hist_long_window_buckets.
// It pins the aggregation that only becomes visible once the window is long
// enough to force it. Every expectation
// is computed from HA's raw series at test time.
func assertLongWindowBucketing(t *testing.T, inst *hatest.Instance, plan backfillPlan) {
	t.Helper()
	haRows := haHistory(t, inst, entClean, plan.Now.Add(-30*time.Hour), plan.Now.Add(time.Hour))
	raw := haNumericStats(t, haRows)

	if raw.N <= histResampleTarget {
		t.Fatalf("precondition: HA holds only %d samples, which is not more than the %d-point resample "+
			"target — this test would prove nothing about bucketing", raw.N, histResampleTarget)
	}

	rows := entHist(t, inst, entClean)

	// 1. It resampled, and to the number the manual promises.
	if len(rows) != histResampleTarget {
		t.Errorf("ent hist %s over %d raw samples rendered %d points, want %d "+
			"(docs/manual.md: \"~50 resampled datapoints\")", entClean, raw.N, len(rows), histResampleTarget)
	}

	// 2. Averaging within a bucket cannot leave the data's range, and — for a
	//    series that genuinely moves — must strictly shrink it. A renderer that
	//    skipped points instead of averaging them, or that mis-assigned points to
	//    buckets, fails here; a count check alone would not notice either.
	for i, r := range rows {
		v := r.value(t)
		if v < raw.Min || v > raw.Max {
			t.Errorf("ent hist %s point %d = %.2f is outside HA's own range [%.2f, %.2f]",
				entClean, i, v, raw.Min, raw.Max)
		}
	}
	gotMin, gotMax, gotSum := math.Inf(1), math.Inf(-1), 0.0
	for _, r := range rows {
		v := r.value(t)
		gotMin, gotMax = math.Min(gotMin, v), math.Max(gotMax, v)
		gotSum += v
	}
	if gotMax >= raw.Max || gotMin <= raw.Min {
		t.Errorf("ent hist %s rendered range [%.2f, %.2f] does not sit strictly inside HA's [%.2f, %.2f] — "+
			"bucket averages of a moving series must be less extreme than its extremes",
			entClean, gotMin, gotMax, raw.Min, raw.Max)
	}

	// 3. Averaging preserves the level. Buckets here are near-equally populated
	//    (the samples are on a uniform 5-minute grid), so the mean of the bucket
	//    means must track HA's own mean closely. Dropped or duplicated buckets
	//    move it.
	gotMean := gotSum / float64(len(rows))
	if math.Abs(gotMean-raw.Mean) > 0.5 {
		t.Errorf("ent hist %s mean of rendered points = %.3f, HA's own mean = %.3f (difference %.3f > 0.5)",
			entClean, gotMean, raw.Mean, math.Abs(gotMean-raw.Mean))
	}

	// 4. --resample honours the bucket width against HA's actual span, rather
	//    than the nominal --since window.
	span := raw.Last.Sub(raw.First)
	wantBuckets := int(span / time.Hour)
	hourly := entHist(t, inst, entClean, "--resample", "1h")
	if len(hourly) != wantBuckets {
		t.Errorf("ent hist %s --resample 1h rendered %d points; HA's series spans %s, so %d one-hour buckets",
			entClean, len(hourly), span.Truncate(time.Minute), wantBuckets)
	}
}

// assertLongWindowDropsEmptyBuckets backs
// TestRecorderBackfill/hist_long_window_drops_empty_buckets. It is the other half
// of the bucketing contract, and it is only observable over a window long enough to contain a
// hole: buckets with no samples are omitted, not rendered as a zero. Emitting
// 0.00 for an empty bucket would invent a reading the recorder never held — and
// would also feed the spike detector an outlier nobody measured.
func assertLongWindowDropsEmptyBuckets(t *testing.T, inst *hatest.Instance, plan backfillPlan) {
	t.Helper()
	haRows := haHistory(t, inst, entGap, plan.Now.Add(-30*time.Hour), plan.Now.Add(time.Hour))
	raw := haNumericStats(t, haRows)
	if raw.N <= histResampleTarget {
		t.Fatalf("precondition: HA holds only %d samples for %s", raw.N, entGap)
	}

	rows := entHist(t, inst, entGap)
	if len(rows) >= histResampleTarget {
		t.Errorf("ent hist %s rendered %d points over a series with a 3h hole; buckets falling entirely "+
			"inside the hole hold no samples and must be dropped, so it cannot reach the %d-point target",
			entGap, len(rows), histResampleTarget)
	}

	// No rendered point may sit in the interior of the hole. The margin is one
	// bucket width on each side, because the buckets that straddle the hole's
	// edges legitimately carry samples from outside it.
	bucket := raw.Last.Sub(raw.First) / time.Duration(histResampleTarget)
	from, to := plan.GapFrom.Add(bucket), plan.GapTo.Add(-bucket)
	forbidden := map[string]bool{}
	for ts := from; !ts.After(to); ts = ts.Add(time.Minute) {
		for _, form := range shortTimeForms(ts) {
			forbidden[form] = true
		}
	}
	for i, r := range rows {
		if forbidden[r.Time] {
			t.Errorf("ent hist %s point %d is at %s, inside the injected %s → %s hole where the recorder "+
				"holds nothing", entGap, i, r.Time,
				plan.GapFrom.Format(time.RFC3339), plan.GapTo.Format(time.RFC3339))
		}
	}
}
