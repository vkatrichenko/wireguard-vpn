package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/disk"
	"wireguard-dashboard/internal/geoip"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
	"wireguard-dashboard/internal/systemd"
	"wireguard-dashboard/internal/wg"

	"golang.org/x/sys/unix"
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

// seededClientsfileSvc returns a clientsfile.Service whose Reader emits a
// one-entry manifest matching the peer line that seededWgSvc returns. Kept
// alongside fakeClientsfileSvc rather than parameterising the original so each
// test's wiring stays readable at the call site.
func seededClientsfileSvc(name, address, publicKey string) *clientsfile.Service {
	manifest := fmt.Sprintf(`[{"name":%q,"address":%q,"public_key":%q}]`, name, address, publicKey)
	return &clientsfile.Service{
		Reader: func(string) ([]byte, error) { return []byte(manifest), nil },
		Path:   "/test/clients.json",
	}
}

// seededWgSvc returns a *wg.Service whose Runner emits the server-info line
// followed by a single peer line. handshakeAgo positions the peer's latest
// handshake relative to time.Now() so the test can drive online vs offline
// status deterministically — pass a value under onlineThreshold (3m) to land
// on "online". Endpoint / allowedIPs / rx / tx are caller-supplied so the
// same helper covers both the geo-resolution and unresolvable-IP cases.
func seededWgSvc(publicKey, endpoint, allowedIPs string, handshakeAgo time.Duration, rx, tx int64) *wg.Service {
	handshake := time.Now().Add(-handshakeAgo).Unix()
	peerLine := fmt.Sprintf("%s\t(none)\t%s\t%s\t%d\t%d\t%d\toff\n",
		publicKey, endpoint, allowedIPs, handshake, rx, tx)
	return &wg.Service{
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("server-key\tserver-pub\t51820\toff\n" + peerLine), nil
		},
		Iface: "wg0",
	}
}

// staticGeoResolver implements server.GeoResolver with a constant return for
// every Lookup. Used by the seeded-row partial-clients test to assert that
// the geo cell renders the resolver's output — the resolver's real filtering
// behaviour (RFC1918, mmdb misses) is covered separately in clientrows_test.go.
type staticGeoResolver struct {
	country string
	city    string
}

func (s staticGeoResolver) Lookup(_ net.IP) (string, string) { return s.country, s.city }

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

// fakeDiskSvc returns a *disk.Service with an in-memory Reader that emits a
// single-row /proc/mounts payload plus a fake Statfs that reports a 4 GiB ext4
// volume at 50% full. Numeric values are arbitrary — the existing tests only
// need Sample() to succeed so server.New() is satisfied and the system-tab
// fragment renders with non-error branches.
//
// Bsize is wrapped through bsizeField to bridge the int64 (Linux) vs uint32
// (Darwin) field type on unix.Statfs_t — the helper lives in
// server_bsize_{linux,darwin}_test.go and mirrors the pattern from the disk
// package's own test helpers.
func fakeDiskSvc() *disk.Service {
	return &disk.Service{
		Reader: func(string) ([]byte, error) {
			return []byte("/dev/sda1 / ext4 rw 0 0\n"), nil
		},
		Statfs: func(_ string, stat *unix.Statfs_t) error {
			stat.Bsize = bsizeField(4096)
			stat.Blocks = 1_000_000
			stat.Bavail = 500_000
			return nil
		},
		MountsPath: "/proc/mounts",
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

	// nil geo resolver is allowed — geo is advisory.
	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc())
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
		// Trend-chart partials — Slice 9 sub-task 5 wires four chart cards
		// into the page; Slice 6 sub-task 4 moves chart-cpu / chart-memory
		// into the System tab body, so the index page renders only the
		// rx/tx charts in the global grid. cpu/memory still render via the
		// System tab fragment when the operator switches tabs.
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

	// nil geo resolver is allowed — geo is advisory.
	handler, err := server.New(dashboard.WebFS(), infoSvc, systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc())
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

	// nil geo resolver is allowed — geo is advisory.
	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc())
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

	// nil geo resolver is allowed — geo is advisory.
	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc())
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

	// nil geo resolver is allowed — geo is advisory.
	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	tests := []struct {
		name             string
		path             string
		wantBodyContains []string
	}{
		{"overview", "/partial/overview", []string{`id="server-info"`}},
		// sub-task 7 expands this with a seeded-row assertion; for now the
		// fake clientsfile + fake wg both return empty so the template renders
		// the empty-state branch.
		{"clients", "/partial/clients", []string{"No clients configured"}},
		// Slice 6 sub-task 4 promotes the System tab from placeholder: the
		// fragment now embeds both the cards/system.html CPU/mem numerics
		// card (id="system") and the cards/disk.html mount table
		// (id="disk"). fakeDiskSvc seeds one /-mounted ext4 row at 50% full,
		// so the populated table branch renders rather than the empty-state.
		{"system", "/partial/system", []string{`id="system"`, `id="disk"`}},
		{"network", "/partial/network", []string{"Coming soon"}},
		{"events", "/partial/events", []string{"Coming soon"}},
		{"about", "/partial/about", []string{"Coming soon"}},
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
			for _, want := range tc.wantBodyContains {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q:\n%s", want, body)
				}
			}
			// Fragments must not include a full HTML document — htmx
			// innerHTML swaps would otherwise inject nested <html>/<body>.
			if strings.Contains(body, "<html") {
				t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
			}
		})
	}
}

// TestHandleGetPartialSystem_RendersDiskCard pins the disk card surface of the
// System tab fragment: the literal <h2>Disk</h2> heading, the table layout
// from cards/disk.html (Mount/Filesystem/Used/Full columns), and the
// progress-bar markup that wraps the per-mount fill div + label. The fake
// statfs reports Bsize=4096, Blocks=1_000_000, Bavail=150_000 → Total≈4 GB,
// Avail≈600 MB, Used≈3.4 GB, PctFull = 85.0 — squarely inside the amber band
// (≥80% and <95%), so `disk.Threshold` returns "amber" and the template
// emits `class="progress-bar-fill threshold-amber"`. This proves the
// `threshold` template func wired up in sub-task 4 picks up the disk
// package's bucket function for an 85%-full mount end-to-end. We assert on
// the `style="width: 85` prefix because the rendered value is `85.0%` after
// one-decimal rounding, and a tighter match would break if the rounding ever
// shifts. The threshold-red branch is exercised in the disk-package unit
// tests; here amber alone is enough to prove the wiring.
func TestHandleGetPartialSystem_RendersDiskCard(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	// Per-test disk fake: same canned single-row /proc/mounts as fakeDiskSvc,
	// but Bavail=150_000 puts the mount at 85.0% full (amber). Kept inline
	// so the per-test wiring stays one-stop and the global fakeDiskSvc keeps
	// driving the existing 50%-full / threshold-ok tests unchanged.
	amberDiskSvc := &disk.Service{
		Reader: func(string) ([]byte, error) {
			return []byte("/dev/sda1 / ext4 rw 0 0\n"), nil
		},
		Statfs: func(_ string, stat *unix.Statfs_t) error {
			stat.Bsize = bsizeField(4096)
			stat.Blocks = 1_000_000
			stat.Bavail = 150_000
			return nil
		},
		MountsPath: "/proc/mounts",
	}

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, amberDiskSvc)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/system", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "text/html; charset=utf-8")
	}

	body := rec.Body.String()
	// Fragment invariant — htmx innerHTML swaps would otherwise nest <html>.
	if strings.Contains(body, "<html") {
		t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
	}

	for _, want := range []string{
		// System (large-numerics) card stays — sub-task 4 embeds it alongside
		// the disk card in the same tab body.
		`id="system"`,
		// Chart cards moved into the System tab body in sub-task 4.
		`id="chart-cpu"`,
		`id="chart-memory"`,
		// Literal disk heading from cards/disk.html.
		`<h2>Disk</h2>`,
		// Table layout — `<th>Full</th>` proves we're on the populated
		// branch, not the empty-state <p class="empty">…</p> wrapper.
		`<th>Full</th>`,
		// Progress-bar wrapper + amber-threshold fill class + label markup.
		// The threshold-amber class is the load-bearing assertion — proves
		// the `threshold` template func wired up in sub-task 4 maps
		// PctFull=85.0 to "amber" via disk.Threshold.
		`class="progress-bar"`,
		`class="progress-bar-fill threshold-amber"`,
		`class="progress-bar-label"`,
		// Inline width style — rendered as `style="width: 85.0%"` after
		// one-decimal rounding. Asserting the prefix keeps the test tolerant
		// to rounding-format tweaks while still pinning the numeric value.
		`style="width: 85`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestHandleGetPartialClients_SeededRow proves the populated branch of the
// clients template: when the clientsfile manifest joins cleanly with a peer
// returned by `wg show wg0 dump`, the fragment renders the table header
// (including the geo column added in Slice 4 sub-task 6) plus one row
// containing the seeded name, WG IP, and resolved geo cell.
//
// The geo resolver is a constant-return fake — the real geoip.Service is
// covered by its own tests; here we only need to prove the wiring runs the
// resolver and the template emits "City, Country" as the cell text.
func TestHandleGetPartialClients_SeededRow(t *testing.T) {
	const (
		fakeIP      = "203.0.113.1"
		fakeKey     = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
		peerName    = "alice"
		peerAddress = "172.16.15.5/32"
		// 44-char base64 — synthetic peer key, distinct from the server pub.
		peerPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		peerEndpoint  = "198.51.100.42:51820"
	)

	enteredAt := time.Now().Add(-(2*time.Hour + 3*time.Minute))

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: fakeIP},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(fakeKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(enteredAt)

	clientsSvc := seededClientsfileSvc(peerName, peerAddress, peerPublicKey)
	// 10s ago — well within onlineThreshold (3m) so status renders "online".
	wgSvc := seededWgSvc(peerPublicKey, peerEndpoint, peerAddress, 10*time.Second, 123456, 654321)

	geo := staticGeoResolver{country: "US", city: "San Francisco"}

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, wgSvc, fakeProcSvc(), newTestDB(t), geo, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/clients", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "text/html; charset=utf-8")
	}

	body := rec.Body.String()
	for _, want := range []string{
		// Geo column header was the Slice 4 sub-task 6 addition; its
		// presence proves we're rendering the populated table, not the
		// pre-Slice-4 layout.
		"<th>Geo</th>",
		peerName,
		peerAddress,
		// Template renders "City, Country" when both fields are non-empty.
		"San Francisco, US",
		// Sub-task 5 row-expand wiring — htmx attrs on the data row plus
		// the empty placeholder <tr> below it. We pin the URL/target/swap
		// prefixes without committing to the exact pubkey encoding so a
		// future urlquery rewrite doesn't break the test.
		`hx-get="/partial/clients/`,
		`hx-target="[id='detail-`,
		`hx-swap="innerHTML"`,
		`class="detail-row hidden"`,
		`id="detail-`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}

	// Must be on the populated branch — the empty-state copy would mean
	// the join silently dropped the row.
	if strings.Contains(body, "No clients configured") {
		t.Errorf("body unexpectedly contains empty-state copy:\n%s", body)
	}
	// Fragments must not include a full HTML document — same invariant as
	// the other partial tests.
	if strings.Contains(body, "<html") {
		t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
	}
}

// TestHandleGetPartialClients_SeededRow_PubkeyWithSpecialChars locks in the
// fix for the row-expand crash on pubkeys containing base64's `+`, `/`, or `=`
// characters. The original `hx-target="#detail-{{ .PublicKey }}"` form fed the
// raw pubkey to document.querySelector as a CSS id selector, which rejects
// those characters and throws SyntaxError. The fix switches to an attribute
// selector — `hx-target="[id='detail-...']"` — which tolerates any non-quote
// content. This test pins the new shape end-to-end:
//
//   - html/template renders `+` as `&#43;` inside both the id and hx-target
//     attribute values; the browser decodes the entity back to `+` at HTML
//     parse time so getElementById and the attribute-selector match work on
//     the runtime string. We assert on the escaped form because that's what
//     is on the wire;
//   - `/` and `=` pass through verbatim — htmx receives `[id='detail-.../...=']`
//     at runtime, which CSS attribute selectors accept;
//   - the broken `hx-target="#detail-` form MUST be absent — this is the
//     regression guard;
//   - the hx-get URL still encodes `+` as `%2B`, `/` as `%2F`, `=` as `%3D`
//     via the existing `urlquery` pipeline.
func TestHandleGetPartialClients_SeededRow_PubkeyWithSpecialChars(t *testing.T) {
	const (
		fakeIP      = "203.0.113.1"
		fakeKey     = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
		peerName    = "carol"
		peerAddress = "172.16.15.7/32"
		// Realistic 44-char base64 pubkey containing ALL three special
		// chars that break the CSS id-selector form: `+`, `/`, and `=`
		// (trailing pad). Locks the fix against any single-char regression.
		peerPublicKey = "q3cv/s6T9o7E0+A6R6A4IJF9kL6D+SJX/FmgD9aAMWY="
		peerEndpoint  = "198.51.100.42:51820"
	)

	enteredAt := time.Now().Add(-(2*time.Hour + 3*time.Minute))

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: fakeIP},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(fakeKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(enteredAt)

	clientsSvc := seededClientsfileSvc(peerName, peerAddress, peerPublicKey)
	wgSvc := seededWgSvc(peerPublicKey, peerEndpoint, peerAddress, 10*time.Second, 1, 2)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, wgSvc, fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/clients", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()

	// html/template HTML-escapes `+` to `&#43;` inside attribute values.
	// The browser decodes the entity at parse time, so the runtime DOM id
	// equals the raw pubkey and getElementById round-trips cleanly. We
	// assert on the wire form (what the server emits) — `/` and `=` are
	// passed through verbatim, only `+` carries the entity escape.
	wantIDOnWire := `id="detail-` + strings.ReplaceAll(peerPublicKey, "+", "&#43;") + `"`
	if !strings.Contains(body, wantIDOnWire) {
		t.Errorf("body missing %q (wire form of id attribute):\n%s", wantIDOnWire, body)
	}

	// hx-target MUST be on the attribute-selector form, not the broken
	// `#detail-...` id-selector form. The single-quote passes through
	// unescaped inside a `"..."` attribute (html/template only escapes the
	// outer quote char), so the wire form carries a literal `'`.
	if !strings.Contains(body, `hx-target="[id='detail-`) {
		t.Errorf("body missing attribute-selector hx-target prefix:\n%s", body)
	}
	if strings.Contains(body, `hx-target="#detail-`) {
		t.Errorf("body unexpectedly contains broken id-selector hx-target form:\n%s", body)
	}

	// hx-get URL MUST contain the URL-encoded form of `+`, `/`, and `=`
	// so the PathValue("pubkey") decode round-trips to the manifest key.
	// This proves the existing `urlquery` escape on the path side is
	// untouched by the hx-target fix.
	for _, want := range []string{"%2B", "%2F", "%3D"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q (URL-encoded special char) in hx-get:\n%s", want, body)
		}
	}
}

// TestHandleGetPartialClients_RFC1918Endpoint exercises the negative geo
// branch end-to-end against the REAL *geoip.Service. The seeded peer's
// endpoint sits in 10.0.0.0/8 — geoip.Service.Lookup's stdlib guards
// (ip.IsPrivate) short-circuit before the mmdb is consulted, returning
// ("", ""). The template's `{{ if .Country }}…{{ else }}—{{ end }}` branch
// then renders the em-dash, proving the production resolver's RFC1918 path
// reaches the UI as the spec requires.
//
// The exact em-dash searched for is the U+2014 byte sequence (0xE2 0x80
// 0x94) literally embedded in web/templates/tabs/clients.html line 30.
func TestHandleGetPartialClients_RFC1918Endpoint(t *testing.T) {
	const (
		fakeIP      = "203.0.113.1"
		fakeKey     = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
		peerName    = "bob"
		peerAddress = "172.16.15.6/32"
		// 44-char base64 — distinct from the SeededRow test's peer key so
		// the two tests can't share state via package-level caches.
		peerPublicKey = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
		peerEndpoint  = "10.0.0.5:51820"
		// emDash is the literal U+2014 in clients.html line 30. The Geo
		// cell renders to exactly "<td>—</td>" when both Country and City
		// are empty (the `with .Geo` / `if .Country` / else branch).
		emDash = "<td>\u2014</td>"
	)

	enteredAt := time.Now().Add(-(2*time.Hour + 3*time.Minute))

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: fakeIP},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(fakeKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(enteredAt)

	clientsSvc := seededClientsfileSvc(peerName, peerAddress, peerPublicKey)
	// 10s ago — well within onlineThreshold so status renders "online";
	// exercises the populated-row branch rather than "pending"/"offline".
	wgSvc := seededWgSvc(peerPublicKey, peerEndpoint, peerAddress, 10*time.Second, 555, 777)

	// Real *geoip.Service — embedded mmdb is decoded from embed.FS, no
	// filesystem path resolution involved. RFC1918 short-circuit in
	// Service.Lookup is what we're proving reaches the template.
	geo, err := geoip.New()
	if err != nil {
		t.Fatalf("geoip.New: %v", err)
	}
	t.Cleanup(func() { _ = geo.Close() })

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, wgSvc, fakeProcSvc(), newTestDB(t), geo, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/clients", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range []string{
		peerName,
		peerAddress,
		"<th>Geo</th>",
		// The geo cell for an RFC1918 endpoint MUST be exactly "<td>—</td>".
		// Searching the wrapped form (rather than a bare em-dash) avoids
		// matching the WG-IP cell, which falls back to the same glyph when
		// Address is empty — see clients.html line 24.
		emDash,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}

	// Populated-branch invariant — empty-state copy would mean the join
	// dropped the seeded row, which would also make the em-dash assertion
	// pass vacuously.
	if strings.Contains(body, "No clients configured") {
		t.Errorf("body unexpectedly contains empty-state copy:\n%s", body)
	}
}

// TestHandleGetPartialClientDetail_404OnUnknown proves the per-client expand
// endpoint returns 404 when the requested pubkey is not in the manifest. The
// 404 contract is what stops a stale htmx swap from rendering a chart for a
// peer that no longer exists.
func TestHandleGetPartialClientDetail_404OnUnknown(t *testing.T) {
	const (
		manifestKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		unknownKey  = "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ="
	)

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	clientsSvc := seededClientsfileSvc("alice", "172.16.15.5/32", manifestKey)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/clients/"+unknownKey+"/detail", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleGetPartialClientDetail_RendersFragment proves the happy path:
// a known pubkey returns a 200 HTML fragment carrying the chart canvas id
// and the p95 line. Two seeded client_traffic rows ~60s apart give p95
// a non-empty input — we don't pin the rendered byte value, only that the
// p95 line is present (Slice 5 sub-task 3 will extract p95 into its own
// package and pin the math there).
func TestHandleGetPartialClientDetail_RendersFragment(t *testing.T) {
	const peerPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	clientsSvc := seededClientsfileSvc("alice", "172.16.15.5/32", peerPublicKey)

	testDB := newTestDB(t)
	t0 := time.Now().Add(-2 * time.Minute)
	if err := testDB.InsertClientTraffic(context.Background(), []db.ClientTraffic{
		{TS: t0, PublicKey: peerPublicKey, Name: "alice", Address: "172.16.15.5/32", RxBytesCum: 1000, TxBytesCum: 2000},
		{TS: t0.Add(60 * time.Second), PublicKey: peerPublicKey, Name: "alice", Address: "172.16.15.5/32", RxBytesCum: 61000, TxBytesCum: 122000},
	}); err != nil {
		t.Fatalf("seed client_traffic: %v", err)
	}

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/clients/"+peerPublicKey+"/detail", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "text/html; charset=utf-8")
	}

	body := rec.Body.String()
	for _, want := range []string{
		"client-chart-" + peerPublicKey,
		"p95 over 24h:",
		// Range-hint element renders alongside the p95 line. We assert on
		// the class name only — the exact wording is allowed to evolve.
		`class="range-hint"`,
		// Sub-task 5 wraps the detail body in <td colspan="8"> so the
		// fragment is valid as innerHTML of a <tr>.
		`<td colspan="8"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
	// Fragments must not include a full HTML document — same invariant as
	// the other partial tests.
	if strings.Contains(body, "<html") {
		t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
	}
}

// TestHandleGetMetricsClient_404OnUnknownPubkey proves the JSON time-series
// endpoint enforces the same manifest-membership check as
// /partial/clients/{pubkey}/detail — an unknown pubkey returns 404 before
// the DB query runs, so a stale chart fetch from a removed peer can't leak
// rates from a residual row in client_traffic.
func TestHandleGetMetricsClient_404OnUnknownPubkey(t *testing.T) {
	const (
		manifestKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		unknownKey  = "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ="
	)

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	clientsSvc := seededClientsfileSvc("alice", "172.16.15.5/32", manifestKey)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/client/"+unknownKey, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleGetMetricsClient_400OnBadRange exercises the four-value enum
// guard: anything other than 1h/6h/24h/7d returns 400 with the canonical
// error string. Covers malformed durations, valid-but-not-allowed durations,
// and a numeric-only string that ParseDuration would reject too.
func TestHandleGetMetricsClient_400OnBadRange(t *testing.T) {
	const manifestKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	clientsSvc := seededClientsfileSvc("alice", "172.16.15.5/32", manifestKey)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	tests := []struct {
		name  string
		value string
	}{
		{"malformed", "99x"},
		{"valid_but_not_enum", "2h"},
		{"numeric_only", "24"},
		{"garbage", "yesterday"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/metrics/client/"+manifestKey+"?range="+tc.value, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, "must be 1h, 6h, 24h, or 7d") {
				t.Errorf("body missing enum-error message:\n%s", body)
			}
		})
	}
}

// TestHandleGetMetricsClient_DefaultRange proves that a request with no
// `?range=` parameter falls back to 24h: the response echoes "24h" in the
// range field and the from/to window spans ~24h. No seeded rows — the
// arrays come back as empty `[]` so the encoder emits non-null slices.
func TestHandleGetMetricsClient_DefaultRange(t *testing.T) {
	const manifestKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	clientsSvc := seededClientsfileSvc("alice", "172.16.15.5/32", manifestKey)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/client/"+manifestKey, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	var got struct {
		PublicKey string    `json:"public_key"`
		Range     string    `json:"range"`
		From      time.Time `json:"from"`
		To        time.Time `json:"to"`
		TS        []int64   `json:"ts"`
		RxRateBps []int64   `json:"rx_rate_bps"`
		TxRateBps []int64   `json:"tx_rate_bps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if got.Range != "24h" {
		t.Errorf("range = %q, want %q", got.Range, "24h")
	}
	if got.PublicKey != manifestKey {
		t.Errorf("public_key = %q, want %q", got.PublicKey, manifestKey)
	}
	// Span should be ~24h — allow a 1m fudge for the time between
	// httptest.NewRequest and handler.ServeHTTP.
	span := got.To.Sub(got.From)
	if span < 24*time.Hour-time.Minute || span > 24*time.Hour+time.Minute {
		t.Errorf("to-from = %s, want ~24h", span)
	}
}

// TestHandleGetMetricsClient_ComputesRates seeds two client_traffic rows for
// a known pubkey 60s apart with Δrx=6000, Δtx=3000 and asserts the handler
// emits exactly one sample with the correct per-second rates. Pins the
// rate-math contract end-to-end (DB → handler → JSON).
func TestHandleGetMetricsClient_ComputesRates(t *testing.T) {
	const peerPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	clientsSvc := seededClientsfileSvc("alice", "172.16.15.5/32", peerPublicKey)

	testDB := newTestDB(t)
	t0 := time.Now().Add(-2 * time.Minute)
	if err := testDB.InsertClientTraffic(context.Background(), []db.ClientTraffic{
		{TS: t0, PublicKey: peerPublicKey, Name: "alice", Address: "172.16.15.5/32", RxBytesCum: 1000, TxBytesCum: 2000},
		{TS: t0.Add(60 * time.Second), PublicKey: peerPublicKey, Name: "alice", Address: "172.16.15.5/32", RxBytesCum: 7000, TxBytesCum: 5000},
	}); err != nil {
		t.Fatalf("seed client_traffic: %v", err)
	}

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/client/"+peerPublicKey+"?range=24h", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	var got struct {
		PublicKey string      `json:"public_key"`
		Range     string      `json:"range"`
		TS        []time.Time `json:"ts"`
		RxRateBps []int64     `json:"rx_rate_bps"`
		TxRateBps []int64     `json:"tx_rate_bps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	if got.PublicKey != peerPublicKey {
		t.Errorf("public_key = %q, want %q", got.PublicKey, peerPublicKey)
	}
	if got.Range != "24h" {
		t.Errorf("range = %q, want %q", got.Range, "24h")
	}
	if len(got.TS) != 1 || len(got.RxRateBps) != 1 || len(got.TxRateBps) != 1 {
		t.Fatalf("lengths = (ts=%d, rx=%d, tx=%d), want all 1; body=%s",
			len(got.TS), len(got.RxRateBps), len(got.TxRateBps), rec.Body.String())
	}
	if got.RxRateBps[0] != 100 {
		t.Errorf("rx_rate_bps[0] = %d, want 100 (6000/60)", got.RxRateBps[0])
	}
	if got.TxRateBps[0] != 50 {
		t.Errorf("tx_rate_bps[0] = %d, want 50 (3000/60)", got.TxRateBps[0])
	}
}

// TestHandleGetMetricsClient_MonotonicTS seeds five client_traffic rows at
// strictly increasing timestamps with strictly increasing cumulative byte
// counters and proves the handler returns a TS array that's strictly
// ascending across the four resulting rate samples. The handler labels each
// rate point with rows[i].TS (end-of-interval), so monotonic input rows must
// yield monotonic output TS — this test pins that contract. The request omits
// `?range=` to exercise the default-24h path; rate-math correctness and the
// 400-on-bad-range surface are owned by sibling tests.
func TestHandleGetMetricsClient_MonotonicTS(t *testing.T) {
	const peerPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	clientsSvc := seededClientsfileSvc("alice", "172.16.15.5/32", peerPublicKey)

	testDB := newTestDB(t)
	now := time.Now()
	rows := []db.ClientTraffic{
		{TS: now.Add(-4 * time.Minute), PublicKey: peerPublicKey, Name: "alice", Address: "172.16.15.5/32", RxBytesCum: 1000, TxBytesCum: 2000},
		{TS: now.Add(-3 * time.Minute), PublicKey: peerPublicKey, Name: "alice", Address: "172.16.15.5/32", RxBytesCum: 7000, TxBytesCum: 5000},
		{TS: now.Add(-2 * time.Minute), PublicKey: peerPublicKey, Name: "alice", Address: "172.16.15.5/32", RxBytesCum: 13000, TxBytesCum: 8000},
		{TS: now.Add(-1 * time.Minute), PublicKey: peerPublicKey, Name: "alice", Address: "172.16.15.5/32", RxBytesCum: 19000, TxBytesCum: 11000},
		{TS: now, PublicKey: peerPublicKey, Name: "alice", Address: "172.16.15.5/32", RxBytesCum: 25000, TxBytesCum: 14000},
	}
	if err := testDB.InsertClientTraffic(context.Background(), rows); err != nil {
		t.Fatalf("seed client_traffic: %v", err)
	}

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	// No ?range= — exercises the default 24h path.
	req := httptest.NewRequest(http.MethodGet, "/api/metrics/client/"+peerPublicKey, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	var got struct {
		PublicKey string      `json:"public_key"`
		Range     string      `json:"range"`
		TS        []time.Time `json:"ts"`
		RxRateBps []int64     `json:"rx_rate_bps"`
		TxRateBps []int64     `json:"tx_rate_bps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	// 5 input rows yield 4 consecutive-pair rate samples.
	if len(got.TS) != 4 {
		t.Fatalf("len(ts) = %d, want 4; body=%s", len(got.TS), rec.Body.String())
	}
	for i := 1; i < len(got.TS); i++ {
		if !got.TS[i].After(got.TS[i-1]) {
			t.Errorf("ts[%d]=%s not strictly after ts[%d]=%s", i, got.TS[i], i-1, got.TS[i-1])
		}
	}
	for i, r := range got.RxRateBps {
		if r < 0 {
			t.Errorf("rx_rate_bps[%d]=%d < 0", i, r)
		}
	}
	for i, r := range got.TxRateBps {
		if r < 0 {
			t.Errorf("tx_rate_bps[%d]=%d < 0", i, r)
		}
	}
}
