#!/usr/bin/env bash
# Generate publishable npm packages from built release tarballs.
#
#   scripts/build-release.sh <ver> dist
#   scripts/make-npm-packages.sh <ver> [dist] [outdir]
#
# Produces, under outdir (default dist/npm):
#   cli/                 the root @tbound/cli package (launcher + optionalDependencies)
#   cli-linux-x64/       the Linux platform package, containing the binaries
# Non-Linux release tarballs are skipped; npm ships the Linux amd64 binary only.
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
# shellcheck source=scripts/lib-release-verify.sh
source "$ROOT/scripts/lib-release-verify.sh"

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
  # Only linux/amd64 reaches here: other OSes are skipped and linux/arm64 dies above.
  [ "$arch" = amd64 ] || die "unsupported arch $arch"
  nos=linux; narch=x64
  pkg="cli-$nos-$narch"
  verify_archive_checksum "$tgz" "$DIST/SHA256SUMS"
  work="$(mktemp -d "${TMPDIR:-/tmp}/tbound-npm.XXXXXX")"
  verify_archive_members "$tgz"
  tar -xzf "$tgz" -C "$work"
  verify_manifest_tree "$work"
  bindir="$OUT/$pkg/bin"
  mkdir -p "$bindir"
  for b in tbound tbound-doctor tbound-profile; do
    [ -f "$work/bin/$b" ] || die "archive $base missing bin/$b"
    cp "$work/bin/$b" "$bindir/$b"
    chmod 0755 "$bindir/$b"
  done
  cp "$ROOT/LICENSE" "$OUT/$pkg/LICENSE"
  rm -rf "$work"
  cat > "$OUT/$pkg/package.json" <<JSON
{
  "name": "@tbound/cli-${nos}-${narch}",
  "version": "$VERSION",
  "description": "tbound platform binary for ${nos}-${narch}",
  "license": "MIT",
  "repository": { "type": "git", "url": "git+https://github.com/faetschi/trust-boundary.git" },
  "homepage": "https://github.com/faetschi/trust-boundary#readme",
  "bugs": { "url": "https://github.com/faetschi/trust-boundary/issues" },
  "os": ["${nos}"],
  "cpu": ["${narch}"],
  "files": ["bin/", "LICENSE"],
  "preferUnplugged": true
}
JSON
  echo "make-npm-packages: generated $pkg"
  platforms+=("cli-$nos-$narch")
done

[ "${#platforms[@]}" -gt 0 ] || die "no linux/amd64 tarball in $DIST for version $VERSION (non-Linux tarballs are skipped)"

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
  printf '  "repository": { "type": "git", "url": "git+https://github.com/faetschi/trust-boundary.git" },\n'
  printf '  "homepage": "https://github.com/faetschi/trust-boundary#readme",\n'
  printf '  "bugs": { "url": "https://github.com/faetschi/trust-boundary/issues" },\n'
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
