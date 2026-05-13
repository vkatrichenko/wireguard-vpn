package server

import (
	"errors"
	"log/slog"
	"net/http"
	"time"
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

// handleGetPartialSystem renders the System tab body — placeholder in Slice 1.
func (s *server) handleGetPartialSystem(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "system-tab", nil); err != nil {
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
