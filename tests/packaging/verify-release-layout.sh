#!/usr/bin/env bash
# Offline packaging-layout verification for the B6 shipping increment.
#
# Builds release tarballs into a private mode-0700 temp directory and asserts the
# shipped binary contract and the verified tarball manifest. It never touches the
# repository's dist/, the user prefix, the network, npm, or any package manager.
#
# Usage:
#   bash tests/packaging/verify-release-layout.sh [version] [--require-linux]
#
# Diagnostic mode skips a Linux target build failure that is clearly from
# in-progress owner source and reports the exact compiler output. Release
# acceptance MUST pass --require-linux (or set
# TBOUND_REQUIRE_LINUX_RELEASE=1); in that mode any required skip, Linux build
# failure, missing Linux artifact, or unavailable required tool fails the test.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="1.2.3"
VERSION_SET=0
REQUIRE_LINUX=0
case "${TBOUND_REQUIRE_LINUX_RELEASE:-0}" in
  1|true|TRUE|yes|YES) REQUIRE_LINUX=1 ;;
  0|false|FALSE|no|NO|"") ;;
  *) echo "verify-release-layout: TBOUND_REQUIRE_LINUX_RELEASE must be 0/1" >&2; exit 2 ;;
esac
for arg in "$@"; do
  case "$arg" in
    --require-linux) REQUIRE_LINUX=1 ;;
    *)
      if [ "$VERSION_SET" -eq 0 ]; then VERSION="$arg"; VERSION_SET=1
      else echo "verify-release-layout: unexpected argument '$arg'" >&2; exit 2; fi
      ;;
  esac
done
GO="${GO:-go}"
NODE="${NODE:-node}"
if [ -z "${TMPDIR:-}" ] || [ ! -d "$TMPDIR" ] || [ "$(stat -c '%a' "$TMPDIR" 2>/dev/null || true)" != 700 ]; then
  echo "verify-release-layout: set TMPDIR to an existing private mode-0700 directory" >&2
  exit 2
fi
BASE="$(mktemp -d "${TMPDIR:-/tmp}/tbound-pkgtest.XXXXXX")"
chmod 700 "$BASE"
trap 'rm -rf "$BASE"' EXIT

pass=0; fail=0; skip=0
ok()    { printf '  PASS  %s\n' "$1"; pass=$((pass+1)); }
bad()   { printf '  FAIL  %s\n' "$1"; fail=$((fail+1)); }
skipf() {
  if [ "$REQUIRE_LINUX" -eq 1 ]; then
    printf '  FAIL  required check skipped: %s\n' "$1"
    fail=$((fail+1))
  else
    printf '  SKIP  %s\n' "$1"
    skip=$((skip+1))
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}';
  else echo "no sha256 tool" >&2; exit 1; fi
}

check_sums() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    ( cd "$(dirname "$file")" && sha256sum --quiet -c "$(basename "$file")" )
  elif command -v shasum >/dev/null 2>&1; then
    ( cd "$(dirname "$file")" && shasum -a 256 -c "$(basename "$file")" >/dev/null )
  else
    return 1
  fi
}

verify_manifest() {
  local dir="$1" line want rel got rc=0
  local seen="|"
  [ -f "$dir/MANIFEST.sha256" ] || return 1
  while IFS= read -r line; do
    [ -n "$line" ] || { printf '    empty manifest record\n'; rc=1; continue; }
    if [[ ! "$line" =~ ^([0-9a-f]{64})\ \ (.+)$ ]]; then
      printf '    malformed manifest record\n'; rc=1; continue
    fi
    want="${BASH_REMATCH[1]}"
    rel="${BASH_REMATCH[2]}"
    case "$rel" in
      ./*) rel="${rel#./}" ;;
    esac
    if [[ ! "$rel" =~ ^[A-Za-z0-9._/-]+$ ]]; then printf '    invalid manifest path %q\n' "$rel"; rc=1; continue; fi
    case "$rel" in
      ""|/*|.|..|../*|*/../*|*/..|*\\*) printf '    unsafe manifest path %q\n' "$rel"; rc=1; continue ;;
    esac
    case "$seen" in *"|$rel|"*) printf '    duplicate manifest path %s\n' "$rel"; rc=1; continue ;; esac
    seen+="$rel|"
    if [ -L "$dir/$rel" ]; then printf '    symlink in manifest %s\n' "$rel"; rc=1; continue; fi
    if [ ! -f "$dir/$rel" ]; then printf '    missing/non-regular %s\n' "$rel"; rc=1; continue; fi
    got="$(sha256_of "$dir/$rel")"
    if [ "$got" != "$want" ]; then printf '    digest mismatch %s\n' "$rel"; rc=1; fi
  done < "$dir/MANIFEST.sha256"
  while IFS= read -r -d '' rel; do
    rel="${rel#"$dir"/}"
    if ! grep -Fq "  $rel" "$dir/MANIFEST.sha256" && ! grep -Fq "  ./$rel" "$dir/MANIFEST.sha256"; then
      printf '    unlisted file %s\n' "$rel"; rc=1
    fi
  done < <(find "$dir" -type f ! -name MANIFEST.sha256 -print0)
  while IFS= read -r -d '' rel; do
    printf '    non-regular artifact object %s\n' "$rel"
    rc=1
  done < <(find "$dir" ! -type d ! -type f -print0)
  return $rc
}

DIST="$BASE/dist"
mode=diagnostic; [ "$REQUIRE_LINUX" -eq 1 ] && mode=release-acceptance
echo "verify-release-layout: base=$BASE version=$VERSION mode=$mode"

if bash "$ROOT/tests/packaging/verify-release-workflow-coverage.sh" "$VERSION"; then
  ok "workflow upload coverage matches release sidecar names"
else
  bad "workflow upload coverage does not match release sidecar names"
fi

echo "[1] build windows/amd64 darwin/amd64 darwin/arm64"
if ! "$ROOT/scripts/build-release.sh" "$VERSION" "$DIST" windows/amd64 darwin/amd64 darwin/arm64 >/dev/null; then
  bad "non-linux release build failed"
  echo "verify-release-layout: $pass passed, $fail failed, $skip skipped"
  exit 1
fi
ok "non-linux tarballs built"

check_nonlinux() {
  local tar="$1" os="$2" ext="$3"
  local x="$BASE/x-$os"
  mkdir -p "$x"; tar -xzf "$tar" -C "$x" || { bad "$os extract"; return; }
  [ -f "$x/bin/tbound$ext" ] && ok "$os bin/tbound$ext" || bad "$os bin/tbound$ext missing"
  [ -f "$x/bin/tbound-doctor$ext" ] && ok "$os bin/tbound-doctor$ext" || bad "$os bin/tbound-doctor$ext missing"
  [ -f "$x/bin/tbound-profile$ext" ] && ok "$os bin/tbound-profile$ext" || bad "$os bin/tbound-profile$ext missing"
  [ -f "$x/runtime/package.json" ] && ok "$os runtime/package.json" || bad "$os runtime/package.json missing"
  [ -f "$x/VERSION" ] && ok "$os VERSION" || bad "$os VERSION missing"
  if [ -e "$x/libexec/tbound/tbound-cell-entrypoint" ]; then
    bad "$os must not ship the linux/amd64 TCB entrypoint"
  else
    ok "$os has no TCB entrypoint"
  fi
  if verify_manifest "$x"; then ok "$os MANIFEST.sha256 verifies"; else bad "$os MANIFEST.sha256 failed"; fi
}

check_nonlinux "$DIST/tbound-$VERSION-windows-amd64.tar.gz" windows .exe
check_nonlinux "$DIST/tbound-$VERSION-darwin-amd64.tar.gz" darwin-amd64 ""
check_nonlinux "$DIST/tbound-$VERSION-darwin-arm64.tar.gz" darwin ""

echo "[1b] manifest rejects bad paths and symlinks"
badpath="$BASE/x-badpath"; cp -a "$BASE/x-windows" "$badpath"
printf '%064d  ../escape\n' 0 > "$badpath/MANIFEST.sha256"
if verify_manifest "$badpath" >/dev/null 2>&1; then bad "manifest accepted ../ path"; else ok "manifest rejects ../ path"; fi
symlinkdir="$BASE/x-symlink"; cp -a "$BASE/x-windows" "$symlinkdir"
ln -s VERSION "$symlinkdir/link-to-version"
printf '%s  link-to-version\n' "$(sha256_of "$symlinkdir/VERSION")" > "$symlinkdir/MANIFEST.sha256"
if verify_manifest "$symlinkdir" >/dev/null 2>&1; then bad "manifest accepted symlink"; else ok "manifest rejects symlink"; fi
tamperdir="$BASE/x-tamper"; cp -a "$BASE/x-windows" "$tamperdir"
printf 'tamper\n' >> "$tamperdir/runtime/package.json"
if verify_manifest "$tamperdir" >/dev/null 2>&1; then bad "manifest accepted content tamper"; else ok "manifest detects content tamper"; fi
dupdir="$BASE/x-duplicate"; cp -a "$BASE/x-windows" "$dupdir"
first_record="$(head -n1 "$dupdir/MANIFEST.sha256")"
printf '%s\n' "$first_record" >> "$dupdir/MANIFEST.sha256"
if verify_manifest "$dupdir" >/dev/null 2>&1; then bad "manifest accepted duplicate path"; else ok "manifest rejects duplicate path"; fi

echo "[2] SHA256SUMS covers tarballs and standalone TCB metadata"
sums="$DIST/SHA256SUMS"
for f in "$DIST"/tbound-"$VERSION"-*.tar.gz; do
  base="$(basename "$f")"
  if grep -q "$base" "$sums"; then ok "SHA256SUMS has $base"; else bad "SHA256SUMS missing $base"; fi
done
if check_sums "$DIST/SHA256SUMS"; then
  ok "SHA256SUMS digests verify"
else
  bad "SHA256SUMS digest verification failed"
fi
echo "[3] build linux/amd64 (entrypoint + metadata)"
LIN="$BASE/dist-lin"
LINLOG="$BASE/linux-build.log"
if "$ROOT/scripts/build-release.sh" "$VERSION" "$LIN" linux/amd64 >"$LINLOG" 2>&1; then
  x="$BASE/x-linux"; mkdir -p "$x"
  tar -xzf "$LIN/tbound-$VERSION-linux-amd64.tar.gz" -C "$x"
  [ -f "$x/bin/tbound" ] && ok "linux bin/tbound" || bad "linux bin/tbound missing"
  [ -f "$x/bin/tbound-doctor" ] && ok "linux bin/tbound-doctor" || bad "linux bin/tbound-doctor missing"
  [ -f "$x/bin/tbound-profile" ] && ok "linux bin/tbound-profile" || bad "linux bin/tbound-profile missing"
  [ -x "$x/libexec/tbound/tbound-cell-entrypoint" ] && ok "linux TCB entrypoint present" || bad "linux TCB entrypoint missing"
  [ -f "$x/libexec/tbound/cell-entrypoint.json" ] && ok "linux entrypoint metadata present" || bad "linux entrypoint metadata missing"
  if verify_manifest "$x"; then ok "linux MANIFEST.sha256 verifies"; else bad "linux MANIFEST.sha256 failed"; fi

  meta="$LIN/tbound-cell-entrypoint-$VERSION-linux-amd64.metadata.json"
  metasha="$LIN/tbound-cell-entrypoint-$VERSION-linux-amd64.sha256"
  if [ -f "$meta" ] && [ -f "$metasha" ]; then
    ok "separate entrypoint metadata + checksum artifacts present"
    if cmp -s "$meta" "$x/libexec/tbound/cell-entrypoint.json"; then ok "external metadata matches tarball metadata"; else bad "external metadata differs from tarball metadata"; fi
    if command -v "$NODE" >/dev/null 2>&1; then
      if "$NODE" -e 'JSON.parse(require("node:fs").readFileSync(process.argv[1], "utf8"))' "$meta"; then
        ok "entrypoint metadata is valid JSON"
      else
        bad "entrypoint metadata is invalid JSON"
      fi
    else
      skipf "Node unavailable for JSON metadata parse"
    fi
    meta_digest="$(sed -n 's/.*"sha256"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$meta" | head -n1)"
    meta_digest="${meta_digest#sha256:}"
    bin_digest="$(sha256_of "$x/libexec/tbound/tbound-cell-entrypoint")"
    if [ -n "$meta_digest" ] && [ "$meta_digest" = "$bin_digest" ]; then
      ok "entrypoint metadata digest matches binary"
    else
      bad "entrypoint metadata digest mismatch ($meta_digest vs $bin_digest)"
    fi
    if grep -q "$bin_digest" "$metasha" && grep -q 'libexec/tbound/tbound-cell-entrypoint' "$metasha"; then ok "entrypoint .sha256 path and digest match binary"; else bad "entrypoint .sha256 path/digest mismatch"; fi
    if ( cd "$x" && sha256sum --quiet -c "$metasha" ); then ok "entrypoint separate checksum verifies extracted binary"; else bad "entrypoint separate checksum verification failed"; fi
    if grep -q "$(basename "$meta")" "$LIN/SHA256SUMS" && grep -q "$(basename "$metasha")" "$LIN/SHA256SUMS"; then
      ok "SHA256SUMS covers standalone metadata and checksum files"
    else
      bad "SHA256SUMS omits standalone metadata/checksum files"
    fi
    workflow="$ROOT/.github/workflows/release.yml"
    metadata_glob='dist/tbound-cell-entrypoint-*-linux-amd64.metadata.json'
    checksum_glob='dist/tbound-cell-entrypoint-*-linux-amd64.sha256'
    if [ "$(grep -Fxc "            dist/SHA256SUMS" "$workflow")" -eq 2 ] &&
       [ "$(grep -Fxc "            $metadata_glob" "$workflow")" -eq 2 ] &&
       [ "$(grep -Fxc "            $checksum_glob" "$workflow")" -eq 2 ]; then
      ok "both upload-artifact and GitHub release include SHA256SUMS and exact metadata sidecar globs"
    else
      bad "workflow is missing the metadata sidecar glob in one or both upload lists"
    fi
    while read -r _sum_member; do
      [ -n "$_sum_member" ] || continue
      case "$_sum_member" in
        tbound-*.tar.gz|tbound-cell-entrypoint-*-linux-amd64.metadata.json|tbound-cell-entrypoint-*-linux-amd64.sha256) ;;
        *) bad "SHA256SUMS member is outside workflow upload globs: $_sum_member" ;;
      esac
    done < <(awk '{print $2}' "$LIN/SHA256SUMS")
    if check_sums "$LIN/SHA256SUMS"; then
      ok "Linux SHA256SUMS digests verify"
    else
      bad "Linux SHA256SUMS digest verification failed"
    fi
    grep -q '"source_tree_sha256": "sha256:' "$meta" && ok "entrypoint source-tree provenance present" || bad "source-tree provenance missing"
    grep -q '"entrypoint_source_sha256": "sha256:' "$meta" && ok "entrypoint source digest present" || bad "entrypoint source digest missing"
    grep -q '"d09_template_source_sha256": "sha256:' "$meta" && ok "D09 template source digest present" || bad "D09 template source digest missing"
    grep -q '"registered_d09_profile_digest": null' "$meta" && ok "unmeasured registered profile stays null" || bad "metadata invented a profile digest"
    grep -q '"signed": false' "$meta" && ok "entrypoint metadata truthfully says unsigned" || bad "entrypoint signed state is misleading"
    grep -q '"license": "MIT"' "$meta" && ok "license metadata present" || bad "license metadata missing"
  else
    bad "separate entrypoint metadata/checksum artifacts missing"
  fi

  if "$x/bin/tbound-profile" --help >/dev/null 2>&1; then ok "linux tbound-profile --help exit 0"; else bad "linux tbound-profile --help failed"; fi
  if command -v dpkg-deb >/dev/null 2>&1; then
    if TBOUND_DEB_ARCH=amd64 "$ROOT/scripts/build-deb.sh" "$VERSION" "$LIN" >/dev/null; then
      deb="$LIN/tbound_${VERSION}_amd64.deb"
      contents="$(dpkg-deb --contents "$deb")"
      for path in "/usr/bin/tbound" "/usr/bin/tbound-doctor" "/usr/bin/tbound-profile" \
                  "/usr/lib/tbound/tbound-cell-entrypoint" "/usr/lib/tbound/cell-entrypoint.json"; do
        if printf '%s\n' "$contents" | grep -Fq "$path"; then ok "DEB contains $path"; else bad "DEB missing $path"; fi
      done
      if printf '%s\n' "$contents" | grep -Fq '/usr/bin/tbound-cell-entrypoint'; then bad "DEB put TCB entrypoint on PATH"; else ok "DEB keeps entrypoint outside PATH"; fi
    else
      bad "build-deb failed on verified linux/amd64 release tarball"
    fi
  else
    skipf "dpkg-deb unavailable"
  fi
  if "$ROOT/scripts/build-rpm.sh" --check "$VERSION" "$LIN" >/dev/null; then
    ok "RPM input checks accept the verified linux/amd64 archive"
  else
    bad "RPM input checks failed for verified linux/amd64 archive"
  fi
  set +e
  "$x/libexec/tbound/tbound-cell-entrypoint" >/dev/null 2>&1
  entrypoint_rc=$?
  set -e
  if [ "$entrypoint_rc" -eq 97 ]; then
    ok "entrypoint fails closed without governed fds"
  else
    bad "entrypoint without governed fds exit=$entrypoint_rc (want 97)"
  fi
  if "$ROOT/scripts/build-release.sh" "$VERSION" "$BASE/dist-unsupported" linux/arm64 >"$BASE/unsupported.log" 2>&1; then
    bad "unsupported linux/arm64 target was built"
  elif grep -q 'linux/arm64 is not supported/advertised' "$BASE/unsupported.log"; then
    ok "linux/arm64 is explicitly rejected"
  else
    bad "linux/arm64 rejection message missing"
  fi
  npm_linux="$BASE/npm-linux"
  if "$ROOT/scripts/make-npm-packages.sh" "$VERSION" "$LIN" "$npm_linux" >/dev/null; then
    for b in tbound tbound-doctor tbound-profile; do
      [ -f "$npm_linux/cli-linux-x64/bin/$b" ] && ok "Linux npm package carries $b" || bad "Linux npm package missing $b"
    done
    if find "$npm_linux" -name tbound-cell-entrypoint -print -quit | grep -q .; then
      bad "Linux npm package contains TCB entrypoint"
    else
      ok "Linux npm package omits TCB entrypoint"
    fi
  else
    bad "Linux npm package assembly failed against verified Linux archive"
  fi
else
  if grep -Eq 'internal/piruntime|cmd/tbound' "$LINLOG"; then
    skipf "linux/amd64 build blocked by supervisor source compile error:"
    sed -n '1,3p' "$LINLOG" | sed 's/^/        /'
  else
    bad "linux/amd64 release build failed for a non-upstream reason"
    sed -n '1,10p' "$LINLOG" | sed 's/^/        /'
  fi
fi

echo
echo "verify-release-layout: $pass passed, $fail failed, $skip skipped"
[ "$fail" -eq 0 ]
