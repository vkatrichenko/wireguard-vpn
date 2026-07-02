package clients

import (
	"context"
	"errors"
	"sync"
	"testing"

	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/clientstore"
)

// fakeStore is the injectable clientstore.Store double used by every test in
// this file — no real AWS, no `aws` CLI, matching spec 018 Slice 4's "no live
// AWS in tests" constraint. loadEntries/loadErr control what Load returns;
// every Save call is recorded so a test can assert both "did it write" and
// "what did it write" (the exact canonical projection).
type fakeStore struct {
	mu sync.Mutex

	loadEntries []clientstore.Entry
	loadErr     error

	saveCalls int
	lastSave  []clientstore.Entry
	saveErr   error
}

func (f *fakeStore) Load(context.Context) ([]clientstore.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadEntries, f.loadErr
}

func (f *fakeStore) Save(_ context.Context, entries []clientstore.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saveCalls++
	f.lastSave = append([]clientstore.Entry(nil), entries...)
	return f.saveErr
}

func (f *fakeStore) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saveCalls
}

// --- ReconcileFromStore: boot-load-applies -----------------------------------

func TestReconcileFromStore_LoadsAndAppliesFromS3(t *testing.T) {
	svc, database := newTestService(t)
	ctx := context.Background()
	rec := &recordingApplier{}
	svc.SetApplier(rec)
	store := &fakeStore{loadEntries: []clientstore.Entry{
		{Name: "alice", Address: "172.16.15.5/32", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		{Name: "bob", Address: "172.16.15.6/32", PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="},
	}}
	svc.SetStore(store)

	if err := svc.ReconcileFromStore(ctx, nil); err != nil {
		t.Fatalf("ReconcileFromStore: %v", err)
	}

	got, err := database.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(clients) = %d, want 2 (got %+v)", len(got), got)
	}
	if rec.calls != 1 {
		t.Errorf("applier.calls = %d, want 1 (live apply must run after loading from S3)", rec.calls)
	}
}

// --- ReconcileFromStore: 404-seeds -------------------------------------------

func TestReconcileFromStore_NotFoundSeedsStoreFromLocalBootSeed(t *testing.T) {
	svc, database := newTestService(t)
	ctx := context.Background()
	rec := &recordingApplier{}
	svc.SetApplier(rec)
	store := &fakeStore{loadErr: clientstore.ErrNotFound}
	svc.SetStore(store)

	localSeed := []clientsfile.Client{
		{Name: "carol", Address: "172.16.15.7/32", PublicKey: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="},
	}

	if err := svc.ReconcileFromStore(ctx, localSeed); err != nil {
		t.Fatalf("ReconcileFromStore: %v", err)
	}

	if store.calls() != 1 {
		t.Fatalf("store.saveCalls = %d, want 1 (a 404 must cold-seed the store from the local boot seed)", store.calls())
	}
	if len(store.lastSave) != 1 || store.lastSave[0].Name != "carol" {
		t.Errorf("seeded store content = %+v, want the local boot seed", store.lastSave)
	}

	got, err := database.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 1 || got[0].Name != "carol" {
		t.Fatalf("db clients = %+v, want the local boot seed imported", got)
	}
	if rec.calls != 1 {
		t.Errorf("applier.calls = %d, want 1 (the 404 branch must still apply live)", rec.calls)
	}
}

// --- ReconcileFromStore: non-404-fails-loud-no-clobber -----------------------

func TestReconcileFromStore_OtherErrorFailsLoudWithoutClobberingDB(t *testing.T) {
	svc, database := newTestService(t)
	ctx := context.Background()

	// Pre-populate the DB with a runtime client — this represents "whatever
	// the box already had" before a boot attempt hits an S3 outage. It must
	// survive untouched.
	if _, err := svc.Add(ctx, AddParams{
		Name:      "existing",
		PublicKey: "EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE=",
	}); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	rec := &recordingApplier{}
	svc.SetApplier(rec)
	rec.calls = 0 // reset: the seeding Add above already invoked the applier once

	wantErr := errors.New("s3: connection reset")
	store := &fakeStore{loadErr: wantErr}
	svc.SetStore(store)

	err := svc.ReconcileFromStore(ctx, []clientsfile.Client{
		{Name: "shouldnotimport", Address: "172.16.15.9/32", PublicKey: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF="},
	})
	if err == nil {
		t.Fatal("ReconcileFromStore err = nil, want a propagated error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("ReconcileFromStore err = %v, want it to wrap %v", err, wantErr)
	}

	got, listErr := database.ListClients(ctx)
	if listErr != nil {
		t.Fatalf("ListClients: %v", listErr)
	}
	if len(got) != 1 || got[0].Name != "existing" {
		t.Fatalf("db clients = %+v, want ONLY the pre-existing client (no clobber, no local-seed import)", got)
	}
	if store.calls() != 0 {
		t.Errorf("store.saveCalls = %d, want 0 (a non-404 error must not seed/write the store)", store.calls())
	}
	if rec.calls != 0 {
		t.Errorf("applier.calls = %d, want 0 (a non-404 error must not re-apply live config)", rec.calls)
	}
}

// --- write-through on mutations ----------------------------------------------

func TestAdd_WritesThroughToStore(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.SetApplier(&recordingApplier{})
	store := &fakeStore{}
	svc.SetStore(store)

	if _, err := svc.Add(ctx, AddParams{Name: "alice", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if store.calls() != 1 {
		t.Fatalf("store.saveCalls = %d, want 1", store.calls())
	}
	if len(store.lastSave) != 1 || store.lastSave[0].Name != "alice" || store.lastSave[0].Address != "172.16.15.2/32" {
		t.Errorf("saved entries = %+v, want the new client", store.lastSave)
	}
}

func TestDelete_WritesThroughToStore(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.SetApplier(&recordingApplier{})
	store := &fakeStore{}
	svc.SetStore(store)

	if _, err := svc.Add(ctx, AddParams{Name: "alice", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	before := store.calls()

	if err := svc.Delete(ctx, "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if store.calls() != before+1 {
		t.Fatalf("store.saveCalls after Delete = %d, want %d", store.calls(), before+1)
	}
	if len(store.lastSave) != 0 {
		t.Errorf("saved entries after Delete = %+v, want empty", store.lastSave)
	}
}

func TestReplaceAll_WritesThroughToStore(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.SetApplier(&recordingApplier{})
	store := &fakeStore{}
	svc.SetStore(store)

	entries := []ReplaceEntry{
		{Name: "alice", Address: "172.16.15.5/32", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
	}
	if _, err := svc.ReplaceAll(ctx, entries); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}

	if store.calls() != 1 {
		t.Fatalf("store.saveCalls = %d, want 1", store.calls())
	}
	if len(store.lastSave) != 1 || store.lastSave[0].Name != "alice" {
		t.Errorf("saved entries = %+v, want %+v", store.lastSave, entries)
	}
}

func TestUpdate_CanonicalFieldChange_WritesThroughToStore(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.SetApplier(&recordingApplier{})
	if _, err := svc.Add(ctx, AddParams{Name: "alice", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store := &fakeStore{}
	svc.SetStore(store)

	newName := "alice2"
	if _, err := svc.Update(ctx, "alice", UpdateParams{Name: &newName}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if store.calls() != 1 {
		t.Fatalf("store.saveCalls = %d, want 1 (a name change is a canonical-field edit)", store.calls())
	}
	if len(store.lastSave) != 1 || store.lastSave[0].Name != "alice2" {
		t.Errorf("saved entries = %+v, want the renamed client", store.lastSave)
	}
}

// --- enable/disable-excluded --------------------------------------------------

func TestUpdate_EnabledOnly_DoesNotWriteThroughToStore(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.SetApplier(&recordingApplier{})
	if _, err := svc.Add(ctx, AddParams{Name: "alice", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store := &fakeStore{}
	svc.SetStore(store)

	disabled := false
	updated, err := svc.Update(ctx, "alice", UpdateParams{Enabled: &disabled})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Enabled {
		t.Fatalf("updated.Enabled = true, want false")
	}

	if store.calls() != 0 {
		t.Errorf("store.saveCalls = %d, want 0 (enable/disable must NOT write through — `enabled` is excluded from the S3 bridge)", store.calls())
	}
}

func TestUpdate_NoteOnly_DoesNotWriteThroughToStore(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.SetApplier(&recordingApplier{})
	if _, err := svc.Add(ctx, AddParams{Name: "alice", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store := &fakeStore{}
	svc.SetStore(store)

	note := "a note"
	if _, err := svc.Update(ctx, "alice", UpdateParams{Note: &note}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if store.calls() != 0 {
		t.Errorf("store.saveCalls = %d, want 0 (note is not part of the canonical projection)", store.calls())
	}
}

// --- default (unwired) Service behaves exactly like pre-Slice-4 -------------

func TestService_WithoutSetStore_MutationsAreNoOps(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.SetApplier(&recordingApplier{})

	// No SetStore call — svc.store is the default clientstore.NoopStore{}.
	// Add must still succeed (Save is a genuine no-op), matching local mode /
	// every pre-Slice-4 caller exactly.
	if _, err := svc.Add(ctx, AddParams{Name: "alice", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("Add without SetStore: %v", err)
	}
}
