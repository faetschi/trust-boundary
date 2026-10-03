#!/usr/bin/env bash
set -Eeuo pipefail
export PATH=/usr/bin:/bin

POLL_SECONDS=5
MAX_WAIT_SECONDS=600
STATUS=STARTING
OUTPUT_DIR=

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

finish() {
  local exit_status=$?
  trap - EXIT
  if [[ -n ${OUTPUT_DIR:-} && -d $OUTPUT_DIR && ! -e $OUTPUT_DIR/completed ]]; then
    printf '%s\n' "${STATUS:-LAUNCHER_FAIL}" >"$OUTPUT_DIR/status" 2>/dev/null || true
    printf '%s\n' "$exit_status" >"$OUTPUT_DIR/launcher.exit-code" 2>/dev/null || true
    date -Is >"$OUTPUT_DIR/completed" 2>/dev/null || true
  fi
}

trap finish EXIT
trap 'STATUS=INTERRUPTED; exit 130' INT
trap 'STATUS=INTERRUPTED; exit 143' HUP TERM

if [[ $# -ne 1 || $1 != --host-adapter-disconnected ]]; then
  fail "usage: bash run-offline-queued.sh --host-adapter-disconnected"
fi
if (( EUID == 0 )); then
  fail "run as the non-root trial account"
fi
if [[ -z ${TMUX:-} || -z ${TMUX_PANE:-} || ! -t 0 || ! -t 1 ]]; then
  fail "start inside an attached tmux pane with a terminal on stdin and stdout"
fi
if [[ ! -x /usr/bin/tmux ]]; then
  fail "/usr/bin/tmux is required"
fi
current_tty=$(tty 2>/dev/null) || fail "could not identify the current terminal"
pane_tty=$(/usr/bin/tmux display-message -p -t "$TMUX_PANE" '#{pane_tty}' 2>/dev/null) || fail "current tmux pane is unavailable"
if [[ $current_tty != "$pane_tty" ]]; then
  fail "the current terminal does not match the tmux pane terminal"
fi

launcher_path=${BASH_SOURCE[0]}
if [[ -L $launcher_path ]]; then
  fail "launcher must not be a symlink"
fi
script_dir=$(cd -- "$(dirname -- "$launcher_path")" && pwd -P) || fail "could not resolve launcher directory"
runner_path=$script_dir/verify-offline.py
if [[ ! -f $runner_path || -L $runner_path ]]; then
  fail "verify-offline.py must be a regular file beside this launcher"
fi
if [[ ! -x /usr/bin/python3 ]]; then
  fail "system Python 3 is required"
fi

if [[ -z ${HOME:-} || $HOME != /* || ! -d $HOME || -L $HOME ]]; then
  fail "HOME must be an existing absolute non-symlink directory"
fi
home_owner=$(stat -c '%u' -- "$HOME") || fail "could not inspect HOME ownership"
home_mode=$(stat -c '%a' -- "$HOME") || fail "could not inspect HOME permissions"
if [[ $home_owner != "$EUID" || ! $home_mode =~ ^[0-7]{3,4}$ ]] || (( (8#$home_mode & 18) != 0 )); then
  fail "HOME must be trial-user-owned and not group- or world-writable"
fi

umask 077
OUTPUT_DIR=$(mktemp -d -- "$HOME/.tbound-offline-queued.XXXXXXXXXX") || fail "could not create private report directory under HOME"
output_owner=$(stat -c '%u' -- "$OUTPUT_DIR") || fail "could not inspect report directory ownership"
output_mode=$(stat -c '%a' -- "$OUTPUT_DIR") || fail "could not inspect report directory permissions"
if [[ $output_owner != "$EUID" || $output_mode != 700 ]]; then
  STATUS=REPORT_DIRECTORY_INVALID
  fail "report directory is not trial-user-owned mode 0700"
fi
printf '%s\n' "$OUTPUT_DIR" >"$OUTPUT_DIR/location"
printf '%s\n' 'WAITING_FOR_OFFLINE' >"$OUTPUT_DIR/status"

log() {
  local line
  line="$(date -Is) $*"
  printf '%s\n' "$line" | tee -a "$OUTPUT_DIR/launcher.log"
}

ip_bin=
for candidate in /usr/sbin/ip /usr/bin/ip /sbin/ip /bin/ip; do
  if [[ -x $candidate ]]; then
    ip_bin=$candidate
    break
  fi
done
if [[ -z $ip_bin ]]; then
  STATUS=IPROUTE2_MISSING
  log "iproute2 is unavailable; refusing to start the verifier"
  exit 1
fi

offline_state() {
  local interface name carrier found=0 connected=0 route4 route6

  [[ -d /sys/class/net ]] || return 2
  for interface in /sys/class/net/*; do
    [[ -e $interface || -L $interface ]] || continue
    name=${interface##*/}
    [[ $name == lo ]] && continue
    found=1
    if [[ ! -r $interface/carrier ]] || ! IFS= read -r carrier <"$interface/carrier"; then
      return 2
    fi
    case $carrier in
      0) ;;
      1) connected=1 ;;
      *) return 2 ;;
    esac
  done
  (( found == 1 )) || return 2

  route4=$("$ip_bin" -4 route show table all) || return 2
  route6=$("$ip_bin" -6 route show table all) || return 2
  if grep -Eq '(^|[[:space:]])default($|[[:space:]])' <<<"$route4" ||
     grep -Eq '(^|[[:space:]])default($|[[:space:]])' <<<"$route6"; then
    connected=1
  fi

  if (( connected == 1 )); then
    return 1
  fi
  return 0
}

log "queued; waiting up to $MAX_WAIT_SECONDS seconds for all guest links to report carrier 0 and both route tables to have no default"
deadline=$((SECONDS + MAX_WAIT_SECONDS))
last_progress=$SECONDS
while true; do
  if offline_state; then
    break
  else
    state=$?
  fi
  if (( state == 2 )); then
    STATUS=OFFLINE_STATE_UNREADABLE
    log "could not verify every carrier and default route; refusing to continue"
    exit 1
  fi
  if (( SECONDS >= deadline )); then
    STATUS=WAIT_TIMEOUT
    log "timed out before the guest reached the offline gate"
    exit 1
  fi
  if (( SECONDS - last_progress >= 60 )); then
    log "still waiting for guest network links to go offline"
    last_progress=$SECONDS
  fi
  sleep "$POLL_SECONDS"
done

STATUS=RUNNING
log "guest offline gate observed; starting the reviewed verifier as the trial user"
if /usr/bin/python3 -I "$runner_path" --host-adapter-disconnected \
    >"$OUTPUT_DIR/verification.json" 2>"$OUTPUT_DIR/verification.stderr"; then
  runner_exit_code=0
else
  runner_exit_code=$?
fi
printf '%s\n' "$runner_exit_code" >"$OUTPUT_DIR/runner.exit-code"
if (( runner_exit_code == 0 )); then
  STATUS=PASS
  log "verifier completed successfully"
else
  STATUS=FAIL
  log "verifier failed with exit code $runner_exit_code; see verification.json and verification.stderr"
fi
printf '%s\n' "$STATUS" >"$OUTPUT_DIR/status"
exit "$runner_exit_code"
