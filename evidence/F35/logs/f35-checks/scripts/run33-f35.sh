#!/bin/bash
# F35 chain part A tail: 33 again with the abandon confirm token from attempt 1 (F34 chain 2 precedent); TBA_PW from the handed-over file in this process only.
set -u
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
W=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
[ "$(git -C $W rev-parse HEAD)" = 09d4a5fee0fa9e8e401323abebad20eb877d09a9 ] || { echo "FATAL kit"; exit 2; }
export MSYS_NO_PATHCONV=1 TBOUND_LOCAL=/f/trust-boundary/.local KIT_FREEZE=F35 GUEST_IP=172.29.57.244 CORPUS_PILOT=1 V_STUB_PROBE=0
export TBOUND_WGA=/home/tbound/tbound-work/f35-kit/infra/freeze/guest
export TBOUND_TRIAL_KEY=F:/trust-boundary/.local/secrets/abi8-forced/tr.key TBOUND_CLIENT_KEY=F:/trust-boundary/.local/secrets/abi8-forced/cl.key TBOUND_EVIDENCE_KEY=F:/trust-boundary/.local/secrets/abi8-forced/ev.key
export ABANDON_CONFIRM=${1:?abandon token from attempt 1}
TBA_PW=$(cat /f/trust-boundary/.local/pilot-walk/secret/tba_pw); export TBA_PW
L=//wsl.localhost/tbound-dev/home/tbound/tbound-work/receipts/F35/logs
cd $W/infra/freeze/guest || exit 2
[ -e $L/guest-33-corpus-pilot.sh.attempt1.txt ] || mv -n $L/guest-33-corpus-pilot.sh.txt $L/guest-33-corpus-pilot.sh.attempt1.txt
echo "##### 33-corpus-pilot.sh (ABANDON_CONFIRM) $(date -u +%FT%TZ)"
bash 33-corpus-pilot.sh > $L/guest-33-corpus-pilot.sh.txt 2>&1; rc=$?
tail -10 $L/guest-33-corpus-pilot.sh.txt
echo "run33 end $(date -u +%FT%TZ) rc=$rc"
exit $rc
