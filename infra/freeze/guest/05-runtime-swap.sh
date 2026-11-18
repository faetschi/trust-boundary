#!/bin/bash
# 05 [PRIVILEGED WRITE, session sudo OPEN] P11 runtime swap, after 27 archive and before 10-upgrade.
# DRY_RUN=1 checks owner inputs and prints the guest programs; it never calls G/GI or reads key files.
set -euo pipefail
case "$-" in *x*) echo "FATAL: refusing to run under xtrace" >&2; exit 1;; esac
case ":${SHELLOPTS:-}:" in *:xtrace:*) echo "FATAL: SHELLOPTS has xtrace" >&2; exit 1;; esac
unset BASH_XTRACEFD || true
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 05-runtime-swap
case "${DRY_RUN:=0}" in 0|1) ;; *) die "DRY_RUN must be 0 or 1";; esac
# Unlike load_env, P11 needs only P1/P2 records, not later image/signing records.
. "$TBOUND_WORK_VIEW/env.sh"; env_paths_ok
for v in W H F HF; do [ -n "${!v:-}" ] || die "env.sh lacks $v"; done
eq "env.sh commit == frozen values" "$F" "$FREEZE_COMMIT"
eq "HF path" "$HF" "$H/$FREEZE_LC"
eq "previous HF path" "$PREV_HF" "$H/$PREV_LC"
eq "FROZEN-COMMIT" "$(hf_get FROZEN-COMMIT)" "$F"
BUNDLE_DIGEST=$(hf_get BUNDLE_DIGEST); need_dig BUNDLE_DIGEST "$BUNDLE_DIGEST"
TBP_DIGEST=$(hf_get TBP_DIGEST); need_dig TBP_DIGEST "$TBP_DIGEST"
eq "P1 tbound-profile == TBP_DIGEST" "$(wd "$W/bin/tbound-profile")" "$TBP_DIGEST"
eq "previous frozen commit" "$(prev_get FROZEN-COMMIT)" "$PREV_COMMIT"
eq "previous BUNDLE_DIGEST record" "$(prev_get BUNDLE_DIGEST)" "$PREV_BUNDLE_DIGEST"
eq "previous TBP_DIGEST record" "$(prev_get TBP_DIGEST)" "$PREV_TBP_DIGEST"
need_dig PREV_BUNDLE_DIGEST "$PREV_BUNDLE_DIGEST"
P2_MODE=reuse
if hf_has P2-MODE; then P2_MODE=$(hf_get P2-MODE); fi
SWAP=0
if [ "$BUNDLE_DIGEST" != "$PREV_BUNDLE_DIGEST" ] && [ "$P2_MODE" = rebuild ]; then SWAP=1; fi

# Read-only git diff eed7722..6162e3c and 6162e3c..02f9fb7: VerifyLinuxPiRuntimeBundle, statDevice,
# requireReadOnlyMount, host.go limits, internal/sessionlaunch and profile_tooling_linux.go are unchanged.
# 02f9fb7..2a6492d (F15): launcher_linux.go, host.go and profile_tooling_linux.go are unchanged; internal/sessionlaunch
# changed (#39) but is outside tbound-profile's import closure (GOOS=linux go list -deps ./cmd/tbound-profile).
# Accept only these audited previous-measurer pairs (previous freeze commit, this freeze commit).
ALLOW_PREV_MEASURER=0
if [ "$PREV_COMMIT" = eed772228f9907f9c20df3177648807f5c1aa83d ] &&
   [ "$F" = 6162e3c32379233c7c4f238c5547599474d373a7 ]; then ALLOW_PREV_MEASURER=1; fi
if [ "$PREV_COMMIT" = 6162e3c32379233c7c4f238c5547599474d373a7 ] &&
   [ "$F" = 02f9fb71a26aadf4eebeb0ff217fe97c374ffff8 ]; then ALLOW_PREV_MEASURER=1; fi
if [ "$PREV_COMMIT" = 02f9fb71a26aadf4eebeb0ff217fe97c374ffff8 ] &&
   [ "$F" = 2a6492dda21e5af9eab8e92e81f56725b3f8e9c5 ]; then ALLOW_PREV_MEASURER=1; fi
# 2a6492d..97738b6 (F16): launcher_linux.go, host.go and profile_tooling_linux.go are unchanged (git diff --quiet).
if [ "$PREV_COMMIT" = 2a6492dda21e5af9eab8e92e81f56725b3f8e9c5 ] &&
   [ "$F" = 97738b60125b77be41c4800c7ce1d0fb327557f6 ]; then ALLOW_PREV_MEASURER=1; fi
# 97738b6..8931c69 (F17): launcher_linux.go, host.go and profile_tooling_linux.go are unchanged (git diff --quiet).
if [ "$PREV_COMMIT" = 97738b60125b77be41c4800c7ce1d0fb327557f6 ] &&
   [ "$F" = 8931c69576c2cb58b0c87d8a4ec0e88dd2c162cc ]; then ALLOW_PREV_MEASURER=1; fi
# 8931c69..b26e5ef (F18): launcher_linux.go, profile_tooling_linux.go and construct_evidence_linux.go have deletion-only
# non-test changes, unreachable from runtime-bundle-digest (F17 caller grep and import-closure audit). runRuntimeBundleDigest,
# VerifyLinuxPiRuntimeBundle, statDevice, requireReadOnlyMount, unescapeMountField and host.go limits are byte-identical.
if [ "$PREV_COMMIT" = 8931c69576c2cb58b0c87d8a4ec0e88dd2c162cc ] &&
   [ "$F" = b26e5efdf8ac04cce185de06d771a558d57b42af ]; then ALLOW_PREV_MEASURER=1; fi
# b26e5ef..2641c6b (F20; F19 b5ed5b0 was cut but not qualified, the guest restored to F18): launcher_linux.go, host.go and
# profile_tooling_linux.go are unchanged (git diff --quiet).
if [ "$PREV_COMMIT" = b26e5efdf8ac04cce185de06d771a558d57b42af ] &&
   [ "$F" = 2641c6bc73a501ee1578d6dcdbe652c6086fac53 ]; then ALLOW_PREV_MEASURER=1; fi
# 2641c6b..13ed119 (F23; the guest was restored to F20 after F22's provider-failed route probe): launcher_linux.go, host.go
# and profile_tooling_linux.go are unchanged (git diff --quiet).
if [ "$PREV_COMMIT" = 2641c6bc73a501ee1578d6dcdbe652c6086fac53 ] &&
   [ "$F" = 13ed11939c06e552a2d8662fb752bf2b3640ac68 ]; then ALLOW_PREV_MEASURER=1; fi
# 2641c6b..bbc6ec8 (F21): launcher_linux.go, host.go and profile_tooling_linux.go are unchanged (git diff --quiet).
if [ "$PREV_COMMIT" = 2641c6bc73a501ee1578d6dcdbe652c6086fac53 ] &&
   [ "$F" = bbc6ec85a0e2f4895c6c9e392b1736a1968d62b8 ]; then ALLOW_PREV_MEASURER=1; fi
# 13ed119..de2a3cf (F24): launcher_linux.go, host.go, profile_tooling_linux.go and cmd/tbound-profile/main_linux.go are unchanged
# (git diff --quiet); VerifyLinuxPiRuntimeBundle's callees all live in launcher_linux.go.
if [ "$PREV_COMMIT" = 13ed11939c06e552a2d8662fb752bf2b3640ac68 ] &&
   [ "$F" = de2a3cfdbae532d8a9b5826af6208c96bcf4a302 ]; then ALLOW_PREV_MEASURER=1; fi
# 13ed119..2fa3e00 (F25; F24 was not qualified, G1 UNKNOWN on an inactive provider subscription, the guest restored to F23):
# the same four files are unchanged (git diff --quiet); F25 is de2a3cf plus the F24 kit and fresh admission ids.
if [ "$PREV_COMMIT" = 13ed11939c06e552a2d8662fb752bf2b3640ac68 ] &&
   [ "$F" = 2fa3e008d01b658b16ddbc59ea2063ec5efdc1d3 ]; then ALLOW_PREV_MEASURER=1; fi
# 13ed119..2780072 (F26; F25 was not qualified, corpus mode refused the pre-D50 pilot budgets, the guest restored to F23):
# the same four files are unchanged (git diff --quiet); F26 adds the F25 kit, the D50 pilot budgets (#138) and fresh ids.
if [ "$PREV_COMMIT" = 13ed11939c06e552a2d8662fb752bf2b3640ac68 ] &&
   [ "$F" = 2780072a7b9305d331504197fae9859547b5049b ]; then ALLOW_PREV_MEASURER=1; fi
# 13ed119..db07853 (F27; the guest restored to F23 again, pre-F26 checkpoint): the same four files are unchanged (git diff --quiet);
# no piruntime file changed since F26, and the changed tbound-profile files are off the runtime-bundle-digest path.
if [ "$PREV_COMMIT" = 13ed11939c06e552a2d8662fb752bf2b3640ac68 ] &&
   [ "$F" = db0785367a301b17347701549e680bbdd02e8a5e ]; then ALLOW_PREV_MEASURER=1; fi
# 13ed119..40e651b (F28; F27 not qualified, rp model refusal, the guest restored to F23): the same four files are unchanged
# (git diff --quiet); F28 adds the F27 kit, fresh ids and the route-probe prompt (no Go change).
if [ "$PREV_COMMIT" = 13ed11939c06e552a2d8662fb752bf2b3640ac68 ] &&
   [ "$F" = 40e651b1c661113e488dc5b28946bfb363b91fd7 ]; then ALLOW_PREV_MEASURER=1; fi
# 13ed119..28f407b (F29; F28 verified, the guest restored to F23 for the F29 screening build): the same four files are unchanged
# (git diff --quiet); the changed piruntime files (#181 memo clock) and construct_evidence_linux.go (#169, B6) are off the digest path.
if [ "$PREV_COMMIT" = 13ed11939c06e552a2d8662fb752bf2b3640ac68 ] &&
   [ "$F" = 28f407b84291222f386134c95620c71114f6b0ed ]; then ALLOW_PREV_MEASURER=1; fi
# 13ed119..5626e34 (F30; F29 verified, the guest restored to F23 for the F30 screening build): the same four files are unchanged
# (git diff --quiet); no piruntime or tbound-profile file changed since F29 (#187, #191, #192 and the install stack are off the digest path).
if [ "$PREV_COMMIT" = 13ed11939c06e552a2d8662fb752bf2b3640ac68 ] &&
   [ "$F" = 5626e34fdbf40983949cde67f17238b82800665b ]; then ALLOW_PREV_MEASURER=1; fi
UNIT_SHA=2bc03ead4ba478d81655029de66a46f4bcb6357c5a24e7f8a3940ed947720ea3
OLD=/var/lib/tbound/runtime-src.old-${PREV_BUNDLE_DIGEST:7:8}-$(date -u +%Y%m%dT%H%M%S%NZ)

if [ "$SWAP" = 1 ]; then
  TAR=$(hf_get BUNDLE_TAR_PATH); TARSHA=$(hf_get BUNDLE_TAR_DIGEST)
  need_dig BUNDLE_TAR_DIGEST "$TARSHA"
  eq "P2 tar path" "$TAR" "$W/p2-rebuild/runtime-bundle-${F:0:8}-lf.tar"
  eq "P2 tar sha256 == HF BUNDLE_TAR_DIGEST" "$(wd "$TAR")" "$TARSHA"
  hf_has BUNDLE_TAR_SIZE || die "missing $HF/BUNDLE_TAR_SIZE: record the verified P2 tar size first"
  TAR_SIZE=$(hf_get BUNDLE_TAR_SIZE)
  [[ $TAR_SIZE =~ ^[1-9][0-9]{0,11}$ ]] || die "invalid tar size: $TAR_SIZE"
  eq "P2 tar size (bytes)" "$(WX stat -c %s "$TAR" | tr -d '\r\n')" "$TAR_SIZE"
  MEMBERS=$(WX tar -tf "$TAR" | wc -l | tr -d ' \r\n')
  [[ $MEMBERS =~ ^[1-9][0-9]{0,8}$ ]] || die "invalid tar member count: $MEMBERS"
  ok "tar members = $MEMBERS (used to budget extraction blocks, plus 64 MiB metadata margin)"
  if archived_prev && WX test -s "$HF/g1-prior/archive-done.txt"; then
    ok "27 archive recorded: $(hf_get g1-prior/archive-done.txt)"
  elif [ "$DRY_RUN" = 1 ]; then
    note "PENDING: 27 archive completion record absent; a live run refuses BEFORE guest contact"
  else die "run 27-archive-state.sh archive first: no nonempty $HF/g1-prior/archive-done.txt"; fi
fi

# Quote one argument for the guest's shell; the actual programs execute under bash -c, never a scratch file.
quote() { printf "'%s'" "${1//\'/\'\\\'\'}"; }
BASE=$(printf 'set -euo pipefail\nSVC=%q\nTBPG=%q\nBUNDLE_DIGEST=%q\nPREV_BUNDLE_DIGEST=%q\nUNIT_SHA=%q\n' \
  "$SVC" "$TBPG" "$BUNDLE_DIGEST" "$PREV_BUNDLE_DIGEST" "$UNIT_SHA")
LIB=$(cat <<'GUEST'
mount_check() {
  local out target fs opts
  out=$(findmnt -n -o TARGET,FSTYPE,OPTIONS /var/lib/tbound/runtime) || return 1
  printf '%s\n' "$out"
  read -r target fs opts <<<"$out"
  [[ $target = /var/lib/tbound/runtime && ,$opts, = *,ro,* && ,$opts, = *,nosuid,* && ,$opts, = *,nodev,* ]]
}
measure_expected() {
  local out digest
  out=$(cd / && $SVC "$TBPG" runtime-bundle-digest --root /var/lib/tbound/runtime) || return 1
  printf '%s\n' "$out"
  digest=$(printf '%s\n' "$out" | sed -n 's/^Pi runtime bundle tree digest: \(sha256:[0-9a-f]\{64\}\)$/\1/p')
  [ "$digest" = "$1" ]
}
GUEST
)
CHECK="$BASE
$LIB
$(printf 'TBP_DIGEST=%q\nPREV_TBP_DIGEST=%q\nALLOW_PREV_MEASURER=%q\n' "$TBP_DIGEST" "$PREV_TBP_DIGEST" "$ALLOW_PREV_MEASURER")
"$(cat <<'GUEST'
test "$(id -un)" = tboundadmin
sudo -n true
echo 'session sudo OPEN (sudo -n true works)'
profile_sha=sha256:$(sudo -n sha256sum "$TBPG" | cut -d' ' -f1)
case "$profile_sha" in
  "$TBP_DIGEST") echo 'using current frozen tbound-profile';;
  "$PREV_TBP_DIGEST") test "$ALLOW_PREV_MEASURER" = 1; echo 'using pinned previous-freeze tbound-profile: audited runtime digest algorithm unchanged in this freeze';;
  *) echo "FATAL: unexpected tbound-profile: $profile_sha" >&2; exit 1;;
esac
mount_check
unit_sha=$(sudo -n sha256sum /etc/systemd/system/var-lib-tbound-runtime.mount | cut -d' ' -f1)
printf 'runtime mount unit sha256: %s\n' "$unit_sha"
test "$unit_sha" = "$UNIT_SHA"
sudo -n test -d /var/lib/tbound/runtime-src
sudo -n test ! -L /var/lib/tbound/runtime-src
printf 'runtime-src inode: %s\n' "$(sudo -n stat -c %d:%i /var/lib/tbound/runtime-src)"
cd / && $SVC "$TBPG" runtime-bundle-digest --root /var/lib/tbound/runtime
GUEST
)

if [ "$DRY_RUN" = 1 ]; then
  note "DRY_RUN: GUEST_IP=$GUEST_IP is for printing only; no guest transport will be invoked"
  printf '\nG %s\n' "bash -c $(quote "$CHECK")"
  if [ "$SWAP" = 0 ]; then
    note "verify-only: require measured digest == $BUNDLE_DIGEST, then no swap needed"
    done_step; exit 0
  fi
  note "if CHECK measures $BUNDLE_DIGEST: no swap needed; exit 0 without streaming or writing"
  note "otherwise CHECK must measure $PREV_BUNDLE_DIGEST; runtime-src inode is captured below"
  SOURCE_INODE='<runtime-src inode from CHECK>'
else
  out=$(G "bash -c $(quote "$CHECK")") || die "guest read-only pre-swap checks failed: $out"
  printf '%s\n' "$out"
  CURRENT=$(printf '%s\n' "$out" | tr -d '\r' | sed -n 's/^Pi runtime bundle tree digest: \(sha256:[0-9a-f]\{64\}\)$/\1/p')
  need_dig "installed runtime digest" "$CURRENT"
  if [ "$CURRENT" = "$BUNDLE_DIGEST" ]; then note "no swap needed"; done_step; exit 0; fi
  [ "$SWAP" = 1 ] || die "verify-only: installed runtime $CURRENT != BUNDLE_DIGEST $BUNDLE_DIGEST"
  eq "pre-swap runtime == PREV_BUNDLE_DIGEST" "$CURRENT" "$PREV_BUNDLE_DIGEST"
  SOURCE_INODE=$(printf '%s\n' "$out" | tr -d '\r' | sed -n 's/^runtime-src inode: //p')
  [[ $SOURCE_INODE =~ ^[0-9]+:[0-9]+$ ]] || die "invalid runtime-src inode: $SOURCE_INODE"
fi

TX_BASE="$BASE
$LIB
$(printf 'OLD=%q\nSOURCE_INODE=%q\nTAR_SIZE=%q\nMEMBERS=%q\n' "$OLD" "$SOURCE_INODE" "$TAR_SIZE" "$MEMBERS")"
# flock's child holds the root-owned lock through EXIT rollback; recovery waits, then re-checks state.
LOCK='sudo -n flock -x -w 120 /run/tbound-runtime-swap.lock'
ROLLBACK=$(cat <<'GUEST'
measure_tree() {
  local out digest
  out=$(sudo -n unshare -m bash -euo pipefail -c '
    mount --make-rprivate /
    mount --bind "$1" /var/lib/tbound/runtime
    mount -o remount,bind,ro,nosuid,nodev /var/lib/tbound/runtime
    cd /
    $2 "$3" runtime-bundle-digest --root /var/lib/tbound/runtime
  ' bash "$1" "$SVC" "$TBPG") || return 1
  printf '%s\n' "$out"
  digest=$(printf '%s\n' "$out" | sed -n 's/^Pi runtime bundle tree digest: \(sha256:[0-9a-f]\{64\}\)$/\1/p')
  [ "$digest" = "$2" ]
}
restore_old() {
  echo "ROLLBACK: $OLD -> /var/lib/tbound/runtime-src"
  sudo -n systemctl stop tbound-pi.service tbound-pi.socket || return 1
  sudo -n systemctl stop var-lib-tbound-runtime.mount || return 1
  if sudo -n test -e "$OLD" || sudo -n test -L "$OLD"; then
    sudo -n test -d "$OLD" && sudo -n test ! -L "$OLD" || return 1
    test "$(sudo -n stat -c %d:%i "$OLD")" = "$SOURCE_INODE" || return 1
    measure_tree "$OLD" "$PREV_BUNDLE_DIGEST" || return 1
    if sudo -n test -e /var/lib/tbound/runtime-src || sudo -n test -L /var/lib/tbound/runtime-src; then
      sudo -n test -d /var/lib/tbound/runtime-src && sudo -n test ! -L /var/lib/tbound/runtime-src || return 1
      if measure_tree /var/lib/tbound/runtime-src "$PREV_BUNDLE_DIGEST"; then
        echo "KEEP previous runtime already restored; also preserve $OLD"
      else
        # Preserve an incomplete/unrecognised replacement; never delete a previous good tree.
        sudo -n test ! -e "$OLD.failed" && sudo -n test ! -L "$OLD.failed" || return 1
        sudo -n mv -T -n -- /var/lib/tbound/runtime-src "$OLD.failed" || return 1
        sudo -n test ! -e /var/lib/tbound/runtime-src || return 1
        echo "KEEP failed replacement: $OLD.failed"
      fi
    fi
    if sudo -n test ! -e /var/lib/tbound/runtime-src; then
      sudo -n mv -T -n -- "$OLD" /var/lib/tbound/runtime-src || return 1
      sudo -n test ! -e "$OLD" || return 1
    fi
  else
    # The rename failed, or the guest EXIT trap already restored it. Never delete an unrecognised tree.
    test "$(sudo -n stat -c %d:%i /var/lib/tbound/runtime-src)" = "$SOURCE_INODE" || return 1
  fi
  sudo -n systemctl start var-lib-tbound-runtime.mount || return 1
  mount_check || return 1
  measure_expected "$PREV_BUNDLE_DIGEST" || return 1
}
GUEST
)
RECOVER="$TX_BASE
$ROLLBACK
restore_old || { echo 'FATAL: rollback could not be verified; leave service/socket stopped and preserve the old tree' >&2; exit 1; }
sudo -n systemctl start tbound-pi.socket
systemctl is-active tbound-pi.socket"
TRANSACTION="$TX_BASE
$ROLLBACK
"$(cat <<'GUEST'
QUIESCED=0; MOUNT_TOUCHED=0; SWAP_ARMED=0
finish() {
  local rc=$?
  trap - EXIT HUP INT TERM
  if [ "$SWAP_ARMED" = 1 ]; then
    if ! restore_old; then echo "FATAL: rollback failed; preserve $OLD and leave service/socket stopped" >&2; exit 1; fi
    [ "$rc" != 0 ] || rc=1
  elif [ "$MOUNT_TOUCHED" = 1 ]; then
    sudo -n systemctl start var-lib-tbound-runtime.mount && mount_check && measure_expected "$PREV_BUNDLE_DIGEST" || exit 1
  fi
  if [ "$QUIESCED" = 1 ]; then
    sudo -n systemctl start tbound-pi.socket && systemctl is-active tbound-pi.socket || rc=1
  fi
  exit "$rc"
}
trap finish EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
mount_check
test "$(sudo -n sha256sum /etc/systemd/system/var-lib-tbound-runtime.mount | cut -d' ' -f1)" = "$UNIT_SHA"
measure_expected "$PREV_BUNDLE_DIGEST"
test "$(sudo -n stat -c %d:%i /var/lib/tbound/runtime-src)" = "$SOURCE_INODE"
old_bytes=$(sudo -n du -sx -B1 /var/lib/tbound/runtime-src | cut -f1)
available=$(df -B1 --output=avail /var/lib/tbound | tail -1 | tr -d ' ')
block=$(stat -f -c %S /var/lib/tbound)
[[ $old_bytes =~ ^[0-9]+$ && $available =~ ^[0-9]+$ && $block =~ ^[1-9][0-9]*$ ]]
new_budget=$((TAR_SIZE + MEMBERS * block + 64 * 1024 * 1024))
printf 'space: old=%s retained; new budget=%s; available=%s bytes (old already allocated)\n' "$old_bytes" "$new_budget" "$available"
test "$available" -ge "$new_budget"
sudo -n test ! -e "$OLD"
sudo -n test ! -L "$OLD"
QUIESCED=1
sudo -n systemctl stop tbound-pi.service tbound-pi.socket
for unit in tbound-pi.service tbound-pi.socket; do
  state=$(systemctl show -p ActiveState --value "$unit")
  case "$state" in inactive|failed) ;; *) echo "FATAL: $unit is $state" >&2; exit 1;; esac
done
MOUNT_TOUCHED=1
sudo -n systemctl stop var-lib-tbound-runtime.mount
SWAP_ARMED=1
sudo -n mv -T -n -- /var/lib/tbound/runtime-src "$OLD"
sudo -n test ! -e /var/lib/tbound/runtime-src
sudo -n install -d -o root -g root -m 0555 /var/lib/tbound/runtime-src
sudo -n tar -x -p --numeric-owner -C /var/lib/tbound/runtime-src -f -
sudo -n systemctl start var-lib-tbound-runtime.mount
mount_check
measure_expected "$BUNDLE_DIGEST"
sudo -n systemctl start tbound-pi.socket
systemctl is-active tbound-pi.socket
printf 'KEEP old tree: %s (remove only after the signed profile is installed and verified, P11)\n' "$OLD"
QUIESCED=0; MOUNT_TOUCHED=0; SWAP_ARMED=0
GUEST
)

if [ "$DRY_RUN" = 1 ]; then
  printf '\nWX cat %s | GI %s\n' "$(quote "$TAR")" "$LOCK bash -c $(quote "$TRANSACTION")"
  printf '\n# On owner-side failure/uncertain SSH completion, G %s\n' "$LOCK bash -c $(quote "$RECOVER")"
  note "planned old tree: $OLD (UTC chosen per invocation; never overwritten)"
  done_step; exit 0
fi

owner_exit() {
  local rc=$?
  trap - EXIT
  if [ "$rc" != 0 ]; then
    echo "cleanup: automatic P11 rollback (also covers an uncertain SSH result)"
    G "$LOCK bash -c $(quote "$RECOVER")" || echo "FATAL: rollback unverified (lock timeout or recovery failure); owner must inspect $OLD; do not continue the guest chain" >&2
  else sleep 1; fi  # let the kit logger flush
  exit "$rc"
}
trap owner_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
WX cat "$TAR" | GI "$LOCK bash -c $(quote "$TRANSACTION")"
note "kept old tree: $OLD; remove only after the signed profile is installed and verified"
done_step
