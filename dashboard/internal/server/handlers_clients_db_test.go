package server_test

import (
	"context"
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

// TestHandleGetPartialClients_FromDB proves the client list now renders from the
// runtime DB (spec 015), not clients.json: with an EMPTY clients.json baseline
// but a DB-seeded client, the row still renders — and because the seeded client
// is absent from the (empty) baseline, the drift badge reports 1.
func TestHandleGetPartialClients_FromDB(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(dbcSrvKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	// clientsfileSvc returns an empty baseline; the DB holds the only client.
	handler, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(),
		fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil, nil, nil,
		seededClientsSvc(t, db.Client{Name: dbcName, Address: dbcAddr, PublicKey: dbcPubKey}),
	)
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
		dbcName,                           // DB-sourced row rendered
		dbcAddr,                           // its tunnel address
		`id="clients-drift"`,              // drift badge element present
		"1 diverged from git-managed set", // exactly one drifted client
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
	if strings.Contains(body, "No clients configured") {
		t.Errorf("body unexpectedly contains empty-state copy:\n%s", body)
	}
}

// TestHandleGetPartialClients_NoDriftWhenSeeded proves the drift badge is hidden
// when every DB client's public key IS present in the clients.json baseline.
func TestHandleGetPartialClients_NoDriftWhenSeeded(t *testing.T) {
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(dbcSrvKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	// Baseline manifest contains the same client → no drift.
	clientsfileSvc := seededClientsfileSvc(dbcName, dbcAddr, dbcPubKey)
	handler, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, clientsfileSvc, fakeWgSvc(),
		fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil, nil, nil,
		seededClientsSvc(t, db.Client{Name: dbcName, Address: dbcAddr, PublicKey: dbcPubKey}),
	)
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
	if !strings.Contains(body, dbcName) {
		t.Errorf("body missing client name %q:\n%s", dbcName, body)
	}
	if strings.Contains(body, `id="clients-drift"`) {
		t.Errorf("drift badge unexpectedly rendered with a fully-seeded baseline:\n%s", body)
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
		seededClientsSvc(t, db.Client{Name: dbcName, Address: dbcAddr, PublicKey: dbcPubKey}),
	)
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
