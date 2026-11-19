#!/bin/bash
# test-recover.sh: local test of the decisions recover.sh takes from files, with synthetic inputs. No guest contact.
# Which request is the one UNKNOWN to abandon (unknown_request), and which plan index ended UNKNOWN with how many governed
# units in a row (unknown_unit), whether that streak stops the round (streak_stop), and the runner's arguments (runner_args). Runs in WSL, on Linux or in Git Bash; PYTHON defaults to python3 where it exists.
# Prints PASS/FAIL per case and exits non-zero on any FAIL.
set -euo pipefail
umask 077
command -v python3 > /dev/null && export PYTHON=${PYTHON:-python3}
RECOVER_LIB=1 . "$(dirname -- "${BASH_SOURCE[0]}")/recover.sh"
T=$(mktemp -d "${TMPDIR:-/tmp}/recover-test.XXXXXX")
case $T in "${TMPDIR:-/tmp}"/recover-test.*) ;; *) echo "FATAL: unexpected temp dir '$T'" >&2; exit 2 ;; esac
trap 'rm -rf -- "$T"' EXIT
FAILS=0
check() { if [ "$2" = "$3" ]; then printf 'PASS %s\n' "$1"; else printf 'FAIL %s: got %q, want %q\n' "$1" "$2" "$3"; FAILS=$((FAILS + 1)); fi; }
rec() { # rec <kind> <request id> [status]: one serve-pi-requests.jsonl record
  local data
  data=$(printf '{"request_id":"%s","response":{"status":"%s"}}' "$2" "${3:-}" | base64 | tr -d '\n')
  printf '{"event":{"kind":"%s","data":"%s"}}\n' "$1" "$data"
}
{
  rec pi_service_request_accepted tbr-done; rec pi_service_request_terminal tbr-done completed
  rec pi_service_request_accepted tbr-old; rec pi_service_request_terminal tbr-old unknown; rec pi_service_request_abandoned tbr-old
  rec pi_service_request_accepted tbr-new; rec pi_service_request_terminal tbr-new unknown
} > "$T/one.jsonl"
check "the one unabandoned UNKNOWN is chosen" "$(unknown_request "$T/one.jsonl")" tbr-new
{ cat "$T/one.jsonl"; rec pi_service_request_accepted tbr-open; } > "$T/two.jsonl"
out=$(unknown_request "$T/two.jsonl")
check "an open request beside it stops" "${out%%:*}" NONE
U='{"kind":"done","step_id":"%s","index":%d,"exec_error":"runner: governed run outcome is unknown: task-outcome-unknown"}\n'
K='{"kind":"done","step_id":"%s","index":%d}\n'
printf "$K$U$K" "a|trajectory|C|0" 61 "b|trajectory|C|0" 62 "c|compliant|V|0" 63 > "$T/j1.jsonl"
check "the UNKNOWN unit, not the V unit after it" "$(unknown_unit "$T/j1.jsonl")" "62 1"
printf "$U$U$K" "a|trajectory|C|0" 10 "b|static-replay|P|0" 11 "c|compliant|V|0" 12 > "$T/j2.jsonl"
check "two governed UNKNOWNs in a row, V skipped" "$(unknown_unit "$T/j2.jsonl")" "11 2"
printf "$K" "a|trajectory|C|0" 1 > "$T/j3.jsonl"
check "no UNKNOWN" "$(unknown_unit "$T/j3.jsonl")" "-1 0"
check "two governed UNKNOWNs in a row do not stop" "$(streak_stop 2)" ""
check "three governed UNKNOWNs in a row stop" "$(streak_stop 3 | cut -d: -f1)" systemic
printf 'not json\n' > "$T/j4.jsonl"
read -r idx streak < <(unknown_unit "$T/j4.jsonl" 2> /dev/null) || true
check "a journal it cannot read stops" "$(streak_stop "$streak" | cut -d' ' -f1-4)" "cannot read the UNKNOWN"
mkdir "$T/round"
check "a round without a registration record runs unregistered" "$(runner_args "$T/round")" "run --config run.json"
: > "$T/round/registration.json"
check "a registered round passes its record to every chunk" "$(runner_args "$T/round")" "run --config run.json --registration registration.json"
[ "$FAILS" -eq 0 ] || exit 1
