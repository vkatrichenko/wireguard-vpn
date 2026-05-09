package server

import (
	"log/slog"
	"net/http"
)

// handleGetPartialDashboard renders just the seven data-card fragment that
// wraps inside the page's <main id="dashboard-content"> element. htmx polls
// this endpoint every 10 seconds and swaps the response into innerHTML —
// keeping the four trend-chart canvases (which Chart.js manages directly via
// /api/metrics) outside the swap target so they aren't destroyed on each
// refresh.
//
// Data gathering reuses buildPageData so a new card / new error gate added
// in handleIndex is automatically picked up here without drift.
//
// On render error we still return a 500: unlike handleIndex (which serves
// the full page and prefers partial degradation across cards), here the
// fragment IS the entire response — htmx's default error handler will leave
// the previous content in place and the next 10s tick will retry.
func (s *server) handleGetPartialDashboard(w http.ResponseWriter, r *http.Request) {
	data := s.buildPageData(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "dashboard-content", data); err != nil {
		slog.Error("GET /partial/dashboard: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}
