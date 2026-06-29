#!/usr/bin/env bash
#
# install.sh — standalone WireGuard *server* bootstrap for Ubuntu.
#
# Purpose:
#   Install WireGuard, write /etc/wireguard/wg0.conf (address, port, server key,
#   iptables NAT + forwarding on the auto-detected egress interface), enable IP
#   forwarding, and bring up wg-quick@wg0 — on any plain Ubuntu host, with no
#   AWS / Terraform dependencies. This is the portable core that the EC2
#   user-data wrapper (and, later, the dashboard install in Slice 2) build on.
#
# Requirements:
#   - Ubuntu only (uses apt-get).
#   - Must run as root (writes /etc/wireguard, loads kernel modules, edits
#     sysctl, manages systemd). Example: sudo bash install.sh
#
# Environment contract (all optional; defaults applied if unset/empty):
#   WG_SERVER_NET          [Interface] Address          (default 172.16.15.1/24)
#   WG_SERVER_PORT         ListenPort + UDP dport        (default 51820)
#   WG_SERVER_PRIVATE_KEY  server private key            (default: generate via
#                          `wg genkey`, persisted to /etc/wireguard/server.key)
#   WG_PEERS               [Peer] stanzas, verbatim      (default empty — the
#                          server comes up with zero peers, which is valid)
#
# Optional WireGuard dashboard (installed only when DASHBOARD_RELEASE_TAG is set):
#   DASHBOARD_RELEASE_TAG  GitHub Release tag to install (default empty -> skip
#                          the whole dashboard install; WG-only box)
#   DASHBOARD_RELEASE_REPO owner/repo to fetch the release from (required when
#                          DASHBOARD_RELEASE_TAG is set; fail hard if missing)
#   DASHBOARD_PORT         dashboard bind port           (default 8080)
#   CLIENTS_JSON           clients manifest written to   (default [])
#                          /etc/wireguard-dashboard/clients.json
#   Alert knobs / transport secrets (all optional; each written to alerts.env
#   only when set): DASHBOARD_HOST_LABEL, DASHBOARD_ALERT_DISK_PCT,
#   DASHBOARD_ALERT_CPU_PCT, DASHBOARD_ALERT_CPU_SUSTAIN,
#   DASHBOARD_ALERT_TRANSFER_BYTES, DASHBOARD_WEBHOOK_URL,
#   DASHBOARD_SLACK_BOT_TOKEN, DASHBOARD_SLACK_CHANNEL,
#   DASHBOARD_TELEGRAM_TOKEN, DASHBOARD_TELEGRAM_CHAT_ID,
#   DASHBOARD_DISCORD_WEBHOOK_URL.
#
# Usage:
#   sudo bash install.sh
#
set -euo pipefail

# --- Preconditions ---------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
  echo "FATAL: install.sh must run as root (try: sudo bash install.sh)" >&2
  exit 1
fi

# --- Env contract: read from environment, apply defaults -------------------
WG_SERVER_NET="${WG_SERVER_NET:-172.16.15.1/24}"
WG_SERVER_PORT="${WG_SERVER_PORT:-51820}"
WG_SERVER_PRIVATE_KEY="${WG_SERVER_PRIVATE_KEY:-}"
WG_PEERS="${WG_PEERS:-}"

# Optional dashboard: empty DASHBOARD_RELEASE_TAG -> the whole dashboard block
# is skipped (see the gate below). DASHBOARD_RELEASE_REPO has no default; it is
# required when the tag is set and validated inside the gate.
DASHBOARD_RELEASE_TAG="${DASHBOARD_RELEASE_TAG:-}"
DASHBOARD_RELEASE_REPO="${DASHBOARD_RELEASE_REPO:-}"
DASHBOARD_PORT="${DASHBOARD_PORT:-8080}"
CLIENTS_JSON="${CLIENTS_JSON:-[]}"
# Alert knobs / transport secrets — all optional; each is written to alerts.env
# only when non-empty (runtime equivalent of user-data's `%{ if … ~}` gates).
DASHBOARD_HOST_LABEL="${DASHBOARD_HOST_LABEL:-}"
DASHBOARD_ALERT_DISK_PCT="${DASHBOARD_ALERT_DISK_PCT:-}"
DASHBOARD_ALERT_CPU_PCT="${DASHBOARD_ALERT_CPU_PCT:-}"
DASHBOARD_ALERT_CPU_SUSTAIN="${DASHBOARD_ALERT_CPU_SUSTAIN:-}"
DASHBOARD_ALERT_TRANSFER_BYTES="${DASHBOARD_ALERT_TRANSFER_BYTES:-}"
DASHBOARD_WEBHOOK_URL="${DASHBOARD_WEBHOOK_URL:-}"
DASHBOARD_SLACK_BOT_TOKEN="${DASHBOARD_SLACK_BOT_TOKEN:-}"
DASHBOARD_SLACK_CHANNEL="${DASHBOARD_SLACK_CHANNEL:-}"
DASHBOARD_TELEGRAM_TOKEN="${DASHBOARD_TELEGRAM_TOKEN:-}"
DASHBOARD_TELEGRAM_CHAT_ID="${DASHBOARD_TELEGRAM_CHAT_ID:-}"
DASHBOARD_DISCORD_WEBHOOK_URL="${DASHBOARD_DISCORD_WEBHOOK_URL:-}"

WG_DIR=/etc/wireguard
WG_CONF="$WG_DIR/wg0.conf"
WG_KEY_FILE="$WG_DIR/server.key"

# --- Wait for the apt/dpkg lock (unattended-upgrades on fresh boots) --------
echo "Waiting for apt lock..."
while fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1; do
  sleep 5
done

# --- Install packages ------------------------------------------------------
apt-get update -y
apt-get install -y wireguard wireguard-tools
# iptables is present on most Ubuntu images, but the NAT/forwarding PostUp
# rules below depend on it — install only if it's missing.
if ! command -v iptables >/dev/null 2>&1; then
  apt-get install -y iptables
fi

# --- CPU architecture detection --------------------------------------------
# Unused in this slice (server-only), but Slice 2's dashboard release binary is
# published per Go arch, so the detection lives here and is consumed there.
#   ARCH   - `uname -m` value (x86_64 / aarch64)
#   GOARCH - Go convention used for release asset names (amd64 / arm64)
# Anything else is unsupported: fail hard so it surfaces instead of silently
# fetching a wrong-arch binary later.
case "$(uname -m)" in
  x86_64)
    ARCH=x86_64
    GOARCH=amd64
    ;;
  aarch64)
    ARCH=aarch64
    GOARCH=arm64
    ;;
  *)
    echo "FATAL: unsupported CPU architecture $(uname -m)" >&2
    exit 1
    ;;
esac
export ARCH GOARCH

# --- Server private key -----------------------------------------------------
# If provided via env, use it as-is. Otherwise generate one and persist it to a
# root-only file so it survives across re-runs / reboots.
mkdir -p "$WG_DIR"
chmod 0700 "$WG_DIR"
if [ -z "$WG_SERVER_PRIVATE_KEY" ]; then
  if [ -s "$WG_KEY_FILE" ]; then
    echo "Reusing existing server key at $WG_KEY_FILE"
    WG_SERVER_PRIVATE_KEY="$(cat "$WG_KEY_FILE")"
  else
    echo "Generating server private key -> $WG_KEY_FILE"
    WG_SERVER_PRIVATE_KEY="$(wg genkey)"
    ( umask 077; printf '%s\n' "$WG_SERVER_PRIVATE_KEY" > "$WG_KEY_FILE" )
    chmod 0600 "$WG_KEY_FILE"
  fi
fi

# --- Detect the egress interface -------------------------------------------
# Portable across cloud and bare-metal: ask the kernel which interface it would
# use to reach the public internet, rather than assuming eth0.
ENI="$(ip route get 8.8.8.8 | grep 8.8.8.8 | awk '{print $5}')"
if [ -z "$ENI" ]; then
  echo "FATAL: could not detect egress network interface" >&2
  exit 1
fi
echo "Egress interface: $ENI"

# --- Write /etc/wireguard/wg0.conf -----------------------------------------
# NAT + forwarding rules are carried over verbatim from the EC2 user-data; only
# the interface name (ENI) and the UDP dport (WG_SERVER_PORT) are substituted.
cat > "$WG_CONF" <<WG_EOF
[Interface]
Address = ${WG_SERVER_NET}
PrivateKey = ${WG_SERVER_PRIVATE_KEY}
ListenPort = ${WG_SERVER_PORT}
PostUp = iptables -I INPUT -p udp --dport ${WG_SERVER_PORT} -j ACCEPT
PostUp = iptables -I FORWARD -i ${ENI} -o wg0 -j ACCEPT
PostUp = iptables -I FORWARD -i wg0 -j ACCEPT
PostUp = iptables -t nat -A POSTROUTING -o ${ENI} -j MASQUERADE
PostUp = ip6tables -I FORWARD -i wg0 -j ACCEPT
PostUp = ip6tables -t nat -A POSTROUTING -o ${ENI} -j MASQUERADE
PostDown = iptables -D INPUT -p udp --dport ${WG_SERVER_PORT} -j ACCEPT
PostDown = iptables -D FORWARD -i ${ENI} -o wg0 -j ACCEPT
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT
PostDown = iptables -t nat -D POSTROUTING -o ${ENI} -j MASQUERADE
PostDown = ip6tables -D FORWARD -i wg0 -j ACCEPT
PostDown = ip6tables -t nat -D POSTROUTING -o ${ENI} -j MASQUERADE
WG_EOF

# Append peer stanzas verbatim (empty is valid: zero-peer server). Written
# separately from the heredoc so nothing in WG_PEERS is shell-expanded.
{
  printf '\n'
  printf '%s\n' "$WG_PEERS"
} >> "$WG_CONF"

chmod 0600 "$WG_CONF"

# --- Enable IP forwarding ---------------------------------------------------
if ! grep -q '^net.ipv4.ip_forward=1' /etc/sysctl.conf; then
  echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf
fi
sysctl -p

# --- Start WireGuard --------------------------------------------------------
systemctl enable --now wg-quick@wg0.service

# --- Success gate -----------------------------------------------------------
if ! systemctl is-active --quiet wg-quick@wg0.service; then
  echo "FATAL: wg-quick@wg0 is not active after start; check 'journalctl -u wg-quick@wg0'" >&2
  exit 1
fi

# Derive the public key from the private key (never print the private key).
SERVER_PUBLIC_KEY="$(printf '%s' "$WG_SERVER_PRIVATE_KEY" | wg pubkey)"

# --- Optional WireGuard dashboard ------------------------------------------
# Provisioned only when DASHBOARD_RELEASE_TAG is non-empty. Reached only after
# the WG success gate above, so the dashboard's systemd unit (Requires=
# wg-quick@wg0) has a confirmed-active tunnel to bind to. The binary is fetched
# from a public GitHub Release over HTTPS and verified against its published
# SHA256SUMS before install — no S3, no AWS specifics (those stay in the EC2
# wrapper). When the tag is empty this whole block is skipped (WG-only box).
if [ -n "${DASHBOARD_RELEASE_TAG:-}" ]; then
  # The repo is mandatory once a tag is pinned — without it there's nowhere to
  # fetch the release from. Fail hard with a clear message rather than building
  # a half-formed URL.
  if [ -z "$DASHBOARD_RELEASE_REPO" ]; then
    echo "FATAL: DASHBOARD_RELEASE_TAG is set but DASHBOARD_RELEASE_REPO is empty" >&2
    exit 1
  fi

  # curl is required for the release download. Slice 1 deliberately omitted it
  # (the server-only path needs no HTTP), so install it here if missing.
  # sha256sum is coreutils (always present); no unzip is needed.
  if ! command -v curl >/dev/null 2>&1; then
    apt-get install -y curl
  fi

  # Derive the bind address from the WG server address: strip the CIDR suffix
  # off WG_SERVER_NET and append the dashboard port. Replaces the user-data's
  # hardcoded 172.16.15.1:8080 so a host on a different subnet still works.
  LISTEN_ADDR="${WG_SERVER_NET%/*}:${DASHBOARD_PORT}"

  # 1. Create the dedicated system user (idempotent).
  id -u wireguard-dashboard >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin wireguard-dashboard

  # 2. Create directories with the right ownership/modes.
  install -d -o root -g root -m 0755 /opt/wireguard-dashboard/bin
  install -d -o wireguard-dashboard -g wireguard-dashboard -m 0750 /var/lib/wireguard-dashboard
  install -d -o root -g root -m 0755 /etc/wireguard-dashboard

  # 3. Drop sudoers file granting the wireguard-dashboard user NOPASSWD on the
  #    narrow set of commands the Go service needs to read WG and systemd state
  #    (wg show wg0 public-key/dump, systemctl is-active/show wg-quick@wg0).
  #    Write to a staging path first, validate with visudo -c, then atomically
  #    move into /etc/sudoers.d/ — guarantees we never leave a malformed sudoers
  #    fragment that could lock everyone out of sudo.
  cat > /etc/sudoers.d/wireguard-dashboard.tmp <<'SUDOERS_EOF'
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/wg show wg0 public-key
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/wg show wg0 dump
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/systemctl is-active wg-quick@wg0.service
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/systemctl show -p ActiveEnterTimestamp wg-quick@wg0.service
SUDOERS_EOF
  chown root:root /etc/sudoers.d/wireguard-dashboard.tmp
  chmod 0440 /etc/sudoers.d/wireguard-dashboard.tmp
  visudo -c -f /etc/sudoers.d/wireguard-dashboard.tmp
  mv /etc/sudoers.d/wireguard-dashboard.tmp /etc/sudoers.d/wireguard-dashboard

  # 4. Render the clients manifest the dashboard reads at runtime
  #    (CLIENTS_CONFIG_PATH=/etc/wireguard-dashboard/clients.json). printf '%s'
  #    writes the CLIENTS_JSON value verbatim — no second round of shell
  #    expansion — so any dollar-prefixed or shell-special tokens in the JSON
  #    survive intact. Mode 0640 + group wireguard-dashboard: only the dashboard
  #    user can read it.
  printf '%s\n' "$CLIENTS_JSON" > /etc/wireguard-dashboard/clients.json
  chown root:wireguard-dashboard /etc/wireguard-dashboard/clients.json
  chmod 0640 /etc/wireguard-dashboard/clients.json

  # 4b. Render the alert environment file consumed by the dashboard's systemd
  #     unit (EnvironmentFile=-/etc/wireguard-dashboard/alerts.env). Each line is
  #     emitted with printf '%s' so the values (the webhook URLs and bot tokens
  #     in particular are secrets and may contain shell-special characters) are
  #     never re-expanded. This is the runtime form of user-data's `%{ if … ~}`
  #     gates: every knob/secret is written only when its env var is non-empty,
  #     so an unset transport leaves no line and the Go side falls back to its
  #     default (e.g. os.Hostname() when DASHBOARD_HOST_LABEL is absent). Mode
  #     0640 + group wireguard-dashboard: only the dashboard user can read it.
  {
    if [ -n "$DASHBOARD_HOST_LABEL" ]; then printf 'DASHBOARD_HOST_LABEL=%s\n' "$DASHBOARD_HOST_LABEL"; fi
    if [ -n "$DASHBOARD_ALERT_DISK_PCT" ]; then printf 'DASHBOARD_ALERT_DISK_PCT=%s\n' "$DASHBOARD_ALERT_DISK_PCT"; fi
    if [ -n "$DASHBOARD_ALERT_CPU_PCT" ]; then printf 'DASHBOARD_ALERT_CPU_PCT=%s\n' "$DASHBOARD_ALERT_CPU_PCT"; fi
    if [ -n "$DASHBOARD_ALERT_CPU_SUSTAIN" ]; then printf 'DASHBOARD_ALERT_CPU_SUSTAIN=%s\n' "$DASHBOARD_ALERT_CPU_SUSTAIN"; fi
    if [ -n "$DASHBOARD_ALERT_TRANSFER_BYTES" ]; then printf 'DASHBOARD_ALERT_TRANSFER_BYTES=%s\n' "$DASHBOARD_ALERT_TRANSFER_BYTES"; fi
    if [ -n "$DASHBOARD_SLACK_BOT_TOKEN" ]; then printf 'DASHBOARD_SLACK_BOT_TOKEN=%s\n' "$DASHBOARD_SLACK_BOT_TOKEN"; fi
    if [ -n "$DASHBOARD_SLACK_CHANNEL" ]; then printf 'DASHBOARD_SLACK_CHANNEL=%s\n' "$DASHBOARD_SLACK_CHANNEL"; fi
    if [ -n "$DASHBOARD_TELEGRAM_TOKEN" ]; then printf 'DASHBOARD_TELEGRAM_TOKEN=%s\n' "$DASHBOARD_TELEGRAM_TOKEN"; fi
    if [ -n "$DASHBOARD_TELEGRAM_CHAT_ID" ]; then printf 'DASHBOARD_TELEGRAM_CHAT_ID=%s\n' "$DASHBOARD_TELEGRAM_CHAT_ID"; fi
    if [ -n "$DASHBOARD_DISCORD_WEBHOOK_URL" ]; then printf 'DASHBOARD_DISCORD_WEBHOOK_URL=%s\n' "$DASHBOARD_DISCORD_WEBHOOK_URL"; fi
    if [ -n "$DASHBOARD_WEBHOOK_URL" ]; then printf 'DASHBOARD_WEBHOOK_URL=%s\n' "$DASHBOARD_WEBHOOK_URL"; fi
  } > /etc/wireguard-dashboard/alerts.env
  chown root:wireguard-dashboard /etc/wireguard-dashboard/alerts.env
  chmod 0640 /etc/wireguard-dashboard/alerts.env

  # 5. Download the pinned release binary + its checksum over HTTPS and verify
  #    before installing. With set -e a bare failing curl already aborts, but we
  #    keep explicit FATAL messages (matching user-data) so a missing asset /
  #    private-repo 404 or a SHA256 mismatch produces a clear diagnostic instead
  #    of a cryptic abort, and a wrong/partial binary never goes live.
  RELEASE_URL="https://github.com/${DASHBOARD_RELEASE_REPO}/releases/download/${DASHBOARD_RELEASE_TAG}"
  DL_DIR="$(mktemp -d)"
  # The release ships one binary per architecture (wireguard-dashboard-amd64 /
  # wireguard-dashboard-arm64); fetch the one matching this host's GOARCH.
  if ! curl -fsSL "$RELEASE_URL/wireguard-dashboard-$GOARCH" -o "$DL_DIR/wireguard-dashboard-$GOARCH"; then
    echo "FATAL: failed to download dashboard binary from $RELEASE_URL" >&2
    exit 1
  fi
  if ! curl -fsSL "$RELEASE_URL/SHA256SUMS" -o "$DL_DIR/SHA256SUMS"; then
    echo "FATAL: failed to download SHA256SUMS from $RELEASE_URL" >&2
    exit 1
  fi
  # SHA256SUMS lists both arch binaries under their bare filenames
  # (wireguard-dashboard-amd64 / wireguard-dashboard-arm64), so verify from
  # within DL_DIR using the same arch-suffixed name we downloaded.
  # --ignore-missing makes sha256sum check only the file we actually fetched and
  # skip the other arch's entry (and any future assets) instead of failing.
  if ! ( cd "$DL_DIR" && sha256sum -c --ignore-missing SHA256SUMS ); then
    echo "FATAL: dashboard binary failed SHA256 verification" >&2
    exit 1
  fi

  # 6. Install the verified binary atomically with the executable bit set, then
  #    drop the temp dir. The arch suffix is dropped here: the installed path
  #    stays /opt/wireguard-dashboard/bin/wireguard-dashboard so the systemd
  #    unit's ExecStart needs no per-arch change.
  install -o root -g root -m 0755 "$DL_DIR/wireguard-dashboard-$GOARCH" /opt/wireguard-dashboard/bin/wireguard-dashboard
  rm -rf "$DL_DIR"

  # 7. Drop the systemd unit. Bound to the WG tunnel IP via the derived
  #    LISTEN_ADDR; Requires/After wg-quick@wg0 ensures the tunnel address is
  #    bindable before start and that the dashboard restarts when WG bounces.
  #    Heredoc is unquoted so ${LISTEN_ADDR} interpolates — the rest of the unit
  #    body contains no other shell-special tokens.
  cat > /etc/systemd/system/wireguard-dashboard.service <<UNIT_EOF
[Unit]
Description=WireGuard VPN dashboard
After=network-online.target wg-quick@wg0.service
Wants=network-online.target
Requires=wg-quick@wg0.service

[Service]
Type=simple
User=wireguard-dashboard
Group=wireguard-dashboard
ExecStart=/opt/wireguard-dashboard/bin/wireguard-dashboard
Environment=LISTEN_ADDR=${LISTEN_ADDR}
# Leading '-' makes the file optional: the unit starts even if alerts.env is
# absent, so alerting is strictly opt-in via the seeded knobs/webhook above.
EnvironmentFile=-/etc/wireguard-dashboard/alerts.env
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
UNIT_EOF

  # 8. Reload systemd and bring the unit up.
  systemctl daemon-reload
  systemctl enable --now wireguard-dashboard.service
fi

echo "==========================================================="
echo "WireGuard server is up."
echo "  Server public key : ${SERVER_PUBLIC_KEY}"
echo "  Listen port (UDP) : ${WG_SERVER_PORT}"
echo "Use these to build client configs (endpoint = <this-host-ip>:${WG_SERVER_PORT})."
if [ -n "${DASHBOARD_RELEASE_TAG:-}" ]; then
  echo "Dashboard URL     : http://${WG_SERVER_NET%/*}:${DASHBOARD_PORT}"
fi
echo "==========================================================="
