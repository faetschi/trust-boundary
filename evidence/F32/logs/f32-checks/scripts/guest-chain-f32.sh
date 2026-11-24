#!/bin/bash
# F32 guest chain with corpus mode (33) and the netsink step; TBA_PW is read from the T3 secret file inside this process only.
set -u
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
W=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
export TBOUND_LOCAL=/f/trust-boundary/.local KIT_FREEZE=F32 GUEST_IP=172.18.247.10 CORPUS_PILOT=1
export TBOUND_WGA=/home/tbound/tbound-work/f32-kit/infra/freeze/guest
export TBOUND_TRIAL_KEY=F:/trust-boundary/.local/secrets/abi8-forced/tr.key TBOUND_CLIENT_KEY=F:/trust-boundary/.local/secrets/abi8-forced/cl.key TBOUND_EVIDENCE_KEY=F:/trust-boundary/.local/secrets/abi8-forced/ev.key
TBA_PW=$(cat "$(cygpath -u 'C:\Users\Admin\.t3\userdata\secrets\files\75bb3e73509294e3373941bc48d21187')"); export TBA_PW
echo "chain start $(date -u +%FT%TZ)"
bash $W/infra/freeze/tools/guest-chain.sh; rc=$?
echo "chain end $(date -u +%FT%TZ) rc=$rc"
