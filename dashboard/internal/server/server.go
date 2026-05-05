// Package server wires the dashboard's HTTP routes onto a stdlib *http.ServeMux.
//
// Slice 1 scope: two handlers (GET /api/health, GET /) and embedded HTML
// templates. Middleware (auth, logging), additional API endpoints, and asset
// serving land in later slices — keep this file small and dependency-free.
package server

import (
	"html/template"
	"io/fs"
	"net/http"
)

// New returns an http.Handler with the Slice 1 routes wired up. The caller
// passes in an fs.FS rooted at the project's `web/` directory (see the
// embed declaration in dashboard/embed.go); decoupling the FS from the
// package keeps `internal/server` testable with an in-memory fstest.MapFS
// in later slices, and sidesteps the fact that //go:embed paths cannot
// climb out of their containing directory with `..`.
//
// Templates are parsed eagerly so a malformed template fails fast on
// startup rather than on the first request.
func New(webFS fs.FS) (http.Handler, error) {
	tmpl, err := template.ParseFS(webFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("GET /", handleIndex(tmpl))
	return mux, nil
}

// handleHealth serves the unauthenticated liveness probe consumed by the
// systemd unit and (later) by the load-balancer-less VPN client.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Body is hand-rolled rather than encoding/json-marshalled to keep the
	// response byte-stable; this endpoint is on the hot path for monitoring.
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleIndex renders the dashboard placeholder template. Note that ServeMux's
// "GET /" pattern is a catch-all for any path the mux doesn't otherwise match,
// so we explicitly 404 anything that isn't exactly "/" to avoid leaking the
// placeholder for typo'd URLs.
func handleIndex(tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "dashboard.html", nil); err != nil {
			// Headers may already be sent — log via the default logger and
			// give up. Better observability arrives with the slog wiring in a
			// later slice.
			http.Error(w, "template render failed", http.StatusInternalServerError)
			return
		}
	}
}
