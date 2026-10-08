#!/usr/bin/env bash
# Build a Debian package from a built linux-amd64 release tarball.
#
#   scripts/build-release.sh <ver> dist linux/amd64
#   scripts/build-deb.sh <ver> [dist]
#
# Requires dpkg-deb (Debian/Ubuntu). Installs binaries to /usr/bin and the
# runtime bundle to /usr/lib/tbound/runtime. No state changes are made on the
# build host beyond writing into the dist directory.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
DIST="${2:-$ROOT/dist}"
ARCH="${TBOUND_DEB_ARCH:-amd64}"
TGZ="$DIST/tbound-$VERSION-linux-$ARCH.tar.gz"

command -v dpkg-deb >/dev/null 2>&1 || { echo "build-deb: dpkg-deb is required" >&2; exit 1; }
[ -f "$TGZ" ] || { echo "build-deb: missing $TGZ (run scripts/build-release.sh first)" >&2; exit 1; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-deb.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
tar -xzf "$TGZ" -C "$WORK"

pkg="$WORK/pkg"
mkdir -p "$pkg/DEBIAN" "$pkg/usr/bin" "$pkg/usr/lib/tbound"
cp "$WORK/bin/tbound" "$WORK/bin/tbound-doctor" "$pkg/usr/bin/"
cp -R "$WORK/runtime" "$pkg/usr/lib/tbound/runtime"
chmod 755 "$pkg/usr/bin/tbound" "$pkg/usr/bin/tbound-doctor"

cat > "$pkg/DEBIAN/control" <<CTRL
Package: tbound
Version: $VERSION
Section: utils
Priority: optional
Architecture: $ARCH
Maintainer: tbound <noreply@tbound.dev>
Description: Host-side supervisor for the Pi coding agent
 Installs the tbound supervisor CLI and the tbound-doctor preflight tool.
 Governed runs additionally require a signed host profile and the Podman/crun
 containment stack.
CTRL

out="$DIST/tbound_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "$pkg" "$out"
echo "build-deb: wrote $out"
