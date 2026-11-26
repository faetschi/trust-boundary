#!/bin/bash
# step 0 (D59 item 4): read-only copy and check of the 2026-11-21 research ledger, on the walk's F32 guest state, before any restore.
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
set -euo pipefail
cd /c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
export GUEST_IP=172.29.57.244 TBOUND_LOCAL=/f/trust-boundary/.local MSYS_NO_PATHCONV=1 TBOUND_WSL_DISTRO=tbound-dev TBOUND_WGA=/home/tbound/tbound-work/f32-kit/infra/freeze/guest
D=/f/trust-boundary/.local/f33-d59-ledger
[ ! -e "$D" ] || { echo "FATAL: $D exists"; exit 1; }
mkdir -p "$D"
G() { sh infra/freeze/tools/gssh.sh "$@" < /dev/null; }
echo "start $(date -u +%FT%TZ)" | tee "$D/step0.log"
TBA_PW=$(cat /f/trust-boundary/.local/pilot-walk/secret/tba_pw); export TBA_PW
KIT_FREEZE=F32 bash infra/freeze/guest/00-session-sudo.sh open > "$D/sudo-open.txt" 2>&1 || { unset TBA_PW; echo "FATAL: session sudo open failed (see sudo-open.txt)" | tee -a "$D/step0.log"; exit 2; }
unset TBA_PW
rc=0
{
G 'sudo -n stat -c "%n %U:%G %a %s %y" /var/lib/tbound/sessions/usd-ledger /etc/tbound/provider/research-ledger-checkpoint.json /etc/tbound/provider/research-ledger.json' > "$D/guest-stat.txt" &&
G 'sudo -n sha256sum /var/lib/tbound/sessions/usd-ledger /etc/tbound/provider/research-ledger-checkpoint.json' > "$D/guest-sha256.txt" &&
G 'sudo -n cat /var/lib/tbound/sessions/usd-ledger' > "$D/usd-ledger.20261121" &&
G 'sudo -n cat /etc/tbound/provider/research-ledger-checkpoint.json' > "$D/research-ledger-checkpoint.json"
} || rc=$?
KIT_FREEZE=F32 bash infra/freeze/guest/00-session-sudo.sh close > "$D/sudo-close.txt" 2>&1 || echo "WARNING: session sudo close failed (see sudo-close.txt)" | tee -a "$D/step0.log"
[ "$rc" -eq 0 ] || { echo "FATAL: a read failed (rc $rc); ledger not proven" | tee -a "$D/step0.log"; exit 3; }
L=$D/usd-ledger.20261121
WANT=6bed50e0065343fbb41b4121873d4b715b0cbd2b26331933176ee22f334148ec
N=$(wc -l < "$L" | tr -d ' ')
HEAD=$(head -n 1 "$L" | head -c -1 | sha256sum | cut -c1-64)
git show origin/codex/tbound-prototype:evidence/F32/proving-rounds/round-research/logs/checkpoint-1.json > "$D/checkpoint-1.kept.json"
CK=$(cmp -s "$D/research-ledger-checkpoint.json" "$D/checkpoint-1.kept.json" && echo equal || echo DIFFERENT)
LOCALSUM=$(sha256sum "$L" "$D/research-ledger-checkpoint.json" | cut -c1-64 | tr '\n' ' ')
GUESTSUM=$(cut -c1-64 "$D/guest-sha256.txt" | tr '\n' ' ')
{
echo "records $N (want 1)"
echo "head $HEAD (want $WANT)"
echo "checkpoint vs evidence/F32/proving-rounds/round-research/logs/checkpoint-1.json: $CK"
echo "local sha256 [$LOCALSUM] guest sha256 [$GUESTSUM]: $([ "$LOCALSUM" = "$GUESTSUM" ] && echo equal || echo DIFFERENT)"
} | tee -a "$D/step0.log"
if [ "$N" = 1 ] && [ "$HEAD" = "$WANT" ] && [ "$CK" = equal ] && [ "$LOCALSUM" = "$GUESTSUM" ]; then
  echo "RESULT UNTOUCHED: 2026-11-21 genesis, unused, superseded by D59" | tee -a "$D/step0.log"
else
  echo "RESULT NOT PROVEN: stop and report (the cap stays US\$5, D59 item 4)" | tee -a "$D/step0.log"; exit 4
fi
echo "end $(date -u +%FT%TZ)" | tee -a "$D/step0.log"
