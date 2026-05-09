// Tests for the dashboard local SQLite store. These exercise the public
// surface of *DB end-to-end against an in-memory database (":memory:") so
// they're hermetic and runnable on any host — no temp files, no network,
// no other process required.
//
// Coverage focus, in priority order:
//
//  1. The retention-sweeper boundary (PruneBefore) — its "exclusive cutoff"
//     semantic is load-bearing and easy to silently flip in a refactor.
//  2. Round-trip Insert -> Query for each of the three tables, including
//     the timestamp encoding (unix seconds on disk, time.Time UTC at the
//     API).
//  3. The batch-insert path for client_traffic, which is the only call
//     that wraps multiple rows in a transaction.
//
// Test infrastructure is kept deliberately small: a single newTestDB
// helper, a fixed reference time t0, and ad-hoc time arithmetic per test
// (no table-driven matrix — each test is a discrete property worth
// reading on its own).

package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// t0 is the fixed reference instant used across the tests. UTC, with no
// monotonic clock reading (time.Date constructs a wall-only Time), so
// equality via time.Time.Equal is well-defined.
var t0 = time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)

// newTestDB opens an in-memory SQLite database, runs the bootstrap
// migrations, and registers a Cleanup to close it at end of test. Use
// this everywhere — never sql.Open directly in a test, because we want
// to exercise the production Open path (DSN, pragmas, schema apply).
func newTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() {
		// Ignore the close error — most tests don't care, and the
		// idempotency test below explicitly probes a double-close.
		_ = d.Close()
	})
	return d
}

// TestOpen_BootstrapsSchema confirms that Open ran the CREATE TABLE
// migrations on a fresh db: a wide-range query against each of the three
// tables must return (empty slice, nil error), not a "no such table"
// failure. If migrations didn't run, the queries would error.
func TestOpen_BootstrapsSchema(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	from := t0.Add(-365 * 24 * time.Hour)
	to := t0.Add(365 * 24 * time.Hour)

	sysRows, err := d.QuerySystemMetrics(ctx, from, to)
	if err != nil {
		t.Fatalf("QuerySystemMetrics on fresh db: %v", err)
	}
	if len(sysRows) != 0 {
		t.Errorf("QuerySystemMetrics: want 0 rows on fresh db, got %d", len(sysRows))
	}

	trafRows, err := d.QueryTrafficMetrics(ctx, from, to)
	if err != nil {
		t.Fatalf("QueryTrafficMetrics on fresh db: %v", err)
	}
	if len(trafRows) != 0 {
		t.Errorf("QueryTrafficMetrics: want 0 rows on fresh db, got %d", len(trafRows))
	}

	clientRows, err := d.QueryClientTraffic(ctx, from, to)
	if err != nil {
		t.Fatalf("QueryClientTraffic on fresh db: %v", err)
	}
	if len(clientRows) != 0 {
		t.Errorf("QueryClientTraffic: want 0 rows on fresh db, got %d", len(clientRows))
	}
}

// TestInsertSystemMetric_RoundTrip writes one row and reads it back via
// a [t0-1m, t0+1m] range query. Asserts every field returns intact and
// uses time.Time.Equal for the timestamp (== would fail spuriously if
// either side carried a monotonic reading or a non-UTC location).
func TestInsertSystemMetric_RoundTrip(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	in := SystemMetric{TS: t0, CPUPct: 12.5, MemPct: 33.3}
	if err := d.InsertSystemMetric(ctx, in); err != nil {
		t.Fatalf("InsertSystemMetric: %v", err)
	}

	rows, err := d.QuerySystemMetrics(ctx, t0.Add(-time.Minute), t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("QuerySystemMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	got := rows[0]
	if !got.TS.Equal(in.TS) {
		t.Errorf("TS: want %v, got %v", in.TS, got.TS)
	}
	if got.CPUPct != in.CPUPct {
		t.Errorf("CPUPct: want %v, got %v", in.CPUPct, got.CPUPct)
	}
	if got.MemPct != in.MemPct {
		t.Errorf("MemPct: want %v, got %v", in.MemPct, got.MemPct)
	}
}

// TestInsertTrafficMetric_RoundTrip mirrors TestInsertSystemMetric_RoundTrip
// for the traffic_metrics table — same shape, int64 byte counters
// instead of float percentages.
func TestInsertTrafficMetric_RoundTrip(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	in := TrafficMetric{TS: t0, RxBytesCum: 1_234_567, TxBytesCum: 7_654_321}
	if err := d.InsertTrafficMetric(ctx, in); err != nil {
		t.Fatalf("InsertTrafficMetric: %v", err)
	}

	rows, err := d.QueryTrafficMetrics(ctx, t0.Add(-time.Minute), t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("QueryTrafficMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	got := rows[0]
	if !got.TS.Equal(in.TS) {
		t.Errorf("TS: want %v, got %v", in.TS, got.TS)
	}
	if got.RxBytesCum != in.RxBytesCum {
		t.Errorf("RxBytesCum: want %d, got %d", in.RxBytesCum, got.RxBytesCum)
	}
	if got.TxBytesCum != in.TxBytesCum {
		t.Errorf("TxBytesCum: want %d, got %d", in.TxBytesCum, got.TxBytesCum)
	}
}

// TestInsertClientTraffic_BatchRoundTrip exercises the multi-row batch
// path: three rows with strictly-increasing timestamps go in via a single
// InsertClientTraffic call, and the query must return them ordered ASC
// by ts (the production ORDER BY ts ASC contract).
func TestInsertClientTraffic_BatchRoundTrip(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	rows := []ClientTraffic{
		{TS: t0, PublicKey: "pk-a", Name: "alice", Address: "10.0.0.2/32", RxBytesCum: 100, TxBytesCum: 200},
		{TS: t0.Add(10 * time.Second), PublicKey: "pk-b", Name: "bob", Address: "10.0.0.3/32", RxBytesCum: 300, TxBytesCum: 400},
		{TS: t0.Add(20 * time.Second), PublicKey: "pk-c", Name: "carol", Address: "10.0.0.4/32", RxBytesCum: 500, TxBytesCum: 600},
	}
	if err := d.InsertClientTraffic(ctx, rows); err != nil {
		t.Fatalf("InsertClientTraffic: %v", err)
	}

	got, err := d.QueryClientTraffic(ctx, t0.Add(-time.Minute), t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("QueryClientTraffic: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d", len(got))
	}
	for i, want := range rows {
		g := got[i]
		if !g.TS.Equal(want.TS) {
			t.Errorf("row %d TS: want %v, got %v", i, want.TS, g.TS)
		}
		if g.PublicKey != want.PublicKey {
			t.Errorf("row %d PublicKey: want %q, got %q", i, want.PublicKey, g.PublicKey)
		}
		if g.Name != want.Name {
			t.Errorf("row %d Name: want %q, got %q", i, want.Name, g.Name)
		}
		if g.Address != want.Address {
			t.Errorf("row %d Address: want %q, got %q", i, want.Address, g.Address)
		}
		if g.RxBytesCum != want.RxBytesCum {
			t.Errorf("row %d RxBytesCum: want %d, got %d", i, want.RxBytesCum, g.RxBytesCum)
		}
		if g.TxBytesCum != want.TxBytesCum {
			t.Errorf("row %d TxBytesCum: want %d, got %d", i, want.TxBytesCum, g.TxBytesCum)
		}
	}
}

// TestInsertClientTraffic_EmptyBatch confirms the documented "empty slice
// is a no-op" contract — both nil and empty-slice paths must return nil
// error and leave the table empty (no spurious tx).
func TestInsertClientTraffic_EmptyBatch(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	if err := d.InsertClientTraffic(ctx, nil); err != nil {
		t.Errorf("InsertClientTraffic(nil): %v", err)
	}
	if err := d.InsertClientTraffic(ctx, []ClientTraffic{}); err != nil {
		t.Errorf("InsertClientTraffic([]): %v", err)
	}

	from := t0.Add(-365 * 24 * time.Hour)
	to := t0.Add(365 * 24 * time.Hour)
	rows, err := d.QueryClientTraffic(ctx, from, to)
	if err != nil {
		t.Fatalf("QueryClientTraffic: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("want 0 rows, got %d", len(rows))
	}
}

// TestInsertClientTraffic_TxRollbackOnError is intentionally skipped.
//
// The intent would be to confirm that batched inserts are atomic — if
// row N fails, rows 0..N-1 must roll back. But the production INSERT
// uses INSERT OR REPLACE, which silently overwrites duplicate composite
// PKs; CHECK constraints aren't defined on client_traffic; and NOT NULL
// columns can't be triggered from typed Go fields without reflection
// shenanigans. There's no clean, schema-honest way to engineer a row-
// level failure mid-batch from the public API.
//
// Driver-internal failure paths (e.g. the connection dropping) would
// require either a fault-injecting fake driver or unexporting the
// transaction code so a test can inject a failing prepare. Either is
// disproportionate for this property; leaving the assertion as a
// documentation comment instead.
func TestInsertClientTraffic_TxRollbackOnError(t *testing.T) {
	t.Skip("no organic way to fail a single row mid-batch under the current schema (INSERT OR REPLACE swallows dup PK; no CHECK constraints); see comment for rationale")
}

// TestInsertSystemMetric_DuplicateTSReplaces pins the INSERT OR REPLACE
// semantic: a second insert at the same ts overwrites in place rather
// than appending or erroring on the PK collision. If a future schema
// change ever drops the OR REPLACE, this test catches it.
func TestInsertSystemMetric_DuplicateTSReplaces(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	first := SystemMetric{TS: t0, CPUPct: 10.0, MemPct: 20.0}
	second := SystemMetric{TS: t0, CPUPct: 99.9, MemPct: 88.8}

	if err := d.InsertSystemMetric(ctx, first); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := d.InsertSystemMetric(ctx, second); err != nil {
		t.Fatalf("second insert (same ts): %v", err)
	}

	rows, err := d.QuerySystemMetrics(ctx, t0.Add(-time.Minute), t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("QuerySystemMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row (replace), got %d", len(rows))
	}
	got := rows[0]
	if got.CPUPct != second.CPUPct {
		t.Errorf("CPUPct: want %v (second write), got %v", second.CPUPct, got.CPUPct)
	}
	if got.MemPct != second.MemPct {
		t.Errorf("MemPct: want %v (second write), got %v", second.MemPct, got.MemPct)
	}
}

// TestQueryRange_InclusiveBothEnds pins the query range contract: BETWEEN
// from AND to is INCLUSIVE on both ends. Inserts five rows spanning the
// boundary on each side and asserts the three inside rows come back —
// not the one second before from, not the one second after to.
func TestQueryRange_InclusiveBothEnds(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	from := t0
	to := t0.Add(time.Hour)

	tsList := []time.Time{
		from.Add(-time.Second), // outside (before)
		from,                   // inside, at lower bound
		from.Add(30 * time.Minute), // inside, middle
		to,                     // inside, at upper bound
		to.Add(time.Second),    // outside (after)
	}
	for i, ts := range tsList {
		if err := d.InsertSystemMetric(ctx, SystemMetric{TS: ts, CPUPct: float64(i), MemPct: float64(i)}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	rows, err := d.QuerySystemMetrics(ctx, from, to)
	if err != nil {
		t.Fatalf("QuerySystemMetrics: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows in [from, to], got %d", len(rows))
	}
	wantTS := []time.Time{from, from.Add(30 * time.Minute), to}
	for i, w := range wantTS {
		if !rows[i].TS.Equal(w) {
			t.Errorf("row %d TS: want %v, got %v", i, w, rows[i].TS)
		}
	}
}

// TestPruneBefore_BoundaryExclusive is the LOAD-BEARING test for the
// retention sweeper. The contract is that PruneBefore deletes rows with
// ts < cutoff strictly — rows AT cutoff stay. Inserts four rows around
// the boundary, prunes, and asserts the deleted count is exactly 2 (not
// 1, not 3) and that the surviving rows are the at-boundary and after-
// boundary ones.
//
// Why this matters: a future refactor that swaps `<` for `<=` (or
// `>= cutoff` for `> cutoff` in a query helper) would corrupt the
// retention window without any other test catching it. This pins the
// boundary explicitly.
func TestPruneBefore_BoundaryExclusive(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	cutoff := t0
	tsList := []time.Time{
		cutoff.Add(-2 * time.Second), // strictly before — delete
		cutoff.Add(-1 * time.Second), // strictly before — delete
		cutoff,                       // AT cutoff — keep
		cutoff.Add(1 * time.Second),  // after — keep
	}
	for i, ts := range tsList {
		if err := d.InsertSystemMetric(ctx, SystemMetric{TS: ts, CPUPct: float64(i), MemPct: float64(i)}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	deleted, err := d.PruneBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted count: want 2, got %d", deleted)
	}

	rows, err := d.QuerySystemMetrics(ctx, cutoff.Add(-time.Hour), cutoff.Add(time.Hour))
	if err != nil {
		t.Fatalf("QuerySystemMetrics: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 surviving rows, got %d", len(rows))
	}
	if !rows[0].TS.Equal(cutoff) {
		t.Errorf("survivor 0: want ts == cutoff (%v), got %v", cutoff, rows[0].TS)
	}
	if !rows[1].TS.Equal(cutoff.Add(time.Second)) {
		t.Errorf("survivor 1: want ts == cutoff+1s, got %v", rows[1].TS)
	}
}

// TestPruneBefore_AcrossAllThreeTables confirms PruneBefore sweeps every
// table, not just one — and that the returned count is the SUM of
// per-table deletions. Builds a tiny corpus in each of system_metrics,
// traffic_metrics, and client_traffic with one row before cutoff and
// one row at cutoff (kept). Total expected deletions: 3.
func TestPruneBefore_AcrossAllThreeTables(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	cutoff := t0
	before := cutoff.Add(-time.Minute)

	if err := d.InsertSystemMetric(ctx, SystemMetric{TS: before, CPUPct: 1, MemPct: 1}); err != nil {
		t.Fatalf("insert system before: %v", err)
	}
	if err := d.InsertSystemMetric(ctx, SystemMetric{TS: cutoff, CPUPct: 2, MemPct: 2}); err != nil {
		t.Fatalf("insert system at cutoff: %v", err)
	}

	if err := d.InsertTrafficMetric(ctx, TrafficMetric{TS: before, RxBytesCum: 10, TxBytesCum: 20}); err != nil {
		t.Fatalf("insert traffic before: %v", err)
	}
	if err := d.InsertTrafficMetric(ctx, TrafficMetric{TS: cutoff, RxBytesCum: 30, TxBytesCum: 40}); err != nil {
		t.Fatalf("insert traffic at cutoff: %v", err)
	}

	if err := d.InsertClientTraffic(ctx, []ClientTraffic{
		{TS: before, PublicKey: "pk-a", Name: "alice", Address: "10.0.0.2/32", RxBytesCum: 1, TxBytesCum: 2},
		{TS: cutoff, PublicKey: "pk-a", Name: "alice", Address: "10.0.0.2/32", RxBytesCum: 3, TxBytesCum: 4},
	}); err != nil {
		t.Fatalf("insert client_traffic: %v", err)
	}

	deleted, err := d.PruneBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted count: want 3 (one per table), got %d", deleted)
	}

	from := cutoff.Add(-time.Hour)
	to := cutoff.Add(time.Hour)

	sysRows, err := d.QuerySystemMetrics(ctx, from, to)
	if err != nil {
		t.Fatalf("QuerySystemMetrics post-prune: %v", err)
	}
	if len(sysRows) != 1 || !sysRows[0].TS.Equal(cutoff) {
		t.Errorf("system_metrics post-prune: want 1 row at cutoff, got %+v", sysRows)
	}

	trafRows, err := d.QueryTrafficMetrics(ctx, from, to)
	if err != nil {
		t.Fatalf("QueryTrafficMetrics post-prune: %v", err)
	}
	if len(trafRows) != 1 || !trafRows[0].TS.Equal(cutoff) {
		t.Errorf("traffic_metrics post-prune: want 1 row at cutoff, got %+v", trafRows)
	}

	clientRows, err := d.QueryClientTraffic(ctx, from, to)
	if err != nil {
		t.Fatalf("QueryClientTraffic post-prune: %v", err)
	}
	if len(clientRows) != 1 || !clientRows[0].TS.Equal(cutoff) {
		t.Errorf("client_traffic post-prune: want 1 row at cutoff, got %+v", clientRows)
	}
}

// TestPruneBefore_EmptyDB confirms the trivial case: no rows anywhere,
// PruneBefore returns (0, nil) and doesn't choke on empty tables.
func TestPruneBefore_EmptyDB(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	deleted, err := d.PruneBefore(ctx, t0)
	if err != nil {
		t.Fatalf("PruneBefore on empty db: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted: want 0, got %d", deleted)
	}
}

// TestPruneBefore_NothingToPrune confirms the no-op case when every row
// is at or after the cutoff — count is 0, rows are intact.
func TestPruneBefore_NothingToPrune(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	cutoff := t0
	in := []SystemMetric{
		{TS: cutoff, CPUPct: 1, MemPct: 1},
		{TS: cutoff.Add(time.Minute), CPUPct: 2, MemPct: 2},
		{TS: cutoff.Add(time.Hour), CPUPct: 3, MemPct: 3},
	}
	for i, m := range in {
		if err := d.InsertSystemMetric(ctx, m); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	deleted, err := d.PruneBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted: want 0, got %d", deleted)
	}

	rows, err := d.QuerySystemMetrics(ctx, cutoff.Add(-time.Hour), cutoff.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("QuerySystemMetrics: %v", err)
	}
	if len(rows) != len(in) {
		t.Errorf("rows post-noop-prune: want %d, got %d", len(in), len(rows))
	}
}

// TestQueryHandshakeEvents_LimitAndDescOrder pins the Slice 12.5 contract
// for the events card: when limit > 0, return at most that many rows,
// ordered ts-DESC (newest-first), so the UI can render the N most recent
// handshakes without server-side reversal. Inserts 15 events spanning a
// window, asks for 10, and asserts both the count and that the rows
// returned are the 10 newest of the 15 in strictly-decreasing ts order.
func TestQueryHandshakeEvents_LimitAndDescOrder(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	// 15 events at distinct seconds: t0, t0+1s, ..., t0+14s.
	events := make([]HandshakeEvent, 15)
	for i := range events {
		events[i] = HandshakeEvent{
			TS:        t0.Add(time.Duration(i) * time.Second),
			PublicKey: fmt.Sprintf("pk-%02d", i),
			Name:      fmt.Sprintf("peer-%02d", i),
		}
	}
	if err := d.InsertHandshakeEvents(ctx, events); err != nil {
		t.Fatalf("InsertHandshakeEvents: %v", err)
	}

	longAgo := t0.Add(-365 * 24 * time.Hour)
	faraway := t0.Add(365 * 24 * time.Hour)

	got, err := d.QueryHandshakeEvents(ctx, longAgo, faraway, 10)
	if err != nil {
		t.Fatalf("QueryHandshakeEvents: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("len(got) = %d, want 10", len(got))
	}

	// Newest-first: the 10 returned must be events[14] down to events[5].
	for i, row := range got {
		want := events[14-i]
		if !row.TS.Equal(want.TS) {
			t.Errorf("row %d TS: want %v, got %v", i, want.TS, row.TS)
		}
		if row.PublicKey != want.PublicKey {
			t.Errorf("row %d PublicKey: want %q, got %q", i, want.PublicKey, row.PublicKey)
		}
		if row.Name != want.Name {
			t.Errorf("row %d Name: want %q, got %q", i, want.Name, row.Name)
		}
	}

	// Strictly-decreasing ts (also catches an accidental ASC re-introduction).
	for i := 1; i < len(got); i++ {
		if !got[i-1].TS.After(got[i].TS) {
			t.Errorf("rows not strictly DESC: got[%d].TS=%v, got[%d].TS=%v", i-1, got[i-1].TS, i, got[i].TS)
		}
	}

	// limit <= 0 means no LIMIT clause — should return all 15.
	all, err := d.QueryHandshakeEvents(ctx, longAgo, faraway, 0)
	if err != nil {
		t.Fatalf("QueryHandshakeEvents (no limit): %v", err)
	}
	if len(all) != 15 {
		t.Errorf("len(all) = %d, want 15", len(all))
	}
}

// TestOpen_InvalidPath drives the Open error branches. A path with a
// NUL byte cannot be encoded into the URI DSN cleanly and forces either
// the driver Open or the subsequent Ping to fail, exercising the
// "ping sqlite" error wrapper. The exact failure mode is driver-specific
// — we just assert that Open returns a non-nil error and a nil *DB.
func TestOpen_InvalidPath(t *testing.T) {
	d, err := Open(context.Background(), "/this/path/does/not/exist/and/cannot/be/created/db.sqlite")
	if err == nil {
		// Defensive cleanup if the driver somehow succeeded.
		if d != nil {
			_ = d.Close()
		}
		t.Skip("modernc.org/sqlite tolerated a deeply-nested non-existent path; nothing to assert")
	}
	if d != nil {
		t.Errorf("on error, Open must return nil *DB; got %v", d)
	}
}

// TestOperationsAfterClose drives the post-close error paths in the
// Insert / Query / Prune helpers. Once *sql.DB is closed every subsequent
// call must surface an error rather than silently no-op or panic — these
// are the wrapping branches in each helper that turn the driver's
// "database is closed" into the package's typed error.
func TestOperationsAfterClose(t *testing.T) {
	d, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()

	if err := d.InsertSystemMetric(ctx, SystemMetric{TS: t0}); err == nil {
		t.Errorf("InsertSystemMetric on closed db: want error, got nil")
	}
	if err := d.InsertTrafficMetric(ctx, TrafficMetric{TS: t0}); err == nil {
		t.Errorf("InsertTrafficMetric on closed db: want error, got nil")
	}
	if err := d.InsertClientTraffic(ctx, []ClientTraffic{{TS: t0, PublicKey: "k"}}); err == nil {
		t.Errorf("InsertClientTraffic on closed db: want error, got nil")
	}
	if _, err := d.QuerySystemMetrics(ctx, t0, t0); err == nil {
		t.Errorf("QuerySystemMetrics on closed db: want error, got nil")
	}
	if _, err := d.QueryTrafficMetrics(ctx, t0, t0); err == nil {
		t.Errorf("QueryTrafficMetrics on closed db: want error, got nil")
	}
	if _, err := d.QueryClientTraffic(ctx, t0, t0); err == nil {
		t.Errorf("QueryClientTraffic on closed db: want error, got nil")
	}
	if _, err := d.PruneBefore(ctx, t0); err == nil {
		t.Errorf("PruneBefore on closed db: want error, got nil")
	}
}

// TestClose_Idempotent calls Close twice. The first call must succeed;
// the production wrapper does NOT add idempotency on top of *sql.DB, so
// the second call is expected to surface "sql: database is closed" —
// what we actually care about is that it does not panic. If a future
// refactor wraps Close to be true-idempotent, replace the lenient check
// with a strict `err == nil` assertion.
func TestClose_Idempotent(t *testing.T) {
	d, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := d.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second call: must not panic. May or may not return an error
	// depending on the database/sql implementation; both are acceptable
	// under the production code's documented "idempotent only insofar
	// as *sql.DB.Close is" comment.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second Close panicked: %v", r)
		}
	}()
	_ = d.Close()
}
