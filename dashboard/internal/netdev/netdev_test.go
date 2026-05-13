package netdev_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"wireguard-dashboard/internal/netdev"
)

// headerLines reproduces the two-line preamble that the kernel writes at the
// top of /proc/net/dev. Pinning it here keeps each test's fixture composition
// to a single string-concat against canonical iface rows.
func headerLines() string {
	return "Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"
}

// canonicalWg0Row is the 16-counter wg0 row used by the happy-path and most
// peer-counter tests. Layout (after the colon):
//
//	rx: bytes=1024 packets=10 errs=1 drop=2 fifo frame compressed multicast
//	tx: bytes=2048 packets=20 errs=3 drop=4 fifo colls carrier compressed
const canonicalWg0Row = "  wg0: 1024 10 1 2 0 0 0 0 2048 20 3 4 0 0 0 0\n"

// canonicalWg0Stats is the Stats value the canonical row decodes to (with
// Peers left zero — callers set it when a PeerCount seam is wired).
var canonicalWg0Stats = netdev.Stats{
	RxBytes:   1024,
	RxPackets: 10,
	RxErrs:    1,
	RxDropped: 2,
	TxBytes:   2048,
	TxPackets: 20,
	TxErrs:    3,
	TxDropped: 4,
}

// readerOf returns a Reader closure that serves the supplied bytes back for
// the configured NetDevPath. Anything else is treated as ENOENT — tests pin
// the exact path the package was given.
func readerOf(t *testing.T, want string, data string) func(path string) ([]byte, error) {
	t.Helper()
	return func(path string) ([]byte, error) {
		if path != want {
			t.Errorf("Reader called with unexpected path %q (want %q)", path, want)
			return nil, os.ErrNotExist
		}
		return []byte(data), nil
	}
}

func wantStats(t *testing.T, got, want netdev.Stats) {
	t.Helper()
	if got != want {
		t.Errorf("stats mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestSample_HappyPathWithPeerCounter(t *testing.T) {
	fixture := headerLines() +
		"   eth0: 9999 9 0 0 0 0 0 0 8888 8 0 0 0 0 0 0\n" +
		canonicalWg0Row

	svc := &netdev.Service{
		Reader:     readerOf(t, "/proc/net/dev", fixture),
		PeerCount:  func(context.Context) (int, error) { return 3, nil },
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
	}

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}

	want := canonicalWg0Stats
	want.Peers = 3
	wantStats(t, got, want)
}

func TestSample_NilPeerCounterLeavesPeersZero(t *testing.T) {
	fixture := headerLines() + canonicalWg0Row

	svc := &netdev.Service{
		Reader:     readerOf(t, "/proc/net/dev", fixture),
		PeerCount:  nil,
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
	}

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if got.Peers != 0 {
		t.Errorf("Peers: got %d want 0 (nil PeerCount)", got.Peers)
	}
	wantStats(t, got, canonicalWg0Stats)
}

func TestSample_PeerCounterErrorLeavesPeersZero(t *testing.T) {
	fixture := headerLines() + canonicalWg0Row

	svc := &netdev.Service{
		Reader: readerOf(t, "/proc/net/dev", fixture),
		PeerCount: func(context.Context) (int, error) {
			return 0, fmt.Errorf("wg show failed")
		},
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
	}

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v (peer-count failure must not fail the whole sample)", err)
	}
	if got.Peers != 0 {
		t.Errorf("Peers: got %d want 0 (PeerCount errored)", got.Peers)
	}
	// Byte/packet fields must still be populated.
	wantStats(t, got, canonicalWg0Stats)
}

func TestSample_IfaceNotFound(t *testing.T) {
	fixture := headerLines() +
		"   eth0: 9999 9 0 0 0 0 0 0 8888 8 0 0 0 0 0 0\n"

	svc := &netdev.Service{
		Reader:     readerOf(t, "/proc/net/dev", fixture),
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
	}

	_, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatalf("Sample: want error, got nil")
	}
	if !strings.Contains(err.Error(), `interface "wg0" not found`) {
		t.Errorf("err = %v, want substring %q", err, `interface "wg0" not found`)
	}
}

func TestSample_TruncatedWg0Row(t *testing.T) {
	// Only 10 counters after the colon — under the 16 required.
	fixture := headerLines() +
		"  wg0: 1 2 3 4 5 6 7 8 9 10\n"

	svc := &netdev.Service{
		Reader:     readerOf(t, "/proc/net/dev", fixture),
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
	}

	_, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatalf("Sample: want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "malformed") || !strings.Contains(msg, "wg0") {
		t.Errorf("err = %v, want substrings %q and %q", err, "malformed", "wg0")
	}
}

func TestSample_NonIntegerFieldValue(t *testing.T) {
	// Position 0 (RxBytes) is "bogus".
	fixture := headerLines() +
		"  wg0: bogus 10 1 2 0 0 0 0 2048 20 3 4 0 0 0 0\n"

	svc := &netdev.Service{
		Reader:     readerOf(t, "/proc/net/dev", fixture),
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
	}

	_, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatalf("Sample: want error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("err = %v, want substring %q", err, "parse")
	}
}

func TestSample_ReaderError(t *testing.T) {
	svc := &netdev.Service{
		Reader: func(path string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
	}

	_, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatalf("Sample: want error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want wrap of os.ErrNotExist", err)
	}
}

func TestSample_OtherIfacesDontPollute(t *testing.T) {
	// eth0 row is full of non-numeric junk; if the iface filter regresses,
	// the parser will hit it first and error out before reaching wg0.
	fixture := headerLines() +
		"   eth0: bogus bogus bogus bogus bogus bogus bogus bogus bogus bogus bogus bogus bogus bogus bogus bogus\n" +
		canonicalWg0Row

	svc := &netdev.Service{
		Reader:     readerOf(t, "/proc/net/dev", fixture),
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
	}

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v (iface filter must skip eth0 entirely)", err)
	}
	wantStats(t, got, canonicalWg0Stats)
}

func TestSample_CustomIface(t *testing.T) {
	// Loopback row with distinct values so a mis-pick of wg0 would surface.
	fixture := headerLines() +
		"    lo: 500 5 0 0 0 0 0 0 700 7 0 0 0 0 0 0\n" +
		canonicalWg0Row

	svc := &netdev.Service{
		Reader:     readerOf(t, "/proc/net/dev", fixture),
		NetDevPath: "/proc/net/dev",
		Iface:      "lo",
	}

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	want := netdev.Stats{
		RxBytes:   500,
		RxPackets: 5,
		TxBytes:   700,
		TxPackets: 7,
	}
	wantStats(t, got, want)
}

func TestSample_ColonWithoutSpace(t *testing.T) {
	// "wg0:1024 ..." — some kernel versions emit no space after the colon.
	// strings.Cut + strings.Fields should handle both forms.
	fixture := headerLines() +
		"  wg0:1024 10 1 2 0 0 0 0 2048 20 3 4 0 0 0 0\n"

	svc := &netdev.Service{
		Reader:     readerOf(t, "/proc/net/dev", fixture),
		NetDevPath: "/proc/net/dev",
		Iface:      "wg0",
	}

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	wantStats(t, got, canonicalWg0Stats)
}
