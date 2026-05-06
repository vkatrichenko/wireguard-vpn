// Package server wires the dashboard's HTTP routes onto a stdlib *http.ServeMux.
//
// Slice 1 scope: two handlers (GET /api/health, GET /) and embedded HTML
// templates. Slice 4 adds GET /api/server (server-info JSON snapshot). Slice 5
// adds GET /api/service plus the service-status / uptime cards on the index
// page. Middleware (auth, logging) and additional API endpoints land in later
// slices — keep this file small and dependency-free.
package server

import (
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"wireguard-dashboard/internal/serverinfo"
	"wireguard-dashboard/internal/systemd"
)

// server holds the shared dependencies routes need at request time. Keeping
// it unexported is deliberate: callers compose with New(...) and treat the
// result as an http.Handler. Handlers hang off the struct (rather than free
// functions) so adding new dependencies in later slices doesn't ripple
// through every handler signature.
type server struct {
	tmpl          *template.Template
	serverinfoSvc *serverinfo.Service
	systemdSvc    *systemd.Service
}

// pageData is the view-model for the dashboard index template. The *Error
// fields being non-empty means the corresponding fetch failed and the
// template should render an error block in place of the relevant card group.
// Defined here (rather than exported) because it's the contract between
// handleIndex and dashboard.html only — no other package needs to construct
// it.
type pageData struct {
	ServerInfo         serverinfo.ServerInfo
	ServerInfoError    string
	ServiceStatus      systemd.ServiceStatus
	ServiceStatusError string
}

// New returns an http.Handler with the wired routes. The caller passes in
// an fs.FS rooted at the project's `web/` directory (see the embed declaration
// in dashboard/embed.go); decoupling the FS from the package keeps
// `internal/server` testable with an in-memory fstest.MapFS in later slices,
// and sidesteps the fact that //go:embed paths cannot climb out of their
// containing directory with `..`.
//
// The serverinfo.Service is passed in (rather than constructed here) so tests
// can inject a Service{} literal with fake IMDS / Runner fields. The
// systemd.Service is similarly injected so tests can construct a fake
// Runner without shelling out. New service deps are appended at the end of
// the parameter list so existing call sites only need to append, never
// re-order.
//
// Templates are parsed eagerly so a malformed template fails fast on
// startup rather than on the first request. The `humanUptime` FuncMap is
// registered before ParseFS because templates/cards/uptime.html invokes it
// and html/template binds funcs at parse time.
func New(webFS fs.FS, serverinfoSvc *serverinfo.Service, systemdSvc *systemd.Service) (http.Handler, error) {
	// Two globs because templates/*.html does not recurse into cards/. The
	// cards/ directory holds named-template fragments ({{ define "..." }})
	// that the page templates pull in via {{ template "..." . }}.
	tmpl, err := template.New("dashboard").
		Funcs(template.FuncMap{"humanUptime": humanUptime}).
		ParseFS(webFS,
			"templates/*.html",
			"templates/cards/*.html",
		)
	if err != nil {
		return nil, err
	}

	s := &server{
		tmpl:          tmpl,
		serverinfoSvc: serverinfoSvc,
		systemdSvc:    systemdSvc,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("GET /api/server", s.handleGetServer)
	mux.HandleFunc("GET /api/service", s.handleGetService)
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

// handleIndex renders the dashboard page with the server-info, service-status
// and uptime cards populated from fresh service fetches. Note that ServeMux's
// "GET /" pattern is a catch-all for any path the mux doesn't otherwise
// match, so we explicitly 404 anything that isn't exactly "/" to avoid
// leaking the page for typo'd URLs.
//
// A serverinfo or systemd fetch failure is intentionally NOT a 500: the page
// itself is still useful (header, the OTHER card group), so we degrade by
// surfacing each error inside the page where its card group would have been.
// Both fetches run sequentially — they're cheap (single-digit ms each) and
// serial keeps slog output ordered. Errors are also logged via slog so an
// operator sees the actionable cause in journald.
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

	status, err := s.systemdSvc.Get(r.Context())
	if err != nil {
		slog.Error("GET /: systemd fetch failed", "err", err)
		data.ServiceStatusError = err.Error()
	} else {
		data.ServiceStatus = status
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

// humanUptime formats time.Since(t) as a short human-readable duration like
// "3d 14h", "7h 22m", "42s". A zero input renders as "-" so the template
// doesn't have to special-case the never-started edge before we know whether
// the unit is active. Negative durations (clock skew between the systemctl
// host and the dashboard's notion of "now") are clamped to zero by the
// switch's default branch, which formats them as "0s" rather than leaking
// negative numbers into the UI.
func humanUptime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}
