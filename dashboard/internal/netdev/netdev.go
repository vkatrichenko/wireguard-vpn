// Package netdev produces the wg0 interface snapshot shown on the dashboard's
// Network tab "WireGuard interface stats" card. It reads /proc/net/dev once
// per Sample call, finds the wg0 row, and pulls four receive fields
// (bytes/packets/errs/drop) and the matching four transmit fields. Peer count
// is supplied separately via a PeerCounter seam — typically a closure over
// internal/wg's `wg show wg0 dump` helper.
//
// The PeerCounter indirection is intentional: pulling internal/wg into this
// package would (a) make tests construct a real wg.Service with sudo
// expectations and (b) drag in the systemd/exec graph for what is otherwise
// a /proc-reader. Production wiring in main.go builds a tiny closure around
// the existing wg.Service, while tests pass a static fake.
//
// Format expectation is pinned to Ubuntu 24.04's /proc/net/dev layout: two
// header lines followed by data rows of `<iface>: <16 space-separated counters>`.
// A wg0 row with fewer than 16 fields is treated as a kernel format change and
// surfaced as a hard error rather than silently parsed into a truncated struct.
//
// Stateless by design: there is no prior-sample state, no mutex, no deltas.
// Sample returns cumulative counters fresh on every call. The bytes-per-second
// rate card the operator sees uses internal/proc.Service (which already holds
// prior-sample state); making netdev a second source of truth for the same
// rate computation would be a guaranteed drift surface.
//
// Portability: this package compiles cleanly on macOS but Sample() will error
// there because /proc/net/dev does not exist. Production runs on linux/amd64.
package netdev

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// DefaultNetDevPath is the kernel-exported network-interface statistics file.
// Substitutable for tests via Service.NetDevPath.
const DefaultNetDevPath = "/proc/net/dev"

// DefaultIface is the WireGuard interface whose row is extracted from
// /proc/net/dev, matching the wg-quick@wg0 unit cloud-init enables.
const DefaultIface = "wg0"

// Stats is the wg0 interface snapshot rendered by the Network tab's
// "WireGuard interface stats" card. Field semantics:
//
//   - Peers: count from `wg show wg0 dump`. Zero on PeerCounter failure
//     (logged via slog.Warn — the rest of the card still renders).
//   - Rx/Tx counters: cumulative since the wg0 interface came up. The card
//     displays them as totals, not rates (rates live on the network-rate
//     card via proc.Service).
type Stats struct {
	Peers     int   `json:"peers"`
	RxBytes   int64 `json:"rx_bytes"`
	TxBytes   int64 `json:"tx_bytes"`
	RxPackets int64 `json:"rx_packets"`
	TxPackets int64 `json:"tx_packets"`
	RxErrs    int64 `json:"rx_errs"`
	TxErrs    int64 `json:"tx_errs"`
	RxDropped int64 `json:"rx_dropped"`
	TxDropped int64 `json:"tx_dropped"`
}

// readFunc reads the entire contents of a file at path. Mirrors os.ReadFile
// so production wiring is a one-liner while tests substitute a closure that
// returns canned bytes for /proc/net/dev. Unexported — only tests in this
// package construct closures of this shape.
type readFunc func(path string) ([]byte, error)

// peerCounter returns the current count of WireGuard peers on wg0. The seam
// exists so this package never imports internal/wg directly; production code
// supplies a closure that calls wg.Service.Show and returns len(peers).
type peerCounter func(ctx context.Context) (int, error)

// Service holds the injectable seams (Reader, PeerCount, NetDevPath, Iface).
// The seam fields are exported so tests can construct a Service{} literal
// with fakes; production code should use New() to get the real defaults and
// then attach PeerCount once internal/wg is in scope.
//
// PeerCount is intentionally optional: a nil value leaves Stats.Peers at 0,
// which lets the package be exercised in isolation (and in tests) without
// any wg-tooling dependency.
type Service struct {
	Reader     readFunc
	PeerCount  peerCounter
	NetDevPath string
	Iface      string
}

// New returns a Service wired with the production defaults: os.ReadFile, the
// real /proc/net/dev path, and the wg0 interface. PeerCount is left nil —
// main.go attaches it after constructing the wg.Service so this package's
// import graph stays free of internal/wg.
func New() *Service {
	return &Service{
		Reader:     os.ReadFile,
		NetDevPath: DefaultNetDevPath,
		Iface:      DefaultIface,
	}
}

// Sample reads the configured /proc/net/dev path, extracts the wg0 row, and
// (if PeerCount is wired) augments the result with the live peer count. The
// file read is ctx-less because os.ReadFile does not honour cancellation and
// /proc/net/dev is a single-digit-kilobyte virtual file; ctx is forwarded
// only to PeerCount, which genuinely can block on exec.
func (s *Service) Sample(ctx context.Context) (Stats, error) {
	data, err := s.Reader(s.NetDevPath)
	if err != nil {
		return Stats{}, fmt.Errorf("read %s: %w", s.NetDevPath, err)
	}

	var stats Stats
	found := false

	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo <= 2 {
			continue // skip the two header lines
		}
		line := scanner.Text()
		before, after, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.TrimSpace(before) != s.Iface {
			continue
		}
		fields := strings.Fields(after)
		if len(fields) < 16 {
			return Stats{}, fmt.Errorf("malformed %s wg0 line: have %d fields, want 16", s.NetDevPath, len(fields))
		}
		// rx indices: 0=bytes 1=packets 2=errs 3=drop
		// tx indices: 8=bytes 9=packets 10=errs 11=drop
		idx := []int{0, 1, 2, 3, 8, 9, 10, 11}
		vals := make([]int64, len(idx))
		for n, i := range idx {
			v, err := strconv.ParseInt(fields[i], 10, 64)
			if err != nil {
				return Stats{}, fmt.Errorf("parse %s field %d: %w", s.NetDevPath, i, err)
			}
			vals[n] = v
		}
		stats.RxBytes = vals[0]
		stats.RxPackets = vals[1]
		stats.RxErrs = vals[2]
		stats.RxDropped = vals[3]
		stats.TxBytes = vals[4]
		stats.TxPackets = vals[5]
		stats.TxErrs = vals[6]
		stats.TxDropped = vals[7]
		found = true
		break
	}
	if err := scanner.Err(); err != nil {
		return Stats{}, fmt.Errorf("scan %s: %w", s.NetDevPath, err)
	}
	if !found {
		return Stats{}, fmt.Errorf("interface %q not found in %s", s.Iface, s.NetDevPath)
	}

	if s.PeerCount != nil {
		n, err := s.PeerCount(ctx)
		if err != nil {
			slog.Warn("netdev: peer count failed", "err", err)
		} else {
			stats.Peers = n
		}
	}

	return stats, nil
}
