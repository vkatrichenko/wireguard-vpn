package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

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

	rows := buildClientRows(clients, peers, time.Now())

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
