#!/usr/bin/env bash
# Mock regression tests for the explicit cgroup-reuse qualification wrapper.
# No cgroups, VMs, probes, or real systemd scopes are touched.
#
# Red/green:
#   HELPER_UNDER_TEST=/path/to/old/helper.sh bash test_cgroup_reuse_check.sh
#   bash test_cgroup_reuse_check.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HELPER="${HELPER_UNDER_TEST:-$ROOT/run-cgroup-reuse-check.sh}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-cq-mock.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
BIN="$WORK/bin"
mkdir -m 700 "$BIN" "$WORK/cases"
PASS=0
FAIL=0
LAST_RC=0

fail() { printf 'FAIL: %s\n' "$*" >&2; FAIL=$((FAIL + 1)); }
ok()   { printf 'PASS: %s\n' "$*"; PASS=$((PASS + 1)); }

write_profile() {
  local mode=$1
  cat > "$BIN/tbound-profile" <<SH
#!/usr/bin/env bash
case "\${1:-}" in
  cgroup-reuse-check)
    case "\${2:-}" in
      --help) exit 0 ;;
      --confirm-disposable-cgroup)
        touch "$WORK/profile-invoked"
        if [ "$mode" = incompatible ]; then
          printf '%s\\n' '{"result":"incompatible","advisory":true,"error":"mock kernel mismatch"}'
          exit 2
        fi
        printf '%s\\n' '{"result":"pass","advisory":true}'
        exit 0
        ;;
    esac
    ;;
esac
exit 2
SH
  chmod 700 "$BIN/tbound-profile"
  rm -f "$WORK/profile-invoked"
}

write_systemd_run() {
  local mode=$1
  case "$mode" in
    fail-no-invoke)
      printf '#!/usr/bin/env bash\nexit 17\n' > "$BIN/systemd-run"
      ;;
    run-then-17)
      cat > "$BIN/systemd-run" <<'SH'
#!/usr/bin/env bash
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done
shift
"$@"
exit 17
SH
      ;;
    run-ok)
      cat > "$BIN/systemd-run" <<'SH'
#!/usr/bin/env bash
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done
shift
"$@"
exit 0
SH
      ;;
  esac
  chmod 700 "$BIN/systemd-run"
}

write_systemctl() {
  local mode=$1
  case "$mode" in
    healthy)
      printf '#!/usr/bin/env bash\nexit 0\n' > "$BIN/systemctl"
      ;;
    degraded)
      cat > "$BIN/systemctl" <<'SH'
#!/usr/bin/env bash
case "$*" in
  *show-environment*) exit 0 ;;
  *is-system-running*) exit 1 ;;
  *) exit 0 ;;
esac
SH
      ;;
    unreachable)
      printf '#!/usr/bin/env bash\nexit 1\n' > "$BIN/systemctl"
      ;;
  esac
  chmod 700 "$BIN/systemctl"
}

run_helper() {
  local out=$1
  shift
  set +e
  PATH="$BIN:$PATH" "$HELPER" --tbound-profile "$BIN/tbound-profile" --out "$out" "$@"     >"$WORK/stdout" 2>"$WORK/stderr"
  LAST_RC=$?
  set -e
}

# 1. Existing incompatible result still returns probe exit 2 in --no-scope mode.
write_profile incompatible
write_systemd_run run-ok
write_systemctl healthy
run_helper "$WORK/cases/incompatible" --no-scope
if [[ "$LAST_RC" -eq 2 && "$(cat "$WORK/cases/incompatible/probe-rc.txt" 2>/dev/null || echo missing)" == 2 \
      && "$(cat "$WORK/cases/incompatible/scope-rc.txt" 2>/dev/null || echo missing)" == 0 ]]; then
  ok "incompatible --no-scope returns probe exit 2"
else
  fail "incompatible --no-scope: rc=$LAST_RC"
fi

# 2. Stale --out must be refused before any write/probe, even if it contains
#    probe-rc=0 and the mocked transport fails 17.
write_profile pass
write_systemd_run fail-no-invoke
write_systemctl healthy
stale="$WORK/cases/stale"
mkdir -m 700 "$stale"
printf '0\n' > "$stale/probe-rc.txt"
printf 'stale-result\n' > "$stale/result.json"
run_helper "$stale"
if [[ "$LAST_RC" -eq 2 && "$(cat "$stale/probe-rc.txt")" == 0 \
      && "$(cat "$stale/result.json")" == stale-result \
      && ! -e "$stale/identity.txt" && ! -e "$WORK/profile-invoked" ]]; then
  ok "stale existing --out refused; no probe invoked; prior evidence intact"
else
  fail "stale --out was reused or overwritten (rc=$LAST_RC)"
fi

# 3. Fresh output, successful probe, then scope/transport exit 17 must be
#    unverified/nonzero, not success from probe-rc=0.
write_profile pass
write_systemd_run run-then-17
write_systemctl healthy
fresh="$WORK/cases/transport"
run_helper "$fresh"
if [[ "$LAST_RC" -eq 2 && "$(cat "$fresh/probe-rc.txt")" == 0 \
      && "$(cat "$fresh/scope-rc.txt")" == 17 \
      && -s "$fresh/result.json" && -s "$fresh/setup-error.txt" ]]; then
  ok "transport failure overrides probe pass and returns nonzero"
else
  fail "transport failure returned rc=$LAST_RC (probe=$(cat "$fresh/probe-rc.txt" 2>/dev/null || echo missing), scope=$(cat "$fresh/scope-rc.txt" 2>/dev/null || echo missing))"
fi

# 4. A reachable degraded user manager is usable for one transient scope.
write_profile pass
write_systemd_run run-ok
write_systemctl degraded
degraded="$WORK/cases/degraded"
run_helper "$degraded"
if [[ "$LAST_RC" -eq 0 && "$(cat "$degraded/probe-rc.txt" 2>/dev/null || echo missing)" == 0 \
      && "$(cat "$degraded/scope-rc.txt" 2>/dev/null || echo missing)" == 0 ]]; then
  ok "reachable degraded manager accepted"
else
  fail "degraded manager rejected or failed (rc=$LAST_RC)"
fi

# 5. Unreachable manager blocks before scope creation and does not invoke probe.
write_profile pass
write_systemd_run run-ok
write_systemctl unreachable
unreachable="$WORK/cases/unreachable"
run_helper "$unreachable"
if [[ "$LAST_RC" -eq 2 && -s "$unreachable/setup-error.txt" \
      && ! -e "$unreachable/result.json" && ! -e "$WORK/profile-invoked" ]]; then
  ok "unreachable manager blocked before probe"
else
  fail "unreachable manager handling wrong (rc=$LAST_RC)"
fi

# 6. --help is mutation-free.
set +e
PATH="$BIN:$PATH" "$HELPER" --help >/dev/null 2>&1
help_rc=$?
set -e
if [[ "$help_rc" -eq 0 ]]; then ok "--help exit 0"; else fail "--help exit $help_rc"; fi

printf '\ncgroup-reuse wrapper mocks: %d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
