#!/bin/bash
# Detached supervisor for the M3 F23 rehearsal. Waits for the runner to exit. If it exited because an earlier request left a
# non-terminal workflow (UNKNOWN), it previews the abandon on the guest, confirms ONLY when every fact is clean, restarts the guest
# service, saves diagnostics and restarts the runner. Anything else: stop and leave it for the owner thread. Max 15 cycles.
set -u
M=/f/trust-boundary/.local/m3-f23; K=/f/trust-boundary/.local/worktrees/f23-kit
export GUEST_IP=${GUEST_IP:-172.24.217.1} TBOUND_LOCAL=/f/trust-boundary/.local MSYS_NO_PATHCONV=1
G(){ sh $K/infra/freeze/tools/gssh.sh "$@" </dev/null; }; OP='sudo -n /opt/tbound/operator/tbound-op'
log(){ echo "$(date -u +%FT%TZ) $*" | tee -a $M/recover.log; }
n=$(ls $M/runs/chunk-*.txt | wc -l)
for cyc in $(seq 1 15); do
  P=$(tr -d '\r\n ' < $M/runs/full.pid)
  while tasklist /FI "PID eq $P" 2>/dev/null | grep -q tbound-run; do sleep 20; done
  last=$(ls $M/runs/chunk-*.txt | sort | tail -1); err=${last%.txt}.err
  if grep -q '^finished true' $last; then log "runner finished (finished true); done"; exit 0; fi
  if ! grep -q 'is not terminal' $err; then log "STOP: runner exit not recognised: $(head -c 400 $err)"; exit 3; fi
  D=$M/diag/auto-$(date -u +%Y%m%dT%H%M%SZ); mkdir -p $D
  G "$OP collect requests" >/dev/null 2>&1; G "cat /var/tmp/tbound-op/_/serve-pi-requests.jsonl" > $D/requests.jsonl
  rid=$(node -e '
const L=require("fs").readFileSync(process.argv[1],"utf8").trim().split("\n").map(JSON.parse);const st={};
for(const r of L){const e=r.event;const d=JSON.parse(Buffer.from(e.data,"base64").toString());
 if(e.kind=="pi_service_request_accepted")st[d.request_id]={a:1};
 else if(e.kind=="pi_service_request_terminal"&&st[d.request_id])st[d.request_id].t=(d.response||{}).status||"?";
 else if(e.kind=="pi_service_request_abandoned"&&st[d.request_id])st[d.request_id].x=1}
const o=Object.entries(st).filter(([k,v])=>!v.x&&v.t!=="completed"&&v.t!=="refused");console.log(o.length==1&&o[0][1].t=="unknown"?o[0][0]:"NONE:"+JSON.stringify(o))' "$(cygpath -w $D/requests.jsonl)")
  case "$rid" in tbr-*) ;; *) log "STOP: cannot single out one unknown request: $rid"; exit 4;; esac
  G "journalctl -u tbound-pi.service --since '-12h' --no-pager | grep -F $rid | cut -c1-2500" > $D/cause.txt 2>&1
  G "$OP abandon $rid" > $D/preview.txt 2>&1
  tok=$(sed -n 's/^TOKEN=//p' $D/preview.txt)
  if [ -z "$tok" ] || ! grep -q '^catalog_owner_scope_drained=' $D/preview.txt || ! grep -q '^cells_procs_bytes=0$' $D/preview.txt || ! grep -q '^podman_ps_a_lines=0$' $D/preview.txt || ! grep -q '^catalog_unexposed_true=0$' $D/preview.txt; then
    log "STOP: abandon preview of $rid is not clean (see $D/preview.txt)"; exit 5; fi
  G "$OP abandon $rid --confirm $tok" > $D/confirm.txt 2>&1 || { log "STOP: confirm failed (see $D/confirm.txt)"; exit 6; }
  grep -q '"settlement":"abandoned-unknown"' $D/confirm.txt || { log "STOP: no settlement in $D/confirm.txt"; exit 6; }
  G "$OP svc start" > $D/svc-start.txt 2>&1; G "$OP status pilot" | grep -q 'service=active socket=active' || { log "STOP: service not active after svc start"; exit 7; }
  G "$OP collect catalog" >/dev/null 2>&1; G "$OP collect requests" >/dev/null 2>&1
  log "cycle $cyc: abandoned $rid; cause: $(head -c 600 $D/cause.txt | tr '\n' ' ')"
  n=$((n+1)); c=$(printf 'chunk-%02d' $n)
  cp $M/runs/pilot/journal.jsonl $D/journal-before-resume.jsonl
  cd $M && nohup ./bin/tbound-run.exe run --config run.json > runs/$c.txt 2> runs/$c.err &
  echo $! > $M/runs/full.pid.sh; sleep 3
  # Windows pid of the exe for tasklist:
  W=$(tasklist /FI "IMAGENAME eq tbound-run.exe" /FO CSV /NH | head -1 | cut -d, -f2 | tr -d '"'); echo "$W" > $M/runs/full.pid
  log "restarted runner pid $W as $c"
done
log "STOP: 15 cycles used"; exit 8
