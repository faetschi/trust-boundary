#!/bin/bash
# F34 chain tail: 33 again with the abandon confirm token from attempt 1, then 34 V stub probe and 40 v-stub (guest-chain.sh order); TBA_PW from the handed-over file in this process only.
set -u
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
W=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
[ "$(git -C $W rev-parse HEAD)" = 8be667e6db1af7cd3e72f93ddf049880f097bbf9 ] || { echo "FATAL kit"; exit 2; }
export MSYS_NO_PATHCONV=1 TBOUND_LOCAL=/f/trust-boundary/.local KIT_FREEZE=F34 GUEST_IP=172.29.57.244 CORPUS_PILOT=1 V_STUB_PROBE=1
export TBOUND_WGA=/home/tbound/tbound-work/f34-kit/infra/freeze/guest
export TBOUND_TRIAL_KEY=F:/trust-boundary/.local/secrets/abi8-forced/tr.key TBOUND_CLIENT_KEY=F:/trust-boundary/.local/secrets/abi8-forced/cl.key TBOUND_EVIDENCE_KEY=F:/trust-boundary/.local/secrets/abi8-forced/ev.key
export ABANDON_CONFIRM=${1:?abandon token from attempt 1}
TBA_PW=$(cat /f/trust-boundary/.local/pilot-walk/secret/tba_pw); export TBA_PW
L=//wsl.localhost/tbound-dev/home/tbound/tbound-work/receipts/F34/logs
cd $W/infra/freeze/guest || exit 2
[ -e $L/guest-33-corpus-pilot.sh.attempt1.txt ] || mv -n $L/guest-33-corpus-pilot.sh.txt $L/guest-33-corpus-pilot.sh.attempt1.txt
step() { local n=$1; shift; echo "##### $* $(date -u +%FT%TZ)"; bash "$@" > $L/guest-$n.txt 2>&1; rc=$?; tail -8 $L/guest-$n.txt; echo "rc=$rc $(date -u +%FT%TZ)"; [ $rc = 0 ] || { echo "STOP rc=$rc at $*"; exit $rc; }; }
step 33-corpus-pilot.sh 33-corpus-pilot.sh
unset ABANDON_CONFIRM
step 34-v-stub-probe.sh 34-v-stub-probe.sh
step 40-guest-evidence.sh-v-stub 40-guest-evidence.sh v-stub
echo "TAIL_OK $(date -u +%FT%TZ)"
