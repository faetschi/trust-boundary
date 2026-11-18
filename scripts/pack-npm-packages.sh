#!/usr/bin/env bash
# Pack the npm package trees from make-npm-packages.sh into the tarballs that get
# published. Run it on Linux: the Actions artifact upload and npm pack on Windows
# both drop the executable bit, and a published version can never be repacked.
#
# Usage: scripts/pack-npm-packages.sh <version> <npm-tree-dir> <out-dir>
set -euo pipefail

die() { echo "pack-npm-packages: $*" >&2; exit 1; }
[ "$#" -eq 3 ] || die "usage: pack-npm-packages.sh <version> <npm-tree-dir> <out-dir>"
VERSION="$1"
[ "$(uname -s)" = Linux ] || die "run on Linux; npm pack elsewhere drops the executable bit"
SRC="$(cd "$2" && pwd)"
mkdir -p "$3"
OUT="$(cd "$3" && pwd)"

# Publish order: the platform package first, then the root package that depends on it.
for pkg in cli-linux-x64 cli; do
  [ -f "$SRC/$pkg/package.json" ] || die "missing $SRC/$pkg/package.json"
  npm pack --ignore-scripts --silent --pack-destination "$OUT" "$SRC/$pkg" >/dev/null
done

platform="tbound-cli-linux-x64-$VERSION.tgz"
root="tbound-cli-$VERSION.tgz"
[ -f "$OUT/$platform" ] && [ -f "$OUT/$root" ] || die "npm pack did not write $platform and $root"
for b in tbound tbound-doctor tbound-profile; do
  tar -tvzf "$OUT/$platform" "package/bin/$b" | grep -q '^-rwxr-xr-x ' \
    || die "package/bin/$b is not mode 0755 in $platform"
done

(cd "$OUT" && sha256sum "$platform" "$root")
