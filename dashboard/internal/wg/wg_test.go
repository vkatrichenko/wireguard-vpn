package wg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runShim runs a tiny `sh -c` subprocess that emits the requested stdout /
// stderr and exits with the requested code. We use this to synthesize a real
// *exec.ExitError (which is awkward to construct directly) so the production
// code's `errors.As(err, &exitErr)` branch is exercised end-to-end.
//
// stdin is used to ferry the stdout payload byte-for-byte (avoids `printf`
// escape-string interpretation, which would otherwise turn a literal `\n`
// into a backslash + 'n' instead of a newline). The stderr payload goes
// through the shell command as a single-quoted argument.
func runShim(stdout, stderr string, exitCode int) ([]byte, error) {
	cmd := exec.Command("sh", "-c", fmt.Sprintf(
		"cat; printf '%%s' %s >&2; exit %d",
		shellSingleQuote(stderr), exitCode,
	))
	cmd.Stdin = strings.NewReader(stdout)
	out, err := cmd.Output()
	return out, err
}

// shellSingleQuote wraps s in POSIX-shell single quotes, escaping any embedded
// single quotes via the standard `'\''` close-reopen idiom. Result is safe to
// splice into an `sh -c` command line as a literal argument.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fakeRunner returns a runFunc closure that hands back canned bytes/err. Used
// when the test does not need a *exec.ExitError (i.e. either exit-0 cases or
// non-ExitError hard failures).
func fakeRunner(out []byte, err error) runFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return out, err
	}
}

// shimRunner returns a runFunc that synthesizes a real *exec.ExitError with
// the requested stdout/stderr/exit code via runShim.
func shimRunner(stdout, stderr string, exitCode int) runFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return runShim(stdout, stderr, exitCode)
	}
}

// Synthetic key fixtures. The parser treats these as opaque strings; only the
// shape (44-char base64 with trailing `=`) matches what `wg show` actually
// emits, so failures in the wild look familiar.
const (
	serverPriv = "kPriv0000000000000000000000000000000000000="
	serverPub  = "kPub00000000000000000000000000000000000000y="
	peerAPub   = "OVtCVOCizGvTVq2vhlymbEOmVnzfZaQKxXgUk+5eYwM="
	peerBPub   = "PeerB0000000000000000000000000000000000000y="
)

// serverLine is the four-field server-info line that `wg show <iface> dump`
// always emits first. Tests prepend it to peer lines to mirror real output.
const serverLine = serverPriv + "\t" + serverPub + "\t51820\toff"

func TestServiceShow_NoPeers(t *testing.T) {
	t.Parallel()

	svc := &Service{
		Runner: fakeRunner([]byte(serverLine+"\n"), nil),
		Iface:  "wg0",
	}

	peers, err := svc.Show(context.Background())
	if err != nil {
		t.Fatalf("Show() returned unexpected error: %v", err)
	}
	if peers != nil {
		t.Errorf("peers = %#v, want nil (server-only output is the no-peers steady state)", peers)
	}
}

func TestServiceShow_EmptyOutput(t *testing.T) {
	t.Parallel()

	svc := &Service{
		Runner: fakeRunner([]byte(""), nil),
		Iface:  "wg0",
	}

	peers, err := svc.Show(context.Background())
	if err != nil {
		t.Fatalf("Show() returned unexpected error: %v", err)
	}
	if peers != nil {
		t.Errorf("peers = %#v, want nil (empty output is the no-peers steady state)", peers)
	}
}

func TestServiceShow_NeverHandshaked(t *testing.T) {
	t.Parallel()

	// Peer line: pubkey, preshared=(none), endpoint=(none),
	// allowed-ips=172.16.15.6/32, latest-handshake=0, rx=0, tx=0,
	// persistent-keepalive=off.
	peerLine := peerAPub + "\t(none)\t(none)\t172.16.15.6/32\t0\t0\t0\toff"
	out := serverLine + "\n" + peerLine + "\n"

	svc := &Service{
		Runner: fakeRunner([]byte(out), nil),
		Iface:  "wg0",
	}

	peers, err := svc.Show(context.Background())
	if err != nil {
		t.Fatalf("Show() returned unexpected error: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("len(peers) = %d, want 1", len(peers))
	}
	p := peers[0]
	if p.PublicKey != peerAPub {
		t.Errorf("PublicKey = %q, want %q", p.PublicKey, peerAPub)
	}
	if p.Endpoint != "" {
		t.Errorf("Endpoint = %q, want %q (sentinel \"(none)\" should map to empty)", p.Endpoint, "")
	}
	if len(p.AllowedIPs) != 1 || p.AllowedIPs[0] != "172.16.15.6/32" {
		t.Errorf("AllowedIPs = %#v, want [\"172.16.15.6/32\"]", p.AllowedIPs)
	}
	if !p.LatestHandshake.IsZero() {
		t.Errorf("LatestHandshake = %v, want zero (never-handshaked)", p.LatestHandshake)
	}
	if p.TransferRx != 0 {
		t.Errorf("TransferRx = %d, want 0", p.TransferRx)
	}
	if p.TransferTx != 0 {
		t.Errorf("TransferTx = %d, want 0", p.TransferTx)
	}
	if p.PersistentKeepalive != 0 {
		t.Errorf("PersistentKeepalive = %d, want 0", p.PersistentKeepalive)
	}
}

func TestServiceShow_ActivePeer(t *testing.T) {
	t.Parallel()

	const handshakeUnix int64 = 1714900000
	peerLine := peerAPub + "\t(none)\t192.0.2.5:51820\t172.16.15.6/32\t" +
		"1714900000\t1234\t5678\t25"
	out := serverLine + "\n" + peerLine + "\n"

	svc := &Service{
		Runner: fakeRunner([]byte(out), nil),
		Iface:  "wg0",
	}

	peers, err := svc.Show(context.Background())
	if err != nil {
		t.Fatalf("Show() returned unexpected error: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("len(peers) = %d, want 1", len(peers))
	}
	p := peers[0]
	if p.Endpoint != "192.0.2.5:51820" {
		t.Errorf("Endpoint = %q, want %q", p.Endpoint, "192.0.2.5:51820")
	}
	if len(p.AllowedIPs) != 1 || p.AllowedIPs[0] != "172.16.15.6/32" {
		t.Errorf("AllowedIPs = %#v, want [\"172.16.15.6/32\"]", p.AllowedIPs)
	}
	wantHS := time.Unix(handshakeUnix, 0).UTC()
	if !p.LatestHandshake.Equal(wantHS) {
		t.Errorf("LatestHandshake = %v, want %v", p.LatestHandshake, wantHS)
	}
	if p.TransferRx != 1234 {
		t.Errorf("TransferRx = %d, want 1234", p.TransferRx)
	}
	if p.TransferTx != 5678 {
		t.Errorf("TransferTx = %d, want 5678", p.TransferTx)
	}
	if p.PersistentKeepalive != 25 {
		t.Errorf("PersistentKeepalive = %d, want 25", p.PersistentKeepalive)
	}
}

func TestServiceShow_MultiplePeers(t *testing.T) {
	t.Parallel()

	active := peerAPub + "\t(none)\t192.0.2.5:51820\t172.16.15.6/32\t" +
		"1714900000\t1234\t5678\t25"
	never := peerBPub + "\t(none)\t(none)\t172.16.15.7/32\t0\t0\t0\toff"
	out := serverLine + "\n" + active + "\n" + never + "\n"

	svc := &Service{
		Runner: fakeRunner([]byte(out), nil),
		Iface:  "wg0",
	}

	peers, err := svc.Show(context.Background())
	if err != nil {
		t.Fatalf("Show() returned unexpected error: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("len(peers) = %d, want 2", len(peers))
	}
	// Order must match stdout order — clients card relies on stable order.
	if peers[0].PublicKey != peerAPub {
		t.Errorf("peers[0].PublicKey = %q, want %q (active peer first)", peers[0].PublicKey, peerAPub)
	}
	if peers[1].PublicKey != peerBPub {
		t.Errorf("peers[1].PublicKey = %q, want %q (never-handshaked peer second)", peers[1].PublicKey, peerBPub)
	}
	if peers[0].Endpoint != "192.0.2.5:51820" {
		t.Errorf("peers[0].Endpoint = %q, want %q", peers[0].Endpoint, "192.0.2.5:51820")
	}
	if peers[1].Endpoint != "" {
		t.Errorf("peers[1].Endpoint = %q, want %q", peers[1].Endpoint, "")
	}
	if !peers[1].LatestHandshake.IsZero() {
		t.Errorf("peers[1].LatestHandshake = %v, want zero", peers[1].LatestHandshake)
	}
}

func TestServiceShow_MultipleAllowedIPs(t *testing.T) {
	t.Parallel()

	// Comma-separated allowed-ips, no spaces — the canonical `wg show` shape.
	peerLine := peerAPub + "\t(none)\t(none)\t172.16.15.6/32,fd00::1/128\t0\t0\t0\toff"
	out := serverLine + "\n" + peerLine + "\n"

	svc := &Service{
		Runner: fakeRunner([]byte(out), nil),
		Iface:  "wg0",
	}

	peers, err := svc.Show(context.Background())
	if err != nil {
		t.Fatalf("Show() returned unexpected error: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("len(peers) = %d, want 1", len(peers))
	}
	want := []string{"172.16.15.6/32", "fd00::1/128"}
	if len(peers[0].AllowedIPs) != len(want) {
		t.Fatalf("AllowedIPs = %#v, want %#v", peers[0].AllowedIPs, want)
	}
	for i, w := range want {
		if peers[0].AllowedIPs[i] != w {
			t.Errorf("AllowedIPs[%d] = %q, want %q", i, peers[0].AllowedIPs[i], w)
		}
	}
}

func TestServiceShow_MalformedLineSkipped(t *testing.T) {
	t.Parallel()

	valid := peerAPub + "\t(none)\t192.0.2.5:51820\t172.16.15.6/32\t" +
		"1714900000\t1234\t5678\t25"
	// Only 5 tab-separated fields — production should log + skip, not error.
	malformed := peerBPub + "\t(none)\t(none)\t172.16.15.7/32\t0"
	out := serverLine + "\n" + valid + "\n" + malformed + "\n"

	svc := &Service{
		Runner: fakeRunner([]byte(out), nil),
		Iface:  "wg0",
	}

	peers, err := svc.Show(context.Background())
	if err != nil {
		t.Fatalf("Show() returned unexpected error: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("len(peers) = %d, want 1 (malformed line should be skipped, not surfaced)", len(peers))
	}
	if peers[0].PublicKey != peerAPub {
		t.Errorf("peers[0].PublicKey = %q, want %q (only the valid line should survive)", peers[0].PublicKey, peerAPub)
	}
}

func TestServiceShow_RunnerExitError(t *testing.T) {
	t.Parallel()

	const stderrMsg = "Unable to access interface: No such file or directory"
	svc := &Service{
		Runner: shimRunner("", stderrMsg, 1),
		Iface:  "wg0",
	}

	peers, err := svc.Show(context.Background())
	if err == nil {
		t.Fatal("Show() returned nil error, want non-nil")
	}
	if peers != nil {
		t.Errorf("peers = %#v, want nil on error", peers)
	}
	// Production wrap shape: fmt.Errorf("wg show %s dump: %w: %s", iface, err, stderr)
	if !strings.Contains(err.Error(), "wg show wg0 dump") {
		t.Errorf("error %q missing 'wg show wg0 dump' wrap prefix", err.Error())
	}
	if !strings.Contains(err.Error(), stderrMsg) {
		t.Errorf("error %q does not contain stderr substring %q", err.Error(), stderrMsg)
	}
	// And it must still wrap a real *exec.ExitError so callers can inspect it.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("error %v does not unwrap to *exec.ExitError", err)
	}
}

func TestServiceShow_RunnerGenericError(t *testing.T) {
	t.Parallel()

	hardErr := errors.New("wg: command not found")
	svc := &Service{
		Runner: fakeRunner(nil, hardErr),
		Iface:  "wg0",
	}

	peers, err := svc.Show(context.Background())
	if err == nil {
		t.Fatal("Show() returned nil error, want non-nil")
	}
	if peers != nil {
		t.Errorf("peers = %#v, want nil on error", peers)
	}
	if !strings.Contains(err.Error(), "wg: command not found") {
		t.Errorf("error %q does not contain %q", err.Error(), "wg: command not found")
	}
	if !errors.Is(err, hardErr) {
		t.Errorf("error chain does not include the original runner error: %v", err)
	}
}

func TestNew_DefaultsAreSet(t *testing.T) {
	t.Parallel()

	svc := New()
	if svc == nil {
		t.Fatal("New() returned nil")
	}
	if svc.Runner == nil {
		t.Error("New().Runner is nil; expected default runner")
	}
	if svc.Iface != "wg0" {
		t.Errorf("New().Iface = %q, want %q", svc.Iface, "wg0")
	}
	// Intentionally do NOT invoke Show() — that would shell out to sudo wg,
	// which is not appropriate in a unit test.
}
