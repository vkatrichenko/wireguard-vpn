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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
	"wireguard-dashboard/internal/systemd"
)

const (
	// defaultListenAddr matches the WireGuard tunnel IP that the systemd unit
	// will pin in production. Overridable via LISTEN_ADDR for local dev where
	// the host obviously can't bind 172.16.15.1.
	defaultListenAddr = "172.16.15.1:8080"

	// shutdownTimeout caps how long we wait for in-flight requests to drain
	// after a SIGINT/SIGTERM. 5s is plenty for a dashboard whose handlers all
	// return in milliseconds; tighter than systemd's default 90s TimeoutStopSec
	// so a hung handler doesn't delay node reboots.
	shutdownTimeout = 5 * time.Second
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = defaultListenAddr
	}

	// Production defaults are correct: real IMDSv2 endpoint + os/exec runner.
	// No env-var configuration of the IMDS URL or `wg` path is exposed yet —
	// add it later only if there's a concrete need (test rigs, alternate WG
	// interface name, etc.).
	serverinfoSvc := serverinfo.New()

	// systemd.New() targets `wg-quick@wg0.service` via sudo systemctl. Like
	// serverinfo, the unit name and runner are not env-configurable yet —
	// add knobs only when a concrete need shows up.
	systemdSvc := systemd.New()

	handler, err := server.New(dashboard.WebFS(), serverinfoSvc, systemdSvc)
	if err != nil {
		log.Fatalf("server init failed: %v", err)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Wire signal handling before ListenAndServe so a Ctrl-C during the
	// (admittedly tiny) startup window still triggers graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
