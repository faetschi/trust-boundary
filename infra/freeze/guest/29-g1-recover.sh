#!/bin/bash
# 29 [PRIVILEGED WRITE, PASSWORD SUDO] G1-5 owner recovery, step 2 (infra/guest/PROVISIONING-SIGNING.md, section "G1-5"):
# Recovery target: the $PREV_FREEZE cycles ($PREV_G1_RID completed/published, $PREV_RP_RID completed, $PREV_DR_RID terminal UNKNOWN BY DESIGN, no Pi). Their items keep identity checks.
#   "(2) the owner moves /root/ga/reg-g1, /root/ga/state/g1 and /root/ga/state/g1.once aside with the password sudo channel".
# Why: $FREEZE is a re-freeze (the $PREV_FREEZE guest results are not claim-bearing after the dr control-path bug), so the $PREV_FREEZE cycles are archived by
# 27 and the state starts fresh. tbound-op refuses a second g1 while /root/ga/state/g1.once holds the client marker;
# reg-measure g1 dies while /root/ga/reg-g1 exists; and `status g1` would still report the $PREV_FREEZE state "started", so 30-reg-g1.sh would skip the whole RG cycle.
# tbound-op has NO verb for any of this, so it is done with the password sudo channel, exactly like 00-session-sudo.sh (TBA_PW from the
# environment, sent on the stdin of the ssh session into `sudo -S -p ''`, never in argv, never written, never printed; xtrace refused).
#
# RECONCILIATION GATE ($FREEZE; route 28 only): a request that CAN be abandoned must be reconciled BEFORE anything moves, or the $FREEZE service would refuse
# the new g1 request as prior-workflow-unsettled (one-prior-at-a-time rule). a manual tbound-op abandon does that on the $PREV_FREEZE guest (`tbound-op abandon <rid>`,
# two-step token flow). The $PREV_FREEZE rp request completed and $FREEZE is a re-freeze, so the $FREEZE order uses option A (27) instead and this gate is not
# reached; it stays for the route where abandon is possible.
# This script refuses (status mode: reports) unless BOTH hold:
#   - `tbound-op status g1` still shows the $PREV_FREEZE marker client=$PREV_G1_RID (the $PREV_FREEZE items are what this script moves), and
#   - the request journal (serve-pi-requests.jsonl, `tbound-op collect requests`, decoded by reqstate.py) shows the $PREV_FREEZE request as
#     terminal + abandoned record (verdict `abandoned`) or as `completed`.
# Any other verdict (unreconciled, no-terminal, terminal-other, inconsistent, ambiguous, not-journaled) stops it. A copy of the journal it judged on is
# kept in $HF/g1-prior/. Nothing is abandoned here.
#
# OPTION A ($PREV_FREEZE state archived for the $FREEZE re-freeze): 27-archive-state.sh already moved
# every item below (and the whole service state, plus the CONTENT
# of the live root /var/lib/tbound/live/root, whose directory itself 27 keeps) into /root/ga/archive-$PREV_LC-<UTC>/rootfs/. Then nothing is left here:
# status and move report "nothing left to move" (no gate: nothing moves, both $PREV_FREEZE requests are archived with their journal), prove that the g1 state and
# marker are gone, and exit 0. An unfinished 27 archive dir is a stop. Only /root/ga/archive-$PREV_LC-* counts: the older /root/ga/archive-f10-*,
# /root/ga/archive-f9-*, /root/ga/archive-f8-*, /root/ga/archive-f7-*, /root/ga/archive-f6-*, /root/ga/archive-f5-* and /root/ga/archive-f4-* directories
# (the F10, F9, F8, F7, F6, F5 and F4 states archived by the $PREV_FREEZE, F10, F9, F8, F7, F6 and F5 kits) are reported as ignored
# and never mistaken for this run's archive.
#
# What moves (all into the root-only directory /root/ga/aside-$PREV_LC-g1/, nothing is deleted, nothing is overwritten; the F3 kit's /root/ga/aside-f3-g1 stays):
#   G1-5 step 2, as written in the runbook:
#     /root/ga/reg-g1                      -> reg-g1                 (reg-measure g1 refuses while it exists)
#     /root/ga/state/g1                    -> state-g1               ($PREV_FREEZE chain state: state=started, cfg/reg/prof hashes)
#     /root/ga/state/g1.once               -> state-g1.once          (the completed G1 marker client=$PREV_G1_RID)
#   NOT named in G1-5 but required by the dispatcher / by 30-reg-g1.sh (found by reading tbound-op):
#     /root/ga/out/response.txt            -> out-response.txt       (client-run g1 dies: "$GA/out/response.txt exists")
#     /var/tmp/tbound-op/g1                -> export-g1              (30-reg-g1.sh would take the stale $PREV_FREEZE managed-live-root.unsigned.json from here)
#   Leftovers of the same consumed run (kept out of the way so no stale file is read; not blocking):
#     /root/ga/out/response.txt.err, /root/ga/state/g1.client-status, /root/ga/g1 (archive; prof-install g1 would overwrite it in place)
#   Kept in place: /root/ga/state/rids (request ids are never reused), /root/ga/prev/g1 (first-time backups, never overwritten by backup_once).
#
# Safety: before anything moves, every present item is proven to be the $PREV_FREEZE G1 or rp item (marker == $PREV_FREEZE rid; state cfg == sha of the $PREV_FREEZE service config;
# file hashes == the copies in $PREV_HF/g1). One unexpected item stops the whole script (nothing moved). The tbound-op lock (/run/tbound-op.lock) is
# taken, tbound-pi service and socket must be inactive (this script calls `tbound-op svc stop` first), an existing aside target is never overwritten.
# Re-running after a successful move is a no-op. Do NOT run it after 30-reg-g1.sh has started the $FREEZE cycle (the identity checks refuse: the new
# state/marker are not the $PREV_FREEZE ones).
#
# Usage:  read -rs TBA_PW; export TBA_PW      (same Git Bash window; the owner-authorized password)
#         bash 29-g1-recover.sh status        read-only report (needs TBA_PW to look into /root/ga)
#         bash 29-g1-recover.sh move          the recovery; run AFTER 27 (option A; or 28 if abandon is possible), 10, 20, 50 and `00-session-sudo.sh close`, BEFORE 30-reg-g1.sh
set -euo pipefail
case "$-" in *x*) echo "FATAL: refusing to run under xtrace (it would print the password)" >&2; exit 1;; esac
case ":${SHELLOPTS:-}:" in *:xtrace:*) echo "FATAL: SHELLOPTS has xtrace" >&2; exit 1;; esac
unset BASH_XTRACEFD || true
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 29-g1-recover

MODE=${1:-}
case "$MODE" in status|move) ;; *) die "usage: bash 29-g1-recover.sh status|move";; esac

ASIDE=/root/ga/aside-$PREV_LC-g1
# Password-sudo transport, as in 00-session-sudo.sh: the password goes on the stdin of the ssh session, sudo reads it with -S and no prompt.
need_pw() { [ -n "${TBA_PW:-}" ] || die "TBA_PW is not set (read -rs TBA_PW; export TBA_PW)"; }
GPW() { printf '%s\n' "$TBA_PW" | sh "$GSSH_SH" "$@"; }
pw() { GPW "sudo -S -p '' $*"; }

load_env
preflight_op
need_pw
[ "$(pw 'id -u' | tr -d '\r')" = 0 ] || die "password sudo did not give uid 0"
ok "password sudo works"

# ---- remote scripts (single-quoted here: no local expansion; they contain no single quote) ---------------------------------------------
REMOTE_PRESENT='for p in /root/ga/reg-g1 /root/ga/state/g1 /root/ga/state/g1.once /root/ga/state/g1.client-status /root/ga/out/response.txt /root/ga/out/response.txt.err /root/ga/g1 /var/tmp/tbound-op/g1 /root/ga/reg-rp /root/ga/state/rp /root/ga/state/rp.once /root/ga/state/rp.client-status /root/ga/out/probe-response.txt /root/ga/out/probe-response.txt.err /root/ga/rp /var/tmp/tbound-op/rp /root/ga/reg-dr /root/ga/state/dr /root/ga/state/dr.once /root/ga/state/dr.client-status /root/ga/out/dr-response.txt /root/ga/out/dr-response.txt.err /root/ga/dr /var/tmp/tbound-op/dr; do if [ -e "$p" ] || [ -L "$p" ]; then echo "present $p"; else echo "absent $p"; fi; done; if [ -d /root/ga/aside-$PREV-g1 ]; then echo "aside-dir present"; ls -A /root/ga/aside-$PREV-g1 | sed "s/^/aside: /"; else echo "aside-dir absent"; fi; for d in /root/ga/archive-$PREV-*; do if [ -d "$d" ]; then if [ -e "$d/ARCHIVE-DONE" ]; then echo "archive-done $d"; else echo "archive-partial $d"; fi; fi; done; for d in /root/ga/archive-f10-*; do if [ -d "$d" ]; then echo "older-archive-f10 $d (ignored)"; fi; done; for d in /root/ga/archive-f9-*; do if [ -d "$d" ]; then echo "older-archive-f9 $d (ignored)"; fi; done; for d in /root/ga/archive-f8-*; do if [ -d "$d" ]; then echo "older-archive-f8 $d (ignored)"; fi; done; for d in /root/ga/archive-f7-*; do if [ -d "$d" ]; then echo "older-archive-f7 $d (ignored)"; fi; done; for d in /root/ga/archive-f6-*; do if [ -d "$d" ]; then echo "older-archive-f6 $d (ignored)"; fi; done; for d in /root/ga/archive-f5-*; do if [ -d "$d" ]; then echo "older-archive-f5 $d (ignored)"; fi; done; for d in /root/ga/archive-f4-*; do if [ -d "$d" ]; then echo "older-archive-f4 $d (ignored)"; fi; done; true'
REMOTE_SHA='for p in /root/ga/reg-g1/managed-live-root.unsigned.json /root/ga/out/response.txt /root/ga/g1/pi-host-profile.json /root/ga/g1/managed-live-root.json /var/tmp/tbound-op/g1/managed-live-root.unsigned.json /root/ga/reg-rp/managed-live-root.unsigned.json /root/ga/out/probe-response.txt /root/ga/rp/pi-host-profile.json /root/ga/rp/managed-live-root.json /root/ga/reg-dr/managed-live-root.unsigned.json /root/ga/out/dr-response.txt /root/ga/dr/pi-host-profile.json /root/ga/dr/managed-live-root.json; do if [ -f "$p" ]; then sha256sum "$p"; fi; done'
REMOTE_MOVE='set -eu; umask 077
exec 9>/run/tbound-op.lock
flock -w 5 9 || { echo "tbound-op lock busy: a verb or a client-run is running"; exit 4; }
for u in tbound-pi.service tbound-pi.socket; do s=$(systemctl is-active $u || true); case $s in inactive|failed) ;; *) echo "$u is $s: svc stop first"; exit 5;; esac; done
A=/root/ga/aside-$PREV-g1
ITEMS="/root/ga/reg-g1:reg-g1 /root/ga/state/g1:state-g1 /root/ga/state/g1.once:state-g1.once /root/ga/out/response.txt:out-response.txt /var/tmp/tbound-op/g1:export-g1 /root/ga/out/response.txt.err:out-response.txt.err /root/ga/state/g1.client-status:state-g1.client-status /root/ga/g1:archive-g1 /root/ga/reg-rp:reg-rp /root/ga/state/rp:state-rp /root/ga/state/rp.once:state-rp.once /root/ga/state/rp.client-status:state-rp.client-status /root/ga/out/probe-response.txt:out-probe-response.txt /root/ga/out/probe-response.txt.err:out-probe-response.txt.err /root/ga/rp:archive-rp /var/tmp/tbound-op/rp:export-rp /root/ga/reg-dr:reg-dr /root/ga/state/dr:state-dr /root/ga/state/dr.once:state-dr.once /root/ga/state/dr.client-status:state-dr.client-status /root/ga/out/dr-response.txt:out-dr-response.txt /root/ga/out/dr-response.txt.err:out-dr-response.txt.err /root/ga/dr:archive-dr /var/tmp/tbound-op/dr:export-dr"
for it in $ITEMS; do src=${it%%:*}; n=${it##*:}
  if { [ -e "$src" ] || [ -L "$src" ]; } && { [ -e "$A/$n" ] || [ -L "$A/$n" ]; }; then echo "REFUSE: both $src and $A/$n exist"; exit 3; fi
done
install -d -o root -g root -m 0700 "$A"
for it in $ITEMS; do src=${it%%:*}; n=${it##*:}
  if [ -e "$src" ] || [ -L "$src" ]; then mv -- "$src" "$A/$n"; echo "moved $src -> $A/$n"; else echo "absent $src (nothing to move)"; fi
done'

# ---- 1. what is on the guest ---------------------------------------------------------------------------------------------------------
say "1. g1 recovery items on the guest (password sudo, read-only)"
PRESENT=$(pw "env PREV=$PREV_LC PREVF=$PREV_FREEZE FREEZEF=$FREEZE sh -c '$REMOTE_PRESENT'" | tr -d '\r')
printf '%s\n' "$PRESENT"
present() { grep -qx "present $1" <<<"$PRESENT"; }
say "tbound-op status g1 (narrowed sudo)"
ST0=$(op "status g1" | tr -d '\r'); printf '%s\n' "$ST0"
[[ $ST0 == *"pins F=$F"* ]] || die "tbound-op pins are not $FREEZE ($F): run 10-upgrade.sh first (G1-5 step 3: the new pins.env with the new TPL_G1)"
[[ $ST0 == *"anchor: ok"* ]] || die "anchor not ok"

# ---- 2. identity checks: every present item must be the $PREV_FREEZE item ----------------------------------------------------------------------
say "2. identity checks against the $PREV_FREEZE record ($PREV_HF/g1, PREV_G1_RID)"
eq "$PREV_FREEZE g1/RID record == PREV_G1_RID" "$(prev_get g1/RID)" "$PREV_G1_RID"
if present /root/ga/state/g1.once; then
  ONCE=$(pw 'cat /root/ga/state/g1.once' | tr -d '\r')
  eq "one-use marker /root/ga/state/g1.once" "$ONCE" "client=$PREV_G1_RID"
fi
if present /root/ga/state/g1; then
  GST=$(pw 'cat /root/ga/state/g1' | tr -d '\r'); printf '%s\n' "$GST" | sed 's/^/   state-g1: /'
  eq "state-g1 cfg == sha256 of the $PREV_FREEZE g1 service config" "$(printf '%s\n' "$GST" | sed -n 's/^cfg=//p')" "$(wd "$PREV_HF/g1/serve-pi-service.json")"
fi
if present /root/ga/state/g1.client-status; then
  CST=$(pw 'cat /root/ga/state/g1.client-status' | tr -d '\r'); echo "client-status: $CST"
  eq "g1.client-status == $PREV_FREEZE client-run.status" "$CST" "$(prev_get g1/client-run.status)"
fi
SHAOUT=$(pw "env PREV=$PREV_LC PREVF=$PREV_FREEZE FREEZEF=$FREEZE sh -c '$REMOTE_SHA'" | tr -d '\r'); printf '%s\n' "$SHAOUT" | sed 's/^/   sha256: /'
shaof() { printf '%s\n' "$SHAOUT" | sed -n "s|^\([0-9a-f]\{64\}\)  $1\$|sha256:\1|p"; }
check_same() { # guest path, owner file in $PREV_HF
  local g; g=$(shaof "$1"); [ -n "$g" ] || return 0
  eq "$1 == $PREV_FREEZE record $2" "$g" "$(wd "$PREV_HF/$2")"
}
check_same /root/ga/reg-g1/managed-live-root.unsigned.json g1/managed-live-root.unsigned.json
check_same /root/ga/out/response.txt g1/response.txt
check_same /root/ga/g1/pi-host-profile.json g1/pi-host-profile.signed.json
check_same /root/ga/g1/managed-live-root.json g1/managed-live-root.json
check_same /var/tmp/tbound-op/g1/managed-live-root.unsigned.json g1/managed-live-root.unsigned.json
eq "$PREV_FREEZE rp/RID" "$(prev_get rp/RID)" "$PREV_RP_RID"
if present /root/ga/state/rp.once; then
  eq "$PREV_FREEZE rp marker" "$(pw 'cat /root/ga/state/rp.once' | tr -d '\r')" "client=$PREV_RP_RID"
fi
if present /root/ga/state/rp; then
  RPCFG=$(pw 'sed -n "s/^cfg=//p" /root/ga/state/rp' | tr -d '\r')
  eq "$PREV_FREEZE rp config" "$RPCFG" "$(wd "$PREV_HF/rp/serve-pi-service.json")"
fi
if present /root/ga/state/rp.client-status; then
  eq "$PREV_FREEZE rp client status" "$(pw 'cat /root/ga/state/rp.client-status' | tr -d '\r')" "$(prev_get rp/client-run.status)"
fi
check_same /root/ga/reg-rp/managed-live-root.unsigned.json rp/managed-live-root.unsigned.json
check_same /root/ga/out/probe-response.txt rp/response.txt
check_same /root/ga/rp/pi-host-profile.json rp/pi-host-profile.signed.json
check_same /root/ga/rp/managed-live-root.json rp/managed-live-root.json
eq "$PREV_FREEZE dr/RID" "$(prev_get dr/RID)" "$PREV_DR_RID"
if present /root/ga/state/dr.once; then
  eq "$PREV_FREEZE dr marker" "$(pw 'cat /root/ga/state/dr.once' | tr -d '\r')" "client=$PREV_DR_RID"
fi
if present /root/ga/state/dr; then
  DRCFG=$(pw 'sed -n "s/^cfg=//p" /root/ga/state/dr' | tr -d '\r')
  eq "$PREV_FREEZE dr config" "$DRCFG" "$(wd "$PREV_HF/dr/serve-pi-service.json")"
fi
if present /root/ga/state/dr.client-status; then
  eq "$PREV_FREEZE dr client status" "$(pw 'cat /root/ga/state/dr.client-status' | tr -d '\r')" "$(prev_get dr/client-run.status)"
fi
check_same /root/ga/reg-dr/managed-live-root.unsigned.json dr/managed-live-root.unsigned.json
check_same /root/ga/out/dr-response.txt dr/response.txt
check_same /root/ga/dr/pi-host-profile.json dr/pi-host-profile.signed.json
check_same /root/ga/dr/managed-live-root.json dr/managed-live-root.json
SRC_ITEMS=$(grep -c '^present ' <<<"$PRESENT" || true)
if [ "$SRC_ITEMS" = 0 ]; then
  if grep -qx 'aside-dir present' <<<"$PRESENT"; then
    ok "nothing left to move: the $PREV_FREEZE g1 items are already in $ASIDE"
  elif grep -q '^archive-done ' <<<"$PRESENT"; then
    ok "nothing left to move: 27-archive-state.sh already moved the $PREV_FREEZE g1 items (and the service state, and the live-root content) into $(grep '^archive-done ' <<<"$PRESENT" | head -1 | cut -d' ' -f2)"
  elif grep -q '^archive-partial ' <<<"$PRESENT"; then
    die "an unfinished 27-archive-state.sh archive exists ($(grep '^archive-partial ' <<<"$PRESENT" | head -1 | cut -d' ' -f2)): re-run bash 27-archive-state.sh archive first"
  else
    die "no $PREV_FREEZE g1 item on the guest, no $ASIDE and no archive-$PREV_LC-* directory: nothing to recover (the state is not the one this script was written for)"
  fi
fi

# ---- 3. reconciliation gate (route 28 only): the $PREV_FREEZE rp request must be terminal + abandoned (a manual tbound-op abandon) or completed -------------------------------
say "3. reconciliation gate for the $PREV_FREEZE rp request $PREV_RP_RID (status g1 + request journal)"
OUT=/var/tmp/tbound-op
MARK=$(printf '%s\n' "$ST0" | sed -n 's/.*client=\(g1-req-[0-9TZ]*\).*/\1/p' | head -1)
if [ "$SRC_ITEMS" = 0 ]; then
  ok "nothing left to move: no gate needed (the $PREV_FREEZE items are already aside)"
else
  if [ -n "$MARK" ]; then eq "tbound-op status g1: one-use marker" "$MARK" "$PREV_G1_RID"; else note "tbound-op status g1 shows no client marker (the state files above were identity-checked)"; fi
  WX mkdir -p -m 0700 "$HF/g1-prior"
  TS29=$(date -u +%Y%m%dT%H%M%SZ)
  op "collect requests"
  fetch_export "$OUT/_/serve-pi-requests.jsonl" "g1-prior/requests-29-$TS29.jsonl"
  req_check "$HF/g1-prior/requests-29-$TS29.jsonl" "$PREV_RP_RID"
  if req_reconciled; then
    ok "the $PREV_FREEZE rp request $PREV_RP_RID is reconciled (journal verdict: $REQ_VERDICT)"
  else
    GATEMSG="the $PREV_FREEZE rp request $PREV_RP_RID is NOT reconciled (journal verdict: $REQ_VERDICT). It needs a terminal plus an abandoned record, or completion. Run a manual tbound-op abandon (tbound-op abandon, two-step token) while the $PREV_FREEZE rp registration is still installed. If 10-upgrade.sh already ran at $FREEZE and the request is still unreconciled, stop and decide by hand: the $PREV_FREEZE registration binds the $PREV_FREEZE runner and is stale"
    if [ "$MODE" = move ]; then die "$GATEMSG"; else note "$GATEMSG (move would refuse)"; fi
  fi
fi

if [ "$MODE" = status ]; then
  note "status only: nothing changed. Next: bash 29-g1-recover.sh move"
  done_step; exit 0
fi

# ---- 4. the move ---------------------------------------------------------------------------------------------------------------------
if [ "$SRC_ITEMS" != 0 ]; then
  say "4. PRIVILEGED WRITE: tbound-op svc stop, then move the items aside (password sudo)"
  op "svc stop"
  svc_inactive
  pw "env PREV=$PREV_LC PREVF=$PREV_FREEZE FREEZEF=$FREEZE sh -c '$REMOTE_MOVE'" | tr -d '\r'
fi

# ---- 5. proof --------------------------------------------------------------------------------------------------------------------------
say "5. proof"
AFTER=$(pw "env PREV=$PREV_LC PREVF=$PREV_FREEZE FREEZEF=$FREEZE sh -c '$REMOTE_PRESENT'" | tr -d '\r'); printf '%s\n' "$AFTER"
for p in /root/ga/reg-g1 /root/ga/state/g1 /root/ga/state/g1.once /root/ga/out/response.txt /var/tmp/tbound-op/g1 /root/ga/reg-rp /root/ga/state/rp /root/ga/state/rp.once /root/ga/state/rp.client-status /root/ga/out/probe-response.txt /root/ga/out/probe-response.txt.err /root/ga/rp /var/tmp/tbound-op/rp /root/ga/reg-dr /root/ga/state/dr /root/ga/state/dr.once /root/ga/state/dr.client-status /root/ga/out/dr-response.txt /root/ga/out/dr-response.txt.err /root/ga/dr /var/tmp/tbound-op/dr; do
  grep -qx "absent $p" <<<"$AFTER" || die "$p is still present"
done
ok "reg-g1, state/g1, state/g1.once, out/response.txt and the g1 export dir are gone from their places"
if grep -qx 'aside-dir present' <<<"$AFTER"; then pw "ls -la $ASIDE" | tr -d '\r'; else note "no $ASIDE: the $PREV_FREEZE g1 items were archived by 27-archive-state.sh (which also emptied the live root, keeping the directory itself)"; fi
ST=$(op "status g1" | tr -d '\r'); printf '%s\n' "$ST"
grep -qx -- '-- g1: no state' <<<"$ST" || die "tbound-op status g1 still reports a g1 state"
[[ $ST != *"-- g1 one-use markers"* ]] || die "tbound-op status g1 still reports a one-use marker"
ok "tbound-op status g1: no state, no one-use marker (the dispatcher accepts cfg-install g1 / client-run g1 again)"
RPST=$(op "status rp" | tr -d '\r'); printf '%s\n' "$RPST"
grep -qx -- '-- rp: no state' <<<"$RPST" || die "tbound-op status rp still reports an rp state"
[[ $RPST != *"-- rp one-use markers"* ]] || die "tbound-op status rp still reports a one-use marker"
DRST=$(op "status dr" | tr -d '\r'); printf '%s\n' "$DRST"
grep -qx -- '-- dr: no state' <<<"$DRST" || die "tbound-op status dr still reports a dr state"
[[ $DRST != *"-- dr one-use markers"* ]] || die "tbound-op status dr still reports a one-use marker"

if G 'sudo -n true' >/dev/null 2>&1; then die "sudo -n true works: the narrowed sudo is not in place"; fi
ok "sudo -n true fails (narrowed sudo unchanged; this script only used password sudo)"
note "G1-5 step 2 done. Step 3 (new pins.env / TPL_G1) was done by 10-upgrade.sh. Next: bash 30-reg-g1.sh (the full RG cycle, two new signatures into $HF/g1)"
done_step
