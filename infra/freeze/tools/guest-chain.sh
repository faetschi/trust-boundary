#!/bin/bash
# $FREEZE guest chain: archive $PREV_FREEZE state, install $FREEZE, G1 recovery status, G1 cycle, EVQ producers, route probe, dr probe, dr evidence.
# Order: 27 archive, 00 open, 05 runtime swap (P11), 10 upgrade, 20, 50, 00 close, 29/30/40/31/40/32/40.
# Stops at the first failing step. The literal "32 then one 40 call with all six kinds" order is impossible: capability/procoracle/
# landlock-connect need the service RUNNING and 32 stops it, while route-direct-runtime needs 32's RUNID. The chain therefore uses
# 40's documented order: service producers after 30 (g1 service up), route-bash-view after 31, route-direct-runtime after 32.
# CORPUS_PILOT=1 appends 33-corpus-pilot.sh (needs TBA_PW; the session drop-in is closed by then); V_STUB_PROBE=1 (with CORPUS_PILOT=1 and TBOUND_TRIAL_KEY) then appends 34-v-stub-probe.sh.
# 50 runs with RECLAIM_PODMAN_PAUSE=1: a stale uid-995 podman pause process after a dr run is expected ($PREV_FREEZE notes), so the chain reclaims
# exactly that process (SIGTERM, re-verified in the same remote command) instead of stopping at the 50 precondition.
. "$(dirname -- "${BASH_SOURCE[0]}")/../freeze-guard.sh" || exit 2
C=$FREEZE_COMMIT
. "$(dirname -- "${BASH_SOURCE[0]}")/../paths.sh"
cd "$KIT_DIR/guest" || exit 2
[ -n "${TBA_PW:-}" ] || { echo "export TBA_PW (password sudo) before running"; exit 2; }
# GUEST_IP changes after a host restart. Look it up first (PowerShell: Get-NetNeighbor -LinkLayerAddress 00-15-5D-0C-40-02) and export it;
# the values file carries the last seen address. gssh.sh still pins the host key (StrictHostKeyChecking=yes).
GUEST_IP_DEFAULT=$(sed -n 's/^GUEST_IP=//p' "$VALUES" | head -1)
export GUEST_IP=${GUEST_IP:-$GUEST_IP_DEFAULT}
L=$TBOUND_WORK_VIEW/receipts/$FREEZE/logs
make_receipts_dir
SESSION_OPEN=0
for s in "27-archive-state.sh archive" "00-session-sudo.sh open" 05-runtime-swap.sh 10-upgrade.sh 20-r1.sh 50-i1-i2.sh "00-session-sudo.sh close" "29-g1-recover.sh status" 30-reg-g1.sh "40-guest-evidence.sh capability procoracle landlock-connect provider-exchange" 31-reg-rp.sh "40-guest-evidence.sh route-bash-view" 32-reg-dr.sh "40-guest-evidence.sh route-direct-runtime"; do
  n=$(echo "$s" | tr ' ' '-')
  echo "##### $s $(date +%T)"
  if [ "$s" = 50-i1-i2.sh ]; then RECLAIM_PODMAN_PAUSE=1 bash $s > "$L/guest-$n.txt" 2>&1; rc=$?
  else bash $s > "$L/guest-$n.txt" 2>&1; rc=$?; fi
  tail -6 "$L/guest-$n.txt"
  if [ "$rc" != 0 ]; then
    echo "STOP rc=$rc at $s"
    if [ "$SESSION_OPEN" = 1 ]; then
      echo "##### 00-session-sudo.sh close (failure cleanup) $(date +%T)"
      bash 00-session-sudo.sh close >> "$L/guest-00-session-sudo.sh-close.txt" 2>&1; close_rc=$?
      tail -6 "$L/guest-00-session-sudo.sh-close.txt"
      if [ "$close_rc" = 0 ]; then echo "cleanup: session sudo close succeeded"
      else echo "FATAL: session sudo close failed rc=$close_rc; close it before continuing" >&2; fi
    fi
    exit "$rc"
  fi
  case "$s" in
    "00-session-sudo.sh open") SESSION_OPEN=1;;
    "00-session-sudo.sh close") SESSION_OPEN=0;;
  esac
done
if [ "${CORPUS_PILOT:-0}" = 1 ]; then   # optional tail (owner approval): corpus mode; TBOUND_TRIAL_KEY=<key path> also runs the netsink step
  echo "##### 33-corpus-pilot.sh $(date +%T)"
  bash 33-corpus-pilot.sh > "$L/guest-33-corpus-pilot.sh.txt" 2>&1; rc=$?
  tail -6 "$L/guest-33-corpus-pilot.sh.txt"
  [ "$rc" = 0 ] || { echo "STOP rc=$rc at 33-corpus-pilot.sh"; exit "$rc"; }
fi
if [ "${V_STUB_PROBE:-0}" = 1 ]; then
  [ "${CORPUS_PILOT:-0}" = 1 ] || { echo "V_STUB_PROBE=1 needs CORPUS_PILOT=1: the probe runs on the corpus-mode service the pilot cycle leaves"; exit 2; }
  echo "##### 34-v-stub-probe.sh $(date +%T)"
  bash 34-v-stub-probe.sh > "$L/guest-34-v-stub-probe.sh.txt" 2>&1; rc=$?
  tail -6 "$L/guest-34-v-stub-probe.sh.txt"
  [ "$rc" = 0 ] || { echo "STOP rc=$rc at 34-v-stub-probe.sh"; exit "$rc"; }
  echo "##### 40-guest-evidence.sh v-stub $(date +%T)"
  bash 40-guest-evidence.sh v-stub > "$L/guest-40-guest-evidence.sh-v-stub.txt" 2>&1; rc=$?
  tail -6 "$L/guest-40-guest-evidence.sh-v-stub.txt"
  [ "$rc" = 0 ] || { echo "STOP rc=$rc at 40-guest-evidence.sh v-stub"; exit "$rc"; }
fi
echo "end $(date +%T)"
