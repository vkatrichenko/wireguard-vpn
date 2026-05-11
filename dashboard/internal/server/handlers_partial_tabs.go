package server

import (
	"log/slog"
	"net/http"
)

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

// handleGetPartialClients renders the Clients tab body. Slice 1 ships a static
// "Coming soon" placeholder; later slices populate the real client table here.
func (s *server) handleGetPartialClients(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "clients", nil); err != nil {
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
