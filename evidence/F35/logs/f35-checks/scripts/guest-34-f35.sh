#!/bin/bash
# F35 guest chain part B: 34-v-stub-probe.sh with RESEARCH_E2E=1 (B, P and V on the v-stub route) and 40-guest-evidence.sh v-stub,
# exactly as guest-chain.sh runs them, after the pre-34 checkpoint E2E_CHECKPOINT. The guest is restored to that checkpoint afterwards in every case.
set -u
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
W=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
[ "$(git -C $W rev-parse HEAD)" = 09d4a5fee0fa9e8e401323abebad20eb877d09a9 ] || { echo "FATAL: worktree is not the F35 kit commit"; exit 1; }
[ -n "${GUEST_IP:-}" ] && [ -n "${E2E_CHECKPOINT:-}" ] || { echo "FATAL: export GUEST_IP and E2E_CHECKPOINT"; exit 1; }
export TBOUND_LOCAL=/f/trust-boundary/.local KIT_FREEZE=F35 GUEST_IP RESEARCH_E2E=1 E2E_CHECKPOINT
export TBOUND_WGA=/home/tbound/tbound-work/f35-kit/infra/freeze/guest
export TBOUND_TRIAL_KEY=F:/trust-boundary/.local/secrets/abi8-forced/tr.key TBOUND_CLIENT_KEY=F:/trust-boundary/.local/secrets/abi8-forced/cl.key TBOUND_EVIDENCE_KEY=F:/trust-boundary/.local/secrets/abi8-forced/ev.key
TBA_PW=$(cat /f/trust-boundary/.local/pilot-walk/secret/tba_pw); export TBA_PW
. $W/infra/freeze/freeze-guard.sh || exit 2
. $W/infra/freeze/paths.sh
cd "$KIT_DIR/guest" || exit 2
L=$TBOUND_WORK_VIEW/receipts/$FREEZE/logs
echo "part B start $(date -u +%FT%TZ) GUEST_IP=$GUEST_IP E2E_CHECKPOINT=$E2E_CHECKPOINT L=$L"
echo "##### 34-v-stub-probe.sh $(date +%T)"
bash 34-v-stub-probe.sh > "$L/guest-34-v-stub-probe.sh.txt" 2>&1; rc=$?
tail -6 "$L/guest-34-v-stub-probe.sh.txt"
[ "$rc" = 0 ] || { echo "STOP rc=$rc at 34-v-stub-probe.sh"; echo "part B end $(date -u +%FT%TZ) rc=$rc"; exit "$rc"; }
echo "##### 40-guest-evidence.sh v-stub $(date +%T)"
bash 40-guest-evidence.sh v-stub > "$L/guest-40-guest-evidence.sh-v-stub.txt" 2>&1; rc=$?
tail -6 "$L/guest-40-guest-evidence.sh-v-stub.txt"
echo "part B end $(date -u +%FT%TZ) rc=$rc"
exit $rc
