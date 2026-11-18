#!/usr/bin/env bash
# Offline npm package-assembly verification for the B6 shipping increment.
#
# Builds the Linux release tarball into a private mode-0700 temp directory,
# assembles the npm package trees, and asserts that the Linux package carries
# the full binary set (tbound, tbound-doctor, tbound-profile), that no other
# platform package is generated, and that the linux/amd64 TCB entrypoint is
# never packaged for npm. Section 8 runs scripts/pack-npm-packages.sh, which packs
# the trees with npm and installs the tarballs offline into a temp prefix. Nothing
# touches the network or the user prefix.
#
# Usage: bash tests/packaging/verify-npm-packages.sh [version]
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${1:-0.0.0-pkgtest}"
NODE="${NODE:-node}"
if [ -z "${TMPDIR:-}" ] || [ ! -d "$TMPDIR" ] || [ "$(stat -c '%a' "$TMPDIR" 2>/dev/null || true)" != 700 ]; then
  echo "verify-npm-packages: set TMPDIR to an existing private mode-0700 directory" >&2
  exit 2
fi
BASE="$(mktemp -d "${TMPDIR:-/tmp}/tbound-npmtest.XXXXXX")"
chmod 700 "$BASE"
trap 'rm -rf "$BASE"' EXIT

pass=0; fail=0
ok()  { printf '  PASS  %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  FAIL  %s\n' "$1"; fail=$((fail+1)); }

DIST="${TBOUND_NPM_DIST:-$BASE/dist}"; OUT="$BASE/npm"
case "$DIST" in
  /*) ;;
  *) DIST="$ROOT/$DIST" ;;
esac
echo "verify-npm-packages: base=$BASE version=$VERSION"

if [ -n "${TBOUND_NPM_DIST:-}" ]; then
  echo "verify-npm-packages: using prebuilt dist $DIST"
else
  if ! "$ROOT/scripts/build-release.sh" "$VERSION" "$DIST" linux/amd64 >/dev/null; then
    bad "release build for npm packaging failed"
    echo "verify-npm-packages: $pass passed, $fail failed"
    exit 1
  fi
fi

if ! "$ROOT/scripts/make-npm-packages.sh" "$VERSION" "$DIST" "$OUT" >/dev/null; then
  bad "make-npm-packages failed"
  echo "verify-npm-packages: $pass passed, $fail failed"
  exit 1
fi
ok "npm package trees assembled"

check_platform() {
  local dir="$1" pkgname="$2" ext="$3"
  [ -d "$OUT/$dir" ] || { bad "$dir missing"; return; }
  for b in tbound tbound-doctor tbound-profile; do
    [ -f "$OUT/$dir/bin/$b$ext" ] && ok "$dir bin/$b$ext" || bad "$dir bin/$b$ext missing"
  done
  [ -f "$OUT/$dir/package.json" ] && ok "$dir package.json" || bad "$dir package.json missing"
  if grep -q "\"$pkgname\"" "$OUT/$dir/package.json"; then ok "$dir name $pkgname"; else bad "$dir name mismatch"; fi
  if cmp -s "$ROOT/LICENSE" "$OUT/$dir/LICENSE"; then ok "$dir carries canonical LICENSE text"; else bad "$dir LICENSE missing or differs from source"; fi
  if grep -q '"files": \["bin/", "LICENSE"\]' "$OUT/$dir/package.json"; then ok "$dir npm files includes LICENSE"; else bad "$dir npm files omits LICENSE"; fi
}

check_platform cli-linux-x64 "@tbound/cli-linux-x64" ""
for pkg in cli-darwin-x64 cli-darwin-arm64 cli-win32-x64; do
  if [ -d "$OUT/$pkg" ]; then bad "$pkg must not be generated"; else ok "$pkg not generated"; fi
done

echo "[2] root package + launcher"
rootpkg="$OUT/cli/package.json"
[ -f "$rootpkg" ] && ok "cli/package.json present" || bad "cli/package.json missing"
if cmp -s "$ROOT/LICENSE" "$OUT/cli/LICENSE"; then ok "root npm package carries canonical LICENSE text"; else bad "root npm package LICENSE missing or differs from source"; fi
if grep -q '"files": \["bin/", "scripts/", "README.md", "LICENSE"\]' "$rootpkg"; then ok "root npm files includes LICENSE"; else bad "root npm files omits LICENSE"; fi
if grep -q '"@tbound/cli-linux-x64"' "$rootpkg"; then
  ok "root optionalDependencies name the Linux package"
else
  bad "root optionalDependencies missing the Linux package"
fi
if grep -q -E '"@tbound/cli-(darwin|win32)' "$rootpkg"; then
  bad "root still advertises a non-Linux package"
else
  ok "root does not advertise a non-Linux package"
fi
if grep -q 'linux-arm64' "$rootpkg"; then bad "root still advertises linux-arm64"; else ok "root does not advertise linux-arm64"; fi

launcher="$OUT/cli/bin/tbound.js"
if grep -q '"doctor"' "$launcher" && grep -q '"profile"' "$launcher"; then
  ok "launcher dispatches doctor and profile"
else
  bad "launcher does not dispatch doctor/profile"
fi
if grep -q 'tbound-cell-entrypoint' "$launcher"; then bad "launcher references the TCB entrypoint"; else ok "launcher never references the TCB entrypoint"; fi

echo "[3] TCB entrypoint is never packaged for npm"
if find "$OUT" -name 'tbound-cell-entrypoint*' -print -quit | grep -q .; then
  bad "TCB entrypoint was packaged for npm"
else
  ok "no TCB entrypoint in npm trees"
fi
if find "$OUT" -name node_modules -type d -print -quit | grep -q .; then
  bad "npm assembly created node_modules"
else
  ok "no node_modules created"
fi

echo "[4] test the actual Node launcher dispatch"
if command -v "$NODE" >/dev/null 2>&1 || [ -x "$NODE" ]; then
  if "$NODE" "$ROOT/tests/packaging/verify-npm-launcher.js"; then
    ok "Node launcher contract passed"
  else
    bad "Node launcher contract failed"
  fi
else
  bad "Node runtime '$NODE' unavailable for launcher test"
fi

echo "[5] reject malicious archive manifests before checksumming paths"
bad_dist="$BASE/bad-dist"; bad_src="$BASE/bad-source"; bad_out="$BASE/bad-npm"
mkdir -m 700 "$bad_dist" "$bad_src"
tar -xzf "$DIST/tbound-$VERSION-linux-amd64.tar.gz" -C "$bad_src"
printf '%064d  ../outside-user-file\n' 0 > "$bad_src/MANIFEST.sha256"
tar -C "$bad_src" -czf "$bad_dist/tbound-$VERSION-linux-amd64.tar.gz" .
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$bad_dist" && sha256sum tbound-"$VERSION"-linux-amd64.tar.gz > SHA256SUMS )
else
  ( cd "$bad_dist" && shasum -a 256 tbound-"$VERSION"-linux-amd64.tar.gz > SHA256SUMS )
fi
if "$ROOT/scripts/make-npm-packages.sh" "$VERSION" "$bad_dist" "$bad_out" >"$BASE/bad-npm.log" 2>&1; then
  bad "npm package maker accepted a traversal path in MANIFEST.sha256"
else
  if grep -q "unsafe manifest path" "$BASE/bad-npm.log"; then ok "npm package maker rejects traversal manifest paths"; else bad "npm maker failed for an unexpected reason"; fi
fi

symlink_dist="$BASE/symlink-dist"; symlink_src="$BASE/symlink-source"; symlink_out="$BASE/symlink-npm"
mkdir -m 700 "$symlink_dist" "$symlink_src"
tar -xzf "$DIST/tbound-$VERSION-linux-amd64.tar.gz" -C "$symlink_src"
ln -s package.json "$symlink_src/runtime/manifest-link.json"
tar -C "$symlink_src" -czf "$symlink_dist/tbound-$VERSION-linux-amd64.tar.gz" .
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$symlink_dist" && sha256sum tbound-"$VERSION"-linux-amd64.tar.gz > SHA256SUMS )
else
  ( cd "$symlink_dist" && shasum -a 256 tbound-"$VERSION"-linux-amd64.tar.gz > SHA256SUMS )
fi
if "$ROOT/scripts/make-npm-packages.sh" "$VERSION" "$symlink_dist" "$symlink_out" >"$BASE/symlink-npm.log" 2>&1; then
  bad "npm package maker accepted a symlink archive entry"
else
  if grep -q "links or special files are not allowed" "$BASE/symlink-npm.log"; then ok "npm package maker rejects symlink archive entries"; else bad "npm maker rejected symlink archive for an unexpected reason"; fi
fi

echo "[6] skip non-Linux tarballs in a mixed dist"
mixed_dist="$BASE/mixed-dist"; mixed_out="$BASE/mixed-npm"
mkdir -m 700 "$mixed_dist"
cp "$DIST/tbound-$VERSION-linux-amd64.tar.gz" "$mixed_dist/"
cp "$DIST/tbound-$VERSION-linux-amd64.tar.gz" "$mixed_dist/tbound-$VERSION-darwin-amd64.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$mixed_dist" && sha256sum tbound-"$VERSION"-*.tar.gz > SHA256SUMS )
else
  ( cd "$mixed_dist" && shasum -a 256 tbound-"$VERSION"-*.tar.gz > SHA256SUMS )
fi
if "$ROOT/scripts/make-npm-packages.sh" "$VERSION" "$mixed_dist" "$mixed_out" >"$BASE/mixed.log" 2>&1; then
  ok "mixed dist assembled"
else
  bad "mixed dist failed to assemble"
fi
[ -d "$mixed_out/cli-linux-x64" ] && ok "mixed dist produced the Linux package" || bad "mixed dist missing the Linux package"
extra="$(cd "$mixed_out" && ls -d cli-*/ | grep -vx 'cli-linux-x64/' || true)"
[ -z "$extra" ] && ok "mixed dist skipped the darwin tarball" || bad "mixed dist produced another platform package: $extra"
grep -q 'skipping non-linux tarball' "$BASE/mixed.log" && ok "mixed dist logged the skip" || bad "mixed dist did not log the skip"

echo "[7] a non-Linux-only dist is refused"
only_dist="$BASE/darwin-only-dist"; only_out="$BASE/darwin-only-npm"
mkdir -m 700 "$only_dist"
cp "$DIST/tbound-$VERSION-linux-amd64.tar.gz" "$only_dist/tbound-$VERSION-darwin-amd64.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$only_dist" && sha256sum tbound-"$VERSION"-*.tar.gz > SHA256SUMS )
else
  ( cd "$only_dist" && shasum -a 256 tbound-"$VERSION"-*.tar.gz > SHA256SUMS )
fi
if "$ROOT/scripts/make-npm-packages.sh" "$VERSION" "$only_dist" "$only_out" >"$BASE/only.log" 2>&1; then
  bad "non-Linux-only dist was accepted"
else
  if grep -q 'no linux/amd64 tarball' "$BASE/only.log"; then
    ok "non-Linux-only dist names the missing Linux tarball"
  else
    bad "non-Linux-only dist failed for an unexpected reason"
  fi
fi

echo "[8] packed tarballs keep the executable bit and run after an offline install"
if "$ROOT/scripts/pack-npm-packages.sh" "$VERSION" "$OUT" "$BASE/packed" >"$BASE/pack.log" 2>&1; then
  ok "npm tarballs packed, checked, installed offline, and ran tbound --version"
else
  bad "pack-npm-packages failed: $(tail -1 "$BASE/pack.log")"
fi

echo
echo "verify-npm-packages: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
