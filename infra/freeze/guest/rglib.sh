#!/bin/bash
# rglib.sh: the generic RG cycle for NAME=rp, NAME=dr and NAME=pilot (PROVISIONING-SIGNING.md "RG: re-registration cycle", Part D), SOURCED by 31-reg-rp.sh, 32-reg-dr.sh and 33-corpus-pilot.sh
# after glib.sh, load_env, check_key_meta and preflight_op. Modeled line by line on 30-reg-g1.sh (same verbs, same resume logic, same STOP_AFTER phases);
# the differences are only: the NAME parameter, no seed-live (the live root keeps the g1 fixture), the signing helper sign-run.sh, and for dr the
# two templates (the PROBE template is installed, the production template is staged beside it) and prof-install's fifth argument.
# NAME=pilot (corpus mode) has no client run (the service refuses client text): rg_cycle RETURNS after svc start and diag l2 (the other names run to
# the end), and the caller does the archive check (rg_archive_check) and everything after.
#
# The caller sets NAME (rp|dr|pilot) and defines these hooks before calling rg_cycle:
#   hook_prior       precondition checks on the earlier cycles (die on failure)
#   hook_cfg_build   build "$HG/serve-pi-service.json" (uses $CD $SD) and check its members
#   hook_baseline    (optional) extra baseline taken before the service of this cycle starts
#   hook_after_s4    (optional) checks on the signed S4 outputs in $HG, before STOP_AFTER=s4 and before anything is installed
#   hook_service_cleanup (optional) register cleanup before service start or resumed client polling
#   hook_run_checks  the checks after the client has returned and the exports are fetched ($DONE, $CLIENT, $HG)
#
# tbound-op verbs used here (line numbers as in infra/guest/operator/tbound-op at the F3 commit ea59706; they may have moved since):
#   status 931 (v_status)         svc stop/start 568 (v_svc)        cfg-install 415 (v_cfg_install)     reg-measure 441 (v_reg_measure)
#   reg-install 471 (v_reg_install) prof-install 488 (v_prof_install) client-run 659 (v_client_run)
#   collect 845 (v_collect: requests|catalog|audit RUNID|l4)         diag 886 (v_diag: l2)
. "$(dirname -- "${BASH_SOURCE[0]}")/../freeze-guard.sh" || exit 2
OUT=/var/tmp/tbound-op

phase_end() { if [ "${STOP_AFTER:-}" = "$1" ]; then say "STOP_AFTER=$1 reached"; done_step; exit 0; fi; }
st_rank_of() { case "$1" in config) echo 1;; reg) echo 2;; prof) echo 3;; started) echo 4;; *) echo 0;; esac; }
rg_status() { G "$OP status $NAME" 2>&1 | tr -d '\r'; }
rg_state() { rg_status | sed -n 's/^ *state=\(.*\)$/\1/p' | head -1; }
tcount() { WB "grep -c pi_workflow_terminal '$1' || true" | tr -d '\r\n'; }

# rg_fetch_audit <RUNID>: collect the run's audit files and fetch session (required) and command/provider (when present) into $HG.
rg_fetch_audit() {
  local rid=$1 f
  op "collect audit $rid"
  for f in session command provider; do
    if G "test -f $OUT/_/$rid-$f-audit.jsonl"; then
      if [ "$NAME" = dr ] && { [ "$f" = command ] || [ "$f" = provider ]; }; then
        # A probe run starts no Pi and runs no command: these two audits are genuinely 0 bytes for the dr run only. Allow exactly these two.
        hf_has "$NAME/$f-audit.jsonl" || ALLOW_EMPTY=1 fetch_export "$OUT/_/$rid-$f-audit.jsonl" "$NAME/$f-audit.jsonl"
      else
        hf_has "$NAME/$f-audit.jsonl" || fetch_export "$OUT/_/$rid-$f-audit.jsonl" "$NAME/$f-audit.jsonl"
      fi
    elif [ "$f" = session ]; then die "no $OUT/_/$rid-session-audit.jsonl after collect audit"; fi
  done
}

rg_cycle() {
  HG=$HF/$NAME
  SIGN="$WGA/sign-run.sh"
  STG=stage-$NAME
  case $NAME in rp) RESP=probe-response.txt;; dr) RESP=dr-response.txt;; pilot) RESP=;; *) die "rg_cycle: NAME must be rp, dr or pilot";; esac

  say "0. dispatcher and pins"
  S=$(rg_status); printf '%s\n' "$S"
  [[ $S == *"pins F=$F"* ]] || die "tbound-op pins are not $FREEZE ($F): run 10-upgrade.sh"
  [[ $S == *"anchor: ok"* ]] || die "anchor not ok"
  [[ $S == *"runner: ${TBOUND_DIGEST:0:23} tbound-profile: ${TBP_DIGEST:0:23}"* ]] || die "runner/tbound-profile on the guest are not the $FREEZE pins"
  hook_prior
  STATE=$(rg_state); RANK=$(st_rank_of "${STATE:-none}")
  echo "$NAME state: ${STATE:-none} (rank $RANK)"
  CLIENT=$(printf '%s\n' "$S" | sed -n "s/.*client=\(${NAME}-req-[0-9TZ]*\).*/\1/p" | head -1)
  [ -z "$CLIENT" ] || echo "$NAME client-run already launched: $CLIENT"
  WX mkdir -p -m 0700 "$HG"
  CAL_RW_DIGEST=$(hf_get CAL_RW_DIGEST); need_dig CAL_RW_DIGEST "$CAL_RW_DIGEST"
  BASE_TD=$(hf_get BASE_TD); need_dig BASE_TD "$BASE_TD"
  hf_has d09-cell.base.json || die "no $HF/d09-cell.base.json (10-upgrade.sh)"

  if [ "$RANK" -lt 4 ] && [ -z "$CLIENT" ]; then
    say "0b. baseline before the $NAME service starts: catalog and requests as they are on the guest"
    if ! hf_has $NAME/catalog-before-start.jsonl; then
      op "collect catalog"; fetch_export "$OUT/_/catalog.jsonl" $NAME/catalog-before-start.jsonl
      op "collect requests"; fetch_export "$OUT/_/serve-pi-requests.jsonl" $NAME/requests-before-start.jsonl
    fi
    echo "catalog before: $(WB "wc -lc < '$HG/catalog-before-start.jsonl'" | tr -d '\r') ; pi_workflow_terminal lines: $(tcount "$HG/catalog-before-start.jsonl")"
    if declare -F hook_baseline >/dev/null; then hook_baseline; fi
  fi

  # -------------------------------------------------------------------------------------------------------------
  if [ "$RANK" -lt 1 ]; then
    say "RG-2 svc stop; build and install the $NAME service config"
    op "svc stop"
    CD=$(G 'sha256sum /etc/tbound/podman-config/containers.conf' | cut -d' ' -f1 | sed 's/^/sha256:/')
    SD=$(G 'sha256sum /etc/tbound/podman-config/storage.conf' | cut -d' ' -f1 | sed 's/^/sha256:/')
    eq "measured CD == recorded CONTAINERS_CONF_DIGEST" "$CD" "$(hf_get CONTAINERS_CONF_DIGEST)"
    eq "measured SD == recorded STORAGE_CONF_DIGEST" "$SD" "$(hf_get STORAGE_CONF_DIGEST)"
    hook_cfg_build
    G "test ! -e $OUT/$NAME/managed-live-root.unsigned.json" || die "$OUT/$NAME still holds an export of an earlier cycle (reg-measure would be skipped and a stale unsigned registration fetched): owner must look"
    CFGSHA=$(wd "$HG/serve-pi-service.json")
    stage_put $STG "$HG/serve-pi-service.json" serve-pi-service.json
    op "cfg-install $NAME $CFGSHA"
    RANK=1
  fi
  phase_end cfg

  # -------------------------------------------------------------------------------------------------------------
  if [ "$RANK" -eq 1 ]; then
    say "RG-3 reg-measure $NAME, fetch the unsigned registration"
    if ! hf_has $NAME/managed-live-root.unsigned.json; then
      if G "test -f $OUT/$NAME/managed-live-root.unsigned.json"; then note "export from an earlier reg-measure found; fetching it"; else
        op "svc stop"
        op "reg-measure $NAME"
      fi
      fetch_export "$OUT/$NAME/managed-live-root.unsigned.json" $NAME/managed-live-root.unsigned.json
    fi
    U=$HG/managed-live-root.unsigned.json
    say "checks on the unsigned registration (before anything is signed)"
    eq "runner_digest == TBOUND_DIGEST ($FREEZE)" "$(jgetw "$U" runner_digest)" "$TBOUND_DIGEST"
    eq "managed_root_xattr_calibration_digest == CAL_RW_DIGEST" "$(jgetw "$U" managed_root_xattr_calibration_digest)" "$CAL_RW_DIGEST"
    eq "service_uid/gid" "$(jgetw "$U" service_uid)/$(jgetw "$U" service_gid)" 995/986
    eq "service_cgroup_path" "$(jgetw "$U" service_cgroup_path)" /system.slice/tbound-pi.service
    eq "runner_path" "$(jgetw "$U" runner_path)" /usr/local/bin/tbound
    eq "service_config_digest == $NAME config" "$(jgetw "$U" service_config_digest)" "$(wd "$HG/serve-pi-service.json")"
    phase_end measure

    say "RG-4 [OWNER SIGN] S3 ($NAME): admin key by path only"
    if hf_has $NAME/managed-live-root.json; then ok "managed-live-root.json already signed in $HG (not re-signed)"; else
      WX bash "$SIGN" $NAME s3 "$U"
    fi
    REGDIGEST=$(hf_get $NAME/REGDIGEST); need_dig REGDIGEST-$NAME "$REGDIGEST"
    phase_end s3

    say "RG-5 stage and reg-install $NAME"
    stage_put $STG "$HG/managed-live-root.json" managed-live-root.json
    stage_put $STG "$HG/managed-live-root.ed25519" managed-live-root.ed25519
    ro=$(op "reg-install $NAME $(wd "$HG/managed-live-root.json") $(wd "$HG/managed-live-root.ed25519")" 2>&1 | tr -d '\r') || true
    printf '%s\n' "$ro"
    [[ $ro == *"reg-install $NAME ok: REGDIGEST $REGDIGEST"* ]] || die "reg-install did not confirm REGDIGEST $REGDIGEST"
    RANK=2
  fi
  phase_end reg

  # -------------------------------------------------------------------------------------------------------------
  if [ "$RANK" -eq 2 ]; then
    REGDIGEST=$(hf_get $NAME/REGDIGEST); need_dig REGDIGEST-$NAME "$REGDIGEST"
    say "RG-6 [OWNER SIGN] $NAME template(s) with REGDIGEST-$NAME, TD, S4 ($NAME): admin key by path only"
    if hf_has $NAME/pi-host-profile.signed.json; then ok "pi-host-profile.signed.json already in $HG (not re-signed)"; else
      WX bash "$SIGN" $NAME s4
    fi
    TD=$(hf_get $NAME/TD); PROFILE_DIGEST=$(hf_get $NAME/PROFILE_DIGEST); need_dig TD "$TD"; need_dig PROFILE_DIGEST "$PROFILE_DIGEST"
    eq "template registration digest == REGDIGEST-$NAME" "$(jgetw "$HG/d09-cell.json" managed_live_root_registration_digest)" "$REGDIGEST"
    eq "template expected_image_digest == IMGDIGEST" "$(jgetw "$HG/d09-cell.json" expected_image_digest)" "$IMGDIGEST"
    if [ "$NAME" = dr ]; then
      TD_PROD=$(hf_get dr/TD_PROD); need_dig TD_PROD "$TD_PROD"
      P=$HG/d09-cell.route-probe.json
      eq "probe template registration digest == REGDIGEST-dr" "$(jgetw "$P" managed_live_root_registration_digest)" "$REGDIGEST"
      eq "probe template route_probe.name" "$(jgetw "$P" route_probe.name)" direct-runtime/v1
      eq "probe template route_probe.target" "$(jgetw "$P" route_probe.target)" /home/node/.profile
      eq "probe template route_probe.control" "$(jgetw "$P" route_probe.control)" /var/lib/tbound/pi-worker/src/release.txt
      [ "$TD" != "$TD_PROD" ] || die "probe TD equals production TD"
      echo "profile pins the PROBE template TD $TD (production TD $TD_PROD)"
    fi
    if declare -F hook_after_s4 >/dev/null; then hook_after_s4; fi
    phase_end s4

    say "RG-7 stage and prof-install $NAME"
    stage_put $STG "$HG/pi-host-profile.signed.json" pi-host-profile.json
    stage_put $STG "$HG/pi-host-profile.ed25519" pi-host-profile.ed25519
    if [ "$NAME" = dr ]; then
      stage_put $STG "$HG/d09-cell.route-probe.json" d09-cell.route-probe.json
      stage_put $STG "$HG/d09-cell.json" d09-cell.json
      po=$(op "prof-install dr $(wd "$HG/pi-host-profile.signed.json") $(wd "$HG/pi-host-profile.ed25519") $(wd "$HG/d09-cell.route-probe.json") $(wd "$HG/d09-cell.json")" 2>&1 | tr -d '\r') || true
    else
      stage_put $STG "$HG/d09-cell.json" d09-cell.json
      po=$(op "prof-install $NAME $(wd "$HG/pi-host-profile.signed.json") $(wd "$HG/pi-host-profile.ed25519") $(wd "$HG/d09-cell.json")" 2>&1 | tr -d '\r') || true
    fi
    printf '%s\n' "$po"
    [[ $po == *"prof-install $NAME ok: "* ]] || die "prof-install $NAME did not confirm"
    [[ $po == *"TD $TD "* ]] || die "prof-install TD is not the signed one ($TD)"
    [[ $po == *"profile $PROFILE_DIGEST "* ]] || note "prof-install profile digest differs from the one the verify output gave ($PROFILE_DIGEST): compare by hand"
    RANK=3
  fi
  phase_end prof

  # -------------------------------------------------------------------------------------------------------------
  if declare -F hook_service_cleanup >/dev/null; then hook_service_cleanup; fi
  if [ "$RANK" -eq 3 ]; then
    say "svc start (the $NAME service starts here; a completed earlier workflow is already terminal)"
    op "svc start"
    hf_has $NAME/svc-start-journal.txt || fetch_export "$OUT/$NAME/svc-start-journal.txt" $NAME/svc-start-journal.txt
    RANK=4
  fi
  if [ "$RANK" -ge 4 ] && [ -z "$CLIENT" ]; then
    op "collect catalog"; hf_has $NAME/catalog-after-start.jsonl || fetch_export "$OUT/_/catalog.jsonl" $NAME/catalog-after-start.jsonl
    echo "pi_workflow_terminal lines after the start (before any $NAME request): $(tcount "$HG/catalog-after-start.jsonl")"
    op "diag l2" || note "diag l2 failed (see above)"
    hf_has $NAME/diag-l2.txt || fetch_export "$OUT/_/diag-l2.txt" $NAME/diag-l2.txt || true
  fi
  phase_end start
  if [ "$NAME" = pilot ]; then return 0; fi   # corpus mode: no client run (the service refuses client text)

  # -------------------------------------------------------------------------------------------------------------
  if [ -z "$CLIENT" ]; then
    say "L client-run $NAME (detached; one use of the admission)"
    co=$(op "client-run $NAME" 2>&1 | tr -d '\r') || die "client-run failed: $co"
    printf '%s\n' "$co"
    CLIENT=$(printf '%s\n' "$co" | sed -n "s/.*rid \(${NAME}-req-[0-9TZ]*\).*/\1/p" | head -1)
    [ -n "$CLIENT" ] || die "no rid in the client-run output"
  fi
  hf_put $NAME/RID "$CLIENT"
  say "poll status $NAME until the client has returned (max ~30 min)"
  DONE=""
  for i in $(seq 1 90); do
    S=$(rg_status)
    DONE=$(printf '%s\n' "$S" | sed -n "s/.*${NAME} client-run.status: \(done exit=[0-9]*\).*/\1/p" | head -1)
    [ -z "$DONE" ] || break
    echo "$(date -u +%FT%TZ) running ($(printf '%s\n' "$S" | sed -n "s/.*${NAME} client-run.status: \(.*\)\$/\1/p" | head -1))"
    sleep 20
  done
  [ -n "$DONE" ] || die "client did not finish within the poll window; status $NAME later, then re-run this script (it resumes at the collect step)"
  echo "client: $DONE"
  hf_put $NAME/CLIENT_STATUS "$DONE" || true

  say "collect l4, catalog, requests; fetch the exports"
  op "collect l4"
  op "collect requests"
  op "collect catalog"
  for pair in _/l4.txt:l4.txt _/catalog.jsonl:catalog-after-run.jsonl _/serve-pi-requests.jsonl:requests-after-run.jsonl $NAME/$RESP:response.txt $NAME/client-run.status:client-run.status; do
    hf_has "$NAME/${pair##*:}" && { ok "$NAME/${pair##*:} already fetched"; continue; }
    fetch_export "$OUT/${pair%%:*}" "$NAME/${pair##*:}"
  done
  if ! hf_has $NAME/response.txt.err && G "test -f $OUT/$NAME/$RESP.err"; then fetch_export "$OUT/$NAME/$RESP.err" $NAME/response.txt.err; fi
  G "ls -l $OUT/_ $OUT/$NAME" | tr -d '\r' | hf_save $NAME/export-listing.txt 2>/dev/null && hf_commit $NAME/export-listing.txt || true
  phase_end run

  say "checks"
  hook_run_checks
  rg_archive_check
  rg_catalog_checks
  WB "python3 -I -c 'import sys,json,base64; rid=sys.argv[2]; sys.exit(0 if any(json.loads(base64.b64decode(json.loads(l)[\"event\"][\"data\"])).get(\"request_id\")==rid for l in open(sys.argv[1]) if l.strip()) else 1)' '$HG/requests-after-run.jsonl' '$CLIENT'" || die "the $NAME request id $CLIENT is not in serve-pi-requests.jsonl"
  ok "request $CLIENT is in serve-pi-requests.jsonl"
  journal_decoded "$HG/requests-after-run.jsonl" | grep -F "$CLIENT" | cut -c1-600
}

# The settlement of the workflows, by admission and outcome (not by counting terminals). A completed request is settled when it
# completes, so after its run the catalog holds a completed terminal for its own admission. The earlier workflow stays
# terminal: completed for g1 and rp. dr ends unknown by design, so its own admission stays open until the owner abandons it
# (then it reads terminal:abandoned-unknown).
rg_catalog_checks() {
  local own_adm prior="" prior_adm="" state
  own_adm=$(jgetw "$HG/serve-pi-service.json" workflow_admission_id)
  state=$(workflow_state "$HG/catalog-after-run.jsonl" "$own_adm")
  if [ "$NAME" = dr ]; then
    prior=rp
    case "$state" in
      open|terminal:abandoned-unknown*) ok "the dr workflow ($own_adm) is $state: unknown by design, settled only by the owner's abandon" ;;
      *) die "the dr workflow ($own_adm) is '$state' after its run: expected open (unknown) or an abandoned terminal" ;;
    esac
  else
    [ "$NAME" = rp ] && prior=g1
    [ "$state" = terminal:completed ] || die "the $NAME workflow ($own_adm) is '$state' after its run, expected terminal:completed (settled at completion; read svc-start journal and the service log for a settlement failure)"
    ok "the $NAME workflow ($own_adm) has a completed pi_workflow_terminal"
  fi
  if [ -n "$prior" ]; then
    prior_adm=$(jgetw "$HF/$prior/serve-pi-service.json" workflow_admission_id)
    state=$(workflow_state "$HG/catalog-after-run.jsonl" "$prior_adm")
    [ "$state" = terminal:completed ] || die "the earlier ($prior) workflow ($prior_adm) is '$state', expected terminal:completed"
    ok "the earlier ($prior) workflow ($prior_adm) is terminal:completed"
  fi
  WB "grep pi_workflow_terminal '$HG/catalog-after-run.jsonl' | cut -c1-400 || true"
  WB "grep -c pi_workflow_admission_reserved '$HG/catalog-after-run.jsonl' || true" | sed 's/^/pi_workflow_admission_reserved lines: /'
}

# The run's own files archived by prof-install in /root/ga/$NAME (what the guest-evidence producers read) are the signed ones: compare the archive's
# sha256 list (exported by the verb, fetched) with the owner files in $HG. For dr the production template is d09-cell.json and the installed probe
# template is d09-cell.route-probe.json.
rg_archive_check() {
  local pair want got AR
  hf_has $NAME/archive.sha256 || fetch_export "$OUT/$NAME/archive.sha256" $NAME/archive.sha256
  AR=$(WX cat "$HG/archive.sha256" | tr -d '\r')
  for pair in pi-host-profile.json:pi-host-profile.signed.json pi-host-profile.ed25519:pi-host-profile.ed25519 pi-host-profile.pub:pi-host-profile.pub \
              managed-live-root.json:managed-live-root.json managed-live-root.ed25519:managed-live-root.ed25519 serve-pi-service.json:serve-pi-service.json \
              d09-cell.json:d09-cell.json; do
    want=$(wd "$HG/${pair##*:}")
    got=$(printf '%s\n' "$AR" | awk -v f="${pair%%:*}" '$2==f{print "sha256:"$1}')
    eq "/root/ga/$NAME/${pair%%:*} == owner ${pair##*:}" "$got" "$want"
  done
  if [ "$NAME" = dr ]; then
    want=$(wd "$HG/d09-cell.route-probe.json")
    got=$(printf '%s\n' "$AR" | awk '$2=="d09-cell.route-probe.json"{print "sha256:"$1}')
    eq "/root/ga/dr/d09-cell.route-probe.json == owner probe template" "$got" "$want"
  fi
}
