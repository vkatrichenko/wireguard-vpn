package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
	"wireguard-dashboard/internal/systemd"
	"wireguard-dashboard/internal/wg"
)

// newTestDB returns an in-memory *db.DB scoped to t — opened once, closed
// in t.Cleanup. Used to satisfy server.New(...)'s metricsDB parameter in
// existing tests that don't exercise /api/metrics.
func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	testDB, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = testDB.Close() })
	return testDB
}

// fakeClientsfileSvc returns an empty manifest. The existing index-page tests
// don't exercise the client-list path, so an empty list is sufficient to keep
// server.New() satisfied at construction time.
func fakeClientsfileSvc() *clientsfile.Service {
	return &clientsfile.Service{
		Reader: func(string) ([]byte, error) { return []byte("[]"), nil },
		Path:   "/test/clients.json",
	}
}

// fakeWgSvc returns canned `wg show wg0 dump` output containing only the
// server's own info line (no peers). Same reasoning as fakeClientsfileSvc:
// keeps server.New() happy without driving the client-list join.
func fakeWgSvc() *wg.Service {
	return &wg.Service{
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("server-key\tserver-pub\t51820\toff\n"), nil
		},
		Iface: "wg0",
	}
}

// fakeProcSvc returns a *proc.Service with an in-memory Reader that serves
// canned /proc/stat, /proc/meminfo, /proc/uptime and /sys/class/net/wg0
// rx/tx byte counters. The numeric values are arbitrary — the existing
// tests only need Sample() to succeed so server.New() is satisfied; they
// don't assert on the rendered Stats (the system + network-rate templates
// land in Slice 8 sub-task 3).
//
// ProcPath / SysPath are kept as the real defaults so the canned paths line
// up with what proc.Service.readCPU / readMem / readUptime / readIfaceCounter
// will request.
func fakeProcSvc() *proc.Service {
	files := map[string][]byte{
		"/proc/stat":                              []byte("cpu  100 0 50 800 10 0 0 0 0 0\n"),
		"/proc/meminfo":                           []byte("MemTotal:    8000000 kB\nMemAvailable:  4000000 kB\n"),
		"/proc/uptime":                            []byte("12345.67 9876.54\n"),
		"/sys/class/net/wg0/statistics/rx_bytes":  []byte("1024\n"),
		"/sys/class/net/wg0/statistics/tx_bytes":  []byte("2048\n"),
	}
	return &proc.Service{
		Reader: func(path string) ([]byte, error) {
			if data, ok := files[path]; ok {
				return data, nil
			}
			return nil, fmt.Errorf("fake reader: unexpected path %q", path)
		},
		Now:      time.Now,
		ProcPath: "/proc",
		SysPath:  "/sys",
		Iface:    "wg0",
	}
}

// systemdRunnerActive returns canned bytes representing a running unit. The
// `is-active` call returns "active\n"; the `show -p ActiveEnterTimestamp`
// call returns a fixed timestamp two hours in the past so humanUptime
// renders a deterministic "2h 0m" (or "1h 59m"/"2h 0m" near the boundary —
// the test only checks the prefix "h " or "m " is present so it is robust
// across boundary jitter).
//
// Building the timestamp string here, rather than at the call site, keeps the
// fake one-liner-friendly in each test that needs it.
func systemdRunnerActive(at time.Time) systemd.Service {
	return systemd.Service{
		Unit: "wg-quick@wg0.service",
		Runner: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "sudo" || len(args) < 2 || args[0] != "/usr/bin/systemctl" {
				return nil, fmt.Errorf("unexpected runner call: name=%q args=%v", name, args)
			}
			switch args[1] {
			case "is-active":
				return []byte("active\n"), nil
			case "show":
				// systemd's default ActiveEnterTimestamp format.
				return []byte("ActiveEnterTimestamp=" + at.Format("Mon 2006-01-02 15:04:05 MST") + "\n"), nil
			}
			return nil, fmt.Errorf("unexpected systemctl verb: %v", args)
		},
	}
}

// fakeSystemdRunnerErr returns a non-ExitError failure for any systemctl
// call. Used by the partial-degradation test to simulate the production case
// where sudo is missing or the binary path is wrong.
func fakeSystemdRunnerErr(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return nil, io.ErrUnexpectedEOF
}

// fakeIMDS is a stub imdsClient that returns a canned public IP. It exists
// only to drive the success-path render test; the production path uses the
// real httpIMDS hitting 169.254.169.254.
type fakeIMDS struct {
	ip  string
	err error
}

func (f fakeIMDS) PublicIP(_ context.Context) (string, error) {
	return f.ip, f.err
}

// TestHandleIndex_Success proves the dashboard.html template renders the
// server-info, service-status and uptime cards with concrete values when
// both serverinfo.Service.Get and systemd.Service.Get return successfully.
// We inject fake IMDS / Runner closures directly into Service literals;
// each Service's Runner field is exported precisely for this kind of test
// wiring.
func TestHandleIndex_Success(t *testing.T) {
	const (
		fakeIP  = "203.0.113.1"
		fakeKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
	)

	// Two hours and three minutes ago — comfortably inside the "Xh Ym"
	// branch of humanUptime regardless of test execution timing.
	enteredAt := time.Now().Add(-(2*time.Hour + 3*time.Minute))

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: fakeIP},
		Runner: func(_ context.Context, name string, args ...string) ([]byte, error) {
			// Sanity-check the call shape so a future refactor that swaps the
			// command surfaces here, not in production.
			if name != "sudo" || len(args) < 1 || args[0] != "/usr/bin/wg" {
				t.Errorf("unexpected runner call: name=%q args=%v", name, args)
			}
			return []byte(fakeKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(enteredAt)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"<h1>WireGuard Dashboard</h1>",
		// server-info card.
		`id="server-info"`,
		"Public IP",
		fakeIP,
		"51820",
		fakeKey,
		`class="copy-btn"`,
		// service-status card.
		`id="service-status"`,
		`state-active`,
		"Active since",
		// uptime card — `humanUptime` of ~2h ago should render "2h Ym".
		`id="uptime"`,
		`class="uptime"`,
		"h ",
		// client-list card — fakes return an empty manifest and a
		// server-only `wg show`, so the card renders the empty-state
		// branch rather than a populated table.
		`id="client-list"`,
		"No clients configured",
		// system card — fakeProcSvc returns first-sample stats:
		// CPUPercent is 0 (no prior sample to delta against), memory
		// is MemTotal=8000000 kB / MemAvailable=4000000 kB so used = 50%.
		`id="system"`,
		"0.0%",
		"50.0%",
		// network-rate card — first-sample rates render as "0 B/s".
		`id="network-rate"`,
		"0 B/s",
		// Trend-chart partials — Slice 9 sub-task 5 wires the four
		// chart cards into the page and references the embedded
		// Chart.js bundle from /static/.
		`id="chart-cpu"`,
		`id="chart-memory"`,
		`id="chart-rx"`,
		`id="chart-tx"`,
		`/static/chart.umd.min.js`,
		`/static/charts.js`,
		// htmx wiring — sub-task 2 of Slice 11. The page polls
		// /partial/dashboard every 10s and swaps the data-card block.
		`<main id="tab-body"`,
		`class="tab-bar"`,
		`/static/htmx.min.js`,
		// Stale-data indicator — sub-task 3 of Slice 11. The pill lives
		// outside #dashboard-content so it survives htmx innerHTML swaps,
		// and the IIFE listener is loaded after htmx itself.
		`id="stale-pill"`,
		`/static/htmx-stale.js`,
		// events card — the test seeds no handshake events, so the
		// card renders the empty-state branch (newest-first reverse
		// is a no-op on an empty slice).
		`id="events"`,
		"No recent handshakes.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
	// Error card MUST NOT render in the success path.
	if strings.Contains(body, `class="card error"`) {
		t.Errorf("body unexpectedly contains error card:\n%s", body)
	}
	// "Service down" is the not-active fallback — must NOT render here.
	if strings.Contains(body, "Service down") {
		t.Errorf("body unexpectedly contains 'Service down':\n%s", body)
	}
}

// TestHandleIndex_BothErrors proves that when BOTH serverinfo.Service.Get and
// systemd.Service.Get fail, the page still renders 200 OK with TWO error
// cards in place of the respective card groups. Partial degradation is the
// documented contract — a hard 500 would deny the operator the rest of the
// page.
func TestHandleIndex_BothErrors(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{err: io.ErrUnexpectedEOF},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, io.ErrUnexpectedEOF
		},
	}
	systemdSvc := &systemd.Service{
		Unit:   "wg-quick@wg0.service",
		Runner: fakeSystemdRunnerErr,
	}

	handler, err := server.New(dashboard.WebFS(), infoSvc, systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (partial degradation)", rec.Code)
	}

	body := rec.Body.String()
	// Both error cards must render.
	if got := strings.Count(body, `class="card error"`); got != 2 {
		t.Errorf("error card count = %d, want 2\n--- body ---\n%s", got, body)
	}
	// Neither set of populated cards must render when their fetch failed.
	for _, unwanted := range []string{
		`id="server-info"`,
		`id="service-status"`,
		`id="uptime"`,
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body unexpectedly contains %q on error path:\n%s", unwanted, body)
		}
	}
}

// TestHandleGetService_IncludesEvents proves GET /api/service returns the
// new {status, events} envelope with last-hour handshake events folded in
// alongside the systemd snapshot. We seed one event inside the window via
// db.InsertHandshakeEvents and assert the JSON envelope contains both the
// status fields AND the single event row.
func TestHandleGetService_IncludesEvents(t *testing.T) {
	enteredAt := time.Now().Add(-(2*time.Hour + 3*time.Minute))
	systemdSvc := systemdRunnerActive(enteredAt)

	testDB := newTestDB(t)
	if err := testDB.InsertHandshakeEvents(context.Background(), []db.HandshakeEvent{
		{TS: time.Now().Add(-5 * time.Minute), PublicKey: "peer-pub", Name: "alice"},
	}); err != nil {
		t.Fatalf("seed handshake events: %v", err)
	}

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), testDB)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/service", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got struct {
		Status systemd.ServiceStatus `json:"status"`
		Events []db.HandshakeEvent   `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if !got.Status.Active {
		t.Errorf("status.active = false, want true; body=%s", rec.Body.String())
	}
	if got.Status.State != "active" {
		t.Errorf("status.state = %q, want %q", got.Status.State, "active")
	}
	if len(got.Events) != 1 {
		t.Fatalf("len(events) = %d, want 1; body=%s", len(got.Events), rec.Body.String())
	}
	if got.Events[0].PublicKey != "peer-pub" || got.Events[0].Name != "alice" {
		t.Errorf("events[0] = %+v, want PublicKey=peer-pub Name=alice", got.Events[0])
	}
}

// TestHandleGetPartialDashboard_RendersFragment proves the htmx polling
// endpoint at GET /partial/dashboard returns just the inner card block —
// no <html>/<head>/<body> wrapper — so htmx's innerHTML swap drops it
// cleanly into <main id="dashboard-content"> on the page.
func TestHandleGetPartialDashboard_RendersFragment(t *testing.T) {
	const (
		fakeIP  = "203.0.113.1"
		fakeKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
	)

	enteredAt := time.Now().Add(-(2*time.Hour + 3*time.Minute))

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: fakeIP},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(fakeKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(enteredAt)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/dashboard", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "text/html; charset=utf-8")
	}

	body := rec.Body.String()
	if !strings.Contains(body, `id="server-info"`) {
		t.Errorf("partial body missing server-info card:\n%s", body)
	}
	// Fragment must NOT contain a full document — that would break htmx
	// innerHTML swapping by injecting nested <html>/<body> tags.
	if strings.Contains(body, "<html") {
		t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
	}
}

// TestHandleGetPartialTabs proves each of the six tab partial routes registered
// in Slice 1 sub-task 5 (overview, clients, system, network, events, about)
// returns a 200 HTML fragment with the expected sentinel string and without a
// full-document wrapper. The handler is constructed once and reused across
// subtests — same fake wiring as TestHandleGetPartialDashboard_RendersFragment
// because the overview path drives the full buildPageData call graph, while
// the five placeholder tabs are template-only.
func TestHandleGetPartialTabs(t *testing.T) {
	const (
		fakeIP  = "203.0.113.1"
		fakeKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
	)

	enteredAt := time.Now().Add(-(2*time.Hour + 3*time.Minute))

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: fakeIP},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(fakeKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(enteredAt)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t))
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	tests := []struct {
		name             string
		path             string
		wantBodyContains string
	}{
		{"overview", "/partial/overview", `id="server-info"`},
		{"clients", "/partial/clients", "Coming soon"},
		{"system", "/partial/system", "Coming soon"},
		{"network", "/partial/network", "Coming soon"},
		{"events", "/partial/events", "Coming soon"},
		{"about", "/partial/about", "Coming soon"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q, want %q", got, "text/html; charset=utf-8")
			}

			body := rec.Body.String()
			if !strings.Contains(body, tc.wantBodyContains) {
				t.Errorf("body missing %q:\n%s", tc.wantBodyContains, body)
			}
			// Fragments must not include a full HTML document — htmx
			// innerHTML swaps would otherwise inject nested <html>/<body>.
			if strings.Contains(body, "<html") {
				t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
			}
		})
	}
}
