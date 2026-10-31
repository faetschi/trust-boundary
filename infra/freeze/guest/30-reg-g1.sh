#!/bin/bash
# 30 G1 RG cycle at $FREEZE, entirely through tbound-op verbs (the narrowed sudo; the session drop-in must be CLOSED: bash 00-session-sudo.sh close).
#   RG-2  [PRIV via tbound-op] svc stop; the g1 service config from infra/guest/serve-pi-service.g1.json with the two MEASURED podman-config digests
#         (sha256 of the installed files); stage; cfg-install g1
#   RG-3  reg-measure g1; fetch the unsigned registration; checks before signing
#   RG-4  [OWNER SIGN] S3g -> $HF/g1/ (sign-g1.sh s3: admin key by path, verify --runner-digest TBOUND_DIGEST, REGDIGEST-g1)
#   RG-5  stage; reg-install g1
#   RG-6  [OWNER SIGN] g1 cell template with REGDIGEST-g1 (from the base template), TD, BASE_TD == pin, S4g -> $HF/g1/ (sign-g1.sh s4)
#   RG-7  stage; prof-install g1; seed-live g1; svc start (the $FREEZE service starts; with option A there is no earlier workflow, otherwise the $PREV_FREEZE rp
#         workflow, which 28 would have abandoned); diag l2
#   L     client-run g1 (detached, one use); poll `status g1`; collect l4, catalog, requests; fetch response/status/journal into $HF/g1/
#   check zero NEW pi_workflow_terminal on fresh option-A state (31 settles G1); other routes retain ALLOW_NO_NEW_TERMINAL; the g1 request is in
#         serve-pi-requests.jsonl, the client exit is 0.
# Resumable: the position is read from `tbound-op status g1` (state none/config/reg/prof/started, client marker); signed files already in $HF/g1 are
# verified, never re-signed. STOP_AFTER=<cfg|measure|s3|reg|s4|prof|start|run> ends the script after that phase (one approval point per owner action).
# The service is left RUNNING at the end (as `svc start` leaves it); `svc stop` is the next owner decision.
# $FREEZE is a RECOVERY/re-freeze cycle (PROVISIONING-SIGNING.md G1-5): the $PREV_FREEZE guest results are NOT claim-bearing (the dr probe control path is
#   fixed for $FREEZE), so $FREEZE reruns G1, the route probe and the direct-runtime probe on a fresh state. $PREV_FREEZE G1 completed and published,
#   its rp completed (route probe, not claim-bearing), and its dr request was refused prior-workflow-unsettled (no run).
#   27-archive-state.sh archives the $PREV_FREEZE service state and all three cycles; 29 confirms nothing is left before moving anything.
#   The $FREEZE commit must carry a NEW admission id, read from the frozen repo.
#   step 1 (a new admission id in infra/guest/serve-pi-service.g1.json) is in the $FREEZE commit (10-upgrade.sh checks it, RG-2 below checks it again);
#   step 2 (owner moves /root/ga/reg-g1, /root/ga/state/g1, /root/ga/state/g1.once aside, password sudo) is 29-g1-recover.sh, run BEFORE this script;
#   step 3 (new pins.env with the new TPL_G1) is 10-upgrade.sh; then this script runs the full RG cycle (two new signatures into $HF/g1, a NEW dir).
# OPTION A (27-archive-state.sh archive, recorded in $HF/g1-prior/archive-done.txt): the $PREV_FREEZE service state was archived and recreated empty, so there is no
#   earlier workflow to settle: the catalog and the request journal start empty (no baseline export, 0 terminal lines before; 0-byte exports are not
#   fetched); G1 adds zero terminals, then the next rp request verifies and settles G1. 27 moved the CONTENT of the live root into its archive and kept the root
#   directory itself (empty), so seed-live g1 finds an empty root and re-creates the canonical fixture; it stops with "unexpected extra entries: owner must
#   look" if the root holds anything else.
# Needs: 10, 20, 50 done; session sudo CLOSED; 29-g1-recover.sh move done. Usage: bash 30-reg-g1.sh
set -euo pipefail
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 30-reg-g1
load_env
check_key_meta
preflight_op

HG=$HF/g1
OUT=/var/tmp/tbound-op
SIGN="$WGA/sign-g1.sh"
phase_end() { if [ "${STOP_AFTER:-}" = "$1" ]; then say "STOP_AFTER=$1 reached"; done_step; exit 0; fi; }
st_rank_of() { case "$1" in config) echo 1;; reg) echo 2;; prof) echo 3;; started) echo 4;; *) echo 0;; esac; }
g1_status() { G "$OP status g1" 2>&1 | tr -d '\r'; }
g1_state() { g1_status | sed -n 's/^ *state=\(.*\)$/\1/p' | head -1; }
tcount() { WB "if [ -f '$1' ]; then grep -c pi_workflow_terminal '$1' || true; else echo 0; fi" | tr -d '\r\n'; }   # a missing export counts 0 (option A: nothing was exported from the empty fresh state)
ARCHMODE=0; if archived_prev; then ARCHMODE=1; fi    # option A: 27-archive-state.sh archived the $PREV_FREEZE state; the journal and catalog start empty

say "0. dispatcher and pins"
S=$(g1_status); printf '%s\n' "$S"
[[ $S == *"pins F=$F"* ]] || die "tbound-op pins are not $FREEZE ($F): run 10-upgrade.sh"
[[ $S == *"anchor: ok"* ]] || die "anchor not ok"
[[ $S == *"runner: ${TBOUND_DIGEST:0:23} tbound-profile: ${TBP_DIGEST:0:23}"* ]] || die "runner/tbound-profile on the guest are not the $FREEZE pins"
STATE=$(g1_state); RANK=$(st_rank_of "${STATE:-none}")
echo "g1 state: ${STATE:-none} (rank $RANK)"
CLIENT=$(printf '%s\n' "$S" | sed -n 's/.*client=\(g1-req-[0-9TZ]*\).*/\1/p' | head -1)
[ -z "$CLIENT" ] || echo "g1 client-run already launched: $CLIENT"
# $PREV_FREEZE leftovers = G1-5 step 2 not done: the $PREV_FREEZE marker/state would make this run skip the cycle (state started) or be refused by the dispatcher.
[ "$CLIENT" != "$PREV_G1_RID" ] || die "the $PREV_FREEZE one-use client marker $PREV_G1_RID is still on the guest: run 29-g1-recover.sh move first (G1-5 step 2)"
STATE_CFG=$(printf '%s\n' "$S" | sed -n '/^-- g1:/,/^-- /s/^ *cfg=\(.*\)$/\1/p' | head -1)
[ -z "$STATE_CFG" ] || [ "$STATE_CFG" != "$(wd "$PREV_HF/g1/serve-pi-service.json")" ] || die "the $PREV_FREEZE g1 state (cfg of the $PREV_FREEZE service config) is still on the guest: run 29-g1-recover.sh move first (G1-5 step 2)"
WX mkdir -p -m 0700 "$HG"
CAL_RW_DIGEST=$(hf_get CAL_RW_DIGEST); need_dig CAL_RW_DIGEST "$CAL_RW_DIGEST"
BASE_TD=$(hf_get BASE_TD); need_dig BASE_TD "$BASE_TD"
hf_has d09-cell.base.json || die "no $HF/d09-cell.base.json (10-upgrade.sh)"

if [ "$RANK" -lt 4 ] && [ -z "$CLIENT" ]; then
  say "0b. baseline before the $FREEZE service starts: catalog and requests as they are on the guest"
  if [ "$ARCHMODE" = 1 ]; then
    # Option A: the state was recreated empty by 27-archive-state.sh. The request journal does not exist yet (the first service start creates it) and
    # the catalog is 0 bytes; hf_commit refuses 0-byte exports, so nothing is exported: the baseline is empty (0 terminal lines) by construction.
    if op "collect catalog"; then
      CSZ=$(G "stat -c %s $OUT/_/catalog.jsonl" | tr -d '\r')
      if [ "$CSZ" = 0 ]; then ok "catalog is empty (fresh state after 27-archive-state.sh): baseline pi_workflow_terminal lines = 0"
      else
        hf_has g1/catalog-before-start.jsonl || fetch_export "$OUT/_/catalog.jsonl" g1/catalog-before-start.jsonl
        note "the catalog is not empty ($CSZ bytes) although 27 recreated it empty: the service ran since; baseline = this export ($(tcount "$HG/catalog-before-start.jsonl") terminal lines)"
      fi
    fi
    if op "collect requests"; then
      hf_has g1/requests-before-start.jsonl || fetch_export_nz "$OUT/_/serve-pi-requests.jsonl" g1/requests-before-start.jsonl || true
    else note "no request journal yet (expected after 27-archive-state.sh: the first service start creates it)"; fi
  else
  if ! hf_has g1/catalog-before-start.jsonl; then
    op "collect catalog"; fetch_export "$OUT/_/catalog.jsonl" g1/catalog-before-start.jsonl
    op "collect requests"; fetch_export "$OUT/_/serve-pi-requests.jsonl" g1/requests-before-start.jsonl
  fi
  echo "catalog before: $(WB "wc -lc < '$HG/catalog-before-start.jsonl'" | tr -d '\r') ; pi_workflow_terminal lines: $(tcount "$HG/catalog-before-start.jsonl")"
  fi
fi

# ---------------------------------------------------------------------------------------------------------------
if [ "$RANK" -lt 1 ]; then
  say "RG-2 svc stop; build and install the g1 service config"
  op "svc stop"
  CD=$(G 'sha256sum /etc/tbound/podman-config/containers.conf' | cut -d' ' -f1 | sed 's/^/sha256:/')
  SD=$(G 'sha256sum /etc/tbound/podman-config/storage.conf' | cut -d' ' -f1 | sed 's/^/sha256:/')
  eq "measured CD == recorded CONTAINERS_CONF_DIGEST" "$CD" "$(hf_get CONTAINERS_CONF_DIGEST)"
  eq "measured SD == recorded STORAGE_CONF_DIGEST" "$SD" "$(hf_get STORAGE_CONF_DIGEST)"
  WX python3 -I "$WGA/mk-svc-config.py" "$SRC/infra/guest/serve-pi-service.g1.json" "$HG/serve-pi-service.json" "$CD" "$SD"
  eq "provider_route" "$(jgetw "$HG/serve-pi-service.json" provider_route)" engineering
  eq "workflow_task_id" "$(jgetw "$HG/serve-pi-service.json" workflow_task_id)" g1-read-edit-bash-read
  ADM_NEW=$(jgetw "$HG/serve-pi-service.json" workflow_admission_id)
  echo "admission id $ADM_NEW (one use)"
  note "$FREEZE g1 admission id is read from the frozen repo: $ADM_NEW (must differ from $PREV_FREEZE)"
  [ "$ADM_NEW" != "$(jgetw "$PREV_HF/g1/serve-pi-service.json" workflow_admission_id)" ] || die "admission id $ADM_NEW is the one consumed at $PREV_FREEZE: G1-5 step 1 (new admission id in the g1 template) is missing"
  G "test ! -e $OUT/g1/managed-live-root.unsigned.json" || die "$OUT/g1 still holds an export of the $PREV_FREEZE cycle (reg-measure would be skipped and the stale unsigned registration fetched): run 29-g1-recover.sh move"
  CFGSHA=$(wd "$HG/serve-pi-service.json")
  stage_put stage-g1 "$HG/serve-pi-service.json" serve-pi-service.json
  op "cfg-install g1 $CFGSHA"
  RANK=1
fi
phase_end cfg

# ---------------------------------------------------------------------------------------------------------------
if [ "$RANK" -eq 1 ]; then
  say "RG-3 precheck: the live root /var/lib/tbound/live/root must exist before reg-measure (belt and braces)"
  # 27-archive-state.sh keeps the live-root DIRECTORY and archives only its content, so after 27 the root exists and is empty. The $FREEZE code also creates the
  # root in reg-measure (live_dir), which is exactly why a missing root here must stop: it would silently be re-created outside the archived state.
  # The check uses `tbound-op diag live-listing` (read-only, no session sudo needed); a missing directory shows up as find's ENOENT.
  LL=$(op "diag live-listing" 2>&1 | tr -d '\r') || true
  printf '%s\n' "$LL" | head -20
  case "$LL" in
    *"No such file or directory"*)
      die "the live root /var/lib/tbound/live/root is MISSING. At $FREEZE 27-archive-state.sh keeps the directory itself (it moves only its content aside), so a missing root is not the state 27 leaves: check whether 27 ran and whether something removed the root by hand. The $FREEZE reg-measure would create the root itself (live_dir), which would hide that: owner must look before the registration binds a new root" ;;
  esac
  ok "live root /var/lib/tbound/live/root exists (tbound-op diag live-listing answered; content: see above)"
  say "RG-3 reg-measure g1, fetch the unsigned registration"
  if ! hf_has g1/managed-live-root.unsigned.json; then
    if G "test -f $OUT/g1/managed-live-root.unsigned.json"; then note "export from an earlier reg-measure found; fetching it"; else
      op "svc stop"
      op "reg-measure g1"
    fi
    fetch_export "$OUT/g1/managed-live-root.unsigned.json" g1/managed-live-root.unsigned.json
  fi
  U=$HG/managed-live-root.unsigned.json
  say "checks on the unsigned registration (before anything is signed)"
  eq "runner_digest == TBOUND_DIGEST ($FREEZE)" "$(jgetw "$U" runner_digest)" "$TBOUND_DIGEST"
  eq "managed_root_xattr_calibration_digest == CAL_RW_DIGEST" "$(jgetw "$U" managed_root_xattr_calibration_digest)" "$CAL_RW_DIGEST"
  eq "service_uid/gid" "$(jgetw "$U" service_uid)/$(jgetw "$U" service_gid)" 995/986
  eq "service_cgroup_path" "$(jgetw "$U" service_cgroup_path)" /system.slice/tbound-pi.service
  eq "runner_path" "$(jgetw "$U" runner_path)" /usr/local/bin/tbound
  eq "service_config_digest == g1 config" "$(jgetw "$U" service_config_digest)" "$(wd "$HG/serve-pi-service.json")"
  phase_end measure

  say "RG-4 [OWNER SIGN] S3g: admin key by path only"
  if hf_has g1/managed-live-root.json; then ok "managed-live-root.json already signed in $HG (not re-signed)"; else
    WX bash "$SIGN" s3 "$U"
  fi
  REGDIGEST=$(hf_get g1/REGDIGEST); need_dig REGDIGEST-g1 "$REGDIGEST"
  phase_end s3

  say "RG-5 stage and reg-install g1"
  stage_put stage-g1 "$HG/managed-live-root.json" managed-live-root.json
  stage_put stage-g1 "$HG/managed-live-root.ed25519" managed-live-root.ed25519
  ro=$(op "reg-install g1 $(wd "$HG/managed-live-root.json") $(wd "$HG/managed-live-root.ed25519")" 2>&1 | tr -d '\r') || true
  printf '%s\n' "$ro"
  [[ $ro == *"reg-install g1 ok: REGDIGEST $REGDIGEST"* ]] || die "reg-install did not confirm REGDIGEST $REGDIGEST"
  RANK=2
fi
phase_end reg

# ---------------------------------------------------------------------------------------------------------------
if [ "$RANK" -eq 2 ]; then
  REGDIGEST=$(hf_get g1/REGDIGEST); need_dig REGDIGEST-g1 "$REGDIGEST"
  say "RG-6 [OWNER SIGN] g1 template with REGDIGEST-g1, TD, S4g: admin key by path only"
  if hf_has g1/pi-host-profile.signed.json; then ok "pi-host-profile.signed.json already in $HG (not re-signed)"; else
    WX bash "$SIGN" s4
  fi
  TD=$(hf_get g1/TD); PROFILE_DIGEST=$(hf_get g1/PROFILE_DIGEST); need_dig TD "$TD"; need_dig PROFILE_DIGEST "$PROFILE_DIGEST"
  eq "template registration digest == REGDIGEST-g1" "$(jgetw "$HG/d09-cell.json" managed_live_root_registration_digest)" "$REGDIGEST"
  eq "template expected_image_digest == IMGDIGEST" "$(jgetw "$HG/d09-cell.json" expected_image_digest)" "$IMGDIGEST"
  phase_end s4

  say "RG-7 stage and prof-install g1"
  stage_put stage-g1 "$HG/pi-host-profile.signed.json" pi-host-profile.json
  stage_put stage-g1 "$HG/pi-host-profile.ed25519" pi-host-profile.ed25519
  stage_put stage-g1 "$HG/d09-cell.json" d09-cell.json
  op "prof-install g1 $(wd "$HG/pi-host-profile.signed.json") $(wd "$HG/pi-host-profile.ed25519") $(wd "$HG/d09-cell.json")"
  RANK=3
fi
phase_end prof

# ---------------------------------------------------------------------------------------------------------------
if [ "$RANK" -eq 3 ]; then
  say "seed-live g1, svc start (the $FREEZE service starts here)"
  op "seed-live g1"
  op "svc start"
  hf_has g1/svc-start-journal.txt || fetch_export "$OUT/g1/svc-start-journal.txt" g1/svc-start-journal.txt
  RANK=4
fi
if [ "$RANK" -ge 4 ] && [ -z "$CLIENT" ]; then
  op "collect catalog"
  if [ "$ARCHMODE" = 1 ]; then hf_has g1/catalog-after-start.jsonl || fetch_export_nz "$OUT/_/catalog.jsonl" g1/catalog-after-start.jsonl || true   # may still be empty: the $FREEZE start has no earlier workflow to settle
  else hf_has g1/catalog-after-start.jsonl || fetch_export "$OUT/_/catalog.jsonl" g1/catalog-after-start.jsonl; fi
  echo "pi_workflow_terminal lines after the start (before any g1 request): $(tcount "$HG/catalog-after-start.jsonl")"
  op "diag l2" || note "diag l2 failed (see above)"
  hf_has g1/diag-l2.txt || fetch_export "$OUT/_/diag-l2.txt" g1/diag-l2.txt || true
fi
phase_end start

# ---------------------------------------------------------------------------------------------------------------
if [ -z "$CLIENT" ]; then
  say "L client-run g1 (detached; one use of the admission)"
  co=$(op "client-run g1" 2>&1 | tr -d '\r') || die "client-run failed: $co"
  printf '%s\n' "$co"
  CLIENT=$(printf '%s\n' "$co" | sed -n 's/.*rid \(g1-req-[0-9TZ]*\).*/\1/p' | head -1)
  [ -n "$CLIENT" ] || die "no rid in the client-run output"
fi
hf_put g1/RID "$CLIENT"
say "poll status g1 until the client has returned (max ~30 min)"
DONE=""
for i in $(seq 1 90); do
  S=$(g1_status)
  DONE=$(printf '%s\n' "$S" | sed -n 's/.*g1 client-run.status: \(done exit=[0-9]*\).*/\1/p' | head -1)
  [ -z "$DONE" ] || break
  echo "$(date -u +%FT%TZ) running ($(printf '%s\n' "$S" | sed -n 's/.*g1 client-run.status: \(.*\)$/\1/p' | head -1))"
  sleep 20
done
[ -n "$DONE" ] || die "client did not finish within the poll window; status g1 later, then re-run this script (it resumes at the collect step)"
echo "client: $DONE"
hf_put g1/CLIENT_STATUS "$DONE" || true

say "collect l4, catalog, requests; fetch the exports"
op "collect l4"
op "collect requests"
op "collect catalog"
for pair in _/l4.txt:l4.txt _/catalog.jsonl:catalog-after-run.jsonl _/serve-pi-requests.jsonl:requests-after-run.jsonl g1/response.txt:response.txt g1/client-run.status:client-run.status; do
  hf_has "g1/${pair##*:}" && { ok "g1/${pair##*:} already fetched"; continue; }
  fetch_export "$OUT/${pair%%:*}" "g1/${pair##*:}"
done
G "ls -l $OUT/_ $OUT/g1" | tr -d '\r' | hf_save g1/export-listing.txt 2>/dev/null && hf_commit g1/export-listing.txt || true
phase_end run

say "checks"
case "$DONE" in "done exit=0") ok "client returned exit=0";; *) die "client did not return exit=0: $DONE (read $HG/response.txt, l4.txt, requests-after-run.jsonl)";; esac
T0=$(tcount "$HG/catalog-before-start.jsonl"); T1=$(tcount "$HG/catalog-after-run.jsonl")
echo "pi_workflow_terminal lines: before the $FREEZE start $T0, after the g1 run $T1"
# On option A the state is fresh: G1 does not write its own terminal. The NEXT request verifies and settles it (31 checks that).
# For other recovery routes retain the prior-settlement check with ALLOW_NO_NEW_TERMINAL=1 as the override.
if [ "$ARCHMODE" = 1 ]; then
  [ "$T1" -eq "$T0" ] || die "fresh G1 unexpectedly added pi_workflow_terminal (before $T0, after $T1): inspect the catalog"
  ok "fresh G1 added 0 terminals; the next rp request must settle G1"
elif [ "$T1" -ge 1 ] && [ "$T1" -gt "$T0" ]; then
  ok "the catalog contains pi_workflow_terminal (new since the $FREEZE start)"
elif [ "${ALLOW_NO_NEW_TERMINAL:-0}" = 1 ]; then
  note "no new pi_workflow_terminal: accepted by ALLOW_NO_NEW_TERMINAL=1; inspect $HG/catalog-after-run.jsonl"
else
  die "no new pi_workflow_terminal for the earlier workflow; inspect the catalog or use ALLOW_NO_NEW_TERMINAL=1 after review"
fi
WB "grep pi_workflow_terminal '$HG/catalog-after-run.jsonl' | cut -c1-400 || true"
WB "grep -c pi_workflow_admission_reserved '$HG/catalog-after-run.jsonl' || true" | sed 's/^/pi_workflow_admission_reserved lines: /'
# event data is base64 JSON, so the request id is matched in the decoded data (a plain grep never matches)
WB "python3 -I -c 'import sys,json,base64; rid=sys.argv[2]; sys.exit(0 if any(json.loads(base64.b64decode(json.loads(l)[\"event\"][\"data\"])).get(\"request_id\")==rid for l in open(sys.argv[1]) if l.strip()) else 1)' '$HG/requests-after-run.jsonl' '$CLIENT'" || die "the g1 request id $CLIENT is not in serve-pi-requests.jsonl"
ok "request $CLIENT is in serve-pi-requests.jsonl"
journal_decoded "$HG/requests-after-run.jsonl" | grep -F "$CLIENT" | cut -c1-600
echo "response (head):"; WB "head -c 1200 '$HG/response.txt'"; echo
stage_clear stage-g1
note "the g1 service is left running; owner decision next: evidence (stage-ga, guest-evidence provider-exchange) and svc stop. Close-out: sudo -n true must still fail."
done_step
