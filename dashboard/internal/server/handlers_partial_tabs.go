package server

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"wireguard-dashboard/internal/disk"
	"wireguard-dashboard/internal/processes"
)

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
// flagged after Slice 6.
type systemTabData struct {
	Disk      diskCardData
	Processes processesCardData
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
	data := systemTabData{}

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

// handleGetPartialNetwork renders the Network tab body — placeholder in Slice 1.
func (s *server) handleGetPartialNetwork(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "network-tab", nil); err != nil {
		slog.Error("GET /partial/network: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// handleGetPartialEvents renders the Events tab body — placeholder in Slice 1.
func (s *server) handleGetPartialEvents(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "events-tab", nil); err != nil {
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
