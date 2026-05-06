package systemd

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
// code's `errors.As(err, &exitErr)` branch is exercised end-to-end. The
// returned bytes are the captured stdout — exactly what cmd.Output() yields,
// which matches the shape defaultRunner returns in production.
//
// stdin is used to ferry the stdout payload byte-for-byte (avoids `printf`
// escape-string interpretation, which would otherwise turn a literal `\n`
// into a backslash + 'n' instead of a newline). The stderr payload is small
// and goes through the shell command as a hex-escaped argument to env(1)'s
// caller via printf %s.
func runShim(stdout, stderr string, exitCode int) ([]byte, error) {
	// Use `cat` to copy stdin straight to stdout — preserves every byte
	// of the caller's payload verbatim. stderr is emitted via printf %s
	// of a single-quoted argument, which sh treats as a literal string.
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

// dispatchRunner picks an inner runFunc by inspecting the systemctl
// sub-command in args. Production Get() makes two distinct calls
// (`is-active` then `show -p ActiveEnterTimestamp`) — this lets each test
// wire one fake per call without coordinating call counts.
func dispatchRunner(isActive, showProperty runFunc) runFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "is-active" {
				return isActive(ctx, name, args...)
			}
			if a == "show" {
				return showProperty(ctx, name, args...)
			}
		}
		return nil, fmt.Errorf("dispatchRunner: unrecognized args %v", args)
	}
}

func TestServiceGet_Active(t *testing.T) {
	t.Parallel()

	svc := &Service{
		Runner: dispatchRunner(
			fakeRunner([]byte("active\n"), nil),
			fakeRunner([]byte("ActiveEnterTimestamp=Mon 2026-05-06 10:23:15 UTC\n"), nil),
		),
		Unit: DefaultUnit,
	}

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if !got.Active {
		t.Errorf("Active = false, want true")
	}
	if got.State != "active" {
		t.Errorf("State = %q, want %q", got.State, "active")
	}
	want := time.Date(2026, 5, 6, 10, 23, 15, 0, time.UTC)
	if !got.ActiveEnterTimestamp.Equal(want) {
		t.Errorf("ActiveEnterTimestamp = %v, want %v", got.ActiveEnterTimestamp, want)
	}
}

func TestServiceGet_Inactive(t *testing.T) {
	t.Parallel()

	svc := &Service{
		Runner: dispatchRunner(
			shimRunner("inactive\n", "", 3),
			fakeRunner([]byte("ActiveEnterTimestamp=\n"), nil),
		),
		Unit: DefaultUnit,
	}

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.Active {
		t.Errorf("Active = true, want false")
	}
	if got.State != "inactive" {
		t.Errorf("State = %q, want %q", got.State, "inactive")
	}
	if !got.ActiveEnterTimestamp.IsZero() {
		t.Errorf("ActiveEnterTimestamp = %v, want zero", got.ActiveEnterTimestamp)
	}
}

func TestServiceGet_Failed(t *testing.T) {
	t.Parallel()

	svc := &Service{
		Runner: dispatchRunner(
			shimRunner("failed\n", "", 3),
			fakeRunner([]byte("ActiveEnterTimestamp=Mon 2026-05-06 09:00:00 UTC\n"), nil),
		),
		Unit: DefaultUnit,
	}

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.Active {
		t.Errorf("Active = true, want false")
	}
	if got.State != "failed" {
		t.Errorf("State = %q, want %q", got.State, "failed")
	}
	want := time.Date(2026, 5, 6, 9, 0, 0, 0, time.UTC)
	if !got.ActiveEnterTimestamp.Equal(want) {
		t.Errorf("ActiveEnterTimestamp = %v, want %v", got.ActiveEnterTimestamp, want)
	}
}

func TestServiceGet_NeverStarted(t *testing.T) {
	t.Parallel()

	svc := &Service{
		Runner: dispatchRunner(
			shimRunner("inactive\n", "", 3),
			fakeRunner([]byte("ActiveEnterTimestamp=n/a\n"), nil),
		),
		Unit: DefaultUnit,
	}

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.Active {
		t.Errorf("Active = true, want false")
	}
	if got.State != "inactive" {
		t.Errorf("State = %q, want %q", got.State, "inactive")
	}
	if !got.ActiveEnterTimestamp.IsZero() {
		t.Errorf("ActiveEnterTimestamp = %v, want zero (n/a)", got.ActiveEnterTimestamp)
	}
}

func TestServiceGet_Activating(t *testing.T) {
	t.Parallel()

	// `systemctl is-active` exits non-zero for any non-active state, including
	// "activating". The production code recovers the state token off
	// *exec.ExitError.Stdout, so synthesize one with exit 3.
	svc := &Service{
		Runner: dispatchRunner(
			shimRunner("activating\n", "", 3),
			fakeRunner([]byte("ActiveEnterTimestamp=n/a\n"), nil),
		),
		Unit: DefaultUnit,
	}

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.Active {
		t.Errorf("Active = true, want false (only \"active\" flips it true)")
	}
	if got.State != "activating" {
		t.Errorf("State = %q, want %q", got.State, "activating")
	}
}

func TestServiceGet_ShowPropertyFails(t *testing.T) {
	t.Parallel()

	hardErr := errors.New("permission denied")
	svc := &Service{
		Runner: dispatchRunner(
			fakeRunner([]byte("active\n"), nil),
			fakeRunner(nil, hardErr),
		),
		Unit: DefaultUnit,
	}

	got, err := svc.Get(context.Background())
	if err == nil {
		t.Fatal("Get() returned nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error %q does not contain %q", err.Error(), "permission denied")
	}
	if got != (ServiceStatus{}) {
		t.Errorf("ServiceStatus = %#v, want zero value on error", got)
	}
}

func TestServiceGet_ShowPropertyExitNonZero(t *testing.T) {
	t.Parallel()

	const stderrMsg = "Failed to get D-Bus connection"
	svc := &Service{
		Runner: dispatchRunner(
			fakeRunner([]byte("active\n"), nil),
			shimRunner("", stderrMsg, 1),
		),
		Unit: DefaultUnit,
	}

	got, err := svc.Get(context.Background())
	if err == nil {
		t.Fatal("Get() returned nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "systemctl show") {
		t.Errorf("error %q missing 'systemctl show' wrap prefix", err.Error())
	}
	if !strings.Contains(err.Error(), stderrMsg) {
		t.Errorf("error %q does not contain stderr substring %q", err.Error(), stderrMsg)
	}
	if got != (ServiceStatus{}) {
		t.Errorf("ServiceStatus = %#v, want zero value on error", got)
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
	if svc.Unit != "wg-quick@wg0.service" {
		t.Errorf("New().Unit = %q, want %q", svc.Unit, "wg-quick@wg0.service")
	}
	// Intentionally do NOT invoke Get() — that would shell out to sudo
	// systemctl, which is not appropriate in a unit test.
}
