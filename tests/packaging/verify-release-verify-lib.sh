#!/usr/bin/env bash
# Regression: scripts/lib-release-verify.sh, which the deb, rpm and npm packagers source,
# refuses an archive member whose name holds a backslash and a member that is a link,
# and accepts a plain archive. Offline; builds its own tarballs in TMPDIR.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
die() { echo "$*" >&2; exit 1; }
# shellcheck source=scripts/lib-release-verify.sh
source "$ROOT/scripts/lib-release-verify.sh"

T="$(mktemp -d "${TMPDIR:-/tmp}/tbound-verify-lib.XXXXXX")"
trap 'rm -rf "$T"' EXIT
mkdir "$T/src"
printf 'x\n' > "$T/src/plain"
printf 'x\n' > "$T/src/back\slash"
ln -s plain "$T/src/link"
tar -czf "$T/plain.tgz" -C "$T/src" plain
tar -czf "$T/backslash.tgz" -C "$T/src" "back\slash"
tar -czf "$T/link.tgz" -C "$T/src" link

( verify_archive_members "$T/plain.tgz" ) || { echo "FAIL: plain archive refused" >&2; exit 1; }
for bad in backslash link; do
  if ( verify_archive_members "$T/$bad.tgz" ) 2>/dev/null; then
    echo "FAIL: $bad archive accepted" >&2; exit 1
  fi
done
echo "verify-release-verify-lib: ok"
