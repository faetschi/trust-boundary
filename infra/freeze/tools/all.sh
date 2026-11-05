#!/bin/bash
# $FREEZE end to end: full gate on the frozen commit, owner-side freeze, guest chain. Each stage runs only if the previous passed.
set -o pipefail
. "$(dirname -- "${BASH_SOURCE[0]}")/../paths.sh"
K=$KIT_DIR
case $(uname -s) in MINGW* | MSYS*) echo "FATAL: run tools/all.sh from a Linux checkout inside WSL, not from Git Bash" >&2; exit 2 ;; esac
R=$REPO_ROOT
. "$(dirname -- "${BASH_SOURCE[0]}")/../freeze-guard.sh" || exit 2
C=$FREEZE_COMMIT
FREEZE_LC=$(printf '%s' "$FREEZE" | tr 'A-Z' 'a-z')
[ -n "${TBA_PW:-}" ] || { echo "export TBA_PW first"; exit 2; }
L=$TBOUND_WORK_VIEW/receipts/$FREEZE/logs
make_receipts_dir
git -C "$R" archive --format=tar "$C" > "$L/gate-$C.tar" || exit 2
on_linux "KIT_FREEZE=$KIT_FREEZE bash '$K/tools/gate.sh' $TBOUND_WORK/receipts/$FREEZE/logs/gate-$C.tar $FREEZE_LC-${C:0:7}" > "$L/gate.txt" 2>&1
grep -v "screen size" "$L/gate.txt" | tail -6
for mark in BUILD_OK VET_OK WINDOWS_OK FMT_OK BOUNDARY_OK TEST_RC=0; do
  grep -q "^$mark$" "$L/gate.txt" || { echo "STOP: gate not green, no $mark"; exit 1; }
done
bash "$K/tools/freeze.sh" || { echo "STOP: freeze failed"; exit 1; }
bash "$K/tools/guest-chain.sh"
