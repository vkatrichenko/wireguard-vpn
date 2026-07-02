package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

// offAWSInfoSvc builds a serverinfo.Service that reports off-AWS: the EC2 probe
// fails (so onEC2 is false) and the public IP comes from the echo client. The
// IMDS seam returns an error to prove the EC2-only methods short-circuit before
// touching it.
func offAWSInfoSvc(echoIP, key string) *serverinfo.Service {
	return &serverinfo.Service{
		IMDS:     fakeIMDS{err: errContextDeadline},
		EC2Probe: func(context.Context) error { return serverinfo.ErrNotOnEC2 },
		Echo:     func(context.Context) (string, error) { return echoIP, nil },
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(key + "\n"), nil
		},
	}
}

// errContextDeadline mimics the off-AWS IMDS failure mode (a timeout) so a
// regression that drops the short-circuit would surface this error in the card.
var errContextDeadline = context.DeadlineExceeded

// TestHandleGetPartialAbout_OffAWS proves the About tab renders the calm
// "Not running on EC2" state with "—" placeholders — NOT a red error box — and
// that the server-endpoint card still shows the echo-resolved public IP.
func TestHandleGetPartialAbout_OffAWS(t *testing.T) {
	const (
		echoIP = "192.0.2.50"
		key    = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
	)
	infoSvc := offAWSInfoSvc(echoIP, key)
	systemdSvc := systemdRunnerActive(time.Now().Add(-time.Hour))

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil, nil, emptyClientsSvc(t), "local")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/partial/about", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Not running on EC2.") {
		t.Errorf("About off-AWS missing the calm 'Not running on EC2.' note\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "Failed to load EC2 metadata") {
		t.Errorf("About off-AWS rendered an EC2 error box; want calm state\n--- body ---\n%s", body)
	}
	// The server-endpoint card (sourced from Get -> echo) must still show the IP.
	if !strings.Contains(body, echoIP) {
		t.Errorf("About off-AWS missing the echo-resolved public IP %q\n--- body ---\n%s", echoIP, body)
	}
}

// TestHandleIndex_OffAWS proves the Overview server-endpoint card shows the
// echo-resolved public IP and does NOT render the red IMDS error box on a
// non-AWS host.
func TestHandleIndex_OffAWS(t *testing.T) {
	const (
		echoIP = "192.0.2.77"
		key    = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="
	)
	infoSvc := offAWSInfoSvc(echoIP, key)
	systemdSvc := systemdRunnerActive(time.Now().Add(-time.Hour))

	handler, err := server.New(dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(), fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil, nil, emptyClientsSvc(t), "local")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="server-info"`) {
		t.Errorf("Overview off-AWS missing the server-info card\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, echoIP) {
		t.Errorf("Overview off-AWS missing the echo-resolved public IP %q\n--- body ---\n%s", echoIP, body)
	}
	if strings.Contains(body, "context deadline exceeded") {
		t.Errorf("Overview off-AWS surfaced an IMDS error; want clean card\n--- body ---\n%s", body)
	}
}
