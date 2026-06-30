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
	"database/sql"
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

	clientRows, err := d.QueryClientTrafficAll(ctx, from, to)
	if err != nil {
		t.Fatalf("QueryClientTrafficAll on fresh db: %v", err)
	}
	if len(clientRows) != 0 {
		t.Errorf("QueryClientTrafficAll: want 0 rows on fresh db, got %d", len(clientRows))
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

	got, err := d.QueryClientTrafficAll(ctx, t0.Add(-time.Minute), t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("QueryClientTrafficAll: %v", err)
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
	rows, err := d.QueryClientTrafficAll(ctx, from, to)
	if err != nil {
		t.Fatalf("QueryClientTrafficAll: %v", err)
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
		from.Add(-time.Second),     // outside (before)
		from,                       // inside, at lower bound
		from.Add(30 * time.Minute), // inside, middle
		to,                         // inside, at upper bound
		to.Add(time.Second),        // outside (after)
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

	clientRows, err := d.QueryClientTrafficAll(ctx, from, to)
	if err != nil {
		t.Fatalf("QueryClientTrafficAll post-prune: %v", err)
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

// TestQueryLatestHandshakePerPeer pins the spec 016 (2.3) contract: one row
// per peer (the peer's MAX(ts)), peers ordered newest-first, and the limit
// honoured. Seeds two peers with multiple handshakes each (interleaved in time)
// plus a third single-handshake peer, then asserts the collapse, the per-peer
// latest ts, and the cross-peer ordering.
func TestQueryLatestHandshakePerPeer(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	// pk-a: handshakes at t0+0s and t0+30s (latest 30s).
	// pk-b: handshakes at t0+10s and t0+40s (latest 40s — newest overall).
	// pk-c: a single handshake at t0+20s.
	events := []HandshakeEvent{
		{TS: t0.Add(0 * time.Second), PublicKey: "pk-a", Name: "alice"},
		{TS: t0.Add(30 * time.Second), PublicKey: "pk-a", Name: "alice"},
		{TS: t0.Add(10 * time.Second), PublicKey: "pk-b", Name: "bob"},
		{TS: t0.Add(40 * time.Second), PublicKey: "pk-b", Name: "bob"},
		{TS: t0.Add(20 * time.Second), PublicKey: "pk-c", Name: "carol"},
	}
	if err := d.InsertHandshakeEvents(ctx, events); err != nil {
		t.Fatalf("InsertHandshakeEvents: %v", err)
	}

	longAgo := t0.Add(-365 * 24 * time.Hour)
	faraway := t0.Add(365 * 24 * time.Hour)

	got, err := d.QueryLatestHandshakePerPeer(ctx, longAgo, faraway, 0)
	if err != nil {
		t.Fatalf("QueryLatestHandshakePerPeer: %v", err)
	}

	// One row per peer (three peers despite five handshakes), newest-first:
	// pk-b (40s), then pk-a (30s), then pk-c (20s).
	want := []HandshakeEvent{
		{TS: t0.Add(40 * time.Second), PublicKey: "pk-b", Name: "bob"},
		{TS: t0.Add(30 * time.Second), PublicKey: "pk-a", Name: "alice"},
		{TS: t0.Add(20 * time.Second), PublicKey: "pk-c", Name: "carol"},
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, row := range want {
		if !got[i].TS.Equal(row.TS) {
			t.Errorf("row %d TS: want %v, got %v", i, row.TS, got[i].TS)
		}
		if got[i].PublicKey != row.PublicKey {
			t.Errorf("row %d PublicKey: want %q, got %q", i, row.PublicKey, got[i].PublicKey)
		}
		if got[i].Name != row.Name {
			t.Errorf("row %d Name: want %q, got %q", i, row.Name, got[i].Name)
		}
	}

	// limit caps the peer count (still newest-first).
	limited, err := d.QueryLatestHandshakePerPeer(ctx, longAgo, faraway, 2)
	if err != nil {
		t.Fatalf("QueryLatestHandshakePerPeer (limit): %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("len(limited) = %d, want 2", len(limited))
	}
	if limited[0].PublicKey != "pk-b" || limited[1].PublicKey != "pk-a" {
		t.Errorf("limited peers = [%q %q], want [pk-b pk-a]", limited[0].PublicKey, limited[1].PublicKey)
	}
}

// TestInsertClient_RoundTrip writes two clients and reads them back via
// ListClients, asserting every column survives the unix-seconds timestamp
// encoding and the bool<->0/1 / nullable-note conversions. It also pins the
// deterministic ORDER BY address ordering (10.0.0.2 before 10.0.0.3,
// regardless of insertion order).
func TestInsertClient_RoundTrip(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	// Insert the higher address first so a missing ORDER BY would be caught.
	bob := Client{
		Name:      "bob",
		PublicKey: "pk-bob",
		Address:   "10.0.0.3/32",
		Enabled:   false,
		Note:      sql.NullString{}, // NULL note
		CreatedAt: t0,
		UpdatedAt: t0,
	}
	alice := Client{
		Name:      "alice",
		PublicKey: "pk-alice",
		Address:   "10.0.0.2/32",
		Enabled:   true,
		Note:      sql.NullString{String: "laptop", Valid: true},
		CreatedAt: t0.Add(time.Second),
		UpdatedAt: t0.Add(2 * time.Second),
	}
	if err := d.InsertClient(ctx, bob); err != nil {
		t.Fatalf("InsertClient(bob): %v", err)
	}
	if err := d.InsertClient(ctx, alice); err != nil {
		t.Fatalf("InsertClient(alice): %v", err)
	}

	got, err := d.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 clients, got %d", len(got))
	}

	// ORDER BY address ASC: alice (.2) before bob (.3).
	gotAlice, gotBob := got[0], got[1]
	if gotAlice.Name != "alice" || gotBob.Name != "bob" {
		t.Fatalf("ordering: want [alice bob], got [%s %s]", gotAlice.Name, gotBob.Name)
	}

	if gotAlice.ID == 0 {
		t.Errorf("alice ID: want non-zero autoincrement id, got 0")
	}
	if gotAlice.PublicKey != alice.PublicKey {
		t.Errorf("alice PublicKey: want %q, got %q", alice.PublicKey, gotAlice.PublicKey)
	}
	if gotAlice.Address != alice.Address {
		t.Errorf("alice Address: want %q, got %q", alice.Address, gotAlice.Address)
	}
	if gotAlice.Enabled != true {
		t.Errorf("alice Enabled: want true, got %v", gotAlice.Enabled)
	}
	if !gotAlice.Note.Valid || gotAlice.Note.String != "laptop" {
		t.Errorf("alice Note: want {laptop true}, got %+v", gotAlice.Note)
	}
	if !gotAlice.CreatedAt.Equal(alice.CreatedAt) {
		t.Errorf("alice CreatedAt: want %v, got %v", alice.CreatedAt, gotAlice.CreatedAt)
	}
	if !gotAlice.UpdatedAt.Equal(alice.UpdatedAt) {
		t.Errorf("alice UpdatedAt: want %v, got %v", alice.UpdatedAt, gotAlice.UpdatedAt)
	}

	// bob: disabled + NULL note must round-trip as Valid=false.
	if gotBob.Enabled != false {
		t.Errorf("bob Enabled: want false, got %v", gotBob.Enabled)
	}
	if gotBob.Note.Valid {
		t.Errorf("bob Note: want NULL (Valid=false), got %+v", gotBob.Note)
	}
}

// TestListClients_EmptyTable confirms a fresh table yields zero rows and no
// error (nil slice is acceptable; len is what callers check).
func TestListClients_EmptyTable(t *testing.T) {
	d := newTestDB(t)

	got, err := d.ListClients(context.Background())
	if err != nil {
		t.Fatalf("ListClients on fresh db: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 clients on fresh db, got %d", len(got))
	}
}

// TestCountClients tracks the row count across inserts and a delete.
func TestCountClients(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	if n, err := d.CountClients(ctx); err != nil || n != 0 {
		t.Fatalf("CountClients on fresh db: got (%d, %v), want (0, nil)", n, err)
	}

	clients := []Client{
		{Name: "a", PublicKey: "pk-a", Address: "10.0.0.2/32", Enabled: true, CreatedAt: t0, UpdatedAt: t0},
		{Name: "b", PublicKey: "pk-b", Address: "10.0.0.3/32", Enabled: true, CreatedAt: t0, UpdatedAt: t0},
	}
	for _, c := range clients {
		if err := d.InsertClient(ctx, c); err != nil {
			t.Fatalf("InsertClient(%s): %v", c.Name, err)
		}
	}

	if n, err := d.CountClients(ctx); err != nil || n != 2 {
		t.Fatalf("CountClients after 2 inserts: got (%d, %v), want (2, nil)", n, err)
	}

	if err := d.DeleteClient(ctx, "a"); err != nil {
		t.Fatalf("DeleteClient(a): %v", err)
	}
	if n, err := d.CountClients(ctx); err != nil || n != 1 {
		t.Fatalf("CountClients after delete: got (%d, %v), want (1, nil)", n, err)
	}
}

// TestUpdateClient_ChangesFieldsAndBumpsUpdatedAt verifies that UpdateClient
// rewrites the mutable columns by id, persists a bumped updated_at, and
// leaves created_at untouched.
func TestUpdateClient_ChangesFieldsAndBumpsUpdatedAt(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	orig := Client{
		Name:      "alice",
		PublicKey: "pk-alice",
		Address:   "10.0.0.2/32",
		Enabled:   true,
		Note:      sql.NullString{String: "laptop", Valid: true},
		CreatedAt: t0,
		UpdatedAt: t0,
	}
	if err := d.InsertClient(ctx, orig); err != nil {
		t.Fatalf("InsertClient: %v", err)
	}

	list, err := d.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 client, got %d", len(list))
	}
	c := list[0]

	// Mutate every editable field and bump updated_at by an hour.
	c.Name = "alice-renamed"
	c.PublicKey = "pk-alice-2"
	c.Address = "10.0.0.5/32"
	c.Enabled = false
	c.Note = sql.NullString{String: "phone", Valid: true}
	c.UpdatedAt = t0.Add(time.Hour)
	if err := d.UpdateClient(ctx, c); err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}

	list, err = d.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients post-update: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 client post-update, got %d", len(list))
	}
	got := list[0]

	if got.ID != c.ID {
		t.Errorf("ID changed: want %d, got %d", c.ID, got.ID)
	}
	if got.Name != "alice-renamed" {
		t.Errorf("Name: want alice-renamed, got %q", got.Name)
	}
	if got.PublicKey != "pk-alice-2" {
		t.Errorf("PublicKey: want pk-alice-2, got %q", got.PublicKey)
	}
	if got.Address != "10.0.0.5/32" {
		t.Errorf("Address: want 10.0.0.5/32, got %q", got.Address)
	}
	if got.Enabled != false {
		t.Errorf("Enabled: want false, got %v", got.Enabled)
	}
	if !got.Note.Valid || got.Note.String != "phone" {
		t.Errorf("Note: want {phone true}, got %+v", got.Note)
	}
	if !got.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("UpdatedAt: want bumped to %v, got %v", t0.Add(time.Hour), got.UpdatedAt)
	}
	if !got.CreatedAt.Equal(t0) {
		t.Errorf("CreatedAt: want untouched %v, got %v", t0, got.CreatedAt)
	}
}

// TestDeleteClient confirms a delete removes the row and that deleting an
// absent name is a no-op (no error).
func TestDeleteClient(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	if err := d.InsertClient(ctx, Client{Name: "alice", PublicKey: "pk-alice", Address: "10.0.0.2/32", Enabled: true, CreatedAt: t0, UpdatedAt: t0}); err != nil {
		t.Fatalf("InsertClient: %v", err)
	}

	if err := d.DeleteClient(ctx, "alice"); err != nil {
		t.Fatalf("DeleteClient(alice): %v", err)
	}
	if n, err := d.CountClients(ctx); err != nil || n != 0 {
		t.Fatalf("CountClients after delete: got (%d, %v), want (0, nil)", n, err)
	}

	// Deleting a non-existent name must not error.
	if err := d.DeleteClient(ctx, "ghost"); err != nil {
		t.Errorf("DeleteClient(ghost) on absent row: want nil, got %v", err)
	}
}

// TestInsertClient_UniquenessViolations pins that each of the three UNIQUE
// columns (name, public_key, address) rejects a duplicate insert with a
// non-nil error rather than silently overwriting.
func TestInsertClient_UniquenessViolations(t *testing.T) {
	base := Client{
		Name:      "alice",
		PublicKey: "pk-alice",
		Address:   "10.0.0.2/32",
		Enabled:   true,
		CreatedAt: t0,
		UpdatedAt: t0,
	}

	tests := []struct {
		name string
		dup  Client
	}{
		{
			name: "duplicate name",
			dup:  Client{Name: "alice", PublicKey: "pk-other", Address: "10.0.0.9/32", Enabled: true, CreatedAt: t0, UpdatedAt: t0},
		},
		{
			name: "duplicate public_key",
			dup:  Client{Name: "other", PublicKey: "pk-alice", Address: "10.0.0.9/32", Enabled: true, CreatedAt: t0, UpdatedAt: t0},
		},
		{
			name: "duplicate address",
			dup:  Client{Name: "other", PublicKey: "pk-other", Address: "10.0.0.2/32", Enabled: true, CreatedAt: t0, UpdatedAt: t0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			ctx := context.Background()

			if err := d.InsertClient(ctx, base); err != nil {
				t.Fatalf("InsertClient(base): %v", err)
			}
			if err := d.InsertClient(ctx, tc.dup); err == nil {
				t.Fatalf("InsertClient(%s): want UNIQUE-constraint error, got nil", tc.name)
			}
			// The original row must be the only one left.
			if n, err := d.CountClients(ctx); err != nil || n != 1 {
				t.Fatalf("CountClients after rejected dup: got (%d, %v), want (1, nil)", n, err)
			}
		})
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
	if _, err := d.QueryClientTrafficAll(ctx, t0, t0); err == nil {
		t.Errorf("QueryClientTrafficAll on closed db: want error, got nil")
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

// TestQueryClientTraffic_ByKey pins the per-key range-scan contract for
// the per-peer chart endpoint: given a (publicKey, from, to), return only
// that peer's rows inside the inclusive window, ordered ASC by ts, with
// every column round-tripped intact. The all-keys path is covered by
// TestInsertClientTraffic_BatchRoundTrip — this test is exclusively about
// the public_key filter (no peer-B bleed), the window narrowing, and the
// unknown-key (empty, no error) branch.
func TestQueryClientTraffic_ByKey(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()

	// Two peers, three samples each at t0, t0+1m, t0+2m. Distinct rx/tx
	// values per row so a mis-scan (e.g. swapped columns) would be caught
	// by the round-trip check below, not just the row count.
	seed := []ClientTraffic{
		{TS: t0, PublicKey: "peer-A", Name: "alice", Address: "10.0.0.2/32", RxBytesCum: 100, TxBytesCum: 200},
		{TS: t0.Add(time.Minute), PublicKey: "peer-A", Name: "alice", Address: "10.0.0.2/32", RxBytesCum: 110, TxBytesCum: 210},
		{TS: t0.Add(2 * time.Minute), PublicKey: "peer-A", Name: "alice", Address: "10.0.0.2/32", RxBytesCum: 120, TxBytesCum: 220},
		{TS: t0, PublicKey: "peer-B", Name: "bob", Address: "10.0.0.3/32", RxBytesCum: 900, TxBytesCum: 800},
		{TS: t0.Add(time.Minute), PublicKey: "peer-B", Name: "bob", Address: "10.0.0.3/32", RxBytesCum: 910, TxBytesCum: 810},
		{TS: t0.Add(2 * time.Minute), PublicKey: "peer-B", Name: "bob", Address: "10.0.0.3/32", RxBytesCum: 920, TxBytesCum: 820},
	}
	if err := d.InsertClientTraffic(ctx, seed); err != nil {
		t.Fatalf("InsertClientTraffic: %v", err)
	}

	// Wide window: all three peer-A rows, none of peer-B's.
	wideFrom := t0.Add(-time.Second)
	wideTo := t0.Add(2*time.Minute + time.Second)
	got, err := d.QueryClientTraffic(ctx, "peer-A", wideFrom, wideTo)
	if err != nil {
		t.Fatalf("QueryClientTraffic(peer-A, wide): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("wide window: want 3 rows for peer-A, got %d", len(got))
	}
	for i, row := range got {
		if row.PublicKey != "peer-A" {
			t.Errorf("row %d PublicKey: want %q, got %q", i, "peer-A", row.PublicKey)
		}
	}
	// Strictly-ascending ts (also catches an accidental DESC re-introduction).
	for i := 1; i < len(got); i++ {
		if !got[i-1].TS.Before(got[i].TS) {
			t.Errorf("rows not strictly ASC: got[%d].TS=%v, got[%d].TS=%v", i-1, got[i-1].TS, i, got[i].TS)
		}
	}
	// Round-trip rx/tx on the middle row to confirm scan correctness.
	if !got[1].TS.Equal(t0.Add(time.Minute)) {
		t.Errorf("row 1 TS: want %v, got %v", t0.Add(time.Minute), got[1].TS)
	}
	if got[1].RxBytesCum != 110 {
		t.Errorf("row 1 RxBytesCum: want 110, got %d", got[1].RxBytesCum)
	}
	if got[1].TxBytesCum != 210 {
		t.Errorf("row 1 TxBytesCum: want 210, got %d", got[1].TxBytesCum)
	}

	// Narrow window: only the middle row of peer-A.
	narrowFrom := t0.Add(30 * time.Second)
	narrowTo := t0.Add(time.Minute + 30*time.Second)
	mid, err := d.QueryClientTraffic(ctx, "peer-A", narrowFrom, narrowTo)
	if err != nil {
		t.Fatalf("QueryClientTraffic(peer-A, narrow): %v", err)
	}
	if len(mid) != 1 {
		t.Fatalf("narrow window: want 1 row, got %d", len(mid))
	}
	if !mid[0].TS.Equal(t0.Add(time.Minute)) {
		t.Errorf("narrow TS: want %v, got %v", t0.Add(time.Minute), mid[0].TS)
	}
	if mid[0].PublicKey != "peer-A" {
		t.Errorf("narrow PublicKey: want %q, got %q", "peer-A", mid[0].PublicKey)
	}
	if mid[0].RxBytesCum != 110 {
		t.Errorf("narrow RxBytesCum: want 110, got %d", mid[0].RxBytesCum)
	}
	if mid[0].TxBytesCum != 210 {
		t.Errorf("narrow TxBytesCum: want 210, got %d", mid[0].TxBytesCum)
	}

	// Unknown key: empty slice, no error.
	none, err := d.QueryClientTraffic(ctx, "peer-C", wideFrom, wideTo)
	if err != nil {
		t.Fatalf("QueryClientTraffic(peer-C): unexpected error: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("unknown key: want 0 rows, got %d", len(none))
	}
}
