package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/wgconfig"
)

// handleGetClientConfig serves a downloadable wg-quick config for one client,
// identified by its manifest name. Every server-derived field is filled in;
// only the Interface PrivateKey is a placeholder the operator replaces locally
// (the server never holds client private keys).
//
// The `?mode=` query selects the routing profile: `split` for VPC-only
// routing, anything else (including absent) for full-tunnel.
//
// Status model:
//   - 404 — the name isn't in the manifest (or is empty). A typo / stale link
//     must not render a config against nothing.
//   - 500 — the manifest read failed (we can't confirm or deny the name) or
//     config assembly failed unexpectedly.
//   - 503 — a server-derived input is unavailable (public IP, server public
//     key, or VPC CIDR). We refuse to emit a config with a blank/wrong field
//     rather than hand the operator a silently-broken file.
func (s *server) handleGetClientConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.NotFound(w, r)
		return
	}

	clients, err := s.clientsfileSvc.Load(r.Context())
	if err != nil {
		slog.Error("GET /api/clients/{name}/config: clientsfile load failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	client, ok := clientsfile.ByName(clients)[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// serverinfo.Get yields the public IP, port, and server public key in one
	// call; any fetch failure (IMDS down, sudo/wg failure) means we can't build
	// a correct config, so it's a 503 rather than a misleading partial file.
	info, err := s.serverinfoSvc.Get(r.Context())
	if err != nil {
		slog.Error("GET /api/clients/{name}/config: serverinfo get failed", "err", err)
		http.Error(w, "server endpoint/key unavailable", http.StatusServiceUnavailable)
		return
	}
	if info.PublicIP == "" || info.ServerPublicKey == "" {
		slog.Error("GET /api/clients/{name}/config: serverinfo incomplete",
			"have_public_ip", info.PublicIP != "", "have_server_key", info.ServerPublicKey != "")
		http.Error(w, "server endpoint/key unavailable", http.StatusServiceUnavailable)
		return
	}

	// The VPC CIDR drives the derived DNS resolver. A read failure or empty
	// value is a 503 for the same reason — a wrong/blank DNS line is worse than
	// no file.
	vpcCIDR, err := s.serverinfoSvc.VPCCIDR(r.Context())
	if err != nil || vpcCIDR == "" {
		slog.Error("GET /api/clients/{name}/config: vpc cidr unavailable", "err", err)
		http.Error(w, "vpc cidr unavailable", http.StatusServiceUnavailable)
		return
	}

	endpoint := net.JoinHostPort(info.PublicIP, strconv.Itoa(info.Port))
	mode := wgconfig.ParseMode(r.URL.Query().Get("mode"))
	conf, err := wgconfig.Build(
		wgconfig.Client{Name: client.Name, Address: client.Address},
		mode,
		info.ServerPublicKey,
		endpoint,
		vpcCIDR,
	)
	if err != nil {
		slog.Error("GET /api/clients/{name}/config: build failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	filename := "wg-" + sanitizeFilename(client.Name) + ".conf"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = w.Write([]byte(conf))
}

// sanitizeFilename reduces a client name to a safe filename token: ASCII
// letters, digits, dot, underscore and hyphen pass through; everything else
// (spaces, slashes, control chars, quotes) becomes a hyphen. Defensive against
// header injection even though names originate from the operator-controlled
// manifest, and keeps the downloaded filename portable across OSes.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "client"
	}
	return out
}
