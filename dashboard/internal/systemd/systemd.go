// Package systemd queries the local systemd unit that backs the WireGuard
// tunnel (default `wg-quick@wg0.service`) and reports a small, render-ready
// snapshot to the dashboard's service-health card.
//
// Two systemctl invocations are issued sequentially:
//
//  1. `systemctl is-active <unit>`  — reports the unit's current state.
//     Exits 0 only when the state is "active"; for inactive/failed/unknown it
//     exits with a non-zero code (typically 3) AND writes the state to stdout.
//     We therefore tolerate a non-zero exit and pull the state out of the
//     captured stdout on *exec.ExitError.
//  2. `systemctl show -p ActiveEnterTimestamp <unit>` — emits a
//     `ActiveEnterTimestamp=...` line whose value is either empty (the unit
//     has never been started in this boot) or a date in systemd's default
//     format `Mon 2006-01-02 15:04:05 MST`.
//
// Both calls go through sudo because the EC2 user-data sudoers entry only
// NOPASSWDs specific binaries (matching the serverinfo package's pattern for
// `sudo /usr/bin/wg ...`). The Runner field is exported so the unit tests in
// the sibling sub-task can swap in a fake without shelling out.
package systemd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultUnit is the systemd unit name the dashboard targets by default — the
// wg-quick template instance for the wg0 interface, matching the cloud-init
// user-data that enables `wg-quick@wg0.service`.
const DefaultUnit = "wg-quick@wg0.service"

// activeEnterTimestampLayout is systemd's default human-readable timestamp
// format as emitted by `systemctl show -p ActiveEnterTimestamp` (e.g.
// `Mon 2026-05-06 10:23:15 UTC`). Expressed in Go's reference time.
const activeEnterTimestampLayout = "Mon 2006-01-02 15:04:05 MST"

// ServiceStatus is the public output shape rendered into the service-health
// card (and returned by the GET /api/service JSON endpoint in a sibling
// sub-task).
type ServiceStatus struct {
	// Active is true iff systemctl reports the unit's state as "active".
	Active bool `json:"active"`
	// State is the raw `systemctl is-active` output, trimmed: one of
	// "active", "inactive", "failed", "activating", or "unknown" (and
	// possibly other values systemd may grow — we pass through whatever
	// it gave us so the caller can decide how to render).
	State string `json:"state"`
	// ActiveEnterTimestamp is the moment the unit last entered the active
	// state. Zero when the unit has never been started or systemd reported
	// "n/a"/empty. `omitzero` keeps the JSON tidy when there's no value.
	ActiveEnterTimestamp time.Time `json:"active_enter_timestamp,omitzero"`
}

// runFunc executes an external command and returns its stdout. Mirrors
// exec.CommandContext(...).Output() so the production wiring is a one-liner,
// while leaving tests free to substitute a closure that returns canned bytes
// and/or *exec.ExitError values without invoking sudo. Type stays unexported;
// tests in the same package can construct closures of this shape directly.
type runFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Service holds the injectable seam (Runner) and the unit name to query.
// Both fields are exported so tests can construct a Service{} literal with
// fakes; production code should use New() to get the real implementation.
type Service struct {
	Runner runFunc
	Unit   string
}

// New returns a Service wired with the production defaults: a Runner that
// shells out via os/exec, and the wg-quick@wg0 unit name.
func New() *Service {
	return &Service{
		Runner: defaultRunner,
		Unit:   DefaultUnit,
	}
}

// Get queries systemctl for the unit's state and last-active timestamp and
// assembles them into a ServiceStatus. The two calls run sequentially —
// they're cheap (single-digit ms each) and serial keeps error wrapping
// straightforward.
//
// A non-zero exit from `is-active` is NOT treated as an error: systemd uses
// the exit code as a signal channel ("0 = active, 3 = anything else"). We
// pull the state token out of *exec.ExitError.Stdout and continue. A non-zero
// exit from `show -p`, by contrast, IS surfaced — that command should always
// succeed for any valid unit name.
func (s *Service) Get(ctx context.Context) (ServiceStatus, error) {
	state, err := s.fetchState(ctx)
	if err != nil {
		return ServiceStatus{}, err
	}

	enteredAt, err := s.fetchActiveEnterTimestamp(ctx)
	if err != nil {
		return ServiceStatus{}, err
	}

	return ServiceStatus{
		Active:               state == "active",
		State:                state,
		ActiveEnterTimestamp: enteredAt,
	}, nil
}

// fetchState runs `sudo /usr/bin/systemctl is-active <unit>` and returns the
// trimmed state token. A non-zero exit is expected for non-active states and
// is recovered by reading stdout off *exec.ExitError; only "no stdout at all"
// or a non-ExitError failure (e.g. binary missing, sudo denied) bubbles up.
func (s *Service) fetchState(ctx context.Context) (string, error) {
	out, err := s.Runner(ctx, "sudo", "/usr/bin/systemctl", "is-active", s.Unit)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// systemctl writes the state to stdout even on non-zero
			// exit. exec.Cmd.Output() captures stdout into the
			// returned bytes (which may still be populated despite
			// the error) AND stderr into ExitError.Stderr. Prefer
			// the stdout bytes returned alongside err; fall back to
			// reporting stderr if stdout was somehow empty.
			state := strings.TrimSpace(string(out))
			if state != "" {
				return state, nil
			}
			return "", fmt.Errorf("systemctl is-active %s: %w: %s", s.Unit, err, bytes.TrimSpace(exitErr.Stderr))
		}
		return "", fmt.Errorf("systemctl is-active %s: %w", s.Unit, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// fetchActiveEnterTimestamp runs `sudo /usr/bin/systemctl show -p
// ActiveEnterTimestamp <unit>`, parses the `KEY=VALUE` line and returns the
// parsed time. Empty / "n/a" values map to a zero time.Time (omitted from JSON
// via the `omitzero` tag).
func (s *Service) fetchActiveEnterTimestamp(ctx context.Context) (time.Time, error) {
	out, err := s.Runner(ctx, "sudo", "/usr/bin/systemctl", "show", "-p", "ActiveEnterTimestamp", s.Unit)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return time.Time{}, fmt.Errorf("systemctl show -p ActiveEnterTimestamp %s: %w: %s", s.Unit, err, bytes.TrimSpace(exitErr.Stderr))
		}
		return time.Time{}, fmt.Errorf("systemctl show -p ActiveEnterTimestamp %s: %w", s.Unit, err)
	}

	line := strings.TrimSpace(string(out))
	// `show -p KEY` always emits exactly one `KEY=VALUE` line. Split on
	// the first `=` so a future systemd that includes `=` characters in
	// the value (unlikely for this property, but cheap insurance) doesn't
	// trip us up.
	_, value, ok := strings.Cut(line, "=")
	if !ok {
		return time.Time{}, fmt.Errorf("systemctl show -p ActiveEnterTimestamp %s: unexpected output %q", s.Unit, line)
	}
	value = strings.TrimSpace(value)
	if value == "" || value == "n/a" {
		return time.Time{}, nil
	}

	t, err := time.Parse(activeEnterTimestampLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("systemctl show -p ActiveEnterTimestamp %s: parse %q: %w", s.Unit, value, err)
	}
	return t, nil
}

// defaultRunner is the production implementation of runFunc. It mirrors
// exec.CommandContext + .Output(), which captures stdout in the return value
// and surfaces stderr via *exec.ExitError on a non-zero exit. Crucially for
// the `is-active` call, the captured stdout bytes are returned alongside the
// error, so callers can still read them.
func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
