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
//   - clients is the runtime source of truth for WireGuard peers
//     (Slice 1 of spec 015) — distinct from the client_traffic metrics
//     table. id is a surrogate AUTOINCREMENT key because name is a
//     mutable, user-facing label; name / public_key / address each carry
//     a UNIQUE constraint so a duplicate add surfaces as an insert error
//     rather than a silent overwrite. created_at / updated_at are
//     unix-seconds, caller-stamped at the API boundary like the metric
//     structs' ts; disabled peers (enabled=0) are retained in the table
//     but later omitted when rendering wg0.conf.
//   - managed_baseline is the dashboard-owned "last Terraform-applied set"
//     (spec 017, Slice 3): ReplaceClientsAndBaseline overwrites it, in the
//     same transaction as the clients-table reconcile, with exactly the
//     entries a successful PUT /api/clients was given. It is intentionally
//     NOT the clients table itself — the baseline must freeze the
//     git-declared shape (name/address/public_key only) independent of
//     any subsequent UI edit to the live clients row, so computeDrift can
//     diff "live" against "last git-applied" rather than "live" against
//     "live". id is a surrogate AUTOINCREMENT key for the same reason as
//     clients.id (there is no natural single-column key to hang UPDATEs
//     off during a replace); no UNIQUE constraints because a whole-set
//     replace always clears the table first (see
//     replaceManagedBaselineTx) rather than reconciling row-by-row.
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

CREATE TABLE IF NOT EXISTS clients (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    public_key  TEXT    NOT NULL UNIQUE,
    address     TEXT    NOT NULL UNIQUE,
    enabled     INTEGER NOT NULL DEFAULT 1,
    note        TEXT,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS managed_baseline (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    address     TEXT    NOT NULL,
    public_key  TEXT    NOT NULL
);
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

// Client is one WireGuard peer in the runtime clients table (Slice 1 of
// spec 015). ID is the surrogate primary key; Name, PublicKey, and Address
// each map to a UNIQUE column. Enabled is stored as 0/1 on disk and exposed
// as a bool here. Note is free-text and nullable — sql.NullString round-
// trips an absent note as (Valid=false) rather than an empty string.
//
// CreatedAt / UpdatedAt are unix-seconds on disk and time.Time (UTC) at the
// API boundary, matching the metric structs' ts convention. They are
// caller-stamped: InsertClient and UpdateClient persist exactly the values
// supplied on the struct (the orchestration layer in a later slice is
// responsible for setting CreatedAt at creation and bumping UpdatedAt on
// every edit), keeping this package free of an implicit clock dependency.
type Client struct {
	ID        int64
	Name      string
	PublicKey string
	Address   string
	Enabled   bool
	Note      sql.NullString
	CreatedAt time.Time
	UpdatedAt time.Time
}

// BaselineEntry is one row of the dashboard-owned managed_baseline table
// (spec 017, Slice 3) — the {name, address, public_key} projection of the
// peer set last applied via a successful PUT /api/clients. It intentionally
// mirrors clients.ExportEntry's shape (this package doesn't import
// internal/clients to avoid a cycle; the two stay in sync by convention,
// checked by the ReplaceAll round-trip tests in internal/clients).
type BaselineEntry struct {
	Name      string
	Address   string
	PublicKey string
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

// QueryLatestHandshakePerPeer returns at most one HandshakeEvent per
// public_key — each peer's most-recent handshake (MAX(ts)) within [from, to]
// inclusive — ordered newest-first. If limit > 0, returns at most that many
// peers; if limit <= 0, returns all matching peers.
//
// This backs the "Recent handshakes" panel (spec 016, 2.3): the operator
// wants "who was last seen, once per peer", not the full per-handshake history
// QueryHandshakeEvents returns. GROUP BY public_key collapses a peer's
// repeated rows; SQLite's documented min/max bare-column rule makes the
// selected name come from the same row that supplied MAX(ts). Ordering is ts
// DESC, public_key DESC — the same deterministic tiebreaker as
// QueryHandshakeEvents, so two peers whose latest handshake landed in the same
// second order stably.
func (d *DB) QueryLatestHandshakePerPeer(ctx context.Context, from, to time.Time, limit int) ([]HandshakeEvent, error) {
	q := `SELECT MAX(ts) AS ts, public_key, name FROM handshake_events WHERE ts BETWEEN ? AND ? GROUP BY public_key ORDER BY ts DESC, public_key DESC`
	args := []any{from.Unix(), to.Unix()}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query latest handshake per peer: %w", err)
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
			return nil, fmt.Errorf("scan latest handshake per peer: %w", err)
		}
		out = append(out, HandshakeEvent{
			TS:        time.Unix(ts, 0).UTC(),
			PublicKey: publicKey,
			Name:      name,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest handshake per peer: %w", err)
	}
	return out, nil
}

// QueryHandshakeEventsByKey returns handshake_events for a single peer with
// ts in [from, to] inclusive, ordered ASC by ts. The query reuses
// idx_handshake_events_ts to narrow the ts range, then filters on
// public_key — same push-filter pattern as QueryClientTraffic. Empty result
// (no events in range) is not an error; callers receive a nil slice and nil
// error, matching the other zero-row query helpers.
func (d *DB) QueryHandshakeEventsByKey(ctx context.Context, publicKey string, from, to time.Time) ([]HandshakeEvent, error) {
	const q = `SELECT ts, public_key, name FROM handshake_events WHERE public_key = ? AND ts BETWEEN ? AND ? ORDER BY ts ASC`

	rows, err := d.sql.QueryContext(ctx, q, publicKey, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("query handshake_events by key: %w", err)
	}
	defer rows.Close()

	var out []HandshakeEvent
	for rows.Next() {
		var (
			ts     int64
			pubKey string
			name   string
		)
		if err := rows.Scan(&ts, &pubKey, &name); err != nil {
			return nil, fmt.Errorf("scan handshake_events by key: %w", err)
		}
		out = append(out, HandshakeEvent{
			TS:        time.Unix(ts, 0).UTC(),
			PublicKey: pubKey,
			Name:      name,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate handshake_events by key: %w", err)
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

// ListClients returns every row in the clients table ordered by address
// then id, giving the UI a stable, deterministic peer list. An empty table
// yields a nil slice and nil error (not an error), matching the zero-row
// behaviour of the metric query helpers.
func (d *DB) ListClients(ctx context.Context) ([]Client, error) {
	const q = `SELECT id, name, public_key, address, enabled, note, created_at, updated_at FROM clients ORDER BY address ASC, id ASC`

	rows, err := d.sql.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query clients: %w", err)
	}
	defer rows.Close()

	var out []Client
	for rows.Next() {
		var (
			c         Client
			enabled   int64
			createdAt int64
			updatedAt int64
		)
		if err := rows.Scan(&c.ID, &c.Name, &c.PublicKey, &c.Address, &enabled, &c.Note, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan clients: %w", err)
		}
		c.Enabled = enabled != 0
		c.CreatedAt = time.Unix(createdAt, 0).UTC()
		c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate clients: %w", err)
	}
	return out, nil
}

// InsertClient writes one client row. ID is assigned by SQLite's
// AUTOINCREMENT and ignored on input. Enabled is encoded as 0/1; CreatedAt
// and UpdatedAt are persisted as the unix-seconds of the supplied times
// (caller-stamped — see the Client doc). A UNIQUE-constraint violation on
// name, public_key, or address surfaces as a wrapped error rather than a
// silent overwrite: unlike the metric inserts there is deliberately no
// INSERT OR REPLACE here.
func (d *DB) InsertClient(ctx context.Context, c Client) error {
	const q = `INSERT INTO clients (name, public_key, address, enabled, note, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	if _, err := d.sql.ExecContext(ctx, q, c.Name, c.PublicKey, c.Address, b2i(c.Enabled), c.Note, c.CreatedAt.Unix(), c.UpdatedAt.Unix()); err != nil {
		return fmt.Errorf("insert clients: %w", err)
	}
	return nil
}

// UpdateClient updates the mutable columns of the client identified by ID
// (name is user-mutable, so the surrogate id is the stable key). It
// persists the supplied UpdatedAt — the caller is responsible for bumping
// it to "now" before the call. CreatedAt is intentionally left untouched.
// A UNIQUE-constraint violation (e.g. renaming onto another client's name)
// surfaces as a wrapped error.
func (d *DB) UpdateClient(ctx context.Context, c Client) error {
	const q = `UPDATE clients SET name = ?, public_key = ?, address = ?, enabled = ?, note = ?, updated_at = ? WHERE id = ?`
	if _, err := d.sql.ExecContext(ctx, q, c.Name, c.PublicKey, c.Address, b2i(c.Enabled), c.Note, c.UpdatedAt.Unix(), c.ID); err != nil {
		return fmt.Errorf("update clients: %w", err)
	}
	return nil
}

// DeleteClient removes the client with the given name. Deleting a
// non-existent name is not an error (zero rows affected), matching the
// idempotent posture of the prune sweep.
func (d *DB) DeleteClient(ctx context.Context, name string) error {
	const q = `DELETE FROM clients WHERE name = ?`
	if _, err := d.sql.ExecContext(ctx, q, name); err != nil {
		return fmt.Errorf("delete clients: %w", err)
	}
	return nil
}

// ReplaceClients reconciles the clients table to exactly match want, keyed by
// public_key, in a single transaction — the whole-set replace spec 017 hands
// to the Terraform-driven bulk-PUT endpoint. Unlike InsertClient there is no
// UNIQUE-violation-as-caller-error contract here: the caller (clients.Service)
// is expected to have already validated want for intra-payload uniqueness.
//
// Reconciliation, by public_key:
//   - present in the table but absent from want: DELETE.
//   - present in both but any of name/address/enabled/note changed: UPDATE.
//     CreatedAt and id are preserved — only the mutable columns move.
//   - present in want but absent from the table: INSERT. CreatedAt/UpdatedAt
//     are caller-stamped on the input Client, same convention as InsertClient.
//
// Execution order is deletes -> updates -> inserts. This isn't just tidy
// bookkeeping: SQLite enforces the UNIQUE constraints on name/public_key/
// address per-statement, so a swap (e.g. two peers trading addresses) can
// only succeed if the stale rows are gone before the new values land.
// Updates before inserts means an update that's simply changing a mutable
// field on an existing key can't collide with a brand new row that
// (coincidentally) wants the same UNIQUE value the update is vacating.
//
// All-or-nothing: any failure at any step rolls back the whole transaction,
// leaving the table exactly as it was before the call.
//
// Kept alongside ReplaceClientsAndBaseline (rather than folded into it) so
// callers that only need the peer-table reconcile — today, none in
// production, but every existing db-layer test — don't have to thread a
// baseline argument through.
func (d *DB) ReplaceClients(ctx context.Context, want []Client) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace clients tx: %w", err)
	}

	if err := replaceClientsTx(ctx, tx, want); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace clients tx: %w", err)
	}
	return nil
}

// ReplaceClientsAndBaseline is ReplaceClients plus an atomic overwrite of the
// managed_baseline table (spec 017, Slice 3), both inside one transaction:
// clients.Service.ReplaceAll calls this instead of ReplaceClients so a PUT
// /api/clients that fails partway (e.g. the clients-table UNIQUE-swap edge
// case) rolls back the baseline write too — the baseline and the live peer
// set can never disagree, even under a forced error.
//
// baseline is the exact {name, address, public_key} set to persist as "last
// applied" — clients.Service passes the same entries it validated and handed
// to the clients-table reconcile, not a re-derivation from want, so the two
// are trivially guaranteed to agree.
func (d *DB) ReplaceClientsAndBaseline(ctx context.Context, want []Client, baseline []BaselineEntry) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace clients+baseline tx: %w", err)
	}

	if err := replaceClientsTx(ctx, tx, want); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := replaceManagedBaselineTx(ctx, tx, baseline); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace clients+baseline tx: %w", err)
	}
	return nil
}

// replaceClientsTx does the actual reconcile work against an open tx. Split
// out from ReplaceClients so the rollback-on-error path stays in exactly one
// place (the caller).
func replaceClientsTx(ctx context.Context, tx *sql.Tx, want []Client) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, public_key, address, enabled, note, created_at, updated_at FROM clients`)
	if err != nil {
		return fmt.Errorf("replace clients: query existing: %w", err)
	}
	existing := make(map[string]Client)
	for rows.Next() {
		var (
			c         Client
			enabled   int64
			createdAt int64
			updatedAt int64
		)
		if err := rows.Scan(&c.ID, &c.Name, &c.PublicKey, &c.Address, &enabled, &c.Note, &createdAt, &updatedAt); err != nil {
			rows.Close()
			return fmt.Errorf("replace clients: scan existing: %w", err)
		}
		c.Enabled = enabled != 0
		c.CreatedAt = time.Unix(createdAt, 0).UTC()
		c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		existing[c.PublicKey] = c
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("replace clients: iterate existing: %w", err)
	}
	rows.Close()

	wantByKey := make(map[string]Client, len(want))
	for _, c := range want {
		wantByKey[c.PublicKey] = c
	}

	// Deletes: present in existing, absent from want.
	for key, old := range existing {
		if _, ok := wantByKey[key]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, old.ID); err != nil {
			return fmt.Errorf("replace clients: delete %q (key=%s): %w", old.Name, key, err)
		}
	}

	// Updates: present in both, preserving id/created_at, only rewriting the
	// mutable columns when something actually changed.
	for key, next := range wantByKey {
		old, ok := existing[key]
		if !ok {
			continue
		}
		if old.Name == next.Name && old.Address == next.Address && old.Enabled == next.Enabled && old.Note == next.Note {
			continue
		}
		const q = `UPDATE clients SET name = ?, address = ?, enabled = ?, note = ?, updated_at = ? WHERE id = ?`
		if _, err := tx.ExecContext(ctx, q, next.Name, next.Address, b2i(next.Enabled), next.Note, next.UpdatedAt.Unix(), old.ID); err != nil {
			return fmt.Errorf("replace clients: update %q (key=%s): %w", next.Name, key, err)
		}
	}

	// Inserts: present in want, absent from existing.
	for key, next := range wantByKey {
		if _, ok := existing[key]; ok {
			continue
		}
		const q = `INSERT INTO clients (name, public_key, address, enabled, note, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q, next.Name, next.PublicKey, next.Address, b2i(next.Enabled), next.Note, next.CreatedAt.Unix(), next.UpdatedAt.Unix()); err != nil {
			return fmt.Errorf("replace clients: insert %q (key=%s): %w", next.Name, key, err)
		}
	}

	return nil
}

// replaceManagedBaselineTx overwrites the managed_baseline table with exactly
// entries, against an open tx — the same clear-then-bulk-insert shape as
// replaceClientsTx's insert half, minus any reconcile-by-key logic: unlike
// clients, the baseline has no id-preservation contract worth keeping (it's a
// pure "last applied" snapshot with no dependent foreign state), so a full
// DELETE + re-INSERT is simplest and correct. An empty entries slice leaves
// the table empty (the DELETE runs; no INSERTs follow) — the same "empty set
// is valid" contract ReplaceClients honours for the clients table.
func replaceManagedBaselineTx(ctx context.Context, tx *sql.Tx, entries []BaselineEntry) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_baseline`); err != nil {
		return fmt.Errorf("replace managed_baseline: clear: %w", err)
	}

	const q = `INSERT INTO managed_baseline (name, address, public_key) VALUES (?, ?, ?)`
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return fmt.Errorf("replace managed_baseline: prepare insert: %w", err)
	}
	defer stmt.Close()

	for i, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.Name, e.Address, e.PublicKey); err != nil {
			return fmt.Errorf("replace managed_baseline: insert row %d (name=%s): %w", i, e.Name, err)
		}
	}
	return nil
}

// LoadManagedBaseline returns the full dashboard-owned baseline set (spec 017,
// Slice 3) — the {name, address, public_key} peers as of the last successful
// PUT /api/clients. An empty table (no PUT has ever succeeded — a fresh box
// pre-first-apply) returns a nil slice and nil error, matching the zero-row
// convention of ListClients and the metric query helpers; computeDrift treats
// a nil/empty result as "no baseline yet" and falls back to clients.json.
func (d *DB) LoadManagedBaseline(ctx context.Context) ([]BaselineEntry, error) {
	const q = `SELECT name, address, public_key FROM managed_baseline ORDER BY address ASC, id ASC`

	rows, err := d.sql.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query managed_baseline: %w", err)
	}
	defer rows.Close()

	var out []BaselineEntry
	for rows.Next() {
		var e BaselineEntry
		if err := rows.Scan(&e.Name, &e.Address, &e.PublicKey); err != nil {
			return nil, fmt.Errorf("scan managed_baseline: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed_baseline: %w", err)
	}
	return out, nil
}

// CountClients returns the total number of rows in the clients table. Used
// by the startup reconcile to decide whether to seed from clients.json (a
// later slice); an empty table returns (0, nil).
func (d *DB) CountClients(ctx context.Context) (int, error) {
	const q = `SELECT COUNT(*) FROM clients`
	var n int
	if err := d.sql.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("count clients: %w", err)
	}
	return n, nil
}

// b2i maps a Go bool to the 0/1 INTEGER encoding the clients.enabled column
// uses on disk.
func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ErrNotInitialised is returned by callers that try to use a *DB before
// Open completed. Reserved for future use — kept exported so dependent
// packages can errors.Is against it without relying on string matching.
//
// (Currently unused; Open returns the wrapped sql.Open / Ping / schema
// error directly. Defined here so the package's error surface is
// discoverable in one place.)
var ErrNotInitialised = errors.New("db: not initialised")
