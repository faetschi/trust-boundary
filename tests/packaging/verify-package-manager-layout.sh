#!/usr/bin/env bash
# Package-layout checks using a synthetic, private release archive. No Go binary
# in this archive is executable product code; it exists only to exercise the DEB
# file-list build and RPM input checks when owner-source Linux builds are blocked.
# The script never installs packages, changes rpm/dpkg databases, or writes into
# the repository dist directory.
set -euo pipefail
umask 077

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION=1.2.3
if [ -z "${TMPDIR:-}" ] || [ ! -d "$TMPDIR" ] || [ "$(stat -c '%a' "$TMPDIR" 2>/dev/null || true)" != 700 ]; then
  echo "verify-package-manager-layout: set TMPDIR to an existing private mode-0700 directory" >&2
  exit 2
fi
BASE="$(mktemp -d "${TMPDIR:-/tmp}/tbound-pkgmgr.XXXXXX")"
chmod 700 "$BASE"
trap 'rm -rf "$BASE"' EXIT

STAGE="$BASE/stage"
DIST="$BASE/dist"
mkdir -m 700 "$STAGE" "$DIST"
mkdir -p "$STAGE/bin" "$STAGE/runtime" "$STAGE/libexec/tbound" "$STAGE/share/completions"
for binary in tbound tbound-doctor tbound-profile; do
  printf 'synthetic test artifact: %s\n' "$binary" > "$STAGE/bin/$binary"
  chmod 755 "$STAGE/bin/$binary"
done
printf '{"synthetic":true}\n' > "$STAGE/runtime/package.json"
printf 'unsigned test-only entrypoint\n' > "$STAGE/libexec/tbound/tbound-cell-entrypoint"
chmod 755 "$STAGE/libexec/tbound/tbound-cell-entrypoint"
printf '{"signed":false,"synthetic":true}\n' > "$STAGE/libexec/tbound/cell-entrypoint.json"
printf '%s  %s\n' "$(sha256sum "$STAGE/libexec/tbound/tbound-cell-entrypoint" | awk '{print $1}')" \
  libexec/tbound/tbound-cell-entrypoint > "$STAGE/libexec/tbound/entrypoint.sha256"
printf '%s\n' "$VERSION" > "$STAGE/VERSION"
( cd "$STAGE" && find . -type f ! -name MANIFEST.sha256 | LC_ALL=C sort | while IFS= read -r file; do
    printf '%s  %s\n' "$(sha256sum "$file" | awk '{print $1}')" "$file"
  done ) > "$STAGE/MANIFEST.sha256"
tar -C "$STAGE" -czf "$DIST/tbound-$VERSION-linux-amd64.tar.gz" .
( cd "$DIST" && sha256sum tbound-"$VERSION"-linux-amd64.tar.gz > SHA256SUMS )

echo "verify-package-manager-layout: private synthetic artifact; not release binaries"
if command -v dpkg-deb >/dev/null 2>&1; then
  "$ROOT/scripts/build-deb.sh" "$VERSION" "$DIST" >/dev/null
  DEB="$DIST/tbound_${VERSION}_amd64.deb"
  [ -f "$DEB" ]
  contents="$(dpkg-deb --contents "$DEB")"
  for path in "/usr/bin/tbound" "/usr/bin/tbound-doctor" "/usr/bin/tbound-profile" \
              "/usr/lib/tbound/runtime/package.json" "/usr/lib/tbound/tbound-cell-entrypoint" \
              "/usr/lib/tbound/cell-entrypoint.json"; do
    if printf '%s\n' "$contents" | grep -Fq "$path"; then
      printf '  PASS  DEB contains %s\n' "$path"
    else
      echo "  FAIL  DEB missing $path" >&2
      exit 1
    fi
  done
  if printf '%s\n' "$contents" | grep -Fq '/usr/bin/tbound-cell-entrypoint'; then
    echo "  FAIL  DEB exposed TCB entrypoint on PATH" >&2
    exit 1
  fi
  echo "  PASS  DEB keeps TCB entrypoint outside /usr/bin"
  check_mode() {
    local listing="$1" mode="$2" path="$3"
    if ! printf '%s\n' "$listing" | awk -v mode="$mode" -v path="$path" \
        '$1 == mode && $NF == path {found=1} END {exit !found}'; then
      echo "  FAIL  DEB expected $mode for $path" >&2
      exit 1
    fi
  }
  for path in ./ ./usr/ ./usr/bin/ ./usr/lib/ ./usr/lib/tbound/ ./usr/lib/tbound/runtime/; do
    check_mode "$contents" drwxr-xr-x "$path"
  done
  for path in ./usr/bin/tbound ./usr/bin/tbound-doctor ./usr/bin/tbound-profile \
              ./usr/lib/tbound/tbound-cell-entrypoint; do
    check_mode "$contents" -rwxr-xr-x "$path"
  done
  check_mode "$contents" -rw-r--r-- ./usr/lib/tbound/runtime/package.json
  check_mode "$contents" -r--r--r-- ./usr/lib/tbound/cell-entrypoint.json
  control_contents="$(dpkg-deb --ctrl-tarfile "$DEB" | tar -tvf -)"
  check_mode "$control_contents" drwxr-xr-x ./
  check_mode "$control_contents" -rw-r--r-- ./control
  [ "$(dpkg-deb --field "$DEB" Package)" = tbound ]
  [ "$(dpkg-deb --field "$DEB" Version)" = "$VERSION" ]
  [ "$(dpkg-deb --field "$DEB" Architecture)" = amd64 ]
  [ "$(stat -c '%a' "$BASE")" = 700 ]
  echo "  PASS  DEB metadata and payload modes are correct under umask 077; outer temp stays private"
else
  echo "  SKIP  dpkg-deb unavailable"
fi

"$ROOT/scripts/build-rpm.sh" --check "$VERSION" "$DIST" >/dev/null
echo "  PASS  RPM input/check path validates the checksum and manifest"
for required in '%{_bindir}/tbound-profile' '%{_libexecdir}/tbound' 'tbound-cell-entrypoint' 'cell-entrypoint.json'; do
  if grep -Fq "$required" "$ROOT/packaging/rpm/tbound.spec"; then
    printf '  PASS  RPM spec includes %s\n' "$required"
  else
    printf '  FAIL  RPM spec missing %s\n' "$required" >&2
    exit 1
  fi
done
echo "verify-package-manager-layout: completed (synthetic layout only; no runtime claim)"
