#!/bin/bash
# 31 Route-probe RG cycle (NAME=rp, guest-route-bash-view) at $FREEZE, entirely through tbound-op verbs (narrowed sudo; the session drop-in must be CLOSED).
# PREPARED, NOT RUN. Same cycle as 30-reg-g1.sh (rglib.sh is the shared driver):
#   RG-2  svc stop; rp config = infra/guest/serve-pi-service.route-probe.json (the admission id carried by the frozen template, task route-probe-bash-view,
#         workflow_bash_output_paths []) with the two measured podman-config digests; stage-rp; cfg-install rp
#   RG-3  reg-measure rp; fetch the unsigned registration; checks before signing
#   RG-4  [OWNER SIGN] S3r -> $HF/rp/  (sign-run.sh rp s3: admin key by path, verify --runner-digest TBOUND_DIGEST, REGDIGEST-rp)
#   RG-5  stage; reg-install rp
#   RG-6  [OWNER SIGN] rp template (base template + REGDIGEST-rp), BASE_TD == pin, S4r -> $HF/rp/  (sign-run.sh rp s4)
#   RG-7  stage; prof-install rp (archives the run's files to /root/ga/rp/); svc start (settles the G1 workflow); diag l2
#   L     client-run rp (detached, ONE use of the admission); poll status rp; collect l4/requests/catalog; fetch response (probe-response.txt),
#         status, journal into $HF/rp/; collect audit <run id>; fetch the session and command audit.
#   checks: client exit 0; response status completed, error_code empty, receipt: success true, disposition NO_DIFF, publication NO_DIFF, settlement STOPPED,
#         provider_exchange true, claim_bearing false, tool_sequence = 4 x bash; 4 production_d09_command_settled events in the command audit;
#         the rp admission completed-terminal and the earlier g1 one, the rid in serve-pi-requests.jsonl.
# Resumable (position from `tbound-op status rp`; signed files in $HF/rp are verified, never re-signed). STOP_AFTER=<cfg|measure|s3|reg|s4|prof|start|run>.
# The service is left RUNNING at the end (as svc start leaves it); the next cycle (32) stops it first.
# Preconditions (checked): 10, 20, 50 done; session sudo CLOSED; 30-reg-g1.sh finished with "done exit=0" ($HF/g1/CLIENT_STATUS) and its exports in $HF/g1;
#   the live root still holds src/release.txt (the control file; client-run rp checks it). Not checked here: the target /home/node/.profile in the signed image
#   (readable by others, outside the ruleset, no symlink component): PROVISIONING-SIGNING.md "Route-probe run" asks for that READ-ONLY check before the run;
#   F9/logs/rp-profile-check.txt verified -rw-r--r-- regular file, no symlink components, for image sha256:e16806af0d73ee439e15322635d3693d4842ccf7a610e31254fef99ffc22f434.
#   That owner READ-ONLY check stands only for that image. $FREEZE changes the cell entrypoint, so IMGDIGEST is expected to change again:
#   STOP_AFTER=start, the owner rechecks /home/node/.profile in the $FREEZE image before the run.
# Open: evidence for guest-route-bash-view is produced by 40-guest-evidence.sh route-bash-view (reads $HF/rp/RUNID, written below).
# Usage: bash 31-reg-rp.sh
set -euo pipefail
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 31-reg-rp
load_env
check_key_meta
preflight_op
. "$GA/rglib.sh"
NAME=rp

hook_prior() {
  hf_has g1/CLIENT_STATUS && [ "$(hf_get g1/CLIENT_STATUS)" = "done exit=0" ] || die "the g1 run is not recorded as done exit=0 in $HF/g1/CLIENT_STATUS: finish 30-reg-g1.sh first"
  hf_has g1/catalog-after-run.jsonl && hf_has g1/response.txt && hf_has g1/serve-pi-service.json || die "g1 exports missing in $HF/g1: finish 30-reg-g1.sh first"
}

hook_cfg_build() {
  WX python3 -I "$WGA/mk-svc-config.py" "$SRC/infra/guest/serve-pi-service.route-probe.json" "$HG/serve-pi-service.json" "$CD" "$SD"
  eq "provider_route" "$(jgetw "$HG/serve-pi-service.json" provider_route)" engineering
  eq "workflow_task_id" "$(jgetw "$HG/serve-pi-service.json" workflow_task_id)" route-probe-bash-view
  local adm; adm=$(jgetw "$HG/serve-pi-service.json" workflow_admission_id)
  eq "workflow_admission_id == the frozen repo template" "$adm" "$(jgetw "$SRC/infra/guest/serve-pi-service.route-probe.json" workflow_admission_id)"
  eq "workflow_bash_output_paths" "$(jgetw "$HG/serve-pi-service.json" workflow_bash_output_paths)" "[]"
  [ "$adm" != "$(jgetw "$HF/g1/serve-pi-service.json" workflow_admission_id)" ] || die "rp admission id equals the g1 one"
  [ "$adm" != "$(jgetw "$PREV_HF/rp/serve-pi-service.json" workflow_admission_id)" ] || die "rp admission id equals the $PREV_FREEZE one: the frozen repo needs a new id"
  echo "admission id $adm (one use)"
}

hook_run_checks() {
  case "$DONE" in "done exit=0") ok "client returned exit=0";; *) die "client did not return exit=0: $DONE (read $HG/response.txt, response.txt.err, l4.txt, requests-after-run.jsonl)";; esac
  local R=$HG/response.txt ec
  echo "response (head):"; WB "head -c 1500 '$R'"; echo
  eq "response status" "$(jgetw "$R" status)" completed
  ec=$(jgetw "$R" error_code 2>/dev/null || true); eq "response error_code" "${ec:-}" ""
  eq "receipt.success" "$(jgetw "$R" receipt.success)" True
  eq "receipt.disposition" "$(jgetw "$R" receipt.disposition)" NO_DIFF
  eq "receipt.publication_status" "$(jgetw "$R" receipt.publication_status)" NO_DIFF
  eq "receipt.settlement" "$(jgetw "$R" receipt.settlement)" STOPPED
  eq "receipt.provider_exchange" "$(jgetw "$R" receipt.provider_exchange)" True
  eq "receipt.claim_bearing" "$(jgetw "$R" receipt.claim_bearing)" False
  eq "receipt.descendants_settled" "$(jgetw "$R" receipt.descendants_settled)" True
  eq "receipt.tool_sequence (4 bash calls)" "$(jgetw "$R" receipt.tool_sequence)" "['bash', 'bash', 'bash', 'bash']"
  note "receipt.command_cell_settlement_count = $(jgetw "$R" receipt.command_cell_settlement_count 2>/dev/null || echo absent)"
  RUNID=$(jgetw "$R" receipt.run_id); [[ $RUNID =~ ^[A-Za-z0-9._-]{8,128}$ ]] || die "receipt.run_id is not a usable run id: '$RUNID'"
  hf_put rp/RUNID "$RUNID"
  rg_fetch_audit "$RUNID"
  local n
  n=$(WB "grep -c production_d09_command_settled '$HG/command-audit.jsonl' || true" | tr -d '\r\n')
  eq "production_d09_command_settled events in the command audit (the 4 bash calls)" "$n" 4
  n=$(WB "grep -c production_tcb_proof '$HG/session-audit.jsonl' || true" | tr -d '\r\n')
  note "production_tcb_proof events in the session audit: $n"
  note "the producer (40-guest-evidence.sh route-bash-view) re-checks the exact results: three Permission denied, the control bytes of src/release.txt"
}

rg_cycle
G "ls -l $OUT/rp" | tr -d '\r' | head -20
hf_put rp/CHECKS_OK ok
note "the rp service is left running; next: 40-guest-evidence.sh route-bash-view (RUNID in $HF/rp/RUNID), then 32-reg-dr.sh (it stops the service). Close-out: sudo -n true must still fail."
done_step
