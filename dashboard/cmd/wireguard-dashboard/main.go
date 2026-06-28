// Command wireguard-dashboard is the single static binary that serves the
// read-only WireGuard ops dashboard.
//
// Slice 1 scope: bind, serve two handlers, exit cleanly on SIGINT/SIGTERM.
// No config files, no flags — only the LISTEN_ADDR env var. Production bind
// is the WG tunnel IP (172.16.15.1:8080); local dev typically uses
// 127.0.0.1:8080 via `make run`.
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/alerts"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/disk"
	"wireguard-dashboard/internal/geoip"
	"wireguard-dashboard/internal/netdev"
	"wireguard-dashboard/internal/notify"
	"wireguard-dashboard/internal/poller"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/processes"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
	"wireguard-dashboard/internal/systemd"
	"wireguard-dashboard/internal/wg"
)

const (
	// defaultListenAddr matches the WireGuard tunnel IP that the systemd unit
	// will pin in production. Overridable via LISTEN_ADDR for local dev where
	// the host obviously can't bind 172.16.15.1.
	defaultListenAddr = "172.16.15.1:8080"

	// defaultDBPath is the on-disk location of the dashboard's SQLite metrics
	// store, matching the path the systemd unit's StateDirectory provisions
	// (/var/lib/wireguard-dashboard, mode 0750, owned by the dashboard user).
	// Overridable via DB_PATH for local dev where /var/lib isn't writable.
	defaultDBPath = "/var/lib/wireguard-dashboard/metrics.db"

	// shutdownTimeout caps how long we wait for in-flight requests to drain
	// after a SIGINT/SIGTERM. 5s is plenty for a dashboard whose handlers all
	// return in milliseconds; tighter than systemd's default 90s TimeoutStopSec
	// so a hung handler doesn't delay node reboots.
	shutdownTimeout = 5 * time.Second
)

// Build-time metadata. Populated via -ldflags "-X main.ReleaseTag=…
// -X main.BuildSHA=… -X main.BuildTime=… -X main.GoVersion=…" from the
// Makefile and the release CI workflow. They carry sentinel defaults so a
// `go run`/`go test` invocation (which doesn't pass ldflags) still produces
// a renderable About card rather than empty strings that would surprise a
// future template branch. ReleaseTag defaults to "dev" rather than "unknown":
// an untagged local build genuinely is not a release, so "dev" is the honest
// label (the CI release workflow injects the real vX.Y.Z from GITHUB_REF_NAME).
// They MUST be `var` not `const`: the `-X` linker flag overwrites variable
// initializers and silently no-ops on constants.
var (
	ReleaseTag = "dev"
	BuildSHA   = "unknown"
	BuildTime  = "unknown"
	GoVersion  = "unknown"
)

func main() {
	addr := getenv("LISTEN_ADDR", defaultListenAddr)

	// Production defaults are correct: real IMDSv2 endpoint + os/exec runner.
	// No env-var configuration of the IMDS URL or `wg` path is exposed yet —
	// add it later only if there's a concrete need (test rigs, alternate WG
	// interface name, etc.).
	serverinfoSvc := serverinfo.New()
	// Wire the build-time metadata (populated via -ldflags -X) onto the
	// Service so the About-tab handler can render the Binary card. Defaults
	// are the sentinel "unknown" values declared above, so a `go run` (no
	// ldflags) still produces a stable card shape.
	serverinfoSvc.Build = serverinfo.BuildInfo{
		ReleaseTag: ReleaseTag,
		SHA:        BuildSHA,
		Time:       BuildTime,
		GoVersion:  GoVersion,
	}

	// systemd.New() targets `wg-quick@wg0.service` via sudo systemctl. Like
	// serverinfo, the unit name and runner are not env-configurable yet —
	// add knobs only when a concrete need shows up.
	systemdSvc := systemd.New()

	// clientsfile.New() reads /etc/wireguard-dashboard/clients.json — the
	// manifest cloud-init renders from var.clients_config. wg.New() shells
	// out via `sudo wg show wg0 dump`. Both seams are env-non-configurable
	// for the same reason as the services above; the GET /api/clients
	// handler joins their outputs.
	clientsfileSvc := clientsfile.New()
	wgSvc := wg.New()

	// proc.New() must be a singleton — it holds the prior /proc/stat and
	// rx/tx byte counters under a mutex so each Sample call can compute
	// CPU% and byte-rate deltas. Constructing a fresh Service per request
	// would reset those priors and the dashboard would render zero rates
	// forever.
	procSvc := proc.New()

	// processes.New() must be a singleton for the same reason as procSvc —
	// it holds the prior per-PID jiffies map under a mutex so each Sample
	// call can compute per-process CPU% as a delta against the previous
	// reading. A fresh Service per request would always be a "first sample"
	// and render zero CPU% for every row.
	processesSvc := processes.New()

	// disk.New() wires os.ReadFile + unix.Statfs against /proc/mounts. Unlike
	// procSvc the service holds no prior-sample state — Sample is a fresh read
	// each call — but it is still constructed once and shared by the poller
	// (Slice 6 sub-task 5) and the system-tab handler so the seam stays
	// uniform across packages.
	diskSvc := disk.New()

	// netdev.New() reads /proc/net/dev for the wg0 interface row. PeerCount is
	// the seam designed so this package never imports internal/wg directly —
	// we wire a closure over wgSvc here so Sample returns Stats.Peers populated
	// without dragging the systemd/exec graph into the netdev package's tests.
	// Same singleton rationale as diskSvc: stateless today, but constructed
	// once so adding a poller hook later is a one-line wire change.
	netdevSvc := netdev.New()
	netdevSvc.PeerCount = func(ctx context.Context) (int, error) {
		peers, err := wgSvc.Show(ctx)
		if err != nil {
			return 0, err
		}
		return len(peers), nil
	}

	// geoip.New parses the embedded dbip-city-lite.mmdb once at startup. A
	// failure here means the committed mmdb is corrupt — the binary cannot
	// recover at runtime, so fail fast before any handlers are wired. The
	// reader is held for the lifetime of the process and passed into
	// server.New so buildClientRows can resolve each peer endpoint's country
	// and city for the clients card.
	geoipSvc, err := geoip.New()
	if err != nil {
		slog.Error("geoip: init failed", "err", err)
		os.Exit(1)
	}
	defer func() { _ = geoipSvc.Close() }()

	// Best-effort warm sample so the first /api/snapshot or page render
	// has non-zero CPU% / rates. Errors here are non-fatal — failure on
	// the Mac (no /proc) shouldn't block local dev, and on the EC2 a real
	// failure surfaces on the next real call anyway.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := procSvc.Sample(ctx); err != nil {
			slog.Warn("proc warm-sample failed", "err", err)
		}
	}()

	// Same rationale as the procSvc warm-sample above: prime the prior
	// per-PID jiffies map so the first System-tab render has non-zero
	// per-process CPU% deltas. Non-fatal for the same reasons — no /proc
	// on the Mac dev box, real EC2 failures resurface on the first request.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := processesSvc.Sample(ctx); err != nil {
			slog.Warn("processes warm-sample failed", "err", err)
		}
	}()

	// Alert delivery (spec 007 transport; spec 008 runtime config). The webhook
	// URL now lives in a runtime-mutable holder seeded from DASHBOARD_WEBHOOK_URL
	// at boot. An operator can re-point or disable delivery at runtime (Slice 2's
	// HTTP endpoint) without a restart; the notifier resolves the holder's
	// current URL on every send and no-ops (no outbound HTTP) when it is empty,
	// so an unset seed runs exactly as today. The override is in-memory only — a
	// restart returns to the seed. Config is env-var-seeded by owner decision
	// (multi-cloud portability) — no SSM/Terraform. The URL is a secret: it is
	// never logged in full (notify redacts to scheme+host), so we only note here
	// whether alerting is enabled at boot. The holder is threaded into server.New
	// (Slice 2) so the /api/webhook status/set/revert endpoints can re-point or
	// disable delivery at runtime.
	webhookCfg := notify.NewWebhookConfig(os.Getenv("DASHBOARD_WEBHOOK_URL"))
	webhook := notify.NewNotifier(webhookCfg)
	// Boot-config transports (spec 012, Slice 3). Each constructor returns a nil
	// Notifier when its env is unset; NewMultiNotifier filters those out, so an
	// unconfigured transport contributes nothing (no goroutine, no HTTP). Unlike
	// the runtime-managed webhook, these are immutable for the process lifetime.
	// Their tokens/URLs are secrets — the transports redact them, so we only log
	// transport NAMES here, never config values.
	slackBot := notify.NewSlackBotFromEnv()
	telegram := notify.NewTelegramFromEnv()
	discord := notify.NewDiscordFromEnv()
	// Fan-out composite (spec 012): the evaluator/poller keep depending on a
	// single Notifier while delivery targets grow. The runtime-managed Slack
	// incoming webhook plus the three boot-config transports all fan out here.
	var notifier notify.Notifier = notify.NewMultiNotifier(webhook, slackBot, telegram, discord)
	if webhookCfg.Enabled() {
		slog.Info("notify: webhook alerting enabled")
	} else {
		slog.Info("notify: DASHBOARD_WEBHOOK_URL unset; alerting disabled (no-op)")
	}
	bootTransports := make([]string, 0, 3)
	if slackBot != nil {
		bootTransports = append(bootTransports, "slack-bot")
	}
	if telegram != nil {
		bootTransports = append(bootTransports, "telegram")
	}
	if discord != nil {
		bootTransports = append(bootTransports, "discord")
	}
	if len(bootTransports) > 0 {
		slog.Info("notify: boot-config transports enabled", "transports", strings.Join(bootTransports, ","))
	} else {
		slog.Info("notify: no boot-config transports configured (slack-bot/telegram/discord)")
	}

	// Alert evaluator (spec 007, Slices 2-3). It runs UNCONDITIONALLY — even
	// with a NoOp notifier — so the in-UI active-alerts view (Slice 5) always has
	// live state; only delivery is gated on the webhook URL. The host label
	// stamped into messages is a PORTABLE identifier (os.Hostname, overridable
	// via DASHBOARD_HOST_LABEL) — deliberately NOT the AWS IMDSv2 instance id,
	// so the dashboard stays cloud-agnostic. The evaluator + systemd reader +
	// disk reader + notifier are threaded into the poller, which evaluates
	// conditions each tick and dispatches fire/recovery events OFF the poll
	// critical path.
	//
	// Slice 3 thresholds are plain (non-secret) env knobs with documented
	// defaults; an unparseable or out-of-range value is logged and ignored,
	// falling back to the alerts package default so a typo never disables a
	// condition silently.
	alertEvaluator := alerts.New(alerts.Config{
		Host:             hostLabel(),
		DiskThresholdPct: envPct("DASHBOARD_ALERT_DISK_PCT", alerts.DefaultDiskThresholdPct),
		CPUThresholdPct:  envPct("DASHBOARD_ALERT_CPU_PCT", alerts.DefaultCPUThresholdPct),
		CPUSustain:       envDuration("DASHBOARD_ALERT_CPU_SUSTAIN", alerts.DefaultCPUSustain),
		TransferCapBytes: envBytes("DASHBOARD_ALERT_TRANSFER_BYTES", alerts.DefaultTransferCapBytes),
	})

	// Shared status holder for the in-UI active-alerts view (spec 007, Slice 5).
	// It is the ONLY safe channel between the poller goroutine (which drives the
	// non-concurrency-safe evaluator and writes the holder each tick) and the HTTP
	// goroutines (which read a deep copy via Snapshot for the Overview strip,
	// Events entries, and GET /api/alerts). The "enabled" flag (whether outbound
	// webhook delivery is configured) is NO LONGER snapshotted here: as of spec
	// 008 Slice 2 it is derived LIVE from webhookCfg by the server's alertSnapshot,
	// so the view tracks runtime enable/disable via /api/webhook without a restart.
	alertStatus := alerts.NewStatusHolder()

	// Wire signal handling early so the poller below can use the same ctx
	// for cancellation, and a Ctrl-C during the (admittedly tiny) startup
	// window still triggers graceful shutdown of every long-lived goroutine.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Open the metrics DB. Path is configurable via DB_PATH env (default
	// /var/lib/wireguard-dashboard/metrics.db, matching the systemd unit's
	// StateDirectory provisioning). Failure to open is fatal — the
	// dashboard would still serve live data without history, but losing
	// the trend feature without a loud signal is worse than crashing fast,
	// so the operator sees a clean systemd "service failed to start" rather
	// than a silently empty chart.
	dbPath := getenv("DB_PATH", defaultDBPath)
	metricsDB, err := db.Open(ctx, dbPath)
	if err != nil {
		slog.Error("open metrics db", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := metricsDB.Close(); err != nil {
			slog.Warn("close metrics db", "err", err)
		}
	}()

	// Start the poller in the background. Run blocks until ctx is
	// cancelled, then waits for both internal loops to drain — so by the
	// time `<-ctx.Done()` fires below and we head into Shutdown, the
	// poller is also winding down in parallel. The deferred metricsDB.Close
	// runs after both the HTTP server and the poller have stopped writing.
	pollerSvc := poller.New(metricsDB, procSvc, wgSvc, clientsfileSvc, systemdSvc, diskSvc, alertEvaluator, notifier, alertStatus)
	go pollerSvc.Run(ctx)

	handler, err := server.New(dashboard.WebFS(), serverinfoSvc, systemdSvc, clientsfileSvc, wgSvc, procSvc, metricsDB, geoipSvc, diskSvc, processesSvc, netdevSvc, alertStatus, webhookCfg, pollerSvc)
	if err != nil {
		log.Fatalf("server init failed: %v", err)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("wireguard-dashboard listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutdown signal received, draining for %s", shutdownTimeout)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("graceful shutdown failed: %v", err)
	}
	log.Printf("wireguard-dashboard stopped cleanly")
}

// getenv returns os.Getenv(key) when set to a non-empty value, otherwise def.
// Local helper rather than a config package because main.go has exactly two
// env knobs (LISTEN_ADDR, DB_PATH) and the rest of the wiring is positional.
func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envPct parses a percentage threshold from the named env var, falling back to
// def. An unset var, an unparseable value, or one outside (0,100] is logged and
// def is returned — a misconfigured knob must never silently disable a
// condition. The lower bound is exclusive of 0 because <= 0 would make the
// alerts package fall back to its own default anyway, and a 0% threshold would
// fire constantly.
func envPct(key string, def float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || v > 100 {
		slog.Warn("alerts: ignoring invalid threshold env; using default", "key", key, "value", raw, "default", def)
		return def
	}
	return v
}

// envDuration parses a Go duration string (e.g. "5m") from the named env var,
// falling back to def. An unset var, an unparseable value, or a non-positive
// duration is logged and def is returned.
func envDuration(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("alerts: ignoring invalid duration env; using default", "key", key, "value", raw, "default", def.String())
		return def
	}
	return d
}

// envBytes parses an operator-friendly byte-size from the named env var, falling
// back to def. An unset, unparseable, or non-positive value is logged and def is
// returned — a typo must never silently disable the transfer-cap condition.
//
// Grammar: a number (integer or decimal) optionally followed by a unit suffix,
// case-insensitive, with optional surrounding whitespace. A bare number is
// plain bytes. Decimal suffixes (KB/MB/GB/TB) are powers of 1000; binary
// suffixes (KiB/MiB/GiB/TiB) are powers of 1024; a single-letter K/M/G/T is
// treated as binary (the conventional VPN-operator reading of "50G"). Examples:
// "53687091200", "50GiB", "50G", "53.5 GB".
func envBytes(key string, def int64) int64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := parseByteSize(raw)
	if err != nil || v <= 0 {
		slog.Warn("alerts: ignoring invalid byte-size env; using default", "key", key, "value", raw, "default", def)
		return def
	}
	return v
}

// parseByteSize parses the envBytes grammar. Kept std-lib only (no go-humanize)
// per the no-new-deps constraint.
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, strconv.ErrSyntax
	}
	// Split the trailing unit (letters) from the leading number.
	i := len(s)
	for i > 0 {
		c := s[i-1]
		if (c >= '0' && c <= '9') || c == '.' {
			break
		}
		i--
	}
	numPart := strings.TrimSpace(s[:i])
	unit := strings.ToLower(strings.TrimSpace(s[i:]))

	num, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, err
	}

	var mult float64
	switch unit {
	case "", "b":
		mult = 1
	case "kb":
		mult = 1e3
	case "mb":
		mult = 1e6
	case "gb":
		mult = 1e9
	case "tb":
		mult = 1e12
	case "k", "kib":
		mult = 1 << 10
	case "m", "mib":
		mult = 1 << 20
	case "g", "gib":
		mult = 1 << 30
	case "t", "tib":
		mult = 1 << 40
	default:
		return 0, strconv.ErrSyntax
	}
	return int64(num * mult), nil
}

// hostLabel returns the identifier stamped into alert messages. It prefers an
// explicit DASHBOARD_HOST_LABEL override, then os.Hostname(), then a fixed
// fallback if the hostname lookup fails. This is a PORTABLE choice (works on any
// cloud/VPS), deliberately NOT the AWS IMDSv2 instance id, per the owner's
// multi-cloud-portability decision for spec 007.
func hostLabel() string {
	if v := os.Getenv("DASHBOARD_HOST_LABEL"); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "wireguard-dashboard"
}
