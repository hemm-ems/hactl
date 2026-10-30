package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hemm-ems/hactl/internal/clock"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// Long-term statistics on `ent hist` (D-47, SPEC-ent-stats.md).
//
// `ent hist` reads the recorder's raw state history, which HA purges after
// purge_keep_days; the hourly aggregates HA keeps forever were unreachable, so
// `ent hist --since 90d` answered with ten days and said nothing about the
// other eighty. Statistics are the same entity's history at a coarser grain,
// so they are a flag on the same command rather than a family of their own —
// and an explicit one: the command never switches source on its own, because
// one command line answering raw states or hourly means depending on the
// window is a contract nobody can read.

var (
	flagEntStats  bool
	flagEntPeriod string
)

func init() {
	entHistCmd.Flags().BoolVar(&flagEntStats, "stats", false,
		"read long-term statistics (hourly aggregates HA keeps after the raw history is purged)")
	entHistCmd.Flags().StringVar(&flagEntPeriod, "period", "",
		"statistics period with --stats: "+strings.Join(haapi.StatisticPeriods, ", ")+" (default: auto, ~50 rows)")
	entHistCmd.MarkFlagsMutuallyExclusive("stats", "attr")
	entHistCmd.MarkFlagsMutuallyExclusive("stats", "resample")
}

// autoStatisticPeriods are the periods the auto choice walks, finest first.
// 5minute and year are explicit-only: 5minute reads short-term statistics,
// which HA purges like the raw history, and year collapses any window a
// caller is likely to ask about into one or two rows.
var autoStatisticPeriods = []struct {
	name string
	span time.Duration
}{
	{"hour", time.Hour},
	{"day", 24 * time.Hour},
	{"week", 7 * 24 * time.Hour},
}

// autoStatisticPeriod picks the finest period giving at most
// defaultResampleTarget rows over the window — the statistics analogue of
// `ent hist`'s ~50-point resample. Anything longer than 50 weeks is monthly.
func autoStatisticPeriod(window time.Duration) string {
	for _, p := range autoStatisticPeriods {
		if window <= time.Duration(defaultResampleTarget)*p.span {
			return p.name
		}
	}
	return "month"
}

// checkEntHistFlags refuses the flag combinations the statistics path cannot
// honour (H-25). --stats against --attr/--resample is cobra's mutual
// exclusion; --period without --stats is checked here because the reverse
// combination is legal.
func checkEntHistFlags(entityID string) error {
	if flagEntPeriod != "" && !flagEntStats {
		return &flagContractError{msg: "--period applies to long-term statistics; add --stats " +
			"(or use --resample to bucket the raw history)"}
	}
	if flagEntPeriod != "" && !slices.Contains(haapi.StatisticPeriods, flagEntPeriod) {
		return &flagContractError{msg: fmt.Sprintf("invalid --period %q: one of %s",
			flagEntPeriod, strings.Join(haapi.StatisticPeriods, ", "))}
	}
	if !flagEntStats && isExternalStatisticID(entityID) {
		return fmt.Errorf("%q is an external statistic id, not an entity — it has statistics only; "+
			"read it with `ent hist %s --stats`", entityID, entityID)
	}
	return nil
}

// isExternalStatisticID reports a `source:name` statistic id — what an
// integration such as Tibber imports, and what `energy show` prints for it.
// Entity ids never contain a colon.
func isExternalStatisticID(id string) bool {
	return strings.Contains(id, ":")
}

func runEntHistStats(ctx context.Context, w, errW io.Writer, cfg *config.Config, id string, start, end time.Time, window time.Duration) error {
	period := flagEntPeriod
	if period == "" {
		period = autoStatisticPeriod(window)
	}
	// Statistic ids are lowercase by HA's own grammar (valid_entity_id,
	// valid_statistic_id), and HA's state API lowercases what it is asked
	// for. The statistics API does not: `SENSOR.X --stats` found the entity
	// and then reported "no long-term statistics" for a sensor that has years
	// of them (live run 2026-10-29).
	id = strings.ToLower(id)

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return fmt.Errorf("connecting to HA: %w", err)
	}
	defer func() { _ = ws.Close() }()

	meta, err := statisticMetadata(ctx, ws, id)
	if errors.Is(err, errNoStatisticMetadata) {
		return errNoStatistic(ctx, haapi.New(cfg.URL, cfg.Token), ws, id)
	}
	if err != nil {
		return err
	}

	rows, err := ws.StatisticsDuringPeriod(ctx, id, start, end, period)
	if err != nil {
		return fmt.Errorf("fetching statistics: %w", err)
	}
	if len(rows) == 0 {
		// The statistic exists (the metadata said so), so an empty answer is
		// a fact about this window, not a missing statistic (H-7) — and the
		// question the caller has next is where the rows ARE. A statistic of
		// a deleted entity keeps its rows forever; on the live run one ended
		// in 2022, and "nothing in the last 365d" was all it said.
		if err := emitEmptyList(w, fmt.Sprintf("%s: no long-term statistics in the last %s (period %s)",
			id, sinceWindow(), period)); err != nil {
			return err
		}
		out := w
		if flagJSON {
			out = errW
		}
		_, _ = fmt.Fprintln(out, newestStatisticHint(ctx, ws, id, end))
		return nil
	}
	return renderStatisticRows(w, id, period, meta, rows)
}

// newestStatistic returns the end of a statistic's newest hourly row, or zero
// when it has no rows at all. Two bounded requests: the monthly rows of the
// whole history find the newest month, the hourly rows of that month the
// newest hour.
func newestStatistic(ctx context.Context, ws *haapi.WSClient, id string, now time.Time) (time.Time, error) {
	months, err := ws.StatisticsDuringPeriod(ctx, id, time.Unix(0, 0), now, "month")
	if err != nil || len(months) == 0 {
		return time.Time{}, err
	}
	monthStart, err := months[len(months)-1].StartTime()
	if err != nil {
		return time.Time{}, err
	}
	hours, err := ws.StatisticsDuringPeriod(ctx, id, monthStart, now, "hour")
	if err != nil || len(hours) == 0 {
		return time.Time{}, err
	}
	return hours[len(hours)-1].EndTime()
}

// sinceCovering is the smallest whole-day --since whose window reaches back
// past t.
func sinceCovering(t, now time.Time) string {
	return strconv.Itoa(int(now.Sub(t).Hours()/24)+1) + "d"
}

// newestStatisticHint is the line that follows an empty statistics answer.
func newestStatisticHint(ctx context.Context, ws *haapi.WSClient, id string, now time.Time) string {
	newest, err := newestStatistic(ctx, ws, id, now)
	switch {
	case err != nil:
		return fmt.Sprintf("note: could not look up the newest row (%v)", err)
	case newest.IsZero():
		return "note: the statistic exists but holds no rows at all"
	}
	return fmt.Sprintf("note: the newest row ends %s — widen the window: hactl ent hist %s --stats --since %s",
		clock.Long(newest.Format(time.RFC3339Nano)), id, sinceCovering(newest.Add(-time.Hour), now))
}

// errNoStatisticMetadata is statisticMetadata's answer when HA keeps no
// statistic under the id — an answer, not a failure, which callers tell apart
// with errors.Is.
var errNoStatisticMetadata = errors.New("no long-term statistic")

// statisticMetadata returns the metadata of one statistic, or
// errNoStatisticMetadata when HA has none for it.
func statisticMetadata(ctx context.Context, ws *haapi.WSClient, id string) (*haapi.StatisticMetadata, error) {
	all, err := ws.StatisticsMetadata(ctx, []string{id})
	if err != nil {
		return nil, fmt.Errorf("fetching statistics metadata: %w", err)
	}
	for i := range all {
		if all[i].StatisticID == id {
			return &all[i], nil
		}
	}
	return nil, errNoStatisticMetadata
}

// errNoStatistic says which "no statistic" this is: an external id HA does
// not have, an entity that does not exist, a real entity without a
// state_class (HA compiles statistics only for those), or a real entity WITH
// one whose statistics are still missing. The last used to be answered as the
// third — "HA keeps them only for entities with a state_class" — about a
// sensor whose state_class was `measurement` and which the recorder did not
// record at all (live run 2026-10-29). Any of them may have left statistics
// under a neighbouring id: a renamed entity keeps its rows under the old one.
func errNoStatistic(ctx context.Context, client *haapi.Client, ws *haapi.WSClient, id string) error {
	similar := similarStatisticIDs(ctx, ws, id)
	if isExternalStatisticID(id) {
		return fmt.Errorf("no long-term statistic %q on this instance "+
			"(external statistic ids feeding the energy dashboard are listed by `energy show`)%s", id, similar)
	}
	ent, err := fetchEntityState(ctx, client, id)
	if err != nil {
		return fmt.Errorf("entity %q: no live state and no long-term statistics "+
			"(check the entity_id with `ent ls --pattern '*%s*'`)%s", id, lastIDSegment(id), similar)
	}
	stateClass, hasStateClass := ent.Attributes["state_class"]
	if !hasStateClass {
		return fmt.Errorf("%s has no long-term statistics: it has no state_class, and HA compiles them only "+
			"for entities that have one; read its recorder history with `ent hist %s` (without --stats)%s",
			id, id, similar)
	}
	return fmt.Errorf("%s has state_class %v but no long-term statistics under this id — HA compiles them "+
		"hourly from what the recorder stores, so an entity the recorder does not record, or one created "+
		"within the last hour, has none (`ent hist %s` shows whether the recorder has rows for it)%s",
		id, stateClass, id, similar)
}

// similarStatisticIDs names statistics whose id is id plus or minus HA's
// de-duplication suffix (sensor.x ↔ sensor.x_2) — the trace an entity rename
// or re-pairing leaves, since the old id keeps its statistics. Siblings that
// merely share a stem (cell_voltage_7 / cell_voltage_8) are not a rename and
// are not named. It is advisory: a failed listing yields nothing rather than
// failing the answer it decorates.
func similarStatisticIDs(ctx context.Context, ws *haapi.WSClient, id string) string {
	all, err := ws.ListStatisticIDs(ctx)
	if err != nil {
		return ""
	}
	var found []string
	for i := range all {
		other := all[i].StatisticID
		if other != id && (trimNumericSuffix(other) == id || trimNumericSuffix(id) == other) {
			found = append(found, other)
		}
	}
	if len(found) == 0 {
		return ""
	}
	slices.Sort(found)
	return "; statistics exist under a similar id (a renamed entity keeps its statistics under the old one): " +
		strings.Join(found, ", ")
}

// trimNumericSuffix removes one trailing `_<digits>` — HA's de-duplication
// suffix for an entity id that is already taken.
func trimNumericSuffix(id string) string {
	i := strings.LastIndexByte(id, '_')
	if i < 0 || i == len(id)-1 {
		return id
	}
	for _, r := range id[i+1:] {
		if r < '0' || r > '9' {
			return id
		}
	}
	return id[:i]
}

// statisticKind names what a statistic aggregates: "mean" (temperature,
// power), "sum" (meters), or both.
func statisticKind(m *haapi.StatisticMetadata) string {
	switch {
	case m.HasMean && m.HasSum:
		return "mean+sum"
	case m.HasSum:
		return "sum"
	default:
		return "mean"
	}
}

func statisticUnit(m *haapi.StatisticMetadata) string {
	if m.DisplayUnit == nil {
		return ""
	}
	return *m.DisplayUnit
}

// statisticRowJSON is one row of `ent hist --stats --json`: the period and
// every value HA returned, null where the statistic has none. The text table
// shows the fields of the statistic's kind; the JSON carries all of them, so
// nothing a caller could need is decided away by the renderer (H-10).
type statisticRowJSON struct {
	Start  string   `json:"start"`
	End    string   `json:"end"`
	Mean   *float64 `json:"mean"`
	Min    *float64 `json:"min"`
	Max    *float64 `json:"max"`
	State  *float64 `json:"state"`
	Sum    *float64 `json:"sum"`
	Change *float64 `json:"change"`
}

func renderStatisticRows(w io.Writer, id, period string, meta *haapi.StatisticMetadata, rows []haapi.StatisticRow) error {
	if flagJSON {
		out := make([]statisticRowJSON, len(rows))
		for i := range rows {
			r := &rows[i]
			start, err := r.StartTime()
			if err != nil {
				return err
			}
			end, err := r.EndTime()
			if err != nil {
				return err
			}
			out[i] = statisticRowJSON{
				Start:  formatMachineTime(start.Format(time.RFC3339)),
				End:    formatMachineTime(end.Format(time.RFC3339)),
				Mean:   r.Mean,
				Min:    r.Min,
				Max:    r.Max,
				State:  r.State,
				Sum:    r.Sum,
				Change: r.Change,
			}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	unit := statisticUnit(meta)
	header := fmt.Sprintf("%s: long-term statistics (%s), period %s, %d rows", id, statisticKind(meta), period, len(rows))
	if unit != "" {
		header += " (" + unit + ")"
	}
	_, _ = fmt.Fprintln(w, header)

	headers := []string{"time"}
	if meta.HasMean {
		headers = append(headers, "mean", "min", "max")
	}
	if meta.HasSum {
		// change is the consumption within the period; state the meter
		// reading at its end.
		headers = append(headers, "change", "state")
	}
	tbl := &format.Table{Headers: headers, Rows: make([][]string, len(rows))}
	tbl.SetTimeColumn("time")
	for i := range rows {
		r := &rows[i]
		start, err := r.StartTime()
		if err != nil {
			return err
		}
		row := []string{start.Format(time.RFC3339)}
		if meta.HasMean {
			row = append(row, statisticCell(r.Mean), statisticCell(r.Min), statisticCell(r.Max))
		}
		if meta.HasSum {
			row = append(row, statisticCell(r.Change), statisticCell(r.State))
		}
		tbl.Rows[i] = row
	}
	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		Compact: true,
	})
}

func statisticCell(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', 2, 64)
}

// writeStatisticsCoverageNote tells the caller, after `ent hist` without
// --stats, that the raw history does not cover the window while long-term
// statistics reach further — and names the command that reads them.
//
// historyStart is the first history row's time, or zero when HA returned no
// history at all. The trigger is oracle-grounded
// (TestOracleHistoryFirstRowMarksCoverage): when --since starts inside the
// recorded range HA clamps the first row to the window start, and when it
// starts before the oldest recorded state the first row carries that state's
// real time — so "the first row is after the window start" is exactly "the
// history does not cover the window". A covered window costs no request.
//
// The note goes to w in text mode and to errW under --json, so the JSON
// document is unchanged (H-10 clause 1). It is advisory, but its absence is
// documented to mean "nothing further to find", so a failed check says so
// rather than staying silent.
func writeStatisticsCoverageNote(ctx context.Context, w, errW io.Writer, cfg *config.Config, entityID string, windowStart, historyStart, now time.Time) {
	if !historyStart.IsZero() && !historyStart.After(windowStart) {
		return
	}
	out := w
	if flagJSON {
		out = errW
	}
	cutoff := historyStart
	historyLine := "no recorder history in the window"
	if historyStart.IsZero() {
		cutoff = now
	} else {
		historyLine = "recorder history starts " + clock.Long(historyStart.Format(time.RFC3339Nano))
	}

	firstStat, err := firstStatisticBefore(ctx, cfg, entityID, windowStart, cutoff)
	if err != nil {
		_, _ = fmt.Fprintf(out, "\nnote: %s; could not check long-term statistics (%v)\n", historyLine, err)
		return
	}
	if firstStat.IsZero() || !firstStat.Before(cutoff) {
		return
	}
	// The probe is bounded by the window, so its first row says where the
	// statistics start INSIDE it — never how far back they reach beyond it.
	statsLine := "long-term statistics cover the whole window"
	if firstStat.After(windowStart) {
		statsLine = "long-term statistics start " + clock.Long(firstStat.Format(time.RFC3339Nano))
	}
	_, _ = fmt.Fprintf(out, "\nnote: %s; %s:\n      hactl ent hist %s --since %s --stats\n",
		historyLine, statsLine, entityID, sinceWindow())
}

// firstStatisticBefore returns the start of the first daily statistics row
// between windowStart and cutoff, or zero when the entity has no statistics
// there. Daily rows keep the request small over a long window; the answer is
// a day boundary in HA's zone, rendered in the reader's.
func firstStatisticBefore(ctx context.Context, cfg *config.Config, entityID string, windowStart, cutoff time.Time) (time.Time, error) {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return time.Time{}, fmt.Errorf("connecting to HA: %w", err)
	}
	defer func() { _ = ws.Close() }()

	_, err := statisticMetadata(ctx, ws, entityID)
	if errors.Is(err, errNoStatisticMetadata) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	rows, err := ws.StatisticsDuringPeriod(ctx, entityID, windowStart, cutoff, "day")
	if err != nil {
		return time.Time{}, fmt.Errorf("fetching statistics: %w", err)
	}
	if len(rows) == 0 {
		return time.Time{}, nil
	}
	return rows[0].StartTime()
}

// entStatistics is `ent show`'s answer to "does this entity have long-term
// statistics?" — the discovery half of D-47, so a caller learns that
// `ent hist --stats` will answer before running it.
type entStatistics struct {
	meta *haapi.StatisticMetadata
	// err is why the question could not be asked. It is never rendered as
	// "none": an instance whose websocket or recorder did not answer has not
	// said the entity has no statistics (H-7).
	err error
}

func (s entStatistics) label() string {
	switch {
	case s.err != nil:
		return "unknown (" + s.err.Error() + ")"
	case s.meta == nil:
		return "none"
	}
	label := statisticKind(s.meta)
	if unit := statisticUnit(s.meta); unit != "" {
		label += " " + unit
	}
	return label + " (ent hist --stats)"
}

// addJSON sets `statistics` to {kind, unit} or null (none); when the question
// could not be asked the key is absent and `statistics_error` says why.
func (s entStatistics) addJSON(result map[string]any) {
	switch {
	case s.err != nil:
		result["statistics_error"] = s.err.Error()
	case s.meta == nil:
		result["statistics"] = nil
	default:
		result["statistics"] = map[string]any{
			"kind": statisticKind(s.meta),
			"unit": statisticUnit(s.meta),
		}
	}
}

// statisticsFallbackHint is the clause appended to an `ent hist` failure —
// no history for an unknown entity, or a history request that failed — when
// long-term statistics exist for the id. It names the --stats command with a
// window that reaches the newest row. Empty when there are none or the check
// itself fails: it decorates an error that already stands on its own.
func statisticsFallbackHint(ctx context.Context, cfg *config.Config, entityID string, windowStart, now time.Time) string {
	id := strings.ToLower(entityID)
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return ""
	}
	defer func() { _ = ws.Close() }()
	if _, err := statisticMetadata(ctx, ws, id); err != nil {
		return ""
	}
	newest, err := newestStatistic(ctx, ws, id, now)
	if err != nil || newest.IsZero() {
		return ""
	}
	since := sinceWindow()
	if newest.Before(windowStart) {
		since = sinceCovering(newest.Add(-time.Hour), now)
	}
	return fmt.Sprintf(" — it has long-term statistics (newest row ends %s): hactl ent hist %s --stats --since %s",
		clock.Long(newest.Format(time.RFC3339Nano)), id, since)
}
