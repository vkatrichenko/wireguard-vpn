// Package poller is the dashboard's background sampler: it wakes on a fixed
// cadence, captures a coordinated snapshot of host metrics (proc), live
// WireGuard peer state (wg), and the client manifest (clientsfile), and
// writes typed rows into the three time-series tables owned by internal/db.
//
// The poller has TWO independent tickers running under one *Poller:
//
//  1. Sample ticker (default 30s) — calls collect on each tick; that's the
//     fan-in step that produces one row per snapshot in system_metrics +
//     traffic_metrics, plus N rows in client_traffic (one per live peer).
//  2. Retention ticker (default 1h) — calls db.PruneBefore(now -
//     Retention) to drop rows older than the dashboard's 24h chart window.
//     Retention defaults to 25h (one extra hour of slack) so the chart's
//     leftmost edge always has data to anchor against.
//
// Failure model: collect runs every tick "best-effort". Each of the three
// upstream services (proc.Sample, wg.Show, clientsfile.Load) can fail
// independently — a /proc read error on macOS, a missing manifest file
// during a redeploy, a transient sudo failure for `wg show`. We capture
// each error but DO NOT early-return: we still write whichever tables we
// CAN populate from the readings that DID succeed. Per-table insert
// failures are logged and the next table proceeds. The result is graceful
// degradation: a single missing input thins one card on the chart, not
// the whole history.
//
// Concurrency / shutdown: Run blocks until ctx is cancelled, then waits
// for both loops to finish via a WaitGroup so the caller can rely on
// "Run returned ⇒ no more DB writes in flight" before closing the *db.DB.
//
// All Poller fields are exported so a same-package test can construct a
// Poller{} literal with hand-rolled fakes — there is intentionally no
// constructor coupling that would force tests through New(). Production
// code uses New(...) which wires the cadence/retention defaults so
// cmd/wireguard-dashboard/main.go doesn't have to know the constants.
package poller

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/wg"
)

// Production cadence defaults. Kept as package-level constants so they're
// discoverable in one place and so tests can reference them when asserting
// on the defaults New(...) wires up.
//
//   - DefaultSampleEvery: a sample every 30s gives 2880 rows/day per
//     time-series — small enough for the 24h chart to render fast,
//     dense enough for spikes shorter than ~1m to still be visible.
//   - DefaultRetentionEvery: prune sweeps every hour. Cheaper than per-
//     tick pruning; 1h drift on the retention edge is invisible to the
//     24h chart window.
//   - DefaultRetention: 25h. The chart shows 24h; the extra hour gives
//     the leftmost edge a non-zero anchor and absorbs clock skew.
const (
	DefaultSampleEvery    = 30 * time.Second
	DefaultRetentionEvery = 1 * time.Hour
	DefaultRetention      = 25 * time.Hour
)

// Poller is the long-running background sampler. Construct with New for
// production wiring, or as a struct literal in same-package tests with
// fakes substituted into DB / Proc / WG / ClientsFile / Now.
type Poller struct {
	DB          *db.DB
	Proc        *proc.Service
	WG          *wg.Service
	ClientsFile *clientsfile.Service

	SampleEvery    time.Duration // default DefaultSampleEvery
	RetentionEvery time.Duration // default DefaultRetentionEvery
	Retention      time.Duration // default DefaultRetention (>24h to give the chart's 24h window a buffer)

	Now func() time.Time // default time.Now — injectable for tests

	// handshakeMu protects lastHandshake. The map is read/written only by
	// the sample loop, but we use a mutex so external callers (tests) can
	// observe the cache without races.
	handshakeMu   sync.Mutex
	lastHandshake map[string]int64 // public_key → unix-seconds of most recent handshake we've seen
}

// New returns a Poller wired with the production cadence/retention
// defaults. The four service deps are required (zero-value services
// would not work) so they're positional; the cadence knobs are not
// exposed because main.go has no opinion on them.
//
// Tests in this package construct a Poller{} literal directly; New is
// exclusively for cmd/wireguard-dashboard.
func New(database *db.DB, p *proc.Service, w *wg.Service, c *clientsfile.Service) *Poller {
	return &Poller{
		DB:             database,
		Proc:           p,
		WG:             w,
		ClientsFile:    c,
		SampleEvery:    DefaultSampleEvery,
		RetentionEvery: DefaultRetentionEvery,
		Retention:      DefaultRetention,
		Now:            time.Now,
	}
}

// Run blocks until ctx.Done(). It spawns two goroutines (sample loop and
// retention loop) under a single sync.WaitGroup and only returns once
// both have observed cancellation and exited cleanly. That ordering lets
// main.go safely Close() the *db.DB after Run returns: there are no more
// in-flight writes by then.
func (p *Poller) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		p.runSampleLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		p.runRetentionLoop(ctx)
	}()
	wg.Wait()
}

// runSampleLoop drives collect on a SampleEvery cadence. It runs an
// immediate sample at startup BEFORE arming the ticker — otherwise the
// system_metrics / traffic_metrics tables stay empty for the first
// SampleEvery seconds after boot, which would leave the chart blank for
// 30s of every restart. Errors from collect are logged and the loop
// continues (best-effort sampling).
func (p *Poller) runSampleLoop(ctx context.Context) {
	ticker := time.NewTicker(p.SampleEvery)
	defer ticker.Stop()

	for {
		if err := p.collect(ctx); err != nil {
			// Don't log on context cancellation — that's a clean shutdown,
			// not a sampling failure. errors.Is handles the wrapped variant
			// that any of the upstream services may have returned.
			if !errors.Is(err, context.Canceled) {
				slog.Warn("poller: sample collect failed", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Loop and collect again.
		}
	}
}

// runRetentionLoop drives PruneBefore on a RetentionEvery cadence. We
// deliberately DO NOT prune at startup — pruning every hour is enough,
// and burning a synchronous DELETE on process boot would slow down
// dashboard cold-start without buying anything visible.
//
// The Retention horizon is computed from p.Now() each tick (not pinned
// at construction) so a long-running process always uses the current
// wall-clock as its anchor.
func (p *Poller) runRetentionLoop(ctx context.Context) {
	ticker := time.NewTicker(p.RetentionEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := p.Now().Add(-p.Retention)
			n, err := p.DB.PruneBefore(ctx, cutoff)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					slog.Warn("poller: prune failed", "cutoff", cutoff, "err", err)
				}
				continue
			}
			slog.Info("poller: pruned rows", "cutoff", cutoff, "deleted", n)
		}
	}
}

// collect captures one coordinated snapshot from the three upstream
// services and writes it to the three time-series tables. It is the
// single unit of work the sample ticker drives.
//
// Sequencing rules:
//
//  1. All three reads happen first, capturing errors but NOT early-
//     returning — even if proc fails on macOS, the wg-derived rows are
//     still useful, and vice versa.
//  2. p.Now() is called ONCE so all rows in this snapshot share an
//     identical ts. The retention sweep, the chart join, and the
//     primary-key uniqueness constraints all rely on this.
//  3. Each table insert is gated on the success of the inputs it
//     needs. system_metrics + traffic_metrics need stats only.
//     client_traffic needs BOTH peers (for byte counters) AND clients
//     (for the name/address denormalised onto the row).
//  4. handshake_events: for each peer with a non-zero LatestHandshake
//     that is strictly greater than the value cached in p.lastHandshake
//     for that public key, we emit one row using the WG-reported
//     handshake time (NOT now) as ts. The composite PK on
//     (ts, public_key) plus INSERT OR REPLACE makes re-emission after
//     a process restart idempotent. The cache is in-process: on cold
//     start it is empty, so the first tick will emit one event per
//     active peer — that's intentional and benign because of the PK.
//
// Return value: the FIRST non-nil error encountered, AFTER all the work
// that COULD complete has completed. The caller logs it as a Warn for
// observability but does not retry — the next tick will just try again.
func (p *Poller) collect(ctx context.Context) error {
	stats, statsErr := p.Proc.Sample(ctx)
	peers, peersErr := p.WG.Show(ctx)
	clients, clientsErr := p.ClientsFile.Load(ctx)
	now := p.Now()

	// firstErr captures the earliest failure so the loop's slog.Warn has
	// something representative to surface. Subsequent errors are logged
	// per-table where they occur, so nothing gets silently swallowed.
	var firstErr error
	setErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	setErr(statsErr)
	setErr(peersErr)
	setErr(clientsErr)

	// system_metrics — needs stats only. Skip the write if the proc
	// sample failed (e.g. running on macOS where /proc doesn't exist):
	// inserting a zero-valued row would lie on the chart.
	if statsErr == nil {
		sysRow := db.SystemMetric{
			TS:     now,
			CPUPct: stats.CPUPercent,
			MemPct: stats.MemUsedPercent,
		}
		if err := p.DB.InsertSystemMetric(ctx, sysRow); err != nil {
			slog.Warn("poller: insert system_metric failed", "err", err)
			setErr(err)
		}
	}

	// traffic_metrics — same gate as system_metrics; cumulative byte
	// counters come from /sys/class/net/wg0/statistics, which is on the
	// same proc.Sample path.
	if statsErr == nil {
		trafficRow := db.TrafficMetric{
			TS:         now,
			RxBytesCum: stats.WgRxBytesCum,
			TxBytesCum: stats.WgTxBytesCum,
		}
		if err := p.DB.InsertTrafficMetric(ctx, trafficRow); err != nil {
			slog.Warn("poller: insert traffic_metric failed", "err", err)
			setErr(err)
		}
	}

	// client_traffic — needs BOTH peers and clients. clients gives us
	// the human name/address; peers gives us the per-peer byte counters.
	// On a peer present in `wg show` but missing from the manifest
	// (e.g. a manual `wg set` add), denormalise with PublicKey-as-name
	// and an empty address — the row is still useful for retroactive
	// diagnostics.
	if peersErr == nil && clientsErr == nil && len(peers) > 0 {
		index := clientsfile.ByPublicKey(clients)
		rows := make([]db.ClientTraffic, 0, len(peers))
		for _, peer := range peers {
			row := db.ClientTraffic{
				TS:         now,
				PublicKey:  peer.PublicKey,
				RxBytesCum: peer.TransferRx,
				TxBytesCum: peer.TransferTx,
			}
			if c, ok := index[peer.PublicKey]; ok {
				row.Name = c.Name
				row.Address = c.Address
			} else {
				row.Name = peer.PublicKey
				row.Address = ""
			}
			rows = append(rows, row)
		}
		if err := p.DB.InsertClientTraffic(ctx, rows); err != nil {
			slog.Warn("poller: insert client_traffic failed", "rows", len(rows), "err", err)
			setErr(err)
		}
	}

	// handshake_events — needs peers only. Names are resolved against the
	// manifest if it loaded; otherwise we fall back to public-key-as-name
	// for every peer (same convention as client_traffic above). The cache
	// + DB write are split: mutex is held only for the cache update so a
	// slow SQLite call doesn't block other readers of lastHandshake.
	if peersErr == nil {
		manifestByKey := map[string]clientsfile.Client{}
		if clientsErr == nil {
			manifestByKey = clientsfile.ByPublicKey(clients)
		} else {
			slog.Warn("poller: clients load failed; handshake events will use public-key-as-name", "err", clientsErr)
		}

		p.handshakeMu.Lock()
		if p.lastHandshake == nil {
			p.lastHandshake = make(map[string]int64)
		}
		var events []db.HandshakeEvent
		for _, peer := range peers {
			if peer.LatestHandshake.IsZero() {
				continue // never handshaked — skip
			}
			hs := peer.LatestHandshake.Unix()
			prior := p.lastHandshake[peer.PublicKey]
			if hs > prior {
				name := peer.PublicKey
				if c, ok := manifestByKey[peer.PublicKey]; ok {
					name = c.Name
				}
				events = append(events, db.HandshakeEvent{
					TS:        time.Unix(hs, 0).UTC(),
					PublicKey: peer.PublicKey,
					Name:      name,
				})
				p.lastHandshake[peer.PublicKey] = hs
			}
		}
		p.handshakeMu.Unlock()

		if len(events) > 0 {
			if err := p.DB.InsertHandshakeEvents(ctx, events); err != nil {
				slog.Warn("poller: insert handshake_events failed", "count", len(events), "err", err)
				setErr(err)
			}
		}
	}

	return firstErr
}
