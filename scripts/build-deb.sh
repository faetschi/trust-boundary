#!/usr/bin/env bash
# Build a Debian package from a built linux-amd64 release tarball.
#
#   scripts/build-release.sh <ver> dist linux/amd64
#   scripts/build-deb.sh <ver> [dist]
#
# Requires dpkg-deb (Debian/Ubuntu). Installs the CLI binaries (tbound,
# tbound-doctor, tbound-profile) to /usr/bin, the runtime bundle to
# /usr/lib/tbound/runtime, and the linux/amd64 unsigned TCB entrypoint binary
# to /usr/lib/tbound/ (package-manager-owned;
# not on PATH). It is not trusted until admitted in a signed image/profile. No state
# changes are made on the build host beyond writing into the dist directory.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"
DIST="${2:-$ROOT/dist}"
ARCH="${TBOUND_DEB_ARCH:-amd64}"
TGZ="$DIST/tbound-$VERSION-linux-$ARCH.tar.gz"

die() { echo "build-deb: $*" >&2; exit 1; }
# shellcheck source=scripts/lib-release-verify.sh
source "$ROOT/scripts/lib-release-verify.sh"

# Validate inputs before requiring the packaging tool, so callers get a clear
# error either way.
if ! printf '%s' "$VERSION" | grep -Eq '^([0-9]+:)?[0-9][0-9A-Za-z.+~-]*$' || [ "${VERSION%-}" != "$VERSION" ]; then
  echo "build-deb: version '$VERSION' is not a valid Debian version (expect [epoch:]upstream[-revision]; no trailing '-')" >&2
  exit 1
fi
case "$ARCH" in
  amd64) ;;
  *) echo "build-deb: only linux/amd64 is supported; got '$ARCH'" >&2; exit 1 ;;
esac
command -v dpkg-deb >/dev/null 2>&1 || { echo "build-deb: dpkg-deb is required" >&2; exit 1; }
[ -f "$TGZ" ] || { echo "build-deb: missing $TGZ (run scripts/build-release.sh first)" >&2; exit 1; }
verify_archive_checksum "$TGZ" "$DIST/SHA256SUMS"
verify_archive_members "$TGZ"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-deb.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
tar -xzf "$TGZ" -C "$WORK"
verify_manifest_tree "$WORK"
[ -f "$WORK/bin/tbound" ] && [ -f "$WORK/bin/tbound-doctor" ] && [ -f "$WORK/bin/tbound-profile" ] || { echo "build-deb: release tarball binary contract is incomplete" >&2; exit 1; }
[ -f "$WORK/libexec/tbound/tbound-cell-entrypoint" ] && [ -f "$WORK/libexec/tbound/cell-entrypoint.json" ] || { echo "build-deb: linux/amd64 TCB artifact set is incomplete" >&2; exit 1; }

pkg="$WORK/pkg"
mkdir -p "$pkg/DEBIAN" "$pkg/usr/bin" "$pkg/usr/lib/tbound"
cp "$WORK/bin/tbound" "$WORK/bin/tbound-doctor" "$WORK/bin/tbound-profile" "$pkg/usr/bin/"
cp -R "$WORK/runtime" "$pkg/usr/lib/tbound/runtime"
cp "$WORK/libexec/tbound/tbound-cell-entrypoint" "$pkg/usr/lib/tbound/"
cp "$WORK/libexec/tbound/cell-entrypoint.json" "$pkg/usr/lib/tbound/"

cat > "$pkg/DEBIAN/control" <<CTRL
Package: tbound
Version: $VERSION
Section: utils
Priority: optional
Architecture: $ARCH
Maintainer: tbound <noreply@tbound.dev>
Description: Host-side supervisor for the Pi coding agent
 Installs the tbound supervisor CLI, the tbound-doctor preflight tool, and the
 Linux-only tbound-profile host-profile signer. On linux/amd64 the unsigned D09
 TCB entrypoint binary is installed under /usr/lib/tbound (not on PATH); package
 installation does not establish image signature or governed trust.
 Governed runs additionally require the runtime composition/authoritative
 verifier plus a signed host profile and the Podman/crun containment stack.
CTRL

# Normalize package modes so the caller's umask (077 in CI) cannot leak in; X keeps exec bits.
chmod -R u=rwX,go=rX "$pkg"
chmod 755 "$pkg/usr/bin/tbound" "$pkg/usr/bin/tbound-doctor" "$pkg/usr/bin/tbound-profile" \
  "$pkg/usr/lib/tbound/tbound-cell-entrypoint"
chmod 444 "$pkg/usr/lib/tbound/cell-entrypoint.json"

out="$DIST/tbound_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "$pkg" "$out"
echo "build-deb: wrote $out"
