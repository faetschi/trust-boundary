#!/bin/bash
set -euo pipefail
case "$-" in *x*) echo "FATAL: refusing to run under xtrace (it would print TBA_PW)" >&2; exit 1;; esac
case ":${SHELLOPTS:-}:" in *:xtrace:*) echo "FATAL: SHELLOPTS has xtrace" >&2; exit 1;; esac
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 34-v-stub-probe
load_env
check_key_meta
preflight_op
HG=$HF/vs
SIGN="$WGA/sign-run.sh"

readonly VS=/etc/tbound/v-stub
readonly VS_LEDGER=/var/lib/tbound/sessions/v-stub-ledger
readonly VS_ADMISSIONS=/var/lib/tbound/sessions/v-stub-admissions
readonly VS_SCRATCH=/var/tmp/tbound-vs
readonly RS_LEDGER=/var/lib/tbound/sessions/usd-ledger
readonly RS_DIR=/etc/tbound/provider
readonly STUB_DIR=$STAGE_HOME/stage-vstub
readonly STUB_PORT=18443
readonly STUB_HOLD=20
readonly STUB_ENDPOINT=http://127.0.0.1:18443/api/v1/chat/completions
readonly LEDGER_ID=tbound-v-stub-probe
readonly FROZEN_AT=2026-11-21T13:35:13Z
readonly FROZEN_PROFILE_SHA=sha256:3d0eaf6411975434daffb265c7911dfc8c11745f512b63959db64d8059a2c357
readonly VWORK=/var/lib/tbound/v-work
readonly OUT=/var/tmp/tbound-op
readonly THROWAWAY_PATHS="$VS $VS_LEDGER $VS_ADMISSIONS $VS_SCRATCH $STUB_DIR"
SESSION_OPEN=0 SWAPPED=0 THROWAWAY=0 STUB_UP=0 VRUN=0 VERIFY=0 TORN_DOWN=0 TD_RC=0 VJOB="" VWORK_PRE="" VWORK_DEL="" DEL_PATHS="" CAT_BEFORE=""
VS_CASE="" VS_KEY="" PILOT_FILES="serve-pi-service.json:serve-pi-service.json managed-live-root.json:managed-live-root.json managed-live-root.ed25519:managed-live-root.ed25519 pi-host-profile.signed.json:pi-host-profile.json pi-host-profile.ed25519:pi-host-profile.ed25519 d09-cell.json:d09-cell.json"

session_close() {
  [ "$SESSION_OPEN" = 1 ] || return 0
  if bash "$GA/00-session-sudo.sh" close; then SESSION_OPEN=0; else
    echo "FATAL: closing the session sudo window FAILED: broad passwordless sudo may still be enabled on the guest. Run: bash $GA/00-session-sudo.sh close (TBA_PW set), then check that 'sudo -n true' fails." >&2
    return 1
  fi
}

pilot_status() { G "$OP status pilot" | tr -d '\r'; }
catalog_sha() {
  local h
  op "collect catalog" > /dev/null || die "tbound-op collect catalog failed"
  h=$(G "sha256sum $OUT/_/catalog.jsonl" | cut -d' ' -f1 | tr -d '\r')
  [[ $h =~ ^[0-9a-f]{64}$ ]] || die "no sha256 of the collected catalog: '$h'"
  printf '%s' "$h"
}
pilot_installed() {
  local s pair
  s=$(pilot_status)
  [[ $s == *"installed config matches state of: pilot"* && $s == *"service=active socket=active"* ]] || die "the pilot chain is not installed and running (tbound-op status pilot): $s"
  for pair in $PILOT_FILES; do
    eq "installed /etc/tbound/${pair##*:} == pilot/${pair%%:*}" "$(gsha "/etc/tbound/${pair##*:}")" "$(wd "$HF/pilot/${pair%%:*}")"
  done
}
trial() { GSSH_IDENTITY=$TBOUND_TRIAL_KEY GSSH_USER=root sh "$GSSH_SH" "/usr/local/bin/tbound trial $1" </dev/null; }
first_compliant_case() {
  local c
  c=$(WX python3 -I -c 'import json,sys
print(next(c["case_id"] for c in json.load(open(sys.argv[1],encoding="utf-8"))["cases"] if c["kind"]=="compliant"))' "$1" | tr -d '\r\n')
  [[ $c =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || die "not a usable case id: '$c'"
  printf '%s' "$c"
}
gput() {
  local mode=$1 owner=$2 src=$3 dst=$4
  WX cat "$src" | GI "sudo -n install -o ${owner%%:*} -g ${owner##*:} -m $mode /dev/stdin $dst"
  eq "guest $dst == ${src##*/}" "$(gsha "$dst")" "$(wd "$src")"
}
save() { hf_save "$1"; hf_commit "$1"; }

research_state() {
  GI "bash -s -- $RS_LEDGER $RS_DIR" <<'EOS' | tr -d '\r'
set -euo pipefail
sudo -n true
L=$1; D=$2
if sudo -n test -e "$L"; then
  n=$(sudo -n wc -l "$L" | cut -d' ' -f1)
  h=$(sudo -n tail -n 1 "$L" | head -c -1 | sha256sum | cut -c1-64)
  s=$(sudo -n sha256sum "$L" | cut -c1-64)
  [ -n "$n" ] && [ -n "$h" ] && [ -n "$s" ] || { echo "the research ledger could not be measured" >&2; exit 1; }
  echo "ledger records=$n head=$h sha256=$s"
else
  echo "ledger absent"
fi
for f in openrouter.key research-profile.json research-ledger.json research-ledger-checkpoint.json; do
  if sudo -n test -e "$D/$f"; then sudo -n stat -c '%n %s %Y %U:%G %a' "$D/$f"; else echo "$D/$f absent"; fi
done
EOS
}

stub_start() {
  [ -z "$(G "ss -ltnH 'sport = :$STUB_PORT'" | tr -d '\r')" ] || die "port $STUB_PORT on the guest is in use: the stub upstream cannot start"
  STUB_UP=1
  WX cat "$SRC/infra/guest/v-stub/stub-upstream.py" | GI "umask 077; install -d -m 0700 $STUB_DIR && cat > $STUB_DIR/stub.py"
  eq "staged stub.py == the frozen source" "$(G "sha256sum $STUB_DIR/stub.py" | cut -d' ' -f1 | sed 's/^/sha256:/')" "$(wd "$SRC/infra/guest/v-stub/stub-upstream.py")"
  G "cd $STUB_DIR && : > stub.log && { setsid nohup python3 -I stub.py stub.log stub.pid $STUB_HOLD > stub.out 2>&1 < /dev/null & }"
  local i
  for i in $(seq 1 20); do
    if G "python3 -I -c 'import socket; socket.create_connection((\"127.0.0.1\", $STUB_PORT), 2)'" >/dev/null 2>&1; then break; fi
    sleep 1
  done
  G "python3 -I -c 'import socket; socket.create_connection((\"127.0.0.1\", $STUB_PORT), 2)'" || die "the stub upstream does not answer on 127.0.0.1:$STUB_PORT ($(G "cat $STUB_DIR/stub.out" | head -c 400))"
  ok "stub upstream listens on 127.0.0.1:$STUB_PORT (pid $(G "cat $STUB_DIR/stub.pid" | tr -d '\r\n'), hold $STUB_HOLD s)"
}

v_leftovers() {
  local ps dirs
  ps=$(podsvc "ps -a --format '{{.Names}}'" | tr -d '\r') || { echo "podman ps as the service failed: cannot tell whether a tbound-v- container is left" >&2; return 1; }
  dirs=$(G "sudo -n sh -c 'if test -d $VWORK; then ls -A $VWORK; fi'" | tr -d '\r') || { echo "cannot list $VWORK" >&2; return 1; }
  printf '%s\n%s\n' "$(grep '^tbound-v-' <<<"$ps" || true)" "$dirs" | grep . || true
}
td_job() { if [ -n "$VJOB" ] && kill -0 "$VJOB" 2>/dev/null; then kill "$VJOB" || true; fi; }
td_vrun() {
  local names n left
  GI 'sudo -n bash -s' <<'EOS' || die "a trial v-run process is still running on the guest after SIGKILL"
set -u
P='[t]bound trial ssh-original-command'
pkill -TERM -u 0 -f "$P" || true
for i in $(seq 1 30); do pgrep -u 0 -f "$P" > /dev/null || exit 0; sleep 1; done
pkill -KILL -u 0 -f "$P" || true
sleep 2
! pgrep -u 0 -f "$P" > /dev/null
EOS
  names=$(podsvc "ps -a --format '{{.Names}}'" | tr -d '\r') || die "podman ps as the service failed: a tbound-v- container may be left"
  for n in $(grep '^tbound-v-' <<<"$names" || true); do
    [[ $n =~ ^tbound-v-[0-9a-f]+$ ]] || die "unexpected container name '$n'"
    podsvc "rm -f --time 0 --ignore $n" > /dev/null || die "podman rm -f of $n failed"
  done
  G "sudo -n sh -c 'if test -d $VWORK; then find $VWORK -mindepth 1 -maxdepth 1 -exec rm -rf {} +; fi'" || die "cannot clear $VWORK"
  left=$(v_leftovers) || die "cannot look for a V container or work directory"
  [ -z "$left" ] || die "a V container or work directory is still on the guest: $left"
  ok "no trial v-run process, no tbound-v- container, no work directory under $VWORK"
}
td_stub() {
  if ! hf_has vs/stub.log && G "test -s $STUB_DIR/stub.log"; then G "cat $STUB_DIR/stub.log" | tr -d '\r' | save vs/stub.log; fi
  G "if test -f $STUB_DIR/stub.pid; then kill \$(cat $STUB_DIR/stub.pid) 2>/dev/null || true; fi; sleep 1; rm -f $STUB_DIR/stub.py $STUB_DIR/stub.pid $STUB_DIR/stub.log $STUB_DIR/stub.out; rmdir $STUB_DIR 2>/dev/null || true"
  [ -z "$(G "ss -ltnH 'sport = :$STUB_PORT'" | tr -d '\r')" ] || die "port $STUB_PORT is still in use after the stub was stopped"
  [ "$(G "test -e $STUB_DIR && echo present || echo absent" | tr -d '\r')" = absent ] || die "$STUB_DIR is still on the guest"
  ok "stub upstream stopped, $STUB_DIR removed, port $STUB_PORT free"
}
td_stop_service() { op "svc stop"; }
td_delete() {
  local listing p left
  listing=$(G "for p in $DEL_PATHS; do sudo -n find \$p -printf '%p %y %s %U:%G %m\n' 2>/dev/null || true; done" | tr -d '\r')
  printf 'to delete:\n%s\n' "$listing"
  hf_has vs/deleted-listing-before.txt || printf '%s\n' "${listing:-nothing present}" | save vs/deleted-listing-before.txt
  G "sudo -n rm -f $VS/research-profile.json $VS/research-ledger.json $VS/research-ledger-checkpoint.json $VS/stub.key $VS_LEDGER; for d in $VS_ADMISSIONS $VS_SCRATCH $VWORK_DEL; do if sudo -n test -d \$d; then sudo -n find \$d -mindepth 1 -maxdepth 1 -type f -delete; sudo -n rmdir \$d; fi; done; if sudo -n test -d $VS; then sudo -n rmdir $VS; fi"
  for p in $DEL_PATHS; do
    [ "$(G "sudo -n test -e $p && echo present || echo absent" | tr -d '\r')" = absent ] || die "$p is still on the guest after the deletion"
    ok "deleted: $p"
  done
  left=$(G "sudo -n ls -A /var/lib/tbound/sessions | grep '^v-stub' || true" | tr -d '\r')
  [ -z "$left" ] || die "files named v-stub* are left in /var/lib/tbound/sessions: $left"
  hf_has vs/DELETED || printf 'deleted %s\n' $DEL_PATHS | save vs/DELETED
}
td_restore() {
  local pair
  for pair in $PILOT_FILES; do gput 0644 root:root "$HF/pilot/${pair%%:*}" "/etc/tbound/${pair##*:}"; done
  op "svc start" || die "tbound-op svc start failed"
  local s; s=$(pilot_status)
  [[ $s == *"installed config matches state of: pilot"* ]] || die "after the restore the installed chain does not match the pilot state (tbound-op status pilot)"
  [[ $s == *"service=active socket=active"* ]] || die "the pilot service is not active after the restore"
  ok "pilot chain restored from $HF/pilot and started: installed config matches state of pilot, service and socket active"
}
td_verify() {
  local l2 csha chk
  l2=$(op "diag l2" 2>&1 | tr -d '\r') || die "diag l2 failed: $l2"
  printf '%s\n' "$l2" | head -40
  l2_check "$l2" || die "diag l2 does not show the expected L2 state after the restore (see above; PROVISIONING-SIGNING.md L2)"
  eq "live cgroup-root digest (diag l2) == recorded CGROUP_ROOT_DIGEST" "$L2_ROOT_DIGEST" "$(hf_get CGROUP_ROOT_DIGEST)"
  hf_has vs/diag-l2-restored.txt || printf '%s\n' "$l2" | save vs/diag-l2-restored.txt
  csha=$(catalog_sha) || die "cannot hash the workflow catalog after the restore"
  eq "workflow catalog sha256 after the probe == before" "$csha" "$CAT_BEFORE"
  chk=$(trial "reset $VS_CASE --check" | tr -d '\r') || die "trial reset $VS_CASE --check refused after the restore: $chk"
  [[ $chk == *tbound-trial-reset/v1* && $chk == *'"check":true'* ]] || die "trial reset --check did not print a check document after the restore: $chk"
  ok "pilot chain verified after the restore: diag l2 as the pilot cycle left it, catalog unchanged, trial reset --check answers"
}
tstep() {
  if ( "$@" ); then return 0; fi
  echo "teardown step failed: $*" >&2
  TD_RC=1
  return 1
}
vs_teardown() {
  [ "$TORN_DOWN" = 0 ] || return 0
  TD_RC=0
  say "teardown: stub, v-stub service, throwaway files, pilot chain"
  tstep td_job
  if [ "$VRUN" = 1 ]; then tstep td_vrun && VRUN=0; fi
  if [ "$STUB_UP" = 1 ]; then tstep td_stub && STUB_UP=0; fi
  if [ "$SWAPPED" = 1 ]; then tstep td_stop_service; fi
  if [ "$THROWAWAY" = 1 ]; then tstep td_delete && THROWAWAY=0; fi
  if [ "$SWAPPED" = 1 ]; then tstep td_restore && SWAPPED=0 && VERIFY=1; fi
  if [ "$VERIFY" = 1 ]; then tstep td_verify && VERIFY=0; fi
  [ "$TD_RC" != 0 ] || TORN_DOWN=1
  return "$TD_RC"
}

say "0. preconditions"
WX test ! -e "$HG" || die "$HG exists from an earlier run of this probe: move it aside first (in WSL: mv -T -n $HG $HF/vs.aside-\$(date -u +%Y%m%dT%H%M%SZ)); nothing of it is resumed"
hf_has pilot/CYCLE_OK && hf_has pilot/manifest.json && hf_has pilot/CORPUS_DIGEST || die "the pilot cycle is not complete ($HF/pilot/CYCLE_OK, manifest.json, CORPUS_DIGEST): run 33-corpus-pilot.sh first"
[ -n "${TBOUND_TRIAL_KEY:-}" ] && [ -f "$TBOUND_TRIAL_KEY" ] || die "TBOUND_TRIAL_KEY must be the path of the trial forced-command key (trial v-run runs through it)"
[ -n "${TBA_PW:-}" ] || die "TBA_PW is not set: the hand install needs the session sudo window (read -rs TBA_PW; export TBA_PW)"
S=$(pilot_status); printf '%s\n' "$S"
[[ $S == *"installed config matches state of: pilot"* ]] || die "the installed chain is not the pilot's: tbound-op status pilot must say 'installed config matches state of: pilot'"
VS_CASE=$(first_compliant_case "$HF/pilot/manifest.json")
[[ $VS_CASE =~ ^PILOT-CMP-[A-Za-z0-9._-]+$ ]] || die "the first compliant case of the installed manifest is '$VS_CASE', not a PILOT-CMP development case: a V unit never runs on an H1 task before T2"
CORPUS_DIGEST=$(hf_get pilot/CORPUS_DIGEST); need_dig CORPUS_DIGEST "$CORPUS_DIGEST"
CAL_RW_DIGEST=$(hf_get CAL_RW_DIGEST); need_dig CAL_RW_DIGEST "$CAL_RW_DIGEST"
need_dig CGROUP_ROOT_DIGEST "$(hf_get CGROUP_ROOT_DIGEST)"
CD=$(G 'sha256sum /etc/tbound/podman-config/containers.conf' | cut -d' ' -f1 | sed 's/^/sha256:/')
SD=$(G 'sha256sum /etc/tbound/podman-config/storage.conf' | cut -d' ' -f1 | sed 's/^/sha256:/')
eq "measured CD == recorded CONTAINERS_CONF_DIGEST" "$CD" "$(hf_get CONTAINERS_CONF_DIGEST)"
eq "measured SD == recorded STORAGE_CONF_DIGEST" "$SD" "$(hf_get STORAGE_CONF_DIGEST)"
ok "V unit case: $VS_CASE (development compliant case of the installed pilot manifest)"
WX mkdir -m 0700 "$HG"
local_tmp
cleanup_add session_close
cleanup_add vs_teardown
SESSION_OPEN=1
bash "$GA/00-session-sudo.sh" open

say "1. the research ledger before the probe (read-only)"
for p in $THROWAWAY_PATHS; do
  [ "$(G "sudo -n test -e $p && echo present || echo absent" | tr -d '\r')" = absent ] || die "$p is already on the guest: an earlier probe left it, the owner looks"
done
VWORK_PRE=$(G "sudo -n sh -c 'if test -e $VWORK; then echo present; else echo absent; fi'" | tr -d '\r') || die "cannot look at $VWORK"
DEL_PATHS="$VS $VS_LEDGER $VS_ADMISSIONS $VS_SCRATCH"
case $VWORK_PRE in
  absent) VWORK_DEL=$VWORK; DEL_PATHS="$DEL_PATHS $VWORK";;
  present) left=$(G "sudo -n ls -A $VWORK" | tr -d '\r') || die "cannot list $VWORK"
    [ -z "$left" ] || die "$VWORK holds work directories from an earlier V run: the owner looks";;
  *) die "unexpected state of $VWORK: '$VWORK_PRE'";;
esac
RS_BEFORE=$(research_state); printf '%s\n' "$RS_BEFORE"
printf '%s\n' "$RS_BEFORE" | save vs/research-before.txt

say "2. inputs on the owner side"
WX sed "s|\"frozen_at\": null|\"frozen_at\": \"$FROZEN_AT\"|" "$SRC/infra/guest/research/research-profile.json" | save vs/research-profile.json
eq "stub profile == the frozen research profile of F32 (RA-2)" "$(wd "$HG/research-profile.json")" "$FROZEN_PROFILE_SHA"
WX cat "$SRC/infra/guest/v-stub/research-ledger.json" | save vs/research-ledger.json
eq "ledger_id of the throwaway ledger" "$(jgetw "$HG/research-ledger.json" ledger_id)" "$LEDGER_ID"
VS_KEY=$(WX python3 -I -c 'import secrets; print("sk-vstub-" + secrets.token_hex(24))' | tr -d '\r\n')
[ "${#VS_KEY}" -ge 40 ] || die "the throwaway stub key was not generated"
note "a random throwaway stub key was generated for this run (never printed, never kept); it is not a research key"
WX python3 -I "$WGA/mk-svc-config.py" "$SRC/infra/guest/serve-pi-service.v-stub.json" "$HG/serve-pi-service.json" "$CD" "$SD" "$CORPUS_DIGEST"
eq "provider_route" "$(jgetw "$HG/serve-pi-service.json" provider_route)" v-stub
eq "workflow_admission_mode" "$(jgetw "$HG/serve-pi-service.json" workflow_admission_mode)" corpus
eq "corpus_manifest_digest == the pilot's CORPUS_DIGEST" "$(jgetw "$HG/serve-pi-service.json" corpus_manifest_digest)" "$CORPUS_DIGEST"
eq "corpus_quota_profile" "$(jgetw "$HG/serve-pi-service.json" corpus_quota_profile)" pilot
eq "config differs from the pilot config in provider_route only" \
  "$(WB "diff '$HF/pilot/serve-pi-service.json' '$HG/serve-pi-service.json' | grep '^[<>]' || true" | tr -d '\r')" \
  "$(printf '%s\n%s' '<   "provider_route": "engineering",' '>   "provider_route": "v-stub",')"

say "3. stop the pilot service, install the v-stub config and measure the registration (RA-5, RA-6)"
pilot_installed
CAT_BEFORE=$(catalog_sha)
printf '%s\n' "$CAT_BEFORE" | save vs/catalog-before.sha256
THROWAWAY=1
SWAPPED=1
op "svc stop"
gput 0644 root:root "$HG/serve-pi-service.json" /etc/tbound/serve-pi-service.json
GI 'sudo -n bash -s' <<'EOS'
set -euo pipefail
umask 077
C=/sys/fs/cgroup/system.slice/tbound-pi.service
S=/var/tmp/tbound-vs
test ! -e "$C"
install -d -o root -g root -m 0700 "$S"
mkdir "$C"
trap 'rmdir "$C" 2>/dev/null || true' EXIT
echo "+cpu +memory +pids" > "$C/cgroup.subtree_control"
chown 995:986 "$C" "$C/cgroup.procs" "$C/cgroup.threads" "$C/cgroup.subtree_control"
[ "$(cat "$C/cgroup.type")" = domain ] && [ "$(cat "$C/cgroup.subtree_control")" = "cpu memory pids" ] && [ "$(wc -c < "$C/cgroup.procs")" = 0 ]
/usr/local/bin/tbound-profile managed-root-measure --root /var/lib/tbound/live/root --service-name tbound-pi --service-group tbound-pi \
  --service-unit /etc/systemd/system/tbound-pi.service --mount-unit /etc/systemd/system/var-lib-tbound-live.mount \
  --runner /usr/local/bin/tbound --service-cgroup /system.slice/tbound-pi.service --allowed-client-uids 994 --allowed-client-primary-gid 985 \
  --out "$S/managed-live-root.unsigned.json" > /dev/null
rmdir "$C"
trap - EXIT
EOS
G "sudo -n cat $VS_SCRATCH/managed-live-root.unsigned.json" | save vs/managed-live-root.unsigned.json
U=$HG/managed-live-root.unsigned.json
eq "runner_digest == TBOUND_DIGEST ($FREEZE)" "$(jgetw "$U" runner_digest)" "$TBOUND_DIGEST"
eq "managed_root_xattr_calibration_digest == CAL_RW_DIGEST" "$(jgetw "$U" managed_root_xattr_calibration_digest)" "$CAL_RW_DIGEST"
eq "service_uid/gid" "$(jgetw "$U" service_uid)/$(jgetw "$U" service_gid)" 995/986
eq "service_config_digest == the v-stub config" "$(jgetw "$U" service_config_digest)" "$(wd "$HG/serve-pi-service.json")"
for pair in passwd_digest:/etc/passwd group_digest:/etc/group nsswitch_digest:/etc/nsswitch.conf; do
  eq "${pair%%:*} == the guest file" "$(jgetw "$U" "${pair%%:*}")" "$(gsha "${pair##*:}")"
done

say "4. [OWNER SIGN] S3 and S4 for NAME=vs (sign-run.sh), install registration, profile and template (RA-7 to RA-10)"
WX bash "$SIGN" vs s3 "$U"
REGDIGEST=$(hf_get vs/REGDIGEST); need_dig REGDIGEST-vs "$REGDIGEST"
gput 0644 root:root "$HG/managed-live-root.json" /etc/tbound/managed-live-root.json
gput 0644 root:root "$HG/managed-live-root.ed25519" /etc/tbound/managed-live-root.ed25519
rv=$(G "$TBPG managed-root-verify --registration /etc/tbound/managed-live-root.json --sig /etc/tbound/managed-live-root.ed25519 --pub /etc/tbound/pi-host-profile.pub --runner-digest $TBOUND_DIGEST 2>&1" | tr -d '\r') || die "managed-root-verify refused the installed v-stub registration: $rv"
[[ $rv == *"$REGDIGEST"* ]] || die "managed-root-verify did not name REGDIGEST-vs $REGDIGEST: $rv"
WX bash "$SIGN" vs s4
TD=$(hf_get vs/TD); PROFILE_DIGEST=$(hf_get vs/PROFILE_DIGEST); need_dig TD "$TD"; need_dig PROFILE_DIGEST "$PROFILE_DIGEST"
eq "template registration digest == REGDIGEST-vs" "$(jgetw "$HG/d09-cell.json" managed_live_root_registration_digest)" "$REGDIGEST"
eq "template expected_image_digest == IMGDIGEST" "$(jgetw "$HG/d09-cell.json" expected_image_digest)" "$IMGDIGEST"
gput 0644 root:root "$HG/pi-host-profile.signed.json" /etc/tbound/pi-host-profile.json
gput 0644 root:root "$HG/pi-host-profile.ed25519" /etc/tbound/pi-host-profile.ed25519
gput 0644 root:root "$HG/d09-cell.json" /etc/tbound/d09-cell.json
pv=$(G "$TBPG verify --profile /etc/tbound/pi-host-profile.json --sig /etc/tbound/pi-host-profile.ed25519 --pub /etc/tbound/pi-host-profile.pub --runtime-bundle-digest $BUNDLE_DIGEST 2>&1" | tr -d '\r') || die "profile verify refused the installed v-stub profile: $pv"
[[ $pv == *"$PROFILE_DIGEST"* ]] || die "profile verify did not name PROFILE_DIGEST $PROFILE_DIGEST: $pv"
tv=$(G "$TBPG template-digest --template /etc/tbound/d09-cell.json 2>&1" | tr -d '\r') || die "template-digest failed on the installed template: $tv"
[[ $tv == *"canonical digest: $TD"* ]] || die "the installed template digest is not TD $TD: $tv"

say "5. throwaway research files and ledger at their own paths, no research key (RA-2, RA-4 with the v-stub paths)"
G "sudo -n install -d -o root -g root -m 0755 $VS"
gput 0644 root:root "$HG/research-profile.json" $VS/research-profile.json
gput 0644 root:root "$HG/research-ledger.json" $VS/research-ledger.json
printf '%s' "$VS_KEY" | GI "sudo -n install -o 995 -g 986 -m 0400 /dev/stdin $VS/stub.key"
eq "guest stub.key == the generated throwaway key" "$(gsha $VS/stub.key)" "sha256:$(printf '%s' "$VS_KEY" | sha256sum | cut -c1-64)"
G "cd / && $SVC /usr/local/bin/tbound-profile research-ledger-genesis --profile $VS/research-profile.json --ledger-config $VS/research-ledger.json --ledger $VS_LEDGER" | tr -d '\r' | save vs/genesis.json
eq "genesis ledger_id" "$(jgetw "$HG/genesis.json" ledger_id)" "$LEDGER_ID"
eq "genesis ledger_path" "$(jgetw "$HG/genesis.json" ledger_path)" "$VS_LEDGER"
WX python3 -I -c 'import json,sys; g=json.load(open(sys.argv[1])); print(json.dumps({"records": g["records"], "head_sha256": g["head_sha256"]}))' "$HG/genesis.json" | tr -d '\r' | save vs/checkpoint-1.json
gput 0644 root:root "$HG/checkpoint-1.json" $VS/research-ledger-checkpoint.json
GENESIS_HEAD=$(jgetw "$HG/genesis.json" head_sha256)

say "6. stub upstream and the v-stub service"
stub_start
T0=$(G "date -u '+%Y-%m-%d %H:%M:%S'" | tr -d '\r')
G "sudo -n systemctl start tbound-pi.socket && sudo -n systemctl start tbound-pi.service" || true
sleep 3
act=$(G 'systemctl is-active tbound-pi.socket tbound-pi.service' | tr -d '\r' | tr '\n' ' ') || true
G "sudo -n journalctl -u tbound-pi.service --since '$T0 UTC' --no-pager" | tr -d '\r' | save vs/svc-start-journal.txt
[ "$act" = "active active " ] || die "the v-stub service did not start (is-active: $act); journal in $HG/svc-start-journal.txt"
[ "$(WB "grep -c 'open the research provider route\|exceeds the research profile\|Failed with result' '$HG/svc-start-journal.txt' || true" | tr -d '\r\n')" = 0 ] || die "the journal shows a refusal: $HG/svc-start-journal.txt"
ok "the v-stub service and socket are active, no refusal in the journal"

say "7. one V unit on $VS_CASE through the forwarder, the stub holds its reply for $STUB_HOLD s"
VRUN=1
( rc=0; trial "v-run $VS_CASE" > "$LOCAL_TMP/vdoc.json" 2> "$LOCAL_TMP/verr.txt" || rc=$?; echo "$rc" > "$LOCAL_TMP/vrc" ) &
VJOB=$!
SEEN=0
for i in $(seq 1 75); do
  if [ "$(G "wc -l < $STUB_DIR/stub.log" | tr -d '\r ')" -ge 1 ] 2>/dev/null; then SEEN=1; break; fi
  kill -0 "$VJOB" 2>/dev/null || break
  sleep 2
done
[ "$SEEN" = 1 ] || die "no request reached the stub (v-run: $(head -c 600 "$LOCAL_TMP/verr.txt" 2>/dev/null))"
NAMES=$(podsvc "ps -a --format '{{.Names}}'" | tr -d '\r' | grep '^tbound-v-' || true)
[ "$(printf '%s\n' "$NAMES" | grep -c .)" = 1 ] || die "expected exactly one tbound-v- container while the stub holds the reply, found: [$NAMES]"
note "live V container: $NAMES"
podsvc "inspect $NAMES" | tr -d '\r' | save vs/inspect.json
printf '%s' "$VS_KEY" | WBI "python3 -I '$WGA/vs-check.py' inspect '$HG/inspect.json' -"
for i in $(seq 1 240); do kill -0 "$VJOB" 2>/dev/null || break; sleep 10; done
if kill -0 "$VJOB" 2>/dev/null; then die "trial v-run did not return in 40 minutes"; fi
wait "$VJOB" || true
VJOB=""
VRC=$(cat "$LOCAL_TMP/vrc")
[ "$VRC" = 0 ] || die "trial v-run exited $VRC: $(head -c 600 "$LOCAL_TMP/verr.txt")"
LEFT=$(v_leftovers) || die "cannot look for a V container or work directory after the unit"
[ -z "$LEFT" ] || die "after the V unit a container or work directory is left on the guest: $LEFT"
VRUN=0
tr -d '\r' < "$LOCAL_TMP/vdoc.json" | save vs/v-doc.json
DOCOUT=$(WX python3 -I "$WGA/vs-check.py" doc "$HG/v-doc.json" "$VS_CASE" | tr -d '\r')
printf '%s\n' "$DOCOUT" | head -n -1
NREC=$(printf '%s\n' "$DOCOUT" | tail -n 1)

say "8. what the stub saw, the throwaway ledger, the research ledger"
G "cat $STUB_DIR/stub.log" | tr -d '\r' | save vs/stub.log
printf '%s' "$VS_KEY" | WBI "python3 -I '$WGA/vs-check.py' stublog '$HG/stub.log' - '$NREC' '$HG/research-profile.json'"
G "sudo -n cat $VS_LEDGER" | save vs/v-stub-ledger.after
WX python3 -I "$WGA/vs-check.py" ledger "$HG/v-stub-ledger.after" "$GENESIS_HEAD"
RS_AFTER=$(research_state); printf '%s\n' "$RS_AFTER" | save vs/research-after.txt
eq "the research ledger, its checkpoint, profile, configuration and key file are unchanged" "$RS_AFTER" "$RS_BEFORE"
note "the research ledger: $(printf '%s\n' "$RS_AFTER" | head -1)"

say "9. teardown: stop the stub and the service, delete the throwaway files, restore the pilot chain"
vs_teardown || die "teardown did not finish (see above): the guest may still hold throwaway files or the v-stub chain; the owner looks"
SWAPPED=0 THROWAWAY=0
RS_FINAL=$(research_state)
eq "the research ledger is still unchanged after the teardown" "$RS_FINAL" "$RS_BEFORE"
session_close
WX python3 -I "$WGA/vs-check.py" record commit "$F" freeze "$FREEZE" case "$VS_CASE" stub_endpoint "$STUB_ENDPOINT" throwaway_ledger "$VS_LEDGER" throwaway_dir "$VS" \
  ledger_id "$LEDGER_ID" genesis_head "$GENESIS_HEAD" unit_ledger_records "$NREC" profile_sha256 "$FROZEN_PROFILE_SHA" regdigest "$REGDIGEST" td "$TD" profile_digest "$PROFILE_DIGEST" \
  research_before "$RS_BEFORE" research_after "$RS_FINAL" research_unchanged true deleted "$(WX cat "$HG/DELETED" | tr -d '\r' | tr '\n' ';')" \
  pilot_restored true | save vs/record.json
hf_put vs/CHECKS_OK ok
WX cat "$HG/record.json"
note "the probe is complete: nothing was booked in the research ledger, the throwaway files are deleted, the pilot chain is running again. Next: 40-guest-evidence.sh v-stub"
done_step
