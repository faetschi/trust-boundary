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
  *[!A-Za-z0-9._]*|"") die "invalid version '$VERSION' (allow A-Za-z0-9._; RPM forbids '-')" ;;
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
[ -f "$DIST/SHA256SUMS" ] || die "missing $DIST/SHA256SUMS; refusing unverified tarball"
want="$(awk -v a="$(basename "$ARTIFACT")" '$2==a || $2=="./"a {print $1; exit}' "$DIST/SHA256SUMS")"
[ -n "$want" ] || die "SHA256SUMS has no entry for $(basename "$ARTIFACT")"
if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$ARTIFACT" | awk '{print $1}')";
elif command -v shasum >/dev/null 2>&1; then got="$(shasum -a 256 "$ARTIFACT" | awk '{print $1}')";
else die "need sha256sum or shasum to verify the tarball"; fi
[ "$want" = "$got" ] || die "tarball checksum mismatch"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-rpm.XXXXXX")"
chmod 700 "$WORK"
trap 'rm -rf "$WORK"' EXIT
ARCHIVE="$WORK/archive"
mkdir -m 700 "$ARCHIVE"
while IFS= read -r member; do
  case "$member" in
    ""|/*|../*|*/../*|*/..|*\\*) die "unsafe path in release archive: $member" ;;
  esac
done < <(tar -tzf "$ARTIFACT")
tar -tvzf "$ARTIFACT" | awk 'substr($0,1,1)!="-" && substr($0,1,1)!="d" { bad=1 } END { exit bad ? 1 : 0 }' || die "links or special files are not allowed in release archive"
tar -xzf "$ARTIFACT" -C "$ARCHIVE" || die "release archive extraction failed"
seen="|"
while IFS= read -r record; do
  [[ "$record" =~ ^[0-9a-f]{64}\ \ (.+)$ ]] || die "malformed MANIFEST.sha256 record"
  member="${BASH_REMATCH[1]}"
  case "$member" in ./*) member="${member#./}" ;; esac
  [[ "$member" =~ ^[A-Za-z0-9._/-]+$ ]] || die "invalid path in MANIFEST.sha256"
  case "$member" in ""|/*|.|..|../*|*/../*|*/..) die "unsafe path in MANIFEST.sha256" ;; esac
  case "$seen" in *"|$member|"*) die "duplicate path in MANIFEST.sha256" ;; esac
  seen+="$member|"
  [ -f "$ARCHIVE/$member" ] && [ ! -L "$ARCHIVE/$member" ] || die "manifest path is missing/non-regular: $member"
done < "$ARCHIVE/MANIFEST.sha256"
while IFS= read -r -d '' file; do
  member="${file#"$ARCHIVE"/}"
  case "$seen" in *"|$member|"*) ;; *) die "unlisted regular file in release archive: $member" ;; esac
done < <(find "$ARCHIVE" -type f ! -name MANIFEST.sha256 -print0)
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$ARCHIVE" && sha256sum --quiet -c MANIFEST.sha256 ) || die "tarball manifest verification failed"
elif command -v shasum >/dev/null 2>&1; then
  ( cd "$ARCHIVE" && shasum -a 256 -c MANIFEST.sha256 >/dev/null ) || die "tarball manifest verification failed"
else
  die "need sha256sum or shasum to verify MANIFEST.sha256"
fi
[ -f "$ARCHIVE/bin/tbound" ] && [ -f "$ARCHIVE/bin/tbound-doctor" ] && [ -f "$ARCHIVE/bin/tbound-profile" ] || die "release tarball binary contract is incomplete"

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
