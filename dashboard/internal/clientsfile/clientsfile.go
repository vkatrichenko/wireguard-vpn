// Package clientsfile reads the JSON client manifest that cloud-init renders
// to /etc/wireguard-dashboard/clients.json on the EC2 host.
//
// Stock WireGuard has no native concept of human-readable peer names — `wg
// show wg0 dump` only knows public keys, allowed-IPs, handshakes, and byte
// counters. The Terraform user-data step is the source of truth that maps
// each peer's public key to a label and tunnel address; it does so by
// `jsonencode`-ing the `var.clients_config` list into the manifest this
// package consumes. See terraform/modules/wireguard/locals.tf
// (`local.clients_json`) and terraform/modules/wireguard/templates/user-data.txt.
//
// The file is mode 0640, owner root:wireguard-dashboard — readable by the
// dashboard's group but not world-readable, since it carries client public
// keys and tunnel CIDRs that aren't strictly secret but aren't broadcast
// either.
//
// Reader is exposed as a function-typed seam (mirroring the runFunc seam
// in internal/serverinfo and internal/systemd) so the unit tests in a later
// sub-task can swap in a fake without touching the real filesystem.
package clientsfile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// DefaultPath is the on-disk location of the client manifest as written by
// the cloud-init user-data step. Hardcoded to match the heredoc target in
// terraform/modules/wireguard/templates/user-data.txt; if either side moves,
// this constant must move with it.
const DefaultPath = "/etc/wireguard-dashboard/clients.json"

// Client is one entry in the manifest. The JSON tags match the keys that
// Terraform's `jsonencode` emits for each element of `var.clients_config`.
// `jsonencode` sorts object keys alphabetically, so the on-disk order is
// `address`, `name`, `public_key` — but Go's encoding/json is field-name-
// driven, so the struct field order here is purely cosmetic.
type Client struct {
	Name      string `json:"name"`
	Address   string `json:"address"`    // e.g. "172.16.15.6/32"
	PublicKey string `json:"public_key"` // 44-char base64
}

// readFunc reads a file by path and returns its contents. Mirrors the
// signature of os.ReadFile so the production wiring is a one-liner, while
// leaving tests free to substitute a closure that returns canned bytes /
// errors without touching the real filesystem.
type readFunc func(path string) ([]byte, error)

// Service holds the injectable seam (Reader) and the manifest path. Both
// fields are exported so tests can construct a Service{} literal with fakes;
// production code should use New() to get the real implementation.
type Service struct {
	Reader readFunc
	Path   string
}

// New returns a Service wired with the production defaults: os.ReadFile as
// the reader and DefaultPath as the manifest location.
func New() *Service {
	return &Service{
		Reader: os.ReadFile,
		Path:   DefaultPath,
	}
}

// Load reads and parses the manifest, returning the decoded client list.
//
// ctx is accepted for API symmetry with the other internal/* services and to
// reserve the option of cgroup-bounded I/O later, but it is intentionally not
// plumbed through to the read itself: the manifest is a small local file and
// the read is fast enough that cancellation would just complicate the seam
// without buying anything measurable.
//
// An empty manifest (`[]`) is a valid, non-error result that returns an
// empty slice — there's nothing pathological about a server with zero
// configured peers.
func (s *Service) Load(ctx context.Context) ([]Client, error) {
	_ = ctx // see doc comment — ctx reserved, not used today

	data, err := s.Reader(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.Path, err)
	}

	var clients []Client
	if err := json.Unmarshal(data, &clients); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.Path, err)
	}
	return clients, nil
}

// ByPublicKey indexes the slice by the peer's WireGuard public key for
// O(1) lookup during the join with `wg show wg0 dump` output. Pre-built
// once per snapshot so the join doesn't quadratic-loop over peers.
//
// Duplicate keys (which shouldn't happen — Terraform clients_config is the
// source of truth and any duplicate there is an operator bug) collapse to
// last-write-wins rather than panicking; the next sub-task can decide
// whether to surface that as a warning at a higher level.
func ByPublicKey(clients []Client) map[string]Client {
	index := make(map[string]Client, len(clients))
	for _, c := range clients {
		index[c.PublicKey] = c
	}
	return index
}
