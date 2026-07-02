package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

// configTestConsts groups the shared fixtures for the config-download tests.
const (
	cfgIP      = "203.0.113.1"
	cfgKey     = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
	cfgName    = "alice"
	cfgAddress = "172.16.15.6/32"
	// Synthetic 44-char base64 peer key — distinct from the server pub above.
	cfgPeerPubKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	cfgVPCCIDR    = "10.23.0.0/16"
)

// newConfigHandler wires a server with an injectable serverinfo.Service (fake
// IMDS + Runner) and a seeded one-client manifest. ip / vpcCIDR / imdsErr let
// each test drive the success and 503 paths without re-stating the full
// server.New argument list.
func newConfigHandler(t *testing.T, ip, vpcCIDR string, imdsErr error) http.Handler {
	t.Helper()

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: ip, vpcCIDR: vpcCIDR, err: imdsErr},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(cfgKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-time.Hour))
	clientsSvc := seededClientsfileSvc(cfgName, cfgAddress, cfgPeerPubKey)

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil, nil, seededClientsSvc(t, db.Client{Name: cfgName, Address: cfgAddress, PublicKey: cfgPeerPubKey}), "local")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return handler
}

// TestHandleGetClientConfig_Full pins the happy path: a known client yields a
// 200 with the attachment headers and the exact full-tunnel config, including
// the derived DNS line (10.23.0.0/16 -> 10.23.0.2) and the placeholder.
func TestHandleGetClientConfig_Full(t *testing.T) {
	handler := newConfigHandler(t, cfgIP, cfgVPCCIDR, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/clients/"+cfgName+"/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "text/plain; charset=utf-8")
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="wg-alice.conf"` {
		t.Errorf("Content-Disposition = %q, want %q", got, `attachment; filename="wg-alice.conf"`)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"[Interface]",
		"PrivateKey = <paste your client private key here>",
		"Address = 172.16.15.6/32",
		"DNS = 10.23.0.2",
		"[Peer]",
		"PublicKey = " + cfgKey,
		"Endpoint = 203.0.113.1:51820",
		"AllowedIPs = 0.0.0.0/0, ::/0",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestHandleGetClientConfig_Split proves `?mode=split` swaps the AllowedIPs to
// the WG overlay + VPC CIDR while leaving the rest of the config identical.
func TestHandleGetClientConfig_Split(t *testing.T) {
	handler := newConfigHandler(t, cfgIP, cfgVPCCIDR, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/clients/"+cfgName+"/config?mode=split", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "AllowedIPs = 172.16.15.0/24, 10.23.0.0/16") {
		t.Errorf("split body missing VPC-scoped AllowedIPs\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "AllowedIPs = 0.0.0.0/0") {
		t.Errorf("split body unexpectedly contains full-tunnel AllowedIPs\n--- body ---\n%s", body)
	}
}

// TestHandleGetClientConfig_GarbageModeDefaultsFull proves an unrecognised
// `?mode=` value falls back to the full-tunnel profile rather than erroring.
func TestHandleGetClientConfig_GarbageModeDefaultsFull(t *testing.T) {
	handler := newConfigHandler(t, cfgIP, cfgVPCCIDR, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/clients/"+cfgName+"/config?mode=garbage", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "AllowedIPs = 0.0.0.0/0, ::/0") {
		t.Errorf("garbage mode did not default to full-tunnel AllowedIPs\n--- body ---\n%s", rec.Body.String())
	}
}

// TestHandleGetPartialClients_DownloadControl proves the Clients tab renders
// the per-client Full/Split download links (keyed by name) plus the private-key
// hint when at least one client is configured. The seeded client has no live
// wg peer, so it renders as a "pending" row — which still carries a name and
// therefore a download control.
func TestHandleGetPartialClients_DownloadControl(t *testing.T) {
	handler := newConfigHandler(t, cfgIP, cfgVPCCIDR, nil)

	req := httptest.NewRequest(http.MethodGet, "/partial/clients", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<th>Config</th>",
		`href="/api/clients/alice/config?mode=full"`,
		`href="/api/clients/alice/config?mode=split"`,
		// Private-key hint — readable without JS, present whenever rows exist.
		"the server never holds it",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestHandleGetClientConfig_404 proves an unknown client name yields 404 rather
// than an empty file.
func TestHandleGetClientConfig_404(t *testing.T) {
	handler := newConfigHandler(t, cfgIP, cfgVPCCIDR, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/clients/ghost/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleGetClientConfig_503MissingServerInfo proves an IMDS failure (so
// serverinfo.Get can't resolve the public IP / key) yields 503, not a config
// with blank server fields.
func TestHandleGetClientConfig_503MissingServerInfo(t *testing.T) {
	handler := newConfigHandler(t, cfgIP, cfgVPCCIDR, io.ErrUnexpectedEOF)

	req := httptest.NewRequest(http.MethodGet, "/api/clients/"+cfgName+"/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleGetClientConfig_NoVPCFallsBackToClientDNS proves an empty VPC CIDR
// no longer 503s: the DNS line falls back to WG_CLIENT_DNS and the split route
// is overlay-only. (Pre-fix this returned 503; the dashboard must now build a
// usable config without a VPC.)
func TestHandleGetClientConfig_NoVPCFallsBackToClientDNS(t *testing.T) {
	handler := newConfigHandler(t, cfgIP, "", nil)

	req := httptest.NewRequest(http.MethodGet, "/api/clients/"+cfgName+"/config?mode=split", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "DNS = "+serverinfo.DefaultClientDNS) {
		t.Errorf("body missing fallback DNS %q\n--- body ---\n%s", serverinfo.DefaultClientDNS, body)
	}
	if !strings.Contains(body, "AllowedIPs = 172.16.15.0/24\n") {
		t.Errorf("split body missing overlay-only AllowedIPs\n--- body ---\n%s", body)
	}
}

// TestHandleGetClientConfig_OffAWS proves a standalone (non-EC2) host builds a
// config: the public IP comes from the echo client, the DNS from WG_CLIENT_DNS,
// and the split route is overlay-only — no IMDS, no 503.
func TestHandleGetClientConfig_OffAWS(t *testing.T) {
	const offAWSDNS = "9.9.9.9"
	infoSvc := &serverinfo.Service{
		IMDS:      fakeIMDS{err: io.ErrUnexpectedEOF}, // must not be reached off-AWS
		EC2Probe:  func(context.Context) error { return serverinfo.ErrNotOnEC2 },
		Echo:      func(context.Context) (string, error) { return cfgIP, nil },
		ClientDNS: offAWSDNS,
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(cfgKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-time.Hour))
	clientsSvc := seededClientsfileSvc(cfgName, cfgAddress, cfgPeerPubKey)
	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, clientsSvc, fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil, nil, seededClientsSvc(t, db.Client{Name: cfgName, Address: cfgAddress, PublicKey: cfgPeerPubKey}), "local")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/clients/"+cfgName+"/config?mode=split", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Endpoint = " + cfgIP + ":51820",
		"DNS = " + offAWSDNS,
		"AllowedIPs = 172.16.15.0/24\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("off-AWS body missing %q\n--- body ---\n%s", want, body)
		}
	}
	if strings.Contains(body, cfgVPCCIDR) {
		t.Errorf("off-AWS body unexpectedly contains a VPC CIDR\n--- body ---\n%s", body)
	}
}
