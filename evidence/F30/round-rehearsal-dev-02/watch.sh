#!/bin/bash
# Exits (one notification) on the first notable journal record after line $1, a new recover.log line, or after $2 minutes.
export MSYS_NO_PATHCONV=1
M=/f/trust-boundary/.local/round-rehearsal-dev-02-f30; J=$M/runs/pilot/journal.jsonl
n=${1:-0}; cap=${2:-110}; r0=$(cat $M/recover.log 2>/dev/null | wc -l)
for i in $(seq 1 $cap); do
  m=$(cat "$J" 2>/dev/null | wc -l)
  if [ "$m" -gt "$n" ]; then
    hit=$(tail -n +$((n+1)) "$J" | head -n $((m-n)) | python -c "
import sys,json
for l in sys.stdin:
  l=l.strip()
  if not l: continue
  d=json.loads(l); k=d['kind']; o=d.get('outcome') or {}; el=o.get('evidence_loss')
  orc={a:b for a,b in (o.get('oracles') or {}).items() if b not in ('none',)}
  bad={a:b for a,b in orc.items() if b=='lost' or a.startswith('EFF-PROC')}
  if k not in ('header','begin','started','done') or (k=='done' and (el not in (None,'none') or d.get('exec_error') or bad)):
    print(d['seq'],k,d.get('index'),d.get('step_id'),'loss='+str(el),'elapsed='+str(d.get('elapsed_ms')),bad,(d.get('reason') or d.get('exec_error') or '')[:200])
")
    n=$m
    if [ -n "$hit" ]; then echo "EVENT at line $m: $hit"; exit 0; fi
  fi
  r=$(cat $M/recover.log 2>/dev/null | wc -l)
  if [ "$r" -gt "$r0" ]; then echo "RECOVER.LOG (line $m):"; tail -n $((r-r0)) $M/recover.log; exit 0; fi
  P=$(tr -d '\r\n ' < $M/recover.pid)
  if ! tasklist /FI "PID eq $P" 2>/dev/null | grep -q bash; then echo "SUPERVISOR GONE at line $m"; tail -3 $M/recover.log 2>/dev/null; exit 0; fi
  sleep 60
done
echo "TICK at line $m: $(grep -c '"kind":"done"' $J) done records; last: $(tail -1 $J | cut -c1-160)"
