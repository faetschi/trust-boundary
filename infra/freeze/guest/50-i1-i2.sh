#!/bin/bash
# 50 I1, I2 at $FREEZE (needs the session sudo drop-in; service and socket are stopped for the I1 window, the socket is started again at exit).
#   I1 [PRIV WRITE only if IMGDIGEST changed vs $PREV_FREEZE] load the signed $FREEZE image into the service podman store, in the SERVICE CONTEXT (SVC + PODMAN_ENV,
#      uid 995, no-new-privs, empty caps, no subuid range, NO --root). Graphroot and podman-home are wiped first, only in that case.
#      IMGDIGEST unchanged: nothing is loaded or wiped; the $PREV_FREEZE store is only CHECKED (same checks as after a load).
#      Checks: image exists, Id == CFG, Digest == IMGDIGEST, OCI manifest type, RepoDigests holds localhost/tbound/pi-cell@IMGDIGEST,
#      exactly one image line, and C4 (Digest read again in a fresh service-context process == IMGDIGEST).
#   I2 [READ-ONLY] the host-provisioned workflow state EXISTS: private-admin/sessions parents, workflow-recovery, workflow-owners, catalog.jsonl,
#      exactly one root.db. Nothing is created, truncated or initialised: `tbound workflow-root-init` is NEVER run here, root.db and the catalog
#      hold the earlier workflows (at $FREEZE the $PREV_FREEZE g1 workflow, which completed and was published). A missing piece is a stop (the F1 kit's I2 would have created it).
#      After option A (27-archive-state.sh archive, recorded in $HF/g1-prior/archive-done.txt) the state is the fresh one 27 recreated with the I2 commands: the same
#      owners and modes, exactly one root.db, but an EMPTY catalog.jsonl (the only check that differs). 27 ran workflow-root-init; this script still never does.
#   Live root [READ-ONLY]: canonical form (the fixture may be smoke or g1 or empty; seeding is a tbound-op verb in 30-reg-g1.sh). After 27 the directory exists
#      and is EMPTY (27 moved the $PREV_FREEZE g1 fixture src/, out/ into its archive and kept the root itself): that is the expected state here, and 30 re-seeds it.
# Needs: 10-upgrade.sh ($FREEZE tbound-profile and layout on the guest) and 20-r1.sh. Usage: bash 50-i1-i2.sh
set -euo pipefail
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 50-i1-i2
load_env
preflight_guest
svc_quiesce

CFGHEX=$(hexof "$CFG")
IMGREF=localhost/tbound/pi-cell
IMG_CHANGED=0; [ "$IMGDIGEST" = "$PREV_IMGDIGEST" ] || IMG_CHANGED=1
echo "IMG_CHANGED=$IMG_CHANGED (IMGDIGEST $FREEZE $IMGDIGEST, $PREV_FREEZE $PREV_IMGDIGEST; CFG $FREEZE $CFG, $PREV_FREEZE $PREV_CFG)"
i1_cleanup() {
  echo "cleanup: no uid-995 process, no /run/tbound-pi"
  G 'sudo -n pkill -u 995; sleep 1; sudo -n rm -rf /run/tbound-pi' || true
}

say "I1 preconditions"
eq "guest podman == pin" "$(gsha /usr/bin/podman)" "$PIN_PODMAN"
eq "containers.conf == P15 canonical" "$(gsha /etc/tbound/podman-config/containers.conf)" "$(hf_get CONTAINERS_CONF_DIGEST)"
eq "storage.conf == P15 canonical" "$(gsha /etc/tbound/podman-config/storage.conf)" "$(hf_get STORAGE_CONF_DIGEST)"
eq "/run/tbound-pi" "$(G 'ls -d /run/tbound-pi 2>&1 | grep -c "No such file" || true' | tr -d '\r')" 1
eq "subid ranges (P14 step 2)" "$(G '! grep -qE "^(tbound-pi|995):" /etc/subuid /etc/subgid && echo no-range' | tr -d '\r')" no-range
# Stale rootless podman pause process [$FREEZE, from the $PREV_FREEZE run]: a uid-995 process whose cmdline is exactly `/usr/bin/podman`, reparented to pid 1, while
# tbound-pi.service is inactive. An earlier `podman ps` in the service context (the L4 diag) leaves one behind (F4: pid 17589, started by the F2 L4 diag,
# session scope) and the "no uid-995 process" check below then blocks this step. It is detected and REPORTED first. It is terminated (SIGTERM, only such
# processes, re-verified in the same remote command) under the session sudo ONLY when RECLAIM_PODMAN_PAUSE=1 is set; otherwise this step stops here.
# The remote text has no single quote: it is run as `sudo -n sh -c '<text>'`.
REMOTE_PAUSE_LIST='for p in $(pgrep -u 995 -x -f /usr/bin/podman); do echo "$p $(sed -n "s/^PPid:[[:space:]]*//p" /proc/$p/status)"; done; true'
REMOTE_PAUSE_KILL='for p in $(pgrep -u 995 -x -f /usr/bin/podman); do pp=$(sed -n "s/^PPid:[[:space:]]*//p" /proc/$p/status); if [ "$pp" = 1 ]; then kill -TERM $p && echo "terminated $p"; fi; done; true'
say "I1 stale rootless podman pause process (uid 995, cmdline exactly /usr/bin/podman, parent pid 1, tbound-pi.service inactive)"
SVC_ST=$(G 'systemctl is-active tbound-pi.service' | tr -d '\r' || true)
echo "tbound-pi.service is-active: $SVC_ST"
PAUSE_LIST=$(G "sudo -n sh -c '$REMOTE_PAUSE_LIST'" | tr -d '\r')
PAUSE_PIDS=$(printf '%s\n' "$PAUSE_LIST" | awk 'NF == 2 && $2 == 1 && $1 ~ /^[0-9]+$/ {print $1}' | tr '\n' ' ')
if [ -n "$PAUSE_PIDS" ] && [ "$SVC_ST" != active ]; then
  note "stale podman pause process(es) of uid 995 with parent pid 1 (pid: $PAUSE_PIDS)"
  for p in $PAUSE_PIDS; do G "sudo -n ps -o pid,ppid,uid,lstart,args -p $p" | tr -d '\r'; done
  if [ "${RECLAIM_PODMAN_PAUSE:-0}" = 1 ]; then
    G "sudo -n sh -c '$REMOTE_PAUSE_KILL'" | tr -d '\r'
    sleep 2
    again=$(G "sudo -n sh -c '$REMOTE_PAUSE_LIST'" | tr -d '\r')
    [ -z "$again" ] || die "the stale podman pause process is still there after SIGTERM: $again (look at it by hand; this script never uses SIGKILL)"
    ok "stale podman pause process terminated (RECLAIM_PODMAN_PAUSE=1)"
  else
    die "stale podman pause process(es) (pid: $PAUSE_PIDS) block the 'no uid-995 process' check. Nothing was touched. To terminate them under the session sudo re-run with: RECLAIM_PODMAN_PAUSE=1 bash 50-i1-i2.sh"
  fi
else
  ok "no stale podman pause process"
fi
left=$(G 'sudo -n pgrep -u 995 -a; true'); [ -z "$left" ] || die "uid 995 processes running: $left"
cleanup_add i1_cleanup

if [ "$IMG_CHANGED" = 1 ]; then
  eq "owner pi-cell-load.tar == LOAD_TAR_DIGEST" "$(wd "$HF/pi-cell-load.tar")" "$LOAD_TAR_DIGEST"
  say "I1 wipe graphroot and podman-home (a store made under another mapping is unusable)"
  G 'sudo -n find /var/lib/tbound/podman-storage/graphroot /var/lib/tbound/podman-home -mindepth 1 -delete'
  eq "entries left" "$(G 'sudo -n find /var/lib/tbound/podman-storage/graphroot /var/lib/tbound/podman-home -mindepth 1 | wc -l')" 0
  eq "graphroot / podman-home owner" "$(G 'sudo -n stat -c %U:%G /var/lib/tbound/podman-storage/graphroot /var/lib/tbound/podman-home | sort -u')" tbound-pi:tbound-pi

  say "I1 stage the load tar (995:986 0600) and the run dirs"
  WX cat "$HF/pi-cell-load.tar" | GI "sudo -n install -o 995 -g 986 -m 0600 /dev/stdin $SCR/pi-cell-load.tar"
  eq "guest load tar == LOAD_TAR_DIGEST" "$(gsha "$SCR/pi-cell-load.tar")" "$LOAD_TAR_DIGEST"
  G 'sudo -n install -d -o 995 -g 986 -m 0700 /run/tbound-pi /run/tbound-pi/tmp /run/tbound-pi/cache'

  say "I1 load and tag in the service context"
  podsvc "load --input $SCR/pi-cell-load.tar"
  podsvc "tag $CFGHEX $IMGREF:latest"
else
  say "I1 skipped: IMGDIGEST unchanged vs $PREV_FREEZE (the $PREV_FREEZE store is checked, not rewritten)"
  G 'sudo -n install -d -o 995 -g 986 -m 0700 /run/tbound-pi /run/tbound-pi/tmp /run/tbound-pi/cache'
fi

say "I1 checks (read-only, all in the service context)"
ex=$(podsvc "image exists $IMGREF:latest; echo exists=\$?" | tr -d '\r')
eq "image exists" "$ex" exists=0
insp=$(podsvc "image inspect $IMGREF:latest --format '{{.Id}} {{.Digest}} {{.ManifestType}} {{range .RepoDigests}}{{.}} {{end}}'" | tr -d '\r')
echo "inspect: $insp"
read -r iid idig imt irepos <<<"$insp"
eq "Id == CFG" "${iid#sha256:}" "$CFGHEX"
eq "Digest == IMGDIGEST" "$idig" "$IMGDIGEST"
eq "ManifestType" "$imt" application/vnd.oci.image.manifest.v1+json
[[ " $irepos " == *" $IMGREF@$IMGDIGEST "* ]] || die "RepoDigests lacks $IMGREF@$IMGDIGEST: '$irepos' (podman rewrote the manifest? stop and report, do not re-sign)"
ok "RepoDigests contains $IMGREF@$IMGDIGEST"
imgs=$(podsvc "images --no-trunc --format '{{.Repository}}:{{.Tag}} {{.ID}}'" | tr -d '\r')
echo "images: $imgs"
eq "image lines" "$(printf '%s\n' "$imgs" | wc -l | tr -d ' ')" 1
read -r iref iid2 <<<"$imgs"
eq "image ref" "$iref" "$IMGREF:latest"
eq "image ID (images)" "${iid2#sha256:}" "$CFGHEX"
eq "C4: Digest in a fresh service-context process == IMGDIGEST" "$(podsvc "image inspect $IMGREF:latest --format '{{.Digest}}'" | tr -d '\r')" "$IMGDIGEST"

say "I1 end of window: no process of uid 995, remove /run/tbound-pi"
G 'sudo -n pkill -u 995; sleep 1; sudo -n rm -rf /run/tbound-pi'
left=$(G 'sudo -n pgrep -u 995 -a; true'); [ -z "$left" ] || die "uid 995 processes still running: $left"
eq "/run/tbound-pi removed" "$(G 'test -e /run/tbound-pi && echo present || echo gone' | tr -d '\r')" gone

# ---------------------------------------------------------------------------------------------------------------
say "I2 [READ-ONLY] the workflow state exists; nothing is created, truncated or initialised"
S1=/var/lib/tbound/private-admin; S2=/var/lib/tbound/sessions
CAT=$S1/workflow-recovery/catalog.jsonl
st=$(G "sudo -n stat -c '%U:%G %a %n' $S1 $S2 $S1/workflow-recovery $S2/workflow-owners $CAT" | tr -d '\r') || die "workflow state paths are missing: the F1 session created them; do NOT recreate by hand, find out what changed"
echo "$st"
while read -r own mode path; do
  case "$path" in
    "$CAT") eq "stat $path" "$own $mode" "tbound-pi:tbound-pi 600";;
    *) eq "stat $path" "$own $mode" "tbound-pi:tbound-pi 700";;
  esac
done <<<"$st"
rootdb=$(G 'sudo -n find /var/lib/tbound -xdev -name root.db 2>/dev/null' | tr -d '\r')
eq "root.db count" "$(printf '%s\n' "$rootdb" | grep -c . || true)" 1
echo "root.db: $rootdb"
eq "root.db is the configured one" "$rootdb" "$S1/workflow-recovery/root.db"
csize=$(G "sudo -n stat -c %s $CAT" | tr -d '\r')
echo "catalog.jsonl size $csize bytes, lines $(G "sudo -n sh -c 'wc -l < $CAT'" | tr -d '\r') (before option A it holds the earlier workflows and must be non-empty; after 27 it is empty)"
if archived_prev; then
  # Option A: 27-archive-state.sh recreated the state exactly as I2 does (empty catalog.jsonl, fresh root.db from workflow-root-init) after moving the $PREV_FREEZE state aside.
  echo "27-archive-state.sh archived the $PREV_FREEZE state ($(hf_get g1-prior/archive-done.txt)): an EMPTY catalog.jsonl and a fresh root.db are the expected state"
  [ "$csize" = 0 ] || note "catalog.jsonl is not empty after the archive ($csize bytes): the service was started since 27; read it before 30"
else
  [ "$csize" -gt 0 ] || die "catalog.jsonl is empty: the smoke workflow record is missing; stop, do not re-init (if the $PREV_FREEZE state was archived on purpose, run 27-archive-state.sh archive until it records $HF/g1-prior/archive-done.txt)"
fi
echo "terminal records now: $(G "sudo -n grep -c pi_workflow_terminal $CAT || true" | tr -d '\r') (with option A the $PREV_FREEZE g1 workflow is archived with its journal, so there is no earlier workflow to settle; without it the $FREEZE service may write its terminal: see 30-reg-g1.sh, ALLOW_NO_NEW_TERMINAL)"

say "live root [READ-ONLY] canonical form"
echo "entries (live root /var/lib/tbound/live/root): $(G 'sudo -n ls -A /var/lib/tbound/live/root 2>/dev/null' | tr -d '\r' | LC_ALL=C sort | tr '\n' '|')"
eq "lost+found" "$(G 'sudo -n stat -c "%U:%G %a" /var/lib/tbound/live/lost+found' | tr -d '\r')" "tbound-pi:tbound-pi 755"
eq "non-canonical entries (dirs != 0755, files != 0644/0755, not dir/file, not 995:986)" "$(G 'sudo -n find /var/lib/tbound/live/root -xdev -mindepth 1 \( \( -type d ! -perm 0755 \) -o \( -type f ! -perm 0644 ! -perm 0755 \) -o \( ! -type d ! -type f \) -o ! -user 995 -o ! -group 986 \) 2>/dev/null | wc -l')" 0
if G 'sudo -n test -d /var/lib/tbound/live/root'; then
  sumline=$(G 'sudo -n python3 -I /opt/tbound/helpers/xattr-census-r9.py /var/lib/tbound/live/root | tail -1' | tr -d '\r')
  echo "$sumline"
  for want in '"xattr_names_by_namespace": {}' '"errors": {"getxattr": 0, "listxattr": 0, "lstat": [], "readlink": 0, "scandir": []}' '"cross_device_pruned": []'; do
    [[ $sumline == *"$want"* ]] || die "census summary lacks: $want"
  done
  ok "live root: no xattr names, no errors"
else
  note "live root /var/lib/tbound/live/root does not exist: at $FREEZE 27-archive-state.sh keeps that directory (it archives only its content), so this is not the expected state; content checks skipped and lost+found at the image root is checked above. Owner must look."
fi
done_step
