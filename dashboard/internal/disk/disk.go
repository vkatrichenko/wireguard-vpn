// Package disk reports mounted-filesystem usage on the WireGuard EC2 host by
// parsing /proc/mounts and calling unix.Statfs against each mountpoint.
//
// The dashboard cares about real disk pressure — the root volume, any attached
// data volumes — and emphatically not about the dozens of kernel pseudo
// filesystems that show up in /proc/mounts. Sample therefore filters by fstype
// and only keeps mounts whose backing storage represents bytes on a block
// device. Specifically, it drops the following fstypes:
//
//	tmpfs, devtmpfs, overlay, squashfs, proc, sysfs, debugfs, tracefs, and
//	any fstype starting with "cgroup" (covers both "cgroup" and "cgroup2").
//
// The Used/Total fields are computed from Statfs as follows:
//
//	Total = Blocks * Bsize
//	Avail = Bavail * Bsize          // NOT Bfree
//	Used  = Total - Avail
//
// Bavail is deliberately preferred over Bfree: Bfree includes the kernel's
// reserved-block pool (typically 5% on ext4) that an unprivileged user — or
// our wireguard-dashboard service user — cannot actually write into. Using
// Bfree would overstate available space and understate "used %", which is
// exactly the metric an operator is watching to decide whether to grow the
// volume.
//
// Statfs failures on an individual mountpoint (broken bind mount, stale NFS
// handle, permission-denied on a userns mount) are logged at warn level and
// the offending row is skipped. The Sample call still succeeds with the other
// rows — partial degradation matches the precedent set by internal/proc and
// internal/wg, where one bad subsystem does not blank the whole card.
//
// Portability: this package compiles cleanly on macOS (golang.org/x/sys/unix
// has a Darwin implementation of Statfs_t with compatible field names) but
// Sample will error there because /proc/mounts does not exist. Production
// runs on linux/amd64; tests in the sibling sub-task inject a fake Reader and
// fake Statfs.
package disk

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// DefaultMountsPath is the kernel-exported mount table consumed by Sample.
// Hardcoded to the canonical Linux path; tests override it via Service.MountsPath.
const DefaultMountsPath = "/proc/mounts"

// Mount is one row of the disk-usage report, scoped to a single real
// filesystem after pseudo filesystems have been filtered out. Bytes are
// reported in absolute units so the template layer can format with whichever
// unit (KiB / MiB / GiB) the card calls for. PctFull is rounded to one
// decimal so the UI does not waste real estate on noise digits.
type Mount struct {
	Path    string  `json:"path"`     // e.g. "/", "/var/lib/docker"
	FsType  string  `json:"fs_type"`  // e.g. "ext4", "xfs"
	Used    int64   `json:"used"`     // bytes
	Total   int64   `json:"total"`    // bytes
	PctFull float64 `json:"pct_full"` // 0.0-100.0, one decimal precision
}

// readFunc reads the entire contents of a file at path. Mirrors os.ReadFile
// so the production wiring is a one-liner, while leaving tests free to
// substitute a closure that returns a canned /proc/mounts payload without
// touching the real filesystem. Type stays unexported; tests in the same
// package can construct closures of this shape directly.
type readFunc func(path string) ([]byte, error)

// statfsFunc wraps unix.Statfs so tests can return canned block counts for a
// given mountpoint without needing the path to actually exist on the test
// host. Kept unexported for the same reason as readFunc.
type statfsFunc func(path string, stat *unix.Statfs_t) error

// Service holds the injectable seams (Reader, Statfs, MountsPath). All three
// fields are exported so tests can construct a Service{} literal with fakes;
// production code should use New() to get a Service wired with the real
// os.ReadFile, unix.Statfs, and DefaultMountsPath.
type Service struct {
	Reader     readFunc
	Statfs     statfsFunc
	MountsPath string
}

// New returns a Service wired with the production defaults.
func New() *Service {
	return &Service{
		Reader:     os.ReadFile,
		Statfs:     unix.Statfs,
		MountsPath: DefaultMountsPath,
	}
}

// Sample reads MountsPath, filters pseudo filesystems, calls Statfs against
// each remaining mountpoint, and returns one Mount per real filesystem in
// /proc/mounts order. The kernel emits /proc/mounts in mount order, which
// places "/" first — preserving that order is what an operator expects when
// glancing at the disk card.
//
// ctx is accepted for API symmetry with the other internal/* services; the
// work performed here is fast local I/O with no point at which cancellation
// would buy anything.
func (s *Service) Sample(ctx context.Context) ([]Mount, error) {
	_ = ctx // see doc comment — ctx reserved, not used today

	data, err := s.Reader(s.MountsPath)
	if err != nil {
		return nil, fmt.Errorf("read mounts: %w", err)
	}

	// /proc/mounts on this host has well under 50 entries even with every
	// pseudo fs counted; cap the initial allocation at a modest number and
	// let the slice grow naturally if a future workload mounts more.
	mounts := make([]Mount, 0, 8)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// Format: <device> <mountpoint> <fstype> <opts> <dump> <pass>
		// We only need the first three; tolerate a short line by skipping.
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mountpoint := fields[1]
		fstype := fields[2]

		if isPseudo(fstype) {
			continue
		}

		var st unix.Statfs_t
		if err := s.Statfs(mountpoint, &st); err != nil {
			slog.Warn("disk: statfs failed", "path", mountpoint, "err", err)
			continue
		}

		bsize := int64(st.Bsize)
		total := int64(st.Blocks) * bsize
		avail := int64(st.Bavail) * bsize
		used := total - avail

		var pct float64
		if total > 0 {
			pct = float64(used) / float64(total) * 100
			pct = math.Round(pct*10) / 10
		}

		mounts = append(mounts, Mount{
			Path:    mountpoint,
			FsType:  fstype,
			Used:    used,
			Total:   total,
			PctFull: pct,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", s.MountsPath, err)
	}

	return mounts, nil
}

// Threshold maps a percentage-full value to the bar's color class used by
// the disk card template. Returns "ok" (<80%), "amber" (80%–<95%), or "red"
// (≥95%). Boundary values map to the higher severity (80.0 → "amber", 95.0
// → "red").
func Threshold(pct float64) string {
	switch {
	case pct >= 95:
		return "red"
	case pct >= 80:
		return "amber"
	default:
		return "ok"
	}
}

// isPseudo reports whether the given fstype names a kernel pseudo filesystem
// that does not represent bytes on a block device. The cgroup family is
// matched by prefix because the kernel exposes both "cgroup" (v1) and
// "cgroup2" (v2) under the same mount table, and they share the "not real
// disk" property.
func isPseudo(fstype string) bool {
	switch fstype {
	case "tmpfs", "devtmpfs", "overlay", "squashfs", "proc", "sysfs", "debugfs", "tracefs":
		return true
	}
	return strings.HasPrefix(fstype, "cgroup")
}
