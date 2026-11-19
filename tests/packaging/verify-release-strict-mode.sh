#!/usr/bin/env bash
# Controlled regression for verify-release-layout acceptance semantics.
# A fake Go wrapper injects a deterministic Linux supervisor-source compile
# failure. No project source is
# changed; all fake artifacts and logs are under a private temp directory.
# Diagnostic mode must report SKIP/exit 0; release acceptance must fail.
set -euo pipefail
unset TBOUND_REQUIRE_LINUX_RELEASE || true

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="strict-fixture"
if [ -z "${TMPDIR:-}" ] || [ ! -d "$TMPDIR" ] || [ "$(stat -c '%a' "$TMPDIR" 2>/dev/null || true)" != 700 ]; then
  echo "verify-release-strict-mode: set TMPDIR to an existing private mode-0700 directory" >&2
  exit 2
fi
BASE="$(mktemp -d "$TMPDIR/tbound-strict-mode.XXXXXX")"
chmod 700 "$BASE"
trap 'rm -rf "$BASE"' EXIT

FAKE_GO="$BASE/fake-go"
cat > "$FAKE_GO" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "version" ]; then
  echo 'go version go1.27.1 linux/amd64 (strict-mode-fixture)'
  exit 0
fi
if [ "${1:-}" != "build" ]; then
  echo "unexpected fake go arguments: $*" >&2
  exit 90
fi
if [ "${GOOS:-}" = "linux" ]; then
  echo '# tbound/supervisor/internal/piruntime' >&2
  echo 'controlled strict-mode fixture: Linux candidate source does not compile' >&2
  exit 1
fi
out=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="${1:?missing -o target}"
  fi
  shift
done
[ -n "$out" ] || { echo 'fake go: missing -o target' >&2; exit 91; }
mkdir -p "$(dirname "$out")"
printf 'synthetic placeholder; never executable product code\n' > "$out"
FAKE
chmod 700 "$FAKE_GO"

echo "verify-release-strict-mode: controlled fake Linux source failure"
if GO="$FAKE_GO" bash "$ROOT/tests/packaging/verify-release-layout.sh" "$VERSION" >"$BASE/diagnostic.log" 2>&1; then
  if grep -q 'SKIP  linux/amd64 build blocked' "$BASE/diagnostic.log"; then
    echo "  PASS  diagnostic mode reports the Linux candidate as skipped and exits 0"
  else
    echo "  FAIL  diagnostic mode did not report the controlled Linux build skip" >&2
    cat "$BASE/diagnostic.log" >&2
    exit 1
  fi
else
  echo "  FAIL  diagnostic mode unexpectedly failed" >&2
  cat "$BASE/diagnostic.log" >&2
  exit 1
fi

set +e
GO="$FAKE_GO" bash "$ROOT/tests/packaging/verify-release-layout.sh" "$VERSION" --require-linux >"$BASE/strict-flag.log" 2>&1
flag_rc=$?
TBOUND_REQUIRE_LINUX_RELEASE=1 GO="$FAKE_GO" bash "$ROOT/tests/packaging/verify-release-layout.sh" "$VERSION" >"$BASE/strict-env.log" 2>&1
env_rc=$?
set -e
if [ "$flag_rc" -ne 0 ] && grep -q 'FAIL  required check skipped: linux/amd64 build blocked' "$BASE/strict-flag.log"; then
  echo "  PASS  --require-linux fails the controlled Linux source compile failure"
else
  echo "  FAIL  --require-linux did not fail closed (exit=$flag_rc)" >&2
  cat "$BASE/strict-flag.log" >&2
  exit 1
fi
if [ "$env_rc" -ne 0 ] && grep -q 'FAIL  required check skipped: linux/amd64 build blocked' "$BASE/strict-env.log"; then
  echo "  PASS  TBOUND_REQUIRE_LINUX_RELEASE=1 fails the controlled Linux source compile failure"
else
  echo "  FAIL  strict environment mode did not fail closed (exit=$env_rc)" >&2
  cat "$BASE/strict-env.log" >&2
  exit 1
fi

echo "verify-release-strict-mode: 3 passed, 0 failed"
