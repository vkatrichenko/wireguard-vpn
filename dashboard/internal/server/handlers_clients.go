package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/p95"
)

// clientDetailData is the view-model for the inline expand fragment rendered
// by `client-detail.html`. P95Bps is the 95th-percentile per-sample throughput
// (rx+tx) across the active range; 0 when fewer than two samples are present.
// Range is the active window string (one of the four-value enum 1h|6h|24h|7d)
// — hardcoded to "24h" here until Slice 9 wires the `?range=` parser through.
// Unexported and co-located here — mirrors clientsTabData's pattern in
// handlers_partial_tabs.go.
type clientDetailData struct {
	PublicKey string
	Range     string
	P95Bps    int64
}

// handleGetClients returns the JSON snapshot of the joined client list:
// every row is one peer combining manifest metadata (name, tunnel address)
// with live `wg show wg0 dump` state (latest handshake, byte counters,
// endpoint). The join itself lives in buildClientRows so the page-render
// path (handleIndex) can reuse it without duplicating logic.
//
// Either underlying fetch failing produces a 500 with the error in the body,
// matching the sibling /api/server and /api/service handlers. We do not fall
// back to a partial response (e.g. "live state failed, here's the manifest")
// — without the join the UI can't show status, which is the whole point of
// the endpoint.
//
// Request timeouts are inherited from r.Context(): both clientsfile.Load
// (file read, ctx reserved) and wg.Show (exec.CommandContext) honor the
// parent http.Server's deadlines.
func (s *server) handleGetClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.clientsfileSvc.Load(r.Context())
	if err != nil {
		slog.Error("GET /api/clients: clientsfile load failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	peers, err := s.wgSvc.Show(r.Context())
	if err != nil {
		slog.Error("GET /api/clients: wg show failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rows := buildClientRows(clients, peers, time.Now(), s.geoipSvc)

	// json.Marshal of a nil slice emits "null"; an empty `[]ClientRow{}`
	// emits "[]". buildClientRows always returns a non-nil slice (it
	// constructs via make([]ClientRow, 0, ...)) so the output is always
	// a valid JSON array.
	body, err := json.Marshal(rows)
	if err != nil {
		slog.Error("GET /api/clients: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleGetPartialClientDetail renders the inline expand fragment for one
// client row: a chart canvas wrapper (JS hydrates it in Slice 5 sub-task 6)
// plus the 24h p95 throughput figure. The pubkey path-param is validated
// against the clientsfile manifest — unknown / empty keys return 404 so a
// stale link or a typo doesn't render a chart against nothing.
//
// Error model:
//   - clientsfile load failure prevents the pubkey-validity check, so we
//     500 the request (we can't safely confirm or deny the key).
//   - DB query failure is logged and falls through to a P95Bps=0 render
//     rather than a 500; htmx would otherwise leave the previous swap
//     content in place, which is a worse UX than a brief "0 B/s" cell.
//
// Range is hardcoded to 24h here — the `?range=` parser lands in Slice 9.
func (s *server) handleGetPartialClientDetail(w http.ResponseWriter, r *http.Request) {
	pubkey := r.PathValue("pubkey")
	if pubkey == "" {
		http.NotFound(w, r)
		return
	}

	clients, err := s.clientsfileSvc.Load(r.Context())
	if err != nil {
		slog.Error("GET /partial/clients/.../detail: clientsfile load failed", "err", err)
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

	data := clientDetailData{PublicKey: pubkey, Range: "24h"}

	now := time.Now()
	rows, err := s.metricsDB.QueryClientTraffic(r.Context(), pubkey, now.Add(-24*time.Hour), now)
	if err != nil {
		slog.Error("GET /partial/clients/.../detail: query client_traffic failed", "err", err, "pubkey", pubkey)
		// Fall through with P95Bps = 0 — see error-model note above.
	} else {
		data.P95Bps = p95ThroughputBps(rows)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "client-detail", data); err != nil {
		slog.Error("GET /partial/clients/.../detail: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// p95ThroughputBps computes the 95th-percentile per-sample throughput (in
// bytes/sec) across consecutive client_traffic rows. Throughput between two
// adjacent samples is (Δrx + Δtx) / Δt seconds. Fewer than two rows or a
// non-positive Δt yields 0 — the chart's empty/cold-start state.
//
// Percentile via internal/p95.Nearest (nearest-rank, no interpolation).
func p95ThroughputBps(rows []db.ClientTraffic) int64 {
	if len(rows) < 2 {
		return 0
	}
	rates := make([]float64, 0, len(rows)-1)
	for i := 1; i < len(rows); i++ {
		dt := rows[i].TS.Sub(rows[i-1].TS).Seconds()
		if dt <= 0 {
			continue
		}
		dRx := rows[i].RxBytesCum - rows[i-1].RxBytesCum
		dTx := rows[i].TxBytesCum - rows[i-1].TxBytesCum
		// Negative deltas (counter resets after a service restart) collapse
		// to zero rather than pollute the percentile with a negative outlier.
		if dRx < 0 {
			dRx = 0
		}
		if dTx < 0 {
			dTx = 0
		}
		rates = append(rates, float64(dRx+dTx)/dt)
	}
	return int64(p95.Nearest(rates))
}
