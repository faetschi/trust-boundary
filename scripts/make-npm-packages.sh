#!/usr/bin/env bash
# Generate publishable npm packages from built release tarballs.
#
#   scripts/build-release.sh <ver> dist
#   scripts/make-npm-packages.sh <ver> [dist] [outdir]
#
# Produces, under outdir (default dist/npm):
#   cli/                 the root @tbound/cli package (launcher + optionalDependencies)
#   cli-<os>-<arch>/     one platform package per tarball, containing the binaries
#
# Each platform package ships bin/tbound, bin/tbound-doctor, and bin/tbound-profile
# (the Linux-only stub elsewhere). The linux/amd64 TCB entrypoint
# (libexec/tbound/tbound-cell-entrypoint) is deliberately NOT packaged for npm:
# it is an image/operator artifact and must never be launched by the npm/node
# launcher.
#
# Nothing is published here; this only assembles the package trees.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
DIST="${2:-$ROOT/dist}"
OUT="${3:-$DIST/npm}"

die() { echo "make-npm-packages: $*" >&2; exit 1; }

# A real path resolver is required: without it we cannot safely prove that the
# output directory is not an alias of DIST/HOME, so we refuse rather than fall
# back to lexical comparison that could delete an unrelated directory.
if command -v realpath >/dev/null 2>&1 && realpath -m -- / >/dev/null 2>&1; then
  canon() { realpath -m -- "$1"; }
elif command -v readlink >/dev/null 2>&1 && readlink -f -- / >/dev/null 2>&1; then
  canon() { readlink -f -- "$1"; }
else
  die "a path resolver (realpath or readlink -f) is required to guard the output directory"
fi

if ! printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
  die "version '$VERSION' is not a valid semver (required for npm packages)"
fi

DIST_C="$(canon "$DIST")"
OUT_C="$(canon "$OUT")"
ROOT_C="$(canon "$ROOT")"
HOME_C="$(canon "${HOME:-/nonexistent}")"
[ -d "$DIST_C" ] || die "dist dir $DIST_C does not exist (run scripts/build-release.sh first)"
[ -s "$DIST_C/SHA256SUMS" ] || die "dist SHA256SUMS is missing or empty"

# Guard the output directory: never delete an unrelated or aliased directory.
case "$OUT_C" in
  ""|"/"|"/usr"|"/etc"|"/var"|"$HOME_C"|"$ROOT_C"|"$DIST_C") die "refusing to write npm packages to '$OUT_C'" ;;
esac
if [ "${DIST_C#"$OUT_C"/}" != "$DIST_C" ]; then
  die "refusing OUT '$OUT_C' that contains DIST '$DIST_C'"
fi
if [ -e "$OUT_C" ]; then
  if [ -d "$OUT_C" ] && [ -f "$OUT_C/.tbound-npm" ]; then
    # Replace our own previous output via rename, never an in-place rm of an
    # arbitrary resolved path.
    old="$(dirname "$OUT_C")/.tbound-npm-old.$$.$RANDOM"
    mv "$OUT_C" "$old" || die "cannot move previous output aside: $OUT_C"
    rm -rf -- "$old"
  else
    die "refusing to remove existing non-tbound-npm path '$OUT_C' (remove it manually)"
  fi
fi
mkdir -p "$OUT_C"
: > "$OUT_C/.tbound-npm"
OUT="$OUT_C"
DIST="$DIST_C"

map_os() { case "$1" in linux) echo linux;; darwin) echo darwin;; windows) echo win32;; *) return 1;; esac; }
map_cpu() { case "$1" in amd64) echo x64;; arm64) echo arm64;; *) return 1;; esac; }

safe_archive() {
  local archive="$1" member
  while IFS= read -r member; do
    case "$member" in
      ""|/*|../*|*/../*|*/..|*\\*) die "unsafe path in archive $(basename "$archive"): $member" ;;
    esac
  done < <(tar -tzf "$archive")
  # Release packages are regular files/directories only. Reject symlink/hardlink
  # or special entries before extraction so package assembly never follows an
  # archive-provided link.
  tar -tvzf "$archive" | awk 'substr($0,1,1)!="-" && substr($0,1,1)!="d" { bad=1 } END { exit bad ? 1 : 0 }' \
    || die "links or special files are not allowed in $(basename "$archive")"
}

safe_manifest_paths() {
  local root="$1" line rel
  local seen="|"
  [ -f "$root/MANIFEST.sha256" ] || die "archive is missing MANIFEST.sha256"
  while IFS= read -r line; do
    [[ "$line" =~ ^[0-9a-f]{64}\ \ (.+)$ ]] || die "malformed MANIFEST.sha256 record"
    rel="${BASH_REMATCH[1]}"
    case "$rel" in
      ./*) rel="${rel#./}" ;;
    esac
    [[ "$rel" =~ ^[A-Za-z0-9._/-]+$ ]] || die "invalid manifest path '$rel'"
    case "$rel" in ""|/*|.|..|../*|*/../*|*/..) die "unsafe manifest path '$rel'" ;; esac
    case "$seen" in *"|$rel|"*) die "duplicate manifest path '$rel'" ;; esac
    seen+="$rel|"
    [ -f "$root/$rel" ] && [ ! -L "$root/$rel" ] || die "manifest path is missing, non-regular, or a symlink: $rel"
  done < "$root/MANIFEST.sha256"
  while IFS= read -r -d '' file; do
    rel="${file#"$root"/}"
    case "$seen" in *"|$rel|"*) ;; *) die "unlisted regular file in archive: $rel" ;; esac
  done < <(find "$root" -type f ! -name MANIFEST.sha256 -print0)
}

platforms=()
for tgz in "$DIST"/tbound-"$VERSION"-*.tar.gz; do
  [ -e "$tgz" ] || continue
  base="$(basename "$tgz")"; mid="${base#tbound-$VERSION-}"; mid="${mid%.tar.gz}"
  os="${mid%%-*}"; arch="${mid##*-}"
  [ "$os" != linux ] || [ "$arch" != arm64 ] || die "linux/arm64 is not a supported release target"
  # npm ships the Linux binary only. Other release tarballs stay installable by
  # hand, but no platform package is generated for them.
  if [ "$os" != linux ]; then
    echo "make-npm-packages: skipping non-linux tarball $base"
    continue
  fi
  nos="$(map_os "$os")" || die "unsupported os $os"
  narch="$(map_cpu "$arch")" || die "unsupported arch $arch"
  pkg="cli-$nos-$narch"
  want="$(awk -v a="$base" '$2==a || $2=="./"a {print $1; exit}' "$DIST/SHA256SUMS")"
  [ -n "$want" ] || die "SHA256SUMS has no entry for $base"
  if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$tgz" | awk '{print $1}')";
  elif command -v shasum >/dev/null 2>&1; then got="$(shasum -a 256 "$tgz" | awk '{print $1}')";
  else die "sha256sum or shasum is required to verify $base"; fi
  [ "$want" = "$got" ] || die "checksum mismatch for $base"
  work="$(mktemp -d "${TMPDIR:-/tmp}/tbound-npm.XXXXXX")"
  safe_archive "$tgz"
  tar -xzf "$tgz" -C "$work"
  safe_manifest_paths "$work"
  if command -v sha256sum >/dev/null 2>&1; then
    ( cd "$work" && sha256sum --quiet -c MANIFEST.sha256 ) || die "manifest verification failed for $base"
  elif command -v shasum >/dev/null 2>&1; then
    ( cd "$work" && shasum -a 256 -c MANIFEST.sha256 >/dev/null ) || die "manifest verification failed for $base"
  else
    die "sha256sum or shasum is required to verify $base"
  fi
  bindir="$OUT/$pkg/bin"
  mkdir -p "$bindir"
  binfile="tbound"; [ "$os" = "windows" ] && binfile="tbound.exe"
  exefile="tbound-doctor"; [ "$os" = "windows" ] && exefile="tbound-doctor.exe"
  proffile="tbound-profile"; [ "$os" = "windows" ] && proffile="tbound-profile.exe"
  src_bin="$work/bin/$binfile"; src_exe="$work/bin/$exefile"; src_prof="$work/bin/$proffile"
  [ -f "$src_bin" ] || die "archive $base missing bin/$binfile"
  [ -f "$src_exe" ] || die "archive $base missing bin/$exefile"
  [ -f "$src_prof" ] || die "archive $base missing bin/$proffile"
  cp "$src_bin" "$bindir/$binfile"
  cp "$src_exe" "$bindir/$exefile"
  cp "$src_prof" "$bindir/$proffile"
  cp "$ROOT/LICENSE" "$OUT/$pkg/LICENSE"
  rm -rf "$work"
  cat > "$OUT/$pkg/package.json" <<JSON
{
  "name": "@tbound/cli-${nos}-${narch}",
  "version": "$VERSION",
  "description": "tbound platform binary for ${nos}-${narch}",
  "license": "MIT",
  "os": ["${nos}"],
  "cpu": ["${narch}"],
  "files": ["bin/", "LICENSE"],
  "preferUnplugged": true
}
JSON
  echo "make-npm-packages: generated $pkg"
  platforms+=("cli-$nos-$narch")
done

[ "${#platforms[@]}" -gt 0 ] || die "no tarballs found in $DIST for version $VERSION"

# Root package: launcher + optionalDependencies pointing at the platform packages.
mkdir -p "$OUT/cli/bin" "$OUT/cli/scripts"
cp "$ROOT/packaging/npm/bin/tbound.js" "$OUT/cli/bin/"
cp "$ROOT/packaging/npm/scripts/postinstall.js" "$OUT/cli/scripts/"
cp "$ROOT/packaging/npm/README.md" "$OUT/cli/README.md"
cp "$ROOT/LICENSE" "$OUT/cli/LICENSE"

depsfile="$OUT/cli/.deps.json"
: > "$depsfile"
n="${#platforms[@]}"; i=0
for p in "${platforms[@]}"; do
  i=$((i+1)); comma=","; [ "$i" = "$n" ] && comma=""
  printf '    "@tbound/%s": "%s"%s\n' "$p" "$VERSION" "$comma" >> "$depsfile"
done

{
  printf '{\n'
  printf '  "name": "@tbound/cli",\n'
  printf '  "version": "%s",\n' "$VERSION"
  printf '  "description": "Install the tbound supervisor CLI next to your Pi coding agent.",\n'
  printf '  "license": "MIT",\n'
  printf '  "bin": { "tbound": "bin/tbound.js" },\n'
  printf '  "files": ["bin/", "scripts/", "README.md", "LICENSE"],\n'
  printf '  "optionalDependencies": {\n'
  cat "$depsfile"
  printf '  },\n'
  printf '  "scripts": { "postinstall": "node scripts/postinstall.js" },\n'
  printf '  "engines": { "node": ">=22.19.0" }\n'
  printf '}\n'
} > "$OUT/cli/package.json"
rm -f "$depsfile"

echo "make-npm-packages: wrote $OUT (cli + ${#platforms[@]} platform packages)"
