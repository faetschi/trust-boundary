#!/bin/bash
# 40 Guest EVQ producers at $FREEZE, entirely through tbound-op verbs (narrowed sudo; the session drop-in must be CLOSED). PREPARED, NOT RUN.
# Stages what the producers need into /root/ga (stage-ga: tbound-profile, run.json, the two test binaries), runs `tbound-op guest-evidence <producer>`
# and fetches each output into $HF/evq/ (the file names the host side passes to `tbound-run evq/route-report --artifact kind=file`).
#
# Usage: bash 40-guest-evidence.sh KIND [KIND ...]      (KINDs run in order; existing outputs are reused only after their commit equals FREEZE_COMMIT)
#   status               what is staged/fetched, service state (no change)
#   provider-exchange    guest-provider-exchange        out provider.json            needs: 30 done (g1 response in /root/ga/out, /root/ga/g1)
#   capability           guest-capability               out capability.json          needs: the service RUNNING (cgroup /system.slice/tbound-pi.service)
#   procoracle           guest-tests-procoracle         out procoracle.json          needs: service RUNNING, TEST_PROCORACLE binary staged
#   landlock-connect     guest-tests-landlock-connect   out landlock-connect.json    needs: service RUNNING, TEST_ENTRYPOINT binary staged
#   netsink              guest-netsink                  out netsink.json             needs: /run/tbound/net-sink.attest + the loaded rule (see BLOCKER below)
#   route-bash-view      guest-route-bash-view          out route-bash-view.json     needs: 31 done ($HF/rp/RUNID)
#   route-direct-runtime guest-route-direct-runtime     out route-direct-runtime.json needs: 32 done ($HF/dr/RUNID)
# Suggested order (PROVISIONING-SIGNING.md producer table): the service must run for capability/procoracle/landlock-connect, 32 stops it, so
#   after 30:   40 capability procoracle landlock-connect     (the g1 service must be running; provider-exchange needs the g1 response and /root/ga/g1 and may be added to this call)
#   after 31:   40 route-bash-view
#   after 32:   40 route-direct-runtime     (then abandon the dr request if it ended unknown: see ..\README.md "Guest order")
#   netsink:    deferred (BLOCKER below); 40 netsink SKIPS with a message while the attestation is absent.
# BLOCKER netsink: the attestation /run/tbound/net-sink.attest and the loaded nft table are made by `tbound trial reset <case>` (Spike B step 1), which the trial
#   key runs and which the runbook says is refused under every non-corpus config ("the service config is not a corpus-mode config"). No tbound-op verb does a
#   trial reset, so netsink (and a meaningful landlock-connect, which also needs the net-sink cgroup rules) can only be produced after Part D (pilot cycle, D8).
#   This script therefore tries them but treats an absent attestation as SKIPPED and a refused producer as REFUSED (nothing written), never as success.
# Staging method (BL-6): owner file -> ~tboundadmin/stage-ga/<fixed name> (sha256 compared on both sides) -> `tbound-op stage-ga <kind> <sha>`, which re-hashes a
#   root-only copy, compares with the pin (TBP_DIGEST, TEST_PROCORACLE, TEST_ENTRYPOINT from pins.env) or regenerates run.json from the pins and cmp's, and installs
#   root:root into /root/ga (0500 binaries, 0444 run.json).
# Owner-side sources (from env.sh / 35-evq-capture.sh): $W/bin/tbound-profile; $CF/guest-tests/procoracle.test and tbound-cell-entrypoint.test (their sha256 must equal
#   $HF/TEST_PROCORACLE / TEST_ENTRYPOINT, i.e. the capture's test_binaries). run.json is generated here from the $FREEZE pins (no file read).
# Exit status: 1 if any requested producer was REFUSED or a precondition of staging failed; SKIPPED does not fail the script.
set -euo pipefail
. "$(dirname -- "${BASH_SOURCE[0]}")/glib.sh"
logto 40-guest-evidence
load_env
preflight_op
OUT=/var/tmp/tbound-op
EV=evq
[ "$#" -ge 1 ] || die "usage: bash 40-guest-evidence.sh status|provider-exchange|capability|procoracle|landlock-connect|netsink|route-bash-view|route-direct-runtime|v-stub ..."
WX mkdir -p -m 0700 "$HF/$EV"
BASE_TD=$(hf_get BASE_TD); need_dig BASE_TD "$BASE_TD"
CAP_DIR=$CF/guest-tests          # WSL path, written by 35-evq-capture.sh (guest-tests/{procoracle,tbound-cell-entrypoint}.test)

# ---- staging -------------------------------------------------------------------------------------------------------
# ga_stage <stage-ga kind> <fixed file name> <WSL source> <expected digest>
ga_stage() {
  local kind=$1 fn=$2 src=$3 want=$4 got o
  if hf_has "$EV/staged.$fn" && [ "$(hf_get "$EV/staged.$fn")" = "$want" ]; then ok "$fn already staged to /root/ga ($want)"; return 0; fi
  got=$(wd "$src"); eq "$fn owner file == expected digest" "$got" "$want"
  stage_put stage-ga "$src" "$fn"
  o=$(op "stage-ga $kind $want" 2>&1 | tr -d '\r') || true
  printf '%s\n' "$o"
  [[ $o == *"stage-ga $kind ok: $want "* ]] || die "stage-ga $kind did not confirm $want"
  hf_put "$EV/staged.$fn" "$want"
}
ga_stage_runjson() {
  local rj sha want
  rj=$(printf '{"freeze_commit":"%s","v_image_digest":"%s","pins":{"frozen_source_digest":"%s","base_template_digest":"%s","admin_pubkey_digest":"%s","runtime_bundle_digest":"%s","runner_digest":"%s"}}' \
    "$F" "$IMGDIGEST" "$SRC_DIGEST" "$BASE_TD" "$ADMIN_PUB_DIGEST" "$BUNDLE_DIGEST" "$TBOUND_DIGEST")
  want="sha256:$(printf '%s\n' "$rj" | sha256sum | cut -d' ' -f1)"
  if hf_has "$EV/staged.run.json" && [ "$(hf_get "$EV/staged.run.json")" = "$want" ]; then ok "run.json already staged to /root/ga ($want)"; return 0; fi
  printf '%s\n' "$rj" | GI "umask 077; install -d -m 0700 $STAGE_HOME/stage-ga && cat > $STAGE_HOME/stage-ga/run.json.new && mv -f $STAGE_HOME/stage-ga/run.json.new $STAGE_HOME/stage-ga/run.json"
  sha="sha256:$(G "sha256sum $STAGE_HOME/stage-ga/run.json" | cut -d' ' -f1)"
  eq "staged run.json == generated" "$sha" "$want"
  hf_has "$EV/ga-run.json" || { printf '%s\n' "$rj" | hf_save "$EV/ga-run.json"; hf_commit "$EV/ga-run.json"; }
  local o; o=$(op "stage-ga run-json $want" 2>&1 | tr -d '\r') || true
  printf '%s\n' "$o"
  [[ $o == *"stage-ga run-json ok: $want "* ]] || die "stage-ga run-json did not confirm $want (the dispatcher regenerates the file from pins.env and compares)"
  hf_put "$EV/staged.run.json" "$want"
}
stage_tbp() { ga_stage tbound-profile tbound-profile "$W/bin/tbound-profile" "$TBP_DIGEST"; }
stage_run() { ga_stage_runjson; }
stage_procoracle() { WX test -f "$CAP_DIR/procoracle.test" || die "no $CAP_DIR/procoracle.test (35-evq-capture.sh)"; ga_stage procoracle-test procoracle.test "$CAP_DIR/procoracle.test" "$TEST_PROCORACLE"; }
stage_entrypoint() { WX test -f "$CAP_DIR/tbound-cell-entrypoint.test" || die "no $CAP_DIR/tbound-cell-entrypoint.test (35-evq-capture.sh)"; ga_stage cell-entrypoint-test tbound-cell-entrypoint.test "$CAP_DIR/tbound-cell-entrypoint.test" "$TEST_ENTRYPOINT"; }

# ---- gates (print why and return 1 -> SKIPPED) ------------------------------------------------------------------------
service_running() { local s; s=$(G "$OP status" 2>&1 | tr -d '\r'); [[ $s == *"service=active socket=active"* ]]; }
need_service() { service_running || { echo "service/socket not active: $1 needs /system.slice/tbound-pi.service (run it while a cycle's service is up: after 30 or 31)"; return 1; }; }
gate_netsink() {
  local d; d=$(op "diag nft-sink" 2>&1 | tr -d '\r') || true
  if [[ $d == *"table inet tbound_net_sink"* ]] && [[ $d != *"No such file"* ]] && [[ $d == *"-- attest"* ]]; then return 0; fi
  echo "no net-sink attestation/rule on the guest (diag nft-sink below): needs 'tbound trial reset <case>' after the pilot cycle"; printf '%s\n' "$d" | head -12; return 1
}

# ---- one producer ------------------------------------------------------------------------------------------------------
declare -a RES=() DONE_OUTPUTS=()
REFUSED=0
ge_check_commit() { # WSL evidence file; compare the decoded envelope commit, never grep JSON.
  local o
  o=$(WX python3 -I -c 'import json,sys
with open(sys.argv[1], encoding="utf-8") as f:
    commit = json.load(f).get("commit", "<missing>")
if commit != sys.argv[2]:
    sys.exit(f"stale evidence from commit {commit!r}; archive it (27) before producing (expected {sys.argv[2]})")' "$1" "$FREEZE_COMMIT" 2>&1 | tr -d '\r') || die "$1: cannot accept evidence: $o"
}
ge_fetch() { # Check the partial file before promoting an export to $HF/evq.
  G "cat $1" | hf_save "$EV/$2"
  ge_check_commit "$HF/$EV/$2.partial"
  hf_commit "$EV/$2"
}
ge_done() {
  ge_check_commit "$HF/$EV/$2"
  RES+=("$1: DONE (commit $FREEZE_COMMIT) -> $HF/$EV/$2 ($(wd "$HF/$EV/$2"))")
  DONE_OUTPUTS+=("$2")
}
# ge_run <kind> <output file name> [RUNID]
ge_run() {
  local kind=$1 outn=$2 runid=${3:-} o rc=0 dg
  if hf_has "$EV/$outn"; then ge_done "$kind" "$outn"; return 0; fi
  if G "test -f $OUT/_/evidence-$outn"; then note "evidence-$outn already exported by an earlier run; fetching it"
  else
    rc=0; o=$(op "guest-evidence $kind${runid:+ $runid}" 2>&1) || rc=$?
    o=$(printf '%s' "$o" | tr -d '\r'); printf '%s\n' "$o"
    if [ "$rc" != 0 ]; then
      if [[ $o == *"exists already"* ]]; then
        note "the guest holds $outn from an earlier run: exporting /root/ga/out"
        op "collect evidence-out"; ge_fetch "$OUT/_/out-$outn" "$outn"; ge_done "$kind" "$outn"; return 0
      fi
      RES+=("$kind: REFUSED (nothing written; see the message above)"); REFUSED=1; return 0
    fi
    dg=$(printf '%s\n' "$o" | sed -n "s/.*guest-evidence $kind ok: \(sha256:[0-9a-f]\{64\}\).*/\1/p" | head -1)
    [ -n "$dg" ] || die "no digest in the guest-evidence output"
  fi
  ge_fetch "$OUT/_/evidence-$outn" "$outn"
  if [ -n "${dg:-}" ]; then eq "fetched $outn == digest the producer printed" "$(wd "$HF/$EV/$outn")" "$dg"; fi
  ge_done "$kind" "$outn"
}
skip() { RES+=("$1: SKIPPED ($2)"); }

for K in "$@"; do
  say "== $K"
  case $K in
    status)
      echo "service running: $(service_running && echo yes || echo no)"
      for f in provider capability procoracle landlock-connect netsink route-bash-view route-direct-runtime v-stub; do
        if hf_has "$EV/$f.json"; then ge_check_commit "$HF/$EV/$f.json"; echo "fetched: $f.json (commit $FREEZE_COMMIT)"
        else echo "not yet: $f.json"; fi
      done
      for f in rp dr; do hf_has "$f/RUNID" && echo "$f RUNID: $(hf_get "$f/RUNID")" || echo "$f RUNID: none"; done
      ;;
    provider-exchange)
      if hf_has g1/CLIENT_STATUS && [ "$(hf_get g1/CLIENT_STATUS)" = "done exit=0" ]; then stage_tbp; stage_run; ge_run provider-exchange provider.json
      else skip $K "g1 not done exit=0 in $HF/g1/CLIENT_STATUS"; fi;;
    capability)
      if g=$(need_service capability); then stage_tbp; ge_run capability capability.json; else echo "$g"; skip $K "service not running"; fi;;
    procoracle)
      if g=$(need_service procoracle); then stage_tbp; stage_procoracle; ge_run procoracle procoracle.json; else echo "$g"; skip $K "service not running"; fi;;
    landlock-connect)
      if g=$(need_service landlock-connect); then stage_tbp; stage_entrypoint; ge_run landlock-connect landlock-connect.json; else echo "$g"; skip $K "service not running"; fi;;
    netsink)
      if g=$(gate_netsink); then stage_tbp; ge_run netsink netsink.json; else echo "$g"; skip $K "no net-sink attestation: trial reset needs the pilot (corpus) config"; fi;;
    route-bash-view)
      if hf_has rp/RUNID && hf_has rp/CHECKS_OK; then stage_tbp; stage_run; ge_run route-bash-view route-bash-view.json "$(hf_get rp/RUNID)"
      else skip $K "31-reg-rp.sh not complete ($HF/rp/RUNID, CHECKS_OK)"; fi;;
    route-direct-runtime)
      if hf_has dr/RUNID && hf_has dr/CHECKS_OK; then stage_tbp; stage_run; ge_run route-direct-runtime route-direct-runtime.json "$(hf_get dr/RUNID)"
      else skip $K "32-reg-dr.sh not complete ($HF/dr/RUNID, CHECKS_OK)"; fi;;
    v-stub)
      if hf_has vs/CHECKS_OK && hf_has vs/DELETED && hf_has vs/record.json; then
        hf_has "$EV/v-stub.json" || { WX cat "$HF/vs/record.json" | hf_save "$EV/v-stub.json"; hf_commit "$EV/v-stub.json"; }
        ge_done v-stub v-stub.json
      else skip $K "34-v-stub-probe.sh not complete ($HF/vs/CHECKS_OK, DELETED, record.json)"; fi;;
    *) die "unknown KIND '$K'";;
  esac
done

stage_clear stage-ga
say "summary"
for outn in "${DONE_OUTPUTS[@]}"; do ge_check_commit "$HF/$EV/$outn"; done
printf '%s\n' "${RES[@]:-no producer requested}"
G "ls -l $OUT/_ | grep -E 'evidence-|out-' || true" | tr -d '\r'
note "host side: tbound-run evq/route-report take --artifact guest-capability=<file> etc. from $HF/$EV (copy to the host $H/ga names as the runbook wants); --guest-binary-sha256 = TBP_DIGEST."
[ "$REFUSED" = 0 ] || die "at least one producer was REFUSED (see the summary); the guest wrote nothing for it"
done_step
