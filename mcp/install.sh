#!/usr/bin/env sh
#
# install.sh — installer for the wireguard-mcp binary (laptop-side MCP server).
#
# Purpose:
#   Detect OS/arch, download the matching GitHub Release archive for
#   wireguard-mcp plus its checksums.txt, verify the archive's SHA-256 silently
#   under the hood, extract the binary, and install it to a bindir (default
#   /usr/local/bin). Prints a ready-to-edit `claude mcp add` registration line
#   so the operator can wire it into an MCP host. No manual checksum step is
#   ever required of the user, but integrity is always checked before install.
#
# Usage (one-liner, piped into sh — see the "POSIX sh" note below):
#   curl -fsSL https://raw.githubusercontent.com/vkatrichenko/wireguard-vpn/main/mcp/install.sh | sh
#
# Environment (all optional):
#   MCP_VERSION   pin a specific version, e.g. "0.0.3" or "v0.0.3" (default:
#                 resolve the latest "mcp/vX.Y.Z" GitHub Release tag; falls
#                 back to a pinned version if that resolution fails).
#   BINDIR        install directory (default /usr/local/bin). Mainly for
#                 testing without touching the real system path.
#
# Note on POSIX sh:
#   The one-liner above pipes this script into `sh`, so the shebang is not
#   honored by the pipe — only by direct execution. Every construct below is
#   intentionally POSIX sh: no `[[ ]]`, no arrays, no `local`, no
#   `set -o pipefail`, no `${var,,}`. This is load-bearing, not stylistic.
#
set -eu

# --- Preflight: required tools ----------------------------------------------
# curl and tar are non-negotiable (download + extract). A sha256 tool is also
# required for the mandatory integrity check: prefer sha256sum (GNU/Linux),
# fall back to shasum -a 256 (macOS, no sha256sum by default).
if ! command -v curl >/dev/null 2>&1; then
  echo "FATAL: curl is required but was not found in PATH" >&2
  exit 1
fi
if ! command -v tar >/dev/null 2>&1; then
  echo "FATAL: tar is required but was not found in PATH" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  SHA_TOOL=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  SHA_TOOL=shasum
else
  echo "FATAL: no SHA-256 tool found (need sha256sum or shasum)" >&2
  exit 1
fi

# --- Workdir + cleanup -------------------------------------------------------
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# --- Detect platform ---------------------------------------------------------
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux | darwin) ;;
  *)
    echo "FATAL: unsupported OS '$os' — download manually from the releases page:" >&2
    echo "       https://github.com/vkatrichenko/wireguard-vpn/releases" >&2
    exit 1
    ;;
esac

arch_raw="$(uname -m)"
case "$arch_raw" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *)
    echo "FATAL: unsupported CPU architecture '$arch_raw'" >&2
    exit 1
    ;;
esac

# --- Resolve version ----------------------------------------------------------
# Priority: MCP_VERSION override > latest "mcp/vX.Y.Z" release tag > pinned
# fallback. The repo also publishes dashboard vX.Y.Z tags (no "mcp/" prefix),
# so the API response is filtered to the mcp/ prefix specifically — picking
# the first bare "vX.Y.Z" match would silently install the wrong artifact.
FALLBACK_VERSION=0.0.3
MCP_VERSION="${MCP_VERSION:-}"

if [ -n "$MCP_VERSION" ]; then
  # Normalize an optional leading "v" (both "0.0.3" and "v0.0.3" are accepted).
  ver_num="${MCP_VERSION#v}"
else
  ver_num=""
  releases_json="$(curl -fsSL https://api.github.com/repos/vkatrichenko/wireguard-vpn/releases 2>/dev/null || true)"
  if [ -n "$releases_json" ]; then
    # Releases come back newest-first; take the FIRST tag_name that starts
    # with "mcp/v" and strip everything but the version number.
    tag="$(printf '%s\n' "$releases_json" | grep -o '"tag_name": *"mcp/v[^"]*"' | head -n 1 | sed 's/.*"mcp\/v\([^"]*\)"/\1/')"
    if [ -n "$tag" ]; then
      ver_num="$tag"
    fi
  fi
  if [ -z "$ver_num" ]; then
    echo "WARNING: could not resolve latest mcp/ release tag; falling back to pinned v${FALLBACK_VERSION}" >&2
    ver_num="$FALLBACK_VERSION"
  fi
fi

tag="mcp/v${ver_num}"

# --- Build URLs ----------------------------------------------------------------
# The release tag contains a literal slash (mcp/vX.Y.Z) — that's a normal path
# segment in a GitHub release download URL, not an escaping concern.
base_url="https://github.com/vkatrichenko/wireguard-vpn/releases/download/${tag}"
archive_name="wireguard-mcp_${ver_num}_${os}_${arch}.tar.gz"
archive_url="${base_url}/${archive_name}"
checksums_url="${base_url}/checksums.txt"

archive_path="${WORKDIR}/${archive_name}"
checksums_path="${WORKDIR}/checksums.txt"

# --- Download ------------------------------------------------------------------
echo "Installing wireguard-mcp ${ver_num} (${os}/${arch})..."
if ! curl -fsSL -o "$archive_path" "$archive_url"; then
  echo "FATAL: failed to download $archive_url" >&2
  exit 1
fi
if ! curl -fsSL -o "$checksums_path" "$checksums_url"; then
  echo "FATAL: failed to download $checksums_url" >&2
  exit 1
fi

# --- Verify SHA-256 (silent, portable) ------------------------------------------
# checksums.txt lines are "<sha256>  <filename>". Grep the exact archive
# filename (anchored at end of line) rather than relying on
# `sha256sum -c --ignore-missing`, which shasum on macOS does not support.
expected="$(grep " ${archive_name}\$" "$checksums_path" | awk '{print $1}')"
if [ -z "$expected" ]; then
  echo "FATAL: ${archive_name} not found in checksums.txt — refusing to install" >&2
  exit 1
fi

if [ "$SHA_TOOL" = sha256sum ]; then
  actual="$(sha256sum "$archive_path" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$archive_path" | awk '{print $1}')"
fi

if [ "$expected" != "$actual" ]; then
  echo "FATAL: checksum mismatch for ${archive_name}" >&2
  echo "       expected: $expected" >&2
  echo "       actual:   $actual" >&2
  exit 1
fi
echo "✓ checksum verified"

# --- Optional: cosign signature verification (best-effort, never blocking) -----
# SHA-256 above is the integrity baseline that must always pass. Cosign is a
# nice-to-have on top of it: if it's absent, or verification errors for any
# reason, skip it silently rather than failing the install.
if command -v cosign >/dev/null 2>&1; then
  sig_ok=1
  curl -fsSL -o "${WORKDIR}/checksums.txt.sig" "${base_url}/checksums.txt.sig" >/dev/null 2>&1 || sig_ok=0
  curl -fsSL -o "${WORKDIR}/checksums.txt.pem" "${base_url}/checksums.txt.pem" >/dev/null 2>&1 || sig_ok=0
  if [ "$sig_ok" -eq 1 ]; then
    cosign verify-blob \
      --certificate "${WORKDIR}/checksums.txt.pem" \
      --signature "${WORKDIR}/checksums.txt.sig" \
      "$checksums_path" >/dev/null 2>&1 || true
  fi
fi

# --- Extract ---------------------------------------------------------------
tar -xzf "$archive_path" -C "$WORKDIR" wireguard-mcp

# --- macOS: drop the quarantine attribute so Gatekeeper doesn't block it -----
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "${WORKDIR}/wireguard-mcp" 2>/dev/null || true
fi

# --- Install -----------------------------------------------------------------
BINDIR="${BINDIR:-/usr/local/bin}"
installed_path="${BINDIR}/wireguard-mcp"

if [ -w "$BINDIR" ] || { [ ! -e "$BINDIR" ] && mkdir -p "$BINDIR" 2>/dev/null; }; then
  mv "${WORKDIR}/wireguard-mcp" "$installed_path"
elif command -v sudo >/dev/null 2>&1; then
  sudo mv "${WORKDIR}/wireguard-mcp" "$installed_path"
else
  echo "FATAL: $BINDIR is not writable and sudo is unavailable." >&2
  echo "       Re-run with sudo, or set BINDIR to a writable directory." >&2
  exit 1
fi
chmod +x "$installed_path"

# --- Success summary -----------------------------------------------------------
echo "==========================================================="
echo "wireguard-mcp ${ver_num} installed -> ${installed_path}"
echo ""
echo "Register it with your MCP host, filling in the dashboard's tunnel address:"
echo "  claude mcp add wireguard-vpn --env MCP_DASHBOARD_ADDR=<ip>:8080 -- ${installed_path}"
echo "==========================================================="
