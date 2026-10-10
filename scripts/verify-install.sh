#!/usr/bin/env bash
# End-to-end verification of the tbound install flow, entirely offline.
#
# It builds a local release, installs it into a throwaway private prefix, and
# asserts the exact runtime contract:
#   * bin/tbound and bin/tbound-doctor exist and run
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
DIST="$BASE/dist"; PREFIX="$BASE/prefix"
export XDG_CONFIG_HOME="$BASE/config"
export XDG_DATA_HOME="$BASE/data"
export GO="${GO:-go}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) echo "unsupported arch"; exit 1 ;; esac
case "$OS" in linux|darwin) ;; *) echo "verify-install supports linux/darwin"; exit 1 ;; esac

pass=0; fail=0
ok()   { printf '  PASS  %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  FAIL  %s\n' "$1"; fail=$((fail+1)); }

echo "verify-install: base=$BASE os=$OS arch=$ARCH"

echo "[1/5] build release"
"$ROOT/scripts/build-release.sh" "$VERSION" "$DIST" "$OS/$ARCH" >/dev/null
[ -f "$DIST/tbound-$VERSION-$OS-$ARCH.tar.gz" ] && ok "artifact built" || bad "artifact missing"

echo "[2/5] install into throwaway prefix"
TBOUND_PREFIX="$PREFIX" "$ROOT/install/install.sh" --version "$VERSION" --local-dist "$DIST" --no-path >/dev/null
[ -x "$PREFIX/bin/tbound" ] && ok "bin/tbound installed" || bad "bin/tbound missing"
[ -x "$PREFIX/bin/tbound-doctor" ] && ok "bin/tbound-doctor installed" || bad "bin/tbound-doctor missing"

echo "[3/5] runtime contract"
if [ "$OS" = "linux" ]; then
  set +e
  out="$("$PREFIX/bin/tbound" serve --pi --native-fixture 2>/dev/null)"; rc=$?
  set -e
  if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q '"claim_bearing":false'; then
    ok "fixture exit 0 and claim_bearing=false"
  else
    bad "fixture contract failed rc=$rc"
  fi
fi
set +e
"$PREFIX/bin/tbound" serve --pi >/dev/null 2>&1; rc=$?
set -e
[ "$rc" -eq 2 ] && ok "serve --pi refuses (exit 2)" || bad "serve --pi expected exit 2, got $rc"

echo "[4/5] doctor report"
set +e
docjson="$(TBOUND_PREFIX="$PREFIX" "$PREFIX/bin/tbound-doctor" --json 2>/dev/null)"; drc=$?
set -e
if printf '%s' "$docjson" | grep -q '"dev_ready"'; then
  ok "doctor emitted JSON report (exit $drc)"
else
  bad "doctor JSON report not produced"
fi

echo "[5/5] uninstall"
TBOUND_PREFIX="$PREFIX" "$ROOT/install/install.sh" uninstall >/dev/null
[ ! -d "$PREFIX" ] && ok "prefix removed" || bad "prefix still present"

echo
echo "verify-install: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
