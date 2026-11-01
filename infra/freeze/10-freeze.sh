#!/bin/bash
# $FREEZE [READ-ONLY on the repo] freeze: check HEAD == <commit> == FREEZE_COMMIT of $KIT_FREEZE.env (set it there first), F0, F1, F2, F3, F4, F5, F6, F7, F8, F9, F10 and $PREV_FREEZE are ancestors, build inputs clean; create new W/B; keep H; write env.sh.
# Usage: KIT_FREEZE=$FREEZE bash 10-freeze.sh <commit>      (full 40-hex hash or unambiguous prefix; HEAD and FREEZE_COMMIT in the values file must both be exactly that commit)
set -euo pipefail
umask 077
. "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
logto 10-freeze

[ $# -eq 1 ] || die "usage: 10-freeze.sh <commit>"
ARG=$1
[[ $ARG =~ ^[0-9a-f]{7,40}$ ]] || die "argument must be lowercase hex (7..40 chars): $ARG"
if [ -e "$TBOUND_WORK/env.sh" ]; then
  OLD_F=$(sed -n 's/^F=//p' "$TBOUND_WORK/env.sh" | head -1)
  [ "$OLD_F" != "$FREEZE_COMMIT" ] || die "$TBOUND_WORK/env.sh already froze $FREEZE ($FREEZE_COMMIT): this freeze already ran. Make a new values file for another freeze."
  echo "NOTE: replacing $TBOUND_WORK/env.sh of an earlier freeze ($OLD_F) with $FREEZE"
fi
mkdir -p "$LOG"

REPO=$REPO_ROOT
git_env
git -C "$REPO" rev-parse --show-toplevel

# 1. HEAD == argument == FREEZE_COMMIT; $FREEZE descends from $PREV_FREEZE, F10, F9, F8, F7, F6, F5, F4, F3, F2, F1 and F0.
FARG=$(git -C "$REPO" rev-parse --verify "${ARG}^{commit}")
F=$(git -C "$REPO" rev-parse --verify 'HEAD^{commit}')
echo "F=$F"
[ "$F" = "$FARG" ] || die "HEAD ($F) != argument ($FARG)"
[ "$F" = "$FREEZE_COMMIT" ] || die "HEAD ($F) != FREEZE_COMMIT in $VALUES ($FREEZE_COMMIT)"
[ "$F" != "$F0COMMIT" ] || die "$FREEZE equals F0"
[ "$F" != "$F1COMMIT" ] || die "$FREEZE equals F1"
[ "$F" != "$F2COMMIT" ] || die "$FREEZE equals F2"
[ "$F" != "$F3COMMIT" ] || die "$FREEZE equals F3"
[ "$F" != "$F4COMMIT" ] || die "$FREEZE equals F4"
[ "$F" != "$F5COMMIT" ] || die "$FREEZE equals F5: nothing to re-freeze"
[ "$F" != "$F6COMMIT" ] || die "$FREEZE equals F6: nothing to re-freeze"
[ "$F" != "$F7COMMIT" ] || die "$FREEZE equals F7: nothing to re-freeze"
[ "$F" != "$F8COMMIT" ] || die "$FREEZE equals F8: nothing to re-freeze"
[ "$F" != "$F9COMMIT" ] || die "$FREEZE equals F9: nothing to re-freeze"
[ "$F" != "$F10COMMIT" ] || die "$FREEZE equals F10: nothing to re-freeze"
[ "$F" != "$PREV_COMMIT" ] || die "$FREEZE equals $PREV_FREEZE: nothing to re-freeze"
git -C "$REPO" merge-base --is-ancestor "$F0COMMIT" "$F" || die "F0 ($F0COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F1COMMIT" "$F" || die "F1 ($F1COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F2COMMIT" "$F" || die "F2 ($F2COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F3COMMIT" "$F" || die "F3 ($F3COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F4COMMIT" "$F" || die "F4 ($F4COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F5COMMIT" "$F" || die "F5 ($F5COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F6COMMIT" "$F" || die "F6 ($F6COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F7COMMIT" "$F" || die "F7 ($F7COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F8COMMIT" "$F" || die "F8 ($F8COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F9COMMIT" "$F" || die "F9 ($F9COMMIT) is not an ancestor of $FREEZE"
git -C "$REPO" merge-base --is-ancestor "$F10COMMIT" "$F" || die "F10 ($F10COMMIT) is not an ancestor of $FREEZE"
if has_prev; then git -C "$REPO" merge-base --is-ancestor "$PREV_COMMIT" "$F" || die "$PREV_FREEZE ($PREV_COMMIT) is not an ancestor of $FREEZE"; fi

# 2. Build inputs clean. Same check as F0: WSL git lacks the Windows core.autocrlf=true, so pass it explicitly.
# The values file names this very commit, so it cannot be part of it.
ST=$(git -C "$REPO" -c core.autocrlf=true status --porcelain -- supervisor adapter scripts packaging infra corpus ':(exclude)infra/freeze/freezes')
echo "status-porcelain-bytes=${#ST}"; echo "$ST" | head
[ -z "$ST" ] || die "dirty build inputs"

# evq-capture runs tests under $CF/work/tmp. The longest test socket path adds 75 bytes, and a Unix socket path must stay under 108.
[ "$(printf %s "$CF" | wc -c)" -le 28 ] || die "$CF is over the 28-byte limit: Unix socket tests would fail after 25 minutes. Put the freeze home on a shorter path (OLDH)"

# 3. The kept H (owner keys + F0 outputs): names only, never contents.
H=$OLDH
[ -d "$H" ] || die "H missing: $H"
[ "$(stat -c %a "$H")" = 700 ] || die "H is not 0700"
[ "$(stat -c %U "$H")" = "$(id -un)" ] || die "H not owned by $(id -un)"
for n in admin.key admin.pub cosign-gen.key cosign-gen.pub cosign.pub ADMIN_PUB_DIGEST COSIGN_PUB_DIGEST; do
  [ -e "$H/$n" ] || die "H lacks $n"
done
if has_prev; then
for n in pi-cell-oci pi-cell-load.tar BUNDLE_DIGEST; do [ -e "$H/$n" ] || die "H lacks $n"; done
[ "$(cat "$H/FROZEN-COMMIT")" = "$F0COMMIT" ] || die "H/FROZEN-COMMIT is not F0"
[ "$(cat "$PREV_HF/FROZEN-COMMIT")" = "$PREV_COMMIT" ] || die "H/$PREV_LC/FROZEN-COMMIT is not $PREV_FREEZE (the $PREV_FREEZE outputs are the comparison base)"
for n in IMGDIGEST CFG TBOUND_DIGEST TBP_DIGEST ENTRYPOINT_DIGEST BASE_TD LANDLOCK_DIGEST XATTR_VIS_DIGEST XATTR_MOPT XATTR_MAGIC CONTAINERS_CONF_DIGEST STORAGE_CONF_DIGEST CGROUP_ROOT_DIGEST pi-cell-oci CAL_RO_DIGEST CAL_RW_DIGEST cal-ro.unsigned.json cal-rw.unsigned.json d09-xattr-enumeration-calibration.json d09-xattr-enumeration-calibration.ed25519 d09-xattr-enumeration-calibration-readonly.json d09-xattr-enumeration-calibration-readonly.ed25519 LOAD_TAR_DIGEST TEST_PROCORACLE TEST_ENTRYPOINT g1/serve-pi-service.json g1/RID g1/CLIENT_STATUS g1/client-run.status g1/response.txt g1/managed-live-root.unsigned.json g1/managed-live-root.json g1/pi-host-profile.signed.json rp/RID rp/serve-pi-service.json rp/response.txt rp/client-run.status rp/RUNID rp/CHECKS_OK dr/RID dr/RUNID dr/CHECKS_OK dr/serve-pi-service.json dr/response.txt dr/client-run.status dr/ADMISSION_ID dr/CLIENT_STATUS; do [ -e "$PREV_HF/$n" ] || die "H/$PREV_LC lacks $n"; done
fi
echo "H names:"; ls -A "$H"
if has_prev; then
check_prev_values
[ "$(cat "$PREV_HF/g1/RID")" = "$PREV_G1_RID" ] || die "$PREV_HF/g1/RID is not PREV_G1_RID ($PREV_G1_RID)"
[ "$(cat "$PREV_HF/rp/RID")" = "$PREV_RP_RID" ] || die "$PREV_HF/rp/RID is not PREV_RP_RID ($PREV_RP_RID)"
[ "$(cat "$PREV_HF/dr/RID")" = "$PREV_DR_RID" ] || die "$PREV_HF/dr/RID is not PREV_DR_RID ($PREV_DR_RID)"
# 3b. Fresh admission ids, read-only. Otherwise a reused id is found only on the guest, after 27 archive and 05 swap
# (10-upgrade.sh:53-57, 30-reg-g1.sh:102-105, 31-reg-rp.sh:44-48, 32-reg-dr.sh:52-63).
adm_of() { python3 -I -c 'import json,sys; print(json.load(sys.stdin)["workflow_admission_id"])'; }
for pair in g1:g1 route-probe:rp; do
  tpl=${pair%%:*}; nm=${pair##*:}
  NEWID=$(git -C "$REPO" show "$F:infra/guest/serve-pi-service.$tpl.json" | adm_of) || die "cannot read the $tpl admission id of $F"
  OLDID=$(adm_of < "$PREV_HF/$nm/serve-pi-service.json") || die "cannot read the $PREV_FREEZE $nm admission id"
  [ -n "$NEWID" ] && [ "$NEWID" != "$OLDID" ] || die "the $FREEZE $tpl template carries the $PREV_FREEZE admission id '$OLDID': commit a new id first"
done
[ "$DR_ADMISSION_ID" != "$PREV_DR_ADMISSION_ID" ] || die "DR_ADMISSION_ID equals the $PREV_FREEZE dr id"
for spent in $DR_ADMISSION_REFUSE; do [ "$DR_ADMISSION_ID" != "$spent" ] || die "DR_ADMISSION_ID $spent is in DR_ADMISSION_REFUSE"; done
echo "OK admission ids: g1 and route-probe templates differ from $PREV_FREEZE, dr id is new"
fi
if [ -e "$HF" ] && [ -n "$(ls -A "$HF")" ]; then die "$HF already has content (runbook rule 4: new freeze outputs go to a new, empty directory)"; fi

# 4. Only now create directories (a failed check leaves nothing behind).
W=$(mktemp -d -p "$HOME" tbound-w.XXXXXX)
B=$(mktemp -d -p "$HOME" tbound-b.XXXXXX)
mkdir -p "$HF"; chmod 0700 "$HF"
stat -c '%a %U %n' "$W" "$B" "$H" "$HF"
stat -f -c 'fs=%T %n' "$W" "$HF"
echo "$F" > "$HF/FROZEN-COMMIT"

printf 'W=%s\nB=%s\nH=%s\nF=%s\nREPO=%q\nHF=%s\nCF=%s\nLOG=%s\n' "$W" "$B" "$H" "$F" "$REPO" "$HF" "$CF" "$LOG" > "$TBOUND_WORK/env.sh"
cat "$TBOUND_WORK/env.sh"
ls -la "$HF"
finish_step "$TBOUND_WORK/env.sh"
echo "== DONE 10-freeze: $FREEZE=$F"
