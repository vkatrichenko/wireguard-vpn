package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/systemd"
)

// serviceResponse is the JSON shape returned by GET /api/service.
//
// Status carries the systemctl-derived service health (the same struct the
// endpoint used to return at the top level). Events is the rolling
// last-hour window of handshake_events rows from the SQLite poller — the
// frontend renders both card groups from a single fetch so they share a
// snapshot.
//
// Events is always a non-nil slice so JSON serialises empty as `[]`,
// never `null` — the frontend treats `null` as a backend-broken signal,
// so an empty steady state must marshal as `[]`.
//
// Note: this changes the wire shape from the previous direct
// systemd.ServiceStatus marshal. Per `context/spec/002-web-dashboard/
// technical-considerations.md` v3 §2.4 this is the documented contract,
// and there is no committed consumer of the old top-level shape yet.
type serviceResponse struct {
	Status systemd.ServiceStatus `json:"status"`
	Events []db.HandshakeEvent   `json:"events"`
}

// handleGetService returns the JSON snapshot of the WireGuard service-health
// card data plus the last-hour handshake events list.
//
// Service status is the load-bearing field: a systemd fetch failure 500s
// the endpoint and surfaces the error in the body so the operator sees the
// actionable cause rather than a bare status code. The events query, by
// contrast, is best-effort — a SQLite read failure is logged and the
// response continues with an empty events slice. The events card on the
// page can still degrade independently when the page-render path notices
// the same error (see handleIndex), but the JSON endpoint must never deny
// an operator the service-status payload because the metrics DB blipped.
//
// The events window is anchored at the request time and looks back exactly
// one hour. We pin `now` once and pass both bounds explicitly so a slow
// DB call cannot widen the window between the upper-bound capture and the
// query execution.
//
// Request timeouts are inherited from r.Context(): the parent http.Server's
// ReadHeaderTimeout (and any future WriteTimeout) bound the deadline, and
// exec.CommandContext / sql.QueryContext both honor that context. No extra
// timeout is layered here to keep the contract single-sourced.
func (s *server) handleGetService(w http.ResponseWriter, r *http.Request) {
	status, err := s.systemdSvc.Get(r.Context())
	if err != nil {
		slog.Error("GET /api/service: systemd fetch failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	now := time.Now()
	events, err := s.metricsDB.QueryHandshakeEvents(r.Context(), now.Add(-1*time.Hour), now, 10)
	if err != nil {
		// Best-effort: surface the failure in logs but don't 500 — the
		// service-status card is the load-bearing payload here.
		slog.Error("GET /api/service: handshake events query failed", "err", err)
		events = nil
	}
	if events == nil {
		// Initialise as a non-nil empty slice so JSON renders `[]`,
		// never `null` (the frontend treats `null` as broken-backend).
		events = make([]db.HandshakeEvent, 0)
	}

	body, err := json.Marshal(serviceResponse{Status: status, Events: events})
	if err != nil {
		// Marshal of a struct with only string/bool/time fields plus a
		// fully-typed slice can't realistically fail, but surface it
		// consistently if it ever does.
		slog.Error("GET /api/service: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
