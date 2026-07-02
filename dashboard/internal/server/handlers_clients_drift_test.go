package server_test

// Tests for spec 017 Slice 3: computeDrift repointed from the boot
// clients.json seed to the dashboard-owned managed_baseline table, written
// atomically by clients.Service.ReplaceAll on every successful
// PUT /api/clients. Reuses the newClientsAdminServer harness (same package,
// handlers_clients_admin_test.go) so these tests drive the real PUT/PATCH/POST
// handlers rather than poking the DB directly — the point is to prove the
// end-to-end contract an operator (or Terraform) actually observes.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// driftBadgePresent reports whether the Clients-tab partial renders the drift
// badge with exactly the given count, via its exact rendered copy — pins both
// the count and the spec 017 relabel ("diverged from git-managed set") in one
// assertion.
func driftBadgePresent(body string, count int) bool {
	return strings.Contains(body, fmt.Sprintf(`id="clients-drift"`)) &&
		strings.Contains(body, fmt.Sprintf("%d diverged from git-managed set", count))
}

// getClientsPartial fetches the Clients-tab fragment, the same route the
// htmx 10s tick hits, so drift assertions read the exact markup an operator
// would see.
func getClientsPartial(t *testing.T, h http.Handler) string {
	t.Helper()
	code, body := doReq(t, h, http.MethodGet, "/partial/clients", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /partial/clients: want 200, got %d (%s)", code, body)
	}
	return body
}

// TestComputeDrift_ZeroAfterReplaceAll proves the core Slice 3 contract: right
// after a PUT /api/clients succeeds, the live set exactly equals what was just
// applied, so drift against the freshly-written managed_baseline is zero — no
// badge at all.
func TestComputeDrift_ZeroAfterReplaceAll(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)

	payload := putClientsPayload(
		[3]string{"alice", "172.16.15.10/32", adminKeyA},
		[3]string{"bob", "172.16.15.11/32", adminKeyB},
	)
	code, body := doReq(t, h, http.MethodPut, "/api/clients", strings.NewReader(payload), jsonHeaders())
	if code != http.StatusOK {
		t.Fatalf("PUT full set: want 200, got %d (%s)", code, body)
	}

	frag := getClientsPartial(t, h)
	if strings.Contains(frag, `id="clients-drift"`) {
		t.Fatalf("drift badge rendered right after ReplaceAll, want none:\n%s", frag)
	}
}

// TestComputeDrift_UIAddAfterReplaceAll proves a UI-only addition on top of a
// git-applied baseline registers as drift: PUT establishes {alice, bob} as the
// baseline, then a runtime POST adds carol (never declared in git) — carol's
// tuple isn't in the baseline, so drift must be 1.
func TestComputeDrift_UIAddAfterReplaceAll(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)

	payload := putClientsPayload([3]string{"alice", "172.16.15.10/32", adminKeyA})
	if code, body := doReq(t, h, http.MethodPut, "/api/clients", strings.NewReader(payload), jsonHeaders()); code != http.StatusOK {
		t.Fatalf("PUT baseline: want 200, got %d (%s)", code, body)
	}

	addOne(t, h, "carol", adminKeyB)

	frag := getClientsPartial(t, h)
	if !driftBadgePresent(frag, 1) {
		t.Fatalf("expected drift=1 (carol is UI-only) after PUT+POST:\n%s", frag)
	}
}

// TestComputeDrift_UIEditAfterReplaceAll proves an in-place UI edit of a
// git-managed peer registers as drift even though the public_key is unchanged
// — computeDrift must compare the full {name, address, public_key} tuple, not
// just public_key, or a rename/re-address would silently pass as "no drift".
func TestComputeDrift_UIEditAfterReplaceAll(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)

	payload := putClientsPayload([3]string{"alice", "172.16.15.10/32", adminKeyA})
	if code, body := doReq(t, h, http.MethodPut, "/api/clients", strings.NewReader(payload), jsonHeaders()); code != http.StatusOK {
		t.Fatalf("PUT baseline: want 200, got %d (%s)", code, body)
	}

	// Sanity: no drift immediately after the PUT.
	if frag := getClientsPartial(t, h); strings.Contains(frag, `id="clients-drift"`) {
		t.Fatalf("drift badge rendered right after ReplaceAll, want none:\n%s", frag)
	}

	// UI-style edit: rename alice -> alice2 (same public_key, same address).
	renamePayload := `{"name":"alice2"}`
	if code, body := doReq(t, h, http.MethodPatch, "/api/clients/alice", strings.NewReader(renamePayload), jsonHeaders()); code != http.StatusOK {
		t.Fatalf("PATCH rename: want 200, got %d (%s)", code, body)
	}

	frag := getClientsPartial(t, h)
	if !driftBadgePresent(frag, 1) {
		t.Fatalf("expected drift=1 after renaming a git-managed peer in the UI:\n%s", frag)
	}
}

// TestComputeDrift_EmptyBaselineFallsBackToClientsfile proves the pre-first-PUT
// fallback: with no baseline ever written (no PUT has succeeded), computeDrift
// falls back to comparing against the clients.json first-boot seed — the
// original spec-015 behaviour — so the badge still means something on a
// freshly-seeded box.
func TestComputeDrift_EmptyBaselineFallsBackToClientsfile(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)

	// No PUT has happened: managed_baseline is empty. addOne seeds a client
	// through the ordinary runtime path (POST /api/clients), which is exactly
	// what an operator-managed (non-Terraform) box looks like pre-first-apply.
	addOne(t, h, "alice", adminKeyA)

	// fakeClientsfileSvc (wired by newClientsAdminServer via server.New) returns
	// an empty manifest, so with an empty baseline AND an empty clients.json,
	// every enabled DB client counts as drift — proves the fallback path is
	// actually exercised, not just silently reporting zero.
	frag := getClientsPartial(t, h)
	if !driftBadgePresent(frag, 1) {
		t.Fatalf("expected drift=1 via clients.json fallback (empty baseline, empty seed, one DB client):\n%s", frag)
	}
}

// Spec 018 cloud-mode cosmetic-gating tests live in
// handlers_clients_cloud_mode_test.go (mode-parametrized coverage of the add
// form / edit toggle / remove / enable-disable / drift badge, plus the
// computeDrift-skipped assertion) — kept out of this file so drift-baseline
// tests (spec 017) and mode-gating tests (spec 018) stay independently
// readable.
