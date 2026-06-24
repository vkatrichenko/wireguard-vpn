package proc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeReader returns a readFunc closure that maps absolute paths to canned
// bytes. Any path that is not in the map produces an error — this mirrors how
// the production os.ReadFile behaves on a missing file and lets tests assert
// that Sample's error path wraps the offending path.
func fakeReader(files map[string][]byte) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		b, ok := files[path]
		if !ok {
			return nil, fmt.Errorf("fakeReader: no fixture for %s", path)
		}
		return b, nil
	}
}

// newTestService wires up a Service with the injected fake Reader, a
// deterministic Now closure that always returns `now`, and the same path /
// interface defaults that production New() uses. Tests that need to advance
// the clock between Sample calls swap svc.Now in place.
func newTestService(files map[string][]byte, now time.Time) *Service {
	t := now
	return &Service{
		Reader:   fakeReader(files),
		Now:      func() time.Time { return t },
		ProcPath: "/proc",
		SysPath:  "/sys",
		Iface:    "wg0",
	}
}

// realisticFixtures returns a fresh map of the four /proc + two /sys files
// that Sample touches, populated with the byte payloads the test asks for.
// The CPU line uses the canonical 10-column format; meminfo carries only the
// two keys the parser cares about plus a noise key to prove the loop skips
// unrelated lines.
func realisticFixtures(cpuLine string, memTotalKB, memAvailableKB int64, uptimeSecs string, rxBytes, txBytes int64) map[string][]byte {
	meminfo := fmt.Sprintf(
		"MemTotal:       %d kB\nMemFree:         123456 kB\nMemAvailable:    %d kB\nBuffers:           1024 kB\n",
		memTotalKB, memAvailableKB,
	)
	return map[string][]byte{
		"/proc/stat":                             []byte(cpuLine + "\nintr 0 0 0\nctxt 0\n"),
		"/proc/meminfo":                          []byte(meminfo),
		"/proc/uptime":                           []byte(uptimeSecs + " 9000.00\n"),
		"/sys/class/net/wg0/statistics/rx_bytes": []byte(fmt.Sprintf("%d\n", rxBytes)),
		"/sys/class/net/wg0/statistics/tx_bytes": []byte(fmt.Sprintf("%d\n", txBytes)),
	}
}

func TestServiceSample_FirstSampleZeroes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)

	svc := newTestService(files, now)

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("Sample() returned unexpected error: %v", err)
	}
	if got.CPUPercent != 0 {
		t.Errorf("CPUPercent = %v, want 0 (first sample)", got.CPUPercent)
	}
	if got.WgRxRateBps != 0 {
		t.Errorf("WgRxRateBps = %d, want 0 (first sample)", got.WgRxRateBps)
	}
	if got.WgTxRateBps != 0 {
		t.Errorf("WgTxRateBps = %d, want 0 (first sample)", got.WgTxRateBps)
	}
	if got.MemTotalKB != 1_000_000 {
		t.Errorf("MemTotalKB = %d, want 1000000", got.MemTotalKB)
	}
	if got.MemUsedKB != 400_000 {
		t.Errorf("MemUsedKB = %d, want 400000 (total - available)", got.MemUsedKB)
	}
	wantPct := 40.0
	if d := got.MemUsedPercent - wantPct; d > 0.001 || d < -0.001 {
		t.Errorf("MemUsedPercent = %v, want ~%v", got.MemUsedPercent, wantPct)
	}
	wantUptime := time.Duration(12345.67 * float64(time.Second))
	if got.HostUptime != wantUptime {
		t.Errorf("HostUptime = %v, want %v", got.HostUptime, wantUptime)
	}
	if got.WgRxBytesCum != 1000 {
		t.Errorf("WgRxBytesCum = %d, want 1000", got.WgRxBytesCum)
	}
	if got.WgTxBytesCum != 500 {
		t.Errorf("WgTxBytesCum = %d, want 500", got.WgTxBytesCum)
	}
}

func TestServiceSample_SecondSampleComputesDeltas(t *testing.T) {
	t.Parallel()

	now1 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	now2 := now1.Add(10 * time.Second)

	files1 := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0", // total=1000, idle+iowait=850
		1_000_000, 600_000,
		"12345.67",
		1000, 2000,
	)

	svc := newTestService(files1, now1)

	if _, err := svc.Sample(context.Background()); err != nil {
		t.Fatalf("first Sample() returned unexpected error: %v", err)
	}

	// Re-wire the Service for the second tick: new fixtures + advanced clock.
	files2 := realisticFixtures(
		"cpu 200 0 100 1700 0 0 0 0 0 0", // total=2000 (Δ=1000), idle+iowait=1700 (Δ=850)
		1_000_000, 600_000,
		"12355.67",
		11_000, 2000, // rx +10000 in 10s = 1000 Bps; tx unchanged → 0 Bps
	)
	svc.Reader = fakeReader(files2)
	svc.Now = func() time.Time { return now2 }

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("second Sample() returned unexpected error: %v", err)
	}

	// CPU%: 100 * (1 - 850/1000) = 15.0
	if d := got.CPUPercent - 15.0; d > 0.01 || d < -0.01 {
		t.Errorf("CPUPercent = %v, want ~15.0", got.CPUPercent)
	}
	if got.WgRxRateBps != 1000 {
		t.Errorf("WgRxRateBps = %d, want 1000 (10000B / 10s)", got.WgRxRateBps)
	}
	if got.WgTxRateBps != 0 {
		t.Errorf("WgTxRateBps = %d, want 0 (counter unchanged)", got.WgTxRateBps)
	}
	if got.WgRxBytesCum != 11_000 {
		t.Errorf("WgRxBytesCum = %d, want 11000", got.WgRxBytesCum)
	}
}

func TestServiceSample_CounterRollback(t *testing.T) {
	t.Parallel()

	now1 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	now2 := now1.Add(10 * time.Second)

	files1 := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		10_000, 0,
	)
	svc := newTestService(files1, now1)
	if _, err := svc.Sample(context.Background()); err != nil {
		t.Fatalf("first Sample() returned unexpected error: %v", err)
	}

	// Counter went backwards: kernel reset on interface restart.
	files2 := realisticFixtures(
		"cpu 200 0 100 1700 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12355.67",
		5000, 0,
	)
	svc.Reader = fakeReader(files2)
	svc.Now = func() time.Time { return now2 }

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("second Sample() returned unexpected error: %v", err)
	}

	if got.WgRxRateBps != 0 {
		t.Errorf("WgRxRateBps = %d, want 0 (counter rolled back, NOT a negative rate)", got.WgRxRateBps)
	}
	if got.WgRxBytesCum != 5000 {
		t.Errorf("WgRxBytesCum = %d, want 5000 (cumulative reflects current value)", got.WgRxBytesCum)
	}
}

func TestServiceSample_CPUClampedToNonNegative(t *testing.T) {
	t.Parallel()

	now1 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	now2 := now1.Add(10 * time.Second)

	// Pathological CPU counters: total stays 2000, but idle+iowait grows by
	// MORE than total grows. With u64 arithmetic the deltas wrap around
	// gracefully — but the formula 1 - idleDelta/totalDelta could still
	// produce a value outside [0,1] in floating-point. We engineer a case
	// where idleDelta > totalDelta: idle column jumps from 850 to 1850 (Δ=1000)
	// while total only grows by 1000 (sum stays 2000 because we shaved the
	// user column down to compensate). The formula then yields 1 - 1000/1000 = 0,
	// which lands at the lower clamp boundary. Even slight float drift won't
	// push it negative because the clamp triggers below zero.
	//
	// To unambiguously exercise the < 0 clamp, we go further and arrange
	// idleDelta > totalDelta strictly. Set first: cpu 100 0 50 850 0 ...
	// (total=1000, idle=850). Set second: cpu 0 0 50 1900 0 ... (total=1950,
	// idle=1900). Δtotal=950, Δidle=1050. 1 - 1050/950 = -0.105 → clamped to 0.
	files1 := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		0, 0,
	)
	svc := newTestService(files1, now1)
	if _, err := svc.Sample(context.Background()); err != nil {
		t.Fatalf("first Sample() returned unexpected error: %v", err)
	}

	files2 := realisticFixtures(
		"cpu 0 0 50 1900 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12355.67",
		0, 0,
	)
	svc.Reader = fakeReader(files2)
	svc.Now = func() time.Time { return now2 }

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("second Sample() returned unexpected error: %v", err)
	}

	if got.CPUPercent < 0 {
		t.Errorf("CPUPercent = %v, want >= 0 (clamp must catch idleDelta > totalDelta)", got.CPUPercent)
	}
	if got.CPUPercent > 100 {
		t.Errorf("CPUPercent = %v, want <= 100 (clamp must cap the upper end)", got.CPUPercent)
	}
}

func TestServiceSample_ZeroClockDelta(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)

	files1 := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	svc := newTestService(files1, now)
	if _, err := svc.Sample(context.Background()); err != nil {
		t.Fatalf("first Sample() returned unexpected error: %v", err)
	}

	// Same `now`, but the byte counters did move. dt == 0 means rates clamp
	// to 0 even though the cumulative counters advanced — guarding against a
	// divide-by-zero on dt and against NTP-stepping backward.
	files2 := realisticFixtures(
		"cpu 200 0 100 1700 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		11_000, 2000,
	)
	svc.Reader = fakeReader(files2)
	// svc.Now still returns the same `now`.

	got, err := svc.Sample(context.Background())
	if err != nil {
		t.Fatalf("second Sample() returned unexpected error: %v", err)
	}

	if got.WgRxRateBps != 0 {
		t.Errorf("WgRxRateBps = %d, want 0 (dt == 0)", got.WgRxRateBps)
	}
	if got.WgTxRateBps != 0 {
		t.Errorf("WgTxRateBps = %d, want 0 (dt == 0)", got.WgTxRateBps)
	}
}

func TestServiceSample_MemTotalZero(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		0, 0,
		"12345.67",
		1000, 500,
	)
	svc := newTestService(files, now)

	got, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatal("Sample() returned nil error, want non-nil for MemTotal=0")
	}
	if !strings.Contains(err.Error(), "MemTotal") {
		t.Errorf("error %q does not mention 'MemTotal'", err.Error())
	}
	if got != (Stats{}) {
		t.Errorf("Stats = %#v, want zero value on error", got)
	}
}

func TestServiceSample_StatReadFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	// Drop /proc/stat from the fixtures — the Reader will return an error
	// for that path.
	delete(files, "/proc/stat")

	svc := newTestService(files, now)

	got, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatal("Sample() returned nil error, want non-nil when /proc/stat read fails")
	}
	if !strings.Contains(err.Error(), "/proc/stat") {
		t.Errorf("error %q does not wrap the offending path '/proc/stat'", err.Error())
	}
	if got != (Stats{}) {
		t.Errorf("Stats = %#v, want zero value on error", got)
	}
	// Production contract: the prior-sample state must NOT be mutated when
	// the very first read in Sample fails. That keeps the next call's
	// baseline clean — the first successful Sample should still behave like
	// "first sample" and yield zero rates.
	if !svc.prior.when.IsZero() {
		t.Errorf("svc.prior.when = %v, want zero (early Sample failure must not seed prior state)", svc.prior.when)
	}
}

func TestServiceSample_ConcurrentSafety(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	svc := newTestService(files, now)

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if _, err := svc.Sample(context.Background()); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Sample() returned error: %v", err)
	}
	// We don't assert specific values here — `go test -race` is the actual
	// detector. Reaching this point without a race report or a panic is the
	// success condition.
}

func TestServiceSample_CPULineMissing(t *testing.T) {
	t.Parallel()

	// /proc/stat with no `cpu ` aggregate line — the parser must surface a
	// hard error rather than silently returning zeros.
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"intr 0 0 0\nctxt 999\n",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	// realisticFixtures already wired a cpu line into /proc/stat; stomp it.
	files["/proc/stat"] = []byte("intr 0 0 0\nctxt 999\n")

	svc := newTestService(files, now)
	if _, err := svc.Sample(context.Background()); err == nil {
		t.Fatal("Sample() returned nil error, want non-nil for /proc/stat without cpu aggregate line")
	}
}

func TestServiceSample_CPULineTooShort(t *testing.T) {
	t.Parallel()

	// Aggregate `cpu` line with fewer than 5 fields — production should
	// reject it with an error that names the field count.
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50", // only 4 fields incl. label
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	files["/proc/stat"] = []byte("cpu 100 0 50\n")

	svc := newTestService(files, now)
	_, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatal("Sample() returned nil error, want non-nil for short cpu line")
	}
	if !strings.Contains(err.Error(), "cpu line") {
		t.Errorf("error %q does not mention 'cpu line'", err.Error())
	}
}

func TestServiceSample_CPULineNonNumeric(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	files["/proc/stat"] = []byte("cpu abc 0 50 850 0 0 0 0 0 0\n")

	svc := newTestService(files, now)
	if _, err := svc.Sample(context.Background()); err == nil {
		t.Fatal("Sample() returned nil error, want non-nil for non-numeric cpu field")
	}
}

func TestServiceSample_MemAvailableMissing(t *testing.T) {
	t.Parallel()

	// MemTotal present, MemAvailable absent — the parser tracks both flags
	// and surfaces a specific error for the missing one.
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	files["/proc/meminfo"] = []byte("MemTotal:       1000000 kB\nMemFree:         123456 kB\n")

	svc := newTestService(files, now)
	_, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatal("Sample() returned nil error, want non-nil for missing MemAvailable")
	}
	if !strings.Contains(err.Error(), "MemAvailable") {
		t.Errorf("error %q does not mention 'MemAvailable'", err.Error())
	}
}

func TestServiceSample_MemValueNonNumeric(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	files["/proc/meminfo"] = []byte("MemTotal:       notanumber kB\nMemAvailable:    600000 kB\n")

	svc := newTestService(files, now)
	if _, err := svc.Sample(context.Background()); err == nil {
		t.Fatal("Sample() returned nil error, want non-nil for non-numeric MemTotal value")
	}
}

func TestServiceSample_UptimeMalformed(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	files["/proc/uptime"] = []byte("notafloat\n")

	svc := newTestService(files, now)
	if _, err := svc.Sample(context.Background()); err == nil {
		t.Fatal("Sample() returned nil error, want non-nil for non-numeric uptime")
	}
}

func TestServiceSample_IfaceCounterMalformed(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	files := realisticFixtures(
		"cpu 100 0 50 850 0 0 0 0 0 0",
		1_000_000, 600_000,
		"12345.67",
		1000, 500,
	)
	files["/sys/class/net/wg0/statistics/rx_bytes"] = []byte("garbage\n")

	svc := newTestService(files, now)
	_, err := svc.Sample(context.Background())
	if err == nil {
		t.Fatal("Sample() returned nil error, want non-nil for non-numeric rx_bytes")
	}
	if !strings.Contains(err.Error(), "rx_bytes") {
		t.Errorf("error %q does not wrap the offending path 'rx_bytes'", err.Error())
	}
}

func TestNew_DefaultsAreSet(t *testing.T) {
	t.Parallel()

	svc := New()
	if svc == nil {
		t.Fatal("New() returned nil")
	}
	if svc.Reader == nil {
		t.Error("New().Reader is nil; expected default os.ReadFile")
	}
	if svc.Now == nil {
		t.Error("New().Now is nil; expected default time.Now")
	}
	if svc.ProcPath != "/proc" {
		t.Errorf("New().ProcPath = %q, want %q", svc.ProcPath, "/proc")
	}
	if svc.SysPath != "/sys" {
		t.Errorf("New().SysPath = %q, want %q", svc.SysPath, "/sys")
	}
	if svc.Iface != "wg0" {
		t.Errorf("New().Iface = %q, want %q", svc.Iface, "wg0")
	}
	// Intentionally do NOT invoke Sample() — that would hit the real /proc
	// and /sys, which don't exist on macOS dev machines and would shape
	// behavior we don't want a unit test to depend on.
}
