package server_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
	"wireguard-dashboard/internal/systemd"
)

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

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc)
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

	handler, err := server.New(dashboard.WebFS(), infoSvc, systemdSvc)
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
