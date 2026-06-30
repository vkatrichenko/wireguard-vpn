package serverinfo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// validKey is a 44-character base64 placeholder (the wire-format size of a
// WireGuard public key: 32 bytes raw → 44 base64 chars including the trailing
// `=` padding). The production code only calls strings.TrimSpace on it, so
// byte content is opaque to the unit under test — only the length matters
// when we assert it stays untouched after trimming.
const validKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG="

// fakeIMDS is a hand-rolled stub for the imdsClient interface. The optional
// delay lets the cancellation test exercise the ctx.Done() branch in Service.Get
// without racing the goroutine fan-out. instanceType / az / amiID default to
// the empty string for existing callers that only care about the public-IP
// path; the IMDS-extended test below sets them explicitly.
type fakeIMDS struct {
	ip           string
	instanceType string
	az           string
	amiID        string
	vpcCIDR      string
	err          error
	delay        time.Duration
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

func (f fakeIMDS) InstanceType(ctx context.Context) (string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.instanceType, f.err
}

func (f fakeIMDS) AvailabilityZone(ctx context.Context) (string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.az, f.err
}

func (f fakeIMDS) AMIID(ctx context.Context) (string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.amiID, f.err
}

func (f fakeIMDS) VPCIPv4CIDR(ctx context.Context) (string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return f.vpcCIDR, f.err
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

	svc := New(Config{})
	if svc == nil {
		t.Fatal("New() returned nil")
	}
	if svc.IMDS == nil {
		t.Error("New().IMDS is nil; expected default httpIMDS")
	}
	if svc.Runner == nil {
		t.Error("New().Runner is nil; expected default runner")
	}
	if svc.Uname == nil {
		t.Error("New().Uname is nil; expected unix.Uname")
	}
	if svc.ReadFile == nil {
		t.Error("New().ReadFile is nil; expected os.ReadFile")
	}
	if svc.Echo == nil {
		t.Error("New().Echo is nil; expected default echo client")
	}
	if svc.EC2Probe == nil {
		t.Error("New().EC2Probe is nil; expected default EC2 probe")
	}
	// Empty Config falls back to the package defaults.
	if svc.ClientDNS != DefaultClientDNS {
		t.Errorf("New().ClientDNS = %q, want default %q", svc.ClientDNS, DefaultClientDNS)
	}
	if svc.ServerNet == "" {
		t.Error("New().ServerNet is empty; expected the wgconfig default")
	}
	// Intentionally do NOT invoke Get() — that would hit the real IMDS and
	// shell out to sudo, neither of which is appropriate in a unit test.
}

// TestKernel_Mocked drives Service.Kernel through a stubbed unameFunc that
// deposits canned C strings (with trailing NULs to mimic the real syscall
// output) into the Utsname struct. The assertions prove each field is
// surfaced AND that unix.ByteSliceToString stripped the \x00 terminator.
func TestKernel_Mocked(t *testing.T) {
	t.Parallel()

	svc := &Service{
		Uname: func(u *unix.Utsname) error {
			copy(u.Sysname[:], "Linux\x00")
			copy(u.Nodename[:], "wg-host\x00")
			copy(u.Release[:], "6.8.0-1015-aws\x00")
			copy(u.Version[:], "#16~22.04.1-Ubuntu SMP\x00")
			copy(u.Machine[:], "x86_64\x00")
			return nil
		},
	}

	got, err := svc.Kernel()
	if err != nil {
		t.Fatalf("Kernel() returned unexpected error: %v", err)
	}
	want := KernelInfo{
		Sysname:  "Linux",
		Nodename: "wg-host",
		Release:  "6.8.0-1015-aws",
		Version:  "#16~22.04.1-Ubuntu SMP",
		Machine:  "x86_64",
	}
	if got != want {
		t.Errorf("Kernel() = %#v, want %#v", got, want)
	}
}

// TestOSRelease_HappyPath drives Service.OSRelease through an in-memory
// /etc/os-release matching the format Amazon Linux 2023 emits. Asserts each
// of the four target keys lands on the right struct field; ignored keys
// (HOME_URL etc.) must be dropped without affecting the parse.
func TestOSRelease_HappyPath(t *testing.T) {
	t.Parallel()

	fixture := []byte(`NAME=Amazon Linux
VERSION=2023
ID=amzn
ID_LIKE="rhel fedora"
PLATFORM_ID="platform:al2023"
PRETTY_NAME=Amazon Linux 2023
ANSI_COLOR="0;33"
HOME_URL="https://amazonlinux.com/"
`)
	svc := &Service{
		ReadFile: func(path string) ([]byte, error) {
			if path != osReleasePath {
				t.Errorf("ReadFile path = %q, want %q", path, osReleasePath)
			}
			return fixture, nil
		},
	}

	got, err := svc.OSRelease()
	if err != nil {
		t.Fatalf("OSRelease() returned unexpected error: %v", err)
	}
	want := OSReleaseInfo{
		ID:         "amzn",
		Name:       "Amazon Linux",
		Version:    "2023",
		PrettyName: "Amazon Linux 2023",
	}
	if got != want {
		t.Errorf("OSRelease() = %#v, want %#v", got, want)
	}
}

// TestOSRelease_QuotedValues confirms the parser strips the surrounding
// double quotes that real distros (Ubuntu, RHEL) emit on NAME / PRETTY_NAME.
// Also smoke-tests blank lines and a leading '#' comment.
func TestOSRelease_QuotedValues(t *testing.T) {
	t.Parallel()

	fixture := []byte(`# Ubuntu 24.04 LTS

NAME="Ubuntu"
VERSION="24.04 LTS (Noble Numbat)"
ID=ubuntu
PRETTY_NAME="Ubuntu 24.04 LTS"
`)
	svc := &Service{
		ReadFile: func(_ string) ([]byte, error) { return fixture, nil },
	}

	got, err := svc.OSRelease()
	if err != nil {
		t.Fatalf("OSRelease() returned unexpected error: %v", err)
	}
	if got.Name != "Ubuntu" {
		t.Errorf("Name = %q, want %q (quotes stripped)", got.Name, "Ubuntu")
	}
	if got.Version != "24.04 LTS (Noble Numbat)" {
		t.Errorf("Version = %q, want %q (quotes stripped)", got.Version, "24.04 LTS (Noble Numbat)")
	}
	if got.PrettyName != "Ubuntu 24.04 LTS" {
		t.Errorf("PrettyName = %q, want %q (quotes stripped)", got.PrettyName, "Ubuntu 24.04 LTS")
	}
	if got.ID != "ubuntu" {
		t.Errorf("ID = %q, want %q", got.ID, "ubuntu")
	}
}

// TestOSRelease_MissingFile proves the macOS-local-dev degradation contract:
// when /etc/os-release is absent, OSRelease returns ID="unknown" alongside
// the wrapped read error, rather than dropping a zero-value struct that the
// handler would have to special-case.
func TestOSRelease_MissingFile(t *testing.T) {
	t.Parallel()

	svc := &Service{
		ReadFile: func(_ string) ([]byte, error) { return nil, os.ErrNotExist },
	}

	got, err := svc.OSRelease()
	if err == nil {
		t.Fatal("OSRelease() returned nil error, want wrapped os.ErrNotExist")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %v is not os.ErrNotExist", err)
	}
	if got.ID != "unknown" {
		t.Errorf("ID = %q, want %q", got.ID, "unknown")
	}
}

// TestService_IMDSExtended proves the three new imdsClient methods are
// reachable through the Service.IMDS seam. One method is enough to pin the
// interface extension; the other two share the same call shape so dedicated
// tests would be near-duplicates.
func TestService_IMDSExtended(t *testing.T) {
	t.Parallel()

	svc := &Service{
		IMDS: fakeIMDS{
			instanceType: "t3.micro",
			az:           "us-east-1a",
			amiID:        "ami-0abcdef1234567890",
		},
	}

	gotType, err := svc.IMDS.InstanceType(context.Background())
	if err != nil {
		t.Fatalf("InstanceType returned unexpected error: %v", err)
	}
	if gotType != "t3.micro" {
		t.Errorf("InstanceType = %q, want %q", gotType, "t3.micro")
	}

	gotAZ, err := svc.IMDS.AvailabilityZone(context.Background())
	if err != nil {
		t.Fatalf("AvailabilityZone returned unexpected error: %v", err)
	}
	if gotAZ != "us-east-1a" {
		t.Errorf("AvailabilityZone = %q, want %q", gotAZ, "us-east-1a")
	}

	gotAMI, err := svc.IMDS.AMIID(context.Background())
	if err != nil {
		t.Fatalf("AMIID returned unexpected error: %v", err)
	}
	if gotAMI != "ami-0abcdef1234567890" {
		t.Errorf("AMIID = %q, want %q", gotAMI, "ami-0abcdef1234567890")
	}
}

// TestServiceVPCCIDR proves the VPCCIDR method surfaces the IMDS seam's VPC
// CIDR value (and its error). The config-download handler relies on this to
// derive the client DNS resolver.
func TestServiceVPCCIDR(t *testing.T) {
	t.Parallel()

	svc := &Service{IMDS: fakeIMDS{vpcCIDR: "10.23.0.0/16"}}
	got, err := svc.VPCCIDR(context.Background())
	if err != nil {
		t.Fatalf("VPCCIDR returned unexpected error: %v", err)
	}
	if got != "10.23.0.0/16" {
		t.Errorf("VPCCIDR = %q, want %q", got, "10.23.0.0/16")
	}

	errSvc := &Service{IMDS: fakeIMDS{err: errors.New("imds down")}}
	if _, err := errSvc.VPCCIDR(context.Background()); err == nil {
		t.Fatal("VPCCIDR returned nil error, want non-nil on IMDS failure")
	}
}

// notOnEC2Probe is an EC2Probe that always reports off-AWS, exercising the
// short-circuit path without any network access.
func notOnEC2Probe(_ context.Context) error { return ErrNotOnEC2 }

// spyIMDS fails the test if ANY of its methods is invoked. Used to prove the
// EC2-only Service methods short-circuit BEFORE touching IMDS off-AWS.
type spyIMDS struct{ t *testing.T }

func (s spyIMDS) PublicIP(context.Context) (string, error) {
	s.t.Fatal("IMDS.PublicIP called off-AWS; expected short-circuit")
	return "", nil
}
func (s spyIMDS) InstanceType(context.Context) (string, error) {
	s.t.Fatal("IMDS.InstanceType called off-AWS; expected short-circuit")
	return "", nil
}
func (s spyIMDS) AvailabilityZone(context.Context) (string, error) {
	s.t.Fatal("IMDS.AvailabilityZone called off-AWS; expected short-circuit")
	return "", nil
}
func (s spyIMDS) AMIID(context.Context) (string, error) {
	s.t.Fatal("IMDS.AMIID called off-AWS; expected short-circuit")
	return "", nil
}
func (s spyIMDS) VPCIPv4CIDR(context.Context) (string, error) {
	s.t.Fatal("IMDS.VPCIPv4CIDR called off-AWS; expected short-circuit")
	return "", nil
}

// TestEC2Methods_NotOnEC2ShortCircuit proves the four EC2-only methods (plus
// EC2PublicIP and VPCCIDR) return ErrNotOnEC2 WITHOUT calling IMDS when the
// host is off-AWS — the fix for the off-AWS metadata-timeout hang.
func TestEC2Methods_NotOnEC2ShortCircuit(t *testing.T) {
	t.Parallel()

	svc := &Service{IMDS: spyIMDS{t: t}, EC2Probe: notOnEC2Probe}
	ctx := context.Background()

	calls := map[string]func() (string, error){
		"InstanceType":     func() (string, error) { return svc.InstanceType(ctx) },
		"AvailabilityZone": func() (string, error) { return svc.AvailabilityZone(ctx) },
		"AMIID":            func() (string, error) { return svc.AMIID(ctx) },
		"VPCCIDR":          func() (string, error) { return svc.VPCCIDR(ctx) },
		"EC2PublicIP":      func() (string, error) { return svc.EC2PublicIP(ctx) },
	}
	for name, fn := range calls {
		got, err := fn()
		if !errors.Is(err, ErrNotOnEC2) {
			t.Errorf("%s err = %v, want ErrNotOnEC2", name, err)
		}
		if got != "" {
			t.Errorf("%s = %q, want empty off-AWS", name, got)
		}
	}
}

// TestServiceGet_PublicEndpointWins proves WG_PUBLIC_ENDPOINT is the
// authoritative public IP: it beats both IMDS and the echo client, and a
// "host:port" form is reduced to its host part.
func TestServiceGet_PublicEndpointWins(t *testing.T) {
	t.Parallel()

	svc := &Service{
		PublicEndpoint: "198.51.100.7:51820",
		IMDS:           fakeIMDS{ip: "203.0.113.1"},
		EC2Probe:       notOnEC2Probe,
		Echo:           func(context.Context) (string, error) { return "192.0.2.50", nil },
		Runner:         fakeRunner([]byte(validKey+"\n"), nil, 0),
	}

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() unexpected error: %v", err)
	}
	if got.PublicIP != "198.51.100.7" {
		t.Errorf("PublicIP = %q, want %q (override host wins)", got.PublicIP, "198.51.100.7")
	}
}

// TestServiceGet_EchoFallbackOffAWS proves that off-AWS (no override) Get
// resolves the public IP via the echo client and still succeeds, given a valid
// server key — the dashboard must work identically off-AWS.
func TestServiceGet_EchoFallbackOffAWS(t *testing.T) {
	t.Parallel()

	svc := &Service{
		IMDS:     spyIMDS{t: t}, // must not be touched off-AWS
		EC2Probe: notOnEC2Probe,
		Echo:     func(context.Context) (string, error) { return "192.0.2.50", nil },
		Runner:   fakeRunner([]byte(validKey+"\n"), nil, 0),
	}

	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() unexpected error: %v", err)
	}
	if got.PublicIP != "192.0.2.50" {
		t.Errorf("PublicIP = %q, want echoed %q", got.PublicIP, "192.0.2.50")
	}
	if got.ServerPublicKey != validKey {
		t.Errorf("ServerPublicKey = %q, want %q", got.ServerPublicKey, validKey)
	}
}
