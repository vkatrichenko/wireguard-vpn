// Package wg parses the live peer state of the local WireGuard interface by
// shelling out to `sudo wg show <iface> dump` and turning each line into a
// structured Peer record for the dashboard.
//
// `wg show wg0 dump` is the machine-readable form of `wg show`. Its output is
// tab-separated with no header line and a fixed shape:
//
//   - Line 1 is the SERVER's own info — four fields:
//     <private-key>\t<public-key>\t<listen-port>\t<fwmark>
//     We deliberately drop this line; the server's public key and port are
//     already surfaced via internal/serverinfo, and the private key has no
//     business leaving the host.
//   - Lines 2..N are PEERS — eight fields each:
//     <public-key>\t<preshared-key>\t<endpoint>\t<allowed-ips>\t
//     <latest-handshake>\t<transfer-rx>\t<transfer-tx>\t<persistent-keepalive>
//
// Sentinel values used by `wg`:
//   - `(none)` for unset preshared-key, endpoint, and allowed-ips.
//   - `0` (literal) for latest-handshake when the peer has never connected.
//   - `off` for persistent-keepalive when not configured.
//
// Empty output (server has no configured peers) is a valid, non-error
// state — the parser returns a nil slice in that case. A line that doesn't
// have exactly eight tab-separated fields is logged and skipped rather than
// failing the whole call: a single rogue peer line shouldn't blank the
// dashboard's clients card.
//
// The Runner field mirrors the seam used in internal/serverinfo and
// internal/systemd so the unit tests in the sibling sub-task can swap in a
// fake without invoking sudo.
package wg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultIface is the WireGuard interface the dashboard targets by default,
// matching the wg-quick@wg0 unit that cloud-init enables.
const DefaultIface = "wg0"

// Peer is the public per-peer output shape rendered into the clients card
// (and returned by the GET /api/clients JSON endpoint in a sibling sub-task).
//
// The JSON tags use `omitempty` / `omitzero` so peers that have never handshaked
// (no endpoint, zero handshake time, no keepalive) produce a tidy payload
// without a forest of empty strings and zero values.
type Peer struct {
	PublicKey           string    `json:"public_key"`
	Endpoint            string    `json:"endpoint,omitempty"`             // omit if "(none)"
	AllowedIPs          []string  `json:"allowed_ips,omitempty"`          // split on comma, omit if empty
	LatestHandshake     time.Time `json:"latest_handshake,omitzero"`      // omit if "0" (never)
	TransferRx          int64     `json:"transfer_rx"`                    // bytes
	TransferTx          int64     `json:"transfer_tx"`                    // bytes
	PersistentKeepalive int       `json:"persistent_keepalive,omitempty"` // seconds, 0 if "off"
}

// runFunc executes an external command and returns its stdout. Mirrors
// exec.CommandContext(...).Output() so the production wiring is a one-liner,
// while leaving tests free to substitute a closure that returns canned bytes
// and/or *exec.ExitError values without invoking sudo.
type runFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Service holds the injectable seam (Runner) and the WireGuard interface name.
// Both fields are exported so tests can construct a Service{} literal with
// fakes; production code should use New() to get the real implementation.
type Service struct {
	Runner runFunc
	Iface  string
}

// New returns a Service wired with the production defaults: a Runner that
// shells out via os/exec, and the wg0 interface name.
func New() *Service {
	return &Service{
		Runner: defaultRunner,
		Iface:  DefaultIface,
	}
}

// Show runs `sudo /usr/bin/wg show <iface> dump` and returns one Peer per
// peer line. The server's own info line (always the first non-empty line) is
// dropped. Empty output and server-only output (no peers configured) both
// return (nil, nil) — those are valid steady states, not errors.
//
// A non-zero exit from `wg show` IS surfaced: it almost always means the
// interface is down, the sudoers entry is missing, or the binary moved —
// each of which is an actionable infrastructure problem the operator should
// see, not a "no peers" condition.
func (s *Service) Show(ctx context.Context) ([]Peer, error) {
	out, err := s.Runner(ctx, "sudo", "/usr/bin/wg", "show", s.Iface, "dump")
	if err != nil {
		// exec.ExitError carries stderr separately; surface it so a
		// missing sudoers entry or a downed wg interface produces an
		// actionable message rather than the bare "exit status 1".
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("wg show %s dump: %w: %s", s.Iface, err, bytes.TrimSpace(exitErr.Stderr))
		}
		return nil, fmt.Errorf("wg show %s dump: %w", s.Iface, err)
	}

	// Filter empties up front so a trailing newline (which `wg show` always
	// emits) doesn't masquerade as a malformed peer line.
	rawLines := strings.Split(string(out), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}

	// Zero non-empty lines: interface exists but produced nothing — treat as
	// "no peers". One non-empty line: server-only, no peers configured.
	if len(lines) <= 1 {
		return nil, nil
	}

	peers := make([]Peer, 0, len(lines)-1)
	// Skip lines[0] — that's the server's own info (private-key, public-key,
	// listen-port, fwmark), not a peer.
	for _, line := range lines[1:] {
		peer, ok := parsePeerLine(line)
		if !ok {
			continue
		}
		peers = append(peers, peer)
	}
	return peers, nil
}

// parsePeerLine turns one tab-separated peer line into a Peer. Returns
// (Peer{}, false) for malformed input (wrong column count) so the caller can
// skip it. All field-level parse errors are logged and degraded to zero
// values rather than failing the whole line — a peer with a garbage byte
// counter is still worth showing in the clients card with the rest of its
// data intact.
func parsePeerLine(line string) (Peer, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) != 8 {
		slog.Warn("wg show dump: skipping malformed peer line",
			"want_fields", 8, "got_fields", len(fields), "line", line)
		return Peer{}, false
	}

	publicKey := fields[0]
	// fields[1] is the preshared-key. We deliberately don't surface it on
	// the dashboard — it's a shared secret and there's nothing useful to
	// render about it beyond "set / not set", which the dashboard doesn't
	// need today.
	endpoint := fields[2]
	allowedIPsRaw := fields[3]
	handshakeRaw := fields[4]
	rxRaw := fields[5]
	txRaw := fields[6]
	keepaliveRaw := fields[7]

	if endpoint == "(none)" {
		endpoint = ""
	}

	var allowedIPs []string
	if allowedIPsRaw != "" && allowedIPsRaw != "(none)" {
		for _, cidr := range strings.Split(allowedIPsRaw, ",") {
			cidr = strings.TrimSpace(cidr)
			if cidr == "" || cidr == "(none)" {
				continue
			}
			allowedIPs = append(allowedIPs, cidr)
		}
	}

	var latestHandshake time.Time
	if handshakeRaw != "0" {
		secs, err := strconv.ParseInt(handshakeRaw, 10, 64)
		if err != nil {
			slog.Warn("wg show dump: invalid latest-handshake, treating as zero",
				"public_key", publicKey, "raw", handshakeRaw, "err", err)
		} else if secs > 0 {
			latestHandshake = time.Unix(secs, 0).UTC()
		}
	}

	rx, err := strconv.ParseInt(rxRaw, 10, 64)
	if err != nil {
		slog.Warn("wg show dump: invalid transfer-rx, treating as 0",
			"public_key", publicKey, "raw", rxRaw, "err", err)
		rx = 0
	}

	tx, err := strconv.ParseInt(txRaw, 10, 64)
	if err != nil {
		slog.Warn("wg show dump: invalid transfer-tx, treating as 0",
			"public_key", publicKey, "raw", txRaw, "err", err)
		tx = 0
	}

	keepalive := 0
	if keepaliveRaw != "off" {
		k, err := strconv.Atoi(keepaliveRaw)
		if err != nil {
			slog.Warn("wg show dump: invalid persistent-keepalive, treating as 0",
				"public_key", publicKey, "raw", keepaliveRaw, "err", err)
		} else {
			keepalive = k
		}
	}

	return Peer{
		PublicKey:           publicKey,
		Endpoint:            endpoint,
		AllowedIPs:          allowedIPs,
		LatestHandshake:     latestHandshake,
		TransferRx:          rx,
		TransferTx:          tx,
		PersistentKeepalive: keepalive,
	}, true
}

// defaultRunner is the production implementation of runFunc. It mirrors
// exec.CommandContext + .Output(), which captures stdout in the return value
// and surfaces stderr via *exec.ExitError on a non-zero exit.
func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
