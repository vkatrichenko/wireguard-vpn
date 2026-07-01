package clients

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
)

// ErrNotFound is returned by Update when no client matches the supplied name.
// It is a sentinel (rather than a bare fmt.Errorf) so the HTTP layer can map a
// missing-client edit to a 404 via errors.Is without string-matching. Delete
// stays idempotent and never returns this — a missing name is a no-op there.
var ErrNotFound = errors.New("clients: client not found")

// Applier is the seam for the live wg-apply step. Add/Update/Delete call it
// AFTER the DB write, still holding the service mutex, with the full current
// client set so the live config and the DB never diverge. The default
// noopApplier makes this slice a pure DB writer — no `wg syncconf`, no sudo,
// no filesystem. Slice 4 injects a real *wgsync applier via SetApplier with no
// change to the CRUD method signatures: the seam is a single interface field.
type Applier interface {
	Apply(ctx context.Context, clients []db.Client) error
}

// noopApplier is the default no-op live-apply step (see Applier). It is what
// keeps this slice free of any privileged write path.
type noopApplier struct{}

func (noopApplier) Apply(context.Context, []db.Client) error { return nil }

// Service is the stateful orchestration layer for runtime client management:
// validation (via the Slice 2 validators), tunnel-address allocation (via
// AllocateAddress), and CRUD-then-apply serialised through one write mutex.
//
// The mutex makes every mutation atomic against concurrent writers — it
// matches the db package's MaxOpenConns(1) posture and guarantees the
// allocate-then-insert read-modify-write can't race a second Add onto the same
// free address. Construct with NewService; the zero value is not usable.
type Service struct {
	db        *db.DB
	serverNet ServerNet

	mu      sync.Mutex
	applier Applier
}

// NewService constructs the orchestration service over the given DB and the
// WG_SERVER_NET value (server-host form, e.g. "172.16.15.1/24"). An empty
// value falls back to wgconfig.DefaultServerNet inside ParseServerNet; a
// non-empty but malformed value is logged and likewise falls back to the
// default so a typo in the unit's Environment= never bricks allocation.
//
// The applier defaults to a no-op; Slice 4 wires the live wg-sync applier via
// SetApplier.
func NewService(database *db.DB, serverNet string) *Service {
	sn, err := ParseServerNet(serverNet)
	if err != nil {
		slog.Warn("clients: invalid WG_SERVER_NET; falling back to default", "value", serverNet, "err", err)
		sn, _ = ParseServerNet("") // the empty-string default never errors
	}
	return &Service{db: database, serverNet: sn, applier: noopApplier{}}
}

// SetApplier swaps the default no-op live-apply step for a real one (Slice 4).
// A nil applier is ignored so a mis-wire can't silently disable the no-op
// default and panic on the next mutation.
func (s *Service) SetApplier(a Applier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a != nil {
		s.applier = a
	}
}

// Seed imports the Terraform-rendered manifest into the clients table on first
// boot only: if the table already holds rows it is a no-op, so a restart (or a
// replaced instance whose DB survived) never re-imports or clobbers
// runtime-added clients. Seeded rows are stamped Enabled=true with CreatedAt /
// UpdatedAt = now. Idempotent and safe to call on every startup.
func (s *Service) Seed(ctx context.Context, seed []clientsfile.Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	n, err := s.db.CountClients(ctx)
	if err != nil {
		return fmt.Errorf("clients: seed count: %w", err)
	}
	if n > 0 {
		return nil
	}

	now := time.Now().UTC()
	for _, c := range seed {
		rec := db.Client{
			Name:      c.Name,
			PublicKey: c.PublicKey,
			Address:   c.Address,
			Enabled:   true,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := s.db.InsertClient(ctx, rec); err != nil {
			return fmt.Errorf("clients: seed insert %q: %w", c.Name, err)
		}
	}
	return nil
}

// List returns every client row, ordered by tunnel address then id (db's
// stable ordering). It is the read path behind the client list, the config
// download, and drift computation.
func (s *Service) List(ctx context.Context) ([]db.Client, error) {
	return s.db.ListClients(ctx)
}

// AddParams is the input to Add. Address is optional: an empty value triggers
// auto-allocation of the lowest free /32; a non-empty value is validated as a
// manual override (in-subnet, not the server IP, not already in use).
type AddParams struct {
	Name      string
	PublicKey string
	Address   string
	Note      string
}

// Add validates and inserts a new client, allocating a tunnel address when one
// isn't supplied. Name/public-key/address uniqueness is enforced against the
// current table; the whole read-validate-allocate-insert sequence runs under
// the mutex so two concurrent Adds can't collide on a name or an address.
//
// On success the live-apply step runs (a no-op until Slice 4) before returning
// the persisted row.
func (s *Service) Add(ctx context.Context, p AddParams) (db.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ValidateName(p.Name); err != nil {
		return db.Client{}, err
	}
	if err := ValidatePublicKey(p.PublicKey); err != nil {
		return db.Client{}, err
	}

	existing, err := s.db.ListClients(ctx)
	if err != nil {
		return db.Client{}, fmt.Errorf("clients: add list: %w", err)
	}
	for _, c := range existing {
		if c.Name == p.Name {
			return db.Client{}, fmt.Errorf("clients: name %q is already in use", p.Name)
		}
		if c.PublicKey == p.PublicKey {
			return db.Client{}, fmt.Errorf("clients: public key is already in use")
		}
	}

	used := addressesOf(existing)
	var addr string
	if p.Address == "" {
		addr, err = AllocateAddress(s.serverNet, used)
	} else {
		addr, err = ValidateOverride(s.serverNet, p.Address, used)
	}
	if err != nil {
		return db.Client{}, err
	}

	now := time.Now().UTC()
	rec := db.Client{
		Name:      p.Name,
		PublicKey: p.PublicKey,
		Address:   addr,
		Enabled:   true,
		Note:      noteToNull(p.Note),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.db.InsertClient(ctx, rec); err != nil {
		return db.Client{}, fmt.Errorf("clients: add insert: %w", err)
	}
	if err := s.applyLocked(ctx); err != nil {
		return db.Client{}, err
	}
	return rec, nil
}

// UpdateParams carries the editable fields. Each is a pointer so a nil leaves
// the column unchanged — PATCH semantics: only the supplied fields are applied.
type UpdateParams struct {
	Name      *string
	PublicKey *string
	Address   *string
	Note      *string
	Enabled   *bool
}

// Update mutates the client identified by its current name. Changed fields are
// validated and checked for uniqueness against the rest of the table; an
// address change is validated as an override excluding the client's own current
// address. The read-modify-write runs under the mutex, followed by the
// live-apply step.
func (s *Service) Update(ctx context.Context, name string, p UpdateParams) (db.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.db.ListClients(ctx)
	if err != nil {
		return db.Client{}, fmt.Errorf("clients: update list: %w", err)
	}

	idx := -1
	for i := range existing {
		if existing[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return db.Client{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	updated := existing[idx]

	if p.Name != nil && *p.Name != updated.Name {
		if err := ValidateName(*p.Name); err != nil {
			return db.Client{}, err
		}
		for _, c := range existing {
			if c.ID != updated.ID && c.Name == *p.Name {
				return db.Client{}, fmt.Errorf("clients: name %q is already in use", *p.Name)
			}
		}
		updated.Name = *p.Name
	}
	if p.PublicKey != nil && *p.PublicKey != updated.PublicKey {
		if err := ValidatePublicKey(*p.PublicKey); err != nil {
			return db.Client{}, err
		}
		for _, c := range existing {
			if c.ID != updated.ID && c.PublicKey == *p.PublicKey {
				return db.Client{}, fmt.Errorf("clients: public key is already in use")
			}
		}
		updated.PublicKey = *p.PublicKey
	}
	if p.Address != nil {
		addr, err := ValidateOverride(s.serverNet, *p.Address, addressesExcluding(existing, updated.ID))
		if err != nil {
			return db.Client{}, err
		}
		updated.Address = addr
	}
	if p.Note != nil {
		updated.Note = noteToNull(*p.Note)
	}
	if p.Enabled != nil {
		updated.Enabled = *p.Enabled
	}
	updated.UpdatedAt = time.Now().UTC()

	if err := s.db.UpdateClient(ctx, updated); err != nil {
		return db.Client{}, fmt.Errorf("clients: update write: %w", err)
	}
	if err := s.applyLocked(ctx); err != nil {
		return db.Client{}, err
	}
	return updated, nil
}

// ReplaceEntry is one desired peer in a bulk-replace payload (spec 017): the
// same {name, address, public_key} projection ExportEntries emits, since the
// Terraform-driven PUT /api/clients endpoint (a later slice) hands the
// dashboard back exactly what a prior export produced. Unlike AddParams there
// is no optional Address / Note — the bulk path requires an explicit address
// on every entry (no auto-allocation, for idempotency) and has no notion of a
// per-peer note or enabled flag: every git-managed peer is enabled.
type ReplaceEntry struct {
	Name      string
	Address   string
	PublicKey string
}

// ReplaceAll validates entries as a whole set, then reconciles the clients
// table AND the dashboard-owned managed_baseline table to match exactly (via
// db.ReplaceClientsAndBaseline, one transaction covering both) and runs the
// live-apply step once. This is the service-layer half of spec 017's
// bulk-replace endpoint: Terraform hands over the entire desired peer set in
// one call, and the dashboard's SQLite table + live wg0 config must end up
// matching it exactly — inserts, updates, AND deletes for anything no longer
// present.
//
// The baseline write is what lets computeDrift (internal/server) mean
// "diverged from the git-managed set" rather than "diverged from the
// first-boot seed": entries is exactly what a future UI edit or removal will
// be diffed against, so it must land in the same transaction as the peer
// reconcile — a partial failure must never leave the baseline claiming a set
// that the clients table doesn't actually hold.
//
// Every replaced peer is stamped Enabled=true: there is no enabled concept in
// the git-managed input, only in runtime-added peers via Add/Update.
// CreatedAt/UpdatedAt are stamped "now" on new peers; db.ReplaceClients
// preserves CreatedAt (and id) for peers whose public_key survives unchanged.
//
// Validation runs before any DB write — an invalid payload leaves the table,
// the baseline, and the live config untouched (all-or-nothing, matching
// db.ReplaceClientsAndBaseline's own transactional guarantee).
func (s *Service) ReplaceAll(ctx context.Context, entries []ReplaceEntry) ([]db.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateSet(s.serverNet, entries); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	want := make([]db.Client, 0, len(entries))
	baseline := make([]db.BaselineEntry, 0, len(entries))
	for _, e := range entries {
		want = append(want, db.Client{
			Name:      e.Name,
			PublicKey: e.PublicKey,
			Address:   e.Address,
			Enabled:   true,
			CreatedAt: now,
			UpdatedAt: now,
		})
		baseline = append(baseline, db.BaselineEntry{
			Name:      e.Name,
			Address:   e.Address,
			PublicKey: e.PublicKey,
		})
	}

	if err := s.db.ReplaceClientsAndBaseline(ctx, want, baseline); err != nil {
		return nil, fmt.Errorf("clients: replace all: %w", err)
	}
	if err := s.applyLocked(ctx); err != nil {
		return nil, err
	}
	return s.db.ListClients(ctx)
}

// Delete removes the client by name and runs the live-apply step. Deleting a
// non-existent name is not an error (db.DeleteClient is idempotent), matching
// the prune sweep's posture.
func (s *Service) Delete(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.db.DeleteClient(ctx, name); err != nil {
		return fmt.Errorf("clients: delete: %w", err)
	}
	return s.applyLocked(ctx)
}

// Reconcile re-applies the current client set through the applier without any DB
// mutation. It is the startup-convergence step (main calls it once after Seed):
// the live config may not reflect the DB after a restart, a replaced instance,
// or an applier swap, so a single Reconcile on boot makes the live wg0.conf
// match the DB. Idempotent; runs under the mutex like every other apply path.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(ctx)
}

// applyLocked re-reads the full client set and hands it to the applier. Callers
// must already hold s.mu. Until Slice 4 wires a real applier this is a cheap
// no-op (one extra ListClients), kept on the write path so the live-apply seam
// is exercised end-to-end now and Slice 4 is a one-line injection.
func (s *Service) applyLocked(ctx context.Context) error {
	all, err := s.db.ListClients(ctx)
	if err != nil {
		return fmt.Errorf("clients: apply list: %w", err)
	}
	if err := s.applier.Apply(ctx, all); err != nil {
		return fmt.Errorf("clients: apply: %w", err)
	}
	return nil
}

// addressesOf projects the client set to its tunnel-address strings — the
// `used` set AllocateAddress / ValidateOverride consume.
func addressesOf(clients []db.Client) []string {
	out := make([]string, 0, len(clients))
	for _, c := range clients {
		out = append(out, c.Address)
	}
	return out
}

// addressesExcluding is addressesOf minus the row with the given id, so an
// address edit doesn't see the client's own current address as "in use".
func addressesExcluding(clients []db.Client, id int64) []string {
	out := make([]string, 0, len(clients))
	for _, c := range clients {
		if c.ID == id {
			continue
		}
		out = append(out, c.Address)
	}
	return out
}

// noteToNull maps an empty note to a NULL column rather than an empty string,
// matching the db.Client doc's (Valid=false) round-trip for an absent note.
func noteToNull(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
