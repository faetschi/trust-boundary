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
[ -f "$DIST/SHA256SUMS" ] || { echo "build-deb: missing $DIST/SHA256SUMS; refusing unverified tarball" >&2; exit 1; }
want="$(awk -v a="$(basename "$TGZ")" '$2==a || $2=="./"a {print $1; exit}' "$DIST/SHA256SUMS")"
[ -n "$want" ] || { echo "build-deb: SHA256SUMS has no entry for $(basename "$TGZ")" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$TGZ" | awk '{print $1}')";
elif command -v shasum >/dev/null 2>&1; then got="$(shasum -a 256 "$TGZ" | awk '{print $1}')";
else echo "build-deb: need sha256sum or shasum to verify tarball" >&2; exit 1; fi
[ "$want" = "$got" ] || { echo "build-deb: tarball checksum mismatch" >&2; exit 1; }
while IFS= read -r member; do
  case "$member" in
    ""|/*|../*|*/../*|*/..|*\\*) echo "build-deb: unsafe archive path: $member" >&2; exit 1 ;;
  esac
done < <(tar -tzf "$TGZ")
tar -tvzf "$TGZ" | awk 'substr($0,1,1)!="-" && substr($0,1,1)!="d" { bad=1 } END { exit bad ? 1 : 0 }' \
  || { echo "build-deb: links or special files are not allowed in the release archive" >&2; exit 1; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-deb.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
tar -xzf "$TGZ" -C "$WORK"
seen="|"
while IFS= read -r record; do
  [[ "$record" =~ ^[0-9a-f]{64}\ \ (.+)$ ]] || { echo "build-deb: malformed MANIFEST.sha256 record" >&2; exit 1; }
  member="${BASH_REMATCH[1]}"
  case "$member" in ./*) member="${member#./}" ;; esac
  [[ "$member" =~ ^[A-Za-z0-9._/-]+$ ]] || { echo "build-deb: invalid manifest path" >&2; exit 1; }
  case "$member" in ""|/*|.|..|../*|*/../*|*/..) echo "build-deb: unsafe manifest path" >&2; exit 1 ;; esac
  case "$seen" in *"|$member|"*) echo "build-deb: duplicate manifest path" >&2; exit 1 ;; esac
  seen+="$member|"
  [ -f "$WORK/$member" ] && [ ! -L "$WORK/$member" ] || { echo "build-deb: manifest path is missing/non-regular" >&2; exit 1; }
done < "$WORK/MANIFEST.sha256"
while IFS= read -r -d '' file; do
  member="${file#"$WORK"/}"
  case "$seen" in *"|$member|"*) ;; *) echo "build-deb: unlisted regular file in archive: $member" >&2; exit 1 ;; esac
done < <(find "$WORK" -type f ! -name MANIFEST.sha256 -print0)
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$WORK" && sha256sum --quiet -c MANIFEST.sha256 ) || { echo "build-deb: tarball manifest verification failed" >&2; exit 1; }
elif command -v shasum >/dev/null 2>&1; then
  ( cd "$WORK" && shasum -a 256 -c MANIFEST.sha256 >/dev/null ) || { echo "build-deb: tarball manifest verification failed" >&2; exit 1; }
else
  echo "build-deb: need sha256sum or shasum to verify MANIFEST.sha256" >&2; exit 1
fi
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

# Package permissions must not inherit the caller's private build umask.
# Only the package tree is normalized; the enclosing work directory stays private.
find "$pkg" -type d -exec chmod 755 {} +
find "$pkg" -type f -exec chmod 644 {} +
chmod 755 "$pkg/usr/bin/tbound" "$pkg/usr/bin/tbound-doctor" "$pkg/usr/bin/tbound-profile" \
  "$pkg/usr/lib/tbound/tbound-cell-entrypoint"
chmod 444 "$pkg/usr/lib/tbound/cell-entrypoint.json"

out="$DIST/tbound_${VERSION}_${ARCH}.deb"
dpkg-deb --build --root-owner-group "$pkg" "$out"
echo "build-deb: wrote $out"
