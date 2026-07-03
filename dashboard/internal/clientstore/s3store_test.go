package clientstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runShim runs a tiny `sh -c` subprocess that exits with the requested code
// and stderr text, synthesizing a REAL *exec.ExitError so isNoSuchKey's
// errors.As branch is exercised end-to-end rather than against a hand-rolled
// stand-in. Mirrors internal/wg's runShim.
func runShim(stderr string, exitCode int) ([]byte, error) {
	cmd := exec.Command("sh", "-c", fmt.Sprintf("printf '%%s' %s >&2; exit %d", shellSingleQuote(stderr), exitCode))
	return cmd.Output()
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fakeGetObjectRunner simulates `aws s3api get-object ... <outfile>`: on
// success it writes body to the trailing positional arg (the real CLI's
// output-file argument) before returning, matching the real command's actual
// side effect. On failure it returns the shimmed *exec.ExitError without
// touching the file, matching a real get-object that never wrote a partial
// download.
func fakeGetObjectRunner(t *testing.T, body []byte, stderr string, exitCode int) runFunc {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "aws" {
			t.Fatalf("runner invoked with name = %q, want %q", name, "aws")
		}
		if exitCode != 0 {
			return runShim(stderr, exitCode)
		}
		outFile := args[len(args)-1]
		if err := os.WriteFile(outFile, body, 0o600); err != nil {
			t.Fatalf("fake runner: write outfile: %v", err)
		}
		return []byte(`{"ContentLength":1}`), nil
	}
}

func TestS3Store_Load_Success(t *testing.T) {
	want := []Entry{
		{Name: "alice", Address: "172.16.15.2/32", PublicKey: "AAAA"},
		{Name: "bob", Address: "172.16.15.3/32", PublicKey: "BBBB"},
	}
	body, err := Canonical(want)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	store := &S3Store{Runner: fakeGetObjectRunner(t, body, "", 0), Bucket: "bucket", Key: "clients.json"}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "bob" {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestS3Store_Load_NoSuchKeyReturnsErrNotFound(t *testing.T) {
	store := &S3Store{
		Runner: fakeGetObjectRunner(t, nil,
			"An error occurred (NoSuchKey) when calling the GetObject operation: The specified key does not exist.", 254),
		Bucket: "bucket", Key: "clients.json",
	}
	_, err := store.Load(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load err = %v, want ErrNotFound", err)
	}
}

func TestS3Store_Load_OtherErrorPropagates(t *testing.T) {
	store := &S3Store{
		Runner: fakeGetObjectRunner(t, nil, "An error occurred (AccessDenied) when calling the GetObject operation: Access Denied", 254),
		Bucket: "bucket", Key: "clients.json",
	}
	_, err := store.Load(context.Background())
	if err == nil {
		t.Fatal("Load err = nil, want a propagated error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("Load err = %v, want NOT ErrNotFound (AccessDenied must not be treated as 404)", err)
	}
}

func TestS3Store_Save_UploadsCanonicalBody(t *testing.T) {
	var capturedArgs []string
	var capturedBody []byte
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "aws" {
			t.Fatalf("runner invoked with name = %q, want %q", name, "aws")
		}
		capturedArgs = args
		for i, a := range args {
			if a == "--body" && i+1 < len(args) {
				data, err := os.ReadFile(args[i+1])
				if err != nil {
					t.Fatalf("read staged body: %v", err)
				}
				capturedBody = data
			}
		}
		return nil, nil
	}

	store := &S3Store{Runner: runner, Bucket: "bucket", Key: "clients.json"}
	entries := []Entry{
		{Name: "bob", Address: "172.16.15.10/32", PublicKey: "BBBB"},
		{Name: "alice", Address: "172.16.15.2/32", PublicKey: "AAAA"},
	}
	if err := store.Save(context.Background(), entries); err != nil {
		t.Fatalf("Save: %v", err)
	}

	want, err := Canonical(entries)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if string(capturedBody) != string(want) {
		t.Errorf("uploaded body = %s, want %s", capturedBody, want)
	}
	if !argsContain(capturedArgs, "--bucket", "bucket") || !argsContain(capturedArgs, "--key", "clients.json") {
		t.Errorf("args %v missing expected --bucket/--key", capturedArgs)
	}
}

func TestS3Store_Save_RunnerErrorPropagates(t *testing.T) {
	store := &S3Store{
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return runShim("An error occurred (AccessDenied) when calling the PutObject operation: Access Denied", 254)
		},
		Bucket: "bucket", Key: "clients.json",
	}
	if err := store.Save(context.Background(), []Entry{{Name: "a", Address: "172.16.15.2/32", PublicKey: "AAAA"}}); err == nil {
		t.Fatal("Save err = nil, want a propagated error")
	}
}

// TestIsNoSuchKey_CodeVsBareFallback locks in the C6c tightening: the
// documented "NoSuchKey" code (and its English prose form) must be
// recognised without any log noise, while a message that matches ONLY the
// bare "404" substring — never the real code — still classifies as
// object-absent (unchanged fallback behaviour) but now logs the raw stderr at
// WARN so an unexpected match is visible in journald. AccessDenied (no code,
// no "404") must not match at all.
func TestIsNoSuchKey_CodeVsBareFallback(t *testing.T) {
	captureWarn := func(t *testing.T, fn func()) string {
		t.Helper()
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(prev)
		fn()
		return buf.String()
	}

	t.Run("NoSuchKey code matches without logging", func(t *testing.T) {
		_, err := runShim("An error occurred (NoSuchKey) when calling the GetObject operation: The specified key does not exist.", 254)
		var got bool
		logged := captureWarn(t, func() { got = isNoSuchKey(err) })
		if !got {
			t.Error("isNoSuchKey() = false, want true for the NoSuchKey code")
		}
		if strings.Contains(logged, "bare") {
			t.Errorf("unexpected WARN log for a real NoSuchKey code match: %s", logged)
		}
	})

	t.Run("prose form matches without logging", func(t *testing.T) {
		_, err := runShim("no such key: clients.json", 254)
		var got bool
		logged := captureWarn(t, func() { got = isNoSuchKey(err) })
		if !got {
			t.Error("isNoSuchKey() = false, want true for the 'no such key' prose form")
		}
		if strings.Contains(logged, "bare") {
			t.Errorf("unexpected WARN log for the prose-form match: %s", logged)
		}
	})

	t.Run("bare 404 matches AND logs a warning", func(t *testing.T) {
		_, err := runShim("An error occurred (404) when calling the GetObject operation: Not Found", 254)
		var got bool
		logged := captureWarn(t, func() { got = isNoSuchKey(err) })
		if !got {
			t.Error("isNoSuchKey() = false, want true for the bare \"404\" fallback")
		}
		if !strings.Contains(logged, "bare") || !strings.Contains(logged, "404") {
			t.Errorf("WARN log missing for the bare \"404\" fallback match: %s", logged)
		}
	})

	t.Run("AccessDenied does not match", func(t *testing.T) {
		_, err := runShim("An error occurred (AccessDenied) when calling the GetObject operation: Access Denied", 254)
		var got bool
		logged := captureWarn(t, func() { got = isNoSuchKey(err) })
		if got {
			t.Error("isNoSuchKey() = true, want false for AccessDenied")
		}
		if logged != "" {
			t.Errorf("unexpected log output for a non-matching error: %s", logged)
		}
	})
}

func argsContain(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}
