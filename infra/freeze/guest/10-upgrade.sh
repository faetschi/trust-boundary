#!/bin/bash
# 10 [PRIVILEGED WRITE, needs the session sudo drop-in: bash 00-session-sudo.sh open] guest upgrade $PREV_FREEZE -> $FREEZE (P10 for $FREEZE).
#   1  owner inputs: $FREEZE build, signed layout, EVQ capture ($CF) agree with the $HF records
#   2  stop tbound-pi.service AND tbound-pi.socket (an exit trap starts the socket again, also after a failure)
#   3  rollback copies of the $PREV_FREEZE runner / tbound-profile / operator files in /opt/tbound/bin/$FREEZE_LC-backup (once)
#   4  runner: stop tbound-netsink, stop usr-local-bin-tbound.mount, install /opt/tbound/tbound/tbound, start mount and netsink
#   5  tbound-profile -> /usr/local/bin
#   6  cell-src: IMGDIGEST unchanged vs $PREV_FREEZE -> verify the guest layout and cosign.pub; changed -> install the $FREEZE signed layout
#   7  measures (service context, read-only): runtime bundle, xattr, then the $FREEZE landlock / xattr-visibility / cgroup-root digests,
#      the BASE template ($HF/d09-cell.base.json, all-zero registration placeholder) and BASE_TD
#   8  operator: /opt/tbound/operator/tbound-op from the $FREEZE source tree, the four cfg-templates, pins.env regenerated
#      (same keys as the current guest pins.env; TPL_* = sha256 of the $FREEZE templates; TEST_* from $CF/capture.json), sudoers rule compared
#   9  start the socket, `tbound-op status`: anchor ok, runner/tbound-profile are the $FREEZE pins
# podman, crun, cosign, seccomp.json, node, the mount units and the podman config are VERIFIED, not reinstalled.
# NOT done here: the installed chain (the $PREV_FREEZE g1 registration) binds the $PREV_FREEZE runner and is stale on purpose; the archive of the $PREV_FREEZE service state (27-archive-state.sh, option A, BEFORE this script; or, only if
# abandon is possible, a manual tbound-op abandon), the G1 recovery (29-g1-recover.sh = the owner step 2 of PROVISIONING-SIGNING.md G1-5) and the new RG cycle (30-reg-g1.sh) replace it. Step 1 only checks that the g1 template has a NEW admission id (G1-5 step 1).
# Idempotent: matching files are skipped, a differing unexpected state stops with the reason.
# Needs: freeze kit 10..40 + session-env.sh + 35-evq-capture.sh done; 27-archive-state.sh archive done (option A) or a manual tbound-op abandon done (step 1c checks which); session drop-in open. Usage: bash 10-upgrade.sh
set -euo pipefail
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 10-upgrade
load_env
check_key_meta
preflight_guest

say "1. owner-side inputs ($FREEZE build, signed layout, EVQ capture)"
eq "tbound == TBOUND_DIGEST" "$(wd "$W/bin/tbound")" "$TBOUND_DIGEST"
eq "tbound-profile == TBP_DIGEST" "$(wd "$W/bin/tbound-profile")" "$TBP_DIGEST"
eq "cosign tool == COSIGN_TOOL_DIGEST" "$(wd "$COSIGN")" "$COSIGN_TOOL_DIGEST"
eq "cosign tool == runbook pin 4629c757..." "$COSIGN_TOOL_DIGEST" "$PIN_COSIGN"
eq "cosign.pub == COSIGN_PUB_DIGEST" "$(wd "$COSIGN_PUB")" "$COSIGN_PUB_DIGEST"
# 05-runtime-swap.sh must provision a rebuilt bundle first; step 7 verifies the installed tree against the current pin.
note_prev BUNDLE_DIGEST "$BUNDLE_DIGEST" "$PREV_BUNDLE_DIGEST"
eq "BASH_DIGEST/NODE_DIGEST: node == pin" "$NODE_DIGEST" "$PIN_NODE"
WX test -d "$HF/pi-cell-oci" || die "no signed layout $HF/pi-cell-oci (run 40-sign.sh of the freeze kit)"
vout=$(WX python3 -I "$SRC/packaging/governed/sign_cell_image.py" verify --layout "$HF/pi-cell-oci" --cosign "$COSIGN" --pub "$COSIGN_PUB" --expect "$IMGDIGEST" 2>&1) || die "owner-side layout verify failed: $vout"
[[ $vout == *"$IMGDIGEST"* ]] || die "owner-side layout verify did not print IMGDIGEST: $vout"
ok "signed layout verifies under cosign.pub and prints IMGDIGEST $IMGDIGEST"
CAP=$CF/capture.json
WX test -f "$CAP" || die "no $CAP (run 35-evq-capture.sh)"
eq "capture commit" "$(jgetw "$CAP" commit)" "$F"
eq "capture archive_tree_digest == SRC_DIGEST" "$(jgetw "$CAP" archive_tree_digest)" "$SRC_DIGEST"
eq "capture exit_status" "$(jgetw "$CAP" exit_status)" 0
eq "capture test_binaries procoracle == TEST_PROCORACLE" "$(jgetw "$CAP" test_binaries.tbound/supervisor/internal/procoracle)" "$TEST_PROCORACLE"
eq "capture test_binaries cell-entrypoint == TEST_ENTRYPOINT" "$(jgetw "$CAP" test_binaries.tbound/supervisor/cmd/tbound-cell-entrypoint)" "$TEST_ENTRYPOINT"
IMG_CHANGED=0; [ "$IMGDIGEST" = "$PREV_IMGDIGEST" ] || IMG_CHANGED=1
eq "IMGDIGEST ($PREV_FREEZE record)" "$(prev_get IMGDIGEST)" "$PREV_IMGDIGEST"
echo "IMG_CHANGED=$IMG_CHANGED (IMGDIGEST $FREEZE $IMGDIGEST, $PREV_FREEZE $PREV_IMGDIGEST)"
eq "ADMIN_PUB_DIGEST == guest trust anchor" "$(gsha /etc/tbound/pi-host-profile.pub)" "$ADMIN_PUB_DIGEST"
# G1-5 step 1 (checked before anything is written): the g1 template of this commit must carry a NEW workflow_admission_id.
# The $PREV_FREEZE G1 id is consumed by its completed, published run; $FREEZE must use the new G1 admission.
# 27-archive-state.sh archives that state before $FREEZE; the catalog keeps the reservation until it is archived.
ADM_NEW=$(jgetw "$SRC/infra/guest/serve-pi-service.g1.json" workflow_admission_id)
ADM_PREV=$(jgetw "$PREV_HF/g1/serve-pi-service.json" workflow_admission_id)
echo "g1 admission id: $FREEZE template $ADM_NEW, $PREV_FREEZE service config $ADM_PREV"
note "$FREEZE g1 admission id is read from the frozen repo: $ADM_NEW (must differ from $PREV_FREEZE)"
[ -n "$ADM_NEW" ] && [ "$ADM_NEW" != "$ADM_PREV" ] || die "the $FREEZE g1 template carries the $PREV_FREEZE admission id '$ADM_PREV': G1-5 step 1 (a new admission id in infra/guest/serve-pi-service.g1.json) is not in this commit"
ok "g1 template has a new admission id (G1-5 step 1)"

# 1c. a request that CAN be abandoned must be reconciled BEFORE the $PREV_FREEZE chain is replaced: `tbound-op abandon` needs the installed $PREV_FREEZE g1 registration
# (a manual tbound-op abandon). The $PREV_FREEZE cycles are not claim-bearing (the dr probe control path is fixed for $FREEZE), so the $FREEZE route is 27-archive-state.sh (option A),
# which moves the whole state aside and leaves nothing to reconcile. $PREV_FREEZE's rp request is expected completed and its dr request UNKNOWN BY DESIGN.
# Read-only here (tbound-op collect requests); skipped when the guest already shows the $FREEZE pins (a re-run of this script after its step 8).
say "1c. the $PREV_FREEZE request $PREV_RP_RID must be reconciled or archived (terminal + abandoned, completed, or moved aside by 27) before the $PREV_FREEZE chain is replaced"
S10=$(op "status g1" | tr -d '\r')
if archived_prev; then
  # Option A: 27-archive-state.sh moved the $PREV_FREEZE request journal, catalog and workflow state aside and recreated them empty. There is no journal to judge.
  ok "the $PREV_FREEZE service state was archived by 27-archive-state.sh ($(hf_get g1-prior/archive-done.txt)): no reconciliation needed, the request journal and catalog are fresh"
  # 27 also moved the $PREV_FREEZE g1 fixture (src/, out/) out of the live root and kept the root directory itself: that empty kept root is what 20 and 50 expect
  # here, and 30 re-seeds the canonical g1 fixture with seed-live g1.
  eq "live root kept by 27 (owner/mode)" "$(G 'sudo -n stat -c "%U:%G %a" /var/lib/tbound/live/root' | tr -d '\r')" "tbound-pi:tbound-pi 700"
  eq "live root content archived by 27 (only lost+found may stay)" "$(G 'sudo -n find /var/lib/tbound/live/root -mindepth 1 -maxdepth 1 ! -name lost+found | wc -l' | tr -d '\r')" 0
elif [[ $S10 == *"pins F=$FREEZE_COMMIT"* ]]; then
  note "the guest already shows the $FREEZE pins: 10-upgrade.sh ran before; the reconciliation gate is 29-g1-recover.sh from here on"
elif [[ $S10 == *"pins F=$PREV_COMMIT"* ]]; then
  WX mkdir -p -m 0700 "$HF/g1-prior"
  op "collect requests"
  TS10=$(date -u +%Y%m%dT%H%M%SZ)
  fetch_export /var/tmp/tbound-op/_/serve-pi-requests.jsonl "g1-prior/requests-10-$TS10.jsonl"
  req_check "$HF/g1-prior/requests-10-$TS10.jsonl" "$PREV_RP_RID"
  req_reconciled || die "the $PREV_FREEZE request $PREV_RP_RID is not reconciled (journal verdict: $REQ_VERDICT). Run a manual tbound-op abandon NOW, while the $PREV_FREEZE rp registration is installed: once this script replaces the runner, tbound-op and pins it can no longer be abandoned the normal way"
  ok "the $PREV_FREEZE request $PREV_RP_RID is reconciled ($REQ_VERDICT)"
else
  die "tbound-op status g1 shows neither the $PREV_FREEZE ($PREV_COMMIT) nor the $FREEZE ($FREEZE_COMMIT) pins: stop and look"
fi

say "1b. guest units and canonical podman config (verified, never reinstalled here)"
for u in usr-local-bin-tbound usr-bin-podman usr-bin-crun opt-tbound-cell usr-local-bin-node; do
  eq "$u.mount" "$(G "systemctl is-active $u.mount" | tr -d '\r' || true)" active
done
CANON=$SRC/infra/guest/podman-config
gsums /etc/tbound/podman-config/containers.conf /etc/tbound/podman-config/storage.conf
for f in containers.conf storage.conf; do
  eq "guest $f == $FREEZE canonical" "${GH[/etc/tbound/podman-config/$f]}" "$(wd "$CANON/$f")"
done
eq "CONTAINERS_CONF_DIGEST == $PREV_FREEZE record" "${GH[/etc/tbound/podman-config/containers.conf]}" "$(prev_get CONTAINERS_CONF_DIGEST)"
eq "STORAGE_CONF_DIGEST == $PREV_FREEZE record" "${GH[/etc/tbound/podman-config/storage.conf]}" "$(prev_get STORAGE_CONF_DIGEST)"
hf_put CONTAINERS_CONF_DIGEST "${GH[/etc/tbound/podman-config/containers.conf]}"
hf_put STORAGE_CONF_DIGEST "${GH[/etc/tbound/podman-config/storage.conf]}"

say "2. stop service and socket"
svc_quiesce

say "3. rollback copies of the $PREV_FREEZE files (written once)"
BK3=/opt/tbound/bin/$FREEZE_LC-backup
G "sudo -n install -d -o root -g root -m 0700 $BK3"
gsums /usr/local/bin/tbound /opt/tbound/tbound/tbound /usr/local/bin/tbound-profile
for p in /usr/local/bin/tbound /opt/tbound/tbound/tbound; do
  case "${GH[$p]}" in "$PREV_TBOUND_DIGEST"|"$TBOUND_DIGEST") ;; *) die "$p is neither the $PREV_FREEZE nor the $FREEZE runner (${GH[$p]}): stop and look";; esac
done
case "${GH[/usr/local/bin/tbound-profile]}" in "$PREV_TBP_DIGEST"|"$TBP_DIGEST") ;; *) die "/usr/local/bin/tbound-profile is neither the $PREV_FREEZE nor the $FREEZE build (${GH[/usr/local/bin/tbound-profile]})";; esac
[ "${GH[/opt/tbound/tbound/tbound]}" != "$PREV_TBOUND_DIGEST" ] || G "sudo -n sh -c 'test -e $BK3/tbound || cp -p /opt/tbound/tbound/tbound $BK3/tbound'"
[ "${GH[/usr/local/bin/tbound-profile]}" != "$PREV_TBP_DIGEST" ] || G "sudo -n sh -c 'test -e $BK3/tbound-profile || cp -p /usr/local/bin/tbound-profile $BK3/tbound-profile'"
G "sudo -n sh -c 'for f in tbound-op pins.env; do test -e $BK3/\$f || cp -p $OPDIR/\$f $BK3/\$f; done; test -e $BK3/cfg-templates || cp -rp $OPDIR/cfg-templates $BK3/cfg-templates'"
echo "backups: $(G "sudo -n ls -l $BK3" | tr -d '\r' | tr '\n' '|')"

say "4. runner: /opt/tbound/tbound/tbound and its read-only file bind"
NS_WAS_ACTIVE=0
restore_netsink() {
  if [ "$NS_WAS_ACTIVE" = 1 ]; then
    echo "cleanup: runner mount and tbound-netsink must be up again"
    G 'sudo -n systemctl start usr-local-bin-tbound.mount; sudo -n systemctl start tbound-netsink.service; systemctl is-active tbound-netsink.service'
  fi
}
if [ "${GH[/usr/local/bin/tbound]}" = "$TBOUND_DIGEST" ] && [ "${GH[/opt/tbound/tbound/tbound]}" = "$TBOUND_DIGEST" ]; then
  ok "runner already is the $FREEZE build; no swap"
else
  [ "$(G 'systemctl is-active tbound-netsink.service' | tr -d '\r' || true)" = active ] && NS_WAS_ACTIVE=1
  cleanup_add restore_netsink
  echo "swap: tbound-netsink was active=$NS_WAS_ACTIVE"
  G 'sudo -n systemctl stop tbound-netsink.service'
  G 'sudo -n systemctl stop usr-local-bin-tbound.mount'
  WX cat "$W/bin/tbound" | GI 'sudo -n install -o root -g root -m 0555 /dev/stdin /opt/tbound/tbound/tbound'
  G 'sudo -n systemctl start usr-local-bin-tbound.mount'
  [ "$NS_WAS_ACTIVE" = 0 ] || G 'sudo -n systemctl start tbound-netsink.service'
  gsums /usr/local/bin/tbound /opt/tbound/tbound/tbound
fi
eq "guest /usr/local/bin/tbound (ro bind)" "${GH[/usr/local/bin/tbound]}" "$TBOUND_DIGEST"
eq "guest /opt/tbound/tbound/tbound" "${GH[/opt/tbound/tbound/tbound]}" "$TBOUND_DIGEST"

say "5. tbound-profile"
if [ "$(gsha "$TBPG")" = "$TBP_DIGEST" ]; then ok "tbound-profile already is the $FREEZE build"; else
  WX cat "$W/bin/tbound-profile" | GI 'sudo -n install -o root -g root -m 0755 /dev/stdin /usr/local/bin/.tbound-profile.new && sudo -n mv -f /usr/local/bin/.tbound-profile.new /usr/local/bin/tbound-profile'
fi
eq "guest /usr/local/bin/tbound-profile" "$(gsha "$TBPG")" "$TBP_DIGEST"
note_prev TBP_DIGEST "$TBP_DIGEST" "$PREV_TBP_DIGEST"

say "6. cell-src: cosign, seccomp.json, cosign.pub, signed layout"
gsums /opt/tbound/cell-src/cosign /opt/tbound/cell-src/seccomp.json
eq "cell-src/cosign == pin" "${GH[/opt/tbound/cell-src/cosign]}" "$PIN_COSIGN"
eq "cell-src/cosign == owner cosign tool" "${GH[/opt/tbound/cell-src/cosign]}" "$COSIGN_TOOL_DIGEST"
eq "cell-src/seccomp.json == pin" "${GH[/opt/tbound/cell-src/seccomp.json]}" "$PIN_SECCOMP"
eq "cell-src/cosign.pub (unchanged key)" "$(gsha /opt/tbound/cell-src/cosign.pub)" "$COSIGN_PUB_DIGEST"
layout_cmd() { printf 'cd %s && (find . -type d | LC_ALL=C sort; find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) | sha256sum' "$1"; }
L2=$(WB "$(layout_cmd "$HF/pi-cell-oci")" | cut -d' ' -f1)
L1=$(WB "$(layout_cmd "$PREV_HF/pi-cell-oci")" | cut -d' ' -f1)
echo "owner layout tree digests: $FREEZE $L2   $PREV_FREEZE $L1   (equal bytes: $([ "$L1" = "$L2" ] && echo yes || echo no))"
HAVE=none
if G 'sudo -n test -e /opt/tbound/cell-src/pi-cell-oci'; then HAVE=$(G "sudo -n sh -c '$(layout_cmd /opt/tbound/cell-src/pi-cell-oci)'" | cut -d' ' -f1); fi
echo "guest cell-src layout tree digest: $HAVE"
if [ "$IMG_CHANGED" = 0 ]; then
  case "$HAVE" in
    "$L1") ok "IMGDIGEST unchanged: the guest keeps the $PREV_FREEZE-signed layout (same image; the $FREEZE signature file differs only if cosign signs non-deterministically: $([ "$L1" = "$L2" ] && echo 'layouts are byte-identical' || echo "$FREEZE layout bytes differ, not installed"))";;
    "$L2") ok "IMGDIGEST unchanged: the guest already has the $FREEZE-signed layout";;
    *) die "IMGDIGEST unchanged but the guest layout ($HAVE) is neither the $PREV_FREEZE ($L1) nor the $FREEZE ($L2) layout: stop and look";;
  esac
  WANT_LAYOUT=$HAVE
else
  if [ "$HAVE" = "$L2" ]; then ok "guest already has the $FREEZE signed layout"; else
    [ "$HAVE" = none ] || [ "$HAVE" = "$L1" ] || die "IMGDIGEST changed but the guest layout ($HAVE) is neither absent nor the $PREV_FREEZE layout ($L1): stop and look"
    if [ "$HAVE" = "$L1" ]; then
      G "sudo -n sh -c 'test -e $BK3/pi-cell-oci || mv /opt/tbound/cell-src/pi-cell-oci $BK3/pi-cell-oci'"
      G 'sudo -n test ! -e /opt/tbound/cell-src/pi-cell-oci' || die "old layout still in cell-src (an earlier backup exists in $BK3: inspect)"
      ok "$PREV_FREEZE layout moved to $BK3/pi-cell-oci"
    fi
    WX tar -C "$HF" -c pi-cell-oci | GI 'sudo -n tar -x --no-same-owner -C /opt/tbound/cell-src'
    G 'sudo -n chown -R root:root /opt/tbound/cell-src/pi-cell-oci && sudo -n find /opt/tbound/cell-src/pi-cell-oci -type d -exec chmod 0755 {} + && sudo -n find /opt/tbound/cell-src/pi-cell-oci -type f -exec chmod 0444 {} +'
  fi
  WANT_LAYOUT=$L2
fi
eq "layout tree digest cell-src" "$(G "sudo -n sh -c '$(layout_cmd /opt/tbound/cell-src/pi-cell-oci)'" | cut -d' ' -f1)" "$WANT_LAYOUT"
eq "layout files not root:root / dirs not 0755 / files not 0444" "$(G "sudo -n find /opt/tbound/cell-src/pi-cell-oci \( ! -user root -o ! -group root -o \( -type d ! -perm 0755 \) -o \( -type f ! -perm 0444 \) \) | wc -l")" 0
ro_mounts /usr/local/bin/tbound /usr/local/bin/node /usr/bin/podman /usr/bin/crun /opt/tbound/cell
V="/usr/local/bin/tbound /usr/local/bin/node /usr/local/bin/tbound-profile /usr/bin/podman /usr/bin/crun /opt/tbound/cell/cosign /opt/tbound/cell/cosign.pub /opt/tbound/cell/seccomp.json"
if [ "$(G "sudo -n sh -c '$(layout_cmd /opt/tbound/cell/pi-cell-oci)'" 2>/dev/null | cut -d' ' -f1)" != "$WANT_LAYOUT" ]; then
  note "ro view of cell-src is stale: restarting opt-tbound-cell.mount"
  G 'sudo -n systemctl restart opt-tbound-cell.mount'
fi
gsums $V
eq "ro /usr/local/bin/tbound == \$W/bin/tbound" "${GH[/usr/local/bin/tbound]}" "$(wd "$W/bin/tbound")"
eq "ro /usr/local/bin/tbound == TBOUND_DIGEST" "${GH[/usr/local/bin/tbound]}" "$TBOUND_DIGEST"
eq "tbound-profile == \$W/bin/tbound-profile" "${GH[/usr/local/bin/tbound-profile]}" "$(wd "$W/bin/tbound-profile")"
eq "node" "${GH[/usr/local/bin/node]}" "$PIN_NODE"
eq "podman" "${GH[/usr/bin/podman]}" "$PIN_PODMAN"
eq "crun" "${GH[/usr/bin/crun]}" "$PIN_CRUN"
eq "ro cosign" "${GH[/opt/tbound/cell/cosign]}" "$PIN_COSIGN"
eq "ro cosign.pub == COSIGN_PUB_DIGEST" "${GH[/opt/tbound/cell/cosign.pub]}" "$COSIGN_PUB_DIGEST"
eq "ro seccomp.json" "${GH[/opt/tbound/cell/seccomp.json]}" "$PIN_SECCOMP"
eq "ro layout tree digest == cell-src" "$(G "sudo -n sh -c '$(layout_cmd /opt/tbound/cell/pi-cell-oci)'" | cut -d' ' -f1)" "$WANT_LAYOUT"
vout=$(G 'sudo -n /opt/tbound/cell/cosign verify --offline --insecure-ignore-tlog=true --key /opt/tbound/cell/cosign.pub --local-image /opt/tbound/cell/pi-cell-oci 2>&1') || die "guest cosign verify failed: $vout"
printf '%s\n' "$vout" | head -c 800; echo
dm=$(printf '%s\n' "$vout" | sed -n 's/.*docker-manifest-digest"[[:space:]]*:[[:space:]]*"\(sha256:[0-9a-f]\{64\}\)".*/\1/p' | sort -u)
eq "guest cosign verify docker-manifest-digest == IMGDIGEST ($FREEZE)" "$dm" "$IMGDIGEST"
eq "tbound-netsink" "$(G 'systemctl is-active tbound-netsink.service' | tr -d '\r' || true)" active
pid=$(G 'systemctl show -p MainPID --value tbound-netsink.service' | tr -d '\r')
[ "${pid:-0}" -gt 0 ] || die "tbound-netsink has no main pid"
eq "netsink process image (/proc/$pid/exe) == $FREEZE runner" "$(gsha /proc/$pid/exe)" "$TBOUND_DIGEST"

say "7. measures (service context, read-only) and the $FREEZE base template"
r1_measure_runtime
MOPT=$(hf_get XATTR_MOPT); MAGIC=$(hf_get XATTR_MAGIC)
LLD=$(WX "$TBP" landlock-digest --read-only /usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev --read-write /work,/tmp | tr -d '\r\n')
need_dig LANDLOCK "$LLD"; note_prev LANDLOCK_DIGEST "$LLD" "$(prev_get LANDLOCK_DIGEST)"
hf_put LANDLOCK_DIGEST "$LLD"
VIS=$(WX "$TBP" xattr-digest --filesystem-magic "$MAGIC" --mount-options-digest "$MOPT" --uid 995 --gid 986 | tr -d '\r\n')
need_dig XATTR_VIS "$VIS"; note_prev XATTR_VIS_DIGEST "$VIS" "$(prev_get XATTR_VIS_DIGEST)"
hf_put XATTR_VIS_DIGEST "$VIS"
CGR=$(WX "$TBP" cgroup-root-digest --uid 995 --cgroups-path /system.slice/tbound-pi.service/cells | tr -d '\r\n')
need_dig CGROUP_ROOT_DIGEST "$CGR"; note_prev CGROUP_ROOT_DIGEST "$CGR" "$(prev_get CGROUP_ROOT_DIGEST)"
hf_put CGROUP_ROOT_DIGEST "$CGR"
ZERO=sha256:0000000000000000000000000000000000000000000000000000000000000000
BASEF=$HF/d09-cell.base.json
if hf_has d09-cell.base.json; then ok "d09-cell.base.json already built in HF"; else
  WX rm -f "$BASEF.partial"
  WX "$TBP" template-init --out "$BASEF.partial"
  WX python3 -I "$WGA/fill-template.py" "$BASEF.partial" "$HF" "$PIN_COSIGN" "$PIN_SECCOMP" "$PIN_PODMAN" "$PIN_CRUN" "$ZERO"
  hf_commit d09-cell.base.json
fi
eq "base template: expected_image_digest == IMGDIGEST" "$(jgetw "$BASEF" expected_image_digest)" "$IMGDIGEST"
eq "base template: user_namespace_mapping_digest" "$(jgetw "$BASEF" user_namespace_mapping_digest)" "$PIN_MAPPING"
eq "base template: registration digest is the placeholder" "$(jgetw "$BASEF" managed_live_root_registration_digest)" "$ZERO"
td_out=$(WX "$TBP" template-digest --template "$BASEF" 2>&1) || die "template-digest failed: $td_out"
echo "$td_out"
BASE_TD=$(printf '%s\n' "$td_out" | sed -n 's/^D09 base template digest .*: \(sha256:[0-9a-f]\{64\}\)$/\1/p')
need_dig BASE_TD "$BASE_TD"
note_prev BASE_TD "$BASE_TD" "$(prev_get BASE_TD)"
hf_put BASE_TD "$BASE_TD"

say "8. operator files (owner tree: $SRC/infra/guest/operator, templates $SRC/infra/guest/serve-pi-service.*.json)"
OPSRC=$SRC/infra/guest/operator
for f in tbound-op 50-tbound-op; do
  [ "$(WX cat "$OPSRC/$f" | tr -dc '\r' | wc -c | tr -d ' ')" = 0 ] || die "$OPSRC/$f contains CR (must be LF)"
done
for t in smoke g1 route-probe corpus-pilot; do
  [ "$(WX cat "$SRC/infra/guest/serve-pi-service.$t.json" | tr -dc '\r' | wc -c | tr -d ' ')" = 0 ] || die "template $t contains CR"
done
# 8a. dispatcher
want=$(wd "$OPSRC/tbound-op")
if [ "$(gsha $OPDIR/tbound-op)" = "$want" ]; then ok "$OPDIR/tbound-op already equals the $FREEZE source"; else
  WX cat "$OPSRC/tbound-op" | GI "sudo -n install -o root -g root -m 0755 /dev/stdin $OPDIR/.tbound-op.new && sudo -n mv -f $OPDIR/.tbound-op.new $OPDIR/tbound-op"
fi
eq "guest tbound-op == owner $FREEZE source" "$(gsha $OPDIR/tbound-op)" "$want"
eq "tbound-op owner/mode" "$(G "sudo -n stat -c '%U:%G %a' $OPDIR/tbound-op $OPDIR" | tr -d '\r' | tr '\n' '|')" "root:root 755|root:root 755|"
# 8b. sudoers rule: compared, not rewritten
eq "/etc/sudoers.d/50-tbound-op == $FREEZE source" "$(gsha /etc/sudoers.d/50-tbound-op)" "$(wd "$OPSRC/50-tbound-op")"
# 8c. templates
G "sudo -n install -d -o root -g root -m 0755 $OPDIR/cfg-templates"
declare -A TPLPIN
for t in smoke g1 route-probe corpus-pilot; do
  src=$SRC/infra/guest/serve-pi-service.$t.json; dst=$OPDIR/cfg-templates/serve-pi-service.$t.json
  want=$(wd "$src"); TPLPIN[$t]=$want
  if [ "$(gsha $dst 2>/dev/null || echo none)" = "$want" ]; then ok "$t template already equals the $FREEZE source"; else
    WX cat "$src" | GI "sudo -n install -o root -g root -m 0444 /dev/stdin $dst.new && sudo -n mv -f $dst.new $dst"
  fi
  eq "guest cfg-template $t == $FREEZE source" "$(gsha $dst)" "$want"
done
G "sudo -n sh -c 'ls $OPDIR/cfg-templates | wc -l; stat -c %U:%G:%a $OPDIR/cfg-templates/*' | sort | uniq -c" | tr -d '\r'
# 8d. pins.env: same keys as the current file, values from $HF (+ capture)
curpins=$(G "cat $OPDIR/pins.env" | tr -d '\r')
curkeys=$(printf '%s\n' "$curpins" | sed -n 's/^\([A-Z][A-Z0-9_]*\)=.*/\1/p' | sort | tr '\n' ' ')
declare -A PV=(
  [ADMIN_PUB_DIGEST]=$ADMIN_PUB_DIGEST [BASE_TD]=$BASE_TD [BUNDLE_DIGEST]=$BUNDLE_DIGEST [CFG]=$CFG [F]=$F [IMGDIGEST]=$IMGDIGEST
  [SRC_DIGEST]=$SRC_DIGEST [TBOUND_DIGEST]=$TBOUND_DIGEST [TBP_DIGEST]=$TBP_DIGEST
  [TPL_SMOKE]=${TPLPIN[smoke]} [TPL_G1]=${TPLPIN[g1]} [TPL_RP]=${TPLPIN[route-probe]} [TPL_PILOT]=${TPLPIN[corpus-pilot]}
  [TEST_ENTRYPOINT]=$TEST_ENTRYPOINT [TEST_PROCORACLE]=$TEST_PROCORACLE )
newkeys=$(printf '%s\n' "${!PV[@]}" | sort | tr '\n' ' ')
eq "pins.env key set (guest now == $FREEZE generated)" "$newkeys" "$curkeys"
PINTXT="# tbound-op pins (generated from \$HF digest files and \$CF/capture.json; $FREEZE frozen commit $F). root:root 0444.
# Parsed (never sourced) by tbound-op: KEY=value, value = sha256:<64 hex> or 40 hex."
for k in $(printf '%s\n' "${!PV[@]}" | sort); do
  v=${PV[$k]}; [[ $v =~ ^(sha256:[0-9a-f]{64}|[0-9a-f]{40})$ ]] || die "pin $k has a bad value: '$v'"
  PINTXT+=$'\n'"$k=$v"
done
PINSHA="sha256:$(printf '%s\n' "$PINTXT" | sha256sum | cut -d' ' -f1)"
echo "generated pins.env sha256 $PINSHA:"; printf '%s\n' "$PINTXT"
echo "-- changes vs the current guest pins.env:"
diff <(printf '%s\n' "$curpins" | grep -v '^#' | sort) <(printf '%s\n' "$PINTXT" | grep -v '^#' | sort) || true
if [ "$(gsha $OPDIR/pins.env)" = "$PINSHA" ]; then ok "pins.env already is the $FREEZE pin set"; else
  printf '%s\n' "$PINTXT" | GI "sudo -n install -o root -g root -m 0444 /dev/stdin $OPDIR/.pins.env.new && sudo -n mv -f $OPDIR/.pins.env.new $OPDIR/pins.env"
fi
eq "guest pins.env == generated" "$(gsha $OPDIR/pins.env)" "$PINSHA"
eq "pins.env owner/mode" "$(G "sudo -n stat -c '%U:%G %a' $OPDIR/pins.env" | tr -d '\r')" "root:root 444"

say "9. socket up, dispatcher status"
G 'sudo -n systemctl start tbound-pi.socket'
eq "tbound-pi.socket" "$(G 'systemctl is-active tbound-pi.socket' | tr -d '\r' || true)" active
st=$(G "$OP status" 2>&1 | tr -d '\r') || die "tbound-op status failed: $st"
printf '%s\n' "$st"
[[ $st == *"pins F=$F"* ]] || die "tbound-op status does not show pins F=$F"
[[ $st == *"anchor: ok"* ]] || die "tbound-op status: anchor not ok"
[[ $st == *"runner: ${TBOUND_DIGEST:0:23} tbound-profile: ${TBP_DIGEST:0:23}"* ]] || die "tbound-op status: runner/tbound-profile are not the $FREEZE pins"
ok "tbound-op status: pins F=$F, anchor ok, runner and tbound-profile are the $FREEZE builds"
note "the installed g1 chain (registration binds the $PREV_FREEZE runner) is stale by design; next: 20-r1.sh (verify), 50-i1-i2.sh, CLOSE the session sudo, then 29-g1-recover.sh (G1-5 step 2, password sudo) and 30-reg-g1.sh"
done_step
