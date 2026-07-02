package server_test

// Tests for spec 018 Slice 1: CLIENT_MANAGEMENT_MODE is a COSMETIC-ONLY gate.
// "cloud" hides every client-mutating control (add form, edit toggle, remove,
// enable/disable) and the drift badge in both the Clients-tab partial and the
// client-count Overview card; it must NOT 403 or otherwise block any handler,
// and computeDrift must be skipped entirely (not computed-and-hidden) so
// Drift stays exactly 0. "local" (the default, and the zero value of an
// unset mode) must render byte-identical to today's markup — proven by the
// existing (unmodified) tests in handlers_clients_admin_test.go and
// handlers_clients_drift_test.go all still passing with mode="local" wired
// through newClientsAdminServer's delegation to newClientsAdminServerMode.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/clients"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

// addFormPresent / editTogglePresent / removeButtonPresent / enableDisablePresent
// key off markup that only the add/edit/enable-disable/remove controls emit —
// mirrors the driftBadgePresent idiom in handlers_clients_drift_test.go
// (exact rendered copy/attribute, not a loose substring).
func addFormPresent(body string) bool {
	return strings.Contains(body, `class="client-add-form"`)
}

func editTogglePresent(body string) bool {
	return strings.Contains(body, `class="client-btn client-edit-toggle"`)
}

func removeButtonPresent(body string) bool {
	return strings.Contains(body, `class="client-btn client-btn-danger"`)
}

func enableDisablePresent(body string) bool {
	return strings.Contains(body, `hx-vals='{"enabled":"false"}'`) ||
		strings.Contains(body, `hx-vals='{"enabled":"true"}'`)
}

// clientCountDriftPresent mirrors driftBadgePresent's exact-copy assertion but
// for the client-count card's shorter "N drift" label (cards/client-count.html
// uses "drift", the clients-tab badge uses "diverged from git-managed set").
func clientCountDriftPresent(body string, count int) bool {
	return strings.Contains(body, "drift-badge") && strings.Contains(body, fmt.Sprintf("%d drift", count))
}

func getOverviewPartial(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/partial/overview", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /partial/overview: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestCloudMode_ClientsTab_ControlsGatedCosmetically proves the Clients-tab
// partial hides add/edit/remove/enable-disable in cloud mode while keeping the
// read-only list intact, and shows them in local mode exactly as before. The
// add-then-render sequence forces a real client row to exist so the per-row
// controls (edit toggle, remove, enable/disable) have something to attach to
// in both modes — a mode gate that only worked on an empty list would be a
// false pass.
func TestCloudMode_ClientsTab_ControlsGatedCosmetically(t *testing.T) {
	cases := []struct {
		mode           string
		wantAddForm    bool
		wantEditToggle bool
		wantRemove     bool
		wantEnableDis  bool
	}{
		{"local", true, true, true, true},
		{"cloud", false, false, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			h, _, _ := newClientsAdminServerMode(t, tc.mode)
			addOne(t, h, "alice", adminKeyA)

			frag := getClientsPartial(t, h)

			if got := addFormPresent(frag); got != tc.wantAddForm {
				t.Errorf("mode=%s: addFormPresent = %v, want %v\n%s", tc.mode, got, tc.wantAddForm, frag)
			}
			if got := editTogglePresent(frag); got != tc.wantEditToggle {
				t.Errorf("mode=%s: editTogglePresent = %v, want %v\n%s", tc.mode, got, tc.wantEditToggle, frag)
			}
			if got := removeButtonPresent(frag); got != tc.wantRemove {
				t.Errorf("mode=%s: removeButtonPresent = %v, want %v\n%s", tc.mode, got, tc.wantRemove, frag)
			}
			if got := enableDisablePresent(frag); got != tc.wantEnableDis {
				t.Errorf("mode=%s: enableDisablePresent = %v, want %v\n%s", tc.mode, got, tc.wantEnableDis, frag)
			}

			// The list itself must ALWAYS stay visible and read-only, regardless
			// of mode — cloud mode hides mutating controls, not the data.
			if !strings.Contains(frag, "alice") {
				t.Errorf("mode=%s: client row for alice missing from read-only list\n%s", tc.mode, frag)
			}
		})
	}
}

// TestCloudMode_ClientsTab_CloudNote proves cloud mode renders the optional
// static note in place of the add form, per the spec's "Peers are
// Terraform-managed" suggestion — and that local mode does NOT render it
// (byte-identical to today's markup).
func TestCloudMode_ClientsTab_CloudNote(t *testing.T) {
	h, _, _ := newClientsAdminServerMode(t, "cloud")
	frag := getClientsPartial(t, h)
	if !strings.Contains(frag, "Terraform-managed") {
		t.Fatalf("cloud mode: expected a Terraform-managed note in place of the add form:\n%s", frag)
	}

	hLocal, _, _ := newClientsAdminServerMode(t, "local")
	fragLocal := getClientsPartial(t, hLocal)
	if strings.Contains(fragLocal, "Terraform-managed") {
		t.Fatalf("local mode: must not render the cloud-mode note:\n%s", fragLocal)
	}
}

// TestCloudMode_DriftBadge_HiddenInBothSurfaces proves the drift badge is
// hidden cosmetically in BOTH the Clients-tab heading and the Overview
// client-count card in cloud mode, and visible in local mode — while a raw
// GET /api/clients (the underlying data endpoint) keeps working unmodified in
// either mode, proving the gate is presentation-only.
func TestCloudMode_DriftBadge_HiddenInBothSurfaces(t *testing.T) {
	cases := []struct {
		mode      string
		wantDrift bool
	}{
		{"local", true},
		{"cloud", false},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			h, _, _ := newClientsAdminServerMode(t, tc.mode)
			// No PUT has ever run: computeDrift falls back to the clients.json
			// seed (empty, via fakeClientsfileSvc), so any enabled DB client
			// counts as drift in local mode — exactly the fixture
			// TestComputeDrift_EmptyBaselineFallsBackToClientsfile uses.
			addOne(t, h, "alice", adminKeyA)

			clientsFrag := getClientsPartial(t, h)
			if got := driftBadgePresent(clientsFrag, 1); got != tc.wantDrift {
				t.Errorf("mode=%s: clients-tab driftBadgePresent(1) = %v, want %v\n%s", tc.mode, got, tc.wantDrift, clientsFrag)
			}

			overviewFrag := getOverviewPartial(t, h)
			if got := clientCountDriftPresent(overviewFrag, 1); got != tc.wantDrift {
				t.Errorf("mode=%s: client-count driftPresent(1) = %v, want %v\n%s", tc.mode, got, tc.wantDrift, overviewFrag)
			}

			// The gate is cosmetic only: the write/read API keeps functioning
			// identically regardless of mode.
			if names := listNames(t, h); !names["alice"] {
				t.Errorf("mode=%s: GET /api/clients must still work unmodified, got %v", tc.mode, names)
			}
		})
	}
}

// countingClientsfileSvc wraps fakeClientsfileSvc's empty-manifest behaviour
// but counts Reader invocations. computeDrift's empty-baseline fallback path
// calls clientsfileSvc.Load (which calls Reader) exactly when it runs — so a
// zero count after a render proves computeDrift was never entered, not just
// that its result was hidden by the template guard.
func countingClientsfileSvc(calls *atomic.Int64) *clientsfile.Service {
	return &clientsfile.Service{
		Reader: func(string) ([]byte, error) {
			calls.Add(1)
			return []byte("[]"), nil
		},
		Path: "/test/clients.json",
	}
}

// newClientsAdminServerModeCounting is newClientsAdminServerMode's twin, built
// standalone (rather than extending the shared helper) because only this test
// needs the instrumented clientsfileSvc — threading a *atomic.Int64 through
// the shared helper's signature would force every other call site to pass nil.
func newClientsAdminServerModeCounting(t *testing.T, mode string, calls *atomic.Int64) http.Handler {
	t.Helper()
	database := newTestDB(t)
	svc := clients.NewService(database, "172.16.15.1/24")
	svc.SetApplier(&recordingApplier{})

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	h, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, countingClientsfileSvc(calls), fakeWgSvc(),
		fakeProcSvc(), database, nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil, nil, nil, svc, mode)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return h
}

// TestCloudMode_ComputeDriftSkipped_NotJustHidden proves cloud mode SKIPS the
// computeDrift call rather than computing it and hiding the result via the
// template guard. It seeds the same empty-baseline-fallback fixture as
// TestComputeDrift_EmptyBaselineFallsBackToClientsfile (which proves drift=1
// in local mode) and counts clientsfileSvc.Reader invocations — computeDrift's
// fallback path is the only thing in the render that calls it. A
// template-only guard would still invoke Reader (and waste the read) on every
// render; only an actual `if !s.cloudMode` skip keeps the count at zero.
func TestCloudMode_ComputeDriftSkipped_NotJustHidden(t *testing.T) {
	var localCalls, cloudCalls atomic.Int64

	hLocal := newClientsAdminServerModeCounting(t, "local", &localCalls)
	addOne(t, hLocal, "alice", adminKeyA)
	getClientsPartial(t, hLocal)
	if localCalls.Load() == 0 {
		t.Fatalf("local mode: expected clientsfileSvc.Reader to be called (computeDrift's fallback path), got 0 calls")
	}

	hCloud := newClientsAdminServerModeCounting(t, "cloud", &cloudCalls)
	addOne(t, hCloud, "alice", adminKeyA)
	getClientsPartial(t, hCloud)
	if got := cloudCalls.Load(); got != 0 {
		t.Fatalf("cloud mode: computeDrift must be skipped entirely, but clientsfileSvc.Reader was called %d time(s)", got)
	}
}
