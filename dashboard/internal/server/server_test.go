package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

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

// TestHandleIndex_ServerInfoSuccess proves the dashboard.html template renders
// the server-info card with concrete values when serverinfo.Service.Get
// returns successfully. We inject a fake IMDS and a fake Runner directly
// into a serverinfo.Service literal; both fields are exported precisely for
// this kind of test wiring.
func TestHandleIndex_ServerInfoSuccess(t *testing.T) {
	const (
		fakeIP  = "203.0.113.1"
		fakeKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
	)

	svc := &serverinfo.Service{
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

	handler, err := server.New(dashboard.WebFS(), svc)
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
		"Public IP",
		fakeIP,
		"51820",
		fakeKey,
		`class="copy-btn"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
	// Error card MUST NOT render in the success path.
	if strings.Contains(body, `class="card error"`) {
		t.Errorf("body unexpectedly contains error card:\n%s", body)
	}
}

// TestHandleIndex_ServerInfoError proves that when serverinfo.Service.Get
// fails, the page still renders 200 OK with the error card in place of the
// server-info card. Partial degradation is the documented contract — a hard
// 500 would deny the operator the rest of the (future) page.
func TestHandleIndex_ServerInfoError(t *testing.T) {
	svc := &serverinfo.Service{
		IMDS: fakeIMDS{err: io.ErrUnexpectedEOF},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, io.ErrUnexpectedEOF
		},
	}

	handler, err := server.New(dashboard.WebFS(), svc)
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
	if !strings.Contains(body, `class="card error"`) {
		t.Errorf("body missing error card:\n%s", body)
	}
	// server-info card must NOT render when there's an error.
	if strings.Contains(body, `id="server-info"`) {
		t.Errorf("body unexpectedly contains server-info card on error path:\n%s", body)
	}
}
