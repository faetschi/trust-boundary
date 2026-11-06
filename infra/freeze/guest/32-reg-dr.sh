#!/bin/bash
# 32 Direct-runtime probe RG cycle (NAME=dr, guest-route-direct-runtime) at $FREEZE, entirely through tbound-op verbs (narrowed sudo; session drop-in CLOSED).
# PREPARED, NOT RUN. Same cycle as 30/31 (rglib.sh is the shared driver), with the runbook's dr differences (PROVISIONING-SIGNING.md RG table, RG-6, RG-7):
#   RG-1/2  config = infra/guest/serve-pi-service.route-probe.json with the two measured podman digests AND a NEW admission id, the one field set by hand
#           (mk-dr-config.py; id = DR_ADMISSION_ID from the values file; recorded in $HF/dr/ADMISSION_ID and never changed afterwards).
#           Every earlier dr id is refused below (the $PREV_FREEZE id and DR_ADMISSION_REFUSE from the values file).
#           The dispatcher (cfg-install dr) accepts exactly the shipped route-probe template + CD/SD + a different id of [a-z0-9][a-z0-9-]{4,62}[a-z0-9], and refuses an id
#           used before (templates, $STATE/admissions). No repo change, no new freeze: the id is a field of the owner-built config, not of the repo template.
#   RG-4    [OWNER SIGN] S3d -> $HF/dr/ (sign-run.sh dr s3)
#   RG-6    [OWNER SIGN] S4d (sign-run.sh dr s4): production template $HF/dr/d09-cell.json (base + REGDIGEST-dr) and PROBE template d09-cell.route-probe.json
#           (same + route_probe {direct-runtime/v1, /home/node/.profile, /var/lib/tbound/pi-worker/src/release.txt}); every BASE_TD == pin; the profile pins the PROBE template's TD.
#   RG-7    prof-install dr <profile> <sig> <probe template> <production template>: installs the PROBE template as /etc/tbound/d09-cell.json, archives
#           /root/ga/dr/d09-cell.route-probe.json (installed) and /root/ga/dr/d09-cell.json (production). Then svc start.
#   L       client-run dr (any task; Pi is not started; the service run FAILS after the launch proof is journaled: a nonzero client exit is EXPECTED), poll status dr.
#           The run id is the new "*-session-audit.jsonl" (listing of /var/tmp/tbound-op/_ before vs after the run; override with DR_RUNID=<id>).
#   The service is STOPPED after the client returns and on cleanup after a timeout/export failure (KEEP_RUNNING=1 to skip), as the runbook says: do not start it for anything else before the
#   pilot cycle replaces the probe template. Checks: the rp workflow is terminal:completed, the dr one open (unknown by design), exactly one production_tcb_proof event in
#   the run's session audit, the archive in /root/ga/dr equals the signed files. Result: $HF/dr/RUNID for 40-guest-evidence.sh route-direct-runtime.
# Resumable and STOP_AFTER=<cfg|measure|s3|reg|s4|prof|start|run> as 30/31.
# Preconditions (checked): 10, 20, 50 done; session sudo CLOSED; 31-reg-rp.sh completed all its checks ($HF/rp/CHECKS_OK). The live root must still hold
#   src/release.txt (control /var/lib/tbound/pi-worker/src/release.txt; client-run dr checks it). NOT checked here (READ-ONLY owner check asked by the runbook before the run):
#   /home/node/.profile exists in the signed image, is readable by others, lies outside the ruleset paths and has no symlink component.
# Open: the dr request ends in failure by design; whether it blocks the pilot as an unsettled/unknown request (prior-workflow-unsettled, "Operator recovery") is
#   not decided by the runbook: after the run read the `tbound-op status dr` block and the requests line printed below before any pilot step; `tbound-op abandon` is NOT called here.
# Usage: bash 32-reg-dr.sh
set -euo pipefail
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 32-reg-dr
load_env
check_key_meta
preflight_op
. "$GA/rglib.sh"
NAME=dr

dr_stop() {
  if [ "${KEEP_RUNNING:-0}" != 1 ]; then op "svc stop"; else note "KEEP_RUNNING=1: the dr service is left running (runbook: stop it)"; fi
}
hook_service_cleanup() { cleanup_add dr_stop; }

hook_prior() {
  hf_has rp/CHECKS_OK && hf_has rp/serve-pi-service.json || die "the rp cycle is not complete ($HF/rp/CHECKS_OK missing): finish 31-reg-rp.sh first"
}

# Listing of the export directory before the dr service request: the run id is the session audit that appears afterwards.
hook_baseline() {
  if ! hf_has dr/outdir-listing-before.txt; then
    op "collect l4"
    G "ls $OUT/_" | tr -d '\r' | hf_save dr/outdir-listing-before.txt; hf_commit dr/outdir-listing-before.txt
  fi
}

hook_cfg_build() {
  local adm=${DR_ADMISSION_ID:-} other
  [ -n "$adm" ] || die "DR_ADMISSION_ID is not set in $V"
  [[ $adm =~ ^[a-z0-9][a-z0-9-]{4,62}[a-z0-9]$ ]] || die "admission id '$adm' does not match [a-z0-9][a-z0-9-]{4,62}[a-z0-9]"
  [ "$adm" != "$PREV_DR_ADMISSION_ID" ] || die "admission id $adm was spent at $PREV_FREEZE ($PREV_HF/dr/ADMISSION_ID); set a new DR_ADMISSION_ID in $V"
  local spent
  for spent in $DR_ADMISSION_REFUSE; do
    [ "$adm" != "$spent" ] || die "admission id $adm was spent by an earlier freeze (DR_ADMISSION_REFUSE in $V)"
  done
  for other in "$HF/rp/serve-pi-service.json" "$HF/g1/serve-pi-service.json" "$PREV_HF/g1/serve-pi-service.json" "$PREV_HF/rp/serve-pi-service.json"; do
    [ "$(jgetw "$other" workflow_admission_id)" != "$adm" ] || die "admission id $adm was used by $other"
  done
  if prev_has dr/ADMISSION_ID; then
    [ "$(prev_get dr/ADMISSION_ID)" != "$adm" ] || die "admission id $adm was spent by the $PREV_FREEZE dr cycle ($PREV_HF/dr/ADMISSION_ID)"
  fi
  hf_put dr/ADMISSION_ID "$adm"
  WX python3 -I "$WGA/mk-dr-config.py" "$SRC/infra/guest/serve-pi-service.route-probe.json" "$HG/serve-pi-service.json" "$CD" "$SD" "$adm"
  eq "provider_route" "$(jgetw "$HG/serve-pi-service.json" provider_route)" engineering
  eq "workflow_task_id" "$(jgetw "$HG/serve-pi-service.json" workflow_task_id)" route-probe-bash-view
  eq "workflow_admission_id" "$(jgetw "$HG/serve-pi-service.json" workflow_admission_id)" "$adm"
  eq "workflow_bash_output_paths" "$(jgetw "$HG/serve-pi-service.json" workflow_bash_output_paths)" "[]"
  echo "admission id $adm (one use)"
}

hook_run_checks() {
  echo "client: $DONE (a nonzero exit is expected: Pi is not started in a probe run)"
  dr_stop
  echo "response (head):"; WB "head -c 1500 '$HG/response.txt'"; echo
  if hf_has dr/response.txt.err; then echo "client stderr (head):"; WB "head -c 600 '$HG/response.txt.err'"; echo; fi
  local RUNID=${DR_RUNID:-} new n
  if [ -z "$RUNID" ]; then
    hf_has dr/outdir-listing-before.txt || die "no $HG/outdir-listing-before.txt (baseline missing): set DR_RUNID=<run id of the new *-session-audit.jsonl>"
    new=$(comm -13 <(WX cat "$HG/outdir-listing-before.txt" | tr -d '\r' | sort) <(G "ls $OUT/_" | tr -d '\r' | sort) \
          | sed -n 's/^audit-\(.*\)-session-audit\.jsonl$/\1/p')
    n=$(printf '%s\n' "$new" | grep -c . || true)
    [ "$n" = 1 ] || die "expected exactly one new session audit after the dr run, found $n: [$new]. Read $HG/l4.txt; set DR_RUNID=<id> to override"
    RUNID=$new
  fi
  [[ $RUNID =~ ^[A-Za-z0-9._-]{8,128}$ ]] || die "not a usable run id: '$RUNID'"
  echo "dr run id: $RUNID"
  hf_put dr/RUNID "$RUNID"
  rg_fetch_audit "$RUNID"
  n=$(WB "grep -c production_tcb_proof '$HG/session-audit.jsonl' || true" | tr -d '\r\n')
  eq "production_tcb_proof events in the session audit (the launch proof)" "$n" 1
  G "$OP status dr" | tr -d '\r' | sed -n '/^-- dr/,$p' | head -30
}

rg_cycle
hf_put dr/CHECKS_OK ok
note "the dr service is stopped (unless KEEP_RUNNING=1). Next: 40-guest-evidence.sh route-direct-runtime (RUNID in $HF/dr/RUNID). Do not start the service before the pilot cycle replaces the probe template."
done_step
