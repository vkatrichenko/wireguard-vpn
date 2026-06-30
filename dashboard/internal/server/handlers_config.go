package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/serverinfo"
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
//   - 503 — a server-derived input is unavailable (public IP or server public
//     key). We refuse to emit a config with a blank/wrong field rather than
//     hand the operator a silently-broken file. The DNS line is NOT a 503
//     gate: on AWS it is the VPC-derived resolver, off-AWS it falls back to
//     WG_CLIENT_DNS, so a missing VPC CIDR degrades rather than fails.
func (s *server) handleGetClientConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		http.NotFound(w, r)
		return
	}

	dbClients, err := s.clientsSvc.List(r.Context())
	if err != nil {
		slog.Error("GET /api/clients/{name}/config: clients list failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var client db.Client
	found := false
	for _, c := range dbClients {
		if c.Name == name {
			client = c
			found = true
			break
		}
	}
	if !found {
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

	// Compute the DNS line and split-tunnel routes per environment. The overlay
	// /24 is always derived from WG_SERVER_NET (falling back to the package
	// default if the Service carries none or an unparseable value).
	serverNet := s.serverinfoSvc.ServerNet
	if serverNet == "" {
		serverNet = wgconfig.DefaultServerNet
	}
	overlay, err := wgconfig.OverlaySubnet(serverNet)
	if err != nil {
		// Only reachable if WG_SERVER_NET is malformed; fall back to the
		// default overlay rather than fail the download.
		slog.Warn("GET /api/clients/{name}/config: bad server net, using default overlay",
			"server_net", serverNet, "err", err)
		overlay, _ = wgconfig.OverlaySubnet(wgconfig.DefaultServerNet)
	}

	// On AWS the VPC CIDR yields the derived resolver and a [overlay, vpc]
	// split route. Off-AWS, VPCCIDR short-circuits with ErrNotOnEC2 (or returns
	// empty), and we fall back to WG_CLIENT_DNS with an overlay-only split.
	var dns string
	var splitRoutes []string
	if vpcCIDR, vpcErr := s.serverinfoSvc.VPCCIDR(r.Context()); vpcErr == nil && vpcCIDR != "" {
		resolver, resErr := wgconfig.ResolverForVPC(vpcCIDR)
		if resErr != nil {
			slog.Error("GET /api/clients/{name}/config: vpc resolver derivation failed", "err", resErr)
			http.Error(w, resErr.Error(), http.StatusInternalServerError)
			return
		}
		dns = resolver
		splitRoutes = []string{overlay, vpcCIDR}
	} else {
		dns = s.serverinfoSvc.ClientDNS
		if dns == "" {
			dns = serverinfo.DefaultClientDNS
		}
		splitRoutes = []string{overlay}
	}

	endpoint := net.JoinHostPort(info.PublicIP, strconv.Itoa(info.Port))
	mode := wgconfig.ParseMode(r.URL.Query().Get("mode"))
	conf, err := wgconfig.Build(
		wgconfig.Client{Name: client.Name, Address: client.Address},
		mode,
		info.ServerPublicKey,
		endpoint,
		dns,
		splitRoutes,
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
