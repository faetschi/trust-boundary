#!/usr/bin/env bash
# Run the audited tbound-profile cgroup-reuse check exactly once.
#
# This helper is the explicit opt-in qualification step for the VM setup paths.
# It is never called by the default install. The check must run as the invoking
# nonroot user inside an existing writable delegated cgroup v2 domain; this
# helper creates that domain with systemd-run --user --scope -p Delegate=yes.
#
# Usage:
#   run-cgroup-reuse-check.sh --tbound-profile PATH --out DIR [--no-scope]
#
# --no-scope is for mock tests only; it is not a qualification result.
#
# The probe contract:
#   tbound-profile cgroup-reuse-check --confirm-disposable-cgroup
#   stdout JSON result=pass|incompatible|unverified, advisory=true, parent,
#   observations, reuse_populated, error; exit 0 pass, exit 2 incompatible/
#   unverified/cleanup. It must not be retried.
set -uo pipefail

PROFILE=""
OUT=""
NO_SCOPE=0
while (($# > 0)); do
  case "$1" in
    --tbound-profile) PROFILE="${2:?--tbound-profile needs a value}"; shift 2 ;;
    --out) OUT="${2:?--out needs a value}"; shift 2 ;;
    --no-scope) NO_SCOPE=1; shift ;;
    -h|--help)
      sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) printf 'ERROR: unknown option: %s\n' "$1" >&2; exit 2 ;;
  esac
done

[[ -n "$PROFILE" && -x "$PROFILE" ]] || { printf 'ERROR: --tbound-profile is not executable: %s\n' "$PROFILE" >&2; exit 2; }
[[ -n "$OUT" ]] || { printf 'ERROR: --out is required\n' >&2; exit 2; }

umask 077
install -d -m 700 "$OUT" || exit 2
OUT="$(cd "$OUT" && pwd)" || exit 2
: > "$OUT/setup-error.txt"

if ! "$PROFILE" cgroup-reuse-check --help >/dev/null 2>&1; then
  printf 'tbound-profile does not expose cgroup-reuse-check\n' > "$OUT/setup-error.txt"
  printf 'ERROR: %s\n' "$(cat "$OUT/setup-error.txt")" >&2
  exit 2
fi

cat > "$OUT/run-check.sh" <<'INNER'
#!/usr/bin/env bash
set -uo pipefail
OUT=$1
PROFILE=$2
umask 077
{
  uname -a
  id
  echo "groups=$(id -G)"
  echo "umask=$(umask)"
  cat /proc/self/cgroup
  grep -E 'Cap(Eff|Prm|Bnd|Inh|Amb)|NoNewPrivs' /proc/self/status
} > "$OUT/identity.txt" 2>&1
printf '%s cgroup-reuse-check --confirm-disposable-cgroup\n' "$PROFILE" > "$OUT/argv.txt"
"$PROFILE" cgroup-reuse-check --confirm-disposable-cgroup > "$OUT/result.json" 2> "$OUT/stderr"
probe_rc=$?
printf '%s\n' "$probe_rc" > "$OUT/probe-rc.txt"
printf '%s\n' "$probe_rc" > "$OUT/rc"
# Keep the scope/transport exit separate from the probe exit.
exit 0
INNER
chmod 700 "$OUT/run-check.sh"

if [[ "$NO_SCOPE" == 1 ]]; then
  bash "$OUT/run-check.sh" "$OUT" "$PROFILE"
  scope_rc=$?
else
  export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
  export DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=$XDG_RUNTIME_DIR/bus}"
  if ! command -v systemd-run >/dev/null 2>&1; then
    printf 'systemd-run is unavailable; a writable delegated user scope is required\n' > "$OUT/setup-error.txt"
    exit 2
  fi
  if ! systemctl --user is-system-running >/dev/null 2>&1; then
    printf 'user systemd manager is unavailable; cannot create a delegated scope\n' > "$OUT/setup-error.txt"
    exit 2
  fi
  systemd-run --user --scope -p Delegate=yes \
    --unit="tbound-cgroup-reuse-$$" -- \
    bash "$OUT/run-check.sh" "$OUT" "$PROFILE" > "$OUT/systemd-run.log" 2>&1
  scope_rc=$?
fi
printf '%s\n' "$scope_rc" > "$OUT/scope-rc.txt"

if [[ -s "$OUT/result.json" ]]; then
  cat "$OUT/result.json"
fi
if [[ -s "$OUT/stderr" ]]; then
  cat "$OUT/stderr" >&2
fi
if [[ -s "$OUT/probe-rc.txt" ]]; then
  # Preserve the probe exit exactly; scope-rc.txt records transport separately.
  exit "$(cat "$OUT/probe-rc.txt")"
fi
if [[ "$scope_rc" -ne 0 ]]; then
  exit 2
fi
printf 'probe produced no rc; qualification is unverified\n' >&2
exit 2
