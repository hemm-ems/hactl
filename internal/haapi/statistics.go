package haapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hemm-ems/hactl/internal/degeneracy"
)

// Long-term statistics — the hourly (and 5-minute) aggregates HA's recorder
// keeps after the raw state history is purged, read by `ent hist --stats`
// (D-47, SPEC-ent-stats.md).
// WS commands: recorder/get_statistics_metadata, recorder/statistics_during_period.
// Source: homeassistant/components/recorder/websocket_api.py.
// Shapes oracle-probed on HA 2026.10.4 (TestOracleStatistics* in
// internal/integration/statistics_oracle_test.go).

// StatisticPeriods are the periods recorder/statistics_during_period accepts,
// finest first.
var StatisticPeriods = []string{"5minute", "hour", "day", "week", "month", "year"}

// StatisticMetadata describes one statistic. Only the rendered fields are
// typed — tolerant by construction (H-21's spirit).
type StatisticMetadata struct {
	StatisticID string `json:"statistic_id"`
	Source      string `json:"source"`
	// Name is null for a statistic HA compiled from an entity; the entity's
	// own name is the one to show then.
	Name *string `json:"name"`
	// DisplayUnit is the unit HA renders in (the entity's current unit);
	// rows from statistics_during_period are converted to it by default.
	DisplayUnit *string `json:"display_unit_of_measurement"`
	HasMean     bool    `json:"has_mean"`
	HasSum      bool    `json:"has_sum"`
}

// Identity reports the statistic id — a metadata row without one cannot be
// matched to the statistic it describes.
func (m *StatisticMetadata) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "statistic_id", Value: &m.StatisticID}}
}

// StatisticRow is one period of one statistic. A mean statistic carries
// mean/min/max, a sum statistic state/sum/change; the other kind's fields are
// absent or null, so every value is nullable. For a circular mean
// (mean_type 2, e.g. wind direction) whether HA fills min/max is unobserved —
// nullable covers either answer.
type StatisticRow struct {
	// Start and End are epoch milliseconds on the wire (numbers, not
	// ISO strings). Kept as json.Number so the identity check can see an
	// absent start; StartTime/EndTime convert.
	Start  json.Number `json:"start"`
	End    json.Number `json:"end"`
	Mean   *float64    `json:"mean"`
	Min    *float64    `json:"min"`
	Max    *float64    `json:"max"`
	State  *float64    `json:"state"`
	Sum    *float64    `json:"sum"`
	Change *float64    `json:"change"`
}

// Identity reports the period start — a row without one cannot be placed on
// the time axis.
func (r *StatisticRow) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "start", Value: (*string)(&r.Start)}}
}

// StartTime is the period start.
func (r *StatisticRow) StartTime() (time.Time, error) { return epochMillis(r.Start) }

// EndTime is the period end.
func (r *StatisticRow) EndTime() (time.Time, error) { return epochMillis(r.End) }

func epochMillis(n json.Number) (time.Time, error) {
	ms, err := n.Float64()
	if err != nil {
		return time.Time{}, fmt.Errorf("statistic timestamp %q: %w", n, err)
	}
	return time.UnixMilli(int64(ms)), nil
}

// StatisticsMetadata returns the metadata of the given statistic ids. An id
// HA has no statistic for is absent from the answer (oracle-probed) — that
// absence is the only way to tell "no such statistic" from "no rows in this
// window", because statistics_during_period answers `{}` for both.
func (ws *WSClient) StatisticsMetadata(ctx context.Context, ids []string) ([]StatisticMetadata, error) {
	result, err := ws.sendCommand(ctx, "recorder/get_statistics_metadata", map[string]any{
		"statistic_ids": ids,
	})
	if err != nil {
		return nil, err
	}
	var meta []StatisticMetadata
	if err := json.Unmarshal(result, &meta); err != nil {
		return nil, fmt.Errorf("parsing statistics metadata: %w", err)
	}
	if err := degeneracy.Check("recorder/get_statistics_metadata", &meta); err != nil {
		return nil, err
	}
	return meta, nil
}

// StatisticsDuringPeriod returns one statistic's rows between start and end
// at the given period. Rows come back in the statistic's display unit. A
// window with no rows answers an empty slice, never an error.
func (ws *WSClient) StatisticsDuringPeriod(ctx context.Context, id string, start, end time.Time, period string) ([]StatisticRow, error) {
	result, err := ws.sendCommand(ctx, "recorder/statistics_during_period", map[string]any{
		"statistic_ids": []string{id},
		"start_time":    start.UTC().Format(time.RFC3339),
		"end_time":      end.UTC().Format(time.RFC3339),
		"period":        period,
	})
	if err != nil {
		return nil, err
	}
	var byID map[string][]StatisticRow
	if err := json.Unmarshal(result, &byID); err != nil {
		return nil, fmt.Errorf("parsing statistics: %w", err)
	}
	if err := degeneracy.Check("recorder/statistics_during_period", &byID); err != nil {
		return nil, err
	}
	return byID[id], nil
}

// ListStatisticIDs returns the metadata of every statistic HA keeps — the
// same row shape get_statistics_metadata answers (oracle-probed). Used only
// on the "no such statistic" path, to name ids a renamed entity left behind.
func (ws *WSClient) ListStatisticIDs(ctx context.Context) ([]StatisticMetadata, error) {
	result, err := ws.sendCommand(ctx, "recorder/list_statistic_ids", nil)
	if err != nil {
		return nil, err
	}
	var meta []StatisticMetadata
	if err := json.Unmarshal(result, &meta); err != nil {
		return nil, fmt.Errorf("parsing statistic ids: %w", err)
	}
	if err := degeneracy.Check("recorder/list_statistic_ids", &meta); err != nil {
		return nil, err
	}
	return meta, nil
}
