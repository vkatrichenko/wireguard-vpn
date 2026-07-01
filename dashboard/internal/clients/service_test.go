package clients

import (
	"context"
	"testing"

	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
)

// newTestService opens an in-memory DB scoped to t and wraps it in a Service
// using the default /24 overlay. Mirrors the db package's :memory: idiom — no
// on-disk fixtures.
func newTestService(t *testing.T) (*Service, *db.DB) {
	t.Helper()
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewService(database, "172.16.15.1/24"), database
}

// recordingApplier captures the last Apply call so tests can assert the live
// step ran with the expected client set (the seam Slice 4 fills in).
type recordingApplier struct {
	calls int
	last  []db.Client
	err   error
}

func (r *recordingApplier) Apply(_ context.Context, clients []db.Client) error {
	r.calls++
	r.last = clients
	return r.err
}

func TestSeed_ImportsOnEmpty(t *testing.T) {
	svc, database := newTestService(t)
	ctx := context.Background()

	seed := []clientsfile.Client{
		{Name: "alice", Address: "172.16.15.5/32", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		{Name: "bob", Address: "172.16.15.6/32", PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="},
	}
	if err := svc.Seed(ctx, seed); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	got, err := database.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(clients) = %d, want 2", len(got))
	}
	for _, c := range got {
		if !c.Enabled {
			t.Errorf("seeded client %q Enabled = false, want true", c.Name)
		}
		if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
			t.Errorf("seeded client %q has unstamped timestamps", c.Name)
		}
	}
}

func TestSeed_NoOpWhenPopulated(t *testing.T) {
	svc, database := newTestService(t)
	ctx := context.Background()

	// Pre-populate with a runtime-added client.
	if _, err := svc.Add(ctx, AddParams{
		Name:      "carol",
		PublicKey: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Seeding now must be a no-op — the table is non-empty.
	if err := svc.Seed(ctx, []clientsfile.Client{
		{Name: "alice", Address: "172.16.15.5/32", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
	}); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	got, err := database.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 1 || got[0].Name != "carol" {
		t.Fatalf("clients = %+v, want only the pre-existing carol", got)
	}
}

func TestAdd_AllocatesLowestFreeAddress(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// .1 is the reserved server IP; the first allocation must be .2.
	c1, err := svc.Add(ctx, AddParams{Name: "a", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="})
	if err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if c1.Address != "172.16.15.2/32" {
		t.Errorf("first address = %q, want 172.16.15.2/32", c1.Address)
	}

	c2, err := svc.Add(ctx, AddParams{Name: "b", PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="})
	if err != nil {
		t.Fatalf("Add b: %v", err)
	}
	if c2.Address != "172.16.15.3/32" {
		t.Errorf("second address = %q, want 172.16.15.3/32", c2.Address)
	}
}

func TestAdd_ManualOverrideAndConflicts(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Add(ctx, AddParams{
		Name:      "a",
		PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		Address:   "172.16.15.20/32",
	}); err != nil {
		t.Fatalf("Add with override: %v", err)
	}

	// Duplicate address.
	if _, err := svc.Add(ctx, AddParams{
		Name:      "b",
		PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		Address:   "172.16.15.20/32",
	}); err == nil {
		t.Error("Add with duplicate address: want error, got nil")
	}

	// Duplicate name.
	if _, err := svc.Add(ctx, AddParams{
		Name:      "a",
		PublicKey: "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD=",
	}); err == nil {
		t.Error("Add with duplicate name: want error, got nil")
	}

	// Duplicate public key.
	if _, err := svc.Add(ctx, AddParams{
		Name:      "c",
		PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}); err == nil {
		t.Error("Add with duplicate public key: want error, got nil")
	}

	// Invalid public key.
	if _, err := svc.Add(ctx, AddParams{Name: "d", PublicKey: "too-short"}); err == nil {
		t.Error("Add with invalid public key: want error, got nil")
	}
}

func TestAdd_InvokesApplier(t *testing.T) {
	svc, _ := newTestService(t)
	rec := &recordingApplier{}
	svc.SetApplier(rec)

	if _, err := svc.Add(context.Background(), AddParams{
		Name:      "a",
		PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("applier calls = %d, want 1", rec.calls)
	}
	if len(rec.last) != 1 || rec.last[0].Name != "a" {
		t.Errorf("applier last = %+v, want the single added client", rec.last)
	}
}

func TestUpdate_RenameNoteEnable(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Add(ctx, AddParams{Name: "a", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	newName := "alice"
	note := "vpn laptop"
	enabled := false
	updated, err := svc.Update(ctx, "a", UpdateParams{Name: &newName, Note: &note, Enabled: &enabled})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "alice" {
		t.Errorf("name = %q, want alice", updated.Name)
	}
	if !updated.Note.Valid || updated.Note.String != "vpn laptop" {
		t.Errorf("note = %+v, want {vpn laptop true}", updated.Note)
	}
	if updated.Enabled {
		t.Errorf("enabled = true, want false")
	}

	// Updating an unknown name is an error.
	if _, err := svc.Update(ctx, "ghost", UpdateParams{Note: &note}); err == nil {
		t.Error("Update unknown name: want error, got nil")
	}
}

// TestReplaceAll_ReconcilesAndAppliesOnce seeds one client via Add, then
// calls ReplaceAll with a set that drops it and adds two new ones. Asserts
// the resulting table matches the new set exactly, every replaced entry is
// Enabled=true (there is no enabled field in the bulk input), and the
// applier fired exactly once with the final set — not once per internal
// insert/update/delete.
func TestReplaceAll_ReconcilesAndAppliesOnce(t *testing.T) {
	svc, database := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Add(ctx, AddParams{
		Name:      "old",
		PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		Address:   "172.16.15.9/32",
	}); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	rec := &recordingApplier{}
	svc.SetApplier(rec)

	entries := []ReplaceEntry{
		{Name: "alice", PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=", Address: "172.16.15.5/32"},
		{Name: "bob", PublicKey: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=", Address: "172.16.15.6/32"},
	}
	got, err := svc.ReplaceAll(ctx, entries)
	if err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ReplaceAll returned %d clients, want 2", len(got))
	}
	for _, c := range got {
		if !c.Enabled {
			t.Errorf("client %q Enabled = false, want true (bulk input has no enabled field)", c.Name)
		}
	}

	// Applier fired exactly once, with the final reconciled set — not once
	// per delete/update/insert performed inside db.ReplaceClients.
	if rec.calls != 1 {
		t.Fatalf("applier calls = %d, want exactly 1", rec.calls)
	}
	if len(rec.last) != 2 {
		t.Fatalf("applier last set has %d clients, want 2", len(rec.last))
	}

	// old must be gone; alice/bob present.
	names := make(map[string]bool, len(got))
	for _, c := range got {
		names[c.Name] = true
	}
	if names["old"] {
		t.Error("old still present after ReplaceAll, want removed")
	}
	if !names["alice"] || !names["bob"] {
		t.Errorf("names = %v, want alice and bob present", names)
	}

	// Confirm against the DB directly too.
	dbClients, err := database.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(dbClients) != 2 {
		t.Fatalf("db has %d clients post-ReplaceAll, want 2", len(dbClients))
	}
}

// TestReplaceAll_EmptySetClearsTable confirms ReplaceAll with an empty slice
// is valid and reconciles to zero peers, applying once with an empty set.
func TestReplaceAll_EmptySetClearsTable(t *testing.T) {
	svc, database := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Add(ctx, AddParams{Name: "a", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	rec := &recordingApplier{}
	svc.SetApplier(rec)

	got, err := svc.ReplaceAll(ctx, nil)
	if err != nil {
		t.Fatalf("ReplaceAll(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReplaceAll(nil) returned %d clients, want 0", len(got))
	}
	if rec.calls != 1 {
		t.Fatalf("applier calls = %d, want 1", rec.calls)
	}

	dbClients, err := database.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(dbClients) != 0 {
		t.Fatalf("db has %d clients post-empty-ReplaceAll, want 0", len(dbClients))
	}
}

// TestReplaceAll_InvalidPayloadRejectedNoWriteNoApply confirms the
// all-or-nothing contract at the service layer: an invalid payload (here, a
// missing address) must fail validation before any DB write or apply call —
// the pre-existing client set and the applier call count are both untouched.
func TestReplaceAll_InvalidPayloadRejectedNoWriteNoApply(t *testing.T) {
	svc, database := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Add(ctx, AddParams{Name: "a", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	rec := &recordingApplier{}
	svc.SetApplier(rec)

	invalid := []ReplaceEntry{
		{Name: "bob", PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=", Address: ""},
	}
	if _, err := svc.ReplaceAll(ctx, invalid); err == nil {
		t.Fatal("ReplaceAll with missing address: want error, got nil")
	}

	if rec.calls != 0 {
		t.Errorf("applier calls = %d, want 0 (validation must fail before any apply)", rec.calls)
	}

	dbClients, err := database.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(dbClients) != 1 || dbClients[0].Name != "a" {
		t.Fatalf("clients after rejected ReplaceAll = %+v, want untouched (only a)", dbClients)
	}
}

func TestDelete(t *testing.T) {
	svc, database := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Add(ctx, AddParams{Name: "a", PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := database.ListClients(ctx)
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(clients) = %d, want 0 after delete", len(got))
	}
	// Deleting again is not an error.
	if err := svc.Delete(ctx, "a"); err != nil {
		t.Errorf("Delete idempotent: %v", err)
	}
}
