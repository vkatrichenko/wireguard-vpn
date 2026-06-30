package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/alerts"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

// newAlertServer builds a handler wired with the given status holder (which may
// be nil to exercise the disabled/empty path). All other deps are the package's
// existing fakes — these tests only touch the alert surfaces.
func newAlertServer(t *testing.T, holder *alerts.StatusHolder) http.Handler {
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
		fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		holder,
		nil,
		nil,
		emptyClientsSvc(t),
	)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return handler
}

// alertsResponse mirrors the /api/alerts JSON shape for decoding in tests.
type alertsResponse struct {
	Enabled bool `json:"enabled"`
	Active  []struct {
		Condition string    `json:"condition"`
		Key       string    `json:"key"`
		Detail    string    `json:"detail"`
		Since     time.Time `json:"since"`
	} `json:"active"`
	Recent []struct {
		Condition string    `json:"condition"`
		Key       string    `json:"key"`
		Kind      string    `json:"kind"`
		Detail    string    `json:"detail"`
		At        time.Time `json:"at"`
	} `json:"recent"`
}

func getAlerts(t *testing.T, handler http.Handler) (int, alertsResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/alerts", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	var resp alertsResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode /api/alerts: %v (body=%s)", err, body)
		}
	}
	return rec.Code, resp
}

func TestGetAlerts_NilHolderEmptyJSON(t *testing.T) {
	handler := newAlertServer(t, nil)
	code, resp := getAlerts(t, handler)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if resp.Enabled {
		t.Fatalf("nil holder should report enabled=false")
	}
	if len(resp.Active) != 0 || len(resp.Recent) != 0 {
		t.Fatalf("nil holder should report empty active/recent, got %+v", resp)
	}
}

func TestGetAlerts_DisabledEmpty(t *testing.T) {
	holder := alerts.NewStatusHolder() // enabled defaults false, nothing firing
	handler := newAlertServer(t, holder)

	code, resp := getAlerts(t, handler)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if resp.Enabled {
		t.Fatalf("want disabled")
	}
	if len(resp.Active) != 0 {
		t.Fatalf("want no active alerts, got %+v", resp.Active)
	}
}

func TestGetAlerts_EnabledFiring(t *testing.T) {
	holder := alerts.NewStatusHolder()
	holder.SetEnabled(true)
	since := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	holder.Update(
		[]alerts.ActiveAlert{{
			Condition: alerts.ConditionServiceDown,
			Key:       "service-down",
			Detail:    "wg-quick@wg0 not active",
			Since:     since,
		}},
		[]alerts.Event{{
			Condition: alerts.ConditionServiceDown,
			Key:       "service-down",
			Kind:      alerts.Fire,
			Detail:    "wg-quick@wg0 not active",
			At:        since,
		}},
	)
	handler := newAlertServer(t, holder)

	code, resp := getAlerts(t, handler)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if !resp.Enabled {
		t.Fatalf("want enabled")
	}
	if len(resp.Active) != 1 || resp.Active[0].Key != "service-down" {
		t.Fatalf("want one firing service-down, got %+v", resp.Active)
	}
	if !resp.Active[0].Since.Equal(since) {
		t.Fatalf("Since = %v, want %v", resp.Active[0].Since, since)
	}
	if len(resp.Recent) != 1 || resp.Recent[0].Kind != "FIRING" {
		t.Fatalf("want one FIRING in recent, got %+v", resp.Recent)
	}
}

// externalURLRe matches an http(s):// resource reference — used to assert the
// alert markup adds no external CDN/font dependency (offline constraint).
var externalURLRe = regexp.MustCompile(`https?://`)

func TestOverviewStrip_FiringRendersAlerts(t *testing.T) {
	holder := alerts.NewStatusHolder()
	holder.SetEnabled(true)
	holder.Update([]alerts.ActiveAlert{{
		Condition: alerts.ConditionHighDisk,
		Key:       "high-disk",
		Detail:    "/ at 95.0%",
		Since:     time.Now().Add(-3 * time.Minute),
	}}, nil)
	handler := newAlertServer(t, holder)

	body := getBody(t, handler, "/partial/overview")
	if !strings.Contains(body, "high-disk") {
		t.Fatalf("overview strip should name the firing condition; body=%s", body)
	}
	if !strings.Contains(body, "/ at 95.0%") {
		t.Fatalf("overview strip should show the alert detail; body=%s", body)
	}
	if !strings.Contains(body, "firing since") {
		t.Fatalf("overview strip should show 'firing since'; body=%s", body)
	}
	if externalURLRe.MatchString(alertMarkup(body)) {
		t.Fatalf("overview alert markup must not reference external URLs; body=%s", body)
	}
}

func TestOverviewStrip_EmptyState(t *testing.T) {
	holder := alerts.NewStatusHolder()
	holder.SetEnabled(true) // enabled but nothing firing
	handler := newAlertServer(t, holder)

	body := getBody(t, handler, "/partial/overview")
	if !strings.Contains(body, "No active alerts.") {
		t.Fatalf("want neutral empty state; body=%s", body)
	}
	if strings.Contains(body, "Alerting is not configured") {
		t.Fatalf("enabled holder should not show the disabled hint; body=%s", body)
	}
}

func TestOverviewStrip_DisabledHint(t *testing.T) {
	holder := alerts.NewStatusHolder() // disabled
	handler := newAlertServer(t, holder)

	body := getBody(t, handler, "/partial/overview")
	if !strings.Contains(body, "Alerting is not configured") {
		t.Fatalf("disabled holder should show the not-configured hint; body=%s", body)
	}
}

func TestEventsTab_RendersRecentAlerts(t *testing.T) {
	holder := alerts.NewStatusHolder()
	holder.SetEnabled(true)
	at := time.Now().Add(-90 * time.Second)
	holder.Update(nil, []alerts.Event{{
		Condition: alerts.ConditionServiceDown,
		Key:       "service-down",
		Kind:      alerts.Fire,
		Detail:    "wg-quick@wg0 not active",
		At:        at,
	}})
	handler := newAlertServer(t, holder)

	body := getBody(t, handler, "/partial/events")
	if !strings.Contains(body, "Recent alerts") {
		t.Fatalf("events tab should have a Recent alerts section; body=%s", body)
	}
	if !strings.Contains(body, "FIRING") || !strings.Contains(body, "service-down") {
		t.Fatalf("events tab should render the alert transition; body=%s", body)
	}
	if !strings.Contains(body, "wg-quick@wg0 not active") {
		t.Fatalf("events tab should show the alert detail; body=%s", body)
	}
}

func TestEventsTab_EmptyAlertState(t *testing.T) {
	holder := alerts.NewStatusHolder()
	handler := newAlertServer(t, holder)

	body := getBody(t, handler, "/partial/events")
	if !strings.Contains(body, "No recent alerts.") {
		t.Fatalf("events tab should show the empty alert state; body=%s", body)
	}
}

func getBody(t *testing.T, handler http.Handler, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: want 200, got %d", path, rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

// alertMarkup extracts just the active-alerts <section> so the external-URL
// assertion checks only the new fragment, not unrelated card markup that may
// legitimately carry a datetime attribute. Falls back to the whole body if the
// section markers aren't found.
func alertMarkup(body string) string {
	start := strings.Index(body, `id="active-alerts"`)
	if start < 0 {
		return body
	}
	rest := body[start:]
	end := strings.Index(rest, "</section>")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
