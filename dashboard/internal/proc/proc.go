// Package proc reads system metrics directly from /proc and /sys on the
// WireGuard EC2 host: CPU utilisation, memory pressure, host uptime, and the
// cumulative + per-second byte counters of the wg0 interface. The dashboard
// binary runs natively on the host (no container, no /host/proc bind mounts),
// so the default ProcPath/SysPath are the real "/proc" and "/sys".
//
// CPU% and the network byte-rate fields are inherently delta measurements:
// they require a previous sample to subtract from. Service therefore retains
// the prior sample under a sync.Mutex. The first call to Sample returns
// CPUPercent=0 and rate fields zero — the second and subsequent calls compute
// deltas against the prior reading. Callers that want meaningful values on
// the first render must invoke Sample twice (the dashboard's snapshot
// endpoint does this, separated by ~1s).
//
// All side-effecting seams are exposed as exported fields on Service so the
// unit tests in the sibling sub-task can substitute fakes:
//
//   - Reader  — file reader, defaults to os.ReadFile.
//   - Now     — clock, defaults to time.Now (used to compute dt for rates).
//   - ProcPath / SysPath — filesystem roots, default "/proc" and "/sys".
//   - Iface   — WireGuard interface name for the statistics path, default "wg0".
//
// Production code should use New() to get a Service wired with these defaults.
//
// Note on portability: this package compiles cleanly on macOS (the dev
// machine for this repo is an M-series Mac) but Sample() will error there
// because /proc and /sys don't exist. Production runs on linux/amd64; tests
// inject a fake Reader.
package proc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultIface is the WireGuard interface whose byte counters are sampled by
// default, matching the wg-quick@wg0 unit that cloud-init enables.
const DefaultIface = "wg0"

// Stats is the public output shape returned by Sample. Field semantics:
//
//   - CPUPercent: 0 on the first sample (no prior to delta against), then
//     the busy fraction of /proc/stat's `cpu` aggregate line scaled to
//     [0, 100].
//   - MemUsedKB / MemUsedPercent: derived from MemTotal − MemAvailable so
//     they reflect what's actually under pressure, not "MemTotal − MemFree"
//     (which double-counts cache as in-use).
//   - HostUptime: seconds-since-boot from /proc/uptime, marshalled as Go's
//     default time.Duration (nanoseconds as a JSON number).
//   - WgRx/TxBytesCum: cumulative bytes since the wg0 interface came up.
//   - WgRx/TxRateBps: bytes-per-second over the interval since the prior
//     sample. Zero on the first sample, and zero if the kernel counter
//     went backward (interface restart) — never negative.
type Stats struct {
	CPUPercent     float64       `json:"cpu_percent"`      // 0-100; 0 on first sample
	MemUsedPercent float64       `json:"mem_used_percent"` // 0-100
	MemTotalKB     int64         `json:"mem_total_kb"`
	MemUsedKB      int64         `json:"mem_used_kb"`
	HostUptime     time.Duration `json:"host_uptime"`     // since boot, marshals as nanoseconds (Go default)
	WgRxBytesCum   int64         `json:"wg_rx_bytes_cum"` // cumulative since interface up
	WgTxBytesCum   int64         `json:"wg_tx_bytes_cum"`
	WgRxRateBps    int64         `json:"wg_rx_rate_bps"` // bytes-per-second since prior sample; 0 on first sample
	WgTxRateBps    int64         `json:"wg_tx_rate_bps"`
}

// readFunc reads the entire contents of a file at path. Mirrors os.ReadFile
// so the production wiring is a one-liner, while leaving tests free to
// substitute a closure that returns canned bytes for /proc and /sys files
// without touching the real filesystem. Type stays unexported; tests in the
// same package can construct closures of this shape directly.
type readFunc func(path string) ([]byte, error)

// priorSample is the unexported state Service holds between Sample calls so
// it can compute CPU% and byte-rate deltas. A zero `when` is the first-sample
// sentinel: when prior.when.IsZero(), Sample returns deltas as 0 instead of
// trying to subtract against a non-existent baseline.
type priorSample struct {
	when     time.Time
	cpuTotal uint64
	cpuIdle  uint64
	rxBytes  int64
	txBytes  int64
}

// Service holds the injectable seams (Reader, Now, ProcPath, SysPath, Iface)
// and the prior-sample state needed for delta-based metrics. The seam fields
// are exported so tests can construct a Service{} literal with fakes;
// production code should use New() to get the real implementation.
//
// The mutex serialises the read-and-update of `prior` so concurrent Sample
// calls never race the delta computation. Sample is the only public method
// and it takes the lock for its full duration — the package isn't meant to
// be hot-pathed; the dashboard polls it at ~1Hz at most.
type Service struct {
	Reader   readFunc         // os.ReadFile by default
	Now      func() time.Time // time.Now by default
	ProcPath string           // "/proc"
	SysPath  string           // "/sys"
	Iface    string           // "wg0"

	mu    sync.Mutex
	prior priorSample
}

// New returns a Service wired with the production defaults: os.ReadFile,
// time.Now, the real /proc and /sys filesystem roots, and the wg0 interface.
func New() *Service {
	return &Service{
		Reader:   os.ReadFile,
		Now:      time.Now,
		ProcPath: "/proc",
		SysPath:  "/sys",
		Iface:    DefaultIface,
	}
}

// Sample reads the current /proc and /sys snapshot, computes CPU% and byte
// rates against the prior sample held on the Service, updates that prior
// sample, and returns the derived Stats.
//
// First call: CPUPercent and rate fields are 0 (no prior sample to subtract
// from). Subsequent calls: CPUPercent uses the busy/total ratio of the
// /proc/stat delta; rates use (counter delta) / (clock delta in seconds).
//
// The whole read-compute-update sequence runs under s.mu so concurrent
// callers can't read a half-updated prior sample.
func (s *Service) Sample(ctx context.Context) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cpuTotal, cpuIdle, err := s.readCPU()
	if err != nil {
		return Stats{}, err
	}

	memTotalKB, memAvailableKB, err := s.readMem()
	if err != nil {
		return Stats{}, err
	}
	if memTotalKB == 0 {
		return Stats{}, errors.New("/proc/meminfo: MemTotal is zero or missing")
	}
	memUsedKB := memTotalKB - memAvailableKB
	memUsedPercent := float64(memUsedKB) / float64(memTotalKB) * 100

	uptime, err := s.readUptime()
	if err != nil {
		return Stats{}, err
	}

	rxBytes, err := s.readIfaceCounter("rx_bytes")
	if err != nil {
		return Stats{}, err
	}
	txBytes, err := s.readIfaceCounter("tx_bytes")
	if err != nil {
		return Stats{}, err
	}

	now := s.Now()

	var (
		cpuPercent  float64
		rxRateBps   int64
		txRateBps   int64
		firstSample = s.prior.when.IsZero()
	)

	if !firstSample {
		// CPU%: idle and total counters are monotonically increasing
		// jiffies; the busy fraction over the interval is
		// 1 − (idleDelta / totalDelta). totalDelta == 0 is impossible
		// in practice (the kernel ticks at least HZ between samples)
		// but we guard against it defensively rather than divide by 0.
		cpuTotalDelta := cpuTotal - s.prior.cpuTotal
		cpuIdleDelta := cpuIdle - s.prior.cpuIdle
		if cpuTotalDelta > 0 {
			cpuPercent = 100 * (1 - float64(cpuIdleDelta)/float64(cpuTotalDelta))
			if cpuPercent < 0 {
				cpuPercent = 0
			} else if cpuPercent > 100 {
				cpuPercent = 100
			}
		}

		// Byte rates: counter delta divided by wall-clock delta. dt<=0
		// is defensive against clock skew (NTP step backwards); we'd
		// rather flatline to 0 than emit a nonsense huge rate.
		dt := now.Sub(s.prior.when).Seconds()
		if dt > 0 {
			// If the kernel counter went BACKWARD (interface
			// restart resets the byte counters to 0), report 0
			// rate, NOT a negative number — a negative rate would
			// mislead the operator. The cumulative field will
			// show the new low value on its own.
			if d := rxBytes - s.prior.rxBytes; d > 0 {
				rxRateBps = int64(float64(d) / dt)
			}
			if d := txBytes - s.prior.txBytes; d > 0 {
				txRateBps = int64(float64(d) / dt)
			}
		}
	}

	s.prior = priorSample{
		when:     now,
		cpuTotal: cpuTotal,
		cpuIdle:  cpuIdle,
		rxBytes:  rxBytes,
		txBytes:  txBytes,
	}

	return Stats{
		CPUPercent:     cpuPercent,
		MemUsedPercent: memUsedPercent,
		MemTotalKB:     memTotalKB,
		MemUsedKB:      memUsedKB,
		HostUptime:     uptime,
		WgRxBytesCum:   rxBytes,
		WgTxBytesCum:   txBytes,
		WgRxRateBps:    rxRateBps,
		WgTxRateBps:    txRateBps,
	}, nil
}

// readCPU parses /proc/stat and returns (total, idle) jiffies for the
// aggregate `cpu` line. The aggregate line is the first line and starts with
// the literal `cpu ` (with trailing space) — without the space we'd match
// `cpu0`, `cpu1`, ... per-core lines too.
//
// The aggregate line's columns are, in order:
//
//	user nice system idle iowait irq softirq steal guest guest_nice
//
// "Idle time" for CPU% is conventionally idle+iowait — both are time the CPU
// was waiting and not doing useful work. "Total time" is the sum of every
// column.
func (s *Service) readCPU() (total, idle uint64, err error) {
	path := s.ProcPath + "/stat"
	data, err := s.Reader(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", path, err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		// Fields: ["cpu", user, nice, system, idle, iowait, irq, softirq, steal, guest, guest_nice]
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, 0, fmt.Errorf("%s: cpu line has %d fields, want >=5: %q", path, len(fields), line)
		}
		var sum uint64
		var idleField, iowaitField uint64
		for i, raw := range fields[1:] {
			v, perr := strconv.ParseUint(raw, 10, 64)
			if perr != nil {
				return 0, 0, fmt.Errorf("%s: cpu field %d %q: %w", path, i, raw, perr)
			}
			sum += v
			switch i {
			case 3: // idle
				idleField = v
			case 4: // iowait
				iowaitField = v
			}
		}
		return sum, idleField + iowaitField, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("%s: scan: %w", path, err)
	}
	return 0, 0, fmt.Errorf("%s: no `cpu ` aggregate line", path)
}

// readMem parses /proc/meminfo and returns (MemTotal, MemAvailable) in kB.
// MemAvailable is preferred over MemFree because the kernel reclaims page
// cache on demand — "free + buff/cache that can be evicted" is the actual
// available memory, and that's what MemAvailable reports.
//
// Lines have the shape `Key:    value kB`, with variable whitespace.
func (s *Service) readMem() (total, available int64, err error) {
	path := s.ProcPath + "/meminfo"
	data, err := s.Reader(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", path, err)
	}

	var (
		gotTotal, gotAvailable bool
	)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "MemTotal":
			v, perr := parseMeminfoKB(value)
			if perr != nil {
				return 0, 0, fmt.Errorf("%s: MemTotal: %w", path, perr)
			}
			total = v
			gotTotal = true
		case "MemAvailable":
			v, perr := parseMeminfoKB(value)
			if perr != nil {
				return 0, 0, fmt.Errorf("%s: MemAvailable: %w", path, perr)
			}
			available = v
			gotAvailable = true
		}
		if gotTotal && gotAvailable {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("%s: scan: %w", path, err)
	}
	if !gotTotal {
		return 0, 0, fmt.Errorf("%s: MemTotal field not found", path)
	}
	if !gotAvailable {
		return 0, 0, fmt.Errorf("%s: MemAvailable field not found", path)
	}
	return total, available, nil
}

// parseMeminfoKB parses the `    1234 kB` portion of a meminfo line into an
// int64. The trailing unit is always "kB" on Linux but we tolerate its
// absence rather than hard-fail.
func parseMeminfoKB(value string) (int64, error) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty value")
	}
	v, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", fields[0], err)
	}
	return v, nil
}

// readUptime parses /proc/uptime — a single line of two whitespace-delimited
// floats: "<total-seconds-since-boot> <idle-seconds-since-boot>". We only
// care about the first field. The file's float format (e.g. "12345.67") maps
// cleanly to time.Duration via float64-seconds × time.Second.
func (s *Service) readUptime() (time.Duration, error) {
	path := s.ProcPath + "/uptime"
	data, err := s.Reader(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, fmt.Errorf("%s: uptime line missing seconds field: %q", path, string(data))
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("%s: parse seconds %q: %w", path, fields[0], err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// readIfaceCounter reads /sys/class/net/<Iface>/statistics/<name> and parses
// it as an int64. Each statistics file is a single integer ASCII line (with
// a trailing newline) maintained by the kernel for the interface — rx_bytes
// and tx_bytes are the cumulative byte counters since the interface came up.
func (s *Service) readIfaceCounter(name string) (int64, error) {
	path := s.SysPath + "/class/net/" + s.Iface + "/statistics/" + name
	data, err := s.Reader(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	raw := strings.TrimSpace(string(data))
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: parse %q: %w", path, raw, err)
	}
	return v, nil
}
