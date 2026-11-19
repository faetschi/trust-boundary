#!/bin/bash
# Runs 33-corpus-pilot.sh from the F30 kit worktree with this round's carrier pins; TBA_PW only if the caller set it in this one command. Log slice -> logs/33-pass<N>.log.
set -u
N=$1; M=/f/trust-boundary/.local/round-rehearsal-dev-02-f30; L=//wsl.localhost/tbound-dev/home/tbound/tbound-work/receipts/F30/logs/33-corpus-pilot.log
[ ! -e $M/logs/33-pass$N.log ] || { echo "pass $N exists"; exit 2; }
a=$(wc -l < $L)
cd /f/trust-boundary/.local/worktrees/round30-kit/infra/freeze/guest || exit 2
GUEST_IP=172.19.180.82 TBOUND_LOCAL=/f/trust-boundary/.local KIT_FREEZE=F30 TBOUND_WGA=/home/tbound/tbound-work/f30-kit/infra/freeze/guest \
CARRIER_DIR=/home/tbound/tbound-work/round30b-carrier/corpus/sampling/carriers/rehearsal-dev-02 \
CARRIER_COMMIT=ffb0bc6699b3f980eadec52fe500cc2a6beec873 \
CARRIER_MANIFEST_DIGEST=sha256:72d70d13d4126b845902728a917a7d3161dfdf4e001ce0a659ab36b3b9440865 \
  bash 33-corpus-pilot.sh > /dev/null 2>&1 < /dev/null
rc=$?
tail -n +$((a+1)) $L > $M/logs/33-pass$N.log
echo "pass $N rc=$rc lines=$(wc -l < $M/logs/33-pass$N.log)"
