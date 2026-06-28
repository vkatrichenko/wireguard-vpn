// Package server wires the dashboard's HTTP routes onto a stdlib *http.ServeMux.
//
// Slice 1 scope: two handlers (GET /api/health, GET /) and embedded HTML
// templates. Slice 4 adds GET /api/server (server-info JSON snapshot). Slice 5
// adds GET /api/service plus the service-status / uptime cards on the index
// page. Middleware (auth, logging) and additional API endpoints land in later
// slices — keep this file small and dependency-free.
package server

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"wireguard-dashboard/internal/alerts"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/disk"
	"wireguard-dashboard/internal/netdev"
	"wireguard-dashboard/internal/notify"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/processes"
	"wireguard-dashboard/internal/serverinfo"
	"wireguard-dashboard/internal/systemd"
	"wireguard-dashboard/internal/wg"
)

// server holds the shared dependencies routes need at request time. Keeping
// it unexported is deliberate: callers compose with New(...) and treat the
// result as an http.Handler. Handlers hang off the struct (rather than free
// functions) so adding new dependencies in later slices doesn't ripple
// through every handler signature.
type server struct {
	tmpl           *template.Template
	serverinfoSvc  *serverinfo.Service
	systemdSvc     *systemd.Service
	clientsfileSvc *clientsfile.Service
	wgSvc          *wg.Service
	procSvc        *proc.Service
	metricsDB      *db.DB
	geoipSvc       GeoResolver
	diskSvc        *disk.Service
	processesSvc   *processes.Service
	netdevSvc      *netdev.Service
	// alertStatus is the read seam to the in-UI active-alerts view (spec 007,
	// Slice 5). The poller writes it each tick; handlers read a deep copy via
	// Snapshot. It is OPTIONAL: nil renders the disabled/empty view (Snapshot is
	// only ever called when non-nil), so server.New tolerates a nil holder in
	// tests without the handlers panicking.
	alertStatus *alerts.StatusHolder
	// webhookCfg is the runtime-mutable alert webhook holder (spec 008, Slice 2).
	// The /api/webhook status/set/revert endpoints read and mutate it, and
	// alertSnapshot reads Enabled() so the in-UI active-alerts view tracks the
	// LIVE webhook state rather than a boot snapshot. It is OPTIONAL: nil means
	// webhook treated as disabled — the write endpoints respond 503 and the
	// status endpoint reports disabled, never a panic.
	webhookCfg *notify.WebhookConfig
	// webhookNotifier is the server-owned holder-backed sender used ONLY by
	// POST /api/webhook/test (spec 008, Slice 3). It is constructed in New from
	// the SAME webhookCfg the write endpoints mutate, so a test send always
	// targets the currently-effective URL (override or seed). It is nil exactly
	// when webhookCfg is nil — the test endpoint nil-guards on either and
	// responds 503 rather than panicking. It is deliberately a distinct
	// notifier from the poller's: both read the one holder, so re-pointing the
	// URL is observed by delivery and by test sends alike, with no shared sender
	// state to coordinate.
	webhookNotifier notify.Notifier
	// metricsProvider backs GET /metrics, the Prometheus text exposition (spec
	// 012, Slice 4). The poller satisfies it; it is read on the scrape path with
	// no I/O (a deep copy of the last poll's in-memory readings). OPTIONAL: nil
	// makes the handler emit a valid mostly-empty exposition rather than panic, so
	// tests can pass nil.
	metricsProvider MetricsProvider
}

// alertSnapshot returns the current alert view from the holder, or a zeroed
// (disabled, empty) Status when no holder is wired. Centralising the nil check
// here keeps every alert render path — index, Overview partial, /api/alerts —
// from repeating it and guarantees a nil holder never panics.
func (s *server) alertSnapshot() alerts.Status {
	if s.alertStatus == nil {
		st := alerts.Status{Active: []alerts.ActiveAlert{}, Recent: []alerts.LogEntry{}}
		if s.webhookCfg != nil {
			st.Enabled = s.webhookCfg.Enabled()
		}
		return st
	}
	st := s.alertStatus.Snapshot()
	// Override the boot-time enabled flag with the LIVE webhook state (spec 008,
	// Slice 2): an operator can enable/disable delivery at runtime via /api/webhook,
	// and the in-UI active-alerts view must reflect that, not the value captured
	// when the holder was wired. When no webhook holder is threaded (tests), the
	// holder's own enabled flag stands.
	if s.webhookCfg != nil {
		st.Enabled = s.webhookCfg.Enabled()
	}
	return st
}

// pageData is the view-model for the dashboard index template. The *Error
// fields being non-empty means the corresponding fetch failed and the
// template should render an error block in place of the relevant card group.
// Defined here (rather than exported) because it's the contract between
// handleIndex and dashboard.html only — no other package needs to construct
// it.
// clientCountData backs the cards/client-count.html template — "N online / M
// total" summary on the Overview tab. Computed by buildPageData from the same
// ClientRows snapshot that drives the Clients tab so the count and the full
// list can't disagree. Online means ClientRow.Status == "online"; "pending"
// and "unknown" peers are NOT counted as online — only handshake-active rows.
type clientCountData struct {
	Online int
	Total  int
}

type pageData struct {
	ServerInfo         serverinfo.ServerInfo
	ServerInfoError    string
	ServiceStatus      systemd.ServiceStatus
	ServiceStatusError string
	ClientRows         []ClientRow
	ClientsError       string
	// ClientCount backs the Slice 12 client-count summary card on Overview.
	// Computed from ClientRows so both views share one snapshot — the count
	// always matches the full list rendered on the Clients tab. ClientsError
	// gates rendering: a join failure leaves ClientRows nil, and the count
	// card hides behind the same error branch as the list.
	ClientCount clientCountData
	// Stats / StatsError back the system + network-rate cards. Stats is a
	// pointer so the template can branch on `if .Stats` without the receive
	// side having to special-case zero values (uptime=0 is technically a
	// valid sample, however unlikely). StatsError is populated iff the
	// proc.Service.Sample call returned an error.
	Stats      *proc.Stats
	StatsError string
	// Alerts backs the active-alerts strip atop the Overview tab (spec 007,
	// Slice 5). It is a deep copy from the status holder, so it is safe to render
	// from the request goroutine. .Enabled drives the "alerting not configured"
	// hint; .Active drives the firing list / empty state. Recent is unused on
	// Overview (the Events tab renders it) but carried for free in the snapshot.
	Alerts alerts.Status
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
// startup rather than on the first request. The FuncMap below is registered
// before ParseFS because several card templates invoke its helpers and
// html/template binds funcs at parse time.
func New(
	webFS fs.FS,
	serverinfoSvc *serverinfo.Service,
	systemdSvc *systemd.Service,
	clientsfileSvc *clientsfile.Service,
	wgSvc *wg.Service,
	procSvc *proc.Service,
	metricsDB *db.DB,
	geoipSvc GeoResolver,
	diskSvc *disk.Service,
	processesSvc *processes.Service,
	netdevSvc *netdev.Service,
	alertStatus *alerts.StatusHolder,
	webhookCfg *notify.WebhookConfig,
	metricsProvider MetricsProvider,
) (http.Handler, error) {
	// Two globs because templates/*.html does not recurse into cards/. The
	// cards/ directory holds named-template fragments ({{ define "..." }})
	// that the page templates pull in via {{ template "..." . }}.
	tmpl, err := template.New("dashboard").
		Funcs(template.FuncMap{
			"humanUptime":      humanUptime,
			"humanBytes":       humanBytes,
			"humanAgo":         humanAgo,
			"humanBytesPerSec": humanBytesPerSec,
			"humanBytesKB":     humanBytesKB,
			"humanDuration":    humanDuration,
			"threshold":        disk.Threshold,
		}).
		ParseFS(webFS,
			"templates/*.html",
			"templates/cards/*.html",
			"templates/cards/charts/*.html",
			"templates/tabs/*.html",
		)
	if err != nil {
		return nil, err
	}

	s := &server{
		tmpl:            tmpl,
		serverinfoSvc:   serverinfoSvc,
		systemdSvc:      systemdSvc,
		clientsfileSvc:  clientsfileSvc,
		wgSvc:           wgSvc,
		procSvc:         procSvc,
		metricsDB:       metricsDB,
		geoipSvc:        geoipSvc,
		diskSvc:         diskSvc,
		processesSvc:    processesSvc,
		netdevSvc:       netdevSvc,
		alertStatus:     alertStatus,
		webhookCfg:      webhookCfg,
		metricsProvider: metricsProvider,
	}

	// Build the server-owned test-send notifier from the same holder the write
	// endpoints mutate (spec 008, Slice 3). Guarded on webhookCfg being non-nil
	// because NewNotifier requires a non-nil holder; a nil holder leaves
	// webhookNotifier nil and the /api/webhook/test handler responds 503.
	if webhookCfg != nil {
		s.webhookNotifier = notify.NewNotifier(webhookCfg)
	}

	// Static-file route — serves the embedded `web/static/` subtree (Chart.js
	// core, the date-fns adapter, and the dashboard's own charts.js) from the
	// compiled binary. Using fs.Sub + http.FileServerFS (Go 1.22+) keeps the
	// handler dependency-free: no third-party static-file library, and no
	// disk reads at runtime. fs.Sub fails only if the named subtree doesn't
	// exist; the `//go:embed all:web` declaration ensures it always does, so
	// the error path here is theoretically unreachable but worth wrapping so
	// a future refactor that breaks the embed surfaces here, not at first
	// request.
	staticFS, err := fs.Sub(webFS, "static")
	if err != nil {
		return nil, fmt.Errorf("staticFS sub: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("GET /api/server", s.handleGetServer)
	mux.HandleFunc("GET /api/service", s.handleGetService)
	mux.HandleFunc("GET /api/clients", s.handleGetClients)
	mux.HandleFunc("GET /api/clients/{name}/config", s.handleGetClientConfig)
	mux.HandleFunc("GET /api/clients/{name}/history", s.handleGetClientHistory)
	mux.HandleFunc("GET /api/geo", s.handleGetGeo)
	mux.HandleFunc("GET /api/alerts", s.handleGetAlerts)
	mux.HandleFunc("GET /api/webhook", s.handleGetWebhook)
	mux.HandleFunc("POST /api/webhook", s.handleSetWebhook)
	mux.HandleFunc("POST /api/webhook/test", s.handleTestWebhook)
	mux.HandleFunc("POST /api/webhook/revert", s.handleRevertWebhook)
	mux.HandleFunc("GET /api/snapshot", s.handleGetSnapshot)
	mux.HandleFunc("GET /metrics", s.handleGetMetricsProm)
	mux.HandleFunc("GET /api/metrics", s.handleGetMetrics)
	mux.HandleFunc("GET /api/metrics/system", s.handleGetMetricsSystem)
	mux.HandleFunc("GET /api/metrics/traffic", s.handleGetMetricsTraffic)
	mux.HandleFunc("GET /api/metrics/client/{pubkey}", s.handleGetMetricsClient)
	mux.HandleFunc("GET /partial/overview", s.handleGetPartialOverview)
	mux.HandleFunc("GET /partial/clients", s.handleGetPartialClients)
	mux.HandleFunc("GET /partial/clients/{pubkey}/detail", s.handleGetPartialClientDetail)
	mux.HandleFunc("GET /partial/system", s.handleGetPartialSystem)
	mux.HandleFunc("GET /partial/network", s.handleGetPartialNetwork)
	mux.HandleFunc("GET /partial/events", s.handleGetPartialEvents)
	mux.HandleFunc("GET /partial/about", s.handleGetPartialAbout)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))
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

	data := s.buildPageData(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		// Headers may already be sent — log and give up. Writing a 500 after
		// a partial body would corrupt the response.
		slog.Error("GET /: template render failed", "err", err)
		http.Error(w, "template render failed", http.StatusInternalServerError)
		return
	}
}

// buildPageData runs the five sequential fetches that populate the dashboard
// view-model. Extracted from handleIndex so handleGetPartialDashboard can
// reuse the same data-gathering — keeps the two refresh paths in lock-step
// (any new card / new error gate is a single change here, not two).
//
// Each fetch failure becomes a per-card error string rather than a hard
// error: the page (or the partial fragment) is still useful with one card
// degraded, so callers always render whatever the returned pageData holds.
func (s *server) buildPageData(ctx context.Context) pageData {
	data := pageData{}

	info, err := s.serverinfoSvc.Get(ctx)
	if err != nil {
		slog.Error("buildPageData: serverinfo fetch failed", "err", err)
		data.ServerInfoError = err.Error()
	} else {
		data.ServerInfo = info
	}

	status, err := s.systemdSvc.Get(ctx)
	if err != nil {
		slog.Error("buildPageData: systemd fetch failed", "err", err)
		data.ServiceStatusError = err.Error()
	} else {
		data.ServiceStatus = status
	}

	// Manifest + live wg state are fetched as a pair: if either fails the
	// joined view is meaningless, so we surface the error and leave
	// ClientRows nil rather than rendering a half-joined list. Per-card
	// degradation is fine; partial-join inside one card is too confusing.
	clients, clientsErr := s.clientsfileSvc.Load(ctx)
	peers, peersErr := s.wgSvc.Show(ctx)
	if joined := errors.Join(clientsErr, peersErr); joined != nil {
		slog.Error("buildPageData: clients fetch failed", "err", joined)
		data.ClientsError = joined.Error()
	} else {
		data.ClientRows = buildClientRows(clients, peers, time.Now(), s.geoipSvc)
		// ClientCount drives the Slice 12 summary card. Counted inline rather
		// than in a helper because the loop is one line and threading a helper
		// from another file would obscure the "same snapshot" guarantee.
		data.ClientCount.Total = len(data.ClientRows)
		for _, row := range data.ClientRows {
			if row.Status == "online" {
				data.ClientCount.Online++
			}
		}
	}

	// proc.Sample feeds the system + network-rate cards. The sample is
	// inherently a delta against the prior reading held on s.procSvc, so the
	// first render after process start returns CPU%/rates as zero — that's
	// expected and the templates render the cumulative + absolute fields
	// regardless. main.go fires a best-effort warm-sample at startup to
	// reduce the chance of seeing a cold first render.
	stats, statsErr := s.procSvc.Sample(ctx)
	if statsErr != nil {
		slog.Error("buildPageData: proc sample failed", "err", statsErr)
		data.StatsError = statsErr.Error()
	} else {
		data.Stats = &stats
	}

	// Active-alerts strip (Slice 5). Read the poller-written snapshot — no fetch,
	// no I/O, no error path: alertSnapshot returns a zeroed disabled view when no
	// holder is wired (tests) so the strip always renders.
	data.Alerts = s.alertSnapshot()

	return data
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

// humanBytes formats a byte count as B / KB / MB / GB with one decimal.
// Uses 1024-based units (KiB-style numbers, KB labels — matches what
// most VPN UIs do). Returns "0 B" on negative or zero.
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n) / 1024
	for _, u := range units {
		if f < 1024 {
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1f PB", f)
}

// humanBytesPerSec formats a bytes-per-second rate using 1024-base units —
// matches humanBytes' base for visual consistency. Returns "0 B/s" on zero.
func humanBytesPerSec(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B/s", n)
	}
	units := []string{"KB/s", "MB/s", "GB/s"}
	f := float64(n) / 1024
	for _, u := range units {
		if f < 1024 {
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1f TB/s", f)
}

// humanBytesKB formats a kilobyte count. Multiplies by 1024 then delegates
// to humanBytes — keeps a single source of truth for the byte-sizing logic.
func humanBytesKB(n int64) string {
	return humanBytes(n * 1024)
}

// humanDuration formats a Duration the same shape as humanUptime — but takes
// a Duration rather than a time.Time. Used for proc.Stats.HostUptime which
// is already a Duration.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
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

// humanAgo formats a time as "Ns ago" / "Nm ago" / "Nh ago" / "Nd ago"
// relative to time.Now(). For a future time, returns "just now". For a
// zero time, returns "never" — though templates should guard with
// .IsZero() before calling this.
func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	if d < 0 {
		return "just now"
	}
	seconds := int(d.Seconds())
	minutes := seconds / 60
	hours := minutes / 60
	days := hours / 24
	switch {
	case days > 0:
		return fmt.Sprintf("%dd ago", days)
	case hours > 0:
		return fmt.Sprintf("%dh ago", hours)
	case minutes > 0:
		return fmt.Sprintf("%dm ago", minutes)
	default:
		return fmt.Sprintf("%ds ago", seconds)
	}
}
