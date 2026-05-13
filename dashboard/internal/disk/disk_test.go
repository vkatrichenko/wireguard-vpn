package disk

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// staticReader returns a readFunc closure that always serves `payload` for
// every path; mirrors how the production os.ReadFile would behave if the
// mounts file were a fixed blob. Tests that need a missing-file error inject
// a closure returning os.ErrNotExist instead.
func staticReader(payload []byte) readFunc {
	return func(string) ([]byte, error) {
		return payload, nil
	}
}

// fixedStatfs returns a statfsFunc closure that writes the given block counts
// into *stat for every path. Used by tests that don't care which mountpoint
// is being interrogated, only that Statfs reports a known volume size.
func fixedStatfs(bsize, blocks, bavail uint64) statfsFunc {
	return func(_ string, stat *unix.Statfs_t) error {
		stat.Bsize = bsizeField(bsize)
		stat.Blocks = blocks
		stat.Bavail = bavail
		return nil
	}
}

// newTestService is the shared scaffold: injects a fake Reader, fake Statfs,
// and a synthetic MountsPath that need not exist on disk.
func newTestService(t *testing.T, r readFunc, s statfsFunc) *Service {
	t.Helper()
	return &Service{
		Reader:     r,
		Statfs:     s,
		MountsPath: "/proc/mounts",
	}
}

func TestSample_FiltersPseudoFilesystems(t *testing.T) {
	t.Parallel()

	fixture := "" +
		"/dev/nvme0n1p1 / ext4 rw,relatime 0 0\n" +
		"/dev/nvme0n1p15 /boot/efi vfat ro,relatime 0 0\n" +
		"tmpfs /run tmpfs rw,nosuid 0 0\n" +
		"devtmpfs /dev devtmpfs rw 0 0\n" +
		"proc /proc proc rw 0 0\n" +
		"sysfs /sys sysfs rw 0 0\n" +
		"cgroup2 /sys/fs/cgroup cgroup2 rw 0 0\n" +
		"overlay /var/lib/docker/overlay2/abc/merged overlay rw 0 0\n" +
		"squashfs /snap/core22/1234 squashfs ro 0 0\n" +
		"debugfs /sys/kernel/debug debugfs rw 0 0\n" +
		"tracefs /sys/kernel/tracing tracefs rw 0 0\n" +
		"/dev/sdb1 /mnt/data xfs rw 0 0\n"

	svc := newTestService(t,
		staticReader([]byte(fixture)),
		fixedStatfs(4096, 1_000_000, 500_000),
	)

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample() returned unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3; got = %#v", len(got), got)
	}

	want := []struct {
		path   string
		fsType string
	}{
		{"/", "ext4"},
		{"/boot/efi", "vfat"},
		{"/mnt/data", "xfs"},
	}
	for i, w := range want {
		if got[i].Path != w.path {
			t.Errorf("got[%d].Path = %q, want %q", i, got[i].Path, w.path)
		}
		if got[i].FsType != w.fsType {
			t.Errorf("got[%d].FsType = %q, want %q", i, got[i].FsType, w.fsType)
		}
	}

	for _, m := range got {
		switch m.FsType {
		case "tmpfs", "devtmpfs", "proc", "sysfs", "cgroup2", "overlay", "squashfs", "debugfs", "tracefs":
			t.Errorf("pseudo fstype %q leaked into results", m.FsType)
		}
	}
}

func TestSample_ComputesUsedTotalPctFull(t *testing.T) {
	t.Parallel()

	t.Run("happy path with even arithmetic", func(t *testing.T) {
		t.Parallel()
		svc := newTestService(t,
			staticReader([]byte("/dev/sda1 / ext4 rw 0 0\n")),
			fixedStatfs(4096, 2_500_000, 500_000),
		)
		got, err := svc.Sample(context.Background())
		if err != nil {
			t.Fatalf("Sample() returned unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		const wantTotal int64 = 10_240_000_000
		const wantUsed int64 = 8_192_000_000
		if got[0].Total != wantTotal {
			t.Errorf("Total = %d, want %d", got[0].Total, wantTotal)
		}
		if got[0].Used != wantUsed {
			t.Errorf("Used = %d, want %d", got[0].Used, wantUsed)
		}
		if got[0].PctFull != 80.0 {
			t.Errorf("PctFull = %v, want 80.0", got[0].PctFull)
		}
	})

	t.Run("pctFull rounds to one decimal", func(t *testing.T) {
		t.Parallel()
		// Blocks=1000, Bavail=11 → used=989, total=1000, pct=98.9% (exactly).
		svc := newTestService(t,
			staticReader([]byte("/dev/sda1 / ext4 rw 0 0\n")),
			fixedStatfs(4096, 1000, 11),
		)
		got, err := svc.Sample(context.Background())
		if err != nil {
			t.Fatalf("Sample() returned unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		if math.Abs(got[0].PctFull-98.9) > 0.01 {
			t.Errorf("PctFull = %v, want 98.9", got[0].PctFull)
		}
	})

	t.Run("zero total mount is skipped", func(t *testing.T) {
		t.Parallel()
		// Pseudo filesystems that slip past isPseudo (or any future
		// kernel-internal fs we haven't seen) always report total==0.
		// Sample drops the row outright instead of rendering "0 B / 0 B".
		svc := newTestService(t,
			staticReader([]byte("/dev/empty /mnt/empty ext4 rw 0 0\n")),
			fixedStatfs(4096, 0, 0),
		)
		got, err := svc.Sample(context.Background())
		if err != nil {
			t.Fatalf("Sample() returned unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("len(got) = %d, want 0; got = %#v", len(got), got)
		}
	})
}

func TestSample_SkipsExpandedPseudoFilesystems(t *testing.T) {
	t.Parallel()

	// The fstype-level denylist should drop these BEFORE Statfs is called,
	// matching the screenshot from the production EC2 box where these mounts
	// were leaking through the disk card.
	fixture := "" +
		"/dev/sda1 / ext4 rw 0 0\n" +
		"securityfs /sys/kernel/security securityfs rw 0 0\n" +
		"devpts /dev/pts devpts rw 0 0\n" +
		"pstore /sys/fs/pstore pstore rw 0 0\n" +
		"efivarfs /sys/firmware/efi/efivars efivarfs rw 0 0\n" +
		"bpf /sys/fs/bpf bpf rw 0 0\n" +
		"systemd-1 /proc/sys/fs/binfmt_misc autofs rw 0 0\n" +
		"hugetlbfs /dev/hugepages hugetlbfs rw 0 0\n" +
		"mqueue /dev/mqueue mqueue rw 0 0\n" +
		"configfs /sys/kernel/config configfs rw 0 0\n"

	statfs := func(path string, stat *unix.Statfs_t) error {
		if path != "/" {
			t.Errorf("Statfs called for pseudo-fs path %q; isPseudo should have filtered it", path)
			return nil
		}
		stat.Bsize = bsizeField(4096)
		stat.Blocks = 250_000
		stat.Bavail = 100_000
		return nil
	}

	svc := newTestService(t, staticReader([]byte(fixture)), statfs)

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample() returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1; got = %#v", len(got), got)
	}
	if got[0].Path != "/" || got[0].FsType != "ext4" {
		t.Errorf("got[0] = %+v, want Path=/, FsType=ext4", got[0])
	}
}

func TestSample_StatfsErrorSkipsRow(t *testing.T) {
	t.Parallel()

	fixture := "" +
		"/dev/sda1 / ext4 rw 0 0\n" +
		"/dev/sdb1 /mnt/broken ext4 rw 0 0\n"

	statfs := func(path string, stat *unix.Statfs_t) error {
		if path == "/mnt/broken" {
			return unix.EACCES
		}
		stat.Bsize = bsizeField(4096)
		stat.Blocks = 1000
		stat.Bavail = 500
		return nil
	}

	svc := newTestService(t, staticReader([]byte(fixture)), statfs)

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample() returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1; got = %#v", len(got), got)
	}
	if got[0].Path != "/" {
		t.Errorf("got[0].Path = %q, want %q", got[0].Path, "/")
	}
}

func TestSample_ReadError(t *testing.T) {
	t.Parallel()

	svc := newTestService(t,
		func(string) ([]byte, error) { return nil, os.ErrNotExist },
		fixedStatfs(4096, 1000, 500),
	)

	_, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatalf("Sample() returned nil error, want non-nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
}

func TestSample_DefensiveEmptyFixture(t *testing.T) {
	t.Parallel()

	statfs := func(string, *unix.Statfs_t) error {
		return fmt.Errorf("statfs should not be called for empty fixture")
	}

	svc := newTestService(t, staticReader([]byte("")), statfs)

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample() returned unexpected error: %v", err)
	}
	if got == nil {
		t.Fatalf("Sample() returned nil slice; want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

func TestSample_HandlesMalformedLines(t *testing.T) {
	t.Parallel()

	fixture := "" +
		"\n" +
		"bogus xyz\n" +
		"/dev/sda1 / ext4 rw 0 0\n"

	svc := newTestService(t,
		staticReader([]byte(fixture)),
		fixedStatfs(4096, 1000, 500),
	)

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample() returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1; got = %#v", len(got), got)
	}
	if got[0].Path != "/" || got[0].FsType != "ext4" {
		t.Errorf("got[0] = %+v, want Path=/, FsType=ext4", got[0])
	}
}

func TestThreshold(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pct  float64
		want string
	}{
		{0.0, "ok"},
		{50.0, "ok"},
		{79.9, "ok"},
		{80.0, "amber"},
		{85.0, "amber"},
		{94.9, "amber"},
		{95.0, "red"},
		{99.0, "red"},
		{100.0, "red"},
	}
	for _, c := range cases {
		c := c
		t.Run(fmt.Sprintf("%.1f", c.pct), func(t *testing.T) {
			t.Parallel()
			if got := Threshold(c.pct); got != c.want {
				t.Errorf("Threshold(%v) = %q, want %q", c.pct, got, c.want)
			}
		})
	}
}
