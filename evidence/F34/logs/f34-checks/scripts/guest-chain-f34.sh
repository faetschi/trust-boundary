#!/bin/bash
# F34 guest chain with corpus mode (33) and the V stub probe (34); TBA_PW is read from the handed-over file inside this process only.
set -u
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
W=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
[ "$(git -C $W rev-parse HEAD)" = 8be667e6db1af7cd3e72f93ddf049880f097bbf9 ] || { echo "FATAL: worktree is not the F34 kit commit"; exit 1; }
[ -n "${GUEST_IP:-}" ] || { echo "FATAL: export GUEST_IP"; exit 1; }
export TBOUND_LOCAL=/f/trust-boundary/.local KIT_FREEZE=F34 GUEST_IP CORPUS_PILOT=1 V_STUB_PROBE=1
export TBOUND_WGA=/home/tbound/tbound-work/f34-kit/infra/freeze/guest
export TBOUND_TRIAL_KEY=F:/trust-boundary/.local/secrets/abi8-forced/tr.key TBOUND_CLIENT_KEY=F:/trust-boundary/.local/secrets/abi8-forced/cl.key TBOUND_EVIDENCE_KEY=F:/trust-boundary/.local/secrets/abi8-forced/ev.key
TBA_PW=$(cat /f/trust-boundary/.local/pilot-walk/secret/tba_pw); export TBA_PW
echo "chain start $(date -u +%FT%TZ) GUEST_IP=$GUEST_IP"
bash $W/infra/freeze/tools/guest-chain.sh; rc=$?
echo "chain end $(date -u +%FT%TZ) rc=$rc"
