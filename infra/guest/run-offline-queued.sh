#!/usr/bin/env bash
set -Eeuo pipefail
export PATH=/usr/bin:/bin

POLL_SECONDS=5
MAX_WAIT_SECONDS=600
STATUS=STARTING
OUTPUT_DIR=
launcher_sha256=
verifier_sha256=

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
    /usr/bin/python3 -I - "$OUTPUT_DIR" "$launcher_path" "$runner_path" "$exit_status" \
      "${launcher_sha256:-}" "${verifier_sha256:-}" <<'PY' 2>/dev/null || true
import datetime
import hashlib
import json
import os
import stat
import sys
from pathlib import Path

root = Path(sys.argv[1])
launcher = Path(sys.argv[2])
verifier = Path(sys.argv[3])
launcher_exit = int(sys.argv[4])
expected_launcher_hash = sys.argv[5]
expected_verifier_hash = sys.argv[6]
if root.is_symlink() or not root.is_dir() or stat.S_IMODE(root.lstat().st_mode) != 0o700:
    raise SystemExit(1)

def regular_hash(path):
    if path.is_symlink() or not path.is_file():
        return None
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()

status_path = root / "status"
try:
    status = status_path.read_text(encoding="utf-8").strip()
except OSError:
    status = "MISSING"

artifacts = {}
for name in (
    "status", "launcher.exit-code", "runner.exit-code", "launcher.log",
    "verification.json", "verification.stderr",
):
    digest = regular_hash(root / name)
    if digest is not None:
        artifacts[name] = digest

report_status = None
report_path = root / "verification.json"
if regular_hash(report_path) is not None:
    try:
        report_status = json.loads(report_path.read_text(encoding="utf-8")).get("status")
    except (OSError, UnicodeError, json.JSONDecodeError):
        report_status = "INVALID"

try:
    runner_exit = int((root / "runner.exit-code").read_text(encoding="ascii").strip())
except (OSError, ValueError, UnicodeError):
    runner_exit = None

actual_source_hashes = {
    "run-offline-queued.sh": regular_hash(launcher),
    "verify-offline.py": regular_hash(verifier),
}
expected_source_hashes = {
    "run-offline-queued.sh": expected_launcher_hash,
    "verify-offline.py": expected_verifier_hash,
}
source_unchanged = (
    all(len(value) == 64 and all(char in "0123456789abcdef" for char in value)
        for value in expected_source_hashes.values())
    and actual_source_hashes == expected_source_hashes
)

completion = {
    "schema": "tbound-offline-queued-completion/v1",
    "completed_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "run_directory": str(root),
    "status": status,
    "launcher_exit_code": launcher_exit,
    "runner_exit_code": runner_exit,
    "verification_status": report_status,
    "pass": status == "PASS" and launcher_exit == 0 and runner_exit == 0 and report_status == "PASS" and source_unchanged,
    "source_sha256": expected_source_hashes,
    "source_unchanged_at_completion": source_unchanged,
    "source_sha256_at_completion": actual_source_hashes,
    "artifact_sha256": artifacts,
}
if None in completion["source_sha256"].values() or "status" not in artifacts:
    raise SystemExit(1)

temporary = root / "completion.json.tmp"
destination = root / "completion.json"
if temporary.exists() or destination.exists():
    raise SystemExit(1)
with temporary.open("x", encoding="utf-8", newline="\n") as output:
    json.dump(completion, output, sort_keys=True, separators=(",", ":"))
    output.write("\n")
    output.flush()
    os.fsync(output.fileno())
os.replace(temporary, destination)
directory_fd = os.open(root, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
try:
    os.fsync(directory_fd)
finally:
    os.close(directory_fd)

completed_tmp = root / "completed.tmp"
completed = root / "completed"
if completed_tmp.exists() or completed.exists():
    raise SystemExit(1)
with completed_tmp.open("x", encoding="ascii", newline="\n") as output:
    output.write(completion["completed_at_utc"] + "\n")
    output.flush()
    os.fsync(output.fileno())
os.replace(completed_tmp, completed)
directory_fd = os.open(root, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
try:
    os.fsync(directory_fd)
finally:
    os.close(directory_fd)
PY
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
launcher_hash_output=$(/usr/bin/sha256sum -- "$launcher_path") || fail "could not hash the queued launcher"
launcher_sha256=${launcher_hash_output%% *}
verifier_hash_output=$(/usr/bin/sha256sum -- "$runner_path") || fail "could not hash verify-offline.py"
verifier_sha256=${verifier_hash_output%% *}
if [[ ! $launcher_sha256 =~ ^[a-f0-9]{64}$ || ! $verifier_sha256 =~ ^[a-f0-9]{64}$ ]]; then
  fail "source SHA-256 output was malformed"
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
printf '%s\n' "$$" >"$OUTPUT_DIR/launcher.pid"
printf '%s\n' 'WAITING_FOR_OFFLINE' >"$OUTPUT_DIR/status"
printf 'TBOUND_RUN_PATH=%s\n' "$OUTPUT_DIR"

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
log "guest offline gate observed; rechecking queued source hashes"
current_launcher_hash=$(/usr/bin/sha256sum -- "$launcher_path") || {
  STATUS=SOURCE_UNREADABLE
  log "could not recheck queued launcher bytes; refusing to start the verifier"
  exit 1
}
current_launcher_hash=${current_launcher_hash%% *}
current_verifier_hash=$(/usr/bin/sha256sum -- "$runner_path") || {
  STATUS=SOURCE_UNREADABLE
  log "could not recheck verifier bytes; refusing to start the verifier"
  exit 1
}
if [[ $current_launcher_hash != "$launcher_sha256" || $current_verifier_hash != "$verifier_sha256" ]]; then
  STATUS=SOURCE_CHANGED
  log "queued source bytes changed while waiting; refusing to start the verifier"
  exit 1
fi
log "queued source hashes match; starting the verifier as the trial user"
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
