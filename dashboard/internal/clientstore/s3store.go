package clientstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// runFunc executes an external command and returns its stdout. Mirrors
// exec.CommandContext(...).Output() so the production wiring is a one-liner,
// while tests substitute a closure returning canned bytes / *exec.ExitError
// without shelling out. Matches the seam in internal/wg and internal/wgsync.
type runFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// S3Store is the cloud-mode Store: it shells out to the `aws` CLI (already
// installed on the box by the EC2 user-data for the health-check `aws s3 cp`
// call) rather than pulling in the AWS Go SDK — matching the project's
// minimalist stance on external dependencies and the IMDSv2-raw-read style
// used elsewhere. No --region flag is passed: the existing user-data's own
// `aws s3 cp` health-check signal doesn't pass one either, relying on the AWS
// CLI's IMDS-based region auto-detection on the EC2 host: if that ever stops
// being true, both call sites need the same fix.
//
// Fields are exported so tests can construct an S3Store{} literal with a fake
// Runner, matching the internal/wg / internal/wgsync posture; production code
// should use NewS3Store.
type S3Store struct {
	Runner runFunc
	Bucket string
	Key    string
}

// NewS3Store returns an S3Store wired with the production Runner (real
// exec.Command via defaultRunner).
func NewS3Store(bucket, key string) *S3Store {
	return &S3Store{Runner: defaultRunner, Bucket: bucket, Key: key}
}

// Load downloads the object to a temp file via `aws s3api get-object` and
// parses it as a canonical Entry list. get-object writes the object BODY to
// the positional output-file argument (not stdout — stdout instead carries a
// small JSON metadata summary we don't need), so Load always routes through a
// real temp file rather than trying to capture stdout directly.
//
// A missing object (S3 NoSuchKey) is reported as ErrNotFound, not a generic
// error — see isNoSuchKey. Any other failure (network, permissions, a
// malformed body) is returned unwrapped-into-ErrNotFound so the caller fails
// loudly instead of silently treating a transient outage as "never seeded".
func (s *S3Store) Load(ctx context.Context) ([]Entry, error) {
	tmp, err := os.CreateTemp("", "clientstore-get-*.json")
	if err != nil {
		return nil, fmt.Errorf("clientstore: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := s.Runner(ctx, "aws", "s3api", "get-object",
		"--bucket", s.Bucket, "--key", s.Key, tmpName); err != nil {
		if isNoSuchKey(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("clientstore: aws s3api get-object s3://%s/%s: %w", s.Bucket, s.Key, err)
	}

	data, err := os.ReadFile(tmpName)
	if err != nil {
		return nil, fmt.Errorf("clientstore: read downloaded s3://%s/%s: %w", s.Bucket, s.Key, err)
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("clientstore: parse s3://%s/%s: %w", s.Bucket, s.Key, err)
	}
	return entries, nil
}

// Save canonicalizes entries (field subset + address sort, see Canonical) and
// uploads them as the object body via `aws s3api put-object`. Like Load, the
// body is staged through a real temp file rather than attempted via stdin —
// put-object's --body flag takes a filesystem path, and a real file is the
// same approach internal/wgsync already uses for its staged peers fragment.
func (s *S3Store) Save(ctx context.Context, entries []Entry) error {
	body, err := Canonical(entries)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "clientstore-put-*.json")
	if err != nil {
		return fmt.Errorf("clientstore: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("clientstore: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("clientstore: close temp file: %w", err)
	}

	if _, err := s.Runner(ctx, "aws", "s3api", "put-object",
		"--bucket", s.Bucket, "--key", s.Key,
		"--body", tmpName, "--content-type", "application/json"); err != nil {
		return fmt.Errorf("clientstore: aws s3api put-object s3://%s/%s: %w", s.Bucket, s.Key, err)
	}
	return nil
}

// isNoSuchKey reports whether err is an *exec.ExitError whose stderr text
// identifies a missing-object condition. botocore (which the aws CLI shells
// through) formats a missing-key GetObject failure as:
//
//	An error occurred (NoSuchKey) when calling the GetObject operation: The specified key does not exist.
//
// We match on the "NoSuchKey" error CODE (the stable, documented part of that
// message) plus a couple of defensive fallbacks ("no such key", "404") so a
// botocore wording tweak across CLI versions doesn't silently reclassify a
// real 404 as a generic failure — which would then incorrectly refuse to
// cold-seed a brand-new bucket. This only ever inspects stderr from a command
// that actually RAN and exited non-zero (*exec.ExitError); a missing `aws`
// binary produces a different error type (*exec.Error) and is correctly
// treated as a generic failure, not a 404.
func isNoSuchKey(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	msg := strings.ToLower(string(exitErr.Stderr))
	return strings.Contains(msg, "nosuchkey") ||
		strings.Contains(msg, "no such key") ||
		strings.Contains(msg, "404")
}

// defaultRunner is the production runFunc: exec.CommandContext + .Output(),
// which captures stdout and surfaces stderr via *exec.ExitError on a non-zero
// exit. Matches internal/wg's and internal/wgsync's defaultRunner.
func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
