#!/bin/bash
# Second read-only collection step: the run ids are base64 inside the request journal, so collect.sh's grep finds none.
# Decoded ids (receipt run_id of every terminal request) are in diag/final/all-run-ids.txt; collect each audit, then retake sha256 and the tarball.
set -u
M=/f/trust-boundary/.local/round-rehearsal-dev-02-f30; F=$M/diag/final; K=/f/trust-boundary/.local/worktrees/round30-kit
export GUEST_IP=172.19.180.82 TBOUND_LOCAL=/f/trust-boundary/.local MSYS_NO_PATHCONV=1
G(){ sh $K/infra/freeze/tools/gssh.sh "$@" </dev/null; }; OP='sudo -n /opt/tbound/operator/tbound-op'
ok=0 bad=0
for r in $(tr -d '\r' < $F/all-run-ids.txt); do if G "$OP collect audit $r" >/dev/null 2>&1; then ok=$((ok+1)); else bad=$((bad+1)); echo "audit $r failed"; fi; done > $F/collect-2.txt 2>&1
echo "ok=$ok bad=$bad (run ids decoded from requests)" >> $F/collect-2.txt
G "cd /var/tmp/tbound-op/_ && sha256sum -- *" > $F/guest-sha256.txt 2>&1
G "tar -C /var/tmp/tbound-op -cf - _" > $F/guest-collect.tar 2> $F/guest-collect.err
mkdir -p $F/audit && tar -C $F/audit --strip-components=1 -xf $F/guest-collect.tar
( cd $F/audit && sha256sum -c --quiet ../guest-sha256.txt ) > $F/guest-sha256-verify.txt 2>&1; echo "verify rc=$?" >> $F/guest-sha256-verify.txt
( cd $F && sha256sum guest-collect.tar ) > $F/COLLECTION-SUMS
