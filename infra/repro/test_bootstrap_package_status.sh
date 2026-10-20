#!/usr/bin/env bash
# Regression test for the bootstrap's held-package detection.
#
# The guarded provisioner holds runtime/build packages, so dpkg reports "hi"
# (hold + installed) instead of "ii". A second bootstrap run must accept both
# and reject only packages that are not installed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP="$ROOT/bootstrap-linux.sh"
[[ -r "$BOOTSTRAP" ]] || { echo "bootstrap-linux.sh not readable" >&2; exit 2; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-pkg-status.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"

cat > "$WORK/bin/dpkg-query" <<'SH'
#!/usr/bin/env bash
case "${FAKE_DPKG_STATUS:-missing}" in
  missing) exit 1 ;;
  *) printf '%s' "$FAKE_DPKG_STATUS" ;;
esac
SH
chmod +x "$WORK/bin/dpkg-query"

# Load the function under test without executing the bootstrap body.
eval "$(sed -n '/^package_installed()/,/^}/p' "$BOOTSTRAP")"
[[ "$(type -t package_installed)" == function ]] || {
  echo "could not load package_installed from $BOOTSTRAP" >&2
  exit 2
}

export PATH="$WORK/bin:$PATH"
pass=0
fail=0
expect() {
  local status=$1 want=$2
  export FAKE_DPKG_STATUS="$status"
  if package_installed example; then
    got=installed
  else
    got=missing
  fi
  if [[ "$got" == "$want" ]]; then
    printf 'PASS  dpkg status %-8s -> %s\n' "$status" "$got"
    pass=$((pass + 1))
  else
    printf 'FAIL  dpkg status %-8s -> %s (want %s)\n' "$status" "$got" "$want" >&2
    fail=$((fail + 1))
  fi
}

expect ii installed
expect hi installed
expect un missing
expect rc missing
expect missing missing

printf '\npackage-status checks: %d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
