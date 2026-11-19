#!/bin/bash
# recover.sh <round dir>: detached supervisor of a runner round (Git Bash on the owner machine). It starts the runner
# (chunk-01) when the round has no chunk yet, then waits for each runner process to exit. When the runner stopped because
# an earlier request left an UNKNOWN terminal (the service refuses the next begin: "is not terminal"), it previews the
# abandon on the guest, confirms it ONLY when every fact of the preview is clean, starts the guest service again, runs the
# runbook leftover-container check (must print nothing), keeps diagnostics in <round>/diag/auto-<time>/ and starts the
# runner again as the next chunk. It stops, for the operator, on: an exit it does not recognise, more or less than one
# UNKNOWN request, a cause that looks like a provider outage, an unclean preview, a failed confirm, a service that is not
# active, a leftover container, 3 UNKNOWN governed units in a row (systemic) or a journal it cannot count them in, or MAX_CYCLES
# recoveries.
#
# Round layout (as the screening rounds keep it): <round>/run.json (its run_dir holds journal.jsonl), <round>/bin/tbound-run.exe,
# <round>/registration.json (optional: the pre-registration record of a registered run; every chunk passes it as --registration),
# <round>/runs/chunk-NN.txt|.err (runner stdout and stderr), <round>/runs/full.pid (the runner's Windows pid),
# <round>/recover.log, <round>/recover.pid (this script's Windows pid, for watch.sh).
# Environment: GUEST_IP (required, for gssh.sh), TBOUND_LOCAL and the other gssh.sh variables, MAX_CYCLES (default 15),
# PYTHON (default python).
# Start it detached: nohup bash infra/freeze/tools/recover.sh <round> > <round>/recover.out 2> <round>/recover.err &
# The abandon settles the guest; it does not rerun the unit. A provider-outage or task-outcome-unknown end is a rerun cause
# (register D30 as amended 2026-11-18), rerun with `tbound-run rerun` after the abandon; a budget or unclassified end never
# is. An outage-looking cause stops for the operator.
set -u
export MSYS_NO_PATHCONV=1
PY=${PYTHON:-python}
winpath() { if command -v cygpath > /dev/null; then cygpath -w "$1"; else printf '%s' "$1"; fi; }

# unknown_request <requests.jsonl>: the one request id with an UNKNOWN terminal that is not abandoned, or NONE:<details>.
unknown_request() {
  "$PY" -I -c '
import base64, json, sys
st = {}
for line in open(sys.argv[1], encoding="utf-8"):
    if not line.strip():
        continue
    e = json.loads(line)["event"]
    d = json.loads(base64.b64decode(e["data"]))
    rid = d.get("request_id")
    if e["kind"] == "pi_service_request_accepted":
        st[rid] = {}
    elif rid in st and e["kind"] == "pi_service_request_terminal":
        st[rid]["t"] = (d.get("response") or {}).get("status", "?")
    elif rid in st and e["kind"] == "pi_service_request_abandoned":
        st[rid]["x"] = True
open_ = {k: v for k, v in st.items() if not v.get("x") and v.get("t") not in ("completed", "refused")}
ids = list(open_)
print(ids[0] if len(ids) == 1 and open_[ids[0]].get("t") == "unknown" else "NONE:" + json.dumps(open_))
' "$(winpath "$1")"
}

# unknown_unit <journal.jsonl>: "<plan index> <streak>" of the last governed step that ended UNKNOWN (0-based plan index),
# with how many governed steps in a row, counted back from the last one, ended UNKNOWN. V steps do not use the service and
# are skipped. "-1 0" when the journal has none.
unknown_unit() {
  "$PY" -I -c '
import json, sys
done = []
for line in open(sys.argv[1], encoding="utf-8"):
    if line.strip():
        d = json.loads(line)
        if d.get("kind") == "done" and "|V|" not in d.get("step_id", ""):
            done.append(d)
unknown = lambda d: "governed run outcome is unknown" in (d.get("exec_error") or "")
idx = next((d["index"] for d in reversed(done) if unknown(d)), -1)
streak = 0
for d in reversed(done):
    if not unknown(d):
        break
    streak += 1
print(idx, streak)
' "$(winpath "$1")"
}

# streak_stop <streak>: the reason to stop, or nothing. A streak that is not a number means unknown_unit could not read
# the journal, so the systemic guard cannot be checked.
streak_stop() {
  case $1 in
    '' | *[!0-9]*) echo "cannot read the UNKNOWN streak from the journal (got '$1')" ;;
    *) if [ "$1" -ge 3 ]; then echo "systemic: $1 governed units in a row ended UNKNOWN"; fi ;;
  esac
}

# runner_args <round dir>: the runner's arguments. A registered run passes its record to every chunk (reproduce.md).
runner_args() {
  if [ -f "$1/registration.json" ]; then echo run --config run.json --registration registration.json; else echo run --config run.json; fi
}

[ "${RECOVER_LIB:-}" = 1 ] && return 0

M=${1:?usage: recover.sh <round dir>}
M=$(cd "$M" && pwd) || exit 2
K=$(cd "$(dirname "$0")" && pwd)
MAX=${MAX_CYCLES:-15}
G() { sh "$K/gssh.sh" "$@" < /dev/null; }
OP='sudo -n /opt/tbound/operator/tbound-op'
log() { echo "$(date -u +%FT%TZ) $*" | tee -a "$M/recover.log"; }
J=$("$PY" -I -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["run_dir"])' "$(winpath "$M/run.json")") || exit 2
command -v cygpath > /dev/null && J=$(cygpath -u "$J")
J=$J/journal.jsonl
start_runner() { # start_runner <chunk>
  # runner_args prints fixed words without spaces: the unquoted expansion splits them as intended.
  (cd "$M" && nohup ./bin/tbound-run.exe $(runner_args "$M") > "runs/$1.txt" 2> "runs/$1.err" &)
  sleep 3
  local pids
  pids=$(tasklist /FI "IMAGENAME eq tbound-run.exe" /FO CSV /NH | grep -i 'tbound-run' | cut -d, -f2 | tr -d '"')
  [ "$(printf '%s\n' "$pids" | grep -c .)" -eq 1 ] || { log "STOP: expected one tbound-run.exe after starting $1, found: $(echo $pids)"; exit 12; }
  printf '%s\n' "$pids" > "$M/runs/full.pid"
  log "started runner pid $(cat "$M/runs/full.pid") as $1"
}
mkdir -p "$M/runs" "$M/diag"
cat /proc/$$/winpid > "$M/recover.pid" 2> /dev/null || echo $$ > "$M/recover.pid"
n=$(ls "$M"/runs/chunk-*.txt 2> /dev/null | wc -l)
if [ "$n" -eq 0 ]; then
  date -u +%FT%TZ > "$M/runs/full.start"
  n=1
  start_runner chunk-01
fi
for cyc in $(seq 1 "$MAX"); do
  P=$(tr -d '\r\n ' < "$M/runs/full.pid")
  while tasklist /FI "PID eq $P" 2> /dev/null | grep -q tbound-run; do sleep 20; done
  last=$(ls "$M"/runs/chunk-*.txt | sort | tail -1)
  err=${last%.txt}.err
  if grep -q '^finished true' "$last"; then log "runner finished (finished true); done"; exit 0; fi
  if ! grep -q 'is not terminal' "$err"; then log "STOP: runner exit not recognised: $(head -c 400 "$err")"; exit 3; fi
  D=$M/diag/auto-$(date -u +%Y%m%dT%H%M%SZ)
  mkdir -p "$D"
  G "$OP collect requests" > /dev/null 2>&1
  G "cat /var/tmp/tbound-op/_/serve-pi-requests.jsonl" > "$D/requests.jsonl"
  rid=$(unknown_request "$D/requests.jsonl")
  case "$rid" in tbr-*) ;; *) log "STOP: cannot single out one unknown request: $rid"; exit 4 ;; esac
  G "journalctl -u tbound-pi.service --since '-12h' --no-pager | grep -F $rid | cut -c1-2500" > "$D/cause.txt" 2>&1
  read -r idx streak < <(unknown_unit "$J")
  if grep -Eqi 'status[ =:"]*(429|5[0-9][0-9])|rate.?limit|overload|transport|connection reset|i/o timeout|no such host|EOF' "$D/cause.txt"; then
    log "STOP: plan index $idx ($rid): the cause looks like a provider outage; decide by hand whether to abandon and continue, then rerun the unit with tbound-run rerun (D30 as amended) (see $D/cause.txt)"
    exit 9
  fi
  why=$(streak_stop "$streak")
  if [ -n "$why" ]; then log "STOP: $why (last plan index $idx, $rid)"; exit 10; fi
  G "$OP abandon $rid" > "$D/preview.txt" 2>&1
  tok=$(sed -n 's/^TOKEN=//p' "$D/preview.txt")
  if [ -z "$tok" ] || ! grep -q '^catalog_owner_scope_drained=' "$D/preview.txt" || ! grep -q '^cells_procs_bytes=0$' "$D/preview.txt" ||
    ! grep -q '^podman_ps_a_lines=0$' "$D/preview.txt" || ! grep -q '^catalog_unexposed_true=0$' "$D/preview.txt"; then
    log "STOP: abandon preview of $rid is not clean (see $D/preview.txt)"
    exit 5
  fi
  G "$OP abandon $rid --confirm $tok" > "$D/confirm.txt" 2>&1 || { log "STOP: confirm failed (see $D/confirm.txt)"; exit 6; }
  grep -q '"settlement":"abandoned-unknown"' "$D/confirm.txt" || { log "STOP: no settlement in $D/confirm.txt"; exit 6; }
  G "$OP svc start" > "$D/svc-start.txt" 2>&1
  G "$OP status pilot" | grep -q 'service=active socket=active' || { log "STOP: service not active after svc start"; exit 7; }
  G "$OP diag podman-ps" > "$D/leftover.txt" 2>&1
  if grep -E 'tbound-(accept|v)-' "$D/leftover.txt"; then log "STOP: leftover container after recovery (see $D/leftover.txt)"; exit 11; fi
  G "$OP collect catalog" > /dev/null 2>&1
  G "$OP collect requests" > /dev/null 2>&1
  log "cycle $cyc: abandoned $rid (plan index $idx, 0-based); leftover check clean; cause: $(head -c 600 "$D/cause.txt" | tr '\n' ' ')"
  cp "$J" "$D/journal-before-resume.jsonl"
  n=$((n + 1))
  start_runner "$(printf 'chunk-%02d' "$n")"
done
log "STOP: $MAX cycles used"
exit 8
