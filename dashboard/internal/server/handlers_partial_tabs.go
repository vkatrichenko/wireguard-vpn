package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"wireguard-dashboard/internal/disk"
	"wireguard-dashboard/internal/netdev"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/processes"
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
}

// handleGetPartialOverview renders the v3 cards fragment for the Overview tab.
// It is the successor to the old handleGetPartialDashboard: same data-gathering
// (buildPageData) and same on-the-wire card markup, just executed via the
// `overview` named template instead of `dashboard-content`. Slice 14 will
// retire the /partial/dashboard alias entirely; until then both routes resolve
// to this handler.
//
// On render error we still return a 500 — the fragment IS the entire response,
// so htmx's default error handler will leave the previous content in place and
// the next 10s tick will retry.
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
	data := clientsTabData{}

	clients, clientsErr := s.clientsfileSvc.Load(r.Context())
	peers, peersErr := s.wgSvc.Show(r.Context())
	if joined := errors.Join(clientsErr, peersErr); joined != nil {
		slog.Error("GET /partial/clients: clients fetch failed", "err", joined)
		data.Error = joined.Error()
	} else {
		data.Rows = buildClientRows(clients, peers, time.Now(), s.geoipSvc)
	}

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

// handleGetPartialEvents renders the Events tab body — the 50 newest
// handshake_events rows over the retention horizon. A query failure renders
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "events-tab", events); err != nil {
		slog.Error("GET /partial/events: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// handleGetPartialAbout renders the About tab body — placeholder in Slice 1.
func (s *server) handleGetPartialAbout(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "about", nil); err != nil {
		slog.Error("GET /partial/about: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}
