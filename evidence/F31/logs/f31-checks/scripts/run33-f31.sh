#!/bin/bash
# F31 step 33 (corpus mode) again, with the abandon confirm token read from attempt 1; TBA_PW from the T3 secret file in this process only.
set -u
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
W=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
export TBOUND_LOCAL=/f/trust-boundary/.local KIT_FREEZE=F31 GUEST_IP=172.18.247.10
export TBOUND_WGA=/home/tbound/tbound-work/f31-kit/infra/freeze/guest
export TBOUND_TRIAL_KEY=F:/trust-boundary/.local/secrets/abi8-forced/tr.key TBOUND_CLIENT_KEY=F:/trust-boundary/.local/secrets/abi8-forced/cl.key TBOUND_EVIDENCE_KEY=F:/trust-boundary/.local/secrets/abi8-forced/ev.key
export ABANDON_CONFIRM=${1:?token}
TBA_PW=$(cat "$(cygpath -u 'C:\Users\Admin\.t3\userdata\secrets\files\75bb3e73509294e3373941bc48d21187')"); export TBA_PW
L=//wsl.localhost/tbound-dev/home/tbound/tbound-work/receipts/F31/logs
cd $W/infra/freeze/guest || exit 2
echo "33 start $(date -u +%FT%TZ)"
bash 33-corpus-pilot.sh > $L/guest-33-corpus-pilot.sh.txt 2>&1; rc=$?
tail -8 $L/guest-33-corpus-pilot.sh.txt
echo "33 end $(date -u +%FT%TZ) rc=$rc"
