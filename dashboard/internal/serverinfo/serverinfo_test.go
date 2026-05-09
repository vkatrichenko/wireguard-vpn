package serverinfo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// validKey is a 44-character base64 placeholder (the wire-format size of a
// WireGuard public key: 32 bytes raw → 44 base64 chars including the trailing
// `=` padding). The production code only calls strings.TrimSpace on it, so
// byte content is opaque to the unit under test — only the length matters
// when we assert it stays untouched after trimming.
const validKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG="

// fakeIMDS is a hand-rolled stub for the imdsClient interface. The optional
// delay lets the cancellation test exercise the ctx.Done() branch in Service.Get
// without racing the goroutine fan-out.
type fakeIMDS struct {
	ip    string
	err   error
	delay time.Duration
}

func (f fakeIMDS) PublicIP(ctx context.Context) (string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.ip, f.err
}

// fakeRunner returns a runFunc closure that produces canned bytes / err. The
// delay parameter mirrors fakeIMDS so cancellation tests can stall both legs.
func fakeRunner(out []byte, err error, delay time.Duration) runFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return out, err
	}
}

func TestServiceGet_HappyPath(t *testing.T) {
	t.Parallel()

	svc := &Service{
		IMDS:   fakeIMDS{ip: "203.0.113.1"},
		Runner: fakeRunner([]byte(validKey+"\n"), nil, 0),
	}

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.PublicIP != "203.0.113.1" {
		t.Errorf("PublicIP = %q, want %q", got.PublicIP, "203.0.113.1")
	}
	if got.Port != 51820 {
		t.Errorf("Port = %d, want 51820", got.Port)
	}
	if got.ServerPublicKey != validKey {
		t.Errorf("ServerPublicKey = %q, want %q", got.ServerPublicKey, validKey)
	}
}

func TestServiceGet_IMDSFails(t *testing.T) {
	t.Parallel()

	svc := &Service{
		IMDS:   fakeIMDS{err: errors.New("metadata service unreachable")},
		Runner: fakeRunner([]byte(validKey+"\n"), nil, 0),
	}

	got, err := svc.Get(context.Background())
	if err == nil {
		t.Fatal("Get() returned nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "metadata service unreachable") {
		t.Errorf("error %q does not contain %q", err.Error(), "metadata service unreachable")
	}
	if got != (ServerInfo{}) {
		t.Errorf("ServerInfo = %#v, want zero value on error", got)
	}
}

func TestServiceGet_RunnerFails(t *testing.T) {
	t.Parallel()

	runnerErr := errors.New("exit status 1: sudo: a terminal is required")
	svc := &Service{
		IMDS:   fakeIMDS{ip: "203.0.113.1"},
		Runner: fakeRunner(nil, runnerErr, 0),
	}

	got, err := svc.Get(context.Background())
	if err == nil {
		t.Fatal("Get() returned nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("error %q does not contain %q", err.Error(), "exit status 1")
	}
	if !strings.Contains(err.Error(), "sudo: a terminal is required") {
		t.Errorf("error %q does not contain stderr detail", err.Error())
	}
	if got != (ServerInfo{}) {
		t.Errorf("ServerInfo = %#v, want zero value on error", got)
	}
}

func TestServiceGet_BothFail(t *testing.T) {
	t.Parallel()

	imdsErr := errors.New("metadata service unreachable")
	runnerErr := errors.New("wg interface down")
	svc := &Service{
		IMDS:   fakeIMDS{err: imdsErr},
		Runner: fakeRunner(nil, runnerErr, 0),
	}

	got, err := svc.Get(context.Background())
	if err == nil {
		t.Fatal("Get() returned nil error, want non-nil")
	}
	// errors.Join keeps both originals reachable via errors.Is.
	if !errors.Is(err, imdsErr) {
		t.Errorf("error chain does not include IMDS error: %v", err)
	}
	if !errors.Is(err, runnerErr) {
		t.Errorf("error chain does not include runner error: %v", err)
	}
	if !strings.Contains(err.Error(), "metadata service unreachable") {
		t.Errorf("error %q missing IMDS message", err.Error())
	}
	if !strings.Contains(err.Error(), "wg interface down") {
		t.Errorf("error %q missing runner message", err.Error())
	}
	if got != (ServerInfo{}) {
		t.Errorf("ServerInfo = %#v, want zero value on error", got)
	}
}

func TestServiceGet_RunnerWhitespace(t *testing.T) {
	t.Parallel()

	// Leading spaces, trailing spaces, and a Windows CRLF — strings.TrimSpace
	// should strip every byte of it.
	noisy := "  " + validKey + "  \r\n"
	svc := &Service{
		IMDS:   fakeIMDS{ip: "203.0.113.1"},
		Runner: fakeRunner([]byte(noisy), nil, 0),
	}

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() returned unexpected error: %v", err)
	}
	if got.ServerPublicKey != validKey {
		t.Errorf("ServerPublicKey = %q, want %q (no surrounding whitespace)", got.ServerPublicKey, validKey)
	}
	if len(got.ServerPublicKey) != 44 {
		t.Errorf("ServerPublicKey length = %d, want 44", len(got.ServerPublicKey))
	}
}

func TestServiceGet_ContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// 50ms delays guarantee both fakes hit the ctx.Done() branch rather than
	// completing first and masking the cancellation.
	svc := &Service{
		IMDS:   fakeIMDS{ip: "203.0.113.1", delay: 50 * time.Millisecond},
		Runner: fakeRunner([]byte(validKey+"\n"), nil, 50*time.Millisecond),
	}

	got, err := svc.Get(ctx)
	if err == nil {
		t.Fatal("Get() returned nil error, want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v is not context.Canceled", err)
	}
	if got != (ServerInfo{}) {
		t.Errorf("ServerInfo = %#v, want zero value on cancellation", got)
	}
}

func TestNew_DefaultsAreSet(t *testing.T) {
	t.Parallel()

	svc := New()
	if svc == nil {
		t.Fatal("New() returned nil")
	}
	if svc.IMDS == nil {
		t.Error("New().IMDS is nil; expected default httpIMDS")
	}
	if svc.Runner == nil {
		t.Error("New().Runner is nil; expected default runner")
	}
	// Intentionally do NOT invoke Get() — that would hit the real IMDS and
	// shell out to sudo, neither of which is appropriate in a unit test.
}
