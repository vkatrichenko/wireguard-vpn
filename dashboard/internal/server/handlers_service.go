package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// handleGetService returns the JSON snapshot of the WireGuard service-health
// card data: the unit's current state token, an active flag, and the last
// ActiveEnterTimestamp (the moment the unit last entered the active state).
// The underlying systemd.Service issues two `systemctl` calls (`is-active`
// and `show -p ActiveEnterTimestamp`); either failure produces a 500 with
// the error surfaced in the response body so the operator sees the
// actionable cause rather than a bare status code. Errors are also logged
// via slog so they land in journald on the EC2 host.
//
// This endpoint is intentionally narrow — it returns only ServiceStatus.
// Handshake events / per-peer last-seen data come from a separate endpoint
// later (once the SQLite poller lands), so we don't pre-shape the payload
// for them here.
//
// Request timeouts are inherited from r.Context(): the parent http.Server's
// ReadHeaderTimeout (and any future WriteTimeout) bound the deadline, and
// exec.CommandContext honors that context. No extra timeout is layered here
// to keep the contract single-sourced.
func (s *server) handleGetService(w http.ResponseWriter, r *http.Request) {
	status, err := s.systemdSvc.Get(r.Context())
	if err != nil {
		slog.Error("GET /api/service: systemd fetch failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(status)
	if err != nil {
		// Marshal of a struct with only string/bool/time fields can't
		// realistically fail, but surface it consistently if it ever does.
		slog.Error("GET /api/service: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
