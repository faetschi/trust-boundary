#!/bin/bash
# $FREEZE owner-side freeze, steps 1-7, for the commit in freezes/$KIT_FREEZE.env (FREEZE_COMMIT). Stops at the first failing step.
. "$(dirname -- "${BASH_SOURCE[0]}")/../freeze-guard.sh" || exit 2
C=$FREEZE_COMMIT
. "$(dirname -- "${BASH_SOURCE[0]}")/../paths.sh"
# Owner steps run natively on Linux (a Linux checkout at the freeze commit): from Git Bash, WSL would reach the kit through /mnt.
case $(uname -s) in MINGW* | MSYS*) echo "FATAL: run tools/freeze.sh from a Linux checkout inside WSL, not from Git Bash" >&2; exit 2 ;; esac
LK=$KIT_DIR
cd "$KIT_DIR" || exit 2
L=$TBOUND_WORK_VIEW/receipts/$FREEZE/logs
make_receipts_dir
for s in "10-freeze.sh $C" 20-p1.sh 25-p2-rebuild.sh 30-p3.sh session-env.sh 35-evq-capture.sh 40-sign.sh; do
  n=${s%% *}; echo "##### $s $(date +%T)"
  on_linux "export KIT_FREEZE=$KIT_FREEZE; cd '$LK' && bash $s; rc=\$?; sync; exit \$rc" > "$L/console-$n.txt" 2>&1; rc=$?; f=$L/console-$n.txt
  grep -v "screen size" "$f" | tail -4
  [ $rc = 0 ] || { echo "STOP rc=$rc at $s"; exit $rc; }
done
echo "freeze end $(date +%T)"
