#!/bin/bash
# assemble-evidence.sh <evidence worktree>: copies the F35 qualification records into <worktree>/evidence/F35/logs, in F34's layout.
# Copies only; reads WSL through wsl.exe tar on stdout (no /mnt). Refuses an existing evidence/F35/logs.
set -euo pipefail
WT=${1:?worktree}; E=$WT/evidence/F35/logs; K=$E/f35-checks; L=/f/trust-boundary/.local
[ ! -e $E ] || { echo "FATAL: $E exists"; exit 2; }
mkdir -p $E $K/candidate $K/red-green $K/scripts $K/vs $K/rewitness $K/probe $E/evq-report $E/gates
wx() { MSYS_NO_PATHCONV=1 wsl.exe -d tbound-dev -u tbound --exec bash -c "$1"; }
wx 'tar -C /home/tbound/tbound-work/receipts/F35/logs -cf - .' | tar -x -C $E
wx 'tar -C /home/tbound/tbh/f35/evq-report --exclude=./npm-cache -cf - .' | tar -x -C $E/evq-report
wx 'tar -C /home/tbound/tbh/f35/vs --exclude="*.ed25519" -cf - .' | tar -x -C $K/vs
cp $L/f35-freeze/freeze-driver.txt $L/f35-freeze/run-f35-freeze.out $E/
cp $L/f35-candidate/gate-g35.log $L/f35-candidate/run-g35.out $L/f35-candidate-r2/gate-g35b.log $L/f35-candidate-r2/run-g35b.out $E/gates/
cp $L/f35-candidate/f35-diff-by-origin.md $L/f35-candidate-r2/f35-diff-by-origin-r2.md $K/candidate/
cp $L/f35-freeze/f35-restore.out $L/f35-freeze/f35-h1-hold-postcheck.txt $L/f35-freeze/chainA.out $L/f35-freeze/run33.out $L/f35-freeze/partB.out $L/f35-freeze/evq35.out $K/
[ -e $L/f35-freeze/f35-end-guest-state.txt ] && cp $L/f35-freeze/f35-end-guest-state.txt $K/
for f in f35-setup.sh run-f35-freeze.sh guest-chain-f35.sh guest-34-f35.sh run33-f35.sh f35-restore.ps1 evq35.sh assemble-evidence.sh; do cp $L/f35-freeze/$f $K/scripts/; done
for f in rw35-aside.sh rw35-33.sh rw35-mkrun.py rw35-screen.sh rw35-prep.sh; do cp $L/f35-rewitness-tools/$f $K/scripts/; done
cp $L/f35-candidate-r2/run-g35b.sh $K/scripts/
[ -d $L/f35-candidate-r2/red-green ] && cp $L/f35-candidate-r2/red-green/* $K/red-green/
for R in pilot-r01 pilot-r02; do
  M=$L/round-$R-f35; D=$K/rewitness/$R; mkdir -p $D
  for p in diag/clock-post-run.txt diag/clock-pre-run.txt diag/final/collect-2.txt diag/final/guest-sha256-verify.txt diag/final/leftover-final.txt diag/leftover-pre-run.txt logs/33-pass1.console.txt logs/aside.txt logs/plan.txt recover.log run.json runs/chunk-01.err runs/chunk-01.txt runs/full.start runs/pilot/journal.jsonl screening-report/report.json screening-report/report.md start.sh vals/values.txt; do
    [ -e $M/$p ] && cp $M/$p $D/$(echo $p | tr '/' '_')
  done
done
P=$L/f35-probe
cp $P/README.md $P/REPORT.md $P/real-SHA256 $P/stub-SHA256 $P/stub-r2-SHA256 $P/blobs-fcf1cee3.txt $K/probe/
mkdir -p $K/probe/harness && cp $P/real/harness/main.go $P/real/harness/v.mjs $P/real/harness/v2.mjs $P/real/harness/task-b.txt $P/real/harness/stub-upstream.py $K/probe/harness/
mkdir -p $K/probe/real && cp -r $P/real/real-runs $K/probe/real/ && mkdir -p $K/probe/real/cfg && cp $P/real/cfg/usd-ledger $P/real/cfg/research-ledger.json $P/real/cfg/research-ledger-checkpoint.json $P/real/cfg/research-profile.json $K/probe/real/cfg/
cp $P/real/r3b-replayed-assistant-message.json $K/probe/real/
mkdir -p $K/probe/stub-r2 && cp -r $P/stub-r2/stub-runs-r2 $K/probe/stub-r2/
if grep -rIEl 'sk-or-v1-[0-9a-f]{16,}' $E; then echo "FATAL: key material in evidence"; exit 3; fi
find $E \( -name '*.key' -o -name '*.pem' -o -name '*.tar' -o -name '*.exe' -o -name '*ed25519' \) -print | head
echo "files $(find $E -type f | wc -l) size $(du -sh $E | cut -f1)"
