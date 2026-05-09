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
