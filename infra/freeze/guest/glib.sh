#!/bin/bash
# Generic TBound freeze kit guest provisioning session: shared helpers. SOURCED by the step scripts in Git Bash (not run directly).
# No secrets here. Key files are only ever passed to tools by PATH; nothing in this kit reads or prints one.
# One reusable kit: every freeze-specific value comes from freezes/$KIT_FREEZE.env (selected by KIT_FREEZE).
# Owner-side files (W, B, H, HF = $H/<freeze>, PREV_HF = $H/<prev> read-only) live on the WSL ext4 disk and are reached through wsl.exe;
# the guest is reached through the helper tools/gssh.sh (tboundadmin), both Git Bash ssh.
umask 077
set -E
export MSYS_NO_PATHCONV=1

. "$(dirname -- "${BASH_SOURCE[0]}")/../paths.sh"
A=$KIT_DIR
GA=$A/guest
WGA=${TBOUND_WGA:-$(linux_path "$GA")}      # same dir, as Linux sees it; TBOUND_WGA: a Linux-side copy, so WSL never reads the kit through /mnt
GSSH_SH=$A/tools/gssh.sh

SCR=/var/tmp/tbound-cal-20261017                        # guest scratch (995:986 0700)
BK=/opt/tbound/bin/dr-backup                            # real files saved by the F0 dry run
TBPG=/usr/local/bin/tbound-profile                      # guest tbound-profile
SVC='sudo -n setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs --'
PODMAN_ENV='env -C / -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/var/lib/tbound/podman-home USER=tbound-pi LOGNAME=tbound-pi XDG_RUNTIME_DIR=/run/tbound-pi XDG_CONFIG_HOME=/etc/tbound/podman-config XDG_DATA_HOME=/var/lib/tbound/podman-home/data XDG_CACHE_HOME=/run/tbound-pi/cache TMPDIR=/run/tbound-pi/tmp CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf LANG=C LC_ALL=C /usr/bin/podman'   # no --root: --root makes podman ignore storage.conf options (ignore_chown_errors); the service never passes it
PODMAN_SVC="$SVC $PODMAN_ENV"

# Pins that do not depend on the frozen commit (runbook "Unchanged" list and F0 receipt).
PIN_PODMAN=sha256:859cb7e29723d1ca2f6bec025405b7ed6ea3040709ed129d7fed5f8fca92d2ca
PIN_CRUN=sha256:bd8ad7e7798edd5a857476c33bbef463b8b60ca2db43d7feb77698a703060c84
PIN_COSIGN=sha256:4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71
PIN_SECCOMP=sha256:cc374cf23846ce1f62f4dc807a8e2b8673c783c6f56cb475467621035d281e6c
PIN_NODE=sha256:7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c
F0_BUNDLE_DIGEST=sha256:8e405ef0f2a3a3ed725dba2815d71bd8e5db007fe993f4ac3979ad59016d4f48
F0_BUNDLE_TAR_SHA=sha256:d0abdaf300ea8e6eacfc0c93df1f5032cf2bf84f686122bc4289c820e97e0471
F0_BUNDLE_TAR_SIZE=429660160
F0_BASH_DIGEST=sha256:55b89ab22bee4792a210f493a53fb066accd5d30b69837c28d98be5ff863efcf
F0_NODE_DIGEST=sha256:7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c
F0_IMGDIGEST=sha256:d02a7543041a52d864fb097f636a591ff1d0d86bfc38fedce645c1537176bd3d
F0COMMIT=c7f9e9ce2516f0377c4ca59a84eea3c55aa18fc5
F1COMMIT=b42b920e9ad3f70e0082aa26d26fe4a27df64e4e
F2COMMIT=feb5c7bc26749705af80cddc82f22b65914e84b4
F3COMMIT=ea5970694252d33f44305b0f9c97afad4164085f       # F3 (ancestor; only for the readonly comparison chain)
F4COMMIT=cb163038e64bee59b550d23e4b0a68581465ff76       # F4 (ancestor)
F5COMMIT=6749cc6d13e0e6be39a27349f9ebaca67b41411a       # F5 (ancestor)
F6COMMIT=e37d5ad418423cf20dae3ada4c1607028d8775be       # F6 (ancestor)
F7COMMIT=608f397be19d994bbe37538ab027001b2f057396   # F7 (ancestor)
F8COMMIT=54c578e51e8af303d125a435b18781179d9aee21   # F8 ancestor
F9COMMIT=af6f38f0bc97a3e62650404bded5b632c9f452c0   # F9 (older freeze)
F10COMMIT=bcac4cdfdc797ede41b69a32fdaa00053565b89e  # F10 (ancestor)

PIN_MAPPING=sha256:30bd0f961c016eaeba90fa5e80a8e4d65893ae1dd142e466a1a4ea21cf5bf7f4   # DigestD09UserNamespaceMapping(1000,1000,995,986)
F0_ADMIN_PUB_DIGEST=sha256:be206a41ad321f29ef89e8157f69a6f0820a93fafa3096b71867ece745f9afac
F0_COSIGN_PUB_DIGEST=sha256:caf906a4ead6cd8998502333030f0602e66302e47b1539afebe5a13c39e66bcd
F0_MOPT=sha256:4ae60254afe183f9f8b49d2956ca8ee68ba00380a20111b8ccb12fed35581cb3         # runtime mount-options digest, measured 2026-10-15 and 2026-10-20
LOCAL_TMP=""

say()  { printf '\n== %s\n' "$*"; }
ok()   { printf 'OK   %s\n' "$*"; }
note() { printf 'NOTE %s\n' "$*"; }
die()  { printf 'FATAL: %s\n' "$*" >&2; exit 1; }

# --- the one values file ----------------------------------------------------------------------------------------
GUEST_IP_ENV=${GUEST_IP:-}
DR_ADMISSION_ID_ENV=${DR_ADMISSION_ID:-}
. "$(dirname -- "${BASH_SOURCE[0]}")/../freeze-guard.sh" || exit 2
V=$VALUES
# shellcheck disable=SC1090
. "$V"
[ "$FREEZE" = "$KIT_FREEZE" ] || die "$V says FREEZE=$FREEZE but KIT_FREEZE=$KIT_FREEZE"
[ "${PREV_FREEZE:-}" != none ] || die "$V: PREV_FREEZE=none starts a new chain, and the guest scripts do not support that yet"
for v in FREEZE FREEZE_COMMIT PREV_FREEZE PREV_COMMIT PREV_W PREV_NAMES PREV_G1_RID PREV_RP_RID PREV_DR_RID PREV_DR_RUNID PREV_DR_CHECKS_OK PREV_DR_ADMISSION_ID DR_ADMISSION_ID DR_ADMISSION_REFUSE GUEST_IP; do
  [ -n "${!v:-}" ] || die "$V lacks $v"
done
[ "$PREV_FREEZE" != "$FREEZE" ] || die "$V: PREV_FREEZE equals FREEZE"
[[ $PREV_COMMIT =~ ^[0-9a-f]{40}$ ]] || die "$V: PREV_COMMIT is not a 40-hex lowercase commit: '$PREV_COMMIT'"
GUEST_IP=${GUEST_IP_ENV:-$GUEST_IP}; export GUEST_IP
DR_ADMISSION_ID=${DR_ADMISSION_ID_ENV:-$DR_ADMISSION_ID}; export DR_ADMISSION_ID
FREEZE_LC=$(printf '%s' "$FREEZE" | tr 'A-Z' 'a-z')
PREV_LC=$(printf '%s' "$PREV_FREEZE" | tr 'A-Z' 'a-z')
HF=$OLDH/$FREEZE_LC
CF=$OLDH/c$FREEZE_LC
PREV_HF=$OLDH/$PREV_LC
PREV_CF=$OLDH/c$PREV_LC
GOUT=$TBOUND_WORK_VIEW/receipts/$FREEZE
GLOG=$GOUT/logs
F=$FREEZE_COMMIT

# FREEZE_COMMIT must be a real 40-hex commit: every guest script sources this file, so every one of them stops here while the placeholder is set.
[ "$FREEZE_COMMIT" != FROZEN_COMMIT_TBD ] || die "FREEZE_COMMIT is still the placeholder FROZEN_COMMIT_TBD: set it in $V (line 'FREEZE_COMMIT=') once the $FREEZE commit is known"
[[ $FREEZE_COMMIT =~ ^[0-9a-f]{40}$ ]] || die "FREEZE_COMMIT is not a 40-hex lowercase commit: '$FREEZE_COMMIT'"
[ "$FREEZE_COMMIT" != "$PREV_COMMIT" ] && [ "$FREEZE_COMMIT" != "$F10COMMIT" ] && [ "$FREEZE_COMMIT" != "$F9COMMIT" ] && [ "$FREEZE_COMMIT" != "$F8COMMIT" ] && [ "$FREEZE_COMMIT" != "$F7COMMIT" ] && [ "$FREEZE_COMMIT" != "$F6COMMIT" ] && [ "$FREEZE_COMMIT" != "$F5COMMIT" ] && [ "$FREEZE_COMMIT" != "$F4COMMIT" ] && [ "$FREEZE_COMMIT" != "$F3COMMIT" ] && [ "$FREEZE_COMMIT" != "$F2COMMIT" ] && [ "$FREEZE_COMMIT" != "$F1COMMIT" ] && [ "$FREEZE_COMMIT" != "$F0COMMIT" ] || die "FREEZE_COMMIT equals an earlier freeze ($PREV_FREEZE, F10, F9, F8, F7, F6, F5, F4, F3, F2, F1 or F0): nothing to re-freeze"

# logto <name>: stdout+stderr to the console and to $GLOG/<name>.log (append).
logto() {
  make_receipts_dir
  exec > >(tee -a "$GLOG/$1.log") 2>&1
  echo "== $1 start $(date -u +%FT%TZ) guest=$GUEST_IP"
  trap 'echo "== FAILED ${0##*/}:$LINENO rc=$?" >&2' ERR
}

# Cleanups run in reverse order at exit (also after a failure). Register with cleanup_add <function-name>.
_CLEAN=()
cleanup_add() { _CLEAN+=("$1"); }
_on_exit() {
  local rc=$? i
  for ((i = ${#_CLEAN[@]} - 1; i >= 0; i--)); do "${_CLEAN[i]}" || true; done
  [ -z "$LOCAL_TMP" ] || rm -rf "$LOCAL_TMP"
  [ "$rc" -eq 0 ] && sleep 1   # let tee flush the log
  exit "$rc"
}
trap _on_exit EXIT
local_tmp() { [ -n "$LOCAL_TMP" ] || LOCAL_TMP=$(mktemp -d); }

# --- transports -------------------------------------------------------------------------------------------------
G()  { sh "$GSSH_SH" "$@" </dev/null; }          # guest as tboundadmin, no stdin
GI() { sh "$GSSH_SH" "$@"; }                     # guest as tboundadmin, stdin passed through (X | GI 'sudo -n install ... /dev/stdin dest')
case $(uname -s) in MINGW* | MSYS*) WXP=(wsl.exe -d "$TBOUND_WSL_DISTRO" --cd / -e) ;; *) WXP=() ;; esac   # as paths.sh: WSL itself is Linux
WX()  { "${WXP[@]}" env KIT_FREEZE="$KIT_FREEZE" "$@" </dev/null; }   # WSL, direct exec (no shell), no stdin; KIT_FREEZE reaches the Python guards
WB()  { "${WXP[@]}" bash -c "export KIT_FREEZE=$KIT_FREEZE; $1" </dev/null; }   # WSL, bash -c <one string>, no stdin
WBI() { "${WXP[@]}" bash -c "export KIT_FREEZE=$KIT_FREEZE; $1"; }              # WSL, bash -c <one string>, stdin passed through (writers: X | WBI "cat > file")
svc()    { G "cd / && $SVC $1"; }                # command in the service context (uid 995, no-new-privs, empty caps)
podsvc() { G "cd / && $PODMAN_SVC $1"; }

# --- digests ----------------------------------------------------------------------------------------------------
isdig()    { [[ ${1:-} =~ ^sha256:[0-9a-f]{64}$ ]]; }
need_dig() { isdig "${2:-}" || die "$1 is not sha256:<64 hex>: '${2:-}'"; }
hexof()    { printf '%s' "${1#sha256:}"; }
eq()       { [ "$2" = "$3" ] || die "$1 mismatch: got '$2' want '$3'"; ok "$1 = $2"; }
has_edges() { # label value prefix suffix  (runbook pins written as aaaa...bbbb)
  case "$2" in "$3"*"$4") ok "$1 = $2 (matches $3...$4)";; *) die "$1 = $2 does not match the runbook pin $3...$4";; esac
}
wd()   { printf 'sha256:%s\n' "$(WX sha256sum "$1" | cut -d' ' -f1)"; }                  # digest of a WSL file
gsha() { printf 'sha256:%s\n' "$(G "sudo -n sha256sum $1" | cut -d' ' -f1)"; }             # digest of one guest file
declare -A GH
gsums() { # fills GH[path]=sha256:... for the given guest paths (one ssh call); dies if any is missing
  local out h p
  out=$(G "sudo -n sha256sum $*") || die "sha256sum failed on the guest for: $*"
  while read -r h p; do GH[$p]="sha256:$h"; done <<<"$out"
  for p in "$@"; do [ -n "${GH[$p]:-}" ] || die "no sha256 for guest path $p"; done
}

# --- owner-side value store: $HF (WSL ext4, 0700) ----------------------------------------------------------------
hf_has() { WX test -e "$HF/$1"; }
hf_get() { WX cat "$HF/$1" | tr -d '\r\n'; }
hf_sha() { WX sha256sum "$HF/$1" | cut -d' ' -f1; }
hf_put() { # name value: record a value; an existing different value is a stop
  local n=$1 v=$2 cur
  if hf_has "$n"; then
    cur=$(hf_get "$n"); [ "$cur" = "$v" ] || die "$HF/$n already holds '$cur', refusing to replace it with '$v'"
    ok "$n already recorded: $v"; return 0
  fi
  printf '%s\n' "$v" | WBI "umask 077; set -C; cat > $HF/$n"
  WX test -s "$HF/$n" || die "$HF/$n is 0 bytes after the write (WSL disk stall?): stop"
  ok "recorded $n = $v"
}
# hf_save <name> (stdin) then hf_commit <name>: write to .partial first so a failed pipeline never leaves a half file.
hf_save()   { local n=$1; hf_has "$n" && die "$HF/$n exists"; WX rm -f "$HF/$n.partial"; WBI "umask 077; set -C; cat > $HF/$n.partial"; }
# A 0-byte export is a stop (a WSL disk stall once produced 0-byte files that were reported as DONE). Stderr captures (*.err) may be empty.
# The dr run's command and provider audits are genuinely empty (a probe starts no Pi and runs no command), so exactly those two files may stay
# empty; no other empty export is accepted.
hf_commit() {
  if ! WX test -s "$HF/$1.partial"; then
    case "$1" in
      *.err|dr/command-audit.jsonl|dr/provider-audit.jsonl) ;;
      *) die "$HF/$1.partial is 0 bytes (WSL disk stall, or a really empty export?): stop and look";;
    esac
  fi
  WX mv "$HF/$1.partial" "$HF/$1"; WX sync; ok "saved $HF/$1"
}
jgetw() { WX python3 -I "$WGA/jget.py" "$1" "$2" | tr -d '\r\n'; }          # one JSON member of a WSL file

# --- environment ------------------------------------------------------------------------------------------------
PIN_NAMES="IMGDIGEST CFG ENTRYPOINT_DIGEST BASH_DIGEST NODE_DIGEST BUNDLE_DIGEST TBOUND_DIGEST TBP_DIGEST SRC_DIGEST COSIGN_PUB_DIGEST ADMIN_PUB_DIGEST LOAD_TAR_DIGEST COSIGN_TOOL_DIGEST TEST_PROCORACLE TEST_ENTRYPOINT"
load_env() {
  [ -f "$TBOUND_WORK_VIEW/env.sh" ] || die "no $TBOUND_WORK_VIEW/env.sh: run 10-freeze.sh <commit> of the freeze kit first"
  # shellcheck disable=SC1091
  . "$TBOUND_WORK_VIEW/env.sh"                                           # W B H F REPO HF CF (WSL paths)
  local v; for v in W B H F REPO HF CF; do [ -n "${!v:-}" ] || die "env.sh lacks $v"; done
  WX test -d "$HF" && WX test -f "$W/session-env.sh" || die "missing $HF or $W/session-env.sh (run the freeze kit through session-env.sh)"
  eq "FROZEN-COMMIT" "$(hf_get FROZEN-COMMIT)" "$F"
  [ "$F" = "$FREEZE_COMMIT" ] || die "env.sh F ($F) != FREEZE_COMMIT in $V ($FREEZE_COMMIT)"
  [ "$HF" = "$H/$FREEZE_LC" ] || die "env.sh HF ($HF) != \$H/$FREEZE_LC"
  [ "$CF" = "$H/c$FREEZE_LC" ] || die "env.sh CF ($CF) != \$H/c$FREEZE_LC"
  WX test -f "$PREV_HF/FROZEN-COMMIT" || die "no $PREV_HF ($PREV_FREEZE outputs: read-only comparison base)"
  check_prev_values
  local sess; sess=$(WB ". $W/session-env.sh && echo \$IMGDIGEST" | tr -d '\r\n')
  TBP=$W/bin/tbound-profile; COSIGN=$W/bin/cosign; SRC=$W/src
  ADMIN_KEY=$H/admin.key; ADMIN_PUB=$H/admin.pub; COSIGN_PUB=$H/cosign.pub        # paths only
  CENSUS_SRC=$SRC/infra/guest/helpers/xattr-census-r9.py
  local out k val
  out=$(WB "cd $HF && for n in $PIN_NAMES; do echo \$n=\$(cat \$n); done" | tr -d '\r') || die "cannot read pins from $HF"
  while IFS== read -r k val; do
    case " $PIN_NAMES " in *" $k "*) ;; *) die "unexpected pin name '$k'";; esac
    need_dig "$k" "$val"; printf -v "$k" '%s' "$val"
  done <<<"$out"
  eq "IMGDIGEST (session-env == HF)" "$sess" "$IMGDIGEST"
  ok "frozen commit $F; HF=$HF"
}
# Key metadata only (never contents): sizes and modes, and the public-key digests.
check_key_meta() {
  eq "admin.key size/mode" "$(WX stat -c '%s %a' "$ADMIN_KEY")" "64 600"
  eq "admin.pub digest == HF ADMIN_PUB_DIGEST" "$(wd "$ADMIN_PUB")" "$ADMIN_PUB_DIGEST"
  eq "admin.pub digest == F0" "$ADMIN_PUB_DIGEST" "$F0_ADMIN_PUB_DIGEST"
  eq "cosign.pub digest == HF COSIGN_PUB_DIGEST" "$(wd "$COSIGN_PUB")" "$COSIGN_PUB_DIGEST"
  eq "cosign.pub digest == F0" "$COSIGN_PUB_DIGEST" "$F0_COSIGN_PUB_DIGEST"
}

# guest_ip_note: the guest address changes after a host restart (172.24.152.73 at the end of $PREV_FREEZE). Look it up first, do not guess:
#   PowerShell: Get-NetNeighbor -LinkLayerAddress 00-15-5D-0C-40-02   ->  IPAddress
# then export GUEST_IP=<that address>. gssh.sh pins HostKeyAlias=172.18.155.19 with StrictHostKeyChecking=yes, so only the address
# moves: the host key is still checked strictly (a wrong address fails, it does not silently accept another host).
guest_ip_note() {
  echo "guest address: GUEST_IP=$GUEST_IP (changes after host restarts; if a connection fails, look it up: PowerShell 'Get-NetNeighbor -LinkLayerAddress 00-15-5D-0C-40-02', then export GUEST_IP=<IPAddress>)"
}

preflight_guest() {
  say "preflight: guest $GUEST_IP (pinned host key, StrictHostKeyChecking=yes)"
  guest_ip_note
  eq "ssh identity" "$(G 'id -un' | tr -d '\r')" tboundadmin
  G 'sudo -n true' || die "no passwordless sudo: open the session drop-in first (bash 00-session-sudo.sh open); close it again at the end"
  ok "sudo -n works"
  echo "guest: $(G 'hostname; uname -r; date -u +%FT%TZ' | tr -d '\r' | tr '\n' ' ')"
}

svc_inactive() { # service and socket must not be active
  local s; s=$(G 'systemctl is-active tbound-pi.service tbound-pi.socket' || true)
  echo "tbound-pi.service/.socket is-active: $(echo $s)"
  if [[ $'\n'$s$'\n' == *$'\n'active$'\n'* ]]; then die "tbound-pi.service or .socket is active: stop it first (10-upgrade.sh stops service and socket)"; fi
  ok "tbound-pi service and socket inactive"
}

# ro_mounts <path>...: each path is a mount target with ro,nosuid,nodev.
ro_mounts() {
  local list="$*" out line tgt opts bad=0
  out=$(G "for p in $list; do findmnt -n -o TARGET,OPTIONS \"\$p\" || echo \"NOMOUNT \$p\"; done" | tr -d '\r')
  while read -r tgt opts; do
    [ -n "$tgt" ] || continue
    if [ "$tgt" = NOMOUNT ]; then echo "  not a mount point: $opts"; bad=1; continue; fi
    if [[ ",$opts," == *,ro,* && ",$opts," == *,nosuid,* && ",$opts," == *,nodev,* ]]; then echo "  ro ok  $tgt $opts"
    else echo "  NOT ro,nosuid,nodev: $tgt $opts"; bad=1; fi
  done <<<"$out"
  [ "$bad" = 0 ] || die "mount check failed for: $list"
  ok "ro,nosuid,nodev: $list"
}

# Extract the first sha256 digest from tool output after a label; empty if absent.
digest_after() { sed -n "s/.*$1[^:]*:[[:space:]]*\(sha256:[0-9a-f]\{64\}\).*/\1/p" | head -1; }

hf_zero_check() { # sync, then no 0-byte regular file under $HF (*.err stderr captures and the dr run's two genuinely empty audits excepted)
  [ -n "${HF:-}" ] || return 0
  WX sync
  local z; z=$(WX find "$HF" -type f -size 0 ! -name '*.err' ! -path "$HF/dr/command-audit.jsonl" ! -path "$HF/dr/provider-audit.jsonl" | tr -d '\r')
  [ -z "$z" ] || die "0-byte output file(s) under $HF (WSL disk stall?): stop and look, do not continue:"$'\n'"$z"
}
done_step() { hf_zero_check; echo; echo "== DONE ${0##*/} $(date -u +%FT%TZ)"; }

# ==================================================================================================================
# $FREEZE additions
# ==================================================================================================================
OP='sudo -n /opt/tbound/operator/tbound-op'             # the only NOPASSWD command of tboundadmin once the session drop-in is closed
op()  { G "$OP $*"; }                                   # one tbound-op verb, no stdin
OPDIR=/opt/tbound/operator
STAGE_HOME=/home/tboundadmin

prev_get() { WX cat "$PREV_HF/$1" | tr -d '\r\n'; }       # $PREV_FREEZE output value (read-only comparison base)
prev_has() { WX test -e "$PREV_HF/$1"; }

# check_prev_values: the PREV_* constants above must equal the recorded $PREV_FREEZE outputs ($PREV_HF/<NAME>, public digests only). Needs PREV_HF.
check_prev_values() {
  local n v
  for n in $PREV_NAMES; do
    v="PREV_$n"
    eq "$PREV_FREEZE record $n == $v" "$(prev_get "$n")" "${!v}"
  done
  eq "$PREV_FREEZE FROZEN-COMMIT record == PREV_COMMIT" "$(prev_get FROZEN-COMMIT)" "$PREV_COMMIT"
  eq "$PREV_FREEZE g1/RID record == PREV_G1_RID" "$(prev_get g1/RID)" "$PREV_G1_RID"
  eq "$PREV_FREEZE rp/RID record == PREV_RP_RID" "$(prev_get rp/RID)" "$PREV_RP_RID"
  eq "$PREV_FREEZE dr/RID record == PREV_DR_RID" "$(prev_get dr/RID)" "$PREV_DR_RID"
  eq "$PREV_FREEZE dr/RUNID record == PREV_DR_RUNID" "$(prev_get dr/RUNID)" "$PREV_DR_RUNID"
  eq "$PREV_FREEZE dr/CHECKS_OK record == '$PREV_DR_CHECKS_OK'" "$(prev_get dr/CHECKS_OK)" "$PREV_DR_CHECKS_OK"
  eq "$PREV_FREEZE dr/ADMISSION_ID record == PREV_DR_ADMISSION_ID" "$(prev_get dr/ADMISSION_ID)" "$PREV_DR_ADMISSION_ID"
}

# preflight for scripts that use ONLY tbound-op (no session sudo): ssh identity, dispatcher answers, tboundadmin sudo is narrow.
preflight_op() {
  say "preflight: guest $GUEST_IP (pinned host key, StrictHostKeyChecking=yes), tbound-op only"
  guest_ip_note
  eq "ssh identity" "$(G 'id -un' | tr -d '\r')" tboundadmin
  if G 'sudo -n true' 2>/dev/null; then die "sudo -n true works: the session drop-in 91-tbound-$FREEZE_LC-session is open. Close it first (bash 00-session-sudo.sh close); 28, 29, 30, 31, 32 and 40 run with the narrowed sudo"; fi
  ok "sudo -n true fails (narrowed sudo)"
  echo "guest: $(G 'hostname; uname -r; date -u +%FT%TZ' | tr -d '\r' | tr '\n' ' ')"
}

# stage_put <stage-NAME> <WSL source path> <file name>: copy an owner file into ~tboundadmin/stage-NAME/ (0700 dir, 0600 file) and print its sha256.
# The dispatcher reads only these fixed names, from a root-owned copy (O_NOFOLLOW, owner tboundadmin).
stage_put() {
  local dir=$1 src=$2 fn=$3
  WX cat "$src" | GI "umask 077; install -d -m 0700 $STAGE_HOME/$dir && cat > $STAGE_HOME/$dir/$fn.new && mv -f $STAGE_HOME/$dir/$fn.new $STAGE_HOME/$dir/$fn"
  eq "staged $dir/$fn == owner file" "$(G "sha256sum $STAGE_HOME/$dir/$fn" | cut -d' ' -f1 | sed 's/^/sha256:/')" "$(wd "$src")"
}
stage_clear() { G "rm -rf $STAGE_HOME/$1"; }           # remove one staging dir (tboundadmin-owned, no sudo)

# fetch an export file of tbound-op (root:tboundadmin 0640 under /var/tmp/tbound-op) into $HF: fetch_export <guest path> <hf5 name>
fetch_export() {
  G "cat $1" | hf_save "$2"; hf_commit "$2"
}

# ---- session-sudo steps (10, 20, 50): quiesce the service, never leave the socket stopped ---------------------------
socket_up() {
  echo "cleanup: tbound-pi.socket must be active again"
  G 'sudo -n systemctl start tbound-pi.socket; systemctl is-active tbound-pi.socket' || true
}
svc_quiesce() { # stop service AND socket; an exit trap starts the socket again (success or failure). Socket activation starts the service on demand.
  say "stop tbound-pi.service and tbound-pi.socket (the socket is started again when this script ends)"
  cleanup_add socket_up
  G 'sudo -n systemctl stop tbound-pi.service tbound-pi.socket'
  svc_inactive
}

# r1_measure_runtime: read-only service-context measures of the runtime (R1-1 of the runbook). Needs the $FREEZE tbound-profile on the guest.
# Records xattr-measure-runtime.json, XATTR_MOPT, XATTR_MAGIC in HF (idempotent) and compares them with F0/$PREV_FREEZE.
r1_measure_runtime() {
  local out XM MOPT MAGIC
  out=$(svc "$TBPG runtime-bundle-digest --root /var/lib/tbound/runtime") || die "runtime-bundle-digest failed: $out"
  eq "BUNDLE_DIGEST (guest, service context)" "$(printf '%s\n' "$out" | sed -n 's/^Pi runtime bundle tree digest: \(sha256:[0-9a-f]\{64\}\)$/\1/p')" "$BUNDLE_DIGEST"
  XM=$(svc "$TBPG xattr-measure --source /var/lib/tbound/runtime" | tr -d '\r')
  echo "$XM"
  eq "xattr-measure output lines" "$(printf '%s\n' "$XM" | wc -l | tr -d ' ')" 1
  case "$XM" in *'"read_only":true'*'"root_owned":true'*|*'"root_owned":true'*'"read_only":true'*) ok "read_only true, root_owned true";; *) die "xattr-measure: runtime is not read_only and root_owned";; esac
  MOPT=$(printf '%s\n' "$XM" | sed -n 's/.*"mount_options_digest":"\(sha256:[0-9a-f]\{64\}\)".*/\1/p')
  MAGIC=$(printf '%s\n' "$XM" | sed -n 's/.*"filesystem_magic":\([0-9]*\).*/\1/p')
  need_dig MOPT "$MOPT"; eq "filesystem magic (0xef63 ext4)" "$MAGIC" 61267
  if [ "$MOPT" != "$F0_MOPT" ]; then
    [ "${ALLOW_MOPT_CHANGE:-0}" = 1 ] || die "runtime mount_options_digest $MOPT differs from the F0 value $F0_MOPT: the mount options changed; read why, then re-run with ALLOW_MOPT_CHANGE=1"
    note "mount options digest differs from F0, accepted by ALLOW_MOPT_CHANGE=1"
  else ok "mount_options_digest == F0 value $MOPT"; fi
  if [ "$MOPT" = "$(prev_get XATTR_MOPT)" ]; then ok "XATTR_MOPT == $PREV_FREEZE"; else note "XATTR_MOPT differs from $PREV_FREEZE ($(prev_get XATTR_MOPT))"; fi
  hf_put xattr-measure-runtime.json "$XM"
  hf_put XATTR_MOPT "$MOPT"
  hf_put XATTR_MAGIC "$MAGIC"
}
# note_prev NAME NEW OLD: informational comparison with the $PREV_FREEZE record
note_prev() { if [ "$2" = "$3" ]; then ok "$1 same as $PREV_FREEZE ($2)"; else note "$1 changed vs $PREV_FREEZE: $3 -> $2"; fi; }

# req_check <journal file (WSL path)> <request id>: decode that request's state in a collected request journal (reqstate.py, WSL python).
# Prints the report and sets REQ_VERDICT = completed | abandoned | unreconciled | no-terminal | terminal-other | inconsistent | ambiguous | not-journaled.
# req_reconciled is true for the two states that release the one-prior-at-a-time rule: terminal + abandoned record, or completed.
REQ_VERDICT=""
req_check() {
  local out rc=0
  out=$(WX python3 -I "$WGA/reqstate.py" "$1" "$2" 2>&1 | tr -d '\r') || rc=$?
  [ "$rc" = 0 ] || die "reqstate.py failed on $1 (rc $rc): $out"
  printf '%s\n' "$out" | sed 's/^/   journal: /'
  REQ_VERDICT=$(printf '%s\n' "$out" | sed -n 's/^verdict=//p' | head -1)
  [ -n "$REQ_VERDICT" ] || die "reqstate.py printed no verdict for $2: $out"
}
req_reconciled() { case "$REQ_VERDICT" in completed|abandoned) return 0;; *) return 1;; esac; }

# ---- option A (27-archive-state.sh): the $PREV_FREEZE TBound service state was archived and recreated empty ------------------------------------
# archived_prev: true once 27-archive-state.sh archive finished and recorded $HF/g1-prior/archive-done.txt (owner side). 10, 29, 30 and 50 then
# expect the fresh state (no request journal yet, empty catalog) instead of the $PREV_FREEZE G1 and rp workflows, which is archived with its journal (not abandoned).
archived_prev() { hf_has g1-prior/archive-done.txt; }
# fetch_export_nz <guest path> <h$FREEZE_LC name>: like fetch_export, but a 0-byte or missing guest file is NOT fetched (returns 1, nothing written):
# hf_commit refuses 0-byte exports and a fresh catalog is empty by construction.
fetch_export_nz() {
  local sz; sz=$(G "stat -c %s $1 2>/dev/null || echo -1" | tr -d '\r')
  if [ "${sz:-0}" -gt 0 ] 2>/dev/null; then fetch_export "$1" "$2"; return 0; fi
  note "$1 is empty or missing on the guest (size ${sz:-?}): not fetched as $2"; return 1
}

# Journal kinds are plaintext event.kind; request/admission ids are inside base64 event.data.
journal_decoded() {
  WX python3 -I -c 'import base64,json,sys
for line in open(sys.argv[1]):
    if not line.strip(): continue
    record=json.loads(line)
    record["event"]["data"]=json.loads(base64.b64decode(record["event"]["data"],validate=True))
    print(json.dumps(record,separators=(",",":")))' "$1"
}
