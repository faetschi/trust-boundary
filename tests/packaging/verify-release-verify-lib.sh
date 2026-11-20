#!/usr/bin/env bash
# Regression: scripts/lib-release-verify.sh, which the deb, rpm and npm packagers source,
# refuses an archive member whose name holds a backslash and a member that is a link,
# and a MANIFEST.sha256 tree with a duplicate entry, an unlisted file or a wrong digest;
# it accepts the plain cases. Offline; builds its own fixtures in TMPDIR.
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

m="$T/tree"
mkdir "$m"
printf 'x\n' > "$m/a"
(cd "$m" && sha256sum a > MANIFEST.sha256)
( verify_manifest_tree "$m" ) || { echo "FAIL: matching manifest refused" >&2; exit 1; }
cp -r "$m" "$T/dup" && (cd "$T/dup" && sha256sum a >> MANIFEST.sha256)
cp -r "$m" "$T/extra" && printf 'y\n' > "$T/extra/b"
cp -r "$m" "$T/digest" && printf 'z\n' > "$T/digest/a"
for bad in dup extra digest; do
  if ( verify_manifest_tree "$T/$bad" ) 2>/dev/null; then
    echo "FAIL: $bad manifest accepted" >&2; exit 1
  fi
done
echo "verify-release-verify-lib: ok"
