package server_test

import (
	"context"
	"encoding/json"
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

// dbClientConsts groups the fixtures for the DB-sourced client tests (spec 015).
const (
	dbcName    = "dave"
	dbcAddr    = "172.16.15.9/32"
	dbcPubKey  = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD="
	dbcSrvKey  = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
	dbcVPCCIDR = "10.23.0.0/16"
)

// TestHandleGetPartialClients_FromDB proves the client list renders from the
// runtime DB (spec 015), not clients.json: with an empty clients.json seed but
// a DB-seeded client, the row still renders.
func TestHandleGetPartialClients_FromDB(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(dbcSrvKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	// clientsfileSvc returns an empty seed; the DB holds the only client.
	handler, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(),
		fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil, nil, nil,
		seededClientsSvc(t, db.Client{Name: dbcName, Address: dbcAddr, PublicKey: dbcPubKey}), "local", nil)
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
		dbcName, // DB-sourced row rendered
		dbcAddr, // its tunnel address
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
	if strings.Contains(body, "No clients configured") {
		t.Errorf("body unexpectedly contains empty-state copy:\n%s", body)
	}
}

// TestHandleGetClientConfig_ResolvesFromDB proves the config-download lookup now
// resolves the client from the DB (spec 015): the clients.json baseline is empty
// but the DB holds the client, and a valid config is still produced. An unknown
// name still 404s.
func TestHandleGetClientConfig_ResolvesFromDB(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1", vpcCIDR: dbcVPCCIDR},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(dbcSrvKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	handler, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(),
		fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil, nil, nil,
		seededClientsSvc(t, db.Client{Name: dbcName, Address: dbcAddr, PublicKey: dbcPubKey}), "local", nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/clients/"+dbcName+"/config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"[Interface]",
		"Address = " + dbcAddr,
		"PublicKey = " + dbcSrvKey,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("config body missing %q\n--- body ---\n%s", want, body)
		}
	}

	// A name only present in the (empty) clients.json baseline but not the DB
	// must 404 — proving the lookup is the DB, not the manifest.
	req404 := httptest.NewRequest(http.MethodGet, "/api/clients/ghost/config", nil)
	rec404 := httptest.NewRecorder()
	handler.ServeHTTP(rec404, req404)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("ghost status = %d, want 404", rec404.Code)
	}
}

// TestHandleGetPartialClientDetail_ResolvesFromDB proves the expand-panel
// fragment (spec 020 Slice 1, A1) resolves a UI-added client — one that only
// ever exists in the runtime clients DB, never in the Terraform-rendered
// clients.json manifest — rather than 404ing. clientsfile.Load's seed is
// empty; only the DB (via seededClientsSvc) holds the client.
func TestHandleGetPartialClientDetail_ResolvesFromDB(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(dbcSrvKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	handler, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(),
		fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil, nil, nil,
		seededClientsSvc(t, db.Client{Name: dbcName, Address: dbcAddr, PublicKey: dbcPubKey}), "local", nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/clients/"+dbcPubKey+"/detail", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a DB-only/UI-added client must resolve, not 404); body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "client-chart-"+dbcPubKey) {
		t.Errorf("body missing the chart canvas for the DB-resolved pubkey\n--- body ---\n%s", rec.Body.String())
	}

	// An unknown pubkey — present in neither the DB nor the (empty) manifest —
	// must still 404.
	req404 := httptest.NewRequest(http.MethodGet, "/partial/clients/ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ=/detail", nil)
	rec404 := httptest.NewRecorder()
	handler.ServeHTTP(rec404, req404)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("unknown pubkey status = %d, want 404", rec404.Code)
	}
}

// TestHandleGetClientHistory_ResolvesFromDB proves GET /api/clients/{name}/history
// (spec 020 Slice 1, A1) resolves a UI-added client from the runtime DB rather
// than 404ing against the empty clients.json baseline — mirrors
// TestHandleGetClientConfig_ResolvesFromDB above for the history endpoint.
func TestHandleGetClientHistory_ResolvesFromDB(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(dbcSrvKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	handler, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(),
		fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil, nil, nil,
		seededClientsSvc(t, db.Client{Name: dbcName, Address: dbcAddr, PublicKey: dbcPubKey}), "local", nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/clients/"+dbcName+"/history", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a DB-only/UI-added client must resolve, not 404); body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got.Name != dbcName || got.PublicKey != dbcPubKey {
		t.Errorf("name/pubkey = %q/%q, want %q/%q", got.Name, got.PublicKey, dbcName, dbcPubKey)
	}

	// A name only present in the (empty) clients.json baseline but not the DB
	// must still 404.
	req404 := httptest.NewRequest(http.MethodGet, "/api/clients/ghost/history", nil)
	rec404 := httptest.NewRecorder()
	handler.ServeHTTP(rec404, req404)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("ghost status = %d, want 404", rec404.Code)
	}
}
