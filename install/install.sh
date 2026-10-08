#!/usr/bin/env bash
# tbound installer.
#
#   curl -fsSL https://get.tbound.dev | bash
#   bash install.sh --version <ver> [--prefix <dir>] [--base-url <url> | --local-dist <dir>] [--no-path] [--no-completions]
#   bash install.sh update  [--version <ver> ...]
#   bash install.sh uninstall [--prefix <dir>]
#
# Installs the tbound and tbound-doctor binaries plus the runtime bundle into a
# user-owned prefix. It never uses sudo and never touches the user's global Node
# or Pi install. Artifacts are checksum-verified before extraction, the prefix is
# marker-owned so uninstall cannot delete unrelated data, and updates are staged
# before being swapped into place.
set -euo pipefail

DEFAULT_VERSION="latest"
NODE_MIN="22.19"
MARKER=".tbound-install"

log()  { printf 'tbound-install: %s\n' "$*"; }
die()  { printf 'tbound-install: error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

default_prefix() {
  if [ -n "${TBOUND_PREFIX:-}" ]; then printf '%s' "$TBOUND_PREFIX"; return; fi
  if [ -n "${XDG_DATA_HOME:-}" ]; then printf '%s/tbound' "$XDG_DATA_HOME"; return; fi
  printf '%s/.local/share/tbound' "$HOME"
}
config_dir() {
  if [ -n "${XDG_CONFIG_HOME:-}" ]; then printf '%s/tbound' "$XDG_CONFIG_HOME"; return; fi
  printf '%s/.config/tbound' "$HOME"
}

# canon_path prints an absolute path with symlinks and . / .. resolved when the
# platform tools allow it, falling back to lexical normalization.
canon_path() {
  local p="$1"
  [ -n "$p" ] || return 1
  case "$p" in
    /*) ;;
    *) p="$PWD/$p" ;;
  esac
  if command -v realpath >/dev/null 2>&1; then
    local rp
    if rp="$(realpath -m -- "$p" 2>/dev/null)" && [ -n "$rp" ]; then printf '%s' "$rp"; return 0; fi
  fi
  if command -v readlink >/dev/null 2>&1; then
    local rl
    if rl="$(readlink -f -- "$p" 2>/dev/null)" && [ -n "$rl" ]; then printf '%s' "$rl"; return 0; fi
  fi
  local part out="" IFS='/'
  for part in $p; do
    case "$part" in
      ""|.) ;;
      ..) out="${out%/*}" ;;
      *) out="$out/$part" ;;
    esac
  done
  printf '%s' "${out:-/}"
}

# canonical_prefix prints a resolved, safe prefix path or exits on a protected or
# shared target (including symlink aliases of HOME and system directories).
canonical_prefix() {
  local p
  p="$(canon_path "$1")" || die "empty prefix"
  case "$p" in
    "/"|"/usr"|"/etc"|"/bin"|"/sbin"|"/var"|"/home"|"/opt"|"/root") die "refusing protected prefix: $p" ;;
  esac
  local home; home="$(canon_path "$HOME" 2>/dev/null || printf '%s' "${HOME%/}")"
  if [ -n "$home" ]; then
    [ "$p" = "$home" ] && die "refusing prefix equal to HOME: $p"
    if [ "${home#"$p"/}" != "$home" ]; then
      die "refusing prefix that contains HOME: $p"
    fi
  fi
  printf '%s' "$p"
}

is_owned_prefix() { [ -f "$1/$MARKER" ]; }

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

json_escape() {
  printf '%s' "$1" \
    | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/\t/\\t/g' -e 's/\r/\\r/g' -e 's/\n/\\n/g' \
    | tr -d '\000-\010\013\014\016-\037'
}

install_completions() {
  local prefix="$1"
  local src="$prefix/share/completions"
  [ -d "$src" ] || return 0
  local data="${XDG_DATA_HOME:-$HOME/.local/share}"
  local cfg="${XDG_CONFIG_HOME:-$HOME/.config}"
  if [ -f "$src/tbound.bash" ]; then
    mkdir -p "$data/bash-completion/completions"
    cp -f "$src/tbound.bash" "$data/bash-completion/completions/tbound"
  fi
  if [ -f "$src/_tbound" ]; then
    mkdir -p "$data/zsh/site-functions"
    cp -f "$src/_tbound" "$data/zsh/site-functions/_tbound"
  fi
  if [ -f "$src/tbound.fish" ]; then
    mkdir -p "$cfg/fish/completions"
    cp -f "$src/tbound.fish" "$cfg/fish/completions/tbound.fish"
  fi
  log "installed shell completions (bash/zsh/fish); restart your shell to activate"
}

remove_completions() {
  local data="${XDG_DATA_HOME:-$HOME/.local/share}"
  local cfg="${XDG_CONFIG_HOME:-$HOME/.config}"
  rm -f "$data/bash-completion/completions/tbound" "$data/zsh/site-functions/_tbound" "$cfg/fish/completions/tbound.fish"
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
  prefix="$(canonical_prefix "$prefix")"
  if [ ! -d "$prefix" ]; then log "nothing to remove at $prefix"; return; fi
  if ! is_owned_prefix "$prefix"; then
    die "refusing to remove '$prefix': not a tbound install (no $MARKER marker). Remove it manually if you are sure."
  fi
  log "removing owned tbound install at $prefix"
  rm -rf -- "$prefix"
  remove_completions
  log "done"
}

install_at() {
  # install a verified artifact; requires $1 artifact path, $2 verified sums file, $3 version, $4 prefix
  local artifact="$1" sums="$2" version="$3" prefix="$4"
  local want got
  want="$(awk -v a="$(basename "$artifact")" '$2==a || $2=="./"a {print $1}' "$sums" | head -n1)"
  [ -n "$want" ] || die "no checksum entry for $(basename "$artifact")"
  got="$(sha256_file "$artifact")"
  [ "$want" = "$got" ] || die "checksum mismatch for $(basename "$artifact") (want $want got $got)"
  log "checksum ok"

  if [ -e "$prefix" ] && [ -n "$(ls -A "$prefix" 2>/dev/null || true)" ] && ! is_owned_prefix "$prefix"; then
    die "refusing to install into non-empty, non-tbound prefix $prefix (remove it or choose another --prefix)"
  fi

  local parent; parent="$(dirname "$prefix")"
  mkdir -p "$parent"
  # Stage next to the prefix so the final swap is a same-filesystem rename.
  local stage; stage="$(mktemp -d "$parent/.tbound-stage.XXXXXX")" || die "cannot stage next to $parent"
  STAGE_INSTALL="$stage"
  if ! tar -xzf "$artifact" -C "$stage"; then die "extraction failed"; fi
  if [ ! -x "$stage/bin/tbound" ]; then die "extracted archive has no bin/tbound"; fi
  printf 'name=tbound\nversion=%s\nprefix=%s\n' "$(json_escape "$version")" "$(json_escape "$prefix")" > "$stage/$MARKER"

  # Move any existing install into an exclusively created hold directory so a
  # concurrent or pre-existing path can never be nested or deleted.
  local hold=""
  if [ -d "$prefix" ]; then
    hold="$(mktemp -d "$parent/.tbound-hold.XXXXXX")" || die "cannot allocate hold directory next to $parent"
    if ! mv "$prefix" "$hold/old"; then rm -rf -- "$hold"; die "failed to move existing prefix aside"; fi
  fi
  if ! mv "$stage" "$prefix"; then
    if [ -n "$hold" ] && [ -d "$hold/old" ]; then mv "$hold/old" "$prefix"; fi
    die "failed to move staged install into place"
  fi
  STAGE_INSTALL=""
  [ -n "$hold" ] && rm -rf -- "$hold"
  chmod 700 "$prefix" 2>/dev/null || true
  log "installed binaries to $prefix/bin"
}

write_config() {
  local version="$1" prefix="$2"
  local cfg; cfg="$(config_dir)"
  mkdir -p "$cfg"
  if [ ! -f "$cfg/config.json" ]; then
    printf '{\n  "version": "%s",\n  "prefix": "%s",\n  "node_min": "%s",\n  "note": "governed serve --pi additionally needs a signed host profile and the Podman/crun containment stack"\n}\n' \
      "$(json_escape "$version")" "$(json_escape "$prefix")" "$(json_escape "$NODE_MIN")" > "$cfg/config.json"
    log "wrote $cfg/config.json"
  fi
}

STAGE=""; STAGE_INSTALL=""
cleanup() {
  [ -n "${STAGE:-}" ] && rm -rf -- "$STAGE" || true
  [ -n "${STAGE_INSTALL:-}" ] && rm -rf -- "$STAGE_INSTALL" || true
}
trap cleanup EXIT

main() {
  case "${1:-}" in
    uninstall) shift; uninstall "$@"; return ;;
    update)    shift; set -- update "$@" ;;
  esac
  local updating=0
  if [ "${1:-}" = "update" ]; then updating=1; shift; fi

  local version="${TBOUND_VERSION:-$DEFAULT_VERSION}"
  local prefix base_url local_dist="" add_path=1 no_completions=0
  prefix="$(default_prefix)"
  base_url="${TBOUND_BASE_URL:-https://get.tbound.dev/releases}"
  local_dist="${TBOUND_LOCAL_DIST:-}"

  while [ $# -gt 0 ]; do
    case "$1" in
      --version)    version="${2:?}"; shift 2 ;;
      --prefix)     prefix="${2:?}"; shift 2 ;;
      --base-url)   base_url="${2:?}"; shift 2 ;;
      --local-dist) local_dist="${2:?}"; shift 2 ;;
      --no-path)    add_path=0; shift ;;
      --no-completions) no_completions=1; shift ;;
      *) die "unexpected argument: $1" ;;
    esac
  done

  prefix="$(canonical_prefix "$prefix")"
  if [ "$updating" = 1 ] && ! is_owned_prefix "$prefix"; then
    die "tbound is not installed at $prefix; run install first"
  fi

  local os arch artifact
  os="$(detect_os)"; arch="$(detect_arch)"
  artifact="tbound-${version}-${os}-${arch}.tar.gz"

  STAGE="$(mktemp -d "${TMPDIR:-/tmp}/tbound-install.XXXXXX")"
  log "${updating:+updating }installing tbound ${version} (${os}/${arch}) into ${prefix}"

  if [ -n "$local_dist" ]; then
    [ -f "$local_dist/$artifact" ] || die "local dist missing $artifact"
    cp "$local_dist/$artifact" "$STAGE/$artifact"
    if [ -f "$local_dist/SHA256SUMS" ]; then cp "$local_dist/SHA256SUMS" "$STAGE/SHA256SUMS"; else : > "$STAGE/SHA256SUMS"; fi
  else
    have curl || die "curl is required for remote install (or pass --local-dist)"
    log "downloading $base_url/$artifact"
    curl -fsSL "$base_url/$artifact" -o "$STAGE/$artifact" || die "download failed"
    curl -fsSL "$base_url/SHA256SUMS" -o "$STAGE/SHA256SUMS" || die "checksum download failed"
  fi
  [ -s "$STAGE/SHA256SUMS" ] || die "no SHA256SUMS available; refusing to install unverified artifacts"

  install_at "$STAGE/$artifact" "$STAGE/SHA256SUMS" "$version" "$prefix"
  write_config "$version" "$prefix"

  [ "$no_completions" = 0 ] && install_completions "$prefix" || true

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
