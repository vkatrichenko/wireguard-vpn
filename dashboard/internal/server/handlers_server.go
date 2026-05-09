package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// handleGetServer returns the JSON snapshot of the server-info card data:
// public IPv4 (from IMDSv2), the WireGuard listening port, and the server's
// WireGuard public key. The underlying serverinfo.Service fans out IMDS and
// `wg show` calls in parallel; either failure produces a 500 with the error
// surfaced in the response body so the operator sees the actionable cause
// rather than a bare status code. Errors are also logged via slog so they
// land in journald on the EC2 host.
//
// Request timeouts are inherited from r.Context(): the parent http.Server's
// ReadHeaderTimeout (and any future WriteTimeout) bound the deadline, and
// the IMDS HTTP client + exec.CommandContext both honor that context. No
// extra timeout is layered here to keep the contract single-sourced.
func (s *server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	info, err := s.serverinfoSvc.Get(r.Context())
	if err != nil {
		slog.Error("GET /api/server: serverinfo fetch failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(info)
	if err != nil {
		// Marshal of a struct with only string/int fields can't realistically
		// fail, but surface it consistently if it ever does.
		slog.Error("GET /api/server: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
