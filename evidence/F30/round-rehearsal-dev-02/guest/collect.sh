#!/bin/bash
# Read-only guest collection after the round (tbound-op status/diag/collect only; collect copies allow-listed files into /var/tmp/tbound-op/_/).
set -u
M=/f/trust-boundary/.local/round-rehearsal-dev-02-f30; F=$M/diag/final; K=/f/trust-boundary/.local/worktrees/round30-kit
export GUEST_IP=172.19.180.82 TBOUND_LOCAL=/f/trust-boundary/.local MSYS_NO_PATHCONV=1
G(){ sh $K/infra/freeze/tools/gssh.sh "$@" </dev/null; }; OP='sudo -n /opt/tbound/operator/tbound-op'
date -u +%FT%TZ > $F/collected-at.txt
G "$OP diag podman-ps" > $F/leftover-final.txt 2>&1
G "$OP status pilot" > $F/status-pilot.txt 2>&1
G "uptime -s" > $F/uptime-since.txt 2>&1
{ G "$OP collect requests"; echo "rq=$?"; G "$OP collect catalog"; echo "cat=$?"; G "$OP collect l4" > $F/l4-stdout.txt; echo "l4=$?"; } > $F/collect-1.txt 2>&1
G "cat /var/tmp/tbound-op/_/serve-pi-requests.jsonl" > $F/requests-now.jsonl
ids=$(grep -o 'production-run-[0-9a-f]\{24\}' $F/requests-now.jsonl | sort -u)
since=$(cat $M/runs/full.start)
ok=0 bad=0
for r in $ids; do if G "$OP collect audit $r" >/dev/null 2>&1; then ok=$((ok+1)); else bad=$((bad+1)); echo "audit $r failed"; fi; done > $F/collect-2.txt 2>&1
echo "ok=$ok bad=$bad" >> $F/collect-2.txt
G "journalctl -u tbound-pi.service --since '$since' --no-pager -o short-iso" > $F/service-journal-iso.txt 2>&1
G "tail -n 2000 /var/log/tbound-op.log" > $F/tbound-op-log.txt 2>&1
G "cd /var/tmp/tbound-op/_ && sha256sum -- *" > $F/guest-sha256.txt 2>&1
G "tar -C /var/tmp/tbound-op -cf - _" > $F/guest-collect.tar 2> $F/guest-collect.err
mkdir -p $F/audit && tar -C $F/audit --strip-components=1 -xf $F/guest-collect.tar
( cd $F/audit && sha256sum -c --quiet ../guest-sha256.txt ) > $F/guest-sha256-verify.txt 2>&1; echo "verify rc=$?" >> $F/guest-sha256-verify.txt
( cd $F && sha256sum guest-collect.tar ) > $F/COLLECTION-SUMS
