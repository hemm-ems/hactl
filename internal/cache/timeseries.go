package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // CGO-free SQLite driver
)

// TSStore provides SQLite-backed caching for entity time series data.
type TSStore struct {
	db  *sql.DB
	dir string
}

// TSStatus holds timeseries cache size and freshness info.
type TSStatus struct {
	SampleCount int   `json:"sample_count"`
	DBSize      int64 `json:"db_size"`
}

// OpenTS opens or creates the timeseries cache in the given instance directory.
func OpenTS(ctx context.Context, instanceDir string) (*TSStore, error) {
	cacheDir := filepath.Join(instanceDir, "cache")
	if err := os.MkdirAll(filepath.Clean(cacheDir), 0o750); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}

	dbPath := filepath.Join(cacheDir, "timeseries.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening timeseries database: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("setting WAL mode: %w", err)
	}

	s := &TSStore{
		db:  db,
		dir: cacheDir,
	}

	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating timeseries schema: %w", err)
	}

	return s, nil
}

// Close closes the timeseries cache database.
func (s *TSStore) Close() error {
	return s.db.Close()
}

// StoreSamples inserts data points for an entity in a single transaction.
func (s *TSStore) StoreSamples(ctx context.Context, entityID string, times []time.Time, values []float64) error {
	if len(times) != len(values) {
		return errors.New("times and values must have same length")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO samples (entity_id, time, value)
		VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range times {
		if _, execErr := stmt.ExecContext(ctx, entityID, times[i].UTC().Format(time.RFC3339Nano), values[i]); execErr != nil {
			return fmt.Errorf("insert sample: %w", execErr)
		}
	}

	slog.Debug("stored timeseries samples", "entity", entityID, "count", len(times))
	return tx.Commit()
}

// GetSamples retrieves cached data points for an entity within a time range.
func (s *TSStore) GetSamples(ctx context.Context, entityID string, since, until time.Time) ([]time.Time, []float64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT time, value FROM samples
		WHERE entity_id = ? AND time >= ? AND time <= ?
		ORDER BY time ASC`,
		entityID,
		since.UTC().Format(time.RFC3339Nano),
		until.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("querying samples: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var times []time.Time
	var values []float64
	for rows.Next() {
		var timeStr string
		var value float64
		if scanErr := rows.Scan(&timeStr, &value); scanErr != nil {
			return nil, nil, fmt.Errorf("scanning sample: %w", scanErr)
		}
		t, parseErr := time.Parse(time.RFC3339Nano, timeStr)
		if parseErr != nil {
			continue
		}
		times = append(times, t)
		values = append(values, value)
	}
	return times, values, rows.Err()
}

// LatestSample returns the most recent sample time for an entity, or zero time if none.
func (s *TSStore) LatestSample(ctx context.Context, entityID string) (time.Time, error) {
	var timeStr sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(time) FROM samples WHERE entity_id = ?`, entityID).Scan(&timeStr)
	if err != nil || !timeStr.Valid {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, timeStr.String)
}

// SampleCount returns the total number of cached samples.
func (s *TSStore) SampleCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM samples").Scan(&count)
	return count, err
}

// ClearEntity removes all cached samples for an entity.
func (s *TSStore) ClearEntity(ctx context.Context, entityID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM samples WHERE entity_id = ?", entityID)
	return err
}

// Clear removes all cached timeseries data.
func (s *TSStore) Clear(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM samples")
	return err
}

// GetStatus returns timeseries cache status information.
func (s *TSStore) GetStatus(ctx context.Context) (*TSStatus, error) {
	count, err := s.SampleCount(ctx)
	if err != nil {
		return nil, err
	}

	dbPath := filepath.Join(s.dir, "timeseries.db")
	dbSize := int64(0)
	if fi, statErr := os.Stat(dbPath); statErr == nil {
		dbSize = fi.Size()
	}

	return &TSStatus{
		SampleCount: count,
		DBSize:      dbSize,
	}, nil
}

func (s *TSStore) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS samples (
			entity_id TEXT NOT NULL,
			time      TEXT NOT NULL,
			value     REAL NOT NULL,
			PRIMARY KEY (entity_id, time)
		);

		CREATE INDEX IF NOT EXISTS idx_samples_entity_time ON samples(entity_id, time);
	`)
	return err
}
