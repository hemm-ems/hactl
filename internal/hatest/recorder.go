//go:build integration

package hatest

// The recorder-backfill rig.
//
// Home Assistant has no API that can write past-dated history: every write path
// (`/api/states`, the WebSocket API, services) timestamps rows *now*. A freshly
// booted test container therefore has minutes of history, and every command that
// reasons over a long window — `ent anomalies`, long `ent hist` — can only ever
// be exercised against a series too short to contain the behaviour it looks for.
// That is why the anomalies tests accepted an empty result as success: no test
// had ever fed the detector data containing an anomaly, so "found nothing" and
// "is broken" were the same observation.
//
// The only way to backdate history is to write the rows HA's own recorder would
// have written, straight into its SQLite database. That is what this file does.
//
// # Ordering: why the container is stopped, and why that is not negotiable
//
// HA's recorder opens home-assistant_v2.db in WAL mode and holds that connection
// for the lifetime of the process. Writing into it from the test process while
// the container runs would mean two writers on opposite sides of the Docker
// Desktop VM boundary: the container's writer sees the file through the VM's
// kernel, ours sees it through a virtiofs/gRPC-FUSE share on the host. SQLite's
// concurrency control is POSIX advisory locking on that file, and advisory locks
// are not reliably carried across that boundary. The failure mode is not a busy
// error we could wait out — it is a corrupted database or a lost write, silently.
//
// So Backfill stops the container first. SIGTERM makes HA close the recorder
// cleanly and checkpoint the WAL, which is also what makes the file safe to open
// at all. The rig then *asserts* the container is not running before it opens the
// database, rather than assuming Stop() did what it said — that assertion is the
// difference between an explicit ordering and an incidental one. Nothing here
// sleeps: Stop() is synchronous, the state check is a fact, and the restart is
// gated on HA reporting RUNNING again.
//
// # Fidelity: writing what HA would have written
//
// A rig that invents row shapes is a fixture-fiction generator, which is the
// disease this whole test effort is treating. Two details matter and are easy to
// get backwards:
//
//   - last_changed_ts is NULL when the state VALUE changed (HA stores NULL when
//     last_changed == last_updated, to save space) and holds the *earlier*
//     change time when only attributes changed. It is not "the time this row
//     changed".
//   - That distinction is load-bearing, not cosmetic: HA's history API defaults
//     to significant_changes_only, whose SQL keeps a row only when
//     `last_changed_ts IS NULL OR last_changed_ts == last_updated_ts` (outside a
//     handful of significant domains). Rows written with the convention
//     inverted survive that filter when real ones would not, and the series the
//     detector sees stops resembling anything HA can produce.
//
// Both claims are executable, not editorial: TestRecorderBackfill's
// rig_lands_in_has_own_history subtest (internal/integration/backfill_test.go)
// reads every backfilled row back out through HA's own history API, and pins the
// attribute-only filter by writing twelve attributes-only updates and requiring
// HA to return exactly one of them. Inverting the last_changed_ts convention
// makes HA return all twelve and that subtest go red.
//
// # Schema coupling
//
// The row layout below is schema-version-coupled by construction. The rig
// refuses to write into a schema version it was not verified against, because
// silently writing a stale shape into a future HA would manufacture exactly the
// kind of plausible-but-wrong fixture this harness exists to prevent.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // CGO-free SQLite driver, already used by internal/cache
)

// recorderDBName is the SQLite database HA's recorder integration creates in
// /config. It is only reachable from the test process when the instance was
// started WithFixture, because that is what bind-mounts /config to the host.
const recorderDBName = "home-assistant_v2.db"

// backfillStopTimeout bounds HA's graceful shutdown. HA flushes and closes the
// recorder on SIGTERM; if it has not finished in this long, Docker kills it and
// the WAL is left dirty, which we detect rather than write through.
const backfillStopTimeout = 90 * time.Second

// supportedRecorderSchema lists the recorder schema versions this rig has been
// verified against by reading the rows back out through HA's own history API.
//
// Adding a version here is not a formality. Check
// homeassistant/components/recorder/db_schema.py for the release in question:
// the columns written by insertStates, and the last_changed_ts / last_reported_ts
// conventions in States.from_event, must both still hold.
var supportedRecorderSchema = map[int]bool{
	53: true, // ghcr.io/home-assistant/home-assistant:stable, HA 2026.7.x
}

// requiredColumns are the columns the rig writes or relies on. Presence is
// checked before any INSERT so a schema drift that keeps the version number but
// moves a column fails with a readable message instead of a driver error.
var requiredColumns = map[string][]string{
	"states": {
		"state_id", "metadata_id", "state", "attributes_id",
		"last_updated_ts", "last_changed_ts", "last_reported_ts",
		"old_state_id", "origin_idx",
	},
	"states_meta":      {"metadata_id", "entity_id"},
	"state_attributes": {"attributes_id", "hash", "shared_attrs"},
}

// Sample is one recorded state, at the moment HA would have recorded it.
// State is the raw string HA stores — numeric sensors store their number as
// text, and "unavailable"/"unknown" are ordinary states like any other.
type Sample struct {
	At    time.Time
	State string
	Attrs map[string]any
}

// Series is one entity's backfilled history. Samples must be in ascending time
// order; Backfill rejects anything else rather than writing a series HA could
// not have produced.
type Series struct {
	EntityID string
	Samples  []Sample
}

// ConfigDir returns the host path bind-mounted as /config in the container, or
// "" for an instance started without a fixture.
func (i *Instance) ConfigDir() string { return i.configDir }

// Backfill writes the given history into HA's recorder database and returns the
// instance to a running state.
//
// It stops the container, verifies the recorder schema is one it knows how to
// write, inserts the rows, and starts the container again — re-resolving the
// published port, which Docker re-assigns on restart, and rewriting the .env in
// Dir() so hactl keeps pointing at the instance.
//
// After it returns, HA serves the backfilled rows through its ordinary history
// and statistics APIs: they are recorder rows like any other.
func (i *Instance) Backfill(ctx context.Context, series ...Series) error {
	if i.configDir == "" {
		return errors.New("hatest: Backfill needs an instance started WithFixture — " +
			"without a bind-mounted /config the recorder database is not reachable from the test process")
	}
	for _, s := range series {
		if err := validateSeries(s); err != nil {
			return err
		}
	}

	dbPath := filepath.Join(i.configDir, recorderDBName)
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("hatest: recorder database %s not found: %w "+
			"(the fixture must enable `recorder:` — `default_config:` does)", dbPath, err)
	}

	if err := i.stopForBackfill(ctx); err != nil {
		return err
	}

	rows, err := backfillDB(ctx, dbPath, series)
	if err != nil {
		// Leave the instance running even when the write failed: a caller that
		// t.Fatals still has a container that can be inspected and torn down.
		if startErr := i.restartAfterBackfill(ctx); startErr != nil {
			return fmt.Errorf("%w (and restarting the container afterwards failed: %w)", err, startErr)
		}
		return err
	}
	slog.Info("hatest: recorder backfill written", "rows", rows, "series", len(series), "db", dbPath)

	return i.restartAfterBackfill(ctx)
}

func validateSeries(s Series) error {
	if s.EntityID == "" || !strings.Contains(s.EntityID, ".") {
		return fmt.Errorf("hatest: backfill series has invalid entity_id %q", s.EntityID)
	}
	if len(s.Samples) == 0 {
		return fmt.Errorf("hatest: backfill series %s has no samples", s.EntityID)
	}
	for j := 1; j < len(s.Samples); j++ {
		if !s.Samples[j].At.After(s.Samples[j-1].At) {
			return fmt.Errorf("hatest: backfill series %s is not strictly ascending at sample %d (%s after %s)",
				s.EntityID, j, s.Samples[j].At, s.Samples[j-1].At)
		}
	}
	return nil
}

// stopForBackfill stops the container and proves it stopped. Docker's stop is
// synchronous, so this is an assertion rather than a wait — if the container is
// still running here, something is wrong that a sleep would only hide.
func (i *Instance) stopForBackfill(ctx context.Context) error {
	timeout := backfillStopTimeout
	if err := i.container.Stop(ctx, &timeout); err != nil {
		return fmt.Errorf("hatest: stopping HA before backfill: %w", err)
	}
	state, err := i.container.State(ctx)
	if err != nil {
		return fmt.Errorf("hatest: reading container state after stop: %w", err)
	}
	if state.Running {
		return fmt.Errorf("hatest: container still running after Stop (status %q) — "+
			"refusing to write into a database HA still has open", state.Status)
	}
	slog.Info("hatest: HA stopped for recorder backfill", "status", state.Status)
	return nil
}

// restartAfterBackfill starts the container again and repairs everything that a
// restart invalidates. Docker re-assigns the published host port on start, so
// the URL captured at first boot is stale from here on; hactl reads the instance
// through Dir()/.env, so that file has to be rewritten too.
func (i *Instance) restartAfterBackfill(ctx context.Context) error {
	if err := i.container.Start(ctx); err != nil {
		return fmt.Errorf("hatest: restarting HA after backfill: %w", err)
	}
	host, err := i.container.Host(ctx)
	if err != nil {
		return fmt.Errorf("hatest: resolving host after restart: %w", err)
	}
	port, err := i.container.MappedPort(ctx, haPort)
	if err != nil {
		return fmt.Errorf("hatest: resolving mapped port after restart: %w", err)
	}
	i.url = "http://" + net.JoinHostPort(host, port.Port())

	if err := writeEnvFile(i.dir, i.url, i.token); err != nil {
		return fmt.Errorf("hatest: rewriting .env after restart: %w", err)
	}
	if err := waitForRunning(ctx, i.url, i.token); err != nil {
		return fmt.Errorf("hatest: HA did not come back up after backfill: %w", err)
	}
	slog.Info("hatest: HA running again after backfill", "url", i.url)
	return nil
}

// backfillDB opens the recorder database and writes every series in one
// transaction. Returns the number of state rows written.
func backfillDB(ctx context.Context, dbPath string, series []Series) (int, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return 0, fmt.Errorf("hatest: opening recorder db: %w", err)
	}
	defer db.Close() //nolint:errcheck // test helper

	if err = verifyRecorderSchema(ctx, db); err != nil {
		return 0, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("hatest: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit

	w, err := newRowWriter(ctx, tx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, s := range series {
		n, err := w.writeSeries(s)
		if err != nil {
			return 0, fmt.Errorf("hatest: writing %s: %w", s.EntityID, err)
		}
		total += n
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("hatest: commit: %w", err)
	}
	// Fold the WAL back into the main database file before HA reopens it. Not
	// strictly required — HA would recover the WAL — but it keeps the file the
	// container sees identical to the one we wrote, with no cross-boundary WAL
	// hand-off to reason about.
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return 0, fmt.Errorf("hatest: wal checkpoint: %w", err)
	}
	return total, nil
}

// verifyRecorderSchema refuses to write into a schema this rig was not built
// for. The version is the tripwire; the column check catches drift that keeps
// the version number.
func verifyRecorderSchema(ctx context.Context, db *sql.DB) error {
	var version int
	err := db.QueryRowContext(ctx,
		"SELECT schema_version FROM schema_changes ORDER BY change_id DESC LIMIT 1").Scan(&version)
	if err != nil {
		return fmt.Errorf("hatest: reading recorder schema version: %w "+
			"(no schema_changes table — is this a recorder database?)", err)
	}
	if !supportedRecorderSchema[version] {
		return fmt.Errorf("hatest: recorder schema version %d is not one this backfill rig was written for (known: %s).\n"+
			"Do NOT widen supportedRecorderSchema without checking "+
			"homeassistant/components/recorder/db_schema.py for that release: the `states` column layout and the "+
			"last_changed_ts/last_reported_ts conventions in States.from_event are what the rig encodes. Writing a "+
			"stale row shape into a newer HA produces history that looks real and is not, which is the exact failure "+
			"mode these tests exist to catch", version, knownSchemaVersions())
	}
	for table, cols := range requiredColumns {
		present, err := tableColumns(ctx, db, table)
		if err != nil {
			return err
		}
		for _, c := range cols {
			if !present[c] {
				return fmt.Errorf("hatest: recorder schema %d has no column %s.%s — "+
					"the layout moved without a version bump this rig knows about", version, table, c)
			}
		}
	}
	return nil
}

func knownSchemaVersions() string {
	vs := make([]int, 0, len(supportedRecorderSchema))
	for v := range supportedRecorderSchema {
		vs = append(vs, v)
	}
	sort.Ints(vs)
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, fmt.Errorf("hatest: reading columns of %s: %w", table, err)
	}
	defer rows.Close() //nolint:errcheck // test helper
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("hatest: recorder database has no table %s", table)
	}
	return cols, nil
}

// rowWriter allocates ids the way the recorder does — monotonically after the
// highest existing one — so HA's own AUTOINCREMENT counters continue past the
// backfill instead of colliding with it.
type rowWriter struct {
	ctx       context.Context //nolint:containedctx // the writer is one call's worth of statements
	tx        *sql.Tx
	nextState int64
	nextAttr  int64
	attrIDs   map[string]int64
	metaIDs   map[string]int64
}

func newRowWriter(ctx context.Context, tx *sql.Tx) (*rowWriter, error) {
	w := &rowWriter{ctx: ctx, tx: tx, attrIDs: map[string]int64{}, metaIDs: map[string]int64{}}
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(state_id),0)+1 FROM states").Scan(&w.nextState); err != nil {
		return nil, fmt.Errorf("hatest: reading max state_id: %w", err)
	}
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(attributes_id),0)+1 FROM state_attributes").Scan(&w.nextAttr); err != nil {
		return nil, fmt.Errorf("hatest: reading max attributes_id: %w", err)
	}
	return w, nil
}

// writeSeries inserts one entity's history.
//
// The last_changed_ts / last_reported_ts values follow States.from_event:
// both are NULL for a row created by a genuine state change, and last_changed_ts
// carries the earlier change time when a row exists only because attributes
// changed. LazyState reads them back as "or last_updated_ts", so a NULL is not a
// missing timestamp — it is HA's encoding of "same as last_updated".
func (w *rowWriter) writeSeries(s Series) (int, error) {
	metaID, err := w.metaID(s.EntityID)
	if err != nil {
		return 0, err
	}

	var (
		prevStateID  any     // old_state_id chain, NULL for the first row
		prevState    *string // nil before the first row, so the first row always counts as a change
		lastChangeTS float64 // when the value last actually changed
	)

	for _, sample := range s.Samples {
		ts := float64(sample.At.UnixNano()) / 1e9

		attrID, err := w.attrID(sample.Attrs)
		if err != nil {
			return 0, err
		}

		var lastChanged any // NULL == "last_changed equals last_updated"
		if prevState != nil && *prevState == sample.State {
			// Attributes-only update: HA keeps the older change time here, and
			// HA's own history API will filter this row out by default.
			lastChanged = lastChangeTS
		} else {
			lastChangeTS = ts
		}

		stateID := w.nextState
		w.nextState++
		_, err = w.tx.ExecContext(w.ctx,
			`INSERT INTO states
			  (state_id, metadata_id, state, attributes_id,
			   last_updated_ts, last_changed_ts, last_reported_ts, old_state_id, origin_idx)
			 VALUES (?,?,?,?,?,?,NULL,?,0)`,
			stateID, metaID, sample.State, attrID, ts, lastChanged, prevStateID)
		if err != nil {
			return 0, err
		}

		prevStateID = stateID
		st := sample.State
		prevState = &st
	}
	return len(s.Samples), nil
}

func (w *rowWriter) metaID(entityID string) (int64, error) {
	if id, ok := w.metaIDs[entityID]; ok {
		return id, nil
	}
	var id int64
	err := w.tx.QueryRowContext(w.ctx, "SELECT metadata_id FROM states_meta WHERE entity_id=?", entityID).Scan(&id)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		res, insErr := w.tx.ExecContext(w.ctx, "INSERT INTO states_meta (entity_id) VALUES (?)", entityID)
		if insErr != nil {
			return 0, insErr
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	default:
		return 0, err
	}
	w.metaIDs[entityID] = id
	return id, nil
}

// attrID stores one attribute set, deduplicated the way the recorder does: the
// shared_attrs JSON is the identity, and `hash` is HA's FNV-1a-32 of those bytes
// (db_schema.StateAttributes.hash_shared_attrs_bytes), so HA's own dedup lookups
// find these rows instead of writing a second copy.
func (w *rowWriter) attrID(attrs map[string]any) (int64, error) {
	if attrs == nil {
		attrs = map[string]any{}
	}
	blob, err := json.Marshal(attrs)
	if err != nil {
		return 0, err
	}
	key := string(blob)
	if id, ok := w.attrIDs[key]; ok {
		return id, nil
	}
	var id int64
	err = w.tx.QueryRowContext(w.ctx,
		"SELECT attributes_id FROM state_attributes WHERE shared_attrs=?", key).Scan(&id)
	if err == nil {
		w.attrIDs[key] = id
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	h := fnv.New32a()
	_, _ = h.Write(blob)
	id = w.nextAttr
	w.nextAttr++
	if _, err := w.tx.ExecContext(w.ctx,
		"INSERT INTO state_attributes (attributes_id, hash, shared_attrs) VALUES (?,?,?)",
		id, int64(h.Sum32()), key); err != nil {
		return 0, err
	}
	w.attrIDs[key] = id
	return id, nil
}
