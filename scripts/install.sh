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
#   WG_CLIENT_DNS          DNS for generated client cfgs (default 1.1.1.1; always
#                          emitted into the dashboard unit)
#   WG_PUBLIC_ENDPOINT     public host/IP for the server (default empty -> not
#                          -endpoint card + client configs   emitted; dashboard
#                          falls back to its own discovery)
#   WG_PUBLIC_IP_ECHO_URL  override for the public-IP echo (default empty -> not
#                          service                           emitted; Go binary
#                          defaults to https://api.ipify.org)
#   Alert knobs / transport secrets (all optional; each written to alerts.env
#   only when set): DASHBOARD_HOST_LABEL, DASHBOARD_ALERT_DISK_PCT,
#   DASHBOARD_ALERT_CPU_PCT, DASHBOARD_ALERT_CPU_SUSTAIN,
#   DASHBOARD_ALERT_TRANSFER_BYTES, DASHBOARD_WEBHOOK_URL,
#   DASHBOARD_SLACK_BOT_TOKEN, DASHBOARD_SLACK_CHANNEL,
#   DASHBOARD_TELEGRAM_TOKEN, DASHBOARD_TELEGRAM_CHAT_ID,
#   DASHBOARD_DISCORD_WEBHOOK_URL.
#
# Usage:
#   sudo bash install.sh                       # install or update (default)
#   sudo bash install.sh --uninstall           # remove stack, KEEP data
#   sudo bash install.sh --purge               # remove stack AND wipe data
#   sudo bash install.sh --dashboard-only      # restrict remove to the dashboard
#   sudo bash install.sh --dashboard-only --purge   # wipe only dashboard data
#
set -euo pipefail

# --- Preconditions ---------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
  echo "FATAL: install.sh must run as root (try: sudo bash install.sh)" >&2
  exit 1
fi

# --- Action dispatch: parse args into an action + modifiers -----------------
# No args        -> install/update (the default). The EC2 user-data wrapper
#                   invokes `bash install.sh` with NO args, so this path must
#                   stay byte-for-byte the install/update flow.
# --uninstall    -> remove the stack but KEEP data (server key, wg0.conf, DB).
# --purge        -> remove the stack AND wipe data (implies remove).
# --dashboard-only -> modifier for remove: tear down only the dashboard and
#                   leave the WireGuard tunnel running.
# Unknown flag   -> usage on stderr, non-zero exit.
# All vars defaulted up-front so the rest of the script is set -u safe.
ACTION=install
PURGE=0
DASHBOARD_ONLY=0
usage() {
  cat >&2 <<'USAGE_EOF'
Usage: install.sh [--uninstall | --purge] [--dashboard-only]
  (no args)         install or update WireGuard (+ dashboard when pinned)
  --uninstall       stop services and remove artifacts; KEEP data
  --purge           --uninstall and also wipe data (server key, wg0.conf, DB)
  --dashboard-only  restrict remove/purge to the dashboard; leave WireGuard up
USAGE_EOF
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --uninstall) ACTION=remove ;;
    --purge) ACTION=remove; PURGE=1 ;;
    --dashboard-only) DASHBOARD_ONLY=1 ;;
    *)
      echo "FATAL: unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
  shift
done

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
# Off-AWS discovery for the dashboard — only consulted when the dashboard is
# installed (harmless reads otherwise). WG_CLIENT_DNS has a default and is always
# emitted into the unit; the other two have no default and are emitted only when
# non-empty (empty -> the Go binary falls back to its own discovery / defaults).
WG_CLIENT_DNS="${WG_CLIENT_DNS:-1.1.1.1}"
WG_PUBLIC_ENDPOINT="${WG_PUBLIC_ENDPOINT:-}"
WG_PUBLIC_IP_ECHO_URL="${WG_PUBLIC_IP_ECHO_URL:-}"
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

# --- remove(): idempotent teardown -----------------------------------------
# Stop/disable services and delete installed artifacts. Every step is guarded
# (existence checks / `|| true`) so it never hard-fails on already-absent units
# or files — safe to re-run and safe on a partially-installed host. Data is KEPT
# by default (server key, wg0.conf, dashboard client DB) so a later reinstall
# keeps the same server identity; --purge additionally wipes it. --dashboard-only
# restricts teardown to the dashboard and leaves the WireGuard tunnel running.
# Ends with a daemon-reload so systemd forgets the removed units.
remove() {
  echo "Removing WireGuard stack (purge=${PURGE}, dashboard-only=${DASHBOARD_ONLY})..."

  # Dashboard teardown (always — there is no WG-only flavour of remove). Every
  # step is guarded, so a host that never had the dashboard installed is fine.
  systemctl disable --now wireguard-dashboard.service 2>/dev/null || true
  systemctl reset-failed wireguard-dashboard.service 2>/dev/null || true
  rm -f /etc/systemd/system/wireguard-dashboard.service
  rm -f /usr/local/sbin/wg-sync
  rm -f /etc/sudoers.d/wireguard-dashboard
  rm -rf /opt/wireguard-dashboard
  if id -u wireguard-dashboard >/dev/null 2>&1; then
    userdel wireguard-dashboard 2>/dev/null || true
  fi

  # WireGuard teardown — skipped under --dashboard-only so the tunnel keeps
  # running while only the dashboard is removed.
  if [ "$DASHBOARD_ONLY" -eq 0 ]; then
    systemctl disable --now wg-quick@wg0.service 2>/dev/null || true
  fi

  # Data: KEEP by default. --purge wipes it.
  if [ "$PURGE" -eq 1 ]; then
    # Dashboard data (client DB + rendered clients.json / alerts.env) — always
    # purged when --purge, regardless of --dashboard-only.
    rm -rf /var/lib/wireguard-dashboard
    rm -rf /etc/wireguard-dashboard
    # WireGuard identity (server key + conf) — purged only when NOT
    # --dashboard-only, so `--dashboard-only --purge` keeps the WG key/conf.
    if [ "$DASHBOARD_ONLY" -eq 0 ]; then
      rm -f "$WG_CONF" "$WG_KEY_FILE"
    fi
  fi

  systemctl daemon-reload

  # Summary: what was removed vs kept.
  echo "==========================================================="
  if [ "$DASHBOARD_ONLY" -eq 1 ]; then
    echo "Removed: dashboard (service, unit, wg-sync, sudoers, /opt, user)."
    echo "Kept:    WireGuard tunnel left running."
  else
    echo "Removed: dashboard + WireGuard service and installed artifacts."
  fi
  if [ "$PURGE" -eq 1 ]; then
    if [ "$DASHBOARD_ONLY" -eq 1 ]; then
      echo "Purged:  dashboard data (/var/lib/wireguard-dashboard, /etc/wireguard-dashboard)."
      echo "Kept:    WireGuard identity (${WG_CONF}, ${WG_KEY_FILE})."
    else
      echo "Purged:  dashboard data + WireGuard identity (server key + wg0.conf)."
      echo "Kept:    nothing — full wipe. A reinstall mints a new server key."
    fi
  else
    echo "Kept:    /etc/wireguard (server key + wg0.conf) and"
    echo "         /var/lib/wireguard-dashboard (client DB) — reinstall keeps identity."
  fi
  echo "==========================================================="
}

# --- Dispatch: remove actions run here and exit before the install body -----
# The root check above already gated us; remove still requires root. The
# install/update body (package install, wg0.conf, dashboard, success summary)
# is reached only when ACTION=install (the no-arg default).
if [ "$ACTION" = "remove" ]; then
  remove
  exit 0
fi

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
# Resolve the key from (in priority): the env var, the persisted server.key, or
# a freshly generated one. Then ALWAYS persist it to a root-only server.key if
# it isn't there yet — including an env-provided key. This is load-bearing for
# safe reruns: without it, a first install that passed WG_SERVER_PRIVATE_KEY
# would never write server.key, so a later rerun WITHOUT the env var would find
# no file and mint a NEW key — changing the server identity and breaking every
# existing client config.
mkdir -p "$WG_DIR"
chmod 0700 "$WG_DIR"
if [ -z "$WG_SERVER_PRIVATE_KEY" ]; then
  if [ -s "$WG_KEY_FILE" ]; then
    echo "Reusing existing server key at $WG_KEY_FILE"
    WG_SERVER_PRIVATE_KEY="$(cat "$WG_KEY_FILE")"
  else
    echo "Generating server private key -> $WG_KEY_FILE"
    WG_SERVER_PRIVATE_KEY="$(wg genkey)"
  fi
fi
# Persist whatever we resolved (env-provided or generated) so reruns reuse the
# same identity. Skip when server.key already holds it (the reuse path above).
if [ ! -s "$WG_KEY_FILE" ]; then
  ( umask 077; printf '%s\n' "$WG_SERVER_PRIVATE_KEY" > "$WG_KEY_FILE" )
  chmod 0600 "$WG_KEY_FILE"
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
#
# Fresh-vs-update is keyed on whether wg0.conf already exists. The peer set is
# re-seeded from WG_PEERS ONLY on a fresh install; on an update the on-disk
# [Peer] stanzas (dashboard-managed or otherwise) are preserved so a rerun never
# clobbers the live peer set with the (possibly empty) WG_PEERS env. Server-key
# reuse is already handled above (env -> persisted server.key -> generate) and
# is unchanged here.

# Capture the existing peer set BEFORE overwriting the conf (update path only):
# the first [Peer] line to EOF — the same merge the wg-sync helper performs. A
# zero-peer conf yields an empty capture, which is valid.
IS_UPDATE=0
EXISTING_PEERS=""
if [ -f "$WG_CONF" ]; then
  IS_UPDATE=1
  EXISTING_PEERS="$(awk '/^\[Peer\]/{p=1} p' "$WG_CONF")"
fi

# Render the new [Interface] block from the current env. Command substitution
# strips trailing newlines, so re-runs converge to the same file (no blank-line
# accumulation), exactly as the wg-sync helper relies on.
NEW_INTERFACE="$(cat <<WG_EOF
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
)"

if [ "$IS_UPDATE" -eq 1 ]; then
  # Update: write the re-rendered [Interface] + a blank-line separator + the
  # PRESERVED on-disk peers. WG_PEERS is intentionally NOT re-applied here — the
  # live/dashboard-managed peer set wins on a rerun.
  {
    printf '%s\n' "$NEW_INTERFACE"
    printf '\n'
    printf '%s\n' "$EXISTING_PEERS"
  } > "$WG_CONF"
else
  # Fresh install: write [Interface] + a blank-line separator + the WG_PEERS
  # stanzas verbatim (empty is valid: zero-peer server). Written via printf so
  # nothing in WG_PEERS is shell-expanded — today's behaviour, unchanged.
  {
    printf '%s\n' "$NEW_INTERFACE"
    printf '\n'
    printf '%s\n' "$WG_PEERS"
  } > "$WG_CONF"
fi

chmod 0600 "$WG_CONF"

# --- Enable IP forwarding ---------------------------------------------------
if ! grep -q '^net.ipv4.ip_forward=1' /etc/sysctl.conf; then
  echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf
fi
sysctl -p

# --- Start / apply WireGuard ------------------------------------------------
# Fresh install: enable + start the unit. Update: if the tunnel is already
# active, apply the new config in place with `wg syncconf` (fed a wg-native
# config via `wg-quick strip`) so existing tunnels are NOT dropped — the same
# live-apply the wg-sync helper performs. If it's an update but the unit isn't
# active (e.g. a rerun after a reboot before wg came up), fall back to enable
# --now so the tunnel still starts.
if [ "$IS_UPDATE" -eq 1 ] && systemctl is-active --quiet wg-quick@wg0.service; then
  wg syncconf wg0 <(wg-quick strip wg0)
else
  systemctl enable --now wg-quick@wg0.service
fi

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
# from a public GitHub Release over HTTPS — no S3, no AWS specifics (those stay
# in the EC2 wrapper). When the tag is empty this whole block is skipped
# (WG-only box).
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

  # 3. Install the privileged peer-sync helper invoked by the dashboard. The Go
  #    service stages a peers-only fragment (only [Peer] stanzas, no [Interface],
  #    no PrivateKey) to /var/lib/wireguard-dashboard/peers.conf, then runs
  #    `sudo /usr/local/sbin/wg-sync` (no args) to merge it into wg0.conf and
  #    apply it live. The helper runs as root; it re-derives the [Interface]
  #    block from the on-disk wg0.conf so the server private key never leaves
  #    /etc/wireguard. Heredoc delimiter is quoted so nothing in the body is
  #    expanded by this installer — the $VARS / $(...) / <(...) belong to the
  #    helper at runtime. Shebang is bash because step 4 below uses process
  #    substitution (<()), which is bash-only.
  cat > /usr/local/sbin/wg-sync <<'WGSYNC_EOF'
#!/usr/bin/env bash
#
# wg-sync — merge the dashboard-staged peers fragment into wg0.conf and apply it
# live, without bouncing the interface. Invoked as: sudo /usr/local/sbin/wg-sync
#
set -euo pipefail

STAGED=/var/lib/wireguard-dashboard/peers.conf
CONF=/etc/wireguard/wg0.conf
IFACE=wg0

if [ ! -r "$STAGED" ]; then
  echo "FATAL: staged peers file $STAGED is missing or unreadable" >&2
  exit 1
fi

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

# Re-derive the [Interface] block from the current on-disk conf: every line up
# to but NOT including the first [Peer] line. This preserves Address/ListenPort/
# PrivateKey/PostUp/PostDown verbatim — the private key is never read from, or
# written by, the dashboard. Command substitution strips trailing newlines, so
# any blank lines below the interface block are dropped; this keeps the merge
# idempotent (re-running with the same staged peers converges to the same file
# instead of accumulating blank lines). If the conf has no [Peer] line (zero
# peers) the whole file becomes the interface block.
INTERFACE_BLOCK="$(awk '/^\[Peer\]/{exit} {print}' "$CONF")"

# Merge: interface block, one blank-line separator, then the staged peers.
{
  printf '%s\n' "$INTERFACE_BLOCK"
  printf '\n'
  cat "$STAGED"
} > "$TMP"

# Atomically replace wg0.conf, root-only (0600).
install -m 0600 -o root -g root "$TMP" "$CONF"

# Apply the new config to the running interface without tearing it down.
# `wg-quick strip` emits a wg-native config (no wg-quick-only keys); process
# substitution feeds it to `wg syncconf`, which diffs and applies peer changes
# only. Existing tunnels with unchanged keys are left untouched.
if ! wg syncconf "$IFACE" <(wg-quick strip "$IFACE"); then
  echo "FATAL: wg syncconf failed for $IFACE" >&2
  exit 1
fi

echo "wg-sync: applied staged peers to $IFACE"
WGSYNC_EOF
  chown root:root /usr/local/sbin/wg-sync
  chmod 0755 /usr/local/sbin/wg-sync

  # 4. Drop sudoers file granting the wireguard-dashboard user NOPASSWD on the
  #    narrow set of commands the Go service needs: read WG and systemd state
  #    (wg show wg0 public-key/dump, systemctl is-active/show wg-quick@wg0) plus
  #    the single privileged write path — the wg-sync helper above (exact-match,
  #    no arguments, so the argv must equal `sudo /usr/local/sbin/wg-sync`).
  #    Write to a staging path first, validate with visudo -c, then atomically
  #    move into /etc/sudoers.d/ — guarantees we never leave a malformed sudoers
  #    fragment that could lock everyone out of sudo.
  cat > /etc/sudoers.d/wireguard-dashboard.tmp <<'SUDOERS_EOF'
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/wg show wg0 public-key
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/wg show wg0 dump
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/systemctl is-active wg-quick@wg0.service
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/systemctl show -p ActiveEnterTimestamp wg-quick@wg0.service
wireguard-dashboard ALL=(root) NOPASSWD: /usr/local/sbin/wg-sync
SUDOERS_EOF
  chown root:root /etc/sudoers.d/wireguard-dashboard.tmp
  chmod 0440 /etc/sudoers.d/wireguard-dashboard.tmp
  visudo -c -f /etc/sudoers.d/wireguard-dashboard.tmp
  mv /etc/sudoers.d/wireguard-dashboard.tmp /etc/sudoers.d/wireguard-dashboard

  # 5. Render the clients manifest the dashboard reads at runtime
  #    (CLIENTS_CONFIG_PATH=/etc/wireguard-dashboard/clients.json). printf '%s'
  #    writes the CLIENTS_JSON value verbatim — no second round of shell
  #    expansion — so any dollar-prefixed or shell-special tokens in the JSON
  #    survive intact. Mode 0640 + group wireguard-dashboard: only the dashboard
  #    user can read it.
  printf '%s\n' "$CLIENTS_JSON" > /etc/wireguard-dashboard/clients.json
  chown root:wireguard-dashboard /etc/wireguard-dashboard/clients.json
  chmod 0640 /etc/wireguard-dashboard/clients.json

  # 5b. Render the alert environment file consumed by the dashboard's systemd
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

  # 6. Download the pinned release binary over HTTPS. With set -e a bare failing
  #    curl already aborts, but we keep an explicit FATAL message (matching
  #    user-data) so a missing asset / private-repo 404 produces a clear
  #    diagnostic instead of a cryptic abort. No checksum verification: release
  #    artifacts are trusted (admin-gated repo + CI/CD-built binaries).
  RELEASE_URL="https://github.com/${DASHBOARD_RELEASE_REPO}/releases/download/${DASHBOARD_RELEASE_TAG}"
  DL_DIR="$(mktemp -d)"
  # The release ships one binary per architecture (wireguard-dashboard-amd64 /
  # wireguard-dashboard-arm64); fetch the one matching this host's GOARCH.
  if ! curl -fsSL "$RELEASE_URL/wireguard-dashboard-$GOARCH" -o "$DL_DIR/wireguard-dashboard-$GOARCH"; then
    echo "FATAL: failed to download dashboard binary from $RELEASE_URL" >&2
    exit 1
  fi

  # 7. Install the binary atomically with the executable bit set, then
  #    drop the temp dir. The arch suffix is dropped here: the installed path
  #    stays /opt/wireguard-dashboard/bin/wireguard-dashboard so the systemd
  #    unit's ExecStart needs no per-arch change.
  install -o root -g root -m 0755 "$DL_DIR/wireguard-dashboard-$GOARCH" /opt/wireguard-dashboard/bin/wireguard-dashboard
  rm -rf "$DL_DIR"

  # 8. Pre-render the optional discovery Environment= lines. WG_PUBLIC_ENDPOINT
  #    and WG_PUBLIC_IP_ECHO_URL have no defaults, so each emits a line only when
  #    set (unset -> the Go binary falls back to its own discovery). The unit
  #    heredoc below is a single unquoted heredoc and can't host a bash `if`, so
  #    we build the lines into a variable here (empty when both are unset) and
  #    interpolate it inline — the runtime form of the alerts.env gating above.
  #    Each rendered line carries its own trailing newline; an empty variable
  #    leaves no blank Environment= line.
  DASHBOARD_OPTIONAL_ENV=""
  if [ -n "$WG_PUBLIC_ENDPOINT" ]; then
    DASHBOARD_OPTIONAL_ENV="${DASHBOARD_OPTIONAL_ENV}Environment=WG_PUBLIC_ENDPOINT=${WG_PUBLIC_ENDPOINT}
"
  fi
  if [ -n "$WG_PUBLIC_IP_ECHO_URL" ]; then
    DASHBOARD_OPTIONAL_ENV="${DASHBOARD_OPTIONAL_ENV}Environment=WG_PUBLIC_IP_ECHO_URL=${WG_PUBLIC_IP_ECHO_URL}
"
  fi

  # 9. Drop the systemd unit. Bound to the WG tunnel IP via the derived
  #    LISTEN_ADDR; Requires/After wg-quick@wg0 ensures the tunnel address is
  #    bindable before start and that the dashboard restarts when WG bounces.
  #    Heredoc is unquoted so ${LISTEN_ADDR}, ${WG_SERVER_NET}, ${WG_CLIENT_DNS}
  #    and the pre-rendered ${DASHBOARD_OPTIONAL_ENV} interpolate — the rest of
  #    the unit body contains no other shell-special tokens.
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
Environment=WG_SERVER_NET=${WG_SERVER_NET}
Environment=WG_CLIENT_DNS=${WG_CLIENT_DNS}
${DASHBOARD_OPTIONAL_ENV}# Leading '-' makes the file optional: the unit starts even if alerts.env is
# absent, so alerting is strictly opt-in via the seeded knobs/webhook above.
EnvironmentFile=-/etc/wireguard-dashboard/alerts.env
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
UNIT_EOF

  # 10. Reload systemd, enable at boot, and (re)start the unit. `restart` —
  #     not `enable --now` — is deliberate: `--now` only *starts* an inactive
  #     unit and is a no-op when it is already running, so a re-run that ships a
  #     NEW binary would leave the OLD process live until a reboot. `restart`
  #     starts it if stopped and swaps the process if running, so re-running the
  #     installer always picks up the freshly downloaded binary.
  systemctl daemon-reload
  systemctl enable wireguard-dashboard.service
  systemctl restart wireguard-dashboard.service
fi

# --- Example first-client config (template only) ----------------------------
# Print an illustrative wg-quick client config to stdout so the operator can
# connect the FIRST client straight from the install output — the chicken-and-egg
# case, since the dashboard is VPN-gated (you can't reach it until you're already
# a peer). This is a TEMPLATE only: no keypair is generated and no private key is
# ever created or stored on the server. Reuses values already computed above
# (SERVER_PUBLIC_KEY, WG_SERVER_PORT, WG_CLIENT_DNS, WG_SERVER_NET).
#
# First-client IP: assume the server holds the first host of WG_SERVER_NET (the
# conventional `.1`), so the first client takes the next address (`.1` -> `.2`).
# For the default 172.16.15.1/24 this yields 172.16.15.2/32.
emit_example_client_config() {
  local server_host net_prefix last_octet first_client_ip
  server_host="${WG_SERVER_NET%/*}"   # drop CIDR suffix -> 172.16.15.1
  net_prefix="${server_host%.*}"      # leading octets   -> 172.16.15
  last_octet="${server_host##*.}"     # final octet      -> 1
  first_client_ip="${net_prefix}.$((last_octet + 1))/32"

  echo "Example client config (first peer):"
  echo "-----------------------------------------------------------"
  # Unquoted heredoc: only the ${...} tokens below expand. The <placeholder>
  # tokens carry no '$', so they stay literal (the operator fills them in).
  cat <<CLIENT_EOF
[Interface]
PrivateKey = <paste the client's private key here>
Address = ${first_client_ip}
DNS = ${WG_CLIENT_DNS}

[Peer]
PublicKey = ${SERVER_PUBLIC_KEY}
Endpoint = <server-public-ip>:${WG_SERVER_PORT}
# Full-tunnel (route all traffic). Split-tunnel alternative: route only the VPN
# overlay, e.g. AllowedIPs = ${net_prefix}.0/24
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
CLIENT_EOF
  echo "-----------------------------------------------------------"
  # The server never holds the client's private key: generate the keypair off-host
  # and register only the PUBLIC key (dashboard, or the WG_PEERS env on reinstall).
  echo "Hint: generate the keypair off-host with 'wg genkey | tee privatekey | wg pubkey',"
  echo "      then register the client's PUBLIC key via the dashboard or the WG_PEERS env."
}

echo "==========================================================="
echo "WireGuard server is up."
echo "  Server public key : ${SERVER_PUBLIC_KEY}"
echo "  Listen port (UDP) : ${WG_SERVER_PORT}"
echo "Use these to build client configs (endpoint = <this-host-ip>:${WG_SERVER_PORT})."
if [ -n "${DASHBOARD_RELEASE_TAG:-}" ]; then
  echo "Dashboard URL     : http://${WG_SERVER_NET%/*}:${DASHBOARD_PORT}"
fi
echo "==========================================================="
# Printed on BOTH the WG-only and dashboard paths — the server (and thus this
# example) always exists regardless of whether the dashboard was installed.
emit_example_client_config
