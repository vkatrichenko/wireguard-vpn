// Package db owns the dashboard's local SQLite store: it bootstraps the
// schema for the three time-series tables that back the trend charts
// (system_metrics, traffic_metrics, client_traffic), and exposes typed
// Insert / Query / Prune helpers around them.
//
// The driver is modernc.org/sqlite — a pure-Go SQLite port — chosen so the
// dashboard binary can keep its CGO_ENABLED=0 static-binary promise. There
// is no libsqlite3 system dep at runtime; the SQLite engine is compiled in.
//
// Storage shape: ts is unix-seconds INTEGER on disk for compactness and to
// make BETWEEN range queries trivially index-friendly. Go callers see
// time.Time (UTC) at the API boundary; the conversion happens in this
// package, not at the call site.
//
// Concurrency: SQLite serialises writes regardless of how many connections
// the pool holds, and modernc.org/sqlite under database/sql is thread-safe
// only insofar as *sql.DB itself is. We pin MaxOpenConns=MaxIdleConns=1 so
// every Insert / Query / Prune funnels through one connection — that
// removes the "database is locked" failure mode under contention from the
// poller (a single 30s-ticked goroutine) and the HTTP handlers (read-only
// range queries) competing for the writer. WAL mode means readers don't
// block the writer at the page level, but the connection-pool serialisation
// is what stops two goroutines from racing on a tx boundary.
//
// Migrations: a single bootstrap CREATE TABLE IF NOT EXISTS block runs on
// Open(). There is no migration framework — the schema is small and any
// future change will be additive (new tables / new indexes), so the same
// idempotent bootstrap continues to work after redeploys.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// modernc.org/sqlite registers itself under the driver name "sqlite"
	// in its init(); we import for side-effect only and use database/sql
	// for the API surface.
	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver name registered by
// modernc.org/sqlite's init(). Keep as a constant so the Open call below
// is the only place that has to know about the driver identity.
const driverName = "sqlite"

// schema is the full bootstrap script run once per Open call. Every
// statement is idempotent (CREATE TABLE / CREATE INDEX with IF NOT EXISTS)
// so re-opening an existing database is a no-op.
//
//   - system_metrics.ts is the PRIMARY KEY (it's a single host metric per
//     timestamp); duplicate-second writes use INSERT OR REPLACE.
//   - traffic_metrics is the same shape but for wg0 cumulative byte
//     counters.
//   - client_traffic has a composite PK (ts, public_key) because every
//     poll snapshot writes one row per peer. The auxiliary index on ts
//     accelerates the common range scan QueryClientTraffic (per-key,
//     hit on every chart refresh) and QueryClientTrafficAll (all-keys,
//     used by retention sweeps) issue — both narrow ts via BETWEEN
//     before ORDER BY ts. Without the index SQLite would scan the
//     whole composite-PK b-tree to find a time slice. A
//     (public_key, ts) covering index would let the per-key variant
//     skip the public_key filter pass entirely, but the current shape
//     hasn't shown up as a hot spot — defer until profile evidence
//     justifies the extra write cost.
//   - handshake_events records a row per detected WireGuard handshake
//     transition (poller-driven; see Slice 10). Composite PK (ts,
//     public_key) lets two peers handshake in the same second without
//     conflict, while the same peer twice-in-a-second is deduped via
//     INSERT OR REPLACE. The auxiliary ts index serves the same
//     range-scan role as on client_traffic.
const schema = `
CREATE TABLE IF NOT EXISTS system_metrics (
    ts      INTEGER PRIMARY KEY,
    cpu_pct REAL    NOT NULL,
    mem_pct REAL    NOT NULL
);

CREATE TABLE IF NOT EXISTS traffic_metrics (
    ts            INTEGER PRIMARY KEY,
    rx_bytes_cum  INTEGER NOT NULL,
    tx_bytes_cum  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS client_traffic (
    ts            INTEGER NOT NULL,
    public_key    TEXT    NOT NULL,
    name          TEXT    NOT NULL,
    address       TEXT    NOT NULL,
    rx_bytes_cum  INTEGER NOT NULL,
    tx_bytes_cum  INTEGER NOT NULL,
    PRIMARY KEY (ts, public_key)
);

CREATE INDEX IF NOT EXISTS idx_client_traffic_ts ON client_traffic(ts);

CREATE TABLE IF NOT EXISTS handshake_events (
    ts          INTEGER NOT NULL,
    public_key  TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    PRIMARY KEY (ts, public_key)
);

CREATE INDEX IF NOT EXISTS idx_handshake_events_ts ON handshake_events(ts);
`

// SystemMetric is one row of CPU% / memory% at a timestamp. TS is
// returned in UTC by the Query helpers and converted to unix seconds on
// insert; callers should not assume any particular wall-clock zone.
type SystemMetric struct {
	TS     time.Time
	CPUPct float64
	MemPct float64
}

// TrafficMetric is one row of wg0 cumulative rx/tx bytes at a timestamp.
// "Cumulative" means counter-since-interface-up, matching what the proc
// package reads from /sys/class/net/wg0/statistics/{rx,tx}_bytes — the
// chart code subtracts neighbouring rows to derive a rate.
type TrafficMetric struct {
	TS         time.Time
	RxBytesCum int64
	TxBytesCum int64
}

// ClientTraffic is one row of a peer's cumulative rx/tx at a timestamp.
// PublicKey identifies the peer (immutable across name/address renames);
// Name and Address are denormalised onto the row at write time so a
// historical chart doesn't need to join against a clients table whose
// rows may have been edited or removed since the sample was taken.
type ClientTraffic struct {
	TS         time.Time
	PublicKey  string
	Name       string
	Address    string
	RxBytesCum int64
	TxBytesCum int64
}

// HandshakeEvent is one row recording that a peer's latest-handshake
// timestamp transitioned to a newer value at TS — i.e. a fresh WireGuard
// handshake completed. PublicKey identifies the peer; Name is the
// manifest-resolved label at the time of the event (or the public key
// itself, by convention, when the peer is "unknown" — present in
// `wg show` but absent from clients.json).
type HandshakeEvent struct {
	TS        time.Time
	PublicKey string
	Name      string
}

// DB is the package's single public handle. It wraps a *sql.DB with the
// driver, pool tuning, and pragmas already applied. Construct with Open;
// the zero value is not usable.
type DB struct {
	sql *sql.DB
}

// Open opens (or creates) a SQLite database file at path and runs the
// bootstrap CREATE TABLE IF NOT EXISTS migrations. The returned *DB is
// safe for concurrent use; callers must Close() it on shutdown.
//
// path can also be ":memory:" (or any modernc.org/sqlite-recognised DSN
// fragment) for tests — the WAL pragma is harmless on in-memory dbs.
//
// Pragmas baked into the DSN:
//
//   - journal_mode=WAL — readers don't block the writer; commits hit the
//     write-ahead log instead of rewriting the main DB file in place.
//   - busy_timeout=5000 — when SQLite would otherwise return SQLITE_BUSY
//     it instead sleeps and retries for up to 5s. With MaxOpenConns=1
//     this is mostly belt-and-braces, but it covers the case where a
//     concurrent process (e.g. a future sqlite3 CLI poke) holds a lock.
//
// Pool sizing: MaxOpenConns(1) and MaxIdleConns(1). Because SQLite
// serialises writes anyway, more connections only buys us "database is
// locked" surprises under contention, not throughput.
func Open(ctx context.Context, path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"

	sqlDB, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}

	// Single-connection pool; see package doc and Open doc above.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	// Surface a connect failure now rather than on the first Insert.
	// modernc.org/sqlite's Open is lazy — without a Ping the file isn't
	// actually touched until the first query.
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}

	if _, err := sqlDB.ExecContext(ctx, schema); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return &DB{sql: sqlDB}, nil
}

// Close closes the underlying *sql.DB. After Close the *DB must not be
// used. Idempotent only insofar as *sql.DB.Close is — calling twice will
// return an error on the second call.
func (d *DB) Close() error {
	return d.sql.Close()
}

// InsertSystemMetric writes one system_metrics row. Uses INSERT OR
// REPLACE so a duplicate ts (e.g. the poller fires twice in the same
// wall-clock second after a clock step) overwrites cleanly instead of
// failing the unique constraint.
func (d *DB) InsertSystemMetric(ctx context.Context, m SystemMetric) error {
	const q = `INSERT OR REPLACE INTO system_metrics (ts, cpu_pct, mem_pct) VALUES (?, ?, ?)`
	if _, err := d.sql.ExecContext(ctx, q, m.TS.Unix(), m.CPUPct, m.MemPct); err != nil {
		return fmt.Errorf("insert system_metrics: %w", err)
	}
	return nil
}

// InsertTrafficMetric writes one traffic_metrics row (wg0 cumulative
// rx/tx). Same INSERT OR REPLACE behaviour as InsertSystemMetric.
func (d *DB) InsertTrafficMetric(ctx context.Context, m TrafficMetric) error {
	const q = `INSERT OR REPLACE INTO traffic_metrics (ts, rx_bytes_cum, tx_bytes_cum) VALUES (?, ?, ?)`
	if _, err := d.sql.ExecContext(ctx, q, m.TS.Unix(), m.RxBytesCum, m.TxBytesCum); err != nil {
		return fmt.Errorf("insert traffic_metrics: %w", err)
	}
	return nil
}

// InsertClientTraffic writes a batch of client_traffic rows in a single
// transaction. The poller emits N rows per snapshot (one per peer), and
// wrapping them in BEGIN/COMMIT both halves the per-row fsync overhead
// and gives the snapshot atomicity — either every peer at this ts is
// visible to a concurrent reader, or none of them are.
//
// On any insert failure the tx rolls back and the original error is
// wrapped and returned; partial snapshots never make it to disk.
//
// An empty slice is a no-op (no tx is opened).
func (d *DB) InsertClientTraffic(ctx context.Context, rows []ClientTraffic) error {
	if len(rows) == 0 {
		return nil
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin client_traffic tx: %w", err)
	}

	const q = `INSERT OR REPLACE INTO client_traffic (ts, public_key, name, address, rx_bytes_cum, tx_bytes_cum) VALUES (?, ?, ?, ?, ?, ?)`

	// Prepare once, exec N times — saves re-parsing the statement on
	// every row. Prepared statement is bound to the tx so it's torn
	// down with the tx, not leaked into the connection pool.
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		// Rollback's own error is intentionally swallowed: the caller
		// cares about the prepare failure that caused the rollback,
		// not the rollback's success.
		_ = tx.Rollback()
		return fmt.Errorf("prepare client_traffic insert: %w", err)
	}
	defer stmt.Close()

	for i, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.TS.Unix(), r.PublicKey, r.Name, r.Address, r.RxBytesCum, r.TxBytesCum); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert client_traffic row %d (key=%s): %w", i, r.PublicKey, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit client_traffic tx: %w", err)
	}
	return nil
}

// QuerySystemMetrics returns system_metrics rows with ts in [from, to]
// inclusive, ordered ASC by ts. The Go-side bounds are converted to unix
// seconds; the returned TS fields are time.Unix(...).UTC().
func (d *DB) QuerySystemMetrics(ctx context.Context, from, to time.Time) ([]SystemMetric, error) {
	const q = `SELECT ts, cpu_pct, mem_pct FROM system_metrics WHERE ts BETWEEN ? AND ? ORDER BY ts ASC`

	rows, err := d.sql.QueryContext(ctx, q, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("query system_metrics: %w", err)
	}
	defer rows.Close()

	var out []SystemMetric
	for rows.Next() {
		var (
			ts     int64
			cpuPct float64
			memPct float64
		)
		if err := rows.Scan(&ts, &cpuPct, &memPct); err != nil {
			return nil, fmt.Errorf("scan system_metrics: %w", err)
		}
		out = append(out, SystemMetric{
			TS:     time.Unix(ts, 0).UTC(),
			CPUPct: cpuPct,
			MemPct: memPct,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate system_metrics: %w", err)
	}
	return out, nil
}

// QueryTrafficMetrics returns traffic_metrics rows with ts in [from, to]
// inclusive, ordered ASC by ts.
func (d *DB) QueryTrafficMetrics(ctx context.Context, from, to time.Time) ([]TrafficMetric, error) {
	const q = `SELECT ts, rx_bytes_cum, tx_bytes_cum FROM traffic_metrics WHERE ts BETWEEN ? AND ? ORDER BY ts ASC`

	rows, err := d.sql.QueryContext(ctx, q, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("query traffic_metrics: %w", err)
	}
	defer rows.Close()

	var out []TrafficMetric
	for rows.Next() {
		var (
			ts         int64
			rxBytesCum int64
			txBytesCum int64
		)
		if err := rows.Scan(&ts, &rxBytesCum, &txBytesCum); err != nil {
			return nil, fmt.Errorf("scan traffic_metrics: %w", err)
		}
		out = append(out, TrafficMetric{
			TS:         time.Unix(ts, 0).UTC(),
			RxBytesCum: rxBytesCum,
			TxBytesCum: txBytesCum,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate traffic_metrics: %w", err)
	}
	return out, nil
}

// QueryClientTrafficAll returns client_traffic rows with ts in [from, to]
// inclusive across every peer, ordered ASC by ts. Multiple rows share the
// same ts (one per peer in a snapshot); secondary ordering is unspecified
// — callers that care should sort by PublicKey themselves.
//
// This is the all-keys variant used by the retention sweep tests and
// future cross-peer aggregations. For single-peer chart queries hit on
// every dashboard refresh, prefer QueryClientTraffic — it pushes the
// public_key filter into SQLite instead of materialising every peer's
// rows just to discard most of them.
func (d *DB) QueryClientTrafficAll(ctx context.Context, from, to time.Time) ([]ClientTraffic, error) {
	const q = `SELECT ts, public_key, name, address, rx_bytes_cum, tx_bytes_cum FROM client_traffic WHERE ts BETWEEN ? AND ? ORDER BY ts ASC`

	rows, err := d.sql.QueryContext(ctx, q, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("query client_traffic: %w", err)
	}
	defer rows.Close()

	var out []ClientTraffic
	for rows.Next() {
		var (
			ts         int64
			publicKey  string
			name       string
			address    string
			rxBytesCum int64
			txBytesCum int64
		)
		if err := rows.Scan(&ts, &publicKey, &name, &address, &rxBytesCum, &txBytesCum); err != nil {
			return nil, fmt.Errorf("scan client_traffic: %w", err)
		}
		out = append(out, ClientTraffic{
			TS:         time.Unix(ts, 0).UTC(),
			PublicKey:  publicKey,
			Name:       name,
			Address:    address,
			RxBytesCum: rxBytesCum,
			TxBytesCum: txBytesCum,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client_traffic: %w", err)
	}
	return out, nil
}

// QueryClientTraffic returns client_traffic rows for a single peer with
// ts in [from, to] inclusive, ordered ASC by ts. This is the per-key
// range scan the per-client chart endpoint hits on every refresh.
//
// The query relies on idx_client_traffic_ts to narrow the ts range
// first, then filters on public_key. A dedicated (public_key, ts)
// covering index could skip the post-filter step entirely, but adding
// one is an optimisation question with write-amplification trade-offs
// — leave until profile evidence justifies it.
func (d *DB) QueryClientTraffic(ctx context.Context, publicKey string, from, to time.Time) ([]ClientTraffic, error) {
	const q = `SELECT ts, public_key, name, address, rx_bytes_cum, tx_bytes_cum FROM client_traffic WHERE public_key = ? AND ts BETWEEN ? AND ? ORDER BY ts ASC`

	rows, err := d.sql.QueryContext(ctx, q, publicKey, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("query client_traffic: %w", err)
	}
	defer rows.Close()

	var out []ClientTraffic
	for rows.Next() {
		var (
			ts         int64
			publicKey  string
			name       string
			address    string
			rxBytesCum int64
			txBytesCum int64
		)
		if err := rows.Scan(&ts, &publicKey, &name, &address, &rxBytesCum, &txBytesCum); err != nil {
			return nil, fmt.Errorf("scan client_traffic: %w", err)
		}
		out = append(out, ClientTraffic{
			TS:         time.Unix(ts, 0).UTC(),
			PublicKey:  publicKey,
			Name:       name,
			Address:    address,
			RxBytesCum: rxBytesCum,
			TxBytesCum: txBytesCum,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client_traffic: %w", err)
	}
	return out, nil
}

// InsertHandshakeEvents writes a batch of handshake_events rows in a
// single transaction. Mirrors InsertClientTraffic: empty/nil slice is a
// no-op (no tx is opened); a batch of N rows opens one tx and prepares
// the statement once.
//
// INSERT OR REPLACE means a duplicate (ts, public_key) silently
// overwrites — the caller can re-emit a previously-seen event without
// needing to track whether it was already persisted.
func (d *DB) InsertHandshakeEvents(ctx context.Context, events []HandshakeEvent) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin handshake_events tx: %w", err)
	}

	const q = `INSERT OR REPLACE INTO handshake_events (ts, public_key, name) VALUES (?, ?, ?)`

	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare handshake_events insert: %w", err)
	}
	defer stmt.Close()

	for i, e := range events {
		if _, err := stmt.ExecContext(ctx, e.TS.Unix(), e.PublicKey, e.Name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert handshake_events row %d (key=%s): %w", i, e.PublicKey, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit handshake_events tx: %w", err)
	}
	return nil
}

// QueryHandshakeEvents returns the most recent handshake events with ts
// in [from, to] inclusive, newest-first. If limit > 0, returns at most
// that many rows; if limit <= 0, returns all matching rows.
//
// Ordering is ts DESC, public_key DESC. The DESC tiebreaker on public_key
// keeps ordering deterministic when two peers handshake in the same
// second — paired with the DESC primary order it preserves the same
// stability the previous ASC/ASC ordering had, just inverted.
func (d *DB) QueryHandshakeEvents(ctx context.Context, from, to time.Time, limit int) ([]HandshakeEvent, error) {
	q := `SELECT ts, public_key, name FROM handshake_events WHERE ts BETWEEN ? AND ? ORDER BY ts DESC, public_key DESC`
	args := []any{from.Unix(), to.Unix()}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query handshake_events: %w", err)
	}
	defer rows.Close()

	var out []HandshakeEvent
	for rows.Next() {
		var (
			ts        int64
			publicKey string
			name      string
		)
		if err := rows.Scan(&ts, &publicKey, &name); err != nil {
			return nil, fmt.Errorf("scan handshake_events: %w", err)
		}
		out = append(out, HandshakeEvent{
			TS:        time.Unix(ts, 0).UTC(),
			PublicKey: publicKey,
			Name:      name,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate handshake_events: %w", err)
	}
	return out, nil
}

// PruneBefore deletes rows with ts < cutoff across all time-series
// tables and returns the total number of rows deleted. The DELETEs run
// inside a single transaction so retention sweeps are atomic — the
// dashboard never observes a partially-pruned db where, say,
// system_metrics has been thinned but traffic_metrics still holds the
// matching ancient rows.
//
// Cutoff is exclusive: rows with ts == cutoff.Unix() are kept (they're
// still inside the retention window). This matches the natural reading
// of "anything older than cutoff".
func (d *DB) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin prune tx: %w", err)
	}

	cutoffUnix := cutoff.Unix()

	// Each DELETE returns its own RowsAffected; we sum them so callers
	// can log "pruned N rows" in one number rather than four.
	tables := []string{"system_metrics", "traffic_metrics", "client_traffic", "handshake_events"}

	var total int64
	for _, table := range tables {
		// Table names cannot be parameterised in SQL so we splice
		// them in — safe because the slice is a hardcoded literal,
		// not user input.
		q := fmt.Sprintf(`DELETE FROM %s WHERE ts < ?`, table)
		res, err := tx.ExecContext(ctx, q, cutoffUnix)
		if err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("prune %s: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			// modernc.org/sqlite always reports RowsAffected, so
			// hitting this would mean a driver bug; surface it
			// rather than silently undercount.
			_ = tx.Rollback()
			return 0, fmt.Errorf("rows affected for %s: %w", table, err)
		}
		total += n
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit prune tx: %w", err)
	}
	return total, nil
}

// ErrNotInitialised is returned by callers that try to use a *DB before
// Open completed. Reserved for future use — kept exported so dependent
// packages can errors.Is against it without relying on string matching.
//
// (Currently unused; Open returns the wrapped sql.Open / Ping / schema
// error directly. Defined here so the package's error surface is
// discoverable in one place.)
var ErrNotInitialised = errors.New("db: not initialised")
