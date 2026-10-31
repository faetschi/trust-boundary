#!/bin/bash
# 27 [PRIVILEGED WRITE, PASSWORD SUDO; owner chose option A] archive the $PREV_FREEZE TBound service state and start fresh at $FREEZE. PREPARED, NOT RUN.
# $PREV_FREEZE G1 completed and published ($PREV_G1_RID, $PREV_G1_RUNID); the rp route probe completed
# ($PREV_RP_RID, $PREV_RP_RUNID); the dr request was accepted, ran as designed and ended UNKNOWN
# ($PREV_DR_RID, $PREV_DR_RUNID; CHECKS_OK, launch proof journaled, Pi not started). None of the $PREV_FREEZE
# guest results is claim-bearing (the dr control path bug fixed for $FREEZE), so $FREEZE reruns G1, rp and dr on a fresh state. Archive all three cycles.
# a manual tbound-op abandon stays only for a request that can be abandoned; it is not part of the $FREEZE order.
# Nothing is deleted: everything is MOVED into /root/ga/archive-$PREV_LC-<UTC>/rootfs/ (same path layout below it), after a sha256 manifest of every file
# (plus owner, group and mode of every entry) was recorded and fetched.
#
# Moves (items under /; each one only if present):
#   /var/lib/tbound/audit  /var/lib/tbound/private-admin  /var/lib/tbound/sessions  /var/lib/tbound/publication-state
#       (the four StateDirectory= paths of tbound-pi.service that hold workflow state: request journal serve-pi-requests.jsonl, provider/session/command
#        audit files, catalog.jsonl, root.db, workflow owner store, publication state)
#   /root/ga/state/g1  /root/ga/state/g1.once  /root/ga/reg-g1  and the leftovers 29-g1-recover.sh handles:
#   /root/ga/state/g1.client-status  /root/ga/g1  /var/tmp/tbound-op/g1
#   the same rp items (state/rp, state/rp.once, reg-rp, state/rp.client-status, /root/ga/rp, /var/tmp/tbound-op/rp)
#   and the dr items (state/dr, state/dr.once, reg-dr, state/dr.client-status, /root/ga/dr, /var/tmp/tbound-op/dr)
#   /root/ga/out (the entire directory: responses and all guest EVQ evidence)
#   /var/tmp/tbound-op/_/evidence-*  /var/tmp/tbound-op/_/out-* (other exports stay in place)
#   the CONTENT of the live root /var/lib/tbound/live/root (every entry inside it except lost+found)
#       At $PREV_FREEZE `tbound-op seed-live g1` (RG-7 in 30-reg-g1.sh) created the $PREV_FREEZE fixture src/release.txt and out/ in the root that 27 had left empty.
#       Those entries are the $PREV_FREEZE fixture and move into the archive at their same paths. The live-root DIRECTORY ITSELF IS KEPT (same inode, same
#       owner/mode, same mount): it is the registered managed root that the RW calibration and the registration bind, so 27 never removes or recreates it.
#       If removing the root were required (it is a symlink, not a directory, or not on the live mount), 27 stops with "owner must look" and says so;
#       it does not guess and never removes it.
# Kept in place: the live root directory /var/lib/tbound/live/root (empty afterwards) and its lost+found, the live mount root /var/lib/tbound/live
#   (a separate ext4 image; tbound-pi:tbound-pi 0700, keeps lost+found), /root/ga/state/rids (request ids are never reused), /root/ga/prev/g1,
#   /root/ga/state/smoke*, /var/tmp/tbound-op/_ (other exports), /var/lib/tbound/{runtime,podman-*,pi-worker}, /etc/tbound/** (not read at all).
#   Also left alone: /root/ga/archive-f10-<UTC>/, /root/ga/archive-f9-<UTC>/, /root/ga/archive-f8-<UTC>/, /root/ga/archive-f7-<UTC>/,
#   /root/ga/archive-f6-<UTC>/, /root/ga/archive-f5-<UTC>/ and /root/ga/archive-f4-<UTC>/ (the F10, F9, F8, F7, F6, F5 and F4 states archived by
#   the $PREV_FREEZE, F10, F9, F8, F7, F6 and F5 kits).
#   This script counts ONLY archive-$PREV_LC-* directories; an older archive-f10-*, archive-f9-*, archive-f8-*, archive-f7-*, archive-f6-*, archive-f5-*
#   or archive-f4-* directory is not a state of this run and is never continued, verified or touched (the probe prints how many it sees).
#   `tbound-op seed-live g1` in 30-reg-g1.sh then re-creates the canonical g1 fixture in the empty root (it stops with "unexpected extra entries:
#   owner must look" if root/ holds anything unexpected; status shows the listing).
#
# Recreates the empty state EXACTLY as PROVISIONING-SIGNING.md "I2" prescribes (as root, which is what `sudo -n` gives there), and
# `tbound workflow-root-init` is the runner subcommand it names (no arguments; run as the service identity, never as root):
#   install -d -o 995 -g 986 -m 0700 /var/lib/tbound/private-admin /var/lib/tbound/sessions
#   install -d -o 995 -g 986 -m 0700 .../private-admin/workflow-recovery .../sessions/workflow-owners
#   install -o 995 -g 986 -m 0600 /dev/null .../private-admin/workflow-recovery/catalog.jsonl
#   setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- /usr/local/bin/tbound workflow-root-init
#   (the runbook SVC line, "after Z1: the same line without sudo -n")
# audit and publication-state are NOT created by a runbook command: tbound-pi.service has StateDirectory=...audit...publication-state with User=tbound-pi
# Group=tbound-pi StateDirectoryMode=0700, so systemd creates them 995:986 0700 at the first service start (the runbook says so for private-admin and
# sessions in I2). Here they are created with the same owner and mode (install -d -o 995 -g 986 -m 0700) so that tbound-op verbs that look at the audit
# directory before the first start do not fail. Before anything moves, the four directories are required to be 995:986 0700 now, so the recreation equals
# the original owner and mode; any other value is a stop.
#
# Order of work: (1) probe (read-only) (2) owner gates: $PREV_FREEZE identity of the items, request journal readable, no request without a terminal (3) tbound-op svc
# stop (4) PHASE manifest: under /run/tbound-op.lock, service and socket stopped (systemctl stop as root if tbound-op left one up), print the manifest
# (static items + the live-root content); the owner copy goes to $HF/g1-prior/archive-manifest.txt and its sha256 is compared (5) PHASE archive, ONE remote
# session under the lock (flock -w 5): recompute the manifest and require the same sha256, write MANIFEST.txt into the archive dir, move (static items and
# the live-root content), verify the archive against the manifest (hashes, owners, modes), recreate the empty state (I2), verify owner/mode of the fresh
# state and that the live root is the empty kept directory, write ARCHIVE-DONE, start the socket.
# The socket is active at the end (an exit trap starts it also after a failure). Re-running is safe: ARCHIVE-DONE present = "nothing left to move"; a
# half-done archive dir is continued (`.moved` marks the end of the move; an item present both at its place and in the archive is a stop).
# The password comes from TBA_PW in the environment (stdin of the ssh session into `sudo -S -p ''`, never in argv, never written, never printed).
# Session sudo (00-session-sudo.sh) must be CLOSED. Run it BEFORE 00 open and BEFORE 10 (it also works later; the pins are only printed).
#
# Usage:  read -rs TBA_PW; export TBA_PW      (same Git Bash window; the owner-authorized password)
#         bash 27-archive-state.sh status     read-only: what would be moved, current terminal state of every dispatcher request, live-root listing
#         bash 27-archive-state.sh archive    the archive (owner approval of option A)
set -euo pipefail
case "$-" in *x*) echo "FATAL: refusing to run under xtrace (it would print the password)" >&2; exit 1;; esac
case ":${SHELLOPTS:-}:" in *:xtrace:*) echo "FATAL: SHELLOPTS has xtrace" >&2; exit 1;; esac
unset BASH_XTRACEFD || true
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 27-archive-state

MODE=${1:-}
case "$MODE" in status|archive) [ $# -eq 1 ] || die "usage: bash 27-archive-state.sh status|archive";; *) die "usage: bash 27-archive-state.sh status|archive";; esac

need_pw() { [ -n "${TBA_PW:-}" ] || die "TBA_PW is not set (read -rs TBA_PW; export TBA_PW)"; }
GPW() { printf '%s\n' "$TBA_PW" | sh "$GSSH_SH" "$@"; }
pw() { GPW "sudo -S -p '' $*"; }

load_env
preflight_op
need_pw
[ "$(pw 'id -u' | tr -d '\r')" = 0 ] || die "password sudo did not give uid 0"
ok "password sudo works"

OUT=/var/tmp/tbound-op
TS=$(date -u +%Y%m%dT%H%M%SZ)
WX mkdir -p -m 0700 "$HF/g1-prior"
NL=$'\n'

# ---- remote scripts (single-quoted here: no local expansion; they contain NO single quote; POSIX sh = dash on the guest) --------------------------
# Common part: item list, live-root content handling, archive-dir detection (AST = NONE | PARTIAL | MOVED | DONE), helper functions. Variables passed in:
# STAMP, PHASE-specific ones. LIVE is relative to / (cwd) so that the manifest prints the same relative paths in place and inside the archive.
REMOTE_COMMON='set -eu; umask 077
cd /
ITEMS="var/lib/tbound/audit var/lib/tbound/private-admin var/lib/tbound/sessions var/lib/tbound/publication-state root/ga/state/g1 root/ga/state/g1.once root/ga/reg-g1 root/ga/state/g1.client-status root/ga/g1 var/tmp/tbound-op/g1 root/ga/state/rp root/ga/state/rp.once root/ga/reg-rp root/ga/state/rp.client-status root/ga/rp var/tmp/tbound-op/rp root/ga/state/dr root/ga/state/dr.once root/ga/reg-dr root/ga/state/dr.client-status root/ga/dr var/tmp/tbound-op/dr root/ga/out var/tmp/tbound-op/_/evidence-* var/tmp/tbound-op/_/out-*"
STATEDIRS="var/lib/tbound/audit var/lib/tbound/private-admin var/lib/tbound/sessions var/lib/tbound/publication-state"
LIVE=var/lib/tbound/live/root
LIVEMNT=/var/lib/tbound/live
PA=/var/lib/tbound/private-admin; SE=/var/lib/tbound/sessions
CAT=$PA/workflow-recovery/catalog.jsonl; RDB=$PA/workflow-recovery/root.db
here() { [ -e "$1" ] || [ -L "$1" ]; }
live_entries() { if [ -d "$LIVE" ]; then find "$LIVE" -mindepth 1 -maxdepth 1 ! -name lost+found -printf "%P\n" | LC_ALL=C sort; fi; }
N=0; NF10=0; NF9=0; NF8=0; NF7=0; NF6=0; NF5=0; NF4=0; ARCH=
for d in /root/ga/archive-$PREV-*; do if [ -d "$d" ]; then N=$((N+1)); ARCH=$d; fi; done
for d in /root/ga/archive-f10-*; do if [ -d "$d" ]; then NF10=$((NF10+1)); fi; done
for d in /root/ga/archive-f9-*; do if [ -d "$d" ]; then NF9=$((NF9+1)); fi; done
for d in /root/ga/archive-f8-*; do if [ -d "$d" ]; then NF8=$((NF8+1)); fi; done
for d in /root/ga/archive-f7-*; do if [ -d "$d" ]; then NF7=$((NF7+1)); fi; done
for d in /root/ga/archive-f6-*; do if [ -d "$d" ]; then NF6=$((NF6+1)); fi; done
for d in /root/ga/archive-f5-*; do if [ -d "$d" ]; then NF5=$((NF5+1)); fi; done
for d in /root/ga/archive-f4-*; do if [ -d "$d" ]; then NF4=$((NF4+1)); fi; done
[ "$N" -le 1 ] || { echo "REFUSE: more than one /root/ga/archive-$PREV-* directory"; exit 6; }
echo "archive-$PREV dirs counted: $N ; older archive-f10 dirs ignored: $NF10 ; older archive-f9 dirs ignored: $NF9 ; older archive-f8 dirs ignored: $NF8 ; older archive-f7 dirs ignored: $NF7 ; older archive-f6 dirs ignored: $NF6 ; older archive-f5 dirs ignored: $NF5 ; older archive-f4 dirs ignored: $NF4"
if [ "$N" = 1 ]; then
  if [ -e "$ARCH/ARCHIVE-DONE" ]; then AST=DONE; elif [ -e "$ARCH/.moved" ]; then AST=MOVED; else AST=PARTIAL; fi
else AST=NONE; ARCH=/root/ga/archive-$PREV-${STAMP:?}; fi
lock() { exec 9>/run/tbound-op.lock; flock -w 5 9 || { echo "tbound-op lock busy: a verb or a client-run is running"; exit 4; }; }
stop_units() {
  for u in tbound-pi.socket tbound-pi.service; do s=$(systemctl is-active $u || true); case $s in inactive|failed) ;; *) echo "stopping $u (was $s)"; systemctl stop $u;; esac; done
  for u in tbound-pi.service tbound-pi.socket; do s=$(systemctl is-active $u || true); case $s in inactive|failed) ;; *) echo "REFUSE: $u is $s after the stop"; exit 5;; esac; done
  echo "tbound-pi.service and tbound-pi.socket inactive"
}
mk_manifest() {
  for it in $ITEMS; do
    if here "$it"; then
      echo "## item /$it"
      echo "### owners-modes (type owner:group mode path -> link)"
      find "$it" -printf "%y %U:%G %m %p -> %l\n" | LC_ALL=C sort
      echo "### sha256 (path relative to the root directory)"
      find "$it" -type f -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum
    else echo "## absent /$it"; fi
  done
  echo "## live-root content (all entries below /$LIVE except lost+found; the directory itself is kept, never moved or removed)"
  if [ -d "$LIVE" ]; then
    find "$LIVE" -mindepth 1 ! -path "$LIVE/lost+found" ! -path "$LIVE/lost+found/*" -printf "%y %U:%G %m %p -> %l\n" | LC_ALL=C sort
    find "$LIVE" -mindepth 1 -type f ! -path "$LIVE/lost+found/*" -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum
  fi
}
body() { if [ -e "$ARCH/MANIFEST.txt" ]; then cat "$ARCH/MANIFEST.txt"; else mk_manifest; fi; }'

# Probe: read-only, no lock, nothing stopped.
REMOTE_PROBE='echo "archive-state=$AST dir=$ARCH"
if [ -e "$ARCH/MANIFEST.txt" ]; then echo "manifest-sha256=$(sha256sum "$ARCH/MANIFEST.txt" | cut -d" " -f1)"; fi
for it in $ITEMS; do if here "$it"; then echo "present /$it"; else echo "absent /$it"; fi; done
for d in $STATEDIRS; do if here "$d"; then
  stat -c "state-dir %u:%g %a %U:%G /%n" "$d"
  echo "size /$d files=$(find "$d" -type f | wc -l) bytes=$(du -sb "$d" | cut -f1)"
  find "$d" -mindepth 1 -maxdepth 2 -printf "   entry %y %U:%G %m %s /%p\n" | LC_ALL=C sort | head -40
fi; done
if [ -e "$CAT" ]; then echo "catalog $(stat -c "%u:%g %a %s bytes" "$CAT")"; else echo "catalog absent"; fi
echo "root.db: $(find /var/lib/tbound -xdev -name root.db | tr "\n" " ")"
echo "units: service=$(systemctl is-active tbound-pi.service || true) socket=$(systemctl is-active tbound-pi.socket || true)"
if [ -f /root/ga/state/g1.once ]; then echo "once=$(cat /root/ga/state/g1.once)"; fi
if [ -f /root/ga/state/g1 ]; then sed -n "s/^cfg=/statecfg=/p" /root/ga/state/g1; fi
sed "s/^/rid /" /root/ga/state/rids 2>/dev/null || true
echo "live mount root /var/lib/tbound/live (read-only listing; keeps lost+found): type mode owner path"
find /var/lib/tbound/live -mindepth 1 -maxdepth 1 -printf "mount %y %m %U:%G %P\n" | LC_ALL=C sort
echo "live root /var/lib/tbound/live/root: mount=$(findmnt -n -o TARGET --target $LIVEMNT 2>/dev/null || echo NOMOUNT) type=$(stat -c %F "$LIVE" 2>/dev/null || echo absent) owner=$(stat -c "%U:%G %a" "$LIVE" 2>/dev/null || echo -)"
if [ -d "$LIVE" ]; then
  for e in $(live_entries); do echo "live-entry /$LIVE/$e type=$(stat -c %F "/$LIVE/$e") owner=$(stat -c "%U:%G %a" "/$LIVE/$e")"; done
  echo "live-entries: $(live_entries | tr "\n" " ")"
  if [ -e "$LIVE/lost+found" ]; then echo "live lost+found: kept in place ($(stat -c "%U:%G %a" "$LIVE/lost+found"))"; else echo "live lost+found: absent"; fi
  if [ -f "$LIVE/src/release.txt" ]; then sed "s/^/live release.txt: /" "$LIVE/src/release.txt"; else echo "live release.txt: absent"; fi
else echo "live root /var/lib/tbound/live/root ABSENT (27 keeps this directory; see the gate)"; fi
echo "probe done"'

# Phase manifest: lock, make sure service and socket are stopped, print the manifest body between markers (read-only otherwise).
REMOTE_MANIFEST='[ "$AST" != DONE ] || { echo "ALREADY-DONE $ARCH"; exit 0; }
lock; stop_units
M=$(mktemp)
body > "$M"
echo "ARCH=$ARCH AST=$AST"
echo "MANIFEST-BEGIN"; cat "$M"; echo "MANIFEST-END"
echo "MANIFEST-SHA256 $(sha256sum "$M" | cut -d" " -f1)"
rm -f "$M"'

# Phase archive: ONE session under the lock. MANSHA = sha256 of the manifest the owner fetched.
REMOTE_ARCHIVE='[ "$AST" != DONE ] || { echo "ALREADY-DONE $ARCH"; exit 0; }
: "${MANSHA:?}"
lock; stop_units
T=$(mktemp)
body > "$T"
[ "$(sha256sum "$T" | cut -d" " -f1)" = "$MANSHA" ] || { echo "REFUSE: the state on the guest differs from the manifest the owner fetched (changed since the manifest phase?)"; rm -f "$T"; exit 7; }
if [ "$AST" = NONE ]; then
  for d in $STATEDIRS; do if here "$d"; then o=$(stat -c "%u:%g %a" "$d"); [ "$o" = "995:986 700" ] || { echo "REFUSE: /$d is $o but the runbook values are 995:986 700: recreating it would not restore the original"; rm -f "$T"; exit 8; }; fi; done
fi
for it in $ITEMS; do if here "$it" && mountpoint -q "/$it"; then echo "REFUSE: /$it is a mount point"; rm -f "$T"; exit 8; fi; done
if [ -L "$LIVE" ]; then echo "REFUSE: /$LIVE is a symlink: 27 keeps the live-root directory itself and never removes or recreates it; owner must look"; rm -f "$T"; exit 8; fi
if [ ! -d "$LIVE" ]; then echo "REFUSE: /$LIVE is missing. 27 moves only the CONTENT of the $PREVF live root and keeps the directory itself. Removing the root is NOT part of this script: owner must look (was it deleted outside 27? is the live mount up: findmnt $LIVEMNT)"; rm -f "$T"; exit 8; fi
if ! findmnt -n -o TARGET --target "$LIVEMNT" | grep -qx "$LIVEMNT"; then echo "REFUSE: $LIVEMNT is not mounted"; rm -f "$T"; exit 8; fi
echo "== archive dir $ARCH (state $AST)"
install -d -o root -g root -m 0700 "$ARCH" "$ARCH/rootfs"
[ -e "$ARCH/MANIFEST.txt" ] || install -o root -g root -m 0600 "$T" "$ARCH/MANIFEST.txt"
rm -f "$T"
if [ ! -e "$ARCH/.moved" ]; then
  for it in $ITEMS; do if here "$it" && here "$ARCH/rootfs/$it"; then echo "REFUSE: both /$it and $ARCH/rootfs/$it exist"; exit 3; fi; done
  for e in $(live_entries); do
    case "$e" in *" "*) echo "REFUSE: live-root entry name contains a space: $e"; exit 3;; esac
    if here "$LIVE/$e" && here "$ARCH/rootfs/$LIVE/$e"; then echo "REFUSE: both /$LIVE/$e and $ARCH/rootfs/$LIVE/$e exist"; exit 3; fi
  done
  echo "== move (nothing is deleted)"
  for it in $ITEMS; do
    if here "$it"; then mkdir -p "$ARCH/rootfs/$(dirname "$it")"; mv -- "/$it" "$ARCH/rootfs/$it"; echo "moved /$it -> $ARCH/rootfs/$it"
    else echo "absent /$it (nothing to move)"; fi
  done
  LE=$(live_entries)
  if [ -n "$LE" ]; then
    install -d -o root -g root -m 0700 "$ARCH/rootfs/$LIVE"
    for e in $LE; do mv -- "/$LIVE/$e" "$ARCH/rootfs/$LIVE/$e"; echo "moved /$LIVE/$e -> $ARCH/rootfs/$LIVE/$e"; done
  else echo "live root /$LIVE has no content to move (the directory itself is kept)"; fi
  : > "$ARCH/.moved"; sync
fi
echo "== verify the archive against the manifest (sha256, owner, group, mode)"
cd "$ARCH/rootfs"
mk_manifest > "$ARCH/.verify"
if cmp -s "$ARCH/MANIFEST.txt" "$ARCH/.verify"; then echo "VERIFIED: the archive equals the manifest"; rm -f "$ARCH/.verify"
else echo "REFUSE: the archive differs from the manifest (kept: $ARCH/.verify)"; diff "$ARCH/MANIFEST.txt" "$ARCH/.verify" | head -40; exit 9; fi
cd /
echo "== recreate the empty state (PROVISIONING-SIGNING.md I2; audit and publication-state as tbound-pi.service StateDirectory= creates them)"
install -d -o 995 -g 986 -m 0700 /var/lib/tbound/audit /var/lib/tbound/publication-state
install -d -o 995 -g 986 -m 0700 /var/lib/tbound/private-admin /var/lib/tbound/sessions
install -d -o 995 -g 986 -m 0700 $PA/workflow-recovery $SE/workflow-owners
if [ -e "$CAT" ]; then [ ! -s "$CAT" ] || { echo "REFUSE: $CAT exists and is not empty"; exit 10; }
else install -o 995 -g 986 -m 0600 /dev/null "$CAT"; fi
if here "$RDB"; then
  if [ -e "$ARCH/.root-init-done" ]; then echo "root.db exists and was created by this script (marker): workflow-root-init is not repeated"
  else echo "REFUSE: root.db exists but this script did not create it: stop and read it, do not delete root.db"; exit 11; fi
else
  OUTJ=$(setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- /usr/local/bin/tbound workflow-root-init) || { echo "workflow-root-init failed"; exit 12; }
  printf "%s\n" "$OUTJ"; printf "%s\n" "$OUTJ" > "$ARCH/workflow-root-init.json"; : > "$ARCH/.root-init-done"
fi
echo "== verify the fresh state (I2: tbound-pi:tbound-pi 700 for the directories, 600 for catalog.jsonl, exactly one root.db)"
bad=0
for d in var/lib/tbound/audit var/lib/tbound/publication-state var/lib/tbound/private-admin var/lib/tbound/sessions var/lib/tbound/private-admin/workflow-recovery var/lib/tbound/sessions/workflow-owners; do
  s=$(stat -c "%u:%g %a" "/$d"); echo "stat $(stat -c "%U:%G %a" "/$d") /$d"; [ "$s" = "995:986 700" ] || bad=1
done
s=$(stat -c "%u:%g %a" "$CAT"); echo "stat $(stat -c "%U:%G %a %s bytes" "$CAT") $CAT"; [ "$s" = "995:986 600" ] || bad=1
[ "$(stat -c %s "$CAT")" = 0 ] || bad=1
echo "stat $(stat -c "%U:%G %a %s bytes" "$RDB") $RDB"; [ "$(stat -c %u:%g "$RDB")" = "995:986" ] || bad=1
[ "$(find /var/lib/tbound -xdev -name root.db | wc -l)" = 1 ] || bad=1
[ "$(find /var/lib/tbound/audit /var/lib/tbound/publication-state -mindepth 1 | wc -l)" = 0 ] || bad=1
echo "== verify the kept live root: the directory itself stays, its content is archived, lost+found is untouched"
s=$(stat -c "%u:%g %a" "$LIVE"); echo "stat $(stat -c "%U:%G %a %F" "$LIVE") /$LIVE"; [ "$s" = "995:986 700" ] || bad=1
[ "$(findmnt -n -o TARGET --target "$LIVEMNT")" = "$LIVEMNT" ] || { echo "REFUSE: $LIVEMNT is no longer mounted"; bad=1; }
[ "$(live_entries | wc -l)" = 0 ] || { echo "REFUSE: the live root still has content: $(live_entries | tr "\n" " ")"; bad=1; }
[ "$(stat -c %d "$LIVE")" = "$(stat -c %d "$LIVEMNT")" ] || { echo "REFUSE: /$LIVE is not on the live mount"; bad=1; }
find $PA $SE /var/lib/tbound/audit /var/lib/tbound/publication-state -printf "fresh %y %U:%G %m %p\n" | LC_ALL=C sort
find "$LIVE" -mindepth 1 -maxdepth 1 -printf "kept %y %U:%G %m %p\n" | LC_ALL=C sort
[ "$bad" = 0 ] || { echo "REFUSE: the fresh state or the kept live root differs from the runbook values"; exit 13; }
: > "$ARCH/ARCHIVE-DONE"; sync
systemctl start tbound-pi.socket
echo "socket: $(systemctl is-active tbound-pi.socket || true)"
echo "ARCHIVE-DONE $ARCH"'

RS_PROBE="$REMOTE_COMMON$NL$REMOTE_PROBE"
RS_MANIFEST="$REMOTE_COMMON$NL$REMOTE_MANIFEST"
RS_ARCHIVE="$REMOTE_COMMON$NL$REMOTE_ARCHIVE"
case "$REMOTE_COMMON$REMOTE_PROBE$REMOTE_MANIFEST$REMOTE_ARCHIVE" in *"'"*) die "internal: a remote script contains a single quote";; esac

# ---- 1. probe (read-only) ----------------------------------------------------------------------------------------------------------------------
say "1. what is on the guest (password sudo, read-only); only archive-$PREV_LC-* counts, archive-f10-*, archive-f9-*, archive-f8-*, archive-f7-*, archive-f6-*, archive-f5-* and archive-f4-* are the older archives of the $PREV_FREEZE, F10, F9, F8, F7, F6 and F5 kits"
PROBE=$(pw "env STAMP=$TS PREV=$PREV_LC PREVF=$PREV_FREEZE FREEZEF=$FREEZE sh -c '$RS_PROBE'" | tr -d '\r')
printf '%s\n' "$PROBE"
AST=$(printf '%s\n' "$PROBE" | sed -n 's/^archive-state=\([A-Z]*\) dir=.*/\1/p' | head -1)
ADIR=$(printf '%s\n' "$PROBE" | sed -n 's/^archive-state=[A-Z]* dir=\(.*\)$/\1/p' | head -1)
case "$AST" in NONE|PARTIAL|MOVED|DONE) ;; *) die "no archive state in the probe output: $AST";; esac
echo "archive state: $AST ($ADIR)"
RIDS=$(printf '%s\n' "$PROBE" | sed -n 's/^rid //p')
eq "$PREV_FREEZE g1/RID record == PREV_G1_RID" "$(prev_get g1/RID)" "$PREV_G1_RID"
eq "$PREV_FREEZE rp/RID record == PREV_RP_RID" "$(prev_get rp/RID)" "$PREV_RP_RID"
eq "$PREV_FREEZE dr/RID record == PREV_DR_RID" "$(prev_get dr/RID)" "$PREV_DR_RID"
say "tbound-op status g1 (narrowed sudo)"
ST0=$(op "status g1" | tr -d '\r'); printf '%s\n' "$ST0"
[[ $ST0 == *"anchor: ok"* ]] || die "anchor not ok"
case "$ST0" in *"pins F=$PREV_COMMIT"*) echo "pins: $PREV_FREEZE";; *"pins F=$FREEZE_COMMIT"*) echo "pins: $FREEZE (10-upgrade.sh ran; the archive does not depend on the chain)";; *) die "tbound-op pins are neither $PREV_FREEZE nor $FREEZE: stop and look";; esac

# ---- 2. terminal state of every dispatcher request (collected journal + reqstate.py) ---------------------------------------------------------------
say "2. request journal and catalog (tbound-op collect, narrowed sudo); terminal state per request id"
JOURNAL_OK=0
if op "collect requests"; then
  if fetch_export_nz "$OUT/_/serve-pi-requests.jsonl" "g1-prior/requests-27-$TS.jsonl"; then
    JOURNAL_OK=1
    VERDICTS=""
    for r in $RIDS; do
      req_check "$HF/g1-prior/requests-27-$TS.jsonl" "$r"
      VERDICTS="$VERDICTS$r $REQ_VERDICT$NL"
    done
    req_check "$HF/g1-prior/requests-27-$TS.jsonl" "$PREV_G1_RID"
    [ "$REQ_VERDICT" = completed ] || die "$PREV_FREEZE G1 journal is not completed: $REQ_VERDICT"
    [[ $RIDS == *"$PREV_RP_RID"* ]] || { req_check "$HF/g1-prior/requests-27-$TS.jsonl" "$PREV_RP_RID"; VERDICTS="$VERDICTS$PREV_RP_RID $REQ_VERDICT$NL"; }
    [[ $RIDS == *"$PREV_DR_RID"* ]] || { req_check "$HF/g1-prior/requests-27-$TS.jsonl" "$PREV_DR_RID"; VERDICTS="$VERDICTS$PREV_DR_RID $REQ_VERDICT$NL"; }
  fi
else
  note "tbound-op collect requests failed: no request journal on the guest (expected after the archive; before it the state is not the $PREV_FREEZE one)"
fi
if op "collect catalog"; then
  if fetch_export_nz "$OUT/_/catalog.jsonl" "g1-prior/catalog-27-$TS.jsonl"; then
    WB "f='$HF/g1-prior/catalog-27-$TS.jsonl'; printf 'catalog lines: %s\n' \$(wc -l < \$f); for k in pi_workflow_admission_reserved pi_workflow_fresh_authorized pi_workflow_owner_scope_bound pi_workflow_owner_scope_drained pi_workflow_terminal; do printf '  %s: %s\n' \$k \$(grep -c \$k \$f || true); done" | tr -d '\r'
  fi
else
  note "tbound-op collect catalog failed"
fi
echo "request id -> journal verdict:"; printf '%s' "${VERDICTS:-}" | sed 's/^/   /'
PREVV=$(printf '%s' "${VERDICTS:-}" | sed -n "s/^$PREV_RP_RID //p" | head -1)
PREVDV=$(printf '%s' "${VERDICTS:-}" | sed -n "s/^$PREV_DR_RID //p" | head -1)

if [ "$MODE" = status ]; then
  say "would move (archive): present items and the live-root content into /root/ga/archive-$PREV_LC-<UTC>/rootfs/, then recreate the empty state"
  printf '%s\n' "$PROBE" | sed -n 's/^present \//   \//p'
  printf '%s\n' "$PROBE" | sed -n 's/^live-entry \//   \//p'
  echo "   recreate: audit, publication-state, private-admin, sessions (995:986 0700); workflow-recovery, workflow-owners (995:986 0700); catalog.jsonl (995:986 0600, empty); root.db via tbound workflow-root-init as uid 995 gid 986"
  echo "   kept: the live-root DIRECTORY /var/lib/tbound/live/root itself (same inode/owner/mode/mount, emptied) and its lost+found; /var/lib/tbound/live/lost+found; /root/ga/state/rids; /root/ga/prev/g1; /var/tmp/tbound-op/_"
  [ "$AST" = NONE ] || note "an archive dir exists ($AST): archive would continue/confirm it, see the state above"
  [ -z "$PREVV" ] || echo "$PREV_FREEZE rp request $PREV_RP_RID: journal verdict $PREVV"
  [ -z "$PREVDV" ] || echo "$PREV_FREEZE dr request $PREV_DR_RID: journal verdict $PREVDV"
  note "status only: nothing changed. Next (owner approval of option A): bash 27-archive-state.sh archive"
  done_step; exit 0
fi

# ---- 3. archive ---------------------------------------------------------------------------------------------------------------------------------
if [ "$AST" = DONE ]; then
  ok "already archived: $ADIR has ARCHIVE-DONE; nothing left to move"
  MSHA_G=$(printf '%s\n' "$PROBE" | sed -n 's/^manifest-sha256=//p' | head -1)
  if archived_prev; then ok "owner record g1-prior/archive-done.txt exists: $(hf_get g1-prior/archive-done.txt)"
  else
    hf_has g1-prior/archive-manifest.txt && eq "owner manifest sha256 == guest MANIFEST.txt" "$(hf_sha g1-prior/archive-manifest.txt)" "$MSHA_G"
    hf_put g1-prior/archive-done.txt "archive_dir=$ADIR manifest_sha256=$MSHA_G"
  fi
  done_step; exit 0
fi

say "3. owner gates"
if [ "$AST" = NONE ]; then
  ONCE=$(printf '%s\n' "$PROBE" | sed -n 's/^once=//p' | head -1)
  [ -z "$ONCE" ] || eq "one-use marker /root/ga/state/g1.once" "$ONCE" "client=$PREV_G1_RID"
  RPONCE=$(pw "sh -c 'if [ -f /root/ga/state/rp.once ]; then cat /root/ga/state/rp.once; fi'" | tr -d '\r')
  [ -z "$RPONCE" ] || eq "$PREV_FREEZE rp one-use marker" "$RPONCE" "client=$PREV_RP_RID"
  RPCFG=$(pw "sh -c 'if [ -f /root/ga/state/rp ]; then sed -n \"s/^cfg=//p\" /root/ga/state/rp; fi'" | tr -d '\r')
  [ -z "$RPCFG" ] || eq "$PREV_FREEZE rp state config" "$RPCFG" "$(wd "$PREV_HF/rp/serve-pi-service.json")"
  DRONCE=$(pw "sh -c 'if [ -f /root/ga/state/dr.once ]; then cat /root/ga/state/dr.once; fi'" | tr -d '\r')
  [ -z "$DRONCE" ] || eq "$PREV_FREEZE dr one-use marker" "$DRONCE" "client=$PREV_DR_RID"
  DRCFG=$(pw "sh -c 'if [ -f /root/ga/state/dr ]; then sed -n \"s/^cfg=//p\" /root/ga/state/dr; fi'" | tr -d '\r')
  [ -z "$DRCFG" ] || eq "$PREV_FREEZE dr state config" "$DRCFG" "$(wd "$PREV_HF/dr/serve-pi-service.json")"
  SCFG=$(printf '%s\n' "$PROBE" | sed -n 's/^statecfg=//p' | head -1)
  [ -z "$SCFG" ] || eq "state-g1 cfg == sha256 of the $PREV_FREEZE g1 service config" "$SCFG" "$(wd "$PREV_HF/g1/serve-pi-service.json")"
  [ "$JOURNAL_OK" = 1 ] || die "no readable request journal on the guest and no archive dir: this is not the $PREV_FREEZE state this script was written for"
  case "$PREVV" in
    "") die "the $PREV_FREEZE rp request $PREV_RP_RID has no journal verdict";;
    not-journaled|ambiguous|no-terminal) die "journal verdict '$PREVV' for $PREV_RP_RID: no complete $PREV_FREEZE route-probe request (no-terminal means it may still run). Read $HF/g1-prior/requests-27-$TS.jsonl and decide by hand";;
    completed) ok "$PREV_RP_RID is terminal completed (the $PREV_FREEZE route probe passed; not claim-bearing)";;
    *) note "$PREV_RP_RID has journal verdict '$PREVV' (the $PREV_FREEZE route probe is archived as evidence); archiving because the owner chose A";;
  esac
  case "$PREVDV" in
    "") die "the $PREV_FREEZE dr request $PREV_DR_RID has no journal verdict";;
    not-journaled) die "journal verdict 'not-journaled' for $PREV_DR_RID: the $PREV_FREEZE dr request was accepted and ran (its route-probe launch proof is journaled), so a missing journal record is NOT the UNKNOWN-by-design outcome. Read $HF/g1-prior/requests-27-$TS.jsonl and decide by hand";;
    ambiguous|no-terminal|inconsistent) die "journal verdict '$PREVDV' for $PREV_DR_RID: the $PREV_FREEZE dr request is not one complete terminal unknown record (no-terminal means it may still run). Read $HF/g1-prior/requests-27-$TS.jsonl and decide by hand";;
    unreconciled|terminal-other|completed|abandoned)
      # The $PREV_FREEZE dr request ended UNKNOWN BY DESIGN: the route-probe launch proof was journaled, Pi was never started, and the client returned
      # nonzero. reqstate.py reports that terminal as "unreconciled" (unknown, no abandoned record). The verdict is accepted ONLY when the
      # recorded response is status "unknown" with error_code "task-outcome-unknown" AND the $PREV_FREEZE dr/CHECKS_OK record (written after the
      # launch-proof check) is "ok". Anything else is a stop: the request may still run.
      [ "$(prev_get dr/CHECKS_OK)" = "$PREV_DR_CHECKS_OK" ] || die "the $PREV_FREEZE dr/CHECKS_OK record is not '$PREV_DR_CHECKS_OK': the dr run did not pass its checks; decide by hand"
      DRRESP=$(prev_get dr/response.txt)
      case $DRRESP in
        *'"status":"unknown"'*'"error_code":"task-outcome-unknown"'*) ok "$PREV_DR_RID is terminal UNKNOWN BY DESIGN (journal verdict $PREVDV; response status unknown / task-outcome-unknown; launch proof journaled, Pi not started; CHECKS_OK): nothing can still run";;
        *) die "journal verdict '$PREVDV' for $PREV_DR_RID but its recorded response is not status unknown / error_code task-outcome-unknown: decide by hand";;
      esac;;
    *) die "journal verdict '$PREVDV' for $PREV_DR_RID: unexpected state; decide by hand";;
  esac
  printf '%s' "${VERDICTS:-}" | while read -r r v; do
    [ -n "$r" ] && [ "$v" = no-terminal ] && { echo "FATAL: request $r has no terminal record (it may still run)" >&2; exit 1; }
    true
  done || die "a request without a terminal record exists: stop and look"
  ok "no request without a terminal record"
  # $PREV_FREEZE G1 published successfully; preserve the fixture and publication output as evidence (the $PREV_FREEZE rp was NO_DIFF and the dr ended UNKNOWN by design).
  PREVDRAFT="$(printf 'version=2.4.0\nstatus=draft')"
  PREVFINAL="$(printf 'version=2.4.0\nstatus=final')"
  if ! pw 'test -f /var/lib/tbound/live/root/src/release.txt'; then
    die "the live root holds no src/release.txt: the $PREV_FREEZE seed-live g1 fixture is not intact (the $PREV_FREEZE G1 completed), so this is not the $PREV_FREEZE state this script was written for. Stop and read the listing above (nothing has moved)"
  elif REL=$(pw 'cat /var/lib/tbound/live/root/src/release.txt' | tr -d '\r') && [ "$REL" = "$PREVFINAL" ]; then
    ok "live-root control file src/release.txt carries the $PREV_FREEZE g1 edit (version=2.4.0, status=final): the known $PREV_FREEZE outcome, archived as evidence"
  elif [ "$REL" = "$PREVDRAFT" ]; then
    note "live-root control file src/release.txt is still the seeded $PREV_FREEZE fixture (version=2.4.0, status=draft): unexpected after the completed $PREV_FREEZE G1; archived as evidence, owner must read why"
  else
    die "live-root src/release.txt is neither the $PREV_FREEZE g1 edit (status=final) nor the seeded fixture (status=draft): '$REL'. Stop and read it before archiving (nothing has moved)"
  fi
  pw 'test -d /var/lib/tbound/live/root/out' || die "the live root holds no out/ directory: the $PREV_FREEZE seed-live g1 fixture is not intact (RG-7 created it). Stop and read the listing above (nothing has moved)"
  OUTENT=$(pw 'ls -A /var/lib/tbound/live/root/out 2>/dev/null | LC_ALL=C sort' | tr -d '\r')
  case "$OUTENT" in
    "") ok "live-root out/ is empty: the $PREV_FREEZE bash cell left no out/release.sha256 (G1 completed; preserve this observed layout)" ;;
    "release.sha256") ok "live-root out/ holds exactly release.sha256 (written by the $PREV_FREEZE bash cell from the completed G1 run); it is archived as evidence, not judged" ;;
    *) die "live-root out/ holds something other than release.sha256: '$OUTENT'. Stop and read it before archiving (nothing has moved)" ;;
  esac
else
  note "archive dir state $AST: continuing the earlier run (items and journal are not re-judged; the stored manifest is authoritative)"
fi

socket_up_pw() { echo "cleanup: tbound-pi.socket must be active again"; GPW "sudo -S -p '' systemctl start tbound-pi.socket" || true; }
cleanup_add socket_up_pw

say "4. PRIVILEGED WRITE: tbound-op svc stop, then the manifest phase (lock, service and socket stopped)"
op "svc stop"
MOUT=$(pw "env STAMP=$TS PREV=$PREV_LC PREVF=$PREV_FREEZE FREEZEF=$FREEZE sh -c '$RS_MANIFEST'" | tr -d '\r') || { printf '%s\n' "$MOUT"; die "manifest phase failed (see above); the exit trap starts the socket again"; }
printf '%s\n' "$MOUT" | sed -n '1,/^MANIFEST-BEGIN$/p;/^MANIFEST-END$/,$p'
if grep -q '^ALREADY-DONE ' <<<"$MOUT"; then die "the guest says ALREADY-DONE although the probe did not: run again"; fi
MBODY=$(printf '%s\n' "$MOUT" | sed -n '/^MANIFEST-BEGIN$/,/^MANIFEST-END$/p' | sed '1d;$d')
MSHA=$(printf '%s\n' "$MOUT" | sed -n 's/^MANIFEST-SHA256 \([0-9a-f]\{64\}\)$/\1/p' | head -1)
[ -n "$MBODY" ] && [ -n "$MSHA" ] || die "no manifest in the output of the manifest phase"
echo "manifest: $(printf '%s\n' "$MBODY" | wc -l | tr -d ' ') lines, $(printf '%s\n' "$MBODY" | grep -c '^[0-9a-f]\{64\}  ') files, sha256 $MSHA"
MN=g1-prior/archive-manifest.txt
if hf_has "$MN" && [ "$(hf_sha "$MN")" != "$MSHA" ]; then MN=g1-prior/archive-manifest-$TS.txt; note "an earlier $HF/g1-prior/archive-manifest.txt differs; this one is saved as $MN"; fi
if ! hf_has "$MN"; then printf '%s\n' "$MBODY" | hf_save "$MN"; hf_commit "$MN"; fi
eq "owner copy $MN sha256 == guest manifest sha256" "$(hf_sha "$MN")" "$MSHA"

say "5. PRIVILEGED WRITE: archive (move, verify, recreate empty state, ONE session under /run/tbound-op.lock)"
rc=0
AOUT=$(pw "env STAMP=$TS MANSHA=$MSHA PREV=$PREV_LC PREVF=$PREV_FREEZE FREEZEF=$FREEZE sh -c '$RS_ARCHIVE'" | tr -d '\r') || rc=$?
printf '%s\n' "$AOUT"
printf '%s\n' "$AOUT" | hf_save "g1-prior/archive-run-$TS.txt"; hf_commit "g1-prior/archive-run-$TS.txt"
[ "$rc" = 0 ] || die "archive session failed (rc $rc, see above). Nothing was deleted. Re-run bash 27-archive-state.sh archive to continue; the socket is started by the exit trap"
ARCHDIR=$(printf '%s\n' "$AOUT" | sed -n "s/^ARCHIVE-DONE \\(\\/root\\/ga\\/archive-$PREV_LC-[0-9TZ]*\\)$/\\1/p" | head -1)
[ -n "$ARCHDIR" ] || die "the archive session did not print ARCHIVE-DONE"
ok "archived into $ARCHDIR; fresh state verified on the guest"
hf_put g1-prior/archive-done.txt "archive_dir=$ARCHDIR manifest_sha256=$MSHA"

say "6. proof"
ST=$(op "status g1" | tr -d '\r'); printf '%s\n' "$ST"
grep -qx -- '-- g1: no state' <<<"$ST" || die "tbound-op status g1 still reports a g1 state"
[[ $ST != *"-- g1 one-use markers"* ]] || die "tbound-op status g1 still reports a one-use marker"
ok "tbound-op status g1: no state, no one-use marker"
eq "tbound-pi.socket" "$(G 'systemctl is-active tbound-pi.socket' | tr -d '\r' || true)" active
note "service state: $(G 'systemctl is-active tbound-pi.service' | tr -d '\r' || true) (the socket starts it on demand; nothing started it on purpose)"
if G 'sudo -n true' >/dev/null 2>&1; then die "sudo -n true works: the narrowed sudo is not in place"; fi
ok "sudo -n true fails (narrowed sudo unchanged; this script only used password sudo and tbound-op)"
AFTER=$(pw "env STAMP=$TS PREV=$PREV_LC PREVF=$PREV_FREEZE FREEZEF=$FREEZE sh -c '$RS_PROBE'" | tr -d '\r')
note "live root after the archive: $(printf '%s\n' "$AFTER" | sed -n 's/^live-entries: //p' | head -1)"
note "the live root directory /var/lib/tbound/live/root was KEPT (only its content moved into the archive); its lost+found, if any, stayed. 30-reg-g1.sh runs tbound-op seed-live g1 to re-create the canonical g1 fixture (it stops with 'owner must look' if the root holds something unexpected)"
note "next: 00-session-sudo.sh open, 10-upgrade.sh, 20-r1.sh, 50-i1-i2.sh, 00-session-sudo.sh close, 29-g1-recover.sh status|move (finds nothing left), 30-reg-g1.sh"
done_step
