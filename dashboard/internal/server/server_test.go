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
	"wireguard-dashboard/internal/netdev"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/processes"
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
// every Lookup / LookupGeo. Used by the seeded-row partial-clients test to
// assert that the geo cell renders the resolver's output — the resolver's real
// filtering behaviour (RFC1918, mmdb misses) is covered separately in
// clientrows_test.go.
//
// lat/lon/ok back LookupGeo for the /api/geo handler test: set ok=true with
// coordinates to make a peer mappable, ok=false to exclude it.
type staticGeoResolver struct {
	country string
	city    string
	lat     float64
	lon     float64
	ok      bool
}

func (s staticGeoResolver) Lookup(_ net.IP) (string, string) { return s.country, s.city }

func (s staticGeoResolver) LookupGeo(_ net.IP) geoip.GeoPoint {
	return geoip.GeoPoint{Country: s.country, City: s.city, Lat: s.lat, Lon: s.lon, OK: s.ok}
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
		"/proc/stat":                             []byte("cpu  100 0 50 800 10 0 0 0 0 0\n"),
		"/proc/meminfo":                          []byte("MemTotal:    8000000 kB\nMemAvailable:  4000000 kB\n"),
		"/proc/uptime":                           []byte("12345.67 9876.54\n"),
		"/sys/class/net/wg0/statistics/rx_bytes": []byte("1024\n"),
		"/sys/class/net/wg0/statistics/tx_bytes": []byte("2048\n"),
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

// fakeProcessesSvc returns a *processes.Service with an in-memory Reader /
// ReadDir / LookupUser that serves canned /proc/stat, /proc/meminfo, and one
// fake PID (1, "init") under /proc/1/. The numeric values are arbitrary —
// the partial-system tests only need Sample() to succeed and emit a single
// row so the template's populated branch renders; rate-math correctness is
// owned by the processes package's own unit tests.
//
// ProcPath is kept as the real default so the canned paths line up with what
// Service.readStat / readStatus / readCmdline will request.
func fakeProcessesSvc() *processes.Service {
	return &processes.Service{
		Reader: func(path string) ([]byte, error) {
			switch path {
			case "/proc/stat":
				return []byte("cpu  100 0 50 1000 0 0 0 0 0 0\n"), nil
			case "/proc/meminfo":
				return []byte("MemTotal: 8000000 kB\n"), nil
			case "/proc/1/stat":
				return []byte("1 (init) S 0 1 1 0 -1 4194560 0 0 0 0 50 30 0 0 20 0 1 0 1 0 0 0\n"), nil
			case "/proc/1/status":
				return []byte("Name: init\nUid: 0 0 0 0\nVmRSS: 2048 kB\n"), nil
			case "/proc/1/cmdline":
				return []byte("/sbin/init\x00"), nil
			}
			return nil, fmt.Errorf("fake reader: unexpected path %q", path)
		},
		ReadDir: func(path string) ([]string, error) {
			if path == "/proc" {
				return []string{"1", "stat", "meminfo"}, nil
			}
			return nil, fmt.Errorf("fake readdir: unexpected path %q", path)
		},
		LookupUser: func(uid string) (string, error) {
			if uid == "0" {
				return "root", nil
			}
			return "", fmt.Errorf("no such user")
		},
		ProcPath: "/proc",
		Now:      time.Now,
	}
}

// fakeNetdevSvc returns a *netdev.Service with an in-memory Reader serving a
// canned two-header-line /proc/net/dev fixture with one wg0 row. The 16
// counters decode to recognisable values (rx_bytes=1024, tx_bytes=2048 etc.)
// so populated-branch assertions on the wg-iface-stats card can pin a known
// number if needed. PeerCount returns 2 — a deterministic non-zero so the
// "Peers" cell renders as "2" rather than "0".
//
// NetDevPath / Iface match the production defaults so the canned path lines
// up with what Service.Sample requests.
func fakeNetdevSvc() *netdev.Service {
	const fixture = "Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"  wg0: 1024 10 1 2 0 0 0 0 2048 20 3 4 0 0 0 0\n"
	return &netdev.Service{
		Reader: func(path string) ([]byte, error) {
			if path != "/proc/net/dev" {
				return nil, fmt.Errorf("fake netdev reader: unexpected path %q", path)
			}
			return []byte(fixture), nil
		},
		PeerCount:  func(_ context.Context) (int, error) { return 2, nil },
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
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

// fakeIMDS is a stub imdsClient that returns canned IMDS values. The four
// fields default to the empty string so existing tests that only care about
// the public-IP path can keep using `fakeIMDS{ip: "..."}`; the about-tab
// test in this package populates instanceType / az / amiID explicitly.
type fakeIMDS struct {
	ip           string
	instanceType string
	az           string
	amiID        string
	vpcCIDR      string
	err          error
}

func (f fakeIMDS) PublicIP(_ context.Context) (string, error) {
	return f.ip, f.err
}

func (f fakeIMDS) InstanceType(_ context.Context) (string, error) {
	return f.instanceType, f.err
}

func (f fakeIMDS) AvailabilityZone(_ context.Context) (string, error) {
	return f.az, f.err
}

func (f fakeIMDS) AMIID(_ context.Context) (string, error) {
	return f.amiID, f.err
}

func (f fakeIMDS) VPCIPv4CIDR(_ context.Context) (string, error) {
	return f.vpcCIDR, f.err
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
	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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
		// client-count card — fakes return an empty manifest, so the
		// online figure renders as 0. Slice-5 redesigned this into a KPI
		// stat tile (number + small "online"/"total" labels), so the count
		// and its label are now separate elements. The full client-list
		// card was retired in Slice 14 (it lives only on the Clients tab).
		`id="client-count"`,
		`class="stat-num stat-online">0<`,
		`class="stat-label">online<`,
		`class="stat-label">total<`,
		// Overview-grid wrapper — Slice 12 layout: cards land in a 2-col
		// grid via the wrapper div rather than #tab-body's auto-fit.
		`class="overview-grid"`,
		// system card — fakeProcSvc returns first-sample stats:
		// CPUPercent is 0 (no prior sample to delta against), memory
		// is MemTotal=8000000 kB / MemAvailable=4000000 kB so used = 50%.
		`id="system"`,
		"0.0%",
		"50.0%",
		// network-rate card — first-sample rates render as "0 B/s".
		`id="network-rate"`,
		"0 B/s",
		// Trend-chart partials moved into the System / Network tab bodies
		// (Slices 6 + 8), so the cold-load Overview page no longer renders
		// any of the four trend charts. The Chart.js JS asset is still
		// loaded globally so tab swaps can hydrate canvases in-place.
		`/static/chart.umd.min.js`,
		`/static/charts.js`,
		// htmx wiring — the page swaps #tab-body via /partial/<tab>.
		`<main id="tab-body"`,
		`class="tab-bar"`,
		`/static/htmx.min.js`,
		// Stale-data indicator — the pill lives outside #tab-body so it
		// survives htmx innerHTML swaps, and the IIFE listener is loaded
		// after htmx itself.
		`id="stale-pill"`,
		`/static/htmx-stale.js`,
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
	handler, err := server.New(dashboard.WebFS(), infoSvc, systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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
	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

// TestHandleGetPartialTabs proves each of the six tab partial routes
// (overview, clients, system, network, events, about) returns a 200 HTML
// fragment with the expected sentinel string and without a full-document
// wrapper. The handler is constructed once and reused across subtests; the
// overview path drives the full buildPageData call graph while the five
// other tabs are template-only or have their own focused data fetches.
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
	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	tests := []struct {
		name             string
		path             string
		wantBodyContains []string
	}{
		// Slice 12 consolidates Overview: server-info + service-status + uptime
		// + client-count + system + network-rate. The client-count card lands
		// from buildPageData with Online=0/Total=0 because fakeClientsfileSvc
		// returns an empty manifest. Sibling test below asserts the negative
		// (chart canvases NOT present).
		{"overview", "/partial/overview", []string{`id="server-info"`, `id="client-count"`, `class="stat-num stat-online">0<`}},
		// sub-task 7 expands this with a seeded-row assertion; for now the
		// fake clientsfile + fake wg both return empty so the template renders
		// the empty-state branch.
		// The geo map card (Slice 4) renders unconditionally — even with no
		// clients — so its shell sentinels appear alongside the empty-state.
		{"clients", "/partial/clients", []string{"No clients configured", `id="geo-map"`, "No mappable peers."}},
		// Slice 6 sub-task 4 promotes the System tab from placeholder: the
		// fragment embeds the cards/disk.html mount table (id="disk").
		// fakeDiskSvc seeds one /-mounted ext4 row at 50% full, so the
		// populated table branch renders rather than the empty-state. The
		// CPU/memory large-numerics card (id="system") was removed from the
		// System tab after the post-Slice-6 UX pass — it lives on Overview
		// only now.
		{"system", "/partial/system", []string{`id="disk"`}},
		// Slice 8 sub-task 5 promotes the Network tab from placeholder: the
		// fragment now embeds the cards/wg-iface-stats.html section (id="wg-iface-stats").
		// fakeNetdevSvc serves a canned wg0 row so the populated branch
		// renders rather than the error branch.
		{"network", "/partial/network", []string{`id="wg-iface-stats"`}},
		// Slice 10 promotes the Events tab from placeholder: with no seeded
		// rows the cards/events.html empty branch renders the canonical
		// "No recent handshakes." copy.
		{"events", "/partial/events", []string{`id="events"`, "No recent handshakes."}},
		// Slice 11 promotes the About tab from placeholder: four cards
		// (about-ec2 / about-binary / about-os / re-used server-info).
		// The Uname/ReadFile seams aren't wired in this test's Service{}
		// literal, so the OS card renders via the "reader not wired"
		// error branches — placeholder-existence assertion is satisfied
		// by the three new id attributes regardless of error state.
		{"about", "/partial/about", []string{`id="about-ec2"`, `id="about-binary"`, `id="about-os"`}},
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

// TestHandleGetPartialClients_RendersGeoMapCard pins the server-rendered shell
// of the geo map card (spec 006 Slice 4). The markers themselves are JS-driven
// (world-map.js fetches /api/geo on the 10s tick), so this asserts only the
// static surface the fragment must emit: the card container the JS hooks onto
// (id="geo-map"), the embedded base-map <img> pointing at the local SVG, the
// marker overlay container, the online/offline legend swatches, the
// "N not mappable" caption element (id="geo-not-mappable") the JS fills, and
// the cold-load empty state. The card renders unconditionally — the fakes here
// return an empty fleet, exercising the no-clients branch alongside it.
//
// A sibling assertion on the index page confirms world-map.js is actually
// referenced (the script tag is what hydrates the shell); without it the
// server-rendered shell would never gain markers.
func TestHandleGetPartialClients_RendersGeoMapCard(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	wantFragment := []string{
		`id="clients"`,            // full-row-span anchor for the wide client table (Slice 6)
		`id="geo-map"`,            // JS hook + render-test anchor
		`src="/static/world.svg"`, // embedded base map, no external tiles
		`class="geo-viewport"`,    // spec 010 Slice 2: clipping viewport
		`class="geo-canvas"`,      // spec 010 Slice 2: transformable canvas
		`class="geo-markers"`,     // marker overlay container
		`data-geo-zoom="in"`,      // spec 010 Slice 2: zoom-in control
		`data-geo-zoom="out"`,     // spec 010 Slice 2: zoom-out control
		`data-geo-zoom="reset"`,   // spec 010 Slice 2: reset/fit control
		`class="geo-legend"`,      // legend
		`geo-swatch online`,       // online legend swatch
		`geo-swatch offline`,      // offline legend swatch
		`id="geo-not-mappable"`,   // "N not mappable" caption element
		"No mappable peers.",      // cold-load empty state
	}
	for _, want := range wantFragment {
		if !strings.Contains(body, want) {
			t.Errorf("clients fragment missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<html") {
		t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
	}

	// The hydrating script must be wired into the page shell.
	idxReq := httptest.NewRequest(http.MethodGet, "/", nil)
	idxRec := httptest.NewRecorder()
	handler.ServeHTTP(idxRec, idxReq)
	if idxRec.Code != http.StatusOK {
		t.Fatalf("index status = %d, want 200", idxRec.Code)
	}
	if !strings.Contains(idxRec.Body.String(), `src="/static/world-map.js"`) {
		t.Errorf("index page missing world-map.js script tag")
	}
}

// TestHandleGetPartialNetwork_RendersCards pins the Network tab fragment's
// load-bearing surface: the WireGuard interface stats card (heading + the
// PeerCount=2 cell from fakeNetdevSvc) plus the aggregate-traffic card driven
// by two seeded traffic_metrics rows ~10 minutes apart. Seed values are picked
// so the deltas (10240 / 20480 bytes) render as "10.0 KB" and "20.0 KB"
// exactly — humanBytes uses 1024-base units with one decimal, so the round
// numbers avoid coupling the assertion to formatting jitter at the rounding
// boundary. The "in /" + "out" sentinels prove the aggregate-traffic template
// emitted both <strong> values plus the closing </p>.
//
// Sibling test TestHandleGetPartialTabs/network already asserts id="wg-iface-stats"
// exists; this test layers in the heading, peer count, and aggregate-traffic
// assertions that the placeholder-existence check intentionally doesn't cover.
func TestHandleGetPartialNetwork_RendersCards(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	testDB := newTestDB(t)
	now := time.Now()
	// Two rows ~10 min apart with clean deltas: Δrx=10240 (= 10.0 KB),
	// Δtx=20480 (= 20.0 KB). The handler queries the last 24h, so anchoring
	// the earlier row at now-10m keeps both rows inside the window.
	for _, row := range []db.TrafficMetric{
		{TS: now.Add(-10 * time.Minute), RxBytesCum: 0, TxBytesCum: 0},
		{TS: now, RxBytesCum: 10240, TxBytesCum: 20480},
	} {
		if err := testDB.InsertTrafficMetric(context.Background(), row); err != nil {
			t.Fatalf("seed traffic_metrics: %v", err)
		}
	}

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/network", nil)
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
		// wg-iface-stats card — heading + PeerCount=2 cell proves fakeNetdevSvc's
		// PeerCount seam landed in the template (the Peers row of the <dl>).
		"<h2>WireGuard interface</h2>",
		"Peers</dt>",
		"<dd>2</dd>",
		// aggregate-traffic card — id, Range label, and the literal "in /"
		// sentinel between the two <strong> values.
		`id="aggregate-traffic"`,
		"Last 24h:",
		"in /",
		// Seeded deltas: 10240 bytes / 1024 = 10.0 KB; 20480 / 1024 = 20.0 KB.
		"10.0 KB",
		"20.0 KB",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}

	// "out" must be followed by the </p> close — proves the second <strong>
	// rendered and the paragraph closed. The template emits a newline plus
	// two-space indent between "out" and "</p>", so we assert on the
	// rendered whitespace form rather than the collapsed variant.
	if !strings.Contains(body, "out\n  </p>") {
		t.Errorf("body missing `out\\n  </p>` (closing aggregate-traffic line):\n%s", body)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, amberDiskSvc, fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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
		// Chart cards moved into the System tab body in sub-task 4. The
		// CPU/memory large-numerics card was removed from the System tab
		// after the post-Slice-6 UX pass — it lives on Overview only now,
		// so we no longer assert id="system" here.
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

// TestHandleGetPartialSystem_RendersProcessesCard pins the processes card
// surface of the System tab fragment: the literal <h2>Top processes</h2>
// heading, the section id, the .process-table layout from
// cards/processes.html, the PID / Command header cells, and a populated row
// emitted from the seeded fake's single PID=1 entry. Numeric CPU% / Mem%
// values are intentionally not pinned — the fake's first-call CPUPct is 0 by
// construction (no prior sample to delta against) and MemPct depends on the
// fake's MemTotal vs VmRSS values; the processes package's unit tests own
// the rate-math contract. Here we only prove the wiring: processesSvc.Sample
// is called, its rows reach the template, and the populated branch renders.
func TestHandleGetPartialSystem_RendersProcessesCard(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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
		// Literal heading + section id from cards/processes.html.
		`<h2>Top processes</h2>`,
		`id="processes"`,
		// Table layout — `class="process-table"` proves we're on the
		// populated branch, not the empty-state <p class="empty">…</p>
		// wrapper.
		`class="process-table"`,
		// Header cells — proves the <thead> rendered.
		`<th>PID</th>`,
		`<th>Command</th>`,
		// Row marker — the seeded fake emits PID=1. Wrapped in <td>…</td>
		// so the assertion doesn't match a stray "1" elsewhere in the
		// fragment (chart datasets, htmx attribute values, etc).
		`<td>1</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
	// Error branch must NOT render in the populated path.
	if strings.Contains(body, "Failed to load processes") {
		t.Errorf("body unexpectedly contains processes error copy:\n%s", body)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, wgSvc, fakeProcSvc(), newTestDB(t), geo, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, wgSvc, fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, wgSvc, fakeProcSvc(), newTestDB(t), geo, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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

// TestHandleGetMetricsSystem_400OnBadRange exercises the four-value enum
// guard on the global system-metrics endpoint. /api/metrics/system shares
// rangeEnumMap + rangeEnumErrMsg with the per-client endpoint, so the same
// canonical error string is asserted. Same table shape as the existing
// TestHandleGetMetricsClient_400OnBadRange for sibling consistency.
func TestHandleGetMetricsSystem_400OnBadRange(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
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
			req := httptest.NewRequest(http.MethodGet, "/api/metrics/system?range="+tc.value, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "must be 1h, 6h, 24h, or 7d") {
				t.Errorf("body missing enum-error message:\n%s", rec.Body.String())
			}
		})
	}
}

// TestHandleGetMetricsSystem_7dRangeWindowFilters seeds system_metrics rows
// spanning the last 8 days and proves that ?range=7d returns only rows
// inside the 7-day window — the 8-day-old row is filtered out by the
// rangeEnumMap-derived (now-7d, now) bounds. Mirror test for the spec's
// "up to 8 d worth of points" phrasing: the seeded data spans 8 days, but
// the active window is 7 days, so the oldest seeded row is correctly excluded
// and the three newer rows pass through in chronological order.
func TestHandleGetMetricsSystem_7dRangeWindowFilters(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	testDB := newTestDB(t)
	now := time.Now().UTC()
	seeded := []db.SystemMetric{
		{TS: now.Add(-8 * 24 * time.Hour), CPUPct: 1, MemPct: 10}, // outside 7d window
		{TS: now.Add(-6 * 24 * time.Hour), CPUPct: 2, MemPct: 20}, // inside
		{TS: now.Add(-24 * time.Hour), CPUPct: 3, MemPct: 30},     // inside
		{TS: now.Add(-1 * time.Hour), CPUPct: 4, MemPct: 40},      // inside
	}
	for _, row := range seeded {
		if err := testDB.InsertSystemMetric(context.Background(), row); err != nil {
			t.Fatalf("seed system_metrics: %v", err)
		}
	}

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/system?range=7d", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	var got struct {
		Range  string      `json:"range"`
		From   time.Time   `json:"from"`
		To     time.Time   `json:"to"`
		TS     []time.Time `json:"ts"`
		CPUPct []float64   `json:"cpu_pct"`
		MemPct []float64   `json:"mem_pct"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}

	if got.Range != "7d" {
		t.Errorf("range = %q, want %q", got.Range, "7d")
	}
	// Three inside-window rows; the -8d row filtered out by the lower bound.
	if len(got.TS) != 3 {
		t.Fatalf("len(ts) = %d, want 3 (filtered out the -8d row); body=%s", len(got.TS), rec.Body.String())
	}
	wantCPU := []float64{2, 3, 4}
	for i, v := range wantCPU {
		if got.CPUPct[i] != v {
			t.Errorf("cpu_pct[%d] = %v, want %v", i, got.CPUPct[i], v)
		}
	}
}

// TestHandleGetPartialEvents_RendersSeededRows seeds 55 handshake_events
// rows at strictly increasing timestamps with distinct peer names ("peer-00"
// through "peer-54"), hits /partial/events, and asserts the rendered fragment
// (a) contains the events card heading, (b) renders the newest event's name
// (peer-54), (c) does NOT contain the oldest seeded row's name (peer-00 ..
// peer-04 — the five rows that fall outside the LIMIT 50 cut). Pins the
// "50 newest" cap raised in Slice 10 from the Overview card's 10.
func TestHandleGetPartialEvents_RendersSeededRows(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	testDB := newTestDB(t)
	// Seed 55 events: peer-00 is the oldest (now-55min), peer-54 is the
	// newest (now-1min). QueryHandshakeEvents orders ts DESC LIMIT 50, so
	// peer-54..peer-05 come back; peer-04..peer-00 are filtered by the cap.
	now := time.Now()
	events := make([]db.HandshakeEvent, 0, 55)
	for i := 0; i < 55; i++ {
		events = append(events, db.HandshakeEvent{
			TS:        now.Add(-time.Duration(55-i) * time.Minute),
			PublicKey: fmt.Sprintf("key-%02d", i),
			Name:      fmt.Sprintf("peer-%02d", i),
		})
	}
	if err := testDB.InsertHandshakeEvents(context.Background(), events); err != nil {
		t.Fatalf("seed handshake_events: %v", err)
	}

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), testDB, nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/events", nil)
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
		`id="events"`,
		"<h2>Recent handshakes</h2>",
		// peer-54 is the newest seeded row — must render.
		"peer-54",
		// peer-05 is the 50th newest (still inside the LIMIT 50 cut).
		"peer-05",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	// peer-00..peer-04 are the 5 oldest rows — beyond the 50-row cap.
	for _, dont := range []string{"peer-00", "peer-01", "peer-02", "peer-03", "peer-04"} {
		if strings.Contains(body, dont) {
			t.Errorf("body unexpectedly contains %q (should be filtered by LIMIT 50):\n%s", dont, body)
		}
	}
}

// TestHandleGetPartialAbout_RendersAllCards pins the About-tab fragment's
// load-bearing surface: the four cards rendered by tabs/about.html with
// canned IMDS values, stubbed Uname + ReadFile seams, and a populated Build
// struct. Asserts the two spec-mandated sentinels ("Instance type", "Build")
// plus the actual injected values, proving the end-to-end wiring from the
// imdsClient stub through to the rendered <dd> cells.
//
// The IMDS / Uname / ReadFile stubs return offline values so this test
// never touches the link-local metadata service, the real uname syscall,
// or /etc/os-release on the host running `go test`.
func TestHandleGetPartialAbout_RendersAllCards(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{
			ip:           "203.0.113.99",
			instanceType: "t3.micro",
			az:           "us-east-1a",
			amiID:        "ami-0abcdef1234567890",
		},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
		// Uname stub deposits canned kernel strings (NUL-terminated to mimic
		// the real syscall) so the OS card renders the populated branch.
		Uname: func(u *unix.Utsname) error {
			copy(u.Sysname[:], "Linux\x00")
			copy(u.Nodename[:], "wg-host\x00")
			copy(u.Release[:], "6.8.0-1015-aws\x00")
			copy(u.Version[:], "#16~22.04.1-Ubuntu SMP\x00")
			copy(u.Machine[:], "x86_64\x00")
			return nil
		},
		// ReadFile stub returns an Amazon Linux 2023-style /etc/os-release
		// so the OS card's PrettyName / ID / Version rows populate.
		ReadFile: func(_ string) ([]byte, error) {
			return []byte("NAME=\"Amazon Linux\"\nVERSION=\"2023\"\nID=amzn\nPRETTY_NAME=\"Amazon Linux 2023\"\n"), nil
		},
		Build: serverinfo.BuildInfo{
			ReleaseTag: "v1.2.3",
			SHA:        "d4e05cb",
			Time:       "2026-05-14T12:35:08Z",
			GoVersion:  "go1.25.5",
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/about", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "text/html; charset=utf-8")
	}

	body := rec.Body.String()
	if strings.Contains(body, "<html") {
		t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
	}

	for _, want := range []string{
		// Four card ids — proves the about template composed all four sections.
		`id="about-ec2"`,
		`id="about-binary"`,
		`id="about-os"`,
		`id="server-info"`,
		// Spec-mandated sentinels: "Instance type" + "Build".
		"Instance type",
		"Release",
		"Build SHA",
		"Build time",
		// EC2 card body cells.
		"203.0.113.99",
		"t3.micro",
		"us-east-1a",
		"ami-0abcdef1234567890",
		// Binary card body cells.
		"v1.2.3",
		"d4e05cb",
		"2026-05-14T12:35:08Z",
		"go1.25.5",
		// OS card body cells (PrettyName + kernel triple).
		"Amazon Linux 2023",
		"Linux 6.8.0-1015-aws",
		"wg-host",
		"x86_64",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

// TestHandleGetPartialOverview_ConsolidatedView pins the Slice 12 consolidation:
// Overview should render server-info + service-status + uptime + client-count
// + system + network-rate. It should NOT carry the chart canvas IDs (those
// live on System / Network) and NOT render the full client list or the
// handshake events log (those live on Clients / Events).
//
// One seeded peer drives a non-zero client-count so the "1 online / 1 total"
// rendering is asserted, plus the cards/system.html `id="system"` for the
// CPU/Memory large-numerics card and the cards/network-rate.html surface.
func TestHandleGetPartialOverview_ConsolidatedView(t *testing.T) {
	const (
		peerName      = "alice"
		peerAddress   = "172.16.15.5/32"
		peerPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		peerEndpoint  = "203.0.113.50:51820"
	)

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("dummy-key=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))
	clientsSvc := seededClientsfileSvc(peerName, peerAddress, peerPublicKey)
	// 10s-ago handshake → status="online" so the client-count card reports 1.
	wgSvc := seededWgSvc(peerPublicKey, peerEndpoint, peerAddress, 10*time.Second, 123456, 654321)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, wgSvc, fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/overview", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, "<html") {
		t.Errorf("partial body unexpectedly contains <html ...>:\n%s", body)
	}

	// Positive assertions — every consolidated card must render.
	// The standalone Uptime card was dropped after the UX pass — "Active
	// since…" lives inside service-status, "Host uptime" stays inside the
	// System card. The wrapper .overview-grid div pins the new 2-col layout.
	for _, want := range []string{
		`class="overview-grid"`,
		`id="server-info"`,
		`id="service-status"`,
		`id="client-count"`,
		`id="system"`,       // CPU/Memory large-numerics card
		`id="network-rate"`, // rx/tx rate card
		// Slice-5 KPI stat tile: online figure (1) carries .stat-online; the
		// "online"/"total" labels are now separate small <span>s.
		`class="stat-num stat-online">1<`,
		`class="stat-label">online<`,
		`class="stat-label">total<`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}

	// Negative assertions — these have moved to other tabs and must NOT
	// appear on Overview. Chart canvases live on System / Network only.
	for _, dont := range []string{
		`id="canvas-cpu"`,
		`id="canvas-memory"`,
		`id="canvas-rx"`,
		`id="canvas-tx"`,
		`data-chart="cpu"`,
		`data-chart="memory"`,
		`data-chart="rx"`,
		`data-chart="tx"`,
		// Events card moved to the Events tab.
		`id="events"`,
		// Standalone Uptime card was dropped — info is in service-status
		// + the System card's Host uptime row.
		`id="uptime"`,
		// Full client list moved to the Clients tab; only the count summary
		// appears on Overview. The seeded peer's name is the cleanest signal
		// that the full list is NOT being rendered here.
		peerName,
	} {
		if strings.Contains(body, dont) {
			t.Errorf("body unexpectedly contains %q (should be absent from Overview):\n%s", dont, body)
		}
	}
}
