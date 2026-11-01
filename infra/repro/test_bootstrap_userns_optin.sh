#!/usr/bin/env bash
# Regression test for the Step 4 user namespace policy change in bootstrap-linux.sh.
#
# TBOUND_ASSUME_YES accepts the whole plan, so an unattended run must not change
# this host policy unless TBOUND_APPLY_USERNS_SYSCTL=1 is also set.
# Runs the bootstrap's real Step 4 against a sysctl stub and a scratch config path.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP="$ROOT/bootstrap-linux.sh"
[[ -r "$BOOTSTRAP" ]] || { echo "bootstrap script not readable" >&2; exit 2; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-userns-optin.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"
cat > "$WORK/bin/sysctl" <<'SH'
#!/bin/bash
# Stub: -n prints the value in $SYSCTL_STATE, -w stores the new value and logs the write.
if [[ "$1" == "-w" ]]; then
  printf '%s\n' "$2" >> "$SYSCTL_WRITES"
  printf '%s\n' "${2#*=}" > "$SYSCTL_STATE"
  exit 0
fi
cat "$SYSCTL_STATE"
SH
chmod +x "$WORK/bin/sysctl"

# Extract the code under test from the real script (not a copy of it).
{
  sed -n '/^say()/p;/^die()/p' "$BOOTSTRAP"
  sed -n '/^confirm()/,/^}/p' "$BOOTSTRAP"
  sed -n "/^SYSCTL_KEY=/,/^# Step 5/p" "$BOOTSTRAP" \
    | sed "s|^SYSCTL_FILE=.*|SYSCTL_FILE=\"\$SYSCTL_CONFIG\"|;/^# /d"
} > "$WORK/step4.sh"
grep -q 'TBOUND_APPLY_USERNS_SYSCTL\|sysctl -w' "$WORK/step4.sh" || { echo "could not find Step 4 in $BOOTSTRAP" >&2; exit 2; }

pass=0
fail=0
# check <name> <starting value> <opt-in> <want exit zero: yes|no> <want policy written: yes|no>
check() {
  local name=$1 start=$2 opt_in=$3 want_ok=$4 want_written=$5 ok=yes written=no
  printf '%s\n' "$start" > "$WORK/state"
  : > "$WORK/writes"
  rm -f "$WORK/config"
  PATH="$WORK/bin:$PATH" SYSCTL_STATE="$WORK/state" SYSCTL_WRITES="$WORK/writes" SYSCTL_CONFIG="$WORK/config" \
    TBOUND_ASSUME_YES=1 TBOUND_APPLY_USERNS_SYSCTL="$opt_in" \
    bash -c "set -euo pipefail; source '$WORK/step4.sh'" > "$WORK/out" 2>&1 || ok=no
  if [[ -s "$WORK/writes" || -e "$WORK/config" ]]; then written=yes; fi
  if [[ "$ok" == "$want_ok" && "$written" == "$want_written" ]]; then
    printf 'PASS  %s\n' "$name"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s: exit zero=%s policy written=%s; want %s and %s\n' "$name" "$ok" "$written" "$want_ok" "$want_written" >&2
    sed 's/^/      /' "$WORK/out" >&2
    fail=$((fail + 1))
  fi
}

check 'unattended without the opt-in stops before changing policy' 1 0 no no
check 'unattended with the opt-in changes policy' 1 1 yes yes
check 'an unrestricted host needs no opt-in' 0 0 yes no

printf '\nuser namespace opt-in checks: %d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
