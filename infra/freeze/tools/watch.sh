#!/bin/bash
# watch.sh <round dir> [line] [minutes]: watcher of a round that recover.sh supervises (Git Bash on the owner machine). It
# exits, once, on the first notable journal record after journal line <line> (default 0), on a new recover.log line, when
# the recover.sh process is gone, or after <minutes> (default 110) with a progress line. Notable is any record other than
# header, begin, started and done, and a done record with evidence loss, an executor error, a lost oracle or any PROC
# finding other than none. Print the reported line number and pass it back as <line> on the next call.
set -u
export MSYS_NO_PATHCONV=1
PY=${PYTHON:-python}
winpath() { if command -v cygpath > /dev/null; then cygpath -w "$1"; else printf '%s' "$1"; fi; }
M=${1:?usage: watch.sh <round dir> [line] [minutes]}
n=${2:-0}
cap=${3:-110}
J=$("$PY" -I -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["run_dir"])' "$(winpath "$M/run.json")") || exit 2
command -v cygpath > /dev/null && J=$(cygpath -u "$J")
J=$J/journal.jsonl
r0=$(cat "$M/recover.log" 2> /dev/null | wc -l)
m=$n
for _ in $(seq 1 "$cap"); do
  m=$(cat "$J" 2> /dev/null | wc -l)
  if [ "$m" -gt "$n" ]; then
    hit=$(tail -n +$((n + 1)) "$J" | head -n $((m - n)) | "$PY" -I -c '
import json, sys
for line in sys.stdin:
    if not line.strip():
        continue
    d = json.loads(line)
    k, o = d["kind"], d.get("outcome") or {}
    loss = o.get("evidence_loss")
    bad = {a: b for a, b in (o.get("oracles") or {}).items() if b != "none" and (b == "lost" or a.startswith("EFF-PROC"))}
    if k not in ("header", "begin", "started", "done") or (k == "done" and (loss not in (None, "none") or d.get("exec_error") or bad)):
        print(d["seq"], k, d.get("index"), d.get("step_id"), "loss=" + str(loss), "elapsed=" + str(d.get("elapsed_ms")), bad,
              (d.get("reason") or d.get("exec_error") or "")[:200])
')
    n=$m
    if [ -n "$hit" ]; then echo "EVENT at line $m: $hit"; exit 0; fi
  fi
  r=$(cat "$M/recover.log" 2> /dev/null | wc -l)
  if [ "$r" -gt "$r0" ]; then echo "RECOVER.LOG (line $m):"; tail -n $((r - r0)) "$M/recover.log"; exit 0; fi
  P=$(tr -d '\r\n ' < "$M/recover.pid")
  if ! tasklist /FI "PID eq $P" 2> /dev/null | grep -q bash; then echo "SUPERVISOR GONE at line $m"; tail -3 "$M/recover.log" 2> /dev/null; exit 0; fi
  sleep 60
done
echo "TICK at line $m: $(grep -c '"kind":"done"' "$J") done records; last: $(tail -1 "$J" | cut -c1-160)"
