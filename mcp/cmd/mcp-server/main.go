// Command mcp-server is a stdio MCP server that wraps the wireguard-dashboard's
// /api/* endpoints as MCP tools. Phase 2 (mcp-server route,
// project-context/routes/mcp-server/README.md) shipped the read-only tools
// first, with zero mutation risk, to validate the MCP-to-dashboard round
// trip; Phase 3 adds the mutating peer-CRUD tools (see
// internal/tools/mutating.go and docs/confirmation-gates.md for the
// confirm-gate / delete-token design).
//
// It is spawned on-demand by an MCP host (e.g. an `mcpServers` config entry),
// talks to the dashboard over the WireGuard tunnel like any other tunnel
// client, and holds no state of its own beyond the in-process delete-token
// Store constructed below — every tool call is a fresh HTTP request via
// internal/dashboard.Client. It is never deployed to the EC2 instance and
// never embedded in the dashboard's own binary (see the route's Invariants).
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"wireguard-mcp/internal/dashboard"
	"wireguard-mcp/internal/tools"
)

const (
	// defaultDashboardAddr mirrors the dashboard's own production bind
	// address (dashboard/cmd/wireguard-dashboard/main.go's
	// defaultListenAddr) — the WireGuard tunnel IP:port the operator reaches
	// once connected. Per the mcp-server route, one MCP server instance
	// addresses exactly one hardcoded target; MCP_DASHBOARD_ADDR exists only
	// to override for local dev (e.g. pointing at `make run`'s
	// 127.0.0.1:8080), not to support multi-target selection at runtime.
	defaultDashboardAddr = "172.16.15.1:8080"

	serverName    = "wireguard-mcp"
	serverVersion = "0.1.0"
)

func main() {
	// -addr takes precedence over MCP_DASHBOARD_ADDR, which takes precedence
	// over the compiled-in default — same three-tier precedence the
	// dashboard's own getenv helper implements for its two env knobs, just
	// with an extra flag tier since this is a CLI-invoked subprocess rather
	// than a systemd-managed service.
	addrFlag := flag.String("addr", "", "dashboard host:port to wrap (overrides MCP_DASHBOARD_ADDR; defaults to "+defaultDashboardAddr+")")
	flag.Parse()

	addr := *addrFlag
	if addr == "" {
		addr = getenv("MCP_DASHBOARD_ADDR", defaultDashboardAddr)
	}

	client := dashboard.New(addr)

	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	tools.Register(server, client)

	// One Store per process, shared by every delete_client/preview_delete_client
	// call — a per-call Store would mean a preview's token is never visible to
	// the delete call that's supposed to redeem it. See tokens.go's Store doc
	// comment for why per-process (not persisted) is correct here.
	store := tools.NewStore()
	tools.RegisterMutating(server, client, store)

	// Mirrors the dashboard's own signal.NotifyContext idiom (main.go) so a
	// Ctrl-C or host-initiated SIGTERM tears the stdio session down cleanly
	// instead of leaving a zombie subprocess behind the MCP host.
	//
	// slog's default handler writes to os.Stderr (matching the dashboard's
	// own unconfigured slog usage) — this matters more here than in the
	// dashboard, because stdout is the MCP JSON-RPC wire in this process;
	// anything accidentally written there would corrupt the protocol
	// framing. No logger is explicitly configured, so verify this holds
	// before adding any new log call in this package.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("wireguard-mcp starting", "dashboard_addr", addr, "transport", "stdio")
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		slog.Error("mcp server exited with error", "err", err)
		os.Exit(1)
	}
}

// getenv returns os.Getenv(key) when set to a non-empty value, otherwise def.
// Copied rather than shared: this is a two-module repo (mcp/ and dashboard/
// are separate Go modules with no dependency between them by design), so
// there is no package to import this tiny helper from without adding a
// cross-module dependency for one function.
func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
