#!/bin/bash
# r-33.sh <round dir> <carrier R> <manifest sha256 hex> <carrier commit> <pass n>: 33-corpus-pilot.sh in carrier mode, as the
# walk tools/r-33.sh for the F34 re-witness: F34 kit worktree, F34 receipts, the handed-over password file (ROUND.md section 3).
# TBA_PW comes from $PWFILE, only inside this process; never printed.
set -u
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
M=${1:?}; CAR=${2:?}; MD=${3:?}; CC=${4:?}; N=${5:?}
W=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
L=//wsl.localhost/tbound-dev/home/tbound/tbound-work/receipts/F34/logs/33-corpus-pilot.log
[ ! -e $M/logs/33-pass$N.log ] || { echo "pass $N exists"; exit 2; }
mkdir -p $M/logs; a=$(wc -l < $L)
[ "$(git -C $W rev-parse HEAD)" = 8be667e6db1af7cd3e72f93ddf049880f097bbf9 ] || { echo "kit not 8be667e6"; exit 2; }
export MSYS_NO_PATHCONV=1 TBOUND_LOCAL=/f/trust-boundary/.local KIT_FREEZE=F34 GUEST_IP=172.29.57.244 TBOUND_WGA=/home/tbound/tbound-work/f34-kit/infra/freeze/guest
export TBOUND_TRIAL_KEY=F:/trust-boundary/.local/secrets/abi8-forced/tr.key TBOUND_CLIENT_KEY=F:/trust-boundary/.local/secrets/abi8-forced/cl.key TBOUND_EVIDENCE_KEY=F:/trust-boundary/.local/secrets/abi8-forced/ev.key
export CARRIER_DIR=/home/tbound/tbound-work/f33-rw-carriers/corpus/sampling/carriers/$CAR CARRIER_COMMIT=$CC CARRIER_MANIFEST_DIGEST=sha256:$MD
TBA_PW=$(cat /f/trust-boundary/.local/pilot-walk/secret/tba_pw); export TBA_PW
cd $W/infra/freeze/guest || exit 2
bash 33-corpus-pilot.sh > $M/logs/33-pass$N.console.txt 2>&1 < /dev/null; rc=$?
unset TBA_PW
tail -n +$((a+1)) $L > $M/logs/33-pass$N.log
echo "pass $N rc=$rc lines=$(wc -l < $M/logs/33-pass$N.log)"; tail -5 $M/logs/33-pass$N.console.txt
