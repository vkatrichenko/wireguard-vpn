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
	"wireguard-dashboard/internal/clientstore"
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
	// store is the cloud-mode S3 client-list bridge seam (spec 018, Slice 4).
	// Defaults to clientstore.NoopStore{} so a Service that never calls
	// SetStore (every pre-Slice-4 test, and every local-mode boot) writes
	// nothing anywhere and Load always reports clientstore.ErrNotFound — see
	// saveStoreLocked and ReconcileFromStore.
	store clientstore.Store
	// storeReady gates write-through (saveStoreLocked): false means "do not
	// write to the store" (see saveStoreLocked). Defaults to true so local
	// mode (NoopStore) and every pre-boot-reconcile test behave exactly like
	// before this field existed. ReconcileFromStore is the only thing that
	// ever sets it false — a hard (non-404, non-empty-list) S3 load error at
	// boot, meaning we genuinely don't know whether S3 holds something better
	// than what we're about to run with, so refusing to overwrite it until a
	// later successful reconcile/restart is the only safe default.
	storeReady bool
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
	return &Service{db: database, serverNet: sn, applier: noopApplier{}, store: clientstore.NoopStore{}, storeReady: true}
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

// SetStore swaps the default clientstore.NoopStore for a real cloud-mode S3
// store (spec 018, Slice 4). A nil store is ignored, mirroring SetApplier —
// a mis-wire can't silently disable the no-op default. main.go calls this
// exactly once at boot, before ReconcileFromStore / Seed / Reconcile run, and
// never in local mode (the Service simply keeps the no-op default).
func (s *Service) SetStore(store clientstore.Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if store != nil {
		s.store = store
	}
}

// StoreReady reports whether the cloud-mode client store is currently trusted
// for write-through (spec 018 Slice 4 incident follow-up). It is true by
// default (local mode, and any test that never calls ReconcileFromStore) and
// only goes false when ReconcileFromStore hits a hard S3 load error at boot.
// The server package can use this to render a non-fatal "client store
// offline" notice; a false value is otherwise only visible in logs.
func (s *Service) StoreReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storeReady
}

// setStoreReady is the locked setter behind ReconcileFromStore's boot
// decisions. Kept separate from the public accessor so callers holding s.mu
// already (none today, but future write paths might) aren't tempted to call
// the locking StoreReady() and deadlock.
func (s *Service) setStoreReady(ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeReady = ready
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
	if err := s.saveStoreLocked(ctx); err != nil {
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
	// Write-through to the store ONLY when a canonical field
	// (name/public_key/address) actually changed. Enable/disable (and a
	// note-only edit) never touch p.Name/p.PublicKey/p.Address, so a pure
	// enable/disable toggle intentionally skips the S3 write — the `enabled`
	// column is excluded from the bridge object by design (spec 018 §2.4),
	// so toggling it is not Terraform drift and must not generate a spurious
	// S3 PutObject / object version.
	if p.Name != nil || p.PublicKey != nil || p.Address != nil {
		if err := s.saveStoreLocked(ctx); err != nil {
			return db.Client{}, err
		}
	}
	return updated, nil
}

// ReplaceEntry is one desired peer in a bulk-replace payload: the
// {name, address, public_key} projection ReconcileFromStore hands ReplaceAll
// when restoring the full peer set from the cloud-mode S3 backup (spec 019).
// Unlike AddParams there is no optional Address / Note — the bulk path
// requires an explicit address on every entry (no auto-allocation, for
// idempotency) and has no notion of a per-peer note or enabled flag: every
// restored peer is enabled.
type ReplaceEntry struct {
	Name      string
	Address   string
	PublicKey string
}

// ReplaceAll validates entries as a whole set, then reconciles the clients
// table to match exactly (via db.ReplaceClients: inserts, updates, AND
// deletes for anything no longer present) and runs the live-apply step once.
// This is the service-layer half of spec 019's cloud-mode boot restore
// (ReconcileFromStore): the S3 backup hands over the entire peer set it holds,
// and the dashboard's SQLite table + live wg0 config must end up matching it
// exactly.
//
// Every replaced peer is stamped Enabled=true: there is no enabled concept in
// the backup's input, only in runtime-added peers via Add/Update.
// CreatedAt/UpdatedAt are stamped "now" on new peers; db.ReplaceClients
// preserves CreatedAt (and id) for peers whose public_key survives unchanged.
//
// Validation runs before any DB write — an invalid payload leaves the table
// and the live config untouched (all-or-nothing, matching db.ReplaceClients's
// own transactional guarantee).
func (s *Service) ReplaceAll(ctx context.Context, entries []ReplaceEntry) ([]db.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateSet(s.serverNet, entries); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	want := make([]db.Client, 0, len(entries))
	for _, e := range entries {
		want = append(want, db.Client{
			Name:      e.Name,
			PublicKey: e.PublicKey,
			Address:   e.Address,
			Enabled:   true,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	if err := s.db.ReplaceClients(ctx, want); err != nil {
		return nil, fmt.Errorf("clients: replace all: %w", err)
	}
	if err := s.applyLocked(ctx); err != nil {
		return nil, err
	}
	if err := s.saveStoreLocked(ctx); err != nil {
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
	if err := s.applyLocked(ctx); err != nil {
		return err
	}
	return s.saveStoreLocked(ctx)
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

// saveStoreLocked re-reads the full client set, projects it to the canonical
// {name,address,public_key} shape, and hands it to the injected Store
// (spec 018, Slice 4). Callers must already hold s.mu, matching applyLocked's
// contract, and MUST call this AFTER applyLocked succeeds — a failed
// live-apply must never reach S3 with a config that didn't actually take
// effect on the wg interface.
//
// Both the storeReady guard and Save's error are handled best-effort
// (log-and-continue, never returned to the caller) — this is a reversal from
// the original Slice 4 design and exists because of a live incident: by the
// time this runs, the DB and the live wg0 config have ALREADY changed
// successfully, so failing the whole mutation over a store problem would make
// the dashboard refuse a locally-correct Add/Update/Delete just because S3 is
// unreachable. The write-through log at the WARN level is what makes this
// non-silent: the operator sees "not persisted to S3" in journald and can
// retry later or wait for the next successful boot reconcile to heal it.
//
//   - storeReady == false (set by a hard ReconcileFromStore load error):
//     attempt a lazy re-check — a fresh Load — rather than giving up
//     immediately. This is the self-heal for a second live incident: a hard
//     boot error (e.g. `exec: "aws": executable file not found in $PATH`)
//     used to pin storeReady false for the life of the process, since
//     nothing ever re-checked it. If the re-check succeeds, or reports
//     ErrNotFound (the object doesn't exist yet, but S3 itself is reachable
//     — our Save below will create it), the underlying problem has resolved
//     itself and write-through resumes without a restart. Any other error
//     means we still don't know whether S3 holds something better than our
//     local state, so Save is skipped for this mutation exactly as before.
//   - storeReady == true (either already, or just healed above) but Save
//     fails: log and move on. The mutation already succeeded locally;
//     propagating the S3 error here is exactly what let a
//     `wg syncconf`-driven write-through client add/delete look like it
//     failed even though the local, functional half of the change had
//     already taken effect.
//
// Until SetStore is called (local mode, and every pre-Slice-4 test) s.store
// is a clientstore.NoopStore with storeReady defaulting to true, so the
// lazy-recheck branch is never entered — this is a cheap extra ListClients +
// a genuine no-op Save, no behavioural change from spec 015.
func (s *Service) saveStoreLocked(ctx context.Context) error {
	if !s.storeReady {
		if _, err := s.store.Load(ctx); err == nil || errors.Is(err, clientstore.ErrNotFound) {
			s.storeReady = true
			slog.Info("clients: client store reachable again; resuming S3 write-through")
		} else {
			slog.Warn("clients: client store offline; change applied locally but NOT persisted to S3", "err", err)
			return nil
		}
	}

	all, err := s.db.ListClients(ctx)
	if err != nil {
		return fmt.Errorf("clients: store save list: %w", err)
	}
	entries := make([]clientstore.Entry, 0, len(all))
	for _, c := range all {
		entries = append(entries, clientstore.Entry{Name: c.Name, Address: c.Address, PublicKey: c.PublicKey})
	}
	if err := s.store.Save(ctx, entries); err != nil {
		slog.Warn("clients: client store save failed; change applied locally but NOT persisted to S3", "err", err)
	}
	return nil
}

// ReconcileFromStore is the cloud-mode boot path (spec 018, Slice 4, revised
// after a live incident where an empty-but-existing S3 object wiped every
// operator peer). It implements this decision table:
//
//   - Load returns a NON-EMPTY list → S3 is authoritative (steady state):
//     ReplaceAll(entries) reconciles the DB to match S3 exactly. Store marked ready.
//   - Load returns clientstore.ErrNotFound OR an empty list (the two are
//     treated identically — an empty object is never authoritative, exactly
//     like a missing one):
//   - current DB is non-empty → HEAL S3 FROM THE DB: Save the DB's own
//     canonical list back to the store, leave the DB untouched. This is
//     what protects a persisted/rebooted box's UI-added clients from an
//     S3 object that reads back empty (the incident's exact shape).
//   - current DB is empty → cold-seed FROM localSeed (the
//     /etc/wireguard-dashboard/clients.json boot manifest): Save it to the
//     store, then Seed + Reconcile the DB/live config the ordinary
//     spec-015 way. This is the one-time bootstrap the S3 object needs
//     before write-through becomes the durable source of truth.
//   - Load returns any OTHER (hard, non-404) error → the store might hold
//     something we can't currently see, so we neither wipe it nor write to
//     it: if the DB is empty, Seed + Reconcile from localSeed so the box
//     stays usable and matches wg0; if the DB already has rows, leave it
//     alone. Either way storeReady is set false (see saveStoreLocked) and
//     the error is returned so main.go logs it loudly.
//
// In every branch the store's Save is skipped/attempted directly by this
// method (not through saveStoreLocked's storeReady guard) because boot
// reconcile IS the thing that decides whether the store is trustworthy —
// the guard exists for the mutation write-through paths that run AFTER boot.
func (s *Service) ReconcileFromStore(ctx context.Context, localSeed []clientsfile.Client) error {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()

	entries, err := store.Load(ctx)
	if err != nil && !errors.Is(err, clientstore.ErrNotFound) {
		return s.reconcileFromHardStoreError(ctx, localSeed, err)
	}

	empty := errors.Is(err, clientstore.ErrNotFound) || len(entries) == 0
	if empty {
		return s.reconcileFromEmptyStore(ctx, store, localSeed)
	}

	replace := make([]ReplaceEntry, 0, len(entries))
	for _, e := range entries {
		replace = append(replace, ReplaceEntry{Name: e.Name, Address: e.Address, PublicKey: e.PublicKey})
	}
	if _, err := s.ReplaceAll(ctx, replace); err != nil {
		s.setStoreReady(false)
		return fmt.Errorf("clients: reconcile from store: %w", err)
	}
	s.setStoreReady(true)
	return nil
}

// reconcileFromEmptyStore handles the ErrNotFound/empty-list branch of
// ReconcileFromStore: the box's own state heals the store, never the other
// way around. See ReconcileFromStore's doc comment for the full decision
// table.
func (s *Service) reconcileFromEmptyStore(ctx context.Context, store clientstore.Store, localSeed []clientsfile.Client) error {
	n, err := s.db.CountClients(ctx)
	if err != nil {
		s.setStoreReady(false)
		return fmt.Errorf("clients: count clients before store heal: %w", err)
	}

	if n > 0 {
		all, err := s.db.ListClients(ctx)
		if err != nil {
			s.setStoreReady(false)
			return fmt.Errorf("clients: list clients to heal store: %w", err)
		}
		healEntries := make([]clientstore.Entry, 0, len(all))
		for _, c := range all {
			healEntries = append(healEntries, clientstore.Entry{Name: c.Name, Address: c.Address, PublicKey: c.PublicKey})
		}
		if err := store.Save(ctx, healEntries); err != nil {
			s.setStoreReady(false)
			return fmt.Errorf("clients: heal store from db: %w", err)
		}
		s.setStoreReady(true)
		return nil
	}

	seedEntries := make([]clientstore.Entry, 0, len(localSeed))
	for _, c := range localSeed {
		seedEntries = append(seedEntries, clientstore.Entry{Name: c.Name, Address: c.Address, PublicKey: c.PublicKey})
	}
	if err := store.Save(ctx, seedEntries); err != nil {
		s.setStoreReady(false)
		return fmt.Errorf("clients: seed store from local boot seed: %w", err)
	}
	if err := s.Seed(ctx, localSeed); err != nil {
		s.setStoreReady(false)
		return fmt.Errorf("clients: seed db after store cold-seed: %w", err)
	}
	if err := s.Reconcile(ctx); err != nil {
		s.setStoreReady(false)
		return fmt.Errorf("clients: reconcile after store cold-seed: %w", err)
	}
	s.setStoreReady(true)
	return nil
}

// reconcileFromHardStoreError handles a non-404 Load failure: the store is
// marked not-ready (gating write-through in saveStoreLocked) and the DB is
// left alone UNLESS it's empty, in which case it's seeded from localSeed so
// the box still has a usable, wg0-matching client set despite the outage.
// The store itself is never written to here — an unreadable object is not
// the same as a confirmed-empty one, and clobbering it would risk destroying
// data we simply failed to see.
func (s *Service) reconcileFromHardStoreError(ctx context.Context, localSeed []clientsfile.Client, loadErr error) error {
	s.setStoreReady(false)

	n, err := s.db.CountClients(ctx)
	if err != nil {
		return fmt.Errorf("clients: count clients after store load error: %w", err)
	}
	if n == 0 {
		if err := s.Seed(ctx, localSeed); err != nil {
			return fmt.Errorf("clients: seed db after store load error: %w", err)
		}
		if err := s.Reconcile(ctx); err != nil {
			return fmt.Errorf("clients: reconcile after store load error: %w", err)
		}
	}
	return fmt.Errorf("clients: load store: %w", loadErr)
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
