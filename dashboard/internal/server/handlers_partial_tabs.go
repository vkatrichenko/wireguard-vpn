package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"wireguard-dashboard/internal/alerts"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/disk"
	"wireguard-dashboard/internal/netdev"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/processes"
	"wireguard-dashboard/internal/serverinfo"
)

// rangeSelectorData is the view-model for the shared `range-selector` partial
// rendered above the System and Network tab bodies. Tab seeds the <select>'s
// id (so the label/for pair stays unique when both tabs ever co-exist on a
// debug page); Endpoint is the hx-get target for the form; Current is the
// validated range string used to pre-select the right <option>; Options is
// the canonical ordered enum (rangeEnumOptions) passed through so the template
// stays loop-friendly without referencing the package-level var directly.
type rangeSelectorData struct {
	Tab      string
	Endpoint string
	Current  string
	Options  []string
}

// parseRangeParam validates ?range= against rangeEnumMap, defaulting to "24h"
// when absent. On miss it writes the canonical 400 body via rangeEnumErrMsg
// and returns ok=false so the caller can return without further work. Shared
// between handleGetPartialSystem and handleGetPartialNetwork — the API chart
// handlers (handleGetMetricsSystem etc.) keep their inline validation so the
// JSON-vs-HTML branches stay legible end-to-end.
func parseRangeParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "24h"
	}
	if _, ok := rangeEnumMap[rangeStr]; !ok {
		http.Error(w, fmt.Sprintf(rangeEnumErrMsg, rangeStr), http.StatusBadRequest)
		return "", false
	}
	return rangeStr, true
}

// clientsTabData is the view-model handed to the `clients` template. It pairs
// the joined ClientRow slice with a single error string so the fragment can
// always render *something* — htmx's innerHTML swap would otherwise leave the
// previous tab body in place on a 500, which is the wrong UX for a tab body.
// Unexported because no other file consumes it; co-located with the handler so
// the contract is one-stop.
type clientsTabData struct {
	Rows  []ClientRow
	Error string
	// Drift is the count of DB clients absent from the boot clients.json
	// baseline (spec 015) — rendered as a small badge in the Clients-tab
	// heading; zero hides it.
	Drift int
	// Message / MessageKind carry the optional outcome line swapped in after an
	// add/edit/remove mutation (spec 015, Slice 6). Both are empty on the
	// tab-tick render so the card shows the bare list + controls; the write
	// handlers populate them on the outerHTML re-render of the clients-card
	// fragment. MessageKind ∈ "" | "success" | "error" | "info" — mirrors the
	// webhook-card outcome convention.
	Message     string
	MessageKind string
}

// buildClientsTabData performs the DB-clients + live-wg join that both the
// Clients-tab partial route and the Slice-6 write handlers render from. The
// join failing is NOT a hard error: it populates Error so the fragment can
// render an inline message rather than 500-ing (which htmx would swallow,
// leaving stale content). Extracted so the partial render and every
// post-mutation re-render stay in lock-step — one place computes rows + drift.
func (s *server) buildClientsTabData(ctx context.Context) clientsTabData {
	data := clientsTabData{}
	dbClients, clientsErr := s.clientsSvc.List(ctx)
	peers, peersErr := s.wgSvc.Show(ctx)
	if joined := errors.Join(clientsErr, peersErr); joined != nil {
		slog.Error("clients tab: clients fetch failed", "err", joined)
		data.Error = joined.Error()
	} else {
		data.Rows = buildClientRows(dbClients, peers, time.Now(), s.geoipSvc)
		data.Drift = s.computeDrift(ctx, dbClients)
	}
	return data
}

// handleGetPartialOverview renders the cards fragment for the Overview tab.
// Shares buildPageData with handleIndex so the cold-load page render and the
// htmx tab swap stay in lock-step (any new card or new error gate is one
// change in buildPageData, not two).
//
// On render error we still return a 500 — the fragment IS the entire response,
// so htmx's default error handler will leave the previous content in place and
// the next tab switch will retry.
func (s *server) handleGetPartialOverview(w http.ResponseWriter, r *http.Request) {
	data := s.buildPageData(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "overview", data); err != nil {
		slog.Error("GET /partial/overview: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// handleGetPartialClients renders the Clients tab body. It joins the
// clientsfile manifest with live `wg show` peers via buildClientRows and
// hands the result to the `clients` template. A fetch failure does NOT
// 500: the fragment populates clientsTabData.Error so the template can
// render an inline error message — a 500 would leave the previous tab
// body intact in htmx, which hides the failure from the operator.
func (s *server) handleGetPartialClients(w http.ResponseWriter, r *http.Request) {
	data := s.buildClientsTabData(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "clients", data); err != nil {
		slog.Error("GET /partial/clients: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// systemTabData is the view-model for the `system-tab` fragment. Disk backs
// the disk-usage table; Processes backs the top-N CPU consumers card. The
// System tab no longer carries the CPU/memory large-numerics card — that
// lives exclusively on Overview to avoid the duplicate render the operator
// flagged after Slice 6. Range is the validated `?range=` value plumbed down
// to the chart partials so each <canvas> carries a data-range attribute and
// the heading reflects the active window. RangeSelector seeds the shared
// <form> rendered above the cards.
type systemTabData struct {
	Range         string
	RangeSelector rangeSelectorData
	Disk          diskCardData
	Processes     processesCardData
}

// diskCardData mirrors the {Mounts, Error} shape the `disk` template branches
// on internally. Inlining the struct here (rather than registering a `dict`
// template helper) keeps the template's data contract type-checked at compile
// time and avoids a global helper that has no other caller yet.
type diskCardData struct {
	Mounts []disk.Mount
	Error  string
}

// processesCardData mirrors the {Procs, Error} shape the `processes` template
// branches on internally. Same rationale as diskCardData — type-checked
// contract at the template boundary, no global helper.
type processesCardData struct {
	Procs []processes.Process
	Error string
}

// handleGetPartialSystem renders the System tab body — the disk usage table
// (sourced from disk.Sample), the top-N processes table (sourced from
// processes.Sample), plus the CPU/memory trend-chart placeholders. Each
// service is sampled independently and degrades per-card: a disk failure
// doesn't blank the processes card and vice versa.
//
// proc.Sample is intentionally not called here: the CPU/memory numeric card
// is rendered on Overview, and the chart-cpu / chart-memory partials are
// pure markup driven by /api/metrics polled from charts.js.
func (s *server) handleGetPartialSystem(w http.ResponseWriter, r *http.Request) {
	rangeStr, ok := parseRangeParam(w, r)
	if !ok {
		return
	}

	data := systemTabData{
		Range: rangeStr,
		RangeSelector: rangeSelectorData{
			Tab:      "system",
			Endpoint: "/partial/system",
			Current:  rangeStr,
			Options:  rangeEnumOptions,
		},
	}

	mounts, err := s.diskSvc.Sample(r.Context())
	if err != nil {
		slog.Error("GET /partial/system: disk sample failed", "err", err)
		data.Disk.Error = err.Error()
	} else {
		data.Disk.Mounts = mounts
	}

	procs, err := s.processesSvc.Sample(r.Context())
	if err != nil {
		slog.Error("GET /partial/system: processes sample failed", "err", err)
		data.Processes.Error = err.Error()
	} else {
		data.Processes.Procs = procs
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "system-tab", data); err != nil {
		slog.Error("GET /partial/system: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// networkTabData is the view-model for the `network-tab` fragment. Stats backs
// the network-rate large-numerics card (same proc.Sample shape as buildPageData
// uses for Overview's network-rate). WgIface backs the WireGuard interface
// stats card (cumulative rx/tx bytes, packets, errs, dropped, peer count).
// AggregateTraffic backs the in-period rx/tx delta line under the chart pair.
//
// Range is the validated `?range=` value plumbed down to the chart partials
// (data-range attribute + heading text). RangeSelector seeds the shared
// <form> rendered above the cards.
type networkTabData struct {
	Range            string
	RangeSelector    rangeSelectorData
	Stats            *proc.Stats
	StatsError       string
	WgIface          wgIfaceStatsCardData
	AggregateTraffic aggregateTrafficCardData
}

// wgIfaceStatsCardData mirrors the {Stats, Error} shape the `wg-iface-stats`
// template branches on internally. Same rationale as diskCardData — inline the
// struct so the template's data contract stays type-checked at compile time.
type wgIfaceStatsCardData struct {
	Stats netdev.Stats
	Error string
}

// aggregateTrafficCardData mirrors the {Range, RxBytesDelta, TxBytesDelta,
// Error} shape the `aggregate-traffic` template renders. Range is the active
// window label ("24h" today, expanded to the four-value enum in Slice 9). The
// two deltas are clamped to zero so a counter reset (interface bounce) never
// renders a negative byte count.
type aggregateTrafficCardData struct {
	Range        string
	RxBytesDelta int64
	TxBytesDelta int64
	Error        string
}

// handleGetPartialNetwork renders the Network tab body — the network-rate
// large-numerics card (proc.Sample), the WireGuard interface stats card
// (netdev.Sample), and the aggregate-traffic line (delta across the last 24h
// of traffic_metrics rows). Each fetch degrades per-card: a netdev failure
// doesn't blank the rate or aggregate cards, and a DB query failure on
// aggregate traffic leaves the other two cards intact.
//
// The aggregate-traffic query window follows the validated ?range= via
// rangeEnumMap (1h/6h/24h/7d → Duration). len(rows) < 2 leaves both deltas
// at 0 with no error — the template renders "0 B in / 0 B out", which is the
// right shape for a freshly-deployed dashboard before the poller has
// accumulated two samples.
func (s *server) handleGetPartialNetwork(w http.ResponseWriter, r *http.Request) {
	rangeStr, ok := parseRangeParam(w, r)
	if !ok {
		return
	}

	data := networkTabData{
		Range: rangeStr,
		RangeSelector: rangeSelectorData{
			Tab:      "network",
			Endpoint: "/partial/network",
			Current:  rangeStr,
			Options:  rangeEnumOptions,
		},
		AggregateTraffic: aggregateTrafficCardData{Range: rangeStr},
	}

	if stats, err := s.procSvc.Sample(r.Context()); err != nil {
		slog.Error("GET /partial/network: proc sample failed", "err", err)
		data.StatsError = err.Error()
	} else {
		data.Stats = &stats
	}

	if ifaceStats, err := s.netdevSvc.Sample(r.Context()); err != nil {
		slog.Error("GET /partial/network: netdev sample failed", "err", err)
		data.WgIface.Error = err.Error()
	} else {
		data.WgIface.Stats = ifaceStats
	}

	// Delta across the active range (rangeEnumMap is the canonical 1h/6h/24h/7d
	// → Duration map shared with the chart endpoints). Negative deltas (counter
	// reset on interface bounce or wraparound) clamp to zero rather than render
	// as "-3.2 MB in"; len(rows) < 2 silently leaves the zero values in place.
	now := time.Now()
	rows, err := s.metricsDB.QueryTrafficMetrics(r.Context(), now.Add(-rangeEnumMap[rangeStr]), now)
	if err != nil {
		slog.Error("GET /partial/network: traffic metrics query failed", "err", err)
		data.AggregateTraffic.Error = err.Error()
	} else if len(rows) >= 2 {
		rxDelta := rows[len(rows)-1].RxBytesCum - rows[0].RxBytesCum
		txDelta := rows[len(rows)-1].TxBytesCum - rows[0].TxBytesCum
		if rxDelta < 0 {
			rxDelta = 0
		}
		if txDelta < 0 {
			txDelta = 0
		}
		data.AggregateTraffic.RxBytesDelta = rxDelta
		data.AggregateTraffic.TxBytesDelta = txDelta
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "network-tab", data); err != nil {
		slog.Error("GET /partial/network: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// eventsWindow caps how far back the Events tab looks for handshake rows.
// 30d matches the retention horizon set in Slice 3 — anything older has
// already been pruned, so widening past it would be wasted query effort.
// The DESC ORDER BY plus LIMIT 50 below picks the 50 newest from within
// this window; in practice operators will see the most-recent 50 regardless
// of how recently the dashboard came up.
const eventsWindow = 30 * 24 * time.Hour

// eventsLimit is the cap raised from the 10-row Overview card to 50 for the
// dedicated Events tab per Slice 10. Hardcoded here rather than threaded
// through `?limit=…` because the spec pins one value and a URL knob would
// invite scraping the full retention horizon, which is also why the window
// is fixed above.
const eventsLimit = 50

// eventsTabData is the view-model for the Events tab. Handshakes is the existing
// 50-newest handshake_events surface; Alerts is the in-memory ring of recent
// alert fire/recovery transitions from the status holder (spec 007, Slice 5),
// newest-first. Each surface branches on its own emptiness so one being empty
// doesn't blank the other.
type eventsTabData struct {
	Handshakes []db.HandshakeEvent
	Alerts     []alerts.LogEntry
}

// handleGetPartialEvents renders the Events tab body — the 50 newest
// handshake_events rows over the retention horizon, plus the recent alert
// fire/recovery transitions from the status holder. A query failure renders
// the empty-state ("No recent handshakes.") rather than a 500: htmx would
// otherwise leave the previous tab body in place, and an empty list is a
// less-confusing fallback than the wrong tab content. The error is logged
// via slog so the operator still sees the actual cause in journald.
func (s *server) handleGetPartialEvents(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	events, err := s.metricsDB.QueryHandshakeEvents(r.Context(), now.Add(-eventsWindow), now, eventsLimit)
	if err != nil {
		slog.Error("GET /partial/events: handshake events query failed", "err", err)
		events = nil
	}

	data := eventsTabData{
		Handshakes: events,
		Alerts:     s.alertSnapshot().Recent,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "events-tab", data); err != nil {
		slog.Error("GET /partial/events: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// aboutTabData is the view-model for the About tab. Four cards: EC2 metadata
// (IMDSv2-sourced), build-time metadata (ldflags-injected package vars),
// OS/kernel metadata (local syscalls + /etc/os-release), and the server-info
// card re-used from Overview so the WireGuard public-key copy button is
// reachable from both tabs. Each card branches on its own *Error field —
// one fetch failing doesn't blank the others.
type aboutTabData struct {
	EC2             aboutEC2CardData
	Binary          aboutBinaryCardData
	OS              aboutOSCardData
	ServerInfo      serverinfo.ServerInfo
	ServerInfoError string
	Webhook         aboutWebhookCardData
}

// aboutWebhookCardData is the view-model for cards/webhook.html (spec 008,
// Slice 4). Status is the masked-only holder view (a nil holder collapses to
// the disabled state inside webhookStatus, so this is always safe to render).
// Message/MessageKind carry the optional outcome line swapped in after a
// set/test/revert; both are empty on the tab-tick render so the card shows the
// bare status + controls. MessageKind ∈ "" | "success" | "error" | "info".
type aboutWebhookCardData struct {
	Status      webhookStatusResponse
	Message     string
	MessageKind string
}

// aboutEC2CardData mirrors the keys cards/about-ec2.html branches on. The
// four IMDS reads are joined into a single Error string via errors.Join so
// the template doesn't need to know which of the four endpoints failed —
// the operator sees the full join in the error message and dashboard logs
// carry the structured slog line.
type aboutEC2CardData struct {
	PublicIP         string
	InstanceType     string
	AvailabilityZone string
	AMIID            string
	Error            string
}

// aboutBinaryCardData is the view-model for cards/about-binary.html. All
// fields are sourced from serverinfoSvc.Build (the BuildInfo struct populated
// in cmd/main.go from the -ldflags -X package vars). No error path — the
// package vars always have at least their sentinel ("dev" for the release
// tag, "unknown" for the rest).
type aboutBinaryCardData struct {
	ReleaseTag string
	BuildSHA   string
	BuildTime  string
	GoVersion  string
}

// aboutOSCardData is the view-model for cards/about-os.html. Kernel and
// OSRelease are independent readers (uname syscall vs. /etc/os-release file
// read), so each has its own *Error string. OSRelease's macOS-degraded
// path returns OSReleaseInfo{ID: "unknown"} + a non-nil error — we still
// surface the error so the operator can tell "running off-Linux" from
// "real read failure on EC2".
type aboutOSCardData struct {
	Kernel         serverinfo.KernelInfo
	OSRelease      serverinfo.OSReleaseInfo
	KernelError    string
	OSReleaseError string
}

// handleGetPartialAbout renders the About tab body. Fans out the four IMDS
// reads + the ServerInfo fetch (which itself runs IMDS PublicIP + the wg
// public-key shell-out in parallel) into one wg.Wait, then synchronously
// reads kernel + OS-release (both cheap local calls — parallelism would be
// overkill). Each card degrades independently per the *Error fields.
//
// Yes, PublicIP is fetched twice: once by the EC2 card's IMDS fan-out, once
// by serverinfoSvc.Get() for the Server endpoint card. IMDSv2 reads are
// single-digit-ms link-local calls and the parallel block hides the cost.
// Coalescing them would require either splitting Get() or threading the
// already-fetched value into a private helper — both more code than the
// duplication is worth.
func (s *server) handleGetPartialAbout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	data := aboutTabData{
		Binary: aboutBinaryCardData{
			ReleaseTag: s.serverinfoSvc.Build.ReleaseTag,
			BuildSHA:   s.serverinfoSvc.Build.SHA,
			BuildTime:  s.serverinfoSvc.Build.Time,
			GoVersion:  s.serverinfoSvc.Build.GoVersion,
		},
		// Masked-only holder view; the tab tick carries no outcome message.
		Webhook: aboutWebhookCardData{Status: s.webhookStatus()},
	}

	var (
		wg sync.WaitGroup

		ip, instanceType, az, ami     string
		ipErr, typeErr, azErr, amiErr error
		info                          serverinfo.ServerInfo
		infoErr                       error
	)

	wg.Add(5)
	go func() { defer wg.Done(); ip, ipErr = s.serverinfoSvc.IMDS.PublicIP(ctx) }()
	go func() { defer wg.Done(); instanceType, typeErr = s.serverinfoSvc.IMDS.InstanceType(ctx) }()
	go func() { defer wg.Done(); az, azErr = s.serverinfoSvc.IMDS.AvailabilityZone(ctx) }()
	go func() { defer wg.Done(); ami, amiErr = s.serverinfoSvc.IMDS.AMIID(ctx) }()
	go func() { defer wg.Done(); info, infoErr = s.serverinfoSvc.Get(ctx) }()
	wg.Wait()

	if joined := errors.Join(ipErr, typeErr, azErr, amiErr); joined != nil {
		slog.Error("GET /partial/about: EC2 metadata fetch failed", "err", joined)
		data.EC2.Error = joined.Error()
	} else {
		data.EC2 = aboutEC2CardData{
			PublicIP:         ip,
			InstanceType:     instanceType,
			AvailabilityZone: az,
			AMIID:            ami,
		}
	}

	if infoErr != nil {
		slog.Error("GET /partial/about: server-info fetch failed", "err", infoErr)
		data.ServerInfoError = infoErr.Error()
	} else {
		data.ServerInfo = info
	}

	// Kernel is a cheap syscall (microseconds); sequential is fine.
	if kernel, err := s.serverinfoSvc.Kernel(); err != nil {
		slog.Error("GET /partial/about: kernel read failed", "err", err)
		data.OS.KernelError = err.Error()
	} else {
		data.OS.Kernel = kernel
	}

	// OSRelease returns OSReleaseInfo{ID: "unknown"} + a wrapped read error
	// on macOS local dev; capture both so the template can decide whether to
	// show the populated row (it does — KernelError is what gates blanking).
	osInfo, osErr := s.serverinfoSvc.OSRelease()
	data.OS.OSRelease = osInfo
	if osErr != nil {
		slog.Warn("GET /partial/about: os-release read failed", "err", osErr)
		data.OS.OSReleaseError = osErr.Error()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "about", data); err != nil {
		slog.Error("GET /partial/about: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}
