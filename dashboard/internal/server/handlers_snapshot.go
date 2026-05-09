package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/serverinfo"
	"wireguard-dashboard/internal/systemd"
)

// snapshotResponse is the payload of GET /api/snapshot — the single aggregator
// endpoint that fans out to every backend service the dashboard owns and
// returns a per-section view of the result.
//
// Per-section error fields (`*_error`) are OMITTED on success and POPULATED
// on failure. Pointer fields (Server, Service, Stats) let JSON omit them
// entirely on failure (cleaner than the zero-value alternative). Clients is
// always present as a slice — empty `[]` on full failure, populated on
// success — so the front-end never has to special-case `null`.
type snapshotResponse struct {
	Server      *serverinfo.ServerInfo `json:"server,omitempty"`
	ServerError string                 `json:"server_error,omitempty"`

	Service      *systemd.ServiceStatus `json:"service,omitempty"`
	ServiceError string                 `json:"service_error,omitempty"`

	Clients      []ClientRow `json:"clients"`
	ClientsError string      `json:"clients_error,omitempty"`

	Stats      *proc.Stats `json:"stats,omitempty"`
	StatsError string      `json:"stats_error,omitempty"`
}

// handleGetSnapshot fans out to all five backend services in parallel and
// composes a snapshotResponse. Per-section failures are surfaced via the
// matching `*_error` field rather than aborting the whole response — the
// dashboard is a read-only ops view, and a partial render is more useful
// than a bare 500 when one sub-fetch is flaky.
//
// Concurrency model: four goroutines (one per logical fetch group) write to
// dedicated variable pairs; no shared state, so no mutex. `clientsfile.Load`
// and `wg.Show` run sequentially within their shared goroutine because the
// join needs both — running them in parallel would save ~milliseconds on a
// page that already serves in tens.
//
// `Clients` is initialised to a non-nil empty slice up front so the JSON
// encoder emits `"clients": []` even on full failure. The other pointer
// fields stay nil until populated; their `omitempty` tags drop them from
// the payload when a fetch fails.
//
// Marshal failure is the only condition that produces a 500 — that would
// indicate a logic bug (none of the value types include unencodable fields),
// not a transient infrastructure issue.
func (s *server) handleGetSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var (
		wg sync.WaitGroup

		serverInfo serverinfo.ServerInfo
		serverErr  error

		serviceStatus systemd.ServiceStatus
		serviceErr    error

		clientList []ClientRow
		clientsErr error

		stats    proc.Stats
		statsErr error
	)

	wg.Add(4)

	go func() {
		defer wg.Done()
		serverInfo, serverErr = s.serverinfoSvc.Get(ctx)
	}()

	go func() {
		defer wg.Done()
		serviceStatus, serviceErr = s.systemdSvc.Get(ctx)
	}()

	go func() {
		defer wg.Done()
		stats, statsErr = s.procSvc.Sample(ctx)
	}()

	go func() {
		defer wg.Done()
		// The two sub-fetches run sequentially here — the join needs both,
		// so parallelising inside this goroutine would only complicate the
		// error joining without measurable wall-clock gain.
		cs, cErr := s.clientsfileSvc.Load(ctx)
		ps, pErr := s.wgSvc.Show(ctx)
		if joined := errors.Join(cErr, pErr); joined != nil {
			clientsErr = joined
			return
		}
		clientList = buildClientRows(cs, ps, time.Now())
	}()

	wg.Wait()

	// Initialise Clients to a non-nil empty slice so the JSON encoder emits
	// `"clients": []` even when every sub-fetch fails. Other pointer fields
	// stay nil and their `omitempty` tags drop them from the payload.
	resp := snapshotResponse{
		Clients: make([]ClientRow, 0),
	}

	if serverErr != nil {
		slog.Error("GET /api/snapshot: serverinfo fetch failed", "err", serverErr)
		resp.ServerError = serverErr.Error()
	} else {
		info := serverInfo
		resp.Server = &info
	}

	if serviceErr != nil {
		slog.Error("GET /api/snapshot: systemd fetch failed", "err", serviceErr)
		resp.ServiceError = serviceErr.Error()
	} else {
		status := serviceStatus
		resp.Service = &status
	}

	if clientsErr != nil {
		slog.Error("GET /api/snapshot: clients fetch failed", "err", clientsErr)
		resp.ClientsError = clientsErr.Error()
	} else if clientList != nil {
		resp.Clients = clientList
	}

	if statsErr != nil {
		slog.Error("GET /api/snapshot: proc sample failed", "err", statsErr)
		resp.StatsError = statsErr.Error()
	} else {
		st := stats
		resp.Stats = &st
	}

	body, err := json.Marshal(resp)
	if err != nil {
		// All embedded value types contain only stdlib-marshallable fields
		// (strings, ints, time.Time, time.Duration) — a marshal failure
		// here would indicate a logic bug, not a runtime input issue.
		slog.Error("GET /api/snapshot: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
