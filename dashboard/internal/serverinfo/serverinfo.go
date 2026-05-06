// Package serverinfo collects the static-ish "where is this VPN reachable"
// data points displayed on the dashboard's server-info card: the EC2 instance's
// public IPv4 (from IMDSv2), the listening UDP port (a hardcoded constant —
// the WireGuard server is provisioned to listen on 51820), and the server's
// WireGuard public key (read out of `wg show wg0 public-key`).
//
// The IMDS HTTP client and the command runner are exposed as injectable
// fields on Service so the unit tests in the sibling sub-task can fake both
// without touching the real metadata service or shelling out to sudo.
package serverinfo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Port is the UDP port WireGuard listens on. Hardcoded to match the value
// baked into the cloud-init user-data and the EC2 security-group rule; if
// either of those ever changes, this constant must move with them.
const Port = 51820

const (
	imdsTokenURL    = "http://169.254.169.254/latest/api/token"
	imdsPublicIPURL = "http://169.254.169.254/latest/meta-data/public-ipv4"
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

// ServerInfo is the public output shape rendered into the server-info card
// (and returned by the GET /api/server JSON endpoint in a sibling sub-task).
type ServerInfo struct {
	PublicIP        string `json:"public_ip"`
	Port            int    `json:"port"`
	ServerPublicKey string `json:"server_public_key"`
}

// imdsClient fetches the EC2 public IPv4 from the Instance Metadata Service.
// Defined as an interface (rather than a concrete type) so tests can swap in
// a stub that returns canned values without spinning up an HTTP server.
type imdsClient interface {
	PublicIP(ctx context.Context) (string, error)
}

// runFunc executes an external command and returns its stdout. Mirrors the
// signature of exec.CommandContext(...).Output() closely enough that the
// production wiring is a one-liner, while leaving tests free to substitute
// a closure that returns canned bytes / errors without invoking sudo.
type runFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Service composes the two side-effecting dependencies the package needs.
// Both fields are exported so tests can construct a Service{} literal with
// fakes; production code should use New() to get the real implementations.
type Service struct {
	IMDS   imdsClient
	Runner runFunc
}

// New returns a Service wired with the production defaults: an httpIMDS
// hitting the real link-local metadata endpoint, and a Runner that shells
// out via os/exec.
func New() *Service {
	return &Service{
		IMDS:   newHTTPIMDS(),
		Runner: defaultRunner,
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
// PUT to obtain a session token, then GET the public-ipv4 path with the
// token in the X-aws-ec2-metadata-token header. IMDSv1 unauthenticated
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
	token, err := h.fetchToken(ctx)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imdsPublicIPURL, nil)
	if err != nil {
		return "", fmt.Errorf("imds %s: %w", imdsPublicIPURL, err)
	}
	req.Header.Set("X-aws-ec2-metadata-token", token)

	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("imds %s: %w", imdsPublicIPURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("imds %s: read body: %w", imdsPublicIPURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("imds %s: status %d: %s", imdsPublicIPURL, resp.StatusCode, bytes.TrimSpace(body))
	}
	ip := strings.TrimSpace(string(body))
	if ip == "" {
		return "", fmt.Errorf("imds %s: empty body", imdsPublicIPURL)
	}
	return ip, nil
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
