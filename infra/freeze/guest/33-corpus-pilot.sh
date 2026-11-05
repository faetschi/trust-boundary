#!/bin/bash
# 33 Corpus mode on the guest (Part D, NAME=pilot) at $FREEZE: PREPARED, NOT RUN. Automates report 01 section 3 (steps 1-12) and PROVISIONING-SIGNING.md Part D / Spike B.
# Every guest write is a tbound-op verb (narrowed sudo, the session drop-in CLOSED when the script starts), except the artifact install, which has no verb
# (runbook P13.3) and runs inside the session sudo window (00-session-sudo.sh open ... close; TBA_PW; closed again even on failure).
#   1 abandon    the unsettled dr request (rid = $HF/dr/RID). Already abandoned/completed (reqstate verdict): continue. Otherwise the script settles NOTHING by
#                itself: it prints tbound-op's abandon facts + TOKEN and the request-specific catalog evidence (admissions, scopes undrained, drained) and stops
#                with the owner's instruction (the verb by hand, or rerun with ABANDON_CONFIRM=<token>). The facts cannot prove "no pending effect" while the
#                service is stopped (cgroup/podman probes read `unavailable`), hence no auto-confirm. With ABANDON_CONFIRM=<token> it requires admissions==1,
#                no undrained scope and service+socket inactive, confirms through `op` (kit log), re-reads the journal (verdict abandoned) and saves the
#                abandoned record (provider_settlement included) as $HF/pilot/dr-abandoned-record.json.
#   1b keys      with TBOUND_TRIAL_KEY and TBOUND_CLIENT_KEY (paths): read-only SSH-3 probes (trial `reset <case> --check`: the pre-corpus refusal or a check
#                document; client `id`: exit 2, "not a client command"). Without them: says that M3 needs them and continues.
#   2 manifest   D1, offline on the WSL side: the T5 draft of $SRC + the four pins of `tbound-profile corpus-pins` (template $HF/d09-cell.base.json) ->
#                $HF/pilot/manifest.json; `corpus-pins --manifest` must pass; `corpus-digest` with the 76 T5 artifacts and the ceilings 17 / 20000 / 1200 ->
#                $HF/pilot/CORPUS_DIGEST. Any refusal prints the tool's violation list and stops.
#   3 artifacts  the 76 files of corpus/pilot/t5-2026-10-29/artifacts of $SRC into /opt/tbound/corpus/artifacts (session sudo window); afterwards count, per-file
#                sha256, owner and modes are verified. Guest files not in the T5 set are moved (mv -n) to artifacts.pre-t5-<UTC>.
#   4 cycle      D2 `manifest-install`, then the RG cycle of rglib.sh for NAME=pilot: svc stop, cfg-install (config = infra/guest/serve-pi-service.corpus-pilot.json
#                with CORPUS_DIGEST and the two measured podman digests, mk-svc-config.py), reg-measure, S3' (sign-run.sh pilot s3), reg-install, S4' (sign-run.sh
#                pilot s4; corpus-pins on the new template must give the same pins), prof-install, svc start, diag l2. No client run: corpus mode refuses client
#                text. Then the archive check. The service is left RUNNING (the netsink step needs it);
#                before CYCLE_OK the service must be active and diag l2 must pass l2_check (one svc start if state started but stopped).
#   5 netsink    only with TBOUND_TRIAL_KEY=<path of the trial forced-command key> (passed to ssh by path, never read): `trial reset <first compliant case> --check`,
#                one real `trial reset` (attempt marker before, success marker after a valid
#                tbound-trial-reset/v1 document; an attempt without success stops for owner recovery, never retried), then 40-guest-evidence.sh netsink. Without the key the script ends after phase 4 and prints what the owner does.
# Resumable (phases 1-3 re-check their result; phase 4 resumes from `tbound-op status pilot`; signed files in $HF/pilot are verified, never re-signed).
# STOP_AFTER=<abandon|keys|manifest|artifacts|cfg|measure|s3|reg|s4|prof|start|cycle>.
# Preconditions (checked): 10, 20, 50 done; session sudo CLOSED; 32-reg-dr.sh finished ($HF/dr/CHECKS_OK, $HF/dr/RID); the dispatcher has no pilot state.
# The owner starts it after 32-reg-dr.sh (and 40-guest-evidence.sh route-direct-runtime). Needs TBA_PW (read -rs TBA_PW; export TBA_PW) for phase 3 only if the artifacts differ.
# Usage: bash 33-corpus-pilot.sh     (TBOUND_TRIAL_KEY=<key path> bash 33-corpus-pilot.sh   for phase 5;  NODE_BIN=<Linux path of the pinned Node> if ~/env.sh has none)
set -euo pipefail
case "$-" in *x*) echo "FATAL: refusing to run under xtrace (it would print TBA_PW)" >&2; exit 1;; esac
case ":${SHELLOPTS:-}:" in *:xtrace:*) echo "FATAL: SHELLOPTS has xtrace" >&2; exit 1;; esac
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 33-corpus-pilot
load_env
check_key_meta
preflight_op
. "$GA/rglib.sh"
NAME=pilot
HG=$HF/pilot
T5_DIR=$SRC/corpus/pilot/t5-2026-10-29      # the frozen source, WSL path
ART=$T5_DIR/artifacts
GUEST_ART=/opt/tbound/corpus/artifacts
T5_ARTIFACT_COUNT=76
PILOT_CEILINGS="--max-host-calls 17 --max-tokens 20000 --duration-seconds 1200"   # the same values tbound-op manifest-install passes
SESSION_OPEN=0
hf_has dr/CHECKS_OK && hf_has dr/RID || die "the dr cycle is not complete ($HF/dr/CHECKS_OK or dr/RID missing): finish 32-reg-dr.sh first"

# The flag is cleared only after a successful close, so the EXIT cleanup retries a failed close.
session_close() {
  [ "$SESSION_OPEN" = 1 ] || return 0
  if bash "$GA/00-session-sudo.sh" close; then SESSION_OPEN=0; else
    echo "FATAL: closing the session sudo window FAILED: broad passwordless sudo may still be enabled on the guest. Run: bash $GA/00-session-sudo.sh close (TBA_PW set), then check that 'sudo -n true' fails." >&2
    return 1
  fi
}

# ---- 1 abandon ---------------------------------------------------------------------------------------------------------------------------------
# req_journal_verdict <rid> <tag>: fetch the guest request journal into a new file of $HG and judge that request (sets REQ_VERDICT).
req_journal_verdict() {
  local f="pilot/requests-$2-$(date -u +%Y%m%dT%H%M%SZ).jsonl"
  op "collect requests"
  fetch_export "$OUT/_/serve-pi-requests.jsonl" "$f"
  REQ_JOURNAL_FILE=$f
  req_check "$HF/$f" "$1"
}
# fact <facts text> <key>: one `key=value` line of the facts `tbound-op abandon <rid>` prints.
fact() { printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -1; }
# catalog_drain_facts <rid>: request-specific drain evidence from the guest catalog (not the whole-catalog count of the abandon facts): the admission(s)
# reserved for this request id (event entry.request_id), and for that admission_hash the owner scopes bound, bound and drained, bound but not drained,
# and drained without a bound scope (the unexposed binder cleanup). Prints key=value lines.
catalog_drain_facts() {
  local f="pilot/catalog-$(date -u +%Y%m%dT%H%M%SZ).jsonl"
  op "collect catalog" >/dev/null
  fetch_export_nz "$OUT/_/catalog.jsonl" "$f" >/dev/null || { echo "admissions=0"; return 0; }   # empty catalog: no workflow was ever reserved
  WX python3 -I -c 'import base64,json,sys
rid=sys.argv[2]; res=set(); b={}; d={}
for l in open(sys.argv[1],encoding="utf-8"):
    if not l.strip(): continue
    ev=json.loads(l)["event"]; x=json.loads(base64.b64decode(ev["data"])); k=ev["kind"]; h=x.get("admission_hash")
    if k=="pi_workflow_admission_reserved" and x.get("entry",{}).get("request_id")==rid: res.add(h)
    elif k=="pi_workflow_owner_scope_bound": b.setdefault(h,set()).add((x.get("workflow_id"),x.get("owner_epoch")))
    elif k=="pi_workflow_owner_scope_drained": d.setdefault(h,set()).add((x.get("workflow_id"),x.get("owner_epoch")))
print("admissions=%d"%len(res))
if len(res)==1:
    h=next(iter(res)); B=b.get(h,set()); D=d.get(h,set())
    print("scopes_bound=%d"%len(B)); print("scopes_drained=%d"%len(B&D)); print("scopes_undrained=%d"%len(B-D)); print("drained_without_bound=%d"%len(D-B))' "$HF/$f" "$1" | tr -d '\r'
}
# abandon_stop <rid> <token>: the script does not settle the request on its own. Print the owner's two ways and stop (exit 1).
abandon_stop() {
  echo "ABANDON NOT DONE: the dr request $1 is still unsettled. The service is stopped, so tbound-op's cgroup and podman probes read 'unavailable' and cannot prove" >&2
  echo "'no pending effect'; the owner decides from the facts and the request-specific catalog evidence printed above. Either run the verb by hand:" >&2
  echo "  GUEST_IP=$GUEST_IP sh $GSSH_SH 'sudo -n /opt/tbound/operator/tbound-op abandon $1 --confirm $2'" >&2
  echo "or run this script again with ABANDON_CONFIRM=$2 (it then requires admissions==1, no undrained scope and service+socket inactive, confirms through the kit log," >&2
  echo "and saves the abandoned record). The TOKEN changes if the facts change; the guest reconciler refuses to abandon what it cannot prove quiescent." >&2
  exit 1
}

phase_abandon() {
  say "1. abandon the unsettled dr request (tbound-op two-step abandon)"
  local rid facts tok cf
  hf_has pilot/DR_SETTLED && { ok "dr request already settled by an earlier run ($(hf_get pilot/DR_SETTLED))"; return 0; }
  hf_has dr/RID || die "no $HF/dr/RID: 32-reg-dr.sh did not record the dr request id"
  rid=$(hf_get dr/RID); [[ $rid =~ ^dr-req-[0-9]{8}T[0-9]{6}Z$ ]] || die "$HF/dr/RID is not a dr request id: '$rid'"
  WX mkdir -p -m 0700 "$HG"
  req_journal_verdict "$rid" check
  case $REQ_VERDICT in
    completed | abandoned) ok "dr request $rid is settled ($REQ_VERDICT): nothing to abandon"; hf_put pilot/DR_SETTLED "$rid"; return 0;;
    unreconciled) ;;
    *) die "dr request $rid: journal verdict '$REQ_VERDICT' is not 'unreconciled' (terminal unknown, not abandoned): the abandon rule does not cover it, the owner decides";;
  esac
  facts=$(op "abandon $rid" 2>&1 | tr -d '\r') || die "tbound-op abandon $rid (facts) failed: $facts"
  printf '%s\n' "$facts"
  tok=$(printf '%s\n' "$facts" | sed -n 's/^TOKEN=\([0-9a-f]\{12\}\)$/\1/p' | head -1)
  [ -n "$tok" ] || die "no 'TOKEN=<12 hex>' line in the abandon output above"
  # No auto-confirm: with the service stopped the cgroup and podman probes are always 'unavailable' (a running dr service is not allowed here), so the facts can
  # never prove 'no pending effect'. The request-specific catalog evidence is printed next to them; the owner confirms (ABANDON_CONFIRM=<token> or by hand).
  cf=$(catalog_drain_facts "$rid"); printf 'request-specific catalog evidence:\n%s\n' "$cf"
  note "unexposed cleanup events in the whole catalog (informational): $(fact "$facts" catalog_unexposed_true)"
  [ -n "${ABANDON_CONFIRM:-}" ] || abandon_stop "$rid" "$tok"
  [[ $ABANDON_CONFIRM =~ ^[0-9a-f]{12}$ ]] || die "ABANDON_CONFIRM must be the 12 hex TOKEN printed above"
  [ "$ABANDON_CONFIRM" = "$tok" ] || die "ABANDON_CONFIRM $ABANDON_CONFIRM is not the current TOKEN $tok (the facts changed): read them again"
  [ "$(fact "$cf" admissions)" = 1 ] || die "the catalog holds $(fact "$cf" admissions) admission(s) for $rid, expected exactly 1: not confirming"
  [ "$(fact "$cf" scopes_undrained)" = 0 ] || die "owner scope(s) of this request are bound but not drained: not confirming"
  [[ $(rg_status) == *"service=inactive socket=inactive"* ]] || die "service and socket must both be inactive before the confirm (tbound-op status above): not confirming"
  ok "ABANDON_CONFIRM given, catalog evidence ok, service and socket inactive: confirming with TOKEN $tok"
  op "abandon $rid --confirm $tok"
  req_journal_verdict "$rid" abandoned
  [ "$REQ_VERDICT" = abandoned ] || die "after the confirm the journal verdict of $rid is '$REQ_VERDICT', expected 'abandoned'"
  # the abandoned record, with its reconciliation (provider_settlement included), as evidence
  WX python3 -I -c 'import base64,json,sys
for l in open(sys.argv[1],encoding="utf-8"):
    if not l.strip(): continue
    ev=json.loads(l)["event"]
    if ev["kind"]=="pi_service_request_abandoned":
        d=json.loads(base64.b64decode(ev["data"]))
        if d.get("request_id")==sys.argv[2]: print(json.dumps({"id":ev["id"],"data":d},indent=2,sort_keys=True))' "$HF/$REQ_JOURNAL_FILE" "$rid" | hf_save pilot/dr-abandoned-record.json
  hf_commit pilot/dr-abandoned-record.json
  hf_put pilot/DR_SETTLED "$rid"
}

# first_compliant_case <manifest or draft (WSL path)>: the case id of the first compliant case.
first_compliant_case() {
  local c
  c=$(WX python3 -I -c 'import json,sys
print(next(c["case_id"] for c in json.load(open(sys.argv[1],encoding="utf-8"))["cases"] if c["kind"]=="compliant"))' "$1" | tr -d '\r\n')
  [[ $c =~ ^[A-Za-z0-9._-]{1,128}$ ]] || die "not a usable case id: '$c'"
  printf '%s' "$c"
}

# ---- 1b keys (runbook Part K SSH-3, read-only probes of the forced-command keys) -----------------------------------------------------------------------
# M3 needs both keys (TBOUND_TRIAL_KEY, TBOUND_CLIENT_KEY: paths, passed to ssh by path, never read). Without them the phase only says so and continues.
phase_keys() {
  say "1b. keys: SSH-3 probes of the trial and client forced-command keys (read-only)"
  local out rc=0 c
  if [ -z "${TBOUND_TRIAL_KEY:-}" ] || [ -z "${TBOUND_CLIENT_KEY:-}" ]; then
    note "M3 needs TBOUND_TRIAL_KEY and TBOUND_CLIENT_KEY (paths of the forced-command keys): not both set, so the key probes are skipped. Set them and rerun to prove the keys before the long cycle."
  fi
  if [ -n "${TBOUND_TRIAL_KEY:-}" ]; then
    [ -f "$TBOUND_TRIAL_KEY" ] || die "TBOUND_TRIAL_KEY=$TBOUND_TRIAL_KEY is not a file"
    c=$(first_compliant_case "$T5_DIR/manifest.draft.json")
    out=$(trial "reset $c --check" 2>&1 | tr -d '\r') || rc=$?
    printf 'trial key, reset %s --check (rc %s): %s\n' "$c" "$rc" "$out"
    # before the corpus config: refused with "not a corpus-mode config" (the key reached `trial`); after the cycle: a tbound-trial-reset/v1 check document
    if [[ $out == *"not a corpus-mode config"* ]]; then ok "trial key reached tbound trial (pre-corpus refusal)"
    elif [ "$rc" = 0 ] && [[ $out == *tbound-trial-reset/v1* && $out == *'"check":true'* ]]; then ok "trial key: check document (corpus config already installed)"
    else die "trial key probe is neither the pre-corpus refusal nor a check document: Part K SSH-1/SSH-2 may be missing on the guest"; fi
  fi
  if [ -n "${TBOUND_CLIENT_KEY:-}" ]; then
    [ -f "$TBOUND_CLIENT_KEY" ] || die "TBOUND_CLIENT_KEY=$TBOUND_CLIENT_KEY is not a file"
    rc=0
    out=$(GSSH_IDENTITY=$TBOUND_CLIENT_KEY GSSH_USER=root sh "$GSSH_SH" id </dev/null 2>&1 | tr -d '\r') || rc=$?
    printf 'client key, id (rc %s): %s\n' "$rc" "$out"
    [ "$rc" = 2 ] && [[ $out == *"not a client command"* && $out != *uid=* ]] || die "client key probe: expected exit 2 and 'not a client command' without uid= (rc $rc)"
    ok "client key reached the forced command (refused id, exit 2)"
  fi
}

# ---- 2 manifest (D1) -----------------------------------------------------------------------------------------------------------------------------
phase_manifest() {
  say "2. manifest (D1, offline): T5 draft + four pins, corpus-digest"
  local draft=$T5_DIR/manifest.draft.json node out n
  hf_has d09-cell.base.json || die "no $HF/d09-cell.base.json (10-upgrade.sh)"
  WX test -f "$draft" || die "no $draft in the frozen source $SRC"
  n=$(WB "ls -A '$ART' | wc -l" | tr -d '\r\n'); eq "T5 artifact files in the frozen source" "$n" "$T5_ARTIFACT_COUNT"
  eq "NODE_DIGEST == PIN_NODE" "$NODE_DIGEST" "$PIN_NODE"
  node=${NODE_BIN:-}
  [ -n "$node" ] || node=$(WB '. ~/env.sh >/dev/null 2>&1; command -v node' | tr -d '\r\n' || true)
  [ -n "$node" ] || die "no node on the WSL side (~/env.sh gives none): set NODE_BIN=<Linux path of the pinned Node binary>"
  linux_safe "$node" || die "node path '$node' is not a plain Linux path outside /mnt"
  eq "WSL node digest == NODE_DIGEST (the pinned Node; corpus-digest runs the artifacts' node_test self-checks with it)" "$(wd "$node")" "$NODE_DIGEST"
  WX mkdir -p -m 0700 "$HG"
  if ! hf_has pilot/pins.json; then
    WX "$TBP" corpus-pins --template "$HF/d09-cell.base.json" --runtime-bundle-digest "$BUNDLE_DIGEST" --node-digest "$NODE_DIGEST" | tr -d '\r' | hf_save pilot/pins.json
    hf_commit pilot/pins.json
  fi
  if ! hf_has pilot/manifest.json; then   # the four pins replaced, pins.attacker_digest and everything else kept
    WX python3 -I -c 'import json,sys
m=json.load(open(sys.argv[1],encoding="utf-8"))
p=json.load(open(sys.argv[2],encoding="utf-8"))
for k in ("environment_digest","model_digest","profile_digest","policy_digest"):
    m["pins"][k]=p[k]
print(json.dumps(m,indent=2))' "$draft" "$HG/pins.json" | hf_save pilot/manifest.json
    hf_commit pilot/manifest.json
  fi
  out=$(WX "$TBP" corpus-pins --template "$HF/d09-cell.base.json" --runtime-bundle-digest "$BUNDLE_DIGEST" --node-digest "$NODE_DIGEST" --manifest "$HG/manifest.json" | tr -d '\r') ||
    die "corpus-pins --manifest refused $HG/manifest.json (output above)"
  eq "corpus-pins --manifest exits 0 and prints the recorded pins" "$out" "$(hf_get pilot/pins.json)"
  out=$(WB "'$TBP' corpus-digest --manifest '$HG/manifest.json' --quota-profile pilot --artifacts '$ART' --node '$node' $PILOT_CEILINGS 2>&1" | tr -d '\r') ||
    die "corpus-digest refused the manifest (the tool's violation list):"$'\n'"$out"
  isdig "$out" || die "corpus-digest printed more than one digest:"$'\n'"$out"
  hf_put pilot/CORPUS_DIGEST "$out"
  note "tbound-run plan was NOT cross-checked (needs a run.json); manifest-install recomputes the digest on the guest with the pinned tbound-profile"
}

# ---- 3 artifacts (P13.3) -------------------------------------------------------------------------------------------------------------------------
guest_artifact_sums() { G "cd $GUEST_ART 2>/dev/null && sha256sum -- * 2>/dev/null | LC_ALL=C sort -k2 || true" | tr -d '\r'; }
sum_names() { printf '%s\n' "$1" | awk 'NF{print $2}' | LC_ALL=C sort; }
# Complete entry set of the guest artifacts directory as "<type> <name>" lines (find: dotfiles, directories and links included, errors not hidden).
# `sha256sum -- *` above ignores hidden entries and hides directory errors, so equality needs both lists.
guest_artifact_entries() {
  local have
  have=$(G "if [ -d $GUEST_ART ]; then echo yes; else echo no; fi" | tr -d '\r')
  [ "$have" = no ] || G "find $GUEST_ART -mindepth 1 -maxdepth 1 -printf '%y %f\n'" | tr -d '\r' | LC_ALL=C sort
}
entry_names() { printf '%s\n' "$1" | awk 'NF{print substr($0, 3)}' | LC_ALL=C sort; }

# Owner and modes under /opt/tbound/corpus (also the pre-t5 sibling directories): root:root, directories 0755, files 0644. Readable without sudo.
artifact_meta_check() {
  local bad
  # stderr is merged and a find failure tolerated, so a permission error is reported below instead of dying silently
  bad=$(G 'find /opt/tbound/corpus \( ! -user root -o ! -group root -o \( -type d ! -perm 0755 \) -o \( -type f ! -perm 0644 \) \) -print 2>&1' | tr -d '\r' || true)
  [ -z "$bad" ] || die "owner or mode wrong under /opt/tbound/corpus, or find could not read it (want root:root, dirs 0755, files 0644):"$'\n'"$bad"
  ok "everything under /opt/tbound/corpus is root:root, directories 0755, files 0644"
}

phase_artifacts() {
  say "3. artifacts: the $T5_ARTIFACT_COUNT T5 files into $GUEST_ART"
  local own own_ent gst gst_ent extra name aside="" differ
  own=$(WB "cd '$ART' && sha256sum -- * | LC_ALL=C sort -k2" | tr -d '\r')
  own_ent=$(WB "cd '$ART' && find . -mindepth 1 -printf '%y %P\n' | LC_ALL=C sort" | tr -d '\r')
  eq "owner-side artifact files" "$(sum_names "$own" | grep -c .)" "$T5_ARTIFACT_COUNT"
  [ "$(printf '%s\n' "$own_ent" | grep -c '^f [^/]*$')" = "$T5_ARTIFACT_COUNT" ] && [ "$(printf '%s\n' "$own_ent" | grep -c .)" = "$T5_ARTIFACT_COUNT" ] ||
    die "the T5 source directory is not exactly $T5_ARTIFACT_COUNT flat regular files (find lists other entries or subdirectories)"
  gst=$(guest_artifact_sums); gst_ent=$(guest_artifact_entries)
  if [ "$gst_ent" = "$own_ent" ] && [ "$gst" = "$own" ]; then
    ok "guest artifacts == frozen source (complete entry set incl. dotfiles and directories, $T5_ARTIFACT_COUNT regular files, per-file sha256): no session sudo needed"
    artifact_meta_check
    return 0
  fi
  echo "guest holds $(printf '%s\n' "$gst_ent" | grep -c .) entries, the frozen source $T5_ARTIFACT_COUNT"
  extra=$(entry_names "$(LC_ALL=C comm -13 <(printf '%s\n' "$own_ent") <(printf '%s\n' "$gst_ent"))")   # every type, same-named entries of another type included
  # a T5-named file with another sha256 would be overwritten by tar: it is moved aside as well
  differ=$(sum_names "$(LC_ALL=C comm -13 <(printf '%s\n' "$own") <(printf '%s\n' "$gst"))")
  extra=$(printf '%s\n%s\n' "$extra" "$differ" | awk 'NF' | LC_ALL=C sort -u)
  # validate each whole line before any sudo: a name with a space must not split into two valid-looking names
  while IFS= read -r name; do [ -z "$name" ] || [[ $name =~ ^[A-Za-z0-9._-]+$ ]] || die "unusable entry name on the guest: '$name'"; done <<< "$extra"
  [ -n "${TBA_PW:-}" ] || die "TBA_PW is not set: the artifact install needs the session sudo window (read -rs TBA_PW; export TBA_PW)"
  cleanup_add session_close
  SESSION_OPEN=1
  bash "$GA/00-session-sudo.sh" open
  G "sudo -n install -d -o root -g root -m 0755 /opt/tbound/corpus $GUEST_ART"
  if [ -n "$extra" ]; then   # entries that are not in the T5 set or whose sha256 differs are moved (mv -n, never removed) into a dated sibling directory
    aside=/opt/tbound/corpus/artifacts.pre-t5-$(date -u +%Y%m%dT%H%M%SZ)
    echo "moving $(printf '%s\n' "$extra" | grep -c .) entrie(s) that are not in the T5 set or differ in sha256 to $aside:"; printf '%s\n' "$extra" | sed 's/^/   /'
    G "sudo -n install -d -o root -g root -m 0755 $aside && cd $GUEST_ART && sudo -n mv -n -- $(printf '%s ' $extra) $aside/"
    G "ls -A $aside" | tr -d '\r' | LC_ALL=C sort | diff - <(printf '%s\n' "$extra") || die "the moved entries in $aside differ from the list above"
  fi
  WX tar -C "$ART" -c . | GI "sudo -n tar -x --no-same-owner --no-same-permissions -C $GUEST_ART"
  # scoped to the artifacts directory and the aside directory (never the rest of /opt/tbound/corpus)
  G "sudo -n chown -R root:root $GUEST_ART $aside && sudo -n find $GUEST_ART $aside -type d -exec chmod 0755 {} + && sudo -n find $GUEST_ART $aside -type f -exec chmod 0644 {} +"
  session_close
  gst=$(guest_artifact_sums); gst_ent=$(guest_artifact_entries)
  [ "$gst_ent" = "$own_ent" ] || die "the guest artifact entry set differs from the frozen source after the install:"$'\n'"$(diff <(printf '%s\n' "$own_ent") <(printf '%s\n' "$gst_ent") || true)"
  [ "$gst" = "$own" ] || die "the guest artifacts differ from the frozen source after the install (count/sha256):"$'\n'"$(diff <(printf '%s\n' "$own") <(printf '%s\n' "$gst") || true)"
  artifact_meta_check
  ok "installed: $T5_ARTIFACT_COUNT files, complete entry set and per-file sha256 == frozen source; session sudo closed"
}

# ---- 4 cycle (D2 manifest-install, then RG for NAME=pilot) ----------------------------------------------------------------------------------------
hook_prior() {
  hf_has pilot/DR_SETTLED || die "the dr request is not recorded as settled ($HF/pilot/DR_SETTLED): phase 1 did not finish"
  hf_has pilot/CORPUS_DIGEST && hf_has pilot/manifest.json || die "no manifest/CORPUS_DIGEST in $HG: phase 2 did not finish"
}

hook_cfg_build() {
  WX python3 -I "$WGA/mk-svc-config.py" "$SRC/infra/guest/serve-pi-service.corpus-pilot.json" "$HG/serve-pi-service.json" "$CD" "$SD" "$CORPUS_DIGEST"
  eq "provider_route" "$(jgetw "$HG/serve-pi-service.json" provider_route)" engineering
  eq "workflow_admission_mode" "$(jgetw "$HG/serve-pi-service.json" workflow_admission_mode)" corpus
  eq "corpus_manifest_digest == CORPUS_DIGEST" "$(jgetw "$HG/serve-pi-service.json" corpus_manifest_digest)" "$CORPUS_DIGEST"
  eq "corpus_quota_profile" "$(jgetw "$HG/serve-pi-service.json" corpus_quota_profile)" pilot
  eq "workflow_bash_output_paths" "$(jgetw "$HG/serve-pi-service.json" workflow_bash_output_paths)" "[]"
  eq "workflow_max_host_calls/max_tokens/duration" "$(jgetw "$HG/serve-pi-service.json" workflow_max_host_calls)/$(jgetw "$HG/serve-pi-service.json" workflow_max_tokens)/$(jgetw "$HG/serve-pi-service.json" workflow_duration_seconds)" 17/20000/1200
}

# D6: corpus-pins on the template with the pilot registration must give the same four pins and accept the manifest (only the registration digest moved).
hook_after_s4() {
  eq "corpus-pins on the S4' template == recorded pins (--manifest exits 0)" \
    "$(WX "$TBP" corpus-pins --template "$HG/d09-cell.json" --runtime-bundle-digest "$BUNDLE_DIGEST" --node-digest "$NODE_DIGEST" --manifest "$HG/manifest.json" | tr -d '\r')" "$(hf_get pilot/pins.json)"
}

phase_cycle() {
  say "4. manifest-install (D2) and the RG cycle for NAME=pilot"
  local state rank mo l2 l2f
  hf_has pilot/CYCLE_OK && { ok "the pilot cycle is already complete ($HG/CYCLE_OK)"; return 0; }
  CORPUS_DIGEST=$(hf_get pilot/CORPUS_DIGEST); need_dig CORPUS_DIGEST "$CORPUS_DIGEST"
  state=$(rg_state); rank=$(st_rank_of "${state:-none}")
  echo "pilot state: ${state:-none} (rank $rank)"
  if [ "$rank" -lt 1 ]; then   # from state config on, cfg-install has already checked the installed manifest digest
    op "svc stop"
    stage_put stage-pilot "$HG/manifest.json" manifest.json
    mo=$(op "manifest-install $(wd "$HG/manifest.json") $CORPUS_DIGEST" 2>&1 | tr -d '\r') || die "manifest-install failed: $mo"
    printf '%s\n' "$mo"
    [[ $mo == *"manifest-install ok: $CORPUS_DIGEST"* ]] || die "manifest-install did not confirm $CORPUS_DIGEST"
  fi
  rg_cycle
  rg_archive_check
  eq "tbound-op state of pilot" "$(rg_state)" started
  # State `started` outlives a svc stop, and rglib tolerates a failed diag l2 for the pilot: check both here before CYCLE_OK.
  if [[ $(rg_status) != *"service=active socket=active"* ]]; then
    note "state is started but the service/socket are not active: svc start once"
    op "svc start"
    [[ $(rg_status) == *"service=active socket=active"* ]] || die "service/socket are not active after svc start (journal: $OUT/pilot/svc-start-journal.txt)"
  fi
  l2=$(op "diag l2" 2>&1 | tr -d '\r') || die "diag l2 failed: $l2"
  printf '%s\n' "$l2" | head -40
  l2_check "$l2" || die "diag l2 does not show the expected L2 state (see above; PROVISIONING-SIGNING.md L2): cells/runner cgroups, owner tbound-pi:tbound-pi 755, domain, controllers cpu memory pids, empty cells, six ro,nosuid,nodev mounts, lock-free"
  eq "live cgroup-root digest (diag l2) == recorded CGROUP_ROOT_DIGEST" "$L2_ROOT_DIGEST" "$(hf_get CGROUP_ROOT_DIGEST)"
  l2f="pilot/diag-l2-final-$(date -u +%Y%m%dT%H%M%SZ).txt"
  printf '%s\n' "$l2" | hf_save "$l2f"; hf_commit "$l2f"
  hf_put pilot/CYCLE_OK ok
}

# l2_check <diag l2 text>: the L2 block of PROVISIONING-SIGNING.md as tbound-op's `diag l2` prints it (the verb itself always exits 0), parsed line by line
# against the expected values, nothing else allowed: child cgroups exactly {cells, runner}; the cells cgroup owned by tbound-pi:tbound-pi mode 755; type
# domain; controllers `cpu memory pids`; no process in cells; exactly one runner pid; the cgroup-root digest the tool measures live; the six mounts (tbound,
# node, podman, crun, cell, runtime) each with the options ro, nosuid, nodev; the maintenance lock free. Sets L2_ROOT_DIGEST for the caller.
L2_ROOT_DIGEST=""
l2_fail() { echo "l2_check: line $((i + 1)) of the diag l2 text does not match: '${L[i]:-<end of text>}'" >&2; }
l2_check() {
  local -a L=(); local line i=0 n t opts dirs="" re_stat re_digest
  re_stat='^[^[:space:]]+:[^[:space:]]+ [0-7]+$'
  re_digest='^-- cgroup-root-digest \(profile: (sha256:[0-9a-f]{64})\)$'
  while IFS= read -r line; do L+=("${line%$'\r'}"); done <<<"$1"
  n=${#L[@]}
  while [ "$n" -gt 0 ] && [ -z "${L[n-1]}" ]; do n=$((n - 1)); done
  [ "${L[i]:-}" = "== service cgroup" ] || { l2_fail; return 1; }; i=$((i + 1))
  while [ "$i" -lt "$n" ] && ! [[ ${L[i]} =~ $re_stat ]]; do dirs="$dirs${L[i]}"$'\n'; i=$((i + 1)); done
  [ "$(printf '%s' "$dirs" | LC_ALL=C sort | tr '\n' ' ')" = "cells runner " ] || { l2_fail; return 1; }
  [ "${L[i]:-}" = "tbound-pi:tbound-pi 755" ] || { l2_fail; return 1; }; i=$((i + 1))
  [ "${L[i]:-}" = domain ] || { l2_fail; return 1; }; i=$((i + 1))
  [ "${L[i]:-}" = "cpu memory pids" ] || { l2_fail; return 1; }; i=$((i + 1))
  [ "${L[i]:-}" = "-- runner procs" ] || { l2_fail; return 1; }; i=$((i + 1))   # cells/cgroup.procs printed nothing: no process in cells
  [[ ${L[i]:-} =~ ^[0-9]+$ ]] || { l2_fail; return 1; }; i=$((i + 1))   # exactly one runner pid: the service main process
  [[ ${L[i]:-} =~ $re_digest ]] || { l2_fail; return 1; }; L2_ROOT_DIGEST=${BASH_REMATCH[1]}; i=$((i + 1))
  [ "${L[i]:-}" = "$L2_ROOT_DIGEST" ] || { l2_fail; return 1; }; i=$((i + 1))
  [ "${L[i]:-}" = "== mounts" ] || { l2_fail; return 1; }; i=$((i + 1))
  for t in /usr/local/bin/tbound /usr/local/bin/node /usr/bin/podman /usr/bin/crun /opt/tbound/cell /var/lib/tbound/runtime; do
    line=${L[i]:-}; [ "${line%% *}" = "$t" ] || { l2_fail; return 1; }
    opts=",${line#* },"
    [[ $opts == *,ro,* && $opts == *,nosuid,* && $opts == *,nodev,* ]] || { l2_fail; return 1; }
    i=$((i + 1))
  done
  [ "${L[i]:-}" = "== maintenance lock" ] || { l2_fail; return 1; }; i=$((i + 1))
  [ "${L[i]:-}" = lock-free ] || { l2_fail; return 1; }; i=$((i + 1))
  [ "$i" -eq "$n" ] || { l2_fail; return 1; }
}

# ---- 5 netsink (Spike B step 1, then the netsink producer) ---------------------------------------------------------------------------------------
trial() { GSSH_IDENTITY=$TBOUND_TRIAL_KEY GSSH_USER=root sh "$GSSH_SH" "/usr/local/bin/tbound trial $1" </dev/null; }   # the forced-command key, by path

phase_netsink() {
  say "5. netsink: trial reset through the trial key, then 40-guest-evidence.sh netsink"
  local case_id chk rs ts
  if [ -z "${TBOUND_TRIAL_KEY:-}" ]; then
    note "TBOUND_TRIAL_KEY is not set: stopping after the cycle. The owner next: KSSH <TRKEY> /usr/local/bin/tbound trial reset <first compliant case> --check, then the real reset"
    note "(service running), then KIT_FREEZE=$FREEZE bash 40-guest-evidence.sh netsink; or run this script again with TBOUND_TRIAL_KEY=<trial key path> (it resumes at phase 5)."
    return 0
  fi
  [ -f "$TBOUND_TRIAL_KEY" ] || die "TBOUND_TRIAL_KEY=$TBOUND_TRIAL_KEY is not a file"
  [[ $(rg_status) == *"service=active socket=active"* ]] || die "the pilot service is not running: the reset needs the cells cgroup (run phase 4 first)"
  case_id=$(first_compliant_case "$HG/manifest.json")
  # The net-sink table and attestation do not prove a finished reset (the confinement is set before wipe, populate and the digest check), so only the
  # success marker decides. An attempt marker without a success marker means an interrupted or failed reset: the owner recovers, nothing is retried.
  if hf_has pilot/TRIAL_RESET_OK; then
    ok "the trial reset already succeeded ($(hf_get pilot/TRIAL_RESET_OK)): no second reset"
  else
    hf_has pilot/TRIAL_RESET_ATTEMPT && die "a trial reset was attempted ($(hf_get pilot/TRIAL_RESET_ATTEMPT)) without a recorded success: it may have failed or been interrupted and a failed reset spends the unit. Owner recovery: read the guest state (diag nft-sink, ls -ln on the cells cgroup, the live root), do not retry blindly; after recovery record the outcome by hand in $HG/TRIAL_RESET_OK"
    chk=$(trial "reset $case_id --check" | tr -d '\r') || die "trial reset $case_id --check refused (nothing was reset): $chk"
    printf 'trial reset --check: %s\n' "$chk"
    ts=$(date -u +%Y%m%dT%H%M%SZ)
    hf_put pilot/TRIAL_RESET_ATTEMPT "$ts $case_id"
    rs=$(trial "reset $case_id" | tr -d '\r') || die "trial reset $case_id failed (the unit may be spent; do not retry blindly): $rs"
    printf 'trial reset: %s\n' "$rs"
    printf '%s\n' "$rs" | hf_save "pilot/trial-reset-$ts.json"; hf_commit "pilot/trial-reset-$ts.json"
    WX python3 -I -c 'import json,sys
d=json.load(open(sys.argv[1],encoding="utf-8"))
sys.exit(0 if d.get("schema")=="tbound-trial-reset/v1" and d.get("case_id")==sys.argv[2] and not d.get("check") and d.get("tree_digest") and d.get("tree_digest")==d.get("fixture_digest") else 1)' \
      "$HG/trial-reset-$ts.json" "$case_id" || die "the reset document in $HG/trial-reset-$ts.json is not a completed tbound-trial-reset/v1 for $case_id (check false, tree_digest == fixture_digest): the attempt marker stays, owner recovery"
    hf_put pilot/TRIAL_RESET_OK "$ts $case_id"
  fi
  bash "$GA/40-guest-evidence.sh" netsink
  hf_has evq/netsink.json || die "$HF/evq/netsink.json was not produced (SKIPPED or REFUSED: read the 40-guest-evidence.sh output above)"
  ok "netsink envelope fetched: $HF/evq/netsink.json"
}

phase_abandon;   phase_end abandon
phase_keys;      phase_end keys
phase_manifest;  phase_end manifest
phase_artifacts; phase_end artifacts
phase_cycle;     phase_end cycle
phase_netsink
note "the pilot service is left running. Close-out: sudo -n true must still fail. Next (owner): rerun tbound-run evq with the new netsink.json (report 01 step 12b)."
note "NOT PROVEN: a pilot request admission (corpus mode with a real request, the Go admission path). M3 should start with ONE run with --max-steps 1 before the full pilot."
done_step
