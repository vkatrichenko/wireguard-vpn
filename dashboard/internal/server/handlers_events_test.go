package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/clients"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

// newEventsServer builds an Events-tab handler wired with a caller-supplied
// metricsDB (so the test can seed handshake_events) and clientsSvc (so the test
// controls the public_key → name resolution). All other deps are the package's
// existing fakes — this test only exercises the handshakes surface.
func newEventsServer(t *testing.T, metricsDB *db.DB, clientsSvc *clients.Service) http.Handler {
	t.Helper()
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))
	handler, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(),
		fakeProcSvc(), metricsDB, nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil,
		nil,
		nil,
		clientsSvc, "local", nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return handler
}

// TestEventsTab_ResolvesNamesAndDedupes pins spec 016 (2.3): a handshake from a
// known public key renders the live client name (not the stored/raw key),
// repeated handshakes for that key collapse to one row, and a handshake from an
// unknown key renders a shortened-key "unknown" fallback rather than being
// hidden.
func TestEventsTab_ResolvesNamesAndDedupes(t *testing.T) {
	const (
		knownKey   = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAk="
		unknownKey = "ZZZZZZZZZZqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq="
	)
	ctx := context.Background()
	metricsDB := newTestDB(t)

	now := time.Now()
	// Two handshakes for the known peer (must collapse to one row) plus one for
	// an unknown peer. The stored name is intentionally the raw key for the
	// known peer to prove resolution moves to render time.
	if err := metricsDB.InsertHandshakeEvents(ctx, []db.HandshakeEvent{
		{TS: now.Add(-5 * time.Minute), PublicKey: knownKey, Name: knownKey},
		{TS: now.Add(-1 * time.Minute), PublicKey: knownKey, Name: knownKey},
		{TS: now.Add(-2 * time.Minute), PublicKey: unknownKey, Name: unknownKey},
	}); err != nil {
		t.Fatalf("InsertHandshakeEvents: %v", err)
	}

	clientsSvc := seededClientsSvc(t, db.Client{Name: "alice", Address: "172.16.15.2/32", PublicKey: knownKey})
	handler := newEventsServer(t, metricsDB, clientsSvc)

	body := getBody(t, handler, "/partial/events")

	// Known key resolves to the client name, exactly once (dedupe).
	if n := strings.Count(body, "alice"); n != 1 {
		t.Errorf("client name should appear exactly once (got %d); body=%s", n, body)
	}
	// The raw known key never leaks into the rendered handshakes.
	if strings.Contains(body, knownKey) {
		t.Errorf("raw known key should be resolved away; body=%s", body)
	}
	// Unknown key renders the shortened prefix + the "unknown" marker, and the
	// full key (its tail) is not shown.
	if !strings.Contains(body, "ZZZZZZZZZZ") {
		t.Errorf("unknown peer should render the shortened key prefix; body=%s", body)
	}
	if !strings.Contains(body, "unknown") {
		t.Errorf("unknown peer should carry an 'unknown' marker; body=%s", body)
	}
	if strings.Contains(body, unknownKey) {
		t.Errorf("unknown peer should render shortened, not the full key; body=%s", body)
	}
}
