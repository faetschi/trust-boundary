#!/bin/bash
# F35 guest chain part A: 27 to 32 with the 40 evidence steps, then 33 (corpus mode). Part B (34 with RESEARCH_E2E=1 and 40 v-stub) runs
# separately after the pre-34 checkpoint. TBA_PW is read from the handed-over file inside this process only.
set -u
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
W=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
[ "$(git -C $W rev-parse HEAD)" = 09d4a5fee0fa9e8e401323abebad20eb877d09a9 ] || { echo "FATAL: worktree is not the F35 kit commit"; exit 1; }
[ -n "${GUEST_IP:-}" ] || { echo "FATAL: export GUEST_IP"; exit 1; }
export TBOUND_LOCAL=/f/trust-boundary/.local KIT_FREEZE=F35 GUEST_IP CORPUS_PILOT=1 V_STUB_PROBE=0
export TBOUND_WGA=/home/tbound/tbound-work/f35-kit/infra/freeze/guest
export TBOUND_TRIAL_KEY=F:/trust-boundary/.local/secrets/abi8-forced/tr.key TBOUND_CLIENT_KEY=F:/trust-boundary/.local/secrets/abi8-forced/cl.key TBOUND_EVIDENCE_KEY=F:/trust-boundary/.local/secrets/abi8-forced/ev.key
TBA_PW=$(cat /f/trust-boundary/.local/pilot-walk/secret/tba_pw); export TBA_PW
echo "chain A start $(date -u +%FT%TZ) GUEST_IP=$GUEST_IP"
bash $W/infra/freeze/tools/guest-chain.sh; rc=$?
echo "chain A end $(date -u +%FT%TZ) rc=$rc"
exit $rc
