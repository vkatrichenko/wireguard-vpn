// Package server wires the dashboard's HTTP routes onto a stdlib *http.ServeMux.
//
// Slice 1 scope: two handlers (GET /api/health, GET /) and embedded HTML
// templates. Slice 4 adds GET /api/server (server-info JSON snapshot). Middleware
// (auth, logging) and additional API endpoints land in later slices — keep
// this file small and dependency-free.
package server

import (
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"wireguard-dashboard/internal/serverinfo"
)

// server holds the shared dependencies routes need at request time. Keeping
// it unexported is deliberate: callers compose with New(...) and treat the
// result as an http.Handler. Handlers hang off the struct (rather than free
// functions) so adding new dependencies in later slices doesn't ripple
// through every handler signature.
type server struct {
	tmpl          *template.Template
	serverinfoSvc *serverinfo.Service
}

// pageData is the view-model for the dashboard index template. ServerInfoError
// being non-empty means the serverinfo fetch failed and the template should
// render an error block in place of the server-info card. Defined here (rather
// than exported) because it's the contract between handleIndex and
// dashboard.html only — no other package needs to construct it.
type pageData struct {
	ServerInfo      serverinfo.ServerInfo
	ServerInfoError string
}

// New returns an http.Handler with the wired routes. The caller passes in
// an fs.FS rooted at the project's `web/` directory (see the embed declaration
// in dashboard/embed.go); decoupling the FS from the package keeps
// `internal/server` testable with an in-memory fstest.MapFS in later slices,
// and sidesteps the fact that //go:embed paths cannot climb out of their
// containing directory with `..`.
//
// The serverinfo.Service is passed in (rather than constructed here) so tests
// can inject a Service{} literal with fake IMDS / Runner fields. It's the
// last parameter so future additions (a wgctrl client, a SQLite handle) can
// keep appending without breaking call sites mid-list.
//
// Templates are parsed eagerly so a malformed template fails fast on
// startup rather than on the first request.
func New(webFS fs.FS, serverinfoSvc *serverinfo.Service) (http.Handler, error) {
	// Two globs because templates/*.html does not recurse into cards/. The
	// cards/ directory holds named-template fragments ({{ define "..." }})
	// that the page templates pull in via {{ template "..." . }}.
	tmpl, err := template.ParseFS(webFS,
		"templates/*.html",
		"templates/cards/*.html",
	)
	if err != nil {
		return nil, err
	}

	s := &server{
		tmpl:          tmpl,
		serverinfoSvc: serverinfoSvc,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("GET /api/server", s.handleGetServer)
	mux.HandleFunc("GET /", s.handleIndex)
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

// handleIndex renders the dashboard page with the server-info card populated
// from a fresh serverinfo.Service.Get(). Note that ServeMux's "GET /" pattern
// is a catch-all for any path the mux doesn't otherwise match, so we
// explicitly 404 anything that isn't exactly "/" to avoid leaking the page
// for typo'd URLs.
//
// A serverinfo fetch failure is intentionally NOT a 500: the page itself is
// still useful (links, header, future cards), so we degrade by surfacing the
// error inside the page where the card would have been. The error is also
// logged via slog so an operator sees the actionable cause in journald.
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := pageData{}
	info, err := s.serverinfoSvc.Get(r.Context())
	if err != nil {
		slog.Error("GET /: serverinfo fetch failed", "err", err)
		data.ServerInfoError = err.Error()
	} else {
		data.ServerInfo = info
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		// Headers may already be sent — log and give up. Writing a 500 after
		// a partial body would corrupt the response.
		slog.Error("GET /: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}
