package wgsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/wgconfig"
)

// Synthetic key fixtures — opaque to the renderer; only the shape mirrors what
// `wg` emits so failures in the wild look familiar.
const (
	pubA = "AClient00000000000000000000000000000000000y="
	pubB = "BClient00000000000000000000000000000000000y="
	pubC = "CClient00000000000000000000000000000000000y="
)

// capturingRunner records the (name, args) of the single helper invocation and
// returns canned bytes/err — no real sudo/exec.
type capturingRunner struct {
	name string
	args []string
	out  []byte
	err  error
}

func (c *capturingRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	c.name = name
	c.args = append([]string(nil), args...)
	return c.out, c.err
}

// runShim synthesizes a real *exec.ExitError carrying the requested stderr and
// exit code, so the production errors.As(err, &exitErr) branch is exercised
// end-to-end. Mirrors the helper in internal/wg's tests.
func runShim(stderr string, exitCode int) ([]byte, error) {
	cmd := exec.Command("sh", "-c", fmt.Sprintf(
		"printf '%%s' %s >&2; exit %d", shellSingleQuote(stderr), exitCode,
	))
	return cmd.Output()
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func newTestApplier(t *testing.T, r runFunc) *Applier {
	t.Helper()
	return &Applier{
		Runner:     r,
		StagePath:  filepath.Join(t.TempDir(), "peers.conf"),
		HelperPath: "/usr/local/sbin/wg-sync",
	}
}

func TestApply_StagedContentEnabledSortedDisabledOmitted(t *testing.T) {
	t.Parallel()

	cr := &capturingRunner{}
	a := newTestApplier(t, cr.run)

	// Deliberately unsorted, with one disabled client that must be omitted.
	clients := []db.Client{
		{Name: "b", PublicKey: pubB, Address: "172.16.15.10/32", Enabled: true},
		{Name: "disabled", PublicKey: pubC, Address: "172.16.15.3/32", Enabled: false},
		{Name: "a", PublicKey: pubA, Address: "172.16.15.2/32", Enabled: true},
	}

	if err := a.Apply(context.Background(), clients); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}

	got, err := os.ReadFile(a.StagePath)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}

	// Expected: enabled peers only, ascending tunnel address (.2 before .10),
	// stanzas separated by a blank line. Built independently of BuildServerPeers
	// so the ordering/filtering assertion is meaningful.
	want := wgconfig.BuildServerPeer(wgconfig.ServerPeer{Name: "a", PublicKey: pubA, Address: "172.16.15.2/32", Enabled: true}) +
		"\n" +
		wgconfig.BuildServerPeer(wgconfig.ServerPeer{Name: "b", PublicKey: pubB, Address: "172.16.15.10/32", Enabled: true})

	if string(got) != want {
		t.Errorf("staged content mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if strings.Contains(string(got), pubC) {
		t.Error("staged content contains the disabled client's public key; it must be omitted")
	}
	if strings.Contains(string(got), "[Interface]") || strings.Contains(string(got), "PrivateKey") {
		t.Error("staged content must be peers-only: no [Interface]/PrivateKey")
	}
}

func TestApply_StagedFileMode(t *testing.T) {
	t.Parallel()

	a := newTestApplier(t, (&capturingRunner{}).run)
	if err := a.Apply(context.Background(), []db.Client{
		{Name: "a", PublicKey: pubA, Address: "172.16.15.2/32", Enabled: true},
	}); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}

	info, err := os.Stat(a.StagePath)
	if err != nil {
		t.Fatalf("stat staged file: %v", err)
	}
	if got := info.Mode().Perm(); got != stageFileMode {
		t.Errorf("staged file mode = %o, want %o", got, stageFileMode)
	}
}

func TestApply_InvokesHelperExactArgv(t *testing.T) {
	t.Parallel()

	cr := &capturingRunner{}
	a := newTestApplier(t, cr.run)

	if err := a.Apply(context.Background(), nil); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if cr.name != "sudo" {
		t.Errorf("runner name = %q, want %q", cr.name, "sudo")
	}
	if len(cr.args) != 1 || cr.args[0] != a.HelperPath {
		t.Errorf("runner args = %#v, want [%q]", cr.args, a.HelperPath)
	}
}

func TestApply_EmptyClientSetWritesEmptyFile(t *testing.T) {
	t.Parallel()

	a := newTestApplier(t, (&capturingRunner{}).run)
	if err := a.Apply(context.Background(), nil); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	got, err := os.ReadFile(a.StagePath)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("staged content = %q, want empty (no peers to converge to)", got)
	}
}

func TestApply_RunnerExitErrorWrapped(t *testing.T) {
	t.Parallel()

	const stderrMsg = "sudo: a password is required"
	r := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return runShim(stderrMsg, 1)
	}
	a := newTestApplier(t, r)

	err := a.Apply(context.Background(), []db.Client{
		{Name: "a", PublicKey: pubA, Address: "172.16.15.2/32", Enabled: true},
	})
	if err == nil {
		t.Fatal("Apply() returned nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), a.HelperPath) {
		t.Errorf("error %q missing helper path %q", err.Error(), a.HelperPath)
	}
	if !strings.Contains(err.Error(), stderrMsg) {
		t.Errorf("error %q does not contain stderr substring %q", err.Error(), stderrMsg)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("error %v does not unwrap to *exec.ExitError", err)
	}

	// The staging file is written before the helper runs, so it should exist
	// even though the helper failed.
	if _, statErr := os.Stat(a.StagePath); statErr != nil {
		t.Errorf("staged file should be written before helper invocation: %v", statErr)
	}
}

func TestApply_Idempotent(t *testing.T) {
	t.Parallel()

	a := newTestApplier(t, (&capturingRunner{}).run)
	clients := []db.Client{
		{Name: "b", PublicKey: pubB, Address: "172.16.15.10/32", Enabled: true},
		{Name: "a", PublicKey: pubA, Address: "172.16.15.2/32", Enabled: true},
	}

	if err := a.Apply(context.Background(), clients); err != nil {
		t.Fatalf("Apply() #1 error: %v", err)
	}
	first, err := os.ReadFile(a.StagePath)
	if err != nil {
		t.Fatalf("read after #1: %v", err)
	}

	if err := a.Apply(context.Background(), clients); err != nil {
		t.Fatalf("Apply() #2 error: %v", err)
	}
	second, err := os.ReadFile(a.StagePath)
	if err != nil {
		t.Fatalf("read after #2: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("staged bytes not idempotent:\n#1: %q\n#2: %q", first, second)
	}
}

func TestNew_Defaults(t *testing.T) {
	t.Parallel()

	a := New()
	if a.Runner == nil {
		t.Error("New().Runner is nil; expected default runner")
	}
	if a.StagePath != DefaultStagePath {
		t.Errorf("New().StagePath = %q, want %q", a.StagePath, DefaultStagePath)
	}
	if a.HelperPath != DefaultHelperPath {
		t.Errorf("New().HelperPath = %q, want %q", a.HelperPath, DefaultHelperPath)
	}
}
