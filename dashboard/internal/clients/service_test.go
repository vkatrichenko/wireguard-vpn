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
