package processes_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"wireguard-dashboard/internal/processes"
)

// procDir is the test scaffolding that owns a temp /proc tree. Methods write
// the needed files at the right paths so each test reads as a recipe, not a
// stack of WriteFile calls.
type procDir struct {
	t    *testing.T
	root string
}

func newProcDir(t *testing.T) *procDir {
	t.Helper()
	return &procDir{t: t, root: t.TempDir()}
}

// writeStat writes the aggregate "cpu " line to <root>/stat. fields contains the
// per-state jiffies (user, nice, system, idle, iowait, …) — the package sums all
// of them to obtain totalJiffies.
func (p *procDir) writeStat(fields []uint64) {
	p.t.Helper()
	parts := make([]string, 0, len(fields)+1)
	parts = append(parts, "cpu")
	for _, f := range fields {
		parts = append(parts, fmt.Sprintf("%d", f))
	}
	line := strings.Join(parts, " ") + "\n"
	if err := os.WriteFile(filepath.Join(p.root, "stat"), []byte(line), 0o644); err != nil {
		p.t.Fatalf("write /proc/stat: %v", err)
	}
}

func (p *procDir) writeMeminfo(memTotalKB int) {
	p.t.Helper()
	body := fmt.Sprintf("MemTotal: %d kB\n", memTotalKB)
	if err := os.WriteFile(filepath.Join(p.root, "meminfo"), []byte(body), 0o644); err != nil {
		p.t.Fatalf("write /proc/meminfo: %v", err)
	}
}

// writePID seeds /proc/<pid>/{stat,status,cmdline}. comm is the process name
// (wrapped in parens in the stat file). utime + stime populate the 12th/13th
// fields of the numeric suffix. uid populates the status Uid line; rssKB
// populates VmRSS. cmdline is the display string (spaces become NULs on disk
// so the parse-side reverse-mapping is exercised); pass "" to write an empty
// cmdline file (kernel-thread shape).
func (p *procDir) writePID(pid int, comm string, utime, stime uint64, uid int, rssKB int, cmdline string) {
	p.t.Helper()
	dir := filepath.Join(p.root, fmt.Sprintf("%d", pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		p.t.Fatalf("mkdir %s: %v", dir, err)
	}

	// stat layout: pid (comm) state ppid pgrp session tty_nr tpgid flags
	//   minflt cminflt majflt cmajflt utime stime ... — package reads up to
	//   index 12 (stime) of the numeric suffix so 13 numeric fields suffice.
	statLine := fmt.Sprintf("%d (%s) S 1 1 1 0 -1 0 0 0 0 0 %d %d\n", pid, comm, utime, stime)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(statLine), 0o644); err != nil {
		p.t.Fatalf("write pid %d stat: %v", pid, err)
	}

	statusBody := fmt.Sprintf("Name:\t%s\nUid:\t%d\t%d\t%d\t%d\nVmRSS:\t%d kB\n", comm, uid, uid, uid, uid, rssKB)
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(statusBody), 0o644); err != nil {
		p.t.Fatalf("write pid %d status: %v", pid, err)
	}

	// /proc/[pid]/cmdline: argv joined by NUL and terminated with NUL. Empty
	// for kernel threads.
	var cmdlineBytes []byte
	if cmdline != "" {
		cmdlineBytes = []byte(strings.ReplaceAll(cmdline, " ", "\x00") + "\x00")
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), cmdlineBytes, 0o644); err != nil {
		p.t.Fatalf("write pid %d cmdline: %v", pid, err)
	}
}

// newService wires a Service against the temp tree with os.ReadFile + a
// readdir-name wrapper. Pass a nil lookup to exercise the LookupUser-nil seam.
func newService(p *procDir, lookup func(uid string) (string, error)) *processes.Service {
	return &processes.Service{
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
		LookupUser: lookup,
		ProcPath:   p.root,
		Now:        time.Now,
	}
}

func lookupFixed(name string) func(uid string) (string, error) {
	return func(uid string) (string, error) { return name, nil }
}

// findByPID returns the first row matching pid, or fails the test.
func findByPID(t *testing.T, rows []processes.Process, pid int) processes.Process {
	t.Helper()
	for _, r := range rows {
		if r.PID == pid {
			return r
		}
	}
	t.Fatalf("pid %d not found in %d rows", pid, len(rows))
	return processes.Process{}
}

func TestSample_FirstSampleZeroCPU(t *testing.T) {
	p := newProcDir(t)
	p.writeStat([]uint64{100, 0, 50, 1000, 0, 0, 0, 0, 0, 0}) // total = 1150
	p.writeMeminfo(8_000_000)

	p.writePID(101, "nginx", 40, 10, 33, 4096, "nginx -g daemon off;")
	p.writePID(202, "redis-server", 80, 20, 100, 8192, "redis-server *:6379")
	p.writePID(303, "wg-dashboard", 30, 5, 1000, 2048, "/usr/local/bin/wireguard-dashboard")

	svc := newService(p, lookupFixed("alice"))
	rows, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if got, want := len(rows), 3; got != want {
		t.Fatalf("rows: got %d want %d", got, want)
	}

	for _, r := range rows {
		if r.CPUPct != 0 {
			t.Errorf("pid %d: first-sample CPUPct should be 0, got %v", r.PID, r.CPUPct)
		}
		if r.User != "alice" {
			t.Errorf("pid %d: User=%q want %q", r.PID, r.User, "alice")
		}
	}

	r101 := findByPID(t, rows, 101)
	wantMem := float64(4096) / float64(8_000_000) * 100
	if math.Abs(r101.MemPct-wantMem) > 1e-9 {
		t.Errorf("pid 101 MemPct: got %v want %v", r101.MemPct, wantMem)
	}
	if r101.Command != "nginx -g daemon off;" {
		t.Errorf("pid 101 Command: got %q", r101.Command)
	}
	if r101.CmdlineFull != "nginx -g daemon off;" {
		t.Errorf("pid 101 CmdlineFull: got %q", r101.CmdlineFull)
	}
}

func TestSample_DeltaCPUAcrossTwoSnapshots(t *testing.T) {
	p := newProcDir(t)

	// Snapshot 1: total = 1150.
	p.writeStat([]uint64{100, 0, 50, 1000, 0, 0, 0, 0, 0, 0})
	p.writeMeminfo(8_000_000)

	// utime+stime values: 100, 200, 300.
	p.writePID(1, "a", 60, 40, 1, 1024, "cmd-a")
	p.writePID(2, "b", 100, 100, 2, 1024, "cmd-b")
	p.writePID(3, "c", 150, 150, 3, 1024, "cmd-c")

	svc := newService(p, lookupFixed("alice"))
	if _, err := svc.Sample(context.Background()); err != nil {
		t.Fatalf("warmup Sample: %v", err)
	}

	// Snapshot 2: total = 2200, delta = 1050. Per-PID deltas 200/300/300.
	p.writeStat([]uint64{200, 0, 100, 1900, 0, 0, 0, 0, 0, 0})
	p.writePID(1, "a", 200, 100, 1, 1024, "cmd-a") // 300 = +200
	p.writePID(2, "b", 250, 250, 2, 1024, "cmd-b") // 500 = +300
	p.writePID(3, "c", 300, 300, 3, 1024, "cmd-c") // 600 = +300

	rows, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if got := len(rows); got != 3 {
		t.Fatalf("rows: got %d want 3", got)
	}

	const totalDelta = 1050.0
	cpuCount := float64(runtime.NumCPU())
	expected := map[int]float64{
		1: 200.0 / totalDelta * cpuCount * 100,
		2: 300.0 / totalDelta * cpuCount * 100,
		3: 300.0 / totalDelta * cpuCount * 100,
	}
	for pid, want := range expected {
		r := findByPID(t, rows, pid)
		if math.Abs(r.CPUPct-want) > 0.01 {
			t.Errorf("pid %d CPUPct: got %v want %v (NumCPU=%d)", pid, r.CPUPct, want, runtime.NumCPU())
		}
	}
}

func TestSample_TopNOrdering(t *testing.T) {
	p := newProcDir(t)
	p.writeStat([]uint64{100, 0, 50, 1000, 0, 0, 0, 0, 0, 0})
	p.writeMeminfo(8_000_000)

	// Eight PIDs all at utime+stime = 100 for warmup.
	for pid := 1; pid <= 8; pid++ {
		p.writePID(pid, fmt.Sprintf("p%d", pid), 50, 50, 0, 1024, fmt.Sprintf("cmd-%d", pid))
	}
	svc := newService(p, lookupFixed("root"))
	if _, err := svc.Sample(context.Background()); err != nil {
		t.Fatalf("warmup Sample: %v", err)
	}

	// Snapshot 2: ample headroom in totalDelta so CPU% values are well-ordered.
	p.writeStat([]uint64{1100, 0, 1050, 1000, 0, 0, 0, 0, 0, 0}) // delta 2000
	deltas := []uint64{10, 30, 50, 70, 90, 110, 130, 150}
	for i, d := range deltas {
		pid := i + 1
		// split d arbitrarily between utime/stime
		p.writePID(pid, fmt.Sprintf("p%d", pid), 50+d/2, 50+(d-d/2), 0, 1024, fmt.Sprintf("cmd-%d", pid))
	}

	rows, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if got := len(rows); got != processes.TopN {
		t.Fatalf("rows: got %d want %d", got, processes.TopN)
	}

	// Expected top-5 (descending delta): pids 8,7,6,5,4.
	wantPIDs := []int{8, 7, 6, 5, 4}
	for i, pid := range wantPIDs {
		if rows[i].PID != pid {
			t.Errorf("rows[%d].PID = %d, want %d (full ordering: %v)", i, rows[i].PID, pid, pidsOf(rows))
		}
	}
	// CPU% must be strictly non-increasing.
	for i := 1; i < len(rows); i++ {
		if rows[i].CPUPct > rows[i-1].CPUPct {
			t.Errorf("ordering broken at %d: %v > %v", i, rows[i].CPUPct, rows[i-1].CPUPct)
		}
	}
}

func pidsOf(rows []processes.Process) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.PID
	}
	return out
}

func TestSample_ENOENTTolerance(t *testing.T) {
	// One sub-test per per-PID file (status, cmdline, stat). The reader
	// wrapper returns ENOENT only for the targeted file of pid 2 and falls
	// through to os.ReadFile for everything else; this models the
	// PID-exited-mid-walk race without mutating the FS between phases.
	cases := []struct {
		name      string
		fileMatch string
	}{
		{"status", "/2/status"},
		{"cmdline", "/2/cmdline"},
		{"stat", "/2/stat"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newProcDir(t)
			p.writeStat([]uint64{100, 0, 50, 1000, 0, 0, 0, 0, 0, 0})
			p.writeMeminfo(8_000_000)
			p.writePID(1, "a", 40, 10, 1, 1024, "cmd-a")
			p.writePID(2, "b", 80, 20, 2, 1024, "cmd-b")
			p.writePID(3, "c", 30, 5, 3, 1024, "cmd-c")

			svc := newService(p, lookupFixed("u"))
			svc.Reader = func(path string) ([]byte, error) {
				if strings.HasSuffix(path, tc.fileMatch) {
					return nil, os.ErrNotExist
				}
				return os.ReadFile(path)
			}

			rows, err := svc.Sample(context.Background())
			if err != nil {
				t.Fatalf("Sample: %v", err)
			}
			if got := len(rows); got != 2 {
				t.Fatalf("rows: got %d want 2 (pid 2 must be skipped); rows=%v", got, pidsOf(rows))
			}
			for _, r := range rows {
				if r.PID == 2 {
					t.Errorf("pid 2 should have been skipped on ENOENT of %s", tc.fileMatch)
				}
			}
		})
	}
}

func TestSample_KernelThreadCmdline(t *testing.T) {
	p := newProcDir(t)
	p.writeStat([]uint64{100, 0, 50, 1000, 0, 0, 0, 0, 0, 0})
	p.writeMeminfo(8_000_000)
	// Empty cmdline file — package falls back to "[comm]".
	p.writePID(2, "kthreadd", 0, 0, 0, 0, "")

	svc := newService(p, lookupFixed("root"))
	rows, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	r := findByPID(t, rows, 2)
	if r.Command != "[kthreadd]" {
		t.Errorf("Command: got %q want %q", r.Command, "[kthreadd]")
	}
	if r.CmdlineFull != "[kthreadd]" {
		t.Errorf("CmdlineFull: got %q want %q", r.CmdlineFull, "[kthreadd]")
	}
}

func TestSample_MeminfoMissing(t *testing.T) {
	p := newProcDir(t)
	p.writeStat([]uint64{100, 0, 50, 1000, 0, 0, 0, 0, 0, 0})
	// No meminfo file.
	p.writePID(1, "a", 50, 50, 0, 4096, "cmd-a")

	svc := newService(p, lookupFixed("u"))
	rows, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	r := findByPID(t, rows, 1)
	if r.MemPct != 0 {
		t.Errorf("MemPct: got %v want 0 (meminfo missing should soft-fail)", r.MemPct)
	}
}

func TestSample_ProcStatMissing(t *testing.T) {
	p := newProcDir(t)
	// No /proc/stat file.
	p.writeMeminfo(8_000_000)
	p.writePID(1, "a", 50, 50, 0, 1024, "cmd-a")

	svc := newService(p, lookupFixed("u"))
	rows, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatalf("Sample: want error, got nil (rows=%v)", rows)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err: got %v, want wrap of os.ErrNotExist", err)
	}
	if rows != nil {
		t.Errorf("rows: got %v want nil", rows)
	}
}

func TestSample_UserLookupFallback(t *testing.T) {
	p := newProcDir(t)
	p.writeStat([]uint64{100, 0, 50, 1000, 0, 0, 0, 0, 0, 0})
	p.writeMeminfo(8_000_000)
	p.writePID(7, "x", 10, 10, 12345, 1024, "cmd-x")

	svc := newService(p, func(uid string) (string, error) {
		return "", fmt.Errorf("no such user")
	})
	rows, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	r := findByPID(t, rows, 7)
	if r.User != "12345" {
		t.Errorf("User: got %q want %q (numeric uid fallback)", r.User, "12345")
	}
}

func TestSample_NilLookupUserSeam(t *testing.T) {
	p := newProcDir(t)
	p.writeStat([]uint64{100, 0, 50, 1000, 0, 0, 0, 0, 0, 0})
	p.writeMeminfo(8_000_000)
	p.writePID(7, "x", 10, 10, 12345, 1024, "cmd-x")

	svc := newService(p, nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil LookupUser panicked: %v", r)
		}
	}()
	rows, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	r := findByPID(t, rows, 7)
	if r.User != "12345" {
		t.Errorf("User: got %q want %q (numeric uid fallback when LookupUser=nil)", r.User, "12345")
	}
}

func TestSample_CommandTruncation(t *testing.T) {
	p := newProcDir(t)
	p.writeStat([]uint64{100, 0, 50, 1000, 0, 0, 0, 0, 0, 0})
	p.writeMeminfo(8_000_000)

	longCmd := strings.Repeat("a", 100)
	exactCmd := strings.Repeat("b", processes.CmdlineDisplayLimit) // 60 chars
	p.writePID(1, "long", 10, 10, 0, 1024, longCmd)
	p.writePID(2, "exact", 10, 10, 0, 1024, exactCmd)

	svc := newService(p, lookupFixed("u"))
	rows, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}

	rLong := findByPID(t, rows, 1)
	wantDisplay := strings.Repeat("a", processes.CmdlineDisplayLimit) + "…"
	if rLong.Command != wantDisplay {
		t.Errorf("long Command: got %q want %q", rLong.Command, wantDisplay)
	}
	if rLong.CmdlineFull != longCmd {
		t.Errorf("long CmdlineFull: got %q want full 100-char string", rLong.CmdlineFull)
	}

	rExact := findByPID(t, rows, 2)
	if rExact.Command != exactCmd {
		t.Errorf("exact Command: got %q want %q (no ellipsis at exact boundary)", rExact.Command, exactCmd)
	}
	if strings.Contains(rExact.Command, "…") {
		t.Errorf("exact Command must not contain ellipsis at boundary; got %q", rExact.Command)
	}
}
