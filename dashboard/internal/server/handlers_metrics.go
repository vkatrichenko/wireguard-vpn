package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// metricsResponse is the payload of GET /api/metrics?range=<duration> — the
// time-series feed for the trend charts. The shape is column-oriented
// (parallel arrays per series) rather than row-oriented because Chart.js
// consumes parallel TS / value arrays directly when configured with a time
// scale; reshaping is cheaper here than in the browser.
//
// time.Time JSON-marshals as RFC 3339, which Chart.js parses natively under
// the time scale (or via chartjs-adapter-date-fns / -luxon). The arrays are
// always non-nil empty slices on success so the encoder emits "[]" rather
// than "null"; that lets the front-end render a "no data" state from
// arrays.length === 0 without a separate sentinel field.
//
// Per-client traffic is intentionally omitted from v1 — there's no card in
// the spec that consumes it, and shipping it now would bloat the response
// without a consumer. Add it later if the per-client trend chart lands.
type metricsResponse struct {
	From    time.Time     `json:"from"`
	To      time.Time     `json:"to"`
	System  systemSeries  `json:"system"`
	Traffic trafficSeries `json:"traffic"`
}

// systemSeries holds CPU% and memory% per timestamp as three parallel
// slices (TS[i] is the timestamp of CPUPct[i] / MemPct[i]).
type systemSeries struct {
	TS     []time.Time `json:"ts"`
	CPUPct []float64   `json:"cpu_pct"`
	MemPct []float64   `json:"mem_pct"`
}

// trafficSeries holds wg0 cumulative rx/tx bytes per timestamp as three
// parallel slices. Cumulative means counter-since-interface-up — Chart.js
// callers should derive a rate by subtracting neighbouring samples.
type trafficSeries struct {
	TS         []time.Time `json:"ts"`
	RxBytesCum []int64     `json:"rx_bytes_cum"`
	TxBytesCum []int64     `json:"tx_bytes_cum"`
}

// rangeBounds clamps the user-supplied range duration. minRange rejects
// (with 400) durations below the poller's tick budget — anything finer
// is asking for empty / single-sample charts. maxRange silently caps at
// the poller's retention horizon (25h) so a hostile client can't request
// arbitrarily large windows.
const (
	minRange     = 1 * time.Minute
	maxRange     = 25 * time.Hour
	defaultRange = 24 * time.Hour
)

// handleGetMetrics serves the time-series feed for the trend charts. It
// parses ?range=<duration> (Go duration syntax — "24h", "30m", "1h"),
// clamps to [minRange, maxRange], and runs the system + traffic queries
// in parallel. Either query failing returns 500 — unlike /api/snapshot
// there's no useful partial-render path here, since Chart.js needs both
// axes to draw a meaningful chart.
func (s *server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rangeStr := r.URL.Query().Get("range")
	duration := defaultRange
	if rangeStr != "" {
		parsed, err := time.ParseDuration(rangeStr)
		if err != nil {
			slog.Warn("GET /api/metrics: invalid range", "range", rangeStr, "err", err)
			http.Error(w, fmt.Sprintf("invalid range %q: %v", rangeStr, err), http.StatusBadRequest)
			return
		}
		duration = parsed
	}

	// Reject implausibly small windows with 400 — the poller's 30s tick
	// means anything under a minute is just noise. Silently clamp the
	// upper bound to retention so the front-end can request "everything"
	// without a bespoke sentinel value.
	if duration < minRange {
		http.Error(w, fmt.Sprintf("range %s below minimum %s", duration, minRange), http.StatusBadRequest)
		return
	}
	if duration > maxRange {
		duration = maxRange
	}

	now := time.Now().UTC()
	from := now.Add(-duration)
	to := now

	var (
		wg sync.WaitGroup

		sysRows []struct {
			ts             time.Time
			cpuPct, memPct float64
		}
		sysErr error

		trafficRows []struct {
			ts         time.Time
			rxCum, txCum int64
		}
		trafficErr error
	)

	wg.Add(2)

	go func() {
		defer wg.Done()
		rows, err := s.metricsDB.QuerySystemMetrics(ctx, from, to)
		if err != nil {
			sysErr = err
			return
		}
		sysRows = make([]struct {
			ts             time.Time
			cpuPct, memPct float64
		}, len(rows))
		for i, row := range rows {
			sysRows[i].ts = row.TS
			sysRows[i].cpuPct = row.CPUPct
			sysRows[i].memPct = row.MemPct
		}
	}()

	go func() {
		defer wg.Done()
		rows, err := s.metricsDB.QueryTrafficMetrics(ctx, from, to)
		if err != nil {
			trafficErr = err
			return
		}
		trafficRows = make([]struct {
			ts         time.Time
			rxCum, txCum int64
		}, len(rows))
		for i, row := range rows {
			trafficRows[i].ts = row.TS
			trafficRows[i].rxCum = row.RxBytesCum
			trafficRows[i].txCum = row.TxBytesCum
		}
	}()

	wg.Wait()

	if sysErr != nil {
		slog.Error("GET /api/metrics: system query failed", "err", sysErr)
		http.Error(w, sysErr.Error(), http.StatusInternalServerError)
		return
	}
	if trafficErr != nil {
		slog.Error("GET /api/metrics: traffic query failed", "err", trafficErr)
		http.Error(w, trafficErr.Error(), http.StatusInternalServerError)
		return
	}

	// Pre-allocate non-nil empty slices so the JSON encoder emits "[]"
	// not "null" — the front-end "no data" branch keys off arrays.length.
	resp := metricsResponse{
		From: from,
		To:   to,
		System: systemSeries{
			TS:     make([]time.Time, 0, len(sysRows)),
			CPUPct: make([]float64, 0, len(sysRows)),
			MemPct: make([]float64, 0, len(sysRows)),
		},
		Traffic: trafficSeries{
			TS:         make([]time.Time, 0, len(trafficRows)),
			RxBytesCum: make([]int64, 0, len(trafficRows)),
			TxBytesCum: make([]int64, 0, len(trafficRows)),
		},
	}

	for _, row := range sysRows {
		resp.System.TS = append(resp.System.TS, row.ts)
		resp.System.CPUPct = append(resp.System.CPUPct, row.cpuPct)
		resp.System.MemPct = append(resp.System.MemPct, row.memPct)
	}
	for _, row := range trafficRows {
		resp.Traffic.TS = append(resp.Traffic.TS, row.ts)
		resp.Traffic.RxBytesCum = append(resp.Traffic.RxBytesCum, row.rxCum)
		resp.Traffic.TxBytesCum = append(resp.Traffic.TxBytesCum, row.txCum)
	}

	body, err := json.Marshal(resp)
	if err != nil {
		// All embedded value types contain only stdlib-marshallable fields;
		// a marshal failure here would indicate a logic bug.
		slog.Error("GET /api/metrics: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// clientMetricsResponse is the payload of GET /api/metrics/client/{pubkey}.
// Like metricsResponse it is column-oriented (parallel TS/value arrays) so
// Chart.js can pass the arrays straight into a time-scale dataset without
// reshaping in the browser. From/To echo the resolved window — Range echoes
// the validated string ("1h"/"6h"/"24h"/"7d") so the front-end can label the
// chart without re-parsing the duration.
//
// One sample per consecutive (i-1, i) pair of client_traffic rows, so the
// arrays are always one element shorter than the underlying row count.
type clientMetricsResponse struct {
	PublicKey string      `json:"public_key"`
	Range     string      `json:"range"`
	From      time.Time   `json:"from"`
	To        time.Time   `json:"to"`
	TS        []time.Time `json:"ts"`
	RxRateBps []int64     `json:"rx_rate_bps"`
	TxRateBps []int64     `json:"tx_rate_bps"`
}

// rangeEnumMap pins the four allowed `?range=` values for the chart endpoints
// to their Duration equivalents. Shared deliberately by the per-client chart
// (/api/metrics/client/{pubkey}) and the global system/traffic chart endpoints
// (/api/metrics/system, /api/metrics/traffic) — the four-value enum is the
// same UI affordance everywhere, so the canonical 400 message lives in one
// place too (see rangeEnumErrMsg). The legacy /api/metrics handler keeps its
// time.ParseDuration path so charts.js can be migrated in a follow-up task
// without breaking on-the-wire today.
var rangeEnumMap = map[string]time.Duration{
	"1h":  1 * time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// rangeEnumErrMsg is the canonical 400 body for an out-of-enum ?range= value.
// Centralised so the three chart endpoints emit byte-identical errors — keeps
// front-end error-handling and server-side tests in lock-step.
const rangeEnumErrMsg = "invalid range %q: must be 1h, 6h, 24h, or 7d"

// rangeEnumOptions is the canonical ordered list of allowed ?range= values.
// Co-located with rangeEnumMap so the keys-vs-display-order contract lives in
// one place — Go map iteration order is non-deterministic, so we cannot just
// range over rangeEnumMap when rendering the <select>. Used by the partial-tab
// handlers to populate the range-selector dropdown.
var rangeEnumOptions = []string{"1h", "6h", "24h", "7d"}

// systemMetricsResponse is the payload of GET /api/metrics/system?range=…
// (Slice 9). Column-oriented for the same reason as metricsResponse: Chart.js
// passes parallel TS/value arrays straight into a time-scale dataset. Range
// echoes the validated string so the front-end can label the chart without
// re-parsing the duration; From/To echo the resolved window for stale-data
// detection.
type systemMetricsResponse struct {
	Range  string      `json:"range"`
	From   time.Time   `json:"from"`
	To     time.Time   `json:"to"`
	TS     []time.Time `json:"ts"`
	CPUPct []float64   `json:"cpu_pct"`
	MemPct []float64   `json:"mem_pct"`
}

// trafficMetricsResponse is the payload of GET /api/metrics/traffic?range=…
// (Slice 9). Cumulative bytes are sent through as-is (counter-since-iface-up);
// Chart.js callers derive a rate by subtracting neighbouring samples — same
// contract as the legacy /api/metrics trafficSeries.
type trafficMetricsResponse struct {
	Range      string      `json:"range"`
	From       time.Time   `json:"from"`
	To         time.Time   `json:"to"`
	TS         []time.Time `json:"ts"`
	RxBytesCum []int64     `json:"rx_bytes_cum"`
	TxBytesCum []int64     `json:"tx_bytes_cum"`
}

// handleGetMetricsSystem serves the system-metrics time-series feed for the
// System tab chart. Range is validated against the four-value enum
// (1h/6h/24h/7d) — any other input is a 400. No concurrent fan-out (unlike
// /api/metrics) because there's only one DB query; goroutine overhead would
// dwarf the saved latency.
func (s *server) handleGetMetricsSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "24h"
	}
	duration, ok := rangeEnumMap[rangeStr]
	if !ok {
		http.Error(w, fmt.Sprintf(rangeEnumErrMsg, rangeStr), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	from := now.Add(-duration)
	to := now

	rows, err := s.metricsDB.QuerySystemMetrics(ctx, from, to)
	if err != nil {
		slog.Error("GET /api/metrics/system: query system_metrics failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Pre-allocate non-nil empty slices so the JSON encoder emits "[]" not
	// "null" — same contract as the other chart endpoints.
	resp := systemMetricsResponse{
		Range:  rangeStr,
		From:   from,
		To:     to,
		TS:     make([]time.Time, 0, len(rows)),
		CPUPct: make([]float64, 0, len(rows)),
		MemPct: make([]float64, 0, len(rows)),
	}
	for _, row := range rows {
		resp.TS = append(resp.TS, row.TS)
		resp.CPUPct = append(resp.CPUPct, row.CPUPct)
		resp.MemPct = append(resp.MemPct, row.MemPct)
	}

	body, err := json.Marshal(resp)
	if err != nil {
		slog.Error("GET /api/metrics/system: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleGetMetricsTraffic serves the wg0 cumulative traffic time-series for
// the Network tab chart. Same range-validation contract as
// handleGetMetricsSystem; cumulative bytes pass through unchanged so the
// front-end controls how rates are derived.
func (s *server) handleGetMetricsTraffic(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "24h"
	}
	duration, ok := rangeEnumMap[rangeStr]
	if !ok {
		http.Error(w, fmt.Sprintf(rangeEnumErrMsg, rangeStr), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	from := now.Add(-duration)
	to := now

	rows, err := s.metricsDB.QueryTrafficMetrics(ctx, from, to)
	if err != nil {
		slog.Error("GET /api/metrics/traffic: query traffic_metrics failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := trafficMetricsResponse{
		Range:      rangeStr,
		From:       from,
		To:         to,
		TS:         make([]time.Time, 0, len(rows)),
		RxBytesCum: make([]int64, 0, len(rows)),
		TxBytesCum: make([]int64, 0, len(rows)),
	}
	for _, row := range rows {
		resp.TS = append(resp.TS, row.TS)
		resp.RxBytesCum = append(resp.RxBytesCum, row.RxBytesCum)
		resp.TxBytesCum = append(resp.TxBytesCum, row.TxBytesCum)
	}

	body, err := json.Marshal(resp)
	if err != nil {
		slog.Error("GET /api/metrics/traffic: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleGetMetricsClient serves the per-client rx/tx rate time-series for
// the inline detail chart. Range is validated against the four-value enum
// (1h/6h/24h/7d) — any other input is a 400. The pubkey path-param is
// validated against the clientsfile manifest so a stale link doesn't get
// a chart of an unknown peer.
//
// Rates are derived from consecutive QueryClientTraffic rows: for each pair
// (rows[i-1], rows[i]) the output emits one sample at rows[i].TS with rates
// computed as Δbytes / Δseconds. Negative byte deltas (counter resets after
// a service restart) clamp to 0; non-positive Δt skips the pair.
func (s *server) handleGetMetricsClient(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pubkey := r.PathValue("pubkey")
	if pubkey == "" {
		http.NotFound(w, r)
		return
	}

	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "24h"
	}
	duration, ok := rangeEnumMap[rangeStr]
	if !ok {
		http.Error(w, fmt.Sprintf(rangeEnumErrMsg, rangeStr), http.StatusBadRequest)
		return
	}

	clients, err := s.clientsfileSvc.Load(ctx)
	if err != nil {
		slog.Error("GET /api/metrics/client: clientsfile load failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	found := false
	for _, c := range clients {
		if c.PublicKey == pubkey {
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	now := time.Now().UTC()
	from := now.Add(-duration)
	to := now

	rows, err := s.metricsDB.QueryClientTraffic(ctx, pubkey, from, to)
	if err != nil {
		slog.Error("GET /api/metrics/client: query client_traffic failed", "err", err, "pubkey", pubkey)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Pre-allocate non-nil empty slices so the encoder emits "[]" not "null"
	// when there are <2 rows (or every pair gets skipped) — matches the
	// /api/metrics response contract.
	n := 0
	if len(rows) > 1 {
		n = len(rows) - 1
	}
	resp := clientMetricsResponse{
		PublicKey: pubkey,
		Range:     rangeStr,
		From:      from,
		To:        to,
		TS:        make([]time.Time, 0, n),
		RxRateBps: make([]int64, 0, n),
		TxRateBps: make([]int64, 0, n),
	}

	for i := 1; i < len(rows); i++ {
		dt := rows[i].TS.Sub(rows[i-1].TS).Seconds()
		if dt <= 0 {
			continue
		}
		dRx := rows[i].RxBytesCum - rows[i-1].RxBytesCum
		dTx := rows[i].TxBytesCum - rows[i-1].TxBytesCum
		if dRx < 0 {
			dRx = 0
		}
		if dTx < 0 {
			dTx = 0
		}
		resp.TS = append(resp.TS, rows[i].TS)
		resp.RxRateBps = append(resp.RxRateBps, int64(float64(dRx)/dt))
		resp.TxRateBps = append(resp.TxRateBps, int64(float64(dTx)/dt))
	}

	body, err := json.Marshal(resp)
	if err != nil {
		slog.Error("GET /api/metrics/client: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
