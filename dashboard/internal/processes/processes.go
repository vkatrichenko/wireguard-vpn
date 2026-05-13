// Package processes reports the top-N CPU-consuming processes on the
// WireGuard EC2 host by walking /proc and parsing the per-PID files
// /proc/[pid]/stat, /proc/[pid]/status, and /proc/[pid]/cmdline, alongside
// the host-wide /proc/stat and /proc/meminfo.
//
// CPU% is inherently a delta measurement: per-PID (utime+stime) jiffies must
// be diffed against a prior sample, and divided by the total-jiffies delta of
// the aggregate /proc/stat line over the same interval. Service therefore
// retains the prior sample (per-PID jiffies and the aggregate total) under a
// sync.Mutex. The first call to Sample returns CPUPct=0 for every row — the
// second and subsequent calls compute meaningful percentages. The reported
// number is htop-style "percent of one CPU": (procDelta/totalDelta) is
// multiplied by runtime.NumCPU() so a single CPU-bound thread reads ~100% and
// a process pegging two cores reads ~200%.
//
// /proc/[pid]/stat is space-separated, but the comm field (index 2) is wrapped
// in parens and may itself contain spaces or close-parens (e.g. "(rcu_sched)",
// "(kworker/u4:0)", or a renamed thread). The robust parse finds the LAST ')'
// in the line — everything to its right is the well-formed numeric suffix.
// Within that suffix, utime is offset 11 and stime is offset 12 (zero-indexed),
// matching the field ordering documented in man 5 proc.
//
// PIDs can disappear between readdir(/proc) and read(/proc/<pid>/stat). Any
// ENOENT during the per-PID reads is treated as "process exited mid-walk" and
// the row is silently skipped — the next Sample picks up the new process
// table. Other errors on a single PID are logged at warn level and the row is
// skipped, so one broken /proc entry never blanks the whole top-5 list.
//
// All side-effecting seams are exposed as exported fields on Service so unit
// tests can substitute fakes:
//
//   - Reader      — file reader, defaults to os.ReadFile.
//   - ReadDir     — directory lister returning entry names, defaults to a
//     wrapper around os.ReadDir.
//   - LookupUser  — uid → username, defaults to a wrapper around
//     os/user.LookupId; optional (nil disables /etc/passwd resolution).
//   - ProcPath    — filesystem root, default "/proc".
//   - Now         — clock, defaults to time.Now.
//
// Production code should use New() to get a Service wired with these defaults.
//
// The mutex serialises the read-and-update of the prior snapshot so concurrent
// Sample calls cannot race the delta computation. Sample is the only public
// method and it takes the lock for its full duration; the dashboard polls it
// at ~1Hz at most so contention is not a concern.
//
// Portability: this package compiles cleanly on macOS but Sample() errors
// there because /proc does not exist. Production runs on linux/amd64; tests
// inject fake Reader/ReadDir closures.
package processes

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/user"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultProcPath is the kernel-exported process table consumed by Sample.
// Hardcoded to the canonical Linux path; tests override it via Service.ProcPath.
const DefaultProcPath = "/proc"

// TopN is the number of highest-CPU processes Sample returns. Five matches
// the dashboard card's row budget; if the spec ever wants a longer table this
// constant is the single place to change.
const TopN = 5

// CmdlineDisplayLimit is the byte length at which Sample truncates the
// human-display Command field. Anything longer gets "…" appended; the full
// cmdline is preserved separately in CmdlineFull so the template can render
// it in a tooltip without the package making HTML-escape decisions.
const CmdlineDisplayLimit = 60

// Process is one row in the top-N table. CPUPct is htop-style "percent of one
// CPU" — a multi-threaded process pegging two cores reports ~200%. MemPct is
// VmRSS as a fraction of /proc/meminfo MemTotal.
//
// Command is the truncated cmdline display string (≤ CmdlineDisplayLimit
// chars, "…" appended when truncated). The original, untruncated cmdline is
// in CmdlineFull so the template can put it in a `title=` tooltip and decide
// the HTML-escape rules itself.
type Process struct {
	PID         int     `json:"pid"`
	User        string  `json:"user"`         // resolved via /etc/passwd; falls back to uid string on lookup failure
	CPUPct      float64 `json:"cpu_pct"`      // percent of one CPU; 0 on first sample
	MemPct      float64 `json:"mem_pct"`      // percent of /proc/meminfo MemTotal
	Command     string  `json:"command"`      // truncated to CmdlineDisplayLimit chars, "…" suffix if truncated
	CmdlineFull string  `json:"cmdline_full"` // full cmdline, null-bytes replaced with spaces
}

// readFunc reads the entire contents of a file at path. Mirrors os.ReadFile
// so the production wiring is a one-liner, while leaving tests free to
// substitute a closure that returns canned bytes for /proc files without
// touching the real filesystem.
type readFunc func(path string) ([]byte, error)

// readDirFunc returns the names of the entries under path. Only the leaf
// name is needed (the PID enumeration only cares about the digit-string
// directory names), so the seam exposes []string rather than the richer
// []fs.DirEntry — keeps test fakes trivial to construct.
type readDirFunc func(path string) ([]string, error)

// userFunc resolves a numeric uid string to a username, mirroring
// os/user.LookupId. Optional on Service: a nil seam disables /etc/passwd
// resolution and falls back to the numeric uid string in the rendered row.
type userFunc func(uid string) (username string, err error)

// priorSample is the unexported state Service holds between Sample calls so
// it can compute per-PID CPU% deltas. A zero `when` is the first-sample
// sentinel: when prior.when.IsZero(), Sample returns CPUPct=0 for every row
// instead of trying to subtract against a non-existent baseline.
type priorSample struct {
	when         time.Time
	totalJiffies uint64         // sum of /proc/stat cpu-aggregate-row fields
	perPID       map[int]uint64 // pid → utime + stime jiffies at last sample
}

// Service holds the injectable seams and the prior-sample state needed for
// delta-based CPU%. The seam fields are exported so tests can construct a
// Service{} literal with fakes; production code should use New() to get the
// real implementation.
//
// The mutex serialises the read-and-update of `prior` so concurrent Sample
// calls cannot race the delta computation. Sample is the only public method
// and it holds the lock for its full duration.
type Service struct {
	Reader     readFunc         // os.ReadFile by default
	ReadDir    readDirFunc      // wrapper around os.ReadDir by default
	LookupUser userFunc         // os/user.LookupId by default; nil disables resolution
	ProcPath   string           // "/proc"
	Now        func() time.Time // time.Now by default

	mu    sync.Mutex
	prior priorSample
}

// New returns a Service wired with the production defaults: os.ReadFile, an
// os.ReadDir wrapper, an os/user.LookupId wrapper, the real /proc filesystem
// root, and time.Now. The prior-sample per-PID map is pre-allocated so the
// first Sample can write into it without a nil check.
func New() *Service {
	return &Service{
		Reader: os.ReadFile,
		ReadDir: func(path string) ([]string, error) {
			entries, err := os.ReadDir(path)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return names, nil
		},
		LookupUser: func(uid string) (string, error) {
			u, err := user.LookupId(uid)
			if err != nil {
				return "", err
			}
			return u.Username, nil
		},
		ProcPath: DefaultProcPath,
		Now:      time.Now,
		prior: priorSample{
			perPID: make(map[int]uint64),
		},
	}
}

// Sample walks /proc once, computes per-PID CPU% against the prior snapshot,
// sorts by CPU% descending, and returns the top TopN rows. On the first call
// every row reports CPUPct=0 — there is no prior snapshot to delta against;
// the second and subsequent calls produce meaningful values.
//
// ctx is accepted for API symmetry with the rest of the codebase but is not
// plumbed through: every read is a small local file I/O and cancellation
// would just complicate the seams. Callers that need a hard deadline should
// wrap Sample in their own goroutine + select.
//
// The entire read-compute-update sequence runs under s.mu so concurrent
// callers cannot read a half-updated prior snapshot.
func (s *Service) Sample(ctx context.Context) ([]Process, error) {
	_ = ctx // see doc comment — ctx reserved, not used today

	s.mu.Lock()
	defer s.mu.Unlock()

	currTotal, err := s.readTotalJiffies()
	if err != nil {
		return nil, fmt.Errorf("read /proc/stat: %w", err)
	}

	memTotalKB, err := s.readMemTotalKB()
	if err != nil {
		// MemTotal failure is soft — MemPct collapses to 0 across the
		// board but the CPU-ordered list is still useful. Matches the
		// "partial degradation beats blank card" precedent in internal/proc.
		slog.Warn("processes: read /proc/meminfo failed, MemPct will be 0", "err", err)
		memTotalKB = 0
	}

	names, err := s.ReadDir(s.ProcPath)
	if err != nil {
		return nil, fmt.Errorf("readdir %s: %w", s.ProcPath, err)
	}

	firstSample := s.prior.when.IsZero()
	totalDelta := currTotal - s.prior.totalJiffies
	cpuCount := float64(runtime.NumCPU())

	candidates := make([]Process, 0, len(names))
	nextPerPID := make(map[int]uint64, len(names))

	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil {
			// non-numeric entry (self, thread-self, mounts, …) — skip
			continue
		}

		utime, stime, comm, ok := s.readStat(pid)
		if !ok {
			continue
		}
		procCurr := utime + stime

		uidStr, vmRSSkB, ok := s.readStatus(pid)
		if !ok {
			continue
		}

		cmdline, ok := s.readCmdline(pid, comm)
		if !ok {
			continue
		}

		username := uidStr
		if s.LookupUser != nil {
			if name, lerr := s.LookupUser(uidStr); lerr == nil {
				username = name
			}
		}

		var cpuPct float64
		if !firstSample && totalDelta > 0 {
			if procPrior, present := s.prior.perPID[pid]; present {
				if procCurr >= procPrior {
					cpuPct = float64(procCurr-procPrior) / float64(totalDelta) * cpuCount * 100
				}
			}
		}
		if cpuPct < 0 {
			cpuPct = 0
		}

		var memPct float64
		if memTotalKB > 0 {
			memPct = float64(vmRSSkB) / float64(memTotalKB) * 100
		}

		display := cmdline
		if len(display) > CmdlineDisplayLimit {
			display = display[:CmdlineDisplayLimit] + "…"
		}

		candidates = append(candidates, Process{
			PID:         pid,
			User:        username,
			CPUPct:      cpuPct,
			MemPct:      memPct,
			Command:     display,
			CmdlineFull: cmdline,
		})
		nextPerPID[pid] = procCurr
	}

	// Rebuild perPID rather than mutating-in-place: processes that exited
	// since the last sample shouldn't carry stale jiffy counts. If the
	// kernel later recycles the same pid, an old (large) prior would make
	// the procCurr - procPrior subtraction underflow uint64 and the CPU%
	// would spike absurdly until the next sample.
	s.prior.when = s.Now()
	s.prior.totalJiffies = currTotal
	s.prior.perPID = nextPerPID

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CPUPct > candidates[j].CPUPct
	})

	if len(candidates) > TopN {
		candidates = candidates[:TopN]
	}
	if candidates == nil {
		// Defensive: a completely empty /proc is impossible in
		// practice but the JSON encoder should still emit `[]`, not
		// `null`, so the chart-poller javascript can iterate safely.
		candidates = make([]Process, 0)
	}
	return candidates, nil
}

// readTotalJiffies returns the sum of every numeric field on the aggregate
// `cpu ` line of /proc/stat. That sum is what divides the per-PID
// (utime+stime) delta to produce a fractional CPU share over the same
// interval. The aggregate line is the first line and starts with the literal
// `cpu ` (trailing space matters — without it we'd also match `cpu0`/`cpu1`).
func (s *Service) readTotalJiffies() (uint64, error) {
	path := s.ProcPath + "/stat"
	data, err := s.Reader(path)
	if err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, fmt.Errorf("%s: cpu line has %d fields, want >=5: %q", path, len(fields), line)
		}
		var sum uint64
		for i, raw := range fields[1:] {
			v, perr := strconv.ParseUint(raw, 10, 64)
			if perr != nil {
				return 0, fmt.Errorf("%s: cpu field %d %q: %w", path, i, raw, perr)
			}
			sum += v
		}
		return sum, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("%s: scan: %w", path, err)
	}
	return 0, fmt.Errorf("%s: no `cpu ` aggregate line", path)
}

// readMemTotalKB parses MemTotal (in kB) out of /proc/meminfo. Returns an
// error on read or parse failure; the caller decides whether to treat that as
// fatal (it doesn't — MemPct simply collapses to 0).
func (s *Service) readMemTotalKB() (int64, error) {
	path := s.ProcPath + "/meminfo"
	data, err := s.Reader(path)
	if err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, ":")
		if !ok || key != "MemTotal" {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return 0, fmt.Errorf("%s: MemTotal empty value", path)
		}
		v, perr := strconv.ParseInt(fields[0], 10, 64)
		if perr != nil {
			return 0, fmt.Errorf("%s: MemTotal parse %q: %w", path, fields[0], perr)
		}
		return v, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("%s: scan: %w", path, err)
	}
	return 0, fmt.Errorf("%s: MemTotal field not found", path)
}

// readStat parses /proc/<pid>/stat and returns (utime, stime, comm, ok).
// ok=false signals "skip this PID": ENOENT (process exited mid-walk, logged
// at debug) or any other read/parse error (logged at warn).
//
// /proc/[pid]/stat layout per man 5 proc: "pid (comm) state ppid pgrp …".
// comm can contain spaces, parens, or really anything except a newline, so
// the only safe split point is the LAST ')' in the buffer. Everything after
// it is the well-formed numeric suffix; within that suffix utime is
// zero-indexed offset 11 and stime is offset 12.
func (s *Service) readStat(pid int) (utime, stime uint64, comm string, ok bool) {
	path := fmt.Sprintf("%s/%d/stat", s.ProcPath, pid)
	data, err := s.Reader(path)
	if err != nil {
		s.logPIDErr(pid, "stat", err)
		return 0, 0, "", false
	}
	line := strings.TrimRight(string(data), "\n")

	open := strings.IndexByte(line, '(')
	close := strings.LastIndexByte(line, ')')
	if open < 0 || close < 0 || close <= open {
		slog.Warn("processes: malformed /proc/[pid]/stat", "pid", pid, "line", line)
		return 0, 0, "", false
	}
	comm = line[open+1 : close]

	suffix := strings.TrimSpace(line[close+1:])
	fields := strings.Fields(suffix)
	// Need state (0) through stime (12) — at least 13 fields.
	if len(fields) < 13 {
		slog.Warn("processes: /proc/[pid]/stat suffix too short", "pid", pid, "fields", len(fields))
		return 0, 0, "", false
	}
	utime, err = strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		slog.Warn("processes: parse utime", "pid", pid, "err", err)
		return 0, 0, "", false
	}
	stime, err = strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		slog.Warn("processes: parse stime", "pid", pid, "err", err)
		return 0, 0, "", false
	}
	return utime, stime, comm, true
}

// readStatus parses /proc/<pid>/status for the real uid (first field of the
// `Uid:` line) and VmRSS (in kB). Kernel threads have no VmRSS line — we
// return vmRSSkB=0 for those rather than failing. ok=false signals "skip
// this PID" with the same ENOENT vs warn distinction as readStat.
func (s *Service) readStatus(pid int) (uidStr string, vmRSSkB int64, ok bool) {
	path := fmt.Sprintf("%s/%d/status", s.ProcPath, pid)
	data, err := s.Reader(path)
	if err != nil {
		s.logPIDErr(pid, "status", err)
		return "", 0, false
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var gotUID bool
	for scanner.Scan() {
		line := scanner.Text()
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch key {
		case "Uid":
			// "Uid:\t<real>\t<eff>\t<saved>\t<filesystem>"
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			uidStr = fields[0]
			gotUID = true
		case "VmRSS":
			fields := strings.Fields(value)
			if len(fields) == 0 {
				continue
			}
			v, perr := strconv.ParseInt(fields[0], 10, 64)
			if perr != nil {
				slog.Warn("processes: parse VmRSS", "pid", pid, "err", perr)
				continue
			}
			vmRSSkB = v
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("processes: scan /proc/[pid]/status", "pid", pid, "err", err)
		return "", 0, false
	}
	if !gotUID {
		slog.Warn("processes: /proc/[pid]/status missing Uid line", "pid", pid)
		return "", 0, false
	}
	return uidStr, vmRSSkB, true
}

// readCmdline reads /proc/<pid>/cmdline (null-separated argv) and returns it
// as a single space-separated string. Kernel threads have an empty cmdline;
// for those we fall back to "[comm]" — the same convention ps(1) uses — so
// the row still has something human-readable to display.
//
// ok=false signals "skip this PID" with the ENOENT vs warn distinction.
func (s *Service) readCmdline(pid int, comm string) (string, bool) {
	path := fmt.Sprintf("%s/%d/cmdline", s.ProcPath, pid)
	data, err := s.Reader(path)
	if err != nil {
		s.logPIDErr(pid, "cmdline", err)
		return "", false
	}
	// Strip a single trailing null (the kernel terminates argv with one)
	// before replacing the separators — otherwise the result has a
	// dangling space.
	data = bytes.TrimRight(data, "\x00")
	if len(data) == 0 {
		return "[" + comm + "]", true
	}
	out := bytes.ReplaceAll(data, []byte{0}, []byte{' '})
	return strings.TrimSpace(string(out)), true
}

// logPIDErr distinguishes the PID-exited-mid-walk race (ENOENT, expected and
// noisy at warn level) from real read failures. The race is logged at debug
// so an operator can opt into seeing it without filling the journal under
// normal load.
func (s *Service) logPIDErr(pid int, which string, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		slog.Debug("processes: pid disappeared mid-walk", "pid", pid, "file", which)
		return
	}
	slog.Warn("processes: read /proc/[pid] failed", "pid", pid, "file", which, "err", err)
}
