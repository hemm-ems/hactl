package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // CGO-free SQLite driver
)

// Store provides SQLite-backed caching for traces and a JSONL ring buffer for logs.
type Store struct {
	db      *sql.DB
	dir     string
	logPath string
}

// Meta holds sync timestamps and HA version for cache freshness checks.
type Meta struct {
	LastSync     time.Time `json:"last_sync"`
	TracesSync   time.Time `json:"traces_sync"`
	LogsSync     time.Time `json:"logs_sync"`
	HAVersion    string    `json:"ha_version"`
	TraceCount   int       `json:"trace_count"`
	LogLineCount int       `json:"log_line_count"`
}

// TraceRecord holds a trace row for batch insertion.
type TraceRecord struct {
	RunID     string
	Domain    string
	ItemID    string
	StartTime string
	Execution string
	ErrorMsg  string
	LastStep  string
	Trigger   string
	RawJSON   string
}

// Status holds cache size and freshness info.
type Status struct {
	TracesSync   string `json:"traces_sync"`
	LogsSync     string `json:"logs_sync"`
	TraceCount   int    `json:"trace_count"`
	TracesDBSize int64  `json:"traces_db_size"`
	LogSize      int64  `json:"log_size"`
}

// maxLogLines is the maximum number of log lines to keep in the ring buffer.
const maxLogLines = 10000

// Open opens or creates the cache in the given instance directory.
// Creates the cache/ subdirectory and traces.db if needed.
func Open(ctx context.Context, instanceDir string) (*Store, error) {
	cacheDir := filepath.Join(instanceDir, "cache")
	if err := os.MkdirAll(filepath.Clean(cacheDir), 0o750); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}

	dbPath := filepath.Join(cacheDir, "traces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening cache database: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("setting WAL mode: %w", err)
	}

	s := &Store{
		db:      db,
		dir:     cacheDir,
		logPath: filepath.Join(cacheDir, "logs.jsonl"),
	}

	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrating cache schema: %w", err)
	}

	return s, nil
}

// Close closes the cache database.
func (s *Store) Close() error {
	return s.db.Close()
}

// StoreTrace inserts or replaces a trace in the cache.
func (s *Store) StoreTrace(ctx context.Context, runID, domain, itemID, startTime, execution, errorMsg, lastStep, trigger string, rawJSON []byte) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO traces (run_id, domain, item_id, start_time, execution, error_msg, last_step, trigger, raw_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, domain, itemID, startTime, execution, errorMsg, lastStep, trigger, string(rawJSON),
	)
	return err
}

// StoreTraces inserts multiple traces in a single transaction.
func (s *Store) StoreTraces(ctx context.Context, traces []TraceRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO traces (run_id, domain, item_id, start_time, execution, error_msg, last_step, trigger, raw_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, t := range traces {
		if _, execErr := stmt.ExecContext(ctx, t.RunID, t.Domain, t.ItemID, t.StartTime, t.Execution, t.ErrorMsg, t.LastStep, t.Trigger, t.RawJSON); execErr != nil {
			return fmt.Errorf("insert trace %s: %w", t.RunID, execErr)
		}
	}

	return tx.Commit()
}

// GetTrace retrieves a single trace's raw JSON by run_id.
func (s *Store) GetTrace(ctx context.Context, runID string) ([]byte, error) {
	var rawJSON string
	err := s.db.QueryRowContext(ctx, "SELECT raw_json FROM traces WHERE run_id = ?", runID).Scan(&rawJSON)
	if err != nil {
		return nil, fmt.Errorf("getting trace %s: %w", runID, err)
	}
	return []byte(rawJSON), nil
}

// GetTracesForItem retrieves traces for a given domain.item_id, ordered by start_time desc.
func (s *Store) GetTracesForItem(ctx context.Context, domain, itemID string, limit int) ([]TraceRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, domain, item_id, start_time, execution, error_msg, last_step, trigger, raw_json
		FROM traces
		WHERE domain = ? AND item_id = ?
		ORDER BY start_time DESC
		LIMIT ?`, domain, itemID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying traces: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []TraceRecord
	for rows.Next() {
		var t TraceRecord
		if err := rows.Scan(&t.RunID, &t.Domain, &t.ItemID, &t.StartTime, &t.Execution, &t.ErrorMsg, &t.LastStep, &t.Trigger, &t.RawJSON); err != nil {
			return nil, fmt.Errorf("scanning trace row: %w", err)
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// TraceCount returns the total number of cached traces.
func (s *Store) TraceCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM traces").Scan(&count)
	return count, err
}

// ClearTraces removes all cached traces.
func (s *Store) ClearTraces(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM traces")
	return err
}

// AppendLogs appends log entries to the JSONL ring buffer, trimming old entries.
func (s *Store) AppendLogs(entries []json.RawMessage) error {
	f, err := os.OpenFile(filepath.Clean(s.logPath), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	defer func() { _ = f.Close() }()

	for _, entry := range entries {
		if _, writeErr := f.Write(append(entry, '\n')); writeErr != nil {
			return fmt.Errorf("writing log entry: %w", writeErr)
		}
	}

	return nil
}

// ReadLogs reads all log lines from the JSONL ring buffer.
func (s *Store) ReadLogs() (string, error) {
	data, err := os.ReadFile(filepath.Clean(s.logPath))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading logs: %w", err)
	}
	return string(data), nil
}

// ClearLogs removes the log ring buffer file.
func (s *Store) ClearLogs() error {
	err := os.Remove(filepath.Clean(s.logPath))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LogSize returns the byte size of the log ring buffer.
func (s *Store) LogSize() (int64, error) {
	fi, err := os.Stat(filepath.Clean(s.logPath))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return fi.Size(), nil
}

// TrimLogs trims the log ring buffer to maxLogLines.
func (s *Store) TrimLogs() error {
	data, err := os.ReadFile(filepath.Clean(s.logPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= maxLogLines {
		return nil
	}
	trimmed := lines[len(lines)-maxLogLines:]
	return os.WriteFile(s.logPath, []byte(strings.Join(trimmed, "\n")+"\n"), 0o600) //nolint:gosec // logPath is constructed from user-provided instanceDir by design
}

// SetMeta sets a metadata key-value pair.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, key, value)
	return err
}

// GetMeta retrieves a metadata value by key.
func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// GetStatus returns cache status information.
func (s *Store) GetStatus(ctx context.Context) (*Status, error) {
	traceCount, err := s.TraceCount(ctx)
	if err != nil {
		return nil, err
	}

	logSize, err := s.LogSize()
	if err != nil {
		return nil, err
	}

	tracesSync, _ := s.GetMeta(ctx, "traces_sync")
	logsSync, _ := s.GetMeta(ctx, "logs_sync")

	dbPath := filepath.Join(s.dir, "traces.db")
	dbSize := int64(0)
	if fi, statErr := os.Stat(dbPath); statErr == nil {
		dbSize = fi.Size()
	}

	return &Status{
		TraceCount:   traceCount,
		TracesDBSize: dbSize,
		LogSize:      logSize,
		TracesSync:   tracesSync,
		LogsSync:     logsSync,
	}, nil
}

// Clear removes all cached data (traces + logs).
func (s *Store) Clear(ctx context.Context) error {
	if err := s.ClearTraces(ctx); err != nil {
		return fmt.Errorf("clearing traces: %w", err)
	}
	if err := s.ClearLogs(); err != nil {
		return fmt.Errorf("clearing logs: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM meta"); err != nil {
		return fmt.Errorf("clearing meta: %w", err)
	}
	slog.Debug("cache cleared")
	return nil
}

// RefreshTraces clears and re-stores all provided traces.
func (s *Store) RefreshTraces(ctx context.Context, traces []TraceRecord) error {
	if err := s.ClearTraces(ctx); err != nil {
		return err
	}
	if err := s.StoreTraces(ctx, traces); err != nil {
		return err
	}
	return s.SetMeta(ctx, "traces_sync", time.Now().UTC().Format(time.RFC3339))
}

// RefreshLogs replaces the log ring buffer with new data.
func (s *Store) RefreshLogs(ctx context.Context, logText string) error {
	if err := os.WriteFile(filepath.Clean(s.logPath), []byte(logText), 0o600); err != nil {
		return fmt.Errorf("writing logs: %w", err)
	}
	return s.SetMeta(ctx, "logs_sync", time.Now().UTC().Format(time.RFC3339))
}

// Dir returns the cache directory path.
func (s *Store) Dir() string {
	return s.dir
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS traces (
			run_id     TEXT PRIMARY KEY,
			domain     TEXT NOT NULL,
			item_id    TEXT NOT NULL,
			start_time TEXT NOT NULL,
			execution  TEXT NOT NULL DEFAULT '',
			error_msg  TEXT NOT NULL DEFAULT '',
			last_step  TEXT NOT NULL DEFAULT '',
			trigger    TEXT NOT NULL DEFAULT '',
			raw_json   TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);

		CREATE INDEX IF NOT EXISTS idx_traces_item ON traces(domain, item_id);
		CREATE INDEX IF NOT EXISTS idx_traces_start ON traces(start_time);

		CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`)
	return err
}
