#!/usr/bin/env bash
# End-to-end verification of the tbound install flow, entirely offline.
#
# It builds a local release, installs it into a throwaway private prefix, and
# asserts the exact runtime contract:
#   * bin/tbound, bin/tbound-doctor, and bin/tbound-profile exist
#   * `serve --pi --native-fixture` exits 0 with claim_bearing=false (Linux)
#   * `serve --pi` exits 2 (production refusal)
#   * tbound-doctor --json emits a parsable report
#   * uninstall removes the prefix
#
# It never touches the real user prefix/HOME and needs no network.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-verify}"
BASE="${TBOUND_VERIFY_ROOT:-$(mktemp -d "${TMPDIR:-/tmp}/tbound-verify.XXXXXX")}"
mkdir -p "$BASE"; chmod 700 "$BASE" 2>/dev/null || true
DIST="${TBOUND_VERIFY_DIST:-$BASE/dist}"
if [ -n "${TBOUND_VERIFY_DIST:-}" ]; then
  case "$DIST" in
    /*) ;;
    *) DIST="$ROOT/$DIST" ;;
  esac
fi
PREFIX="$BASE/prefix"
export XDG_CONFIG_HOME="$BASE/config"
export XDG_DATA_HOME="$BASE/data"
export GO="${GO:-go}"

[ "$(uname -s)/$(uname -m)" = Linux/x86_64 ] || { echo "verify-install supports Linux x86_64 only"; exit 1; }
OS=linux; ARCH=amd64

pass=0; fail=0
ok()   { printf '  PASS  %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  FAIL  %s\n' "$1"; fail=$((fail+1)); }
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}';
  else echo "verify-install: need sha256sum or shasum" >&2; return 1; fi
}

echo "verify-install: base=$BASE os=$OS arch=$ARCH"

if [ -n "${TBOUND_VERIFY_DIST:-}" ]; then
  echo "[1/6] use prebuilt release"
else
  echo "[1/6] build release"
  "$ROOT/scripts/build-release.sh" "$VERSION" "$DIST" "$OS/$ARCH" >/dev/null
fi
[ -f "$DIST/tbound-$VERSION-$OS-$ARCH.tar.gz" ] && ok "artifact available" || bad "artifact missing"

echo "[2/6] install into throwaway prefix"
TBOUND_PREFIX="$PREFIX" "$ROOT/install/install.sh" --version "$VERSION" --local-dist "$DIST" --no-path >/dev/null
[ -x "$PREFIX/bin/tbound" ] && ok "bin/tbound installed" || bad "bin/tbound missing"
[ -x "$PREFIX/bin/tbound-doctor" ] && ok "bin/tbound-doctor installed" || bad "bin/tbound-doctor missing"
[ -x "$PREFIX/bin/tbound-profile" ] && ok "bin/tbound-profile installed" || bad "bin/tbound-profile missing"

STUB="$BASE/uname-stub"; mkdir "$STUB"
for platform in "Darwin x86_64" "Linux aarch64"; do
  printf '#!/bin/sh\ncase "$1" in -s) echo %s ;; -m) echo %s ;; *) exit 1 ;; esac\n' $platform > "$STUB/uname"
  chmod +x "$STUB/uname"
  set +e
  out="$(PATH="$STUB:$PATH" TBOUND_PREFIX="$BASE/unsupported" "$ROOT/install/install.sh" --version "$VERSION" --local-dist "$DIST" --no-path 2>&1)"; rc=$?
  set -e
  if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -Fq "unsupported platform '$platform'" && [ ! -e "$BASE/unsupported" ]; then
    ok "$platform refused before any prefix work"
  else
    bad "$platform not refused as unsupported (rc=$rc): $out"
  fi
done

echo "[3/6] reject incomplete updates and preserve installed binaries"
ARTIFACT="tbound-$VERSION-$OS-$ARCH.tar.gz"
for missing in tbound-doctor tbound-profile; do
  FIXTURE="$(mktemp -d "$BASE/incomplete-$missing.XXXXXX")"
  BAD_STAGE="$FIXTURE/extracted"; BAD_DIST="$FIXTURE/dist"
  mkdir "$BAD_STAGE" "$BAD_DIST"
  tar -xzf "$DIST/$ARTIFACT" -C "$BAD_STAGE"
  rm "$BAD_STAGE/bin/$missing"
  ( cd "$BAD_STAGE"
    find . -type f ! -name MANIFEST.sha256 | LC_ALL=C sort | while IFS= read -r rel; do
      printf '%s  %s\n' "$(sha256_of "$rel")" "$rel"
    done
  ) > "$BAD_STAGE/MANIFEST.sha256"
  tar -C "$BAD_STAGE" -czf "$BAD_DIST/$ARTIFACT" .
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$BAD_DIST" && sha256sum "$ARTIFACT" > SHA256SUMS)
  else
    (cd "$BAD_DIST" && shasum -a 256 "$ARTIFACT" > SHA256SUMS)
  fi
  for bin in tbound tbound-doctor tbound-profile; do
    cp "$PREFIX/bin/$bin" "$FIXTURE/$bin.before"
  done
  cp "$PREFIX/.tbound-install" "$FIXTURE/marker.before"
  UPDATE_LOG="$FIXTURE/update.log"
  set +e
  TBOUND_PREFIX="$PREFIX" "$ROOT/install/install.sh" update --version "$VERSION" --local-dist "$BAD_DIST" --no-path >"$UPDATE_LOG" 2>&1
  update_rc=$?
  set -e
  expected_error="tbound-install: error: extracted archive has no bin/$missing"
  if [ "$update_rc" -ne 0 ] \
    && grep -Fqx "$expected_error" "$UPDATE_LOG" \
    && cmp -s "$PREFIX/bin/tbound" "$FIXTURE/tbound.before" \
    && cmp -s "$PREFIX/bin/tbound-doctor" "$FIXTURE/tbound-doctor.before" \
    && cmp -s "$PREFIX/bin/tbound-profile" "$FIXTURE/tbound-profile.before" \
    && cmp -s "$PREFIX/.tbound-install" "$FIXTURE/marker.before"; then
    ok "incomplete update missing $missing refused and prior install preserved"
  else
    bad "incomplete update missing $missing was accepted or prior install changed"
  fi
done

echo "[4/6] runtime contract"
if [ "$OS" = "linux" ]; then
  set +e
  err="$BASE/native-fixture.err"
  out="$("$PREFIX/bin/tbound" serve --pi --native-fixture 2>"$err")"; rc=$?
  set -e
  if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q '"claim_bearing":false'; then
    ok "fixture exit 0 and claim_bearing=false"
  else
    bad "fixture contract failed rc=$rc"
    sed -n '1,20p' "$err" >&2 || true
  fi
fi
set +e
"$PREFIX/bin/tbound" serve --pi >/dev/null 2>&1; rc=$?
set -e
[ "$rc" -eq 2 ] && ok "serve --pi refuses (exit 2)" || bad "serve --pi expected exit 2, got $rc"

echo "[5/6] doctor report"
set +e
docjson="$(TBOUND_PREFIX="$PREFIX" "$PREFIX/bin/tbound-doctor" --json 2>/dev/null)"; drc=$?
set -e
if printf '%s' "$docjson" | grep -q '"dev_ready"'; then
  ok "doctor emitted JSON report (exit $drc)"
else
  bad "doctor JSON report not produced"
fi

echo "[6/6] uninstall"
TBOUND_PREFIX="$PREFIX" "$ROOT/install/install.sh" uninstall >/dev/null
[ ! -d "$PREFIX" ] && ok "prefix removed" || bad "prefix still present"

echo
echo "verify-install: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
