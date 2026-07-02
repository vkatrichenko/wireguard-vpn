// Package clientstore is the cloud-mode client-list bridge (spec 018): a
// versioned S3 object that the dashboard reads at boot and rewrites on every
// UI mutation, while Terraform seeds it once and warns (never reverts) on
// drift. Package internal/clients owns the write-through/boot-reconcile call
// sites; this package owns the Store seam, the two implementations
// (NoopStore for local mode, S3Store for cloud mode), and the canonical
// serializer both sides of the bridge must agree on byte-for-structure.
//
// Entry deliberately mirrors internal/clients.ReplaceEntry's {name, address,
// public_key} shape rather than importing internal/clients directly: this
// package must stay leaf-level (internal/clients imports IT, to hold a Store
// dependency and call Save after a mutation), so importing internal/clients
// here would be a cycle. The two types are kept in sync by convention.
package clientstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Entry is one client in the canonical {name, address, public_key}
// projection — the exact fields Terraform's `clients_config` carries. Every
// other dashboard-only field (enabled, note, timestamps, id) is intentionally
// absent: the S3 bridge object is not a full client record, only the subset
// Terraform's `check "client_list_drift"` block compares against.
type Entry struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
}

// ErrNotFound is the sentinel a Store's Load returns when the backing object
// does not exist (S3 404 / NoSuchKey). It is distinct from every other error
// Load can return: callers treat ErrNotFound as "never seeded, initialize it
// from the local boot seed" and treat anything else as a hard failure that
// must NOT be papered over with an empty list (that would silently wipe a
// real, temporarily-unreachable client set).
var ErrNotFound = errors.New("clientstore: object not found")

// Store is the seam between the dashboard and the durable client-list bridge.
// Load returns the current canonical list, or ErrNotFound when the backing
// object has never been seeded. Save overwrites it with the given list —
// callers are expected to pass the FULL current set (this is a whole-object
// replace, matching the Terraform side's `jsonencode` of the entire list),
// not a diff.
type Store interface {
	Load(ctx context.Context) ([]Entry, error)
	Save(ctx context.Context, entries []Entry) error
}

// NoopStore is the local-mode default: Load always reports ErrNotFound (there
// is nothing to load — local mode has no bridge object) and Save is a pure
// no-op. It is what makes internal/clients' write-through/boot-reconcile
// hooks free of any behavioural change in local mode, matching spec 015
// exactly. Every clients.Service starts wired with this store until main.go
// calls SetStore with a real one in cloud mode.
type NoopStore struct{}

func (NoopStore) Load(context.Context) ([]Entry, error) { return nil, ErrNotFound }
func (NoopStore) Save(context.Context, []Entry) error   { return nil }

// Canonical serializes entries into the exact JSON shape Terraform's
// `locals.clients_canonical_json` produces (terraform/modules/wireguard/locals.tf):
//
//	clients_by_address     = { for c in var.clients_config : c.address => { name = c.name, address = c.address, public_key = c.public_key } }
//	clients_canonical_json = jsonencode([ for addr in sort(keys(local.clients_by_address)) : local.clients_by_address[addr] ])
//
// Terraform's root-module drift `check` decodes BOTH sides before comparing
// (jsondecode(live) == jsondecode(canonical)), so byte-level formatting
// (key order, whitespace, HTML-escaping) never causes a false positive. What
// MUST match is the logical structure:
//
//  1. Field set — exactly {name, address, public_key}. Any other field
//     (enabled, note, timestamps, id) must already be stripped from entries
//     before calling this — Entry's shape enforces that at the type level.
//  2. Order — ascending by the Address field as a plain STRING comparison
//     (Terraform's sort() is lexicographic, not IP-aware). This means
//     "172.16.15.10/32" sorts BEFORE "172.16.15.6/32" (byte '1' < byte '6').
//     Do NOT "fix" this by parsing to net.IP — that would silently diverge
//     from the Terraform side and reintroduce a phantom-drift bug (the exact
//     class of bug spec 017's clients_sorted logic existed to avoid).
//
// A nil or empty input encodes as "[]", matching jsonencode([]) — never
// "null".
func Canonical(entries []Entry) ([]byte, error) {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Address < sorted[j].Address })

	body, err := json.Marshal(sorted)
	if err != nil {
		return nil, fmt.Errorf("clientstore: marshal canonical: %w", err)
	}
	return body, nil
}
