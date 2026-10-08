#!/usr/bin/env bash
# tbound installer.
#
#   curl -fsSL https://get.tbound.dev | sh
#   install.sh --version <ver> [--prefix <dir>] [--base-url <url> | --local-dist <dir>]
#   install.sh uninstall [--prefix <dir>]
#
# Installs the tbound and tbound-doctor binaries plus the runtime bundle into a
# user-owned prefix. It never uses sudo and never touches the user's global Node
# or Pi install. Artifacts are checksum-verified before extraction.
set -euo pipefail

PROG="install.sh"
DEFAULT_VERSION="latest"
NODE_MIN="22.19"

log()  { printf 'tbound-install: %s\n' "$*"; }
die()  { printf 'tbound-install: error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

TBOUND_INSTALL_TMP=""
cleanup() { [ -n "${TBOUND_INSTALL_TMP:-}" ] && rm -rf -- "$TBOUND_INSTALL_TMP" || true; }
trap cleanup EXIT

default_prefix() {
  if [ -n "${TBOUND_PREFIX:-}" ]; then printf '%s' "$TBOUND_PREFIX"; return; fi
  if [ -n "${XDG_DATA_HOME:-}" ]; then printf '%s/tbound' "$XDG_DATA_HOME"; return; fi
  printf '%s/.local/share/tbound' "$HOME"
}
config_dir() {
  if [ -n "${XDG_CONFIG_HOME:-}" ]; then printf '%s/tbound' "$XDG_CONFIG_HOME"; return; fi
  printf '%s/.config/tbound' "$HOME"
}

detect_os() {
  case "$(uname -s)" in
    Linux)  printf 'linux' ;;
    Darwin) printf 'darwin' ;;
    *) die "unsupported OS '$(uname -s)'; use the release tarball or npm package" ;;
  esac
}
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    *) die "unsupported architecture '$(uname -m)'" ;;
  esac
}

sha256_file() {
  if have sha256sum; then sha256sum "$1" | awk '{print $1}';
  elif have shasum; then shasum -a 256 "$1" | awk '{print $1}';
  else die "need sha256sum or shasum to verify the download"; fi
}

uninstall() {
  local prefix
  prefix="$(default_prefix)"
  while [ $# -gt 0 ]; do
    case "$1" in
      --prefix) prefix="${2:?--prefix needs a value}"; shift 2 ;;
      *) die "unexpected argument: $1" ;;
    esac
  done
  case "$prefix" in
    ""|"/"|"$HOME") die "refusing to remove '$prefix'" ;;
  esac
  if [ -d "$prefix" ]; then
    log "removing $prefix"
    rm -rf -- "$prefix"
  else
    log "nothing to remove at $prefix"
  fi
  log "done (config left at $(config_dir)/config.json if it existed)"
}

main() {
  if [ "${1:-}" = "uninstall" ]; then shift; uninstall "$@"; return; fi

  local version="${TBOUND_VERSION:-$DEFAULT_VERSION}"
  local prefix base_url local_dist=""
  prefix="$(default_prefix)"
  base_url="${TBOUND_BASE_URL:-https://get.tbound.dev/releases}"
  local_dist="${TBOUND_LOCAL_DIST:-}"
  local add_path=1

  while [ $# -gt 0 ]; do
    case "$1" in
      --version)   version="${2:?}"; shift 2 ;;
      --prefix)    prefix="${2:?}"; shift 2 ;;
      --base-url)  base_url="${2:?}"; shift 2 ;;
      --local-dist) local_dist="${2:?}"; shift 2 ;;
      --no-path)   add_path=0; shift ;;
      *) die "unexpected argument: $1" ;;
    esac
  done

  local os arch artifact tmp
  os="$(detect_os)"; arch="$(detect_arch)"
  artifact="tbound-${version}-${os}-${arch}.tar.gz"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/tbound-install.XXXXXX")"
  TBOUND_INSTALL_TMP="$tmp"

  log "installing tbound ${version} (${os}/${arch}) into ${prefix}"

  if [ -n "$local_dist" ]; then
    [ -f "$local_dist/$artifact" ] || die "local dist missing $artifact"
    cp "$local_dist/$artifact" "$tmp/$artifact"
    [ -f "$local_dist/SHA256SUMS" ] && cp "$local_dist/SHA256SUMS" "$tmp/SHA256SUMS" || : > "$tmp/SHA256SUMS"
  else
    have curl || die "curl is required for remote install (or pass --local-dist)"
    log "downloading $base_url/$artifact"
    curl -fsSL "$base_url/$artifact" -o "$tmp/$artifact" || die "download failed"
    curl -fsSL "$base_url/SHA256SUMS" -o "$tmp/SHA256SUMS" || die "checksum download failed"
  fi

  [ -s "$tmp/SHA256SUMS" ] || die "no SHA256SUMS available; refusing to install unverified artifacts"
  local want got
  want="$(awk -v a="$artifact" '$2==a {print $1}' "$tmp/SHA256SUMS" | head -n1)"
  [ -n "$want" ] || die "no checksum entry for $artifact"
  got="$(sha256_file "$tmp/$artifact")"
  [ "$want" = "$got" ] || die "checksum mismatch for $artifact (want $want got $got)"
  log "checksum ok"

  mkdir -p "$prefix"
  chmod 700 "$prefix" 2>/dev/null || true
  tar -xzf "$tmp/$artifact" -C "$prefix"
  [ -x "$prefix/bin/tbound" ] || die "extracted archive has no bin/tbound"
  log "installed binaries to $prefix/bin"

  local cfg; cfg="$(config_dir)"
  mkdir -p "$cfg"
  if [ ! -f "$cfg/config.json" ]; then
    cat > "$cfg/config.json" <<JSON
{
  "version": "$version",
  "prefix": "$prefix",
  "node_min": "$NODE_MIN",
  "note": "governed serve --pi additionally needs a signed host profile and the Podman/crun containment stack"
}
JSON
    log "wrote $cfg/config.json"
  fi

  if [ "$add_path" = 1 ]; then
    log "add to PATH (then restart your shell):"
    log "  export PATH=\"$prefix/bin:\$PATH\""
  fi
  log "next:"
  log "  $prefix/bin/tbound-doctor        # check readiness"
  log "  $prefix/bin/tbound serve --pi --native-fixture   # non-claim-bearing smoke test"
  log "done"
}

main "$@"
