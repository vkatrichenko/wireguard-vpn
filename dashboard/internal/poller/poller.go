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

	"wireguard-dashboard/internal/alerts"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/disk"
	"wireguard-dashboard/internal/notify"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/systemd"
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
//   - DefaultRetention: 8d 1h. Spec 003 added a 1h / 6h / 24h / 7d range
//     selector; the widest option is 7 days, so retention is 7d of chart
//     window + 1h slack to give the leftmost edge a non-zero anchor and
//     absorb clock skew. Expected on-disk DB size at 2 peers / 30s cadence
//     ≈ 17 MB (system_metrics + traffic_metrics + client_traffic × peers
//   - indexes). Worst-case at 20 peers ≈ 170 MB — still well within
//     operational budget. See spec 003 §2.7 for the sizing math.
const (
	DefaultSampleEvery    = 30 * time.Second
	DefaultRetentionEvery = 1 * time.Hour
	DefaultRetention      = 8*24*time.Hour + time.Hour
)

// dispatchBufferSize bounds the queue of alert Events waiting to be delivered
// by the dispatch worker. Alert events are RARE (a state transition or a
// post-cooldown reminder — not one per tick), so a small buffer is generous.
// If it ever fills (a wedged webhook taking longer than the dispatch backlog
// drains), Evaluate-produced events are DROPPED with a log rather than blocking
// the sample tick — losing an alert notification is strictly better than
// stalling metric collection. The in-UI active-alerts view (Slice 5) reflects
// state regardless of delivery, so a dropped webhook is not a lost alert state.
const dispatchBufferSize = 64

// ServiceReader is the seam the poller uses to read the wg-quick@wg0 systemd
// status each tick for the service-down alert condition. *systemd.Service
// satisfies it in production; tests inject a fake that returns canned statuses
// without shelling out to systemctl. Kept to the single method the poller needs.
type ServiceReader interface {
	Get(ctx context.Context) (systemd.ServiceStatus, error)
}

// DiskReader is the seam the poller uses to sample filesystem usage each tick
// for the high-disk alert condition. *disk.Service satisfies it in production;
// tests inject a fake that returns canned mounts (or an error) without touching
// /proc/mounts. disk.Sample is STATELESS — a fresh read each call holding no
// prior-sample state — so unlike proc it is safe to call inside the alert path.
type DiskReader interface {
	Sample(ctx context.Context) ([]disk.Mount, error)
}

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
	Retention      time.Duration // default DefaultRetention (~8d to back the 7d chart range)

	Now func() time.Time // default time.Now — injectable for tests

	// Alerting deps (spec 007, Slice 2). All three are OPTIONAL: when
	// Evaluator is nil the poller behaves exactly as it did before alerting
	// existed — no systemd read, no dispatch goroutine, no notifier call. New
	// wires all three together for production; same-package tests set them on
	// a Poller{} literal. Service is the systemd status reader (an interface
	// so tests fake it); Notifier is the delivery seam (notify.NoOp when
	// alerting is disabled but the evaluator still runs for the in-UI view).
	Service   ServiceReader
	Disk      DiskReader
	Evaluator *alerts.Evaluator
	Notifier  notify.Notifier

	// StatusHolder is the optional seam to the in-UI active-alerts view (spec
	// 007, Slice 5). When non-nil, evaluateAlerts writes the evaluator's current
	// firing set + this tick's transition events into it once per tick, from THIS
	// (poller) goroutine — the server then reads a deep copy via Snapshot from its
	// own goroutines. nil holder → skip the write (back-compat: the poller behaves
	// exactly as before the in-UI view existed). Written only from the sample
	// loop; the holder owns its own mutex for the cross-goroutine read.
	StatusHolder *alerts.StatusHolder

	// dispatch carries Evaluator-produced events from the sample loop to the
	// dispatch worker so webhook delivery happens OFF the poll critical path.
	// nil until Run starts the worker (only when Evaluator != nil). Bounded by
	// dispatchBufferSize; a full buffer drops the event with a log.
	dispatch chan alerts.Event

	// handshakeMu protects lastHandshake. The map is read/written only by
	// the sample loop, but we use a mutex so external callers (tests) can
	// observe the cache without races.
	handshakeMu   sync.Mutex
	lastHandshake map[string]int64 // public_key → unix-seconds of most recent handshake we've seen

	// statsMu protects lastCPUPct / lastCPUValid. collect writes the CPU% it
	// already computed from THIS tick's single proc.Sample; evaluateAlerts reads
	// it. This threading is what lets the alert path reuse collect's reading
	// instead of calling proc.Sample a second time — a double sample would split
	// the per-tick CPU delta and corrupt BOTH the chart row and the alert
	// reading. lastCPUValid is false until the first successful proc sample, so a
	// proc failure feeds the evaluator no CPU reading rather than a stale/zero one.
	statsMu      sync.Mutex
	lastCPUPct   float64
	lastCPUValid bool

	// peersMu protects lastPeers. Same pattern as lastCPUPct: collect builds the
	// per-peer alert snapshot from THIS tick's single p.WG.Show call (already
	// fetched for client_traffic / handshake_events) and stashes it here;
	// evaluateAlerts reads it into alerts.Input.Peers. This is what lets the
	// alert path reuse collect's `wg show` instead of shelling out a SECOND
	// `sudo wg show` — a double exec would both cost an extra sudo call and skew
	// the byte/handshake values between the chart row and the alert reading.
	// lastPeersValid is false until the first successful wg read, so a wg failure
	// feeds the evaluator no peer input rather than a stale snapshot.
	peersMu        sync.Mutex
	lastPeers      []alerts.PeerSample
	lastPeersValid bool

	// metricsMu guards metrics, the latest in-memory snapshot the Prometheus
	// /metrics handler reads (spec 012, Slice 4). It is updated INCREMENTALLY
	// from THIS tick's already-taken readings — collect writes the host CPU%/Mem%
	// and per-peer fields; evaluateAlerts writes the service + disk fields. Both
	// writers run on the single sample goroutine, so they never race each OTHER;
	// the mutex exists solely so the HTTP handler can read a consistent deep copy
	// from its own goroutine. Crucially, NO new exec / /proc read / DB query backs
	// this — the scrape path is pure in-memory, off the poll critical path, so a
	// hostile or frequent scraper can never amplify into extra `sudo wg show` /
	// systemctl calls or split proc's per-tick CPU delta.
	metricsMu sync.Mutex
	metrics   MetricsSnapshot
}

// peerOnlineThreshold mirrors the server's clients-view definition of "online":
// a peer whose most recent handshake landed within the last three minutes. Kept
// as a poller-local const (rather than importing internal/server, which would be
// a cycle) so wireguard_peers_online and the Clients tab agree on the boundary.
// 3 minutes is the WireGuard rekey timeout — a meaningful protocol boundary, not
// an arbitrary UI choice.
const peerOnlineThreshold = 3 * time.Minute

// MetricsSnapshot is the in-memory view the Prometheus /metrics handler renders
// (spec 012, Slice 4). It is a flattened copy of the last sample's readings —
// never a live pointer into poller state — so the HTTP goroutine reads it under
// no lock once MetricsSnapshot has returned. The *Known flags distinguish "value
// is genuinely 0" from "we have never successfully read this input": the handler
// OMITS a metric whose Known flag is false rather than emit a fabricated zero.
type MetricsSnapshot struct {
	ServiceActive bool // wg-quick@wg0 active?
	ServiceKnown  bool // false until a systemd status has been read at least once

	PeersTotal  int
	PeersOnline int
	Peers       []PeerMetric

	CPUPercent float64
	CPUKnown   bool // false until a proc sample has succeeded at least once
	MemPercent float64
	MemKnown   bool

	Disks []DiskMetric
}

// PeerMetric is one peer's per-scrape values. The handler computes the
// last-handshake AGE (seconds) from LastHandshake versus scrape-time now, so a
// snapshot held briefly before a scrape ages correctly rather than freezing the
// age at sample time.
type PeerMetric struct {
	Name          string
	LastHandshake time.Time // zero ⇒ never handshaked; handler omits the age series
	RxBytes       int64
	TxBytes       int64
}

// DiskMetric is one filesystem's fullness percentage for the scrape.
type DiskMetric struct {
	Mount   string
	PctFull float64
}

// MetricsSnapshot returns a deep copy of the latest sampled metrics for the
// Prometheus /metrics handler. The Peers/Disks slices are freshly allocated so
// the caller can range over them after the lock is dropped without risking a
// torn read against the next tick's overwrite. This method does ZERO I/O — no
// exec, no /proc read, no DB query — it only copies state the sample loop has
// already accumulated, which is what keeps /metrics off the poll critical path
// and immune to scrape-frequency abuse.
func (p *Poller) MetricsSnapshot() MetricsSnapshot {
	p.metricsMu.Lock()
	defer p.metricsMu.Unlock()
	out := p.metrics
	if p.metrics.Peers != nil {
		out.Peers = append([]PeerMetric(nil), p.metrics.Peers...)
	}
	if p.metrics.Disks != nil {
		out.Disks = append([]DiskMetric(nil), p.metrics.Disks...)
	}
	return out
}

// recordHostMetrics stashes THIS tick's host CPU%/Mem% into the metrics
// snapshot. valid is statsErr == nil from collect's single proc.Sample — on a
// proc failure the Known flags drop to false so the handler omits the host
// gauges rather than emit a stale/zero reading. Called on the sample goroutine.
func (p *Poller) recordHostMetrics(valid bool, cpuPct, memPct float64) {
	p.metricsMu.Lock()
	defer p.metricsMu.Unlock()
	if valid {
		p.metrics.CPUPercent = cpuPct
		p.metrics.CPUKnown = true
		p.metrics.MemPercent = memPct
		p.metrics.MemKnown = true
	} else {
		p.metrics.CPUKnown = false
		p.metrics.MemKnown = false
	}
}

// recordPeerMetrics stashes THIS tick's per-peer snapshot, derived from the SAME
// samples collect built for the alert path (never a second wg show). Online is
// computed against now with peerOnlineThreshold so wireguard_peers_online tracks
// the Clients tab. Called on the sample goroutine, only when the wg + manifest
// reads both succeeded this tick; a failed read leaves the prior peer metrics in
// place (best-effort, matching collect's posture).
func (p *Poller) recordPeerMetrics(samples []alerts.PeerSample, now time.Time) {
	peers := make([]PeerMetric, 0, len(samples))
	online := 0
	for _, s := range samples {
		if !s.LastHandshake.IsZero() && now.Sub(s.LastHandshake) <= peerOnlineThreshold {
			online++
		}
		peers = append(peers, PeerMetric{
			Name:          s.Name,
			LastHandshake: s.LastHandshake,
			RxBytes:       s.RxBytes,
			TxBytes:       s.TxBytes,
		})
	}
	p.metricsMu.Lock()
	p.metrics.Peers = peers
	p.metrics.PeersTotal = len(peers)
	p.metrics.PeersOnline = online
	p.metricsMu.Unlock()
}

// recordServiceMetric stashes the wg-quick@wg0 active flag from THIS tick's
// systemd read into the metrics snapshot, marking it Known. Called on the sample
// goroutine only after a SUCCESSFUL status read, so ServiceKnown stays false (and
// the handler omits wireguard_service_active) until the first good read.
func (p *Poller) recordServiceMetric(active bool) {
	p.metricsMu.Lock()
	p.metrics.ServiceActive = active
	p.metrics.ServiceKnown = true
	p.metricsMu.Unlock()
}

// recordDiskMetrics stashes THIS tick's filesystem fullness, reusing the disk
// sample evaluateAlerts already took for the high-disk condition — no extra
// statfs. Called on the sample goroutine only when the disk read succeeded.
func (p *Poller) recordDiskMetrics(mounts []disk.Mount) {
	disks := make([]DiskMetric, 0, len(mounts))
	for _, m := range mounts {
		disks = append(disks, DiskMetric{Mount: m.Path, PctFull: m.PctFull})
	}
	p.metricsMu.Lock()
	p.metrics.Disks = disks
	p.metricsMu.Unlock()
}

// New returns a Poller wired with the production cadence/retention
// defaults. The four data-collection deps are required (zero-value
// services would not work) so they're positional; the cadence knobs are
// not exposed because main.go has no opinion on them.
//
// The alerting deps (svc / diskReader / evaluator / notifier) are passed too
// but are OPTIONAL: pass evaluator == nil to disable alerting entirely (the
// poller then behaves exactly as before — no systemd read, no disk read, no
// dispatch goroutine). When evaluator is non-nil, notifier should be set (a
// notify.NoOp keeps the evaluator running with delivery disabled); a nil
// notifier with a non-nil evaluator is tolerated and treated as a NoOp. A nil
// diskReader simply omits the high-disk condition's input each tick. The holder
// is the optional in-UI active-alerts seam (Slice 5): when non-nil it is fed the
// evaluator's firing set + transition events each tick; nil disables the in-UI
// view without affecting evaluation or delivery.
//
// Tests in this package construct a Poller{} literal directly; New is
// exclusively for cmd/wireguard-dashboard.
func New(database *db.DB, p *proc.Service, w *wg.Service, c *clientsfile.Service, svc ServiceReader, diskReader DiskReader, evaluator *alerts.Evaluator, notifier notify.Notifier, holder *alerts.StatusHolder) *Poller {
	return &Poller{
		DB:             database,
		Proc:           p,
		WG:             w,
		ClientsFile:    c,
		Service:        svc,
		Disk:           diskReader,
		Evaluator:      evaluator,
		Notifier:       notifier,
		StatusHolder:   holder,
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

	// Alert dispatch worker — started only when alerting is enabled
	// (Evaluator wired). It owns the off-critical-path webhook delivery: the
	// sample loop only ever does a non-blocking send onto p.dispatch, so a
	// slow/failing notifier can never stall a tick. The channel is created
	// here (not in New) so a Poller that never Runs has no goroutine to leak.
	if p.Evaluator != nil {
		p.dispatch = make(chan alerts.Event, dispatchBufferSize)
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.runDispatchLoop(ctx)
		}()
	}

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
		// Alert evaluation runs after collect, on the same tick. It is a
		// no-op when alerting is disabled (Evaluator nil) and is itself
		// best-effort: a systemd read error is logged, never fatal, and
		// dispatch is non-blocking — so this can never slow a tick.
		p.evaluateAlerts(ctx)
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

	// Stash THIS tick's CPU% for the alert path to reuse. proc.Sample holds the
	// prior /proc/stat to compute the CPU delta, so it must be sampled exactly
	// once per tick — evaluateAlerts reads this cached value instead of calling
	// Sample again (a second call would split the delta across two reads and
	// corrupt both this row and the alert reading). lastCPUValid stays false on a
	// proc failure so the evaluator gets no fabricated CPU reading that tick.
	p.statsMu.Lock()
	if statsErr == nil {
		p.lastCPUPct = stats.CPUPercent
		p.lastCPUValid = true
	} else {
		p.lastCPUValid = false
	}
	p.statsMu.Unlock()

	// Mirror the same reading into the Prometheus metrics snapshot (spec 012,
	// Slice 4). Same single-sample reuse as lastCPUPct — no second proc read.
	p.recordHostMetrics(statsErr == nil, stats.CPUPercent, stats.MemUsedPercent)

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

	// Stash THIS tick's per-peer alert snapshot for evaluateAlerts to reuse,
	// built from the SAME peers slice + manifest we already have — never a second
	// p.WG.Show (extra sudo exec + value skew). Manifest-skip: a peer with no
	// client name is a wg-only peer (e.g. a manual `wg set` add), not a "client",
	// so it carries no per-client alert. We require BOTH reads to have succeeded;
	// without the manifest we couldn't resolve names, and a partial snapshot would
	// silently drop peers from alerting. On failure lastPeersValid stays false so
	// the evaluator gets no fabricated peer input.
	peersOK := peersErr == nil && clientsErr == nil
	var peerSamples []alerts.PeerSample
	if peersOK {
		index := clientsfile.ByPublicKey(clients)
		peerSamples = make([]alerts.PeerSample, 0, len(peers))
		for _, peer := range peers {
			c, ok := index[peer.PublicKey]
			if !ok {
				continue // wg-only peer, not a manifest client — skip
			}
			peerSamples = append(peerSamples, alerts.PeerSample{
				Name:          c.Name,
				LastHandshake: peer.LatestHandshake,
				RxBytes:       peer.TransferRx,
				TxBytes:       peer.TransferTx,
			})
		}
	}
	p.peersMu.Lock()
	if peersOK {
		p.lastPeers = peerSamples
		p.lastPeersValid = true
	} else {
		p.lastPeersValid = false
	}
	p.peersMu.Unlock()

	// Feed the Prometheus metrics snapshot (spec 012, Slice 4) from the SAME
	// samples — never a second wg show. Online is counted against this tick's now.
	if peersOK {
		p.recordPeerMetrics(peerSamples, now)
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

// evaluateAlerts reads the inputs each watched condition needs, runs the
// Evaluator for this tick, and hands any resulting events to the dispatch
// worker WITHOUT blocking. It is a no-op when alerting is disabled.
//
// Best-effort, matching collect: the systemd and disk reads are the only I/O
// here. A systemd failure skips the tick entirely (a fabricated status could
// trip a false service-down transition). A disk failure is softer — it just
// omits the high-disk input for this tick and the OTHER conditions still
// evaluate, so a transient statfs error never blanks CPU/service alerting. CPU%
// is NOT sampled here: it is reused from collect's single proc.Sample (see
// lastCPUPct) to avoid splitting the per-tick CPU delta. The dispatch send is
// non-blocking: a full buffer drops the event with a log so a wedged webhook can
// never back-pressure the sample loop.
func (p *Poller) evaluateAlerts(ctx context.Context) {
	if p.Evaluator == nil {
		return
	}

	in := alerts.Input{}
	if p.Service != nil {
		status, err := p.Service.Get(ctx)
		if err != nil {
			// Skip this tick's evaluation rather than risk a false transition
			// off a zero-value status. The next tick retries.
			if !errors.Is(err, context.Canceled) {
				slog.Warn("poller: systemd status read failed; skipping alert evaluation this tick", "err", err)
			}
			return
		}
		in.ServiceActive = status.Active
		// Mirror the same read into the Prometheus metrics snapshot (spec 012,
		// Slice 4) — no extra systemctl call.
		p.recordServiceMetric(status.Active)
	}

	// Reuse the CPU% collect already computed from this tick's single
	// proc.Sample. When proc hasn't produced a valid sample yet (or failed this
	// tick) we leave CPUPercent at its zero value, which is below threshold and
	// keeps the sustained-CPU run reset.
	p.statsMu.Lock()
	if p.lastCPUValid {
		in.CPUPercent = p.lastCPUPct
	}
	p.statsMu.Unlock()

	// Reuse the per-peer snapshot collect built from this tick's single
	// p.WG.Show. We do NOT call p.WG.Show again here — that would mean a second
	// `sudo wg show` exec and byte/handshake skew versus the chart row. When the
	// wg read failed this tick lastPeersValid is false and we leave Peers nil, so
	// the per-peer conditions simply observe nothing (holding their state).
	p.peersMu.Lock()
	if p.lastPeersValid {
		in.Peers = p.lastPeers
	}
	p.peersMu.Unlock()

	// Disk is sampled inline (it is stateless, unlike proc). A read error is
	// logged and the disk field omitted for this tick — never fatal, consistent
	// with collect's best-effort posture.
	if p.Disk != nil {
		mounts, err := p.Disk.Sample(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Warn("poller: disk sample failed; omitting high-disk input this tick", "err", err)
			}
		} else {
			in.DiskUsage = make([]alerts.FilesystemUsage, 0, len(mounts))
			for _, m := range mounts {
				in.DiskUsage = append(in.DiskUsage, alerts.FilesystemUsage{Mount: m.Path, PctFull: m.PctFull})
			}
			// Mirror the same disk sample into the Prometheus metrics snapshot
			// (spec 012, Slice 4) — reuses this tick's statfs, no extra read.
			p.recordDiskMetrics(mounts)
		}
	}

	events := p.Evaluator.Evaluate(p.Now(), in)

	// Feed the in-UI active-alerts view (Slice 5) from THIS goroutine: read the
	// evaluator's current firing set and hand it plus this tick's transitions to
	// the holder, which the server reads via Snapshot from its own goroutines.
	// Done BEFORE dispatch so the view reflects state even if delivery is dropped
	// — a wedged webhook never desyncs the UI from the evaluator. Nil holder skips
	// (back-compat). Both Active() and Update run on the poller goroutine, so the
	// evaluator's maps are never touched concurrently.
	if p.StatusHolder != nil {
		p.StatusHolder.Update(p.Evaluator.Active(), events)
	}

	for _, ev := range events {
		select {
		case p.dispatch <- ev:
		default:
			// Buffer full: drop rather than block the sample tick. Rare —
			// alert events are state transitions, not per-tick traffic.
			slog.Warn("poller: alert dispatch buffer full; dropping event",
				"condition", ev.Condition, "kind", ev.Kind.String())
		}
	}
}

// runDispatchLoop is the off-critical-path alert deliverer. It drains the
// dispatch channel and calls Notifier.Notify for each event, formatting the
// message via alerts.FormatMessage. Delivery failures are logged (the notifier
// already retries internally and redacts the URL) and never propagate back to
// the sample loop. The loop exits on ctx cancellation; any events still queued
// at shutdown are abandoned — losing an in-flight alert on a clean shutdown is
// acceptable and avoids blocking Run's drain.
func (p *Poller) runDispatchLoop(ctx context.Context) {
	host := p.Evaluator.Host()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-p.dispatch:
			notifier := p.Notifier
			if notifier == nil {
				notifier = notify.NoOp{}
			}
			msg := alerts.FormatMessage(ev, host)
			if err := notifier.Notify(ctx, msg); err != nil {
				if !errors.Is(err, context.Canceled) {
					slog.Warn("poller: alert delivery failed",
						"condition", ev.Condition, "kind", ev.Kind.String(), "err", err)
				}
			}
		}
	}
}
