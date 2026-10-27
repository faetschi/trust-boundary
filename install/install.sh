#!/usr/bin/env bash
# tbound installer.
#
#   curl -fsSL https://get.tbound.dev | bash
#   bash install.sh --version <ver> [--prefix <dir>] [--base-url <url> | --local-dist <dir>] [--no-path] [--no-completions]
#                   [--public-key <file> [--key-fingerprint <hex16>] [--verifier <tbound>]]
#   bash install.sh update  [--version <ver> ...]
#   bash install.sh uninstall [--prefix <dir>]
#
# Installs the tbound and tbound-doctor binaries plus the runtime bundle into a
# user-owned prefix. It never uses sudo and never touches the user's global Node
# or Pi install. Artifacts are verified before extraction (see TRUST below), the prefix is
# marker-owned so uninstall cannot delete unrelated data, and updates are staged
# before being swapped into place.
#
# TRUST. Without --public-key the installer only compares the artifact with SHA256SUMS from the
# same origin: that detects corruption, not a replaced download, and is not publisher
# authentication. With --public-key FILE it requires tbound-release-manifest.json and
# tbound-release-manifest.json.sig next to the artifacts and runs `tbound release verify`
# (signature, then the artifact's size and SHA-256) BEFORE extracting anything; a checksum file is
# then not used at all. The verifier is --verifier PATH, $TBOUND_VERIFIER, or `tbound` on PATH. It
# must come from somewhere you trust independently of the download (a build from source, or an
# earlier verified install). A verifier taken from the archive being checked proves nothing.
# FIRST-INSTALL LIMIT: the first time, nothing proves the public key is the publisher's. Compare its
# fingerprint (`tbound release fingerprint --public-key FILE`, printed on success) with the value in
# the README and release notes, obtained separately, or pass it as --key-fingerprint HEX16.
# KEY PIN: the first signed install stores the public key it verified in the prefix
# (.tbound-publisher.pub, 0644). Every later install or update into that prefix is verified against the
# stored key and is refused without a valid signature: it never falls back to SHA256SUMS, and a
# different --public-key is refused (uninstall first to trust another publisher). A prefix installed
# without a key keeps the checksum-only behaviour and says so; its first signed update pins the key.
set -euo pipefail

DEFAULT_VERSION="latest"
NODE_MIN="22.19"
MARKER=".tbound-install"
MANIFEST_NAME="tbound-release-manifest.json"
PUBLISHER_KEY=".tbound-publisher.pub"

log()  { printf 'tbound-install: %s\n' "$*"; }
die()  { printf 'tbound-install: error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
  local self="${BASH_SOURCE[0]:-}"
  if [ -r "$self" ]; then
    sed -n '2,/^set -euo pipefail/{/^set -euo/d;s/^# \{0,1\}//;p;}' "$self"
  else
    printf 'see the header of install.sh (download it first to read the TRUST notes)\n'
  fi
}

# verify_signature runs the verifier on the signed manifest and the artifact. It dies before anything
# is extracted. A checksum file is not an input.
verify_signature() {
  local dir="$1" artifact="$2" key="$3" fingerprint="$4" verifier="$5"
  [ -f "$key" ] || die "public key file not found: $key"
  [ -n "$verifier" ] || verifier="$(command -v tbound || true)"
  { [ -n "$verifier" ] && [ -x "$verifier" ]; } || die "no verifier: pass --verifier PATH to a tbound binary you trust independently of this download"
  local args=(release verify --manifest "$dir/$MANIFEST_NAME" --signature "$dir/$MANIFEST_NAME.sig" --public-key "$key" --artifact "$dir/$artifact")
  [ -z "$fingerprint" ] || args+=(--fingerprint "$fingerprint")
  "$verifier" "${args[@]}" || die "release signature verification failed; nothing was extracted"
}

default_prefix() {
  if [ -n "${TBOUND_PREFIX:-}" ]; then printf '%s' "$TBOUND_PREFIX"; return; fi
  if [ -n "${XDG_DATA_HOME:-}" ]; then printf '%s/tbound' "$XDG_DATA_HOME"; return; fi
  printf '%s/.local/share/tbound' "$HOME"
}
config_dir() {
  if [ -n "${XDG_CONFIG_HOME:-}" ]; then printf '%s/tbound' "$XDG_CONFIG_HOME"; return; fi
  printf '%s/.config/tbound' "$HOME"
}

# A real path resolver is required for any prefix operation: without one we
# cannot prove the prefix is not a symlink alias of HOME or a system directory,
# so we refuse rather than fall back to lexical comparison.
require_resolver() {
  if command -v realpath >/dev/null 2>&1 && realpath -m -- / >/dev/null 2>&1; then return 0; fi
  if command -v readlink >/dev/null 2>&1 && readlink -f -- / >/dev/null 2>&1; then return 0; fi
  return 1
}

# canon_path prints an absolute, symlink/. / .. resolved path.
canon_path() {
  local p="$1"
  [ -n "$p" ] || return 1
  case "$p" in
    /*) ;;
    *) p="$PWD/$p" ;;
  esac
  if command -v realpath >/dev/null 2>&1 && realpath -m -- / >/dev/null 2>&1; then
    realpath -m -- "$p"
  else
    readlink -f -- "$p"
  fi
}

# canonical_prefix prints a resolved, safe prefix path or exits on a protected or
# shared target (including symlink aliases of HOME and system directories).
canonical_prefix() {
  require_resolver || die "a path resolver (realpath or readlink -f) is required to operate on a prefix safely"
  local p
  p="$(canon_path "$1")" || die "empty prefix"
  [ -n "$p" ] || die "empty prefix"
  case "$p" in
    "/"|"/usr"|"/etc"|"/bin"|"/sbin"|"/var"|"/home"|"/opt"|"/root") die "refusing protected prefix: $p" ;;
  esac
  local home
  home="$(canon_path "$HOME")" || home="${HOME%/}"
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
  # install a verified artifact; requires $1 artifact path, $2 sums file (empty when the signed
  # manifest was already verified), $3 version, $4 prefix, $5 the verified public key to pin (or empty)
  local artifact="$1" sums="$2" version="$3" prefix="$4" pin_key="${5:-}"
  local want got
  if [ -n "$sums" ]; then
    want="$(awk -v a="$(basename "$artifact")" '$2==a || $2=="./"a {print $1}' "$sums" | head -n1)"
    [ -n "$want" ] || die "no checksum entry for $(basename "$artifact")"
    got="$(sha256_file "$artifact")"
    [ "$want" = "$got" ] || die "checksum mismatch for $(basename "$artifact") (want $want got $got)"
    log "checksum ok (integrity only: not publisher authentication; use --public-key for a signed install)"
  fi

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
  if [ ! -x "$stage/bin/tbound-doctor" ]; then die "extracted archive has no bin/tbound-doctor"; fi
  if [ ! -x "$stage/bin/tbound-profile" ]; then die "extracted archive has no bin/tbound-profile"; fi
  printf 'name=tbound\nversion=%s\nprefix=%s\n' "$(json_escape "$version")" "$(json_escape "$prefix")" > "$stage/$MARKER"
  rm -rf -- "$stage/$PUBLISHER_KEY" # the pin comes only from the verified key below, never from the archive
  if [ -n "$pin_key" ]; then
    cp -- "$pin_key" "$stage/$PUBLISHER_KEY" && chmod 0644 "$stage/$PUBLISHER_KEY" || die "cannot pin the publisher key"
  fi

  # Move any existing install into an exclusively created hold directory so a
  # concurrent or pre-existing path can never be nested or deleted.
  local hold=""
  if [ -d "$prefix" ]; then
    hold="$(mktemp -d "$parent/.tbound-hold.XXXXXX")" || die "cannot allocate hold directory next to $parent"
    STAGE_HOLD="$hold"
    if ! mv "$prefix" "$hold/old"; then die "failed to move existing prefix aside"; fi
  fi
  if ! mv "$stage" "$prefix"; then
    if [ -n "$hold" ] && [ -d "$hold/old" ]; then
      if mv "$hold/old" "$prefix"; then
        rm -rf -- "$hold"; STAGE_HOLD=""
        die "failed to move staged install into place; the previous installation was restored"
      fi
      # Restoration failed: keep the hold directory and tell the operator how to
      # recover, rather than letting the EXIT trap delete the previous install.
      STAGE_HOLD=""
      die "failed to move staged install into place AND failed to restore; the previous installation is preserved at $hold/old (move it back to $prefix manually)"
    fi
    die "failed to move staged install into place"
  fi
  STAGE_INSTALL=""
  [ -n "$hold" ] && rm -rf -- "$hold"
  STAGE_HOLD=""
  chmod 700 "$prefix" 2>/dev/null || true
  log "installed binaries to $prefix/bin"
}

write_config() {
  local version="$1" prefix="$2"
  local cfg; cfg="$(config_dir)"
  mkdir -p "$cfg"
  if [ ! -f "$cfg/config.json" ]; then
    printf '{\n  "version": "%s",\n  "prefix": "%s",\n  "node_min": "%s",\n  "note": "governed serve --pi additionally needs runtime composition, a signed host profile, and the Podman/crun containment stack"\n}\n' \
      "$(json_escape "$version")" "$(json_escape "$prefix")" "$(json_escape "$NODE_MIN")" > "$cfg/config.json"
    log "wrote $cfg/config.json"
  fi
}

STAGE=""; STAGE_INSTALL=""; STAGE_HOLD=""
cleanup() {
  [ -n "${STAGE:-}" ] && rm -rf -- "$STAGE" || true
  [ -n "${STAGE_INSTALL:-}" ] && rm -rf -- "$STAGE_INSTALL" || true
  # Never delete a hold directory that still contains a moved-aside previous
  # installation; preserve it and tell the operator how to restore.
  if [ -n "${STAGE_HOLD:-}" ]; then
    if [ -e "$STAGE_HOLD/old" ]; then
      printf 'tbound-install: preserving previous installation at %s (move it back manually)\n' "$STAGE_HOLD/old" >&2
    else
      rm -rf -- "$STAGE_HOLD" || true
    fi
  fi
}
trap cleanup EXIT

main() {
  case "${1:-}" in
    -h|--help) usage; return ;;
    uninstall) shift; uninstall "$@"; return ;;
    update)    shift; set -- update "$@" ;;
  esac
  local updating=0
  if [ "${1:-}" = "update" ]; then updating=1; shift; fi

  local version="${TBOUND_VERSION:-$DEFAULT_VERSION}"
  local prefix base_url local_dist="" add_path=1 no_completions=0
  local public_key="" key_fingerprint="" verifier="${TBOUND_VERIFIER:-}"
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
      --public-key) public_key="${2:?}"; shift 2 ;;
      --key-fingerprint) key_fingerprint="${2:?}"; shift 2 ;;
      --verifier)   verifier="${2:?}"; shift 2 ;;
      -h|--help)    usage; return ;;
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

  # Key pin: work from a private copy of the key, so the key that is verified is the key that is stored.
  local pinned="$prefix/$PUBLISHER_KEY"
  if [ -n "$public_key" ]; then
    [ -f "$public_key" ] || die "public key file not found: $public_key"
    cp -- "$public_key" "$STAGE/$PUBLISHER_KEY" || die "cannot read the public key: $public_key"
    public_key="$STAGE/$PUBLISHER_KEY"
  fi
  if is_owned_prefix "$prefix"; then
    if [ -f "$pinned" ]; then
      if [ -n "$public_key" ]; then
        cmp -s -- "$public_key" "$pinned" || die "--public-key differs from the pinned publisher key of $prefix; uninstall first to trust another publisher"
      else
        cp -- "$pinned" "$STAGE/$PUBLISHER_KEY" || die "cannot read the pinned publisher key $pinned"
        public_key="$STAGE/$PUBLISHER_KEY"
      fi
      log "this prefix pins a publisher key: the release must carry a valid signature by the pinned publisher key (no checksum fallback)"
    elif [ -z "$public_key" ]; then
      log "warning: this prefix was installed without a publisher key, so this install is verified by the same-origin checksum only (not publisher authentication); pass --public-key to pin one"
    fi
  fi

  if [ -n "$local_dist" ]; then
    [ -f "$local_dist/$artifact" ] || die "local dist missing $artifact"
    cp "$local_dist/$artifact" "$STAGE/$artifact"
    if [ -n "$public_key" ]; then
      local f
      for f in "$MANIFEST_NAME" "$MANIFEST_NAME.sig"; do
        [ -f "$local_dist/$f" ] || die "local dist missing $f (required with --public-key)"
        cp "$local_dist/$f" "$STAGE/$f"
      done
    elif [ -f "$local_dist/SHA256SUMS" ]; then cp "$local_dist/SHA256SUMS" "$STAGE/SHA256SUMS"; else : > "$STAGE/SHA256SUMS"; fi
  else
    have curl || die "curl is required for remote install (or pass --local-dist)"
    log "downloading $base_url/$artifact"
    curl -fsSL "$base_url/$artifact" -o "$STAGE/$artifact" || die "download failed"
    if [ -n "$public_key" ]; then
      curl -fsSL "$base_url/$MANIFEST_NAME" -o "$STAGE/$MANIFEST_NAME" || die "manifest download failed"
      curl -fsSL "$base_url/$MANIFEST_NAME.sig" -o "$STAGE/$MANIFEST_NAME.sig" || die "manifest signature download failed"
    else
      curl -fsSL "$base_url/SHA256SUMS" -o "$STAGE/SHA256SUMS" || die "checksum download failed"
    fi
  fi
  local sums=""
  if [ -n "$public_key" ]; then
    verify_signature "$STAGE" "$artifact" "$public_key" "$key_fingerprint" "$verifier"
  else
    [ -s "$STAGE/SHA256SUMS" ] || die "no SHA256SUMS available; refusing to install unverified artifacts"
    log "warning: no --public-key given; the checksum file only detects corruption and is not publisher authentication"
    sums="$STAGE/SHA256SUMS"
  fi

  install_at "$STAGE/$artifact" "$sums" "$version" "$prefix" "$public_key"
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

# Run only when executed (or piped to bash), so a test can source the functions.
if [ "${BASH_SOURCE[0]:-$0}" = "$0" ]; then main "$@"; fi
