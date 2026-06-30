// Package wgsync is the live-apply step for runtime client management: it turns
// the DB client set into the WireGuard interface's live peer state.
//
// PEERS-ONLY BY DESIGN. The dashboard runs as the unprivileged
// `wireguard-dashboard` user, which cannot read /etc/wireguard/wg0.conf (mode
// 0600, owned by root) and so must NEVER need the server private key. Therefore
// this applier stages a PEERS-ONLY fragment — the [Peer] stanzas for enabled
// clients, rendered via wgconfig.BuildServerPeers — and NOT a full wg0.conf. The
// privileged root helper at HelperPath (/usr/local/sbin/wg-sync, provisioned in
// a later slice) is responsible for combining the on-disk [Interface] block
// (which holds the private key) with these staged peers and running
// `wg syncconf`. We deliberately do not use wgconfig.BuildServerConf here: it
// embeds PrivateKey, which this process has no business holding.
//
// Wiring: Apply implements internal/clients' Applier interface, so the clients
// Service calls it after every CRUD write (and once at startup via Reconcile)
// with the full current client set. The Runner field mirrors the seam used in
// internal/wg / internal/serverinfo so unit tests can swap in a fake without
// invoking sudo or touching the real helper.
package wgsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/wgconfig"
)

const (
	// DefaultStagePath is where the peers-only fragment is written. It lives in
	// the systemd StateDirectory (/var/lib/wireguard-dashboard, mode 0750, owned
	// by the dashboard user) alongside metrics.db. The Slice 5 wg-sync helper
	// MUST read from exactly this path. The name carries "peers" so it is never
	// mistaken for a complete wg0.conf — it has no [Interface] block.
	DefaultStagePath = "/var/lib/wireguard-dashboard/peers.conf"

	// DefaultHelperPath is the root-owned merge-and-syncconf helper invoked via
	// sudo. Its argv ("sudo /usr/local/sbin/wg-sync") must match the sudoers
	// NOPASSWD entry character-for-character (provisioned in Slice 5).
	DefaultHelperPath = "/usr/local/sbin/wg-sync"

	// stageFileMode is 0640: owner (dashboard user) read/write, group read, no
	// world access. The staged fragment contains only public keys and tunnel
	// addresses — no secret — so group-read is harmless; the root helper reads
	// it regardless of mode. We do not need 0600 because there is nothing
	// sensitive to hide, but we keep world access off as a matter of hygiene.
	stageFileMode = 0o640
)

// runFunc executes an external command and returns its stdout. Mirrors
// exec.CommandContext(...).Output() so the production wiring is a one-liner
// while tests substitute a closure returning canned bytes / *exec.ExitError
// without invoking sudo. Matches the seam in internal/wg.
type runFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Applier renders the peers-only staging file and invokes the privileged sync
// helper. Fields are exported so tests can construct an Applier{} literal with
// fakes (matching the wg.Service posture); production code should use New().
type Applier struct {
	Runner     runFunc
	StagePath  string
	HelperPath string
}

// New returns an Applier wired with the production defaults: a Runner that
// shells out via os/exec, the StateDirectory staging path, and the sudo helper.
func New() *Applier {
	return &Applier{
		Runner:     defaultRunner,
		StagePath:  DefaultStagePath,
		HelperPath: DefaultHelperPath,
	}
}

// Apply renders the [Peer] stanzas for the enabled clients (ascending tunnel
// address, deterministic), atomically writes them to the staging file, then
// invokes `sudo <HelperPath>` to merge-and-syncconf. It satisfies
// internal/clients.Applier. Disabled clients are retained in the DB but omitted
// from the live config. A nil/empty client set produces an empty staging file —
// the correct convergence target for "no peers".
func (a *Applier) Apply(ctx context.Context, clients []db.Client) error {
	peers := make([]wgconfig.ServerPeer, 0, len(clients))
	for _, c := range clients {
		peers = append(peers, wgconfig.ServerPeer{
			Name:      c.Name,
			PublicKey: c.PublicKey,
			Address:   c.Address,
			Enabled:   c.Enabled,
		})
	}

	if err := a.writeStaged([]byte(wgconfig.BuildServerPeers(peers))); err != nil {
		return err
	}

	if _, err := a.Runner(ctx, "sudo", a.HelperPath); err != nil {
		// exec.ExitError carries stderr separately; surface it so a missing
		// sudoers entry or a failed `wg syncconf` produces an actionable
		// message rather than the bare "exit status 1". Mirrors internal/wg.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return fmt.Errorf("wgsync: sudo %s: %w: %s", a.HelperPath, err, bytes.TrimSpace(exitErr.Stderr))
		}
		return fmt.Errorf("wgsync: sudo %s: %w", a.HelperPath, err)
	}
	return nil
}

// writeStaged writes data to a temp file in the staging directory and renames it
// over StagePath, so the root helper never observes a half-written fragment
// (rename within a filesystem is atomic). The temp file is chmod'd to
// stageFileMode before rename; the deferred Remove is a no-op once the rename
// succeeds (the temp name no longer exists) and cleans up on any early return.
func (a *Applier) writeStaged(data []byte) error {
	dir := filepath.Dir(a.StagePath)
	tmp, err := os.CreateTemp(dir, ".peers.conf-*")
	if err != nil {
		return fmt.Errorf("wgsync: create temp staging file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("wgsync: write staging file: %w", err)
	}
	if err := tmp.Chmod(stageFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("wgsync: chmod staging file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("wgsync: close staging file: %w", err)
	}
	if err := os.Rename(tmpName, a.StagePath); err != nil {
		return fmt.Errorf("wgsync: rename staging file to %s: %w", a.StagePath, err)
	}
	return nil
}

// defaultRunner is the production runFunc. It mirrors exec.CommandContext +
// .Output(), which captures stdout and surfaces stderr via *exec.ExitError on a
// non-zero exit. Matches internal/wg's defaultRunner.
func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
