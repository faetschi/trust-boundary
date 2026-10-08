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
# Nothing is published here; this only assembles the package trees.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
DIST="${2:-$ROOT/dist}"
OUT="${3:-$DIST/npm}"

die() { echo "make-npm-packages: $*" >&2; exit 1; }
case "$VERSION" in *[!A-Za-z0-9._-]*|"") die "invalid version '$VERSION'";; esac
[ -d "$DIST" ] || die "dist dir $DIST does not exist (run scripts/build-release.sh first)"

rm -rf "$OUT"
mkdir -p "$OUT"

map_os() { case "$1" in linux) echo linux;; darwin) echo darwin;; windows) echo win32;; *) return 1;; esac; }
map_cpu() { case "$1" in amd64) echo x64;; arm64) echo arm64;; *) return 1;; esac; }

platforms=()
for tgz in "$DIST"/tbound-"$VERSION"-*.tar.gz; do
  [ -e "$tgz" ] || continue
  base="$(basename "$tgz")"; mid="${base#tbound-$VERSION-}"; mid="${mid%.tar.gz}"
  os="${mid%%-*}"; arch="${mid##*-}"
  nos="$(map_os "$os")" || die "unsupported os $os"
  narch="$(map_cpu "$arch")" || die "unsupported arch $arch"
  pkg="cli-$nos-$narch"
  work="$(mktemp -d "${TMPDIR:-/tmp}/tbound-npm.XXXXXX")"
  tar -xzf "$tgz" -C "$work"
  bindir="$OUT/$pkg/bin"
  mkdir -p "$bindir"
  cp "$work/bin/tbound" "$bindir/tbound"
  [ -f "$work/bin/tbound-doctor" ] && cp "$work/bin/tbound-doctor" "$bindir/tbound-doctor"
  binfile="tbound"; [ "$os" = "windows" ] && binfile="tbound.exe"
  exefile="tbound-doctor"; [ "$os" = "windows" ] && exefile="tbound-doctor.exe"
  [ -f "$work/bin/$binfile" ] && mv "$bindir/tbound" "$bindir/$binfile" 2>/dev/null || true
  [ -f "$work/bin/$exefile" ] && mv "$bindir/tbound-doctor" "$bindir/$exefile" 2>/dev/null || true
  rm -rf "$work"
  cat > "$OUT/$pkg/package.json" <<JSON
{
  "name": "@tbound/cli-${nos}-${narch}",
  "version": "$VERSION",
  "description": "tbound platform binary for ${nos}-${narch}",
  "license": "MIT",
  "os": ["${nos}"],
  "cpu": ["${narch}"],
  "files": ["bin/"],
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
  printf '  "files": ["bin/", "scripts/", "README.md"],\n'
  printf '  "optionalDependencies": {\n'
  cat "$depsfile"
  printf '  },\n'
  printf '  "scripts": { "postinstall": "node scripts/postinstall.js" },\n'
  printf '  "engines": { "node": ">=22.19.0" }\n'
  printf '}\n'
} > "$OUT/cli/package.json"
rm -f "$depsfile"

echo "make-npm-packages: wrote $OUT (cli + ${#platforms[@]} platform packages)"
