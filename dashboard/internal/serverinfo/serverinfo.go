// Package serverinfo collects the static-ish "where is this VPN reachable"
// data points displayed on the dashboard's server-info card: the EC2 instance's
// public IPv4 (from IMDSv2), the listening UDP port (a hardcoded constant —
// the WireGuard server is provisioned to listen on 51820), and the server's
// WireGuard public key (read out of `wg show wg0 public-key`).
//
// It also exposes the slower-moving "what is this host" data the About tab
// renders: the EC2 instance-type / availability-zone / AMI id (also via
// IMDSv2), the running kernel triple (from unix.Uname), and the OS release
// identifiers parsed from /etc/os-release.
//
// Every side-effecting dependency — the IMDS HTTP client, the command runner,
// the uname syscall, and the file reader — is exposed as an injectable field
// on Service so unit tests can fake the lot without touching the real
// metadata service, shelling out to sudo, or expecting /etc/os-release to
// exist (it doesn't on macOS, where most local development happens).
package serverinfo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Port is the UDP port WireGuard listens on. Hardcoded to match the value
// baked into the cloud-init user-data and the EC2 security-group rule; if
// either of those ever changes, this constant must move with them.
const Port = 51820

const (
	imdsBaseURL  = "http://169.254.169.254/latest"
	imdsTokenURL = imdsBaseURL + "/api/token"
	// IMDSv2 metadata paths. Each method on httpIMDS targets exactly one of
	// these and shares the token-fetch + GET helper below; keeping the URLs
	// alongside the helper avoids drift between the call-sites and the
	// error-wrapping path.
	imdsPublicIPURL        = imdsBaseURL + "/meta-data/public-ipv4"
	imdsInstanceTypeURL    = imdsBaseURL + "/meta-data/instance-type"
	imdsAvailabilityZone   = imdsBaseURL + "/meta-data/placement/availability-zone"
	imdsAMIIDURL           = imdsBaseURL + "/meta-data/ami-id"
	// imdsTokenTTL is the IMDSv2 token lifetime requested via the
	// X-aws-ec2-metadata-token-ttl-seconds header. Spec allows 1–21600;
	// we use the lower-end "fresh per request" 60 seconds since each Get()
	// call mints a new token rather than caching one.
	imdsTokenTTL = "60"
	// httpTimeout caps each individual IMDS HTTP request. The link-local
	// 169.254.169.254 endpoint should answer in single-digit milliseconds
	// on EC2; 2s is a generous failure cutoff that still keeps a stuck
	// metadata service from stalling the dashboard's snapshot endpoint.
	httpTimeout = 2 * time.Second
)

// osReleasePath is the canonical Linux location of the OS release file.
// Exposed as a const (rather than inline) so the test can reference the same
// path the production code reads.
const osReleasePath = "/etc/os-release"

// ServerInfo is the public output shape rendered into the server-info card
// (and returned by the GET /api/server JSON endpoint in a sibling sub-task).
type ServerInfo struct {
	PublicIP        string `json:"public_ip"`
	Port            int    `json:"port"`
	ServerPublicKey string `json:"server_public_key"`
}

// KernelInfo mirrors the five most useful fields of `uname -a`: the OS
// kernel name, the host's nodename, the kernel release ("6.8.0-1015-aws"),
// the build/version string, and the machine architecture. Domainname is
// intentionally omitted — it's blank on EC2 and not Darwin-portable.
type KernelInfo struct {
	Sysname  string `json:"sysname"`
	Nodename string `json:"nodename"`
	Release  string `json:"release"`
	Version  string `json:"version"`
	Machine  string `json:"machine"`
}

// OSReleaseInfo carries the four /etc/os-release keys the About tab renders.
// The file ships many more (HOME_URL, BUG_REPORT_URL, CPE_NAME, …) but those
// add visual noise without operational value. If the file is missing or
// unreadable (macOS local dev), ID is set to "unknown" so the handler can
// still render the card without special-casing nil.
type OSReleaseInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	PrettyName string `json:"pretty_name"`
}

// imdsClient fetches per-path strings from the EC2 Instance Metadata Service.
// Defined as an interface (rather than a concrete type) so tests can swap in
// a stub that returns canned values without spinning up an HTTP server. All
// four methods share the same shape: one IMDSv2 GET, trim the body, return
// it. Errors are wrapped with the metadata path so a failure points straight
// at the misbehaving endpoint.
type imdsClient interface {
	PublicIP(ctx context.Context) (string, error)
	InstanceType(ctx context.Context) (string, error)
	AvailabilityZone(ctx context.Context) (string, error)
	AMIID(ctx context.Context) (string, error)
}

// runFunc executes an external command and returns its stdout. Mirrors the
// signature of exec.CommandContext(...).Output() closely enough that the
// production wiring is a one-liner, while leaving tests free to substitute
// a closure that returns canned bytes / errors without invoking sudo.
type runFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// unameFunc fills a *unix.Utsname via the SYS_UNAME syscall. Injectable so
// tests can deposit canned bytes into the struct without invoking the real
// syscall (which produces machine-dependent output and would tie test
// assertions to the host running `go test`).
type unameFunc func(*unix.Utsname) error

// readFileFunc reads a whole file into memory. Injectable so OSRelease tests
// can drive the parser from in-memory bytes — touching /etc/os-release on a
// developer's macOS box is both racy and platform-specific.
type readFileFunc func(path string) ([]byte, error)

// Service composes every side-effecting dependency the package needs. Fields
// are grouped by concern (IMDS / command runner / kernel & os-release
// readers) and exported so tests can construct a Service{} literal with
// fakes; production code should use New() to get the real implementations.
type Service struct {
	// IMDS-backed readers (public IP, instance type, AZ, AMI id).
	IMDS imdsClient

	// Sudo-gated command runner used to fetch the WireGuard public key.
	Runner runFunc

	// Local system readers used by the About tab.
	Uname    unameFunc
	ReadFile readFileFunc
}

// New returns a Service wired with the production defaults: an httpIMDS
// hitting the real link-local metadata endpoint, an exec.CommandContext-based
// Runner, the real unix.Uname syscall, and os.ReadFile for /etc/os-release.
func New() *Service {
	return &Service{
		IMDS:     newHTTPIMDS(),
		Runner:   defaultRunner,
		Uname:    unix.Uname,
		ReadFile: os.ReadFile,
	}
}

// Get fetches the public IP and the server's WireGuard public key in
// parallel, then assembles them into a ServerInfo. Either failure aborts
// the call — partial results would silently mislead the operator (e.g. an
// empty public IP looks like a bug rather than a fetch failure), so we
// surface the first error we see.
func (s *Service) Get(ctx context.Context) (ServerInfo, error) {
	var (
		wg        sync.WaitGroup
		publicIP  string
		publicKey string
		ipErr     error
		keyErr    error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		publicIP, ipErr = s.IMDS.PublicIP(ctx)
	}()
	go func() {
		defer wg.Done()
		publicKey, keyErr = s.fetchServerPublicKey(ctx)
	}()
	wg.Wait()

	if err := errors.Join(ipErr, keyErr); err != nil {
		return ServerInfo{}, err
	}
	return ServerInfo{
		PublicIP:        publicIP,
		Port:            Port,
		ServerPublicKey: publicKey,
	}, nil
}

// Kernel reports the running kernel triple via unix.Uname. The C strings
// returned by the syscall are NUL-terminated within fixed-size byte arrays;
// unix.ByteSliceToString strips the terminator and any trailing garbage so
// the resulting Go strings are safe to render directly.
func (s *Service) Kernel() (KernelInfo, error) {
	var uts unix.Utsname
	if err := s.Uname(&uts); err != nil {
		return KernelInfo{}, fmt.Errorf("uname: %w", err)
	}
	return KernelInfo{
		Sysname:  unix.ByteSliceToString(uts.Sysname[:]),
		Nodename: unix.ByteSliceToString(uts.Nodename[:]),
		Release:  unix.ByteSliceToString(uts.Release[:]),
		Version:  unix.ByteSliceToString(uts.Version[:]),
		Machine:  unix.ByteSliceToString(uts.Machine[:]),
	}, nil
}

// OSRelease parses /etc/os-release into the four keys the About tab cares
// about. Missing files (macOS local dev) degrade to OSReleaseInfo{ID:
// "unknown"} with the original read error returned alongside, so the
// handler can render a stable "unknown" row rather than 500ing the whole
// page when development happens off-Linux.
func (s *Service) OSRelease() (OSReleaseInfo, error) {
	body, err := s.ReadFile(osReleasePath)
	if err != nil {
		return OSReleaseInfo{ID: "unknown"}, fmt.Errorf("read %s: %w", osReleasePath, err)
	}
	return parseOSRelease(body), nil
}

// parseOSRelease walks the file line-by-line. Lines that are blank or start
// with '#' are skipped; KEY=VALUE lines have their VALUE stripped of any
// surrounding double quotes before being assigned. Unknown keys are
// dropped silently — os-release files routinely carry distro-specific
// extensions the dashboard has no use for.
func parseOSRelease(body []byte) OSReleaseInfo {
	out := OSReleaseInfo{}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"`)
		switch key {
		case "ID":
			out.ID = value
		case "NAME":
			out.Name = value
		case "VERSION":
			out.Version = value
		case "PRETTY_NAME":
			out.PrettyName = value
		}
	}
	return out
}

// fetchServerPublicKey runs `sudo wg show wg0 public-key` and trims the
// trailing newline that wg always emits. The full path /usr/bin/wg matches
// the sudoers NOPASSWD entry that the cloud-init step provisions — using a
// bare `wg` here would make sudo prompt for a password and the dashboard
// user has none.
func (s *Service) fetchServerPublicKey(ctx context.Context) (string, error) {
	out, err := s.Runner(ctx, "sudo", "/usr/bin/wg", "show", "wg0", "public-key")
	if err != nil {
		// exec.ExitError carries stderr separately; surface it so a missing
		// sudoers entry or a downed wg interface produces an actionable
		// message rather than the bare "exit status 1".
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("wg show wg0 public-key: %w: %s", err, bytes.TrimSpace(exitErr.Stderr))
		}
		return "", fmt.Errorf("wg show wg0 public-key: %w", err)
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", errors.New("wg show wg0 public-key: empty output")
	}
	return key, nil
}

// defaultRunner is the production implementation of runFunc. It mirrors
// exec.CommandContext + .Output(), which captures stdout and surfaces
// stderr via *exec.ExitError on a non-zero exit.
func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// httpIMDS is the production imdsClient. It performs the IMDSv2 two-step:
// PUT to obtain a session token, then GET the requested metadata path with
// the token in the X-aws-ec2-metadata-token header. IMDSv1 unauthenticated
// fallback is intentionally not attempted — the EC2 instance is configured
// to require IMDSv2 (`http_tokens = "required"` in Terraform).
type httpIMDS struct {
	client *http.Client
}

func newHTTPIMDS() *httpIMDS {
	return &httpIMDS{
		client: &http.Client{Timeout: httpTimeout},
	}
}

func (h *httpIMDS) PublicIP(ctx context.Context) (string, error) {
	return h.metadata(ctx, imdsPublicIPURL)
}

func (h *httpIMDS) InstanceType(ctx context.Context) (string, error) {
	return h.metadata(ctx, imdsInstanceTypeURL)
}

func (h *httpIMDS) AvailabilityZone(ctx context.Context) (string, error) {
	return h.metadata(ctx, imdsAvailabilityZone)
}

func (h *httpIMDS) AMIID(ctx context.Context) (string, error) {
	return h.metadata(ctx, imdsAMIIDURL)
}

// metadata performs one IMDSv2 token-fetch + GET against the given absolute
// URL and returns the trimmed body. Factored out so the four exported
// methods aren't four near-identical copies of the same dance — any change
// to error wrapping, timeouts, or header handling lands here once.
func (h *httpIMDS) metadata(ctx context.Context, url string) (string, error) {
	token, err := h.fetchToken(ctx)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("imds %s: %w", url, err)
	}
	req.Header.Set("X-aws-ec2-metadata-token", token)

	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("imds %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("imds %s: read body: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("imds %s: status %d: %s", url, resp.StatusCode, bytes.TrimSpace(body))
	}
	value := strings.TrimSpace(string(body))
	if value == "" {
		return "", fmt.Errorf("imds %s: empty body", url)
	}
	return value, nil
}

func (h *httpIMDS) fetchToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, imdsTokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("imds %s: %w", imdsTokenURL, err)
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", imdsTokenTTL)

	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("imds %s: %w", imdsTokenURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("imds %s: read body: %w", imdsTokenURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("imds %s: status %d: %s", imdsTokenURL, resp.StatusCode, bytes.TrimSpace(body))
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("imds %s: empty token", imdsTokenURL)
	}
	return token, nil
}
