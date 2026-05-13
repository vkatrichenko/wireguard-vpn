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
	"syscall"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/disk"
	"wireguard-dashboard/internal/geoip"
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

func main() {
	addr := getenv("LISTEN_ADDR", defaultListenAddr)

	// Production defaults are correct: real IMDSv2 endpoint + os/exec runner.
	// No env-var configuration of the IMDS URL or `wg` path is exposed yet —
	// add it later only if there's a concrete need (test rigs, alternate WG
	// interface name, etc.).
	serverinfoSvc := serverinfo.New()

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

	// geoip.New parses the embedded GeoLite2-City.mmdb once at startup. A
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
	pollerSvc := poller.New(metricsDB, procSvc, wgSvc, clientsfileSvc)
	go pollerSvc.Run(ctx)

	handler, err := server.New(dashboard.WebFS(), serverinfoSvc, systemdSvc, clientsfileSvc, wgSvc, procSvc, metricsDB, geoipSvc, diskSvc, processesSvc)
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
