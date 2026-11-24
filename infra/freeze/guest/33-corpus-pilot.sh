#!/bin/bash
# 33 Corpus mode on the guest (Part D, NAME=pilot) at $FREEZE: PREPARED, NOT RUN. Automates report 01 section 3 (steps 1-12) and PROVISIONING-SIGNING.md Part D / Spike B.
# Every guest write is a tbound-op verb (narrowed sudo, the session drop-in CLOSED when the script starts), except the artifact install, which has no verb
# (runbook P13.3) and runs inside the session sudo window (00-session-sudo.sh open ... close; TBA_PW; closed again even on failure).
#   0 carrier    only with CARRIER_DIR, CARRIER_COMMIT and CARRIER_MANIFEST_DIGEST (a screening round; all three or none): the round's carrier
#                <Linux checkout>/corpus/sampling/carriers/<round> replaces the T5 draft and artifacts of $SRC; the runtime stays $FREEZE. Before any guest write
#                carrier-check.sh verifies the checkout (plain path outside /mnt, no symlink, HEAD == CARRIER_COMMIT, every file == its blob at that commit, no
#                extra or missing file, sha256(manifest.draft.json) == CARRIER_MANIFEST_DIGEST), copies it to $HF/pilot/carrier and verifies the copy, which
#                phases 2 and 3 then read. $HF/pilot/CARRIER records the round; a $HF/pilot of another round or of the T5 run is refused (move it aside),
#                never reused, so no manifest, digest or signed file leaks between rounds. A carrier round skips phase 5 (the $FREEZE netsink evidence stands).
#   1 abandon    the unsettled dr request (rid = $HF/dr/RID). Already abandoned/completed (reqstate verdict): continue. Otherwise the script settles NOTHING by
#                itself: it prints tbound-op's abandon facts + TOKEN and the request-specific catalog evidence (admissions, scopes undrained, drained) and stops
#                with the owner's instruction (the verb by hand, or rerun with ABANDON_CONFIRM=<token>). The facts cannot prove "no pending effect" while the
#                service is stopped (cgroup/podman probes read `unavailable`), hence no auto-confirm. With ABANDON_CONFIRM=<token> it requires admissions==1,
#                no undrained scope and service+socket inactive, confirms through `op` (kit log), re-reads the journal (verdict abandoned) and saves the
#                abandoned record (provider_settlement included) as $HF/pilot/dr-abandoned-record.json.
#   1b keys      with TBOUND_TRIAL_KEY and TBOUND_CLIENT_KEY (paths): read-only SSH-3 probes (trial `reset <case> --check`: the pre-corpus refusal or a check
#                document; client `id`: exit 2, "not a client command"). Without them: says that M3 needs them and continues.
#   2 manifest   D1, offline on the WSL side: the T5 draft of $SRC + the four pins of `tbound-profile corpus-pins` (template $HF/d09-cell.base.json) ->
#                $HF/pilot/manifest.json; `corpus-pins --manifest` must pass; `corpus-digest` with the 76 T5 artifacts and the ceilings 17 / 69632 / 1200 ->
#                $HF/pilot/CORPUS_DIGEST. Any refusal prints the tool's violation list and stops.
#   3 artifacts  the 76 files of corpus/pilot/t5-2026-10-29/artifacts of $SRC into /opt/tbound/corpus/artifacts (session sudo window); afterwards count, per-file
#                sha256, owner and modes are verified. Guest files not in the T5 set are moved (mv -n) to artifacts.pre-t5-<UTC>.
#   3b pilot-reset  (a carrier round runs it before phase 3) only if the guest holds a pilot state that is not from this freeze (cfg differs from $HG/serve-pi-service.json, or the signed files of its rank are not in
#                $HG): svc stop, `rollback pilot cfg` (its refusal stops the script), then in the session sudo window mv -n /root/ga/reg-pilot, /root/ga/pilot and
#                /var/tmp/tbound-op/pilot into /root/ga/archive-pilot-<prev freeze>-<UTC>/rootfs/<same path> (never deleted); needs TBA_PW; marker RESET_PENDING/RESET_DONE in $HG.
#   4 cycle      D2 `manifest-install`, then the RG cycle of rglib.sh for NAME=pilot: svc stop, cfg-install (config = infra/guest/serve-pi-service.corpus-pilot.json
#                with CORPUS_DIGEST and the two measured podman digests, mk-svc-config.py), reg-measure, S3' (sign-run.sh pilot s3), reg-install, S4' (sign-run.sh
#                pilot s4; corpus-pins on the new template must give the same pins), prof-install, svc start, diag l2. No client run: corpus mode refuses client
#                text. Then the archive check. The service is left RUNNING (the netsink step needs it);
#                before CYCLE_OK the service must be active and diag l2 must pass l2_check (one svc start if state started but stopped).
#   5 netsink    only with TBOUND_TRIAL_KEY=<path of the trial forced-command key> (passed to ssh by path, never read): `trial reset <first compliant case> --check`,
#                one real `trial reset` (attempt marker before, success marker after a valid
#                tbound-trial-reset/v1 document; an attempt without success stops for owner recovery, never retried), then 40-guest-evidence.sh netsink. Without the key the script ends after phase 4 and prints what the owner does.
# Resumable (phases 1-3 re-check their result; phase 4 resumes from `tbound-op status pilot`; signed files in $HF/pilot are verified, never re-signed).
# STOP_AFTER=<carrier|abandon|keys|manifest|artifacts|pilot-reset|cfg|measure|s3|reg|s4|prof|start|cycle>.
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
PILOT_CEILINGS="--max-host-calls 17 --max-tokens 69632 --duration-seconds 1200"   # the same values tbound-op manifest-install passes
SESSION_OPEN=0
CARRIER_ROUND="" CARRIER_REC=""
if [ -n "${CARRIER_DIR:-}${CARRIER_COMMIT:-}${CARRIER_MANIFEST_DIGEST:-}" ]; then
  [ -n "${CARRIER_DIR:-}" ] && [ -n "${CARRIER_COMMIT:-}" ] && [ -n "${CARRIER_MANIFEST_DIGEST:-}" ] || die "CARRIER_DIR, CARRIER_COMMIT and CARRIER_MANIFEST_DIGEST go together: set all three or none"
  CARRIER_ROUND=${CARRIER_DIR##*/}
  CARRIER_REPO=${CARRIER_DIR%/corpus/sampling/carriers/"$CARRIER_ROUND"}
  linux_safe "$CARRIER_DIR" && [[ $CARRIER_ROUND =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] && [ -n "$CARRIER_REPO" ] && [ "$CARRIER_REPO" != "$CARRIER_DIR" ] ||
    die "CARRIER_DIR=$CARRIER_DIR is not a plain Linux path <checkout>/corpus/sampling/carriers/<round> outside /mnt"
  [ ${#WXP[@]} -gt 0 ] || not_mnt "$CARRIER_DIR" || die "CARRIER_DIR=$CARRIER_DIR resolves into /mnt"
  [ "$(WX realpath -e -- "$CARRIER_DIR" | tr -d '\r\n')" = "$CARRIER_DIR" ] || die "CARRIER_DIR=$CARRIER_DIR does not exist or passes a symlink (its realpath differs)"
  [[ $CARRIER_COMMIT =~ ^[0-9a-f]{40}$ ]] || die "CARRIER_COMMIT is not a 40-hex lowercase commit: '$CARRIER_COMMIT'"
  need_dig CARRIER_MANIFEST_DIGEST "$CARRIER_MANIFEST_DIGEST"
  CARRIER_REC="$CARRIER_ROUND $CARRIER_COMMIT $CARRIER_MANIFEST_DIGEST"
  T5_DIR=$HG/carrier                        # the verified owner-side copy (phase 0), never the checkout
  ART=$T5_DIR/artifacts
fi
hf_has dr/CHECKS_OK && hf_has dr/RID || die "the dr cycle is not complete ($HF/dr/CHECKS_OK or dr/RID missing): finish 32-reg-dr.sh first"

# The flag is cleared only after a successful close, so the EXIT cleanup retries a failed close.
session_close() {
  [ "$SESSION_OPEN" = 1 ] || return 0
  if bash "$GA/00-session-sudo.sh" close; then SESSION_OPEN=0; else
    echo "FATAL: closing the session sudo window FAILED: broad passwordless sudo may still be enabled on the guest. Run: bash $GA/00-session-sudo.sh close (TBA_PW set), then check that 'sudo -n true' fails." >&2
    return 1
  fi
}

# ---- 0 carrier (screening round) -----------------------------------------------------------------------------------------------------------------
# aside_cmd <label>: the WSL command that moves $HG aside (renamed inside $HF, nothing deleted).
aside_cmd() { printf 'mv -T -n -- %s %s/pilot.%s-%s' "$HG" "$HF" "$1" "$(date -u +%Y%m%dT%H%M%SZ)"; }
carrier_check() { WX bash "$WGA/carrier-check.sh" "$CARRIER_REPO" "$CARRIER_COMMIT" "$CARRIER_ROUND" "$@" | tr -d '\r'; }
phase_carrier() {
  local rec="" tmp
  hf_has pilot/CARRIER && rec=$(hf_get pilot/CARRIER)
  if [ -z "$CARRIER_REC" ]; then
    [ -z "$rec" ] || die "$HG holds the carrier round '$rec' and no carrier is set: move it aside before the T5 run (in WSL: $(aside_cmd "${rec%% *}"))"
    return 0
  fi
  say "0. carrier: $CARRIER_REC"
  [ -n "$rec" ] || ! WX test -d "$HG" || [ -z "$(WX ls -A "$HG" | tr -d '\r')" ] ||
    die "$HG holds an earlier pilot run without a carrier record (the T5 run): move it aside first (in WSL: $(aside_cmd t5))"
  [ -z "$rec" ] || [ "$rec" = "$CARRIER_REC" ] || die "$HG holds the carrier round '$rec', not '$CARRIER_REC': move it aside first (in WSL: $(aside_cmd "${rec%% *}"))"
  if hf_has pilot/carrier; then   # a resumed round: the copy is checked against the same commit and digest; the checkout is not read again
    carrier_check "$T5_DIR" "$CARRIER_MANIFEST_DIGEST" || die "$T5_DIR (the copy of an earlier run of this round) failed verification: owner looks"
  else
    carrier_check "$CARRIER_DIR" "$CARRIER_MANIFEST_DIGEST" --checkout || die "the carrier checkout $CARRIER_DIR failed verification (output above): nothing was written"
    WX mkdir -p -m 0700 "$HG"
    hf_put pilot/CARRIER "$CARRIER_REC"
    tmp=$(WX mktemp -d "$HG/carrier.partial.XXXXXX" | tr -d '\r\n')
    WX cp -R -- "$CARRIER_DIR/." "$tmp/"
    carrier_check "$tmp" "$CARRIER_MANIFEST_DIGEST" || die "the copy $tmp failed verification (the checkout changed while it was copied?)"
    WX mv -T -- "$tmp" "$T5_DIR"; WX sync
  fi
  T5_ARTIFACT_COUNT=$(WB "find '$ART' -mindepth 1 -maxdepth 1 -type f | wc -l" | tr -d '\r\n')
  ok "carrier $CARRIER_ROUND verified at $CARRIER_COMMIT and copied to $T5_DIR ($T5_ARTIFACT_COUNT artifacts): phases 2 and 3 read only that copy"
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
  [[ $c =~ ^[A-Za-z0-9._:-]{1,128}$ ]] || die "not a usable case id: '$c'"   # ':' for ledger ids (agentdojo:slack:UserTask0:InjectionTask1)
  printf '%s' "$c"
}

# ---- 1b keys (runbook Part K SSH-3, read-only probes of the forced-command keys) -----------------------------------------------------------------------
# M3 needs the trial, client and evidence keys (TBOUND_TRIAL_KEY, TBOUND_CLIENT_KEY, TBOUND_EVIDENCE_KEY: paths, passed to ssh by path, never read). Only the
# restriction probes (`id`) run here, so the phase works at any cycle state; the trial `reset --check` needs the running corpus service and runs at the end of
# phase 4. Without the keys the phase only says so and continues.
# key_id_probe <label> <key path> <ERE of the accepted refusal text>: `id` through a forced-command key must exit 2, print the refusal and no uid=.
key_id_probe() {
  local out rc=0
  [ -f "$2" ] || die "$1 key $2 is not a file"
  out=$(GSSH_IDENTITY=$2 GSSH_USER=root sh "$GSSH_SH" id </dev/null 2>&1 | tr -d '\r') || rc=$?
  printf '%s key, id (rc %s): %s\n' "$1" "$rc" "$out"
  [ "$rc" = 2 ] && [[ $out =~ $3 && $out != *uid=* ]] || die "$1 key probe: expected exit 2, the forced-command refusal and no uid= (rc $rc): the key may be unrestricted or not installed"
  ok "$1 key is restricted (id refused, exit 2)"
}
phase_keys() {
  say "1b. keys: SSH-3 restriction probes of the trial, client and evidence keys (read-only)"
  if [ -z "${TBOUND_TRIAL_KEY:-}" ] || [ -z "${TBOUND_CLIENT_KEY:-}" ] || [ -z "${TBOUND_EVIDENCE_KEY:-}" ]; then
    note "M3 needs TBOUND_TRIAL_KEY, TBOUND_CLIENT_KEY and TBOUND_EVIDENCE_KEY (key paths): not all set, so the missing probes are skipped. Set them and rerun to prove the keys."
  fi
  # live wording: evidence "SSH_ORIGINAL_COMMAND is not a evidence command" (trial assumed analogous); the client key printed its usage line, exit 2
  [ -z "${TBOUND_TRIAL_KEY:-}" ] || key_id_probe trial "$TBOUND_TRIAL_KEY" 'not an? trial command'
  [ -z "${TBOUND_EVIDENCE_KEY:-}" ] || key_id_probe evidence "$TBOUND_EVIDENCE_KEY" 'not an? evidence command'
  [ -z "${TBOUND_CLIENT_KEY:-}" ] || key_id_probe client "$TBOUND_CLIENT_KEY" 'not an? client command|usage: tbound client-forced'
}

# trial_check_doc: with the corpus service running, `trial reset <case> --check` through the trial key must print a check document (read-only).
trial_check_doc() {
  local c out rc=0
  c=$(first_compliant_case "$HG/manifest.json")
  out=$(trial "reset $c --check" | tr -d '\r') || rc=$?
  printf 'trial key, reset %s --check (rc %s): %s\n' "$c" "$rc" "$out"
  [ "$rc" = 0 ] && [[ $out == *tbound-trial-reset/v1* && $out == *'"check":true'* ]] || die "trial reset --check did not print a check document with the corpus service running (rc $rc)"
  ok "trial key: check document for $c"
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
# sha256 of every regular file (dotfiles included) as "<hash>  <name>", whole-line sorted (comm needs that). An unreadable file makes sha256sum, find and so this
# function fail: nothing is swallowed. A missing directory gives an empty list.
guest_artifact_sums() {
  local have
  have=$(G "if [ -d $GUEST_ART ]; then echo yes; else echo no; fi" | tr -d '\r')
  [ "$have" = no ] || G "find $GUEST_ART -mindepth 1 -maxdepth 1 -type f -exec sha256sum -- {} +" | tr -d '\r' | sed "s|  $GUEST_ART/|  |" | LC_ALL=C sort
}
# check_sums_complete <entries> <sums>: every regular file entry has exactly one checksum line (an unreadable T5-named file must not slip through).
check_sums_complete() {
  [ "$(printf '%s\n' "$1" | awk '$1=="f"{print substr($0, 3)}' | LC_ALL=C sort)" = "$(sum_names "$2")" ] ||
    die "the checksum list does not cover exactly the regular files of $GUEST_ART (unreadable or odd entries): nothing is extracted"
}
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
  own=$(WB "set -o pipefail; cd '$ART' && sha256sum -- * | LC_ALL=C sort" | tr -d '\r')
  own_ent=$(WB "cd '$ART' && find . -mindepth 1 -printf '%y %P\n' | LC_ALL=C sort" | tr -d '\r')
  eq "owner-side artifact files" "$(sum_names "$own" | grep -c .)" "$T5_ARTIFACT_COUNT"
  [ "$(printf '%s\n' "$own_ent" | grep -c '^f [^/]*$')" = "$T5_ARTIFACT_COUNT" ] && [ "$(printf '%s\n' "$own_ent" | grep -c .)" = "$T5_ARTIFACT_COUNT" ] ||
    die "the T5 source directory is not exactly $T5_ARTIFACT_COUNT flat regular files (find lists other entries or subdirectories)"
  gst=$(guest_artifact_sums); gst_ent=$(guest_artifact_entries); check_sums_complete "$gst_ent" "$gst"
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
  gst=$(guest_artifact_sums); gst_ent=$(guest_artifact_entries); check_sums_complete "$gst_ent" "$gst"
  [ "$gst_ent" = "$own_ent" ] || die "the guest artifact entry set differs from the frozen source after the install:"$'\n'"$(diff <(printf '%s\n' "$own_ent") <(printf '%s\n' "$gst_ent") || true)"
  [ "$gst" = "$own" ] || die "the guest artifacts differ from the frozen source after the install (count/sha256):"$'\n'"$(diff <(printf '%s\n' "$own") <(printf '%s\n' "$gst") || true)"
  artifact_meta_check
  ok "installed: $T5_ARTIFACT_COUNT files, complete entry set and per-file sha256 == frozen source; session sudo closed"
}

# ---- 3b pilot-reset (a pilot install left by an earlier freeze) ------------------------------------------------------------------------------------
# 27 archives the pilot items too, but a guest that was not archived (or an install of an earlier freeze) must not be resumed: its signed files are not in $HG.
# "Ours" = the installed pilot state's cfg equals the config this freeze built ($HG/serve-pi-service.json) and, from state reg / prof on, the signed
# registration / profile of this freeze are in $HG. Anything else is foreign. The archive name is recorded (RESET_PENDING) before anything is changed, so a rerun resumes.
PILOT_ITEMS="root/ga/reg-pilot root/ga/pilot var/tmp/tbound-op/pilot"   # moved to <archive>/rootfs/<same path>; nothing is deleted
pilot_state_is_ours() {   # $1 = status text with a state
  local cfg here state
  cfg=$(printf '%s\n' "$1" | sed -n 's/^ *cfg=\(.*\)$/\1/p' | head -1); state=$(printf '%s\n' "$1" | sed -n 's/^ *state=\(.*\)$/\1/p' | head -1)
  hf_has pilot/serve-pi-service.json || return 1
  here=$(wd "$HG/serve-pi-service.json")
  [ -n "$cfg" ] && [ "${cfg#sha256:}" = "${here#sha256:}" ] || return 1
  [ "$(st_rank_of "$state")" -lt 2 ] || hf_has pilot/managed-live-root.json || return 1
  [ "$(st_rank_of "$state")" -lt 3 ] || hf_has pilot/pi-host-profile.signed.json || return 1
}
phase_pilot_reset() {
  say "3b. pilot-reset: clear a pilot install that belongs to an earlier freeze (svc stop, rollback pilot cfg, archive the leftovers)"
  local s arch it d present="" ro
  hf_has pilot/RESET_DONE && { ok "pilot reset already done ($(hf_get pilot/RESET_DONE))"; return 0; }
  WX mkdir -p -m 0700 "$HG"
  if hf_has pilot/RESET_PENDING; then
    arch=$(hf_get pilot/RESET_PENDING); note "resuming the pilot reset, archive $arch"
  else
    s=$(rg_status); printf '%s\n' "$s"
    [ -n "$(rg_state)" ] || { ok "no pilot state on the guest: nothing to reset (leftover reg-pilot/pilot dirs without a state are not visible without sudo; reg-measure would refuse them)"; return 0; }
    pilot_state_is_ours "$s" && { ok "the pilot state belongs to $FREEZE (cfg and signed files match $HG): resumed by the cycle"; return 0; }
    [ -n "${TBA_PW:-}" ] || die "the installed pilot state is not from $FREEZE and must be reset, which needs the session sudo window (read -rs TBA_PW; export TBA_PW)"
    arch=/root/ga/archive-pilot-$([ -n "$CARRIER_ROUND" ] && echo "$FREEZE_LC" || echo "$PREV_LC")-$(date -u +%Y%m%dT%H%M%SZ)   # a carrier round archives an earlier round of $FREEZE
    hf_put pilot/RESET_PENDING "$arch"
  fi
  [[ $arch =~ ^/root/ga/archive-pilot-f[0-9]+-[0-9]{8}T[0-9]{6}Z$ ]] || die "bad archive name '$arch' in $HG/RESET_PENDING"
  if [ -n "$(rg_state)" ]; then
    op "svc stop"
    ro=$(op "rollback pilot cfg" 2>&1 | tr -d '\r') || die "rollback pilot cfg refused: $ro"
    printf '%s\n' "$ro"
  fi
  [[ $(rg_status) == *"-- pilot: no state"* ]] || die "the pilot state is still present after the rollback (tbound-op status pilot)"
  [ -n "${TBA_PW:-}" ] || die "TBA_PW is not set: moving the pilot leftovers needs the session sudo window (read -rs TBA_PW; export TBA_PW), then rerun (resumes)"
  cleanup_add session_close
  SESSION_OPEN=1
  bash "$GA/00-session-sudo.sh" open
  G "sudo -n install -d -o root -g root -m 0700 $arch $arch/rootfs"
  for it in $PILOT_ITEMS; do
    [ "$(G "if sudo -n test -e /$it || sudo -n test -L /$it; then echo yes; else echo no; fi" | tr -d '\r')" = yes ] || continue
    d=$arch/rootfs/$(dirname "$it")
    G "sudo -n install -d -o root -g root -m 0700 $d && sudo -n mv -n -- /$it $d/"
    present="$present /$it"
  done
  echo "moved into $arch/rootfs:${present:- (nothing was present)}"
  for it in $PILOT_ITEMS; do   # verify: no source left; every moved item is in the archive
    [ "$(G "if sudo -n test -e /$it || sudo -n test -L /$it; then echo yes; else echo no; fi" | tr -d '\r')" = no ] || die "/$it is still on the guest after the move (an item of that name already exists in $arch/rootfs? owner looks)"
  done
  for it in $present; do G "sudo -n ls -ldn -- $arch/rootfs$it"; done
  session_close
  [[ $(rg_status) == *"-- pilot: no state"* ]] || die "tbound-op status pilot still shows a pilot state"
  hf_put pilot/RESET_DONE "$arch"
  ok "pilot reset: state cleared, leftovers archived in $arch/rootfs, session sudo closed"
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
  eq "workflow_max_host_calls/max_tokens/duration" "$(jgetw "$HG/serve-pi-service.json" workflow_max_host_calls)/$(jgetw "$HG/serve-pi-service.json" workflow_max_tokens)/$(jgetw "$HG/serve-pi-service.json" workflow_duration_seconds)" 17/69632/1200
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
  if [ -n "${TBOUND_TRIAL_KEY:-}" ]; then trial_check_doc; else note "TBOUND_TRIAL_KEY not set: trial reset --check not probed"; fi
  hf_put pilot/CYCLE_OK ok
}

# ---- 5 netsink (Spike B step 1, then the netsink producer) ---------------------------------------------------------------------------------------
trial() { GSSH_IDENTITY=$TBOUND_TRIAL_KEY GSSH_USER=root sh "$GSSH_SH" "/usr/local/bin/tbound trial $1" </dev/null; }   # the forced-command key, by path

phase_netsink() {
  say "5. netsink: trial reset through the trial key, then 40-guest-evidence.sh netsink"
  local case_id chk rs ts
  if [ -n "$CARRIER_ROUND" ]; then
    note "carrier round $CARRIER_ROUND: no trial reset and no netsink here (the $FREEZE netsink evidence stands; the runner resets each case itself)"
    return 0
  fi
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

phase_carrier;   phase_end carrier
phase_abandon;   phase_end abandon
phase_keys;      phase_end keys
phase_manifest;  phase_end manifest
# A carrier round resets first: the previous round's service still runs and reads the guest artifacts live, so they change only after it is stopped.
[ -z "$CARRIER_ROUND" ] || { phase_pilot_reset; phase_end pilot-reset; }
phase_artifacts; phase_end artifacts
[ -n "$CARRIER_ROUND" ] || { phase_pilot_reset; phase_end pilot-reset; }
phase_cycle;     phase_end cycle
phase_netsink
note "the pilot service is left running. Close-out: sudo -n true must still fail.$([ -n "$CARRIER_ROUND" ] || echo " Next (owner): rerun tbound-run evq with the new netsink.json (report 01 step 12b).")"
[ -z "$CARRIER_ROUND" ] || note "carrier round $CARRIER_ROUND: run config manifest = host copy of $HG/manifest.json, artifacts_dir = host copy of $ART, corpus digest $(hf_get pilot/CORPUS_DIGEST)"
note "NOT PROVEN: a pilot request admission (corpus mode with a real request, the Go admission path). M3 should start with ONE run with --max-steps 1 before the full pilot."
done_step
