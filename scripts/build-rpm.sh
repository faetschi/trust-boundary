#!/usr/bin/env bash
# Build an RPM from a built linux release tarball using rpmbuild.
#
#   scripts/build-release.sh <ver> dist linux/amd64
#   scripts/build-rpm.sh <ver> [dist]
#   scripts/build-rpm.sh --check <ver> [dist]   # validate inputs only
#
# Requires rpmbuild. The build writes into a private topdir and copies the
# resulting .rpm into dist/. No changes are made to the host rpm database.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SPEC="$ROOT/packaging/rpm/tbound.spec"

CHECK=0
if [ "${1:-}" = "--check" ]; then CHECK=1; shift; fi
VERSION="${1:-dev}"
DIST="${2:-$ROOT/dist}"

die() { echo "build-rpm: $*" >&2; exit 1; }

case "$VERSION" in
  *[!A-Za-z0-9._-]*|"") die "invalid version '$VERSION' (allow A-Za-z0-9._-)" ;;
esac
# Bind the RPM architecture to the artifact we actually ship. Only the
# linux/amd64 (x86_64) tarball is supported today; linux/arm64 does not build.
ARCH_TAG="amd64"; RPM_ARCH="x86_64"
if [ -n "${TBOUND_RPM_ARCH:-}" ] && [ "$TBOUND_RPM_ARCH" != "x86_64" ]; then
  die "only x86_64 is supported (linux/arm64 does not build yet); got TBOUND_RPM_ARCH=$TBOUND_RPM_ARCH"
fi
ARTIFACT="$DIST/tbound-$VERSION-linux-$ARCH_TAG.tar.gz"

[ -f "$SPEC" ] || die "missing spec $SPEC"
[ -f "$ARTIFACT" ] || die "missing $ARTIFACT (run scripts/build-release.sh first)"

if [ "$CHECK" = 1 ]; then
  if command -v rpmbuild >/dev/null 2>&1; then
    echo "build-rpm: check ok (rpmbuild present)"
  else
    echo "build-rpm: check ok, but rpmbuild is ABSENT — build will be skipped"
  fi
  echo "build-rpm: would package $ARTIFACT as tbound-$VERSION-1.$RPM_ARCH.rpm"
  exit 0
fi

command -v rpmbuild >/dev/null 2>&1 || die "rpmbuild is required to build an RPM (install rpm-build)"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-rpm.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
TOPDIR="$WORK/rpmbuild"
mkdir -p "$TOPDIR"/{BUILD,BUILDROOT,RPMS,SOURCES,SPECS,SRPMS}
cp "$ARTIFACT" "$TOPDIR/SOURCES/$(basename "$ARTIFACT")"

rpmbuild -bb "$SPEC" \
  --define "_topdir $TOPDIR" \
  --define "_tbound_version $VERSION" \
  --define "_tbound_rpm_arch $RPM_ARCH" \
  --define "_tbound_srcfile $(basename "$ARTIFACT")"

built="$(find "$TOPDIR/RPMS" -name '*.rpm' -print -quit)"
[ -n "$built" ] || die "rpmbuild produced no .rpm"
cp "$built" "$DIST/"
echo "build-rpm: wrote $DIST/$(basename "$built")"
