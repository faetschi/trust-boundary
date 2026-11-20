#!/bin/bash
# [OWNER SIGN, WSL] registration and host-profile signatures of the route-probe (rp), direct-runtime probe (dr) and pilot RG cycles at $FREEZE,
# and of the research route (rr, runbook RA-7 and RA-9: the profile pins --provider-route research with the frozen $HF/rr/research-profile.json of RA-2),
# into the NEW directory $HF/<NAME> (rule 4: never overwritten, no --force). Same as sign-g1.sh, with NAME and the dr two-template rule.
#   bash sign-run.sh <rp|dr|pilot|rr> s3 <unsigned-registration.json>   S3r / S3d / S3': managed-root-sign with $H/admin.key (by path), verify with
#                                                                  --runner-digest TBOUND_DIGEST, records REGDIGEST and the registration files.
#   bash sign-run.sh <rp|dr|pilot|rr> s4                                 S4r / S4d / S4': builds the template(s) from the base template ($HF/d09-cell.base.json)
#        with managed_live_root_registration_digest = REGDIGEST. rp and pilot: $O/d09-cell.json is the installed template.
#        dr (RG-6): $O/d09-cell.json is the PRODUCTION template, $O/d09-cell.route-probe.json = the same plus the route_probe member
#        {"name":"direct-runtime/v1","target":"/home/node/.profile","control":"/var/lib/tbound/pi-worker/src/release.txt"}; the profile pins the PROBE template's TD.
#        BASE_TD of every template (printed by template-digest, route_probe ignored) must equal the pinned BASE_TD. Signs the host profile
#        pi-host-profile-<NAME>-<$FREEZE> and verifies it.
# g1 is signed by sign-g1.sh, not here. The admin key is only passed to tbound-profile by path.
set -euo pipefail
umask 077
case "$-" in *x*) echo "FATAL: refusing to run under xtrace" >&2; exit 1;; esac
. "$(dirname -- "${BASH_SOURCE[0]}")/../lib.sh"      # guard: dies while FREEZE_COMMIT is the placeholder
. "$TBOUND_WORK/env.sh"; env_paths_ok
. "$W/session-env.sh"; env_paths_ok
[ "$F" = "$FREEZE_COMMIT" ] || { echo "env.sh F ($F) != FREEZE_COMMIT in $V" >&2; exit 2; }
[ "$HF" = "$H/$FREEZE_LC" ] || { echo "HF ($HF) is not \$H/$FREEZE_LC: refusing (never sign into an earlier freeze directory)" >&2; exit 2; }
NAME=${1:-}
MODE=${2:-}
case "$NAME" in rp|dr|pilot|rr) ;; *) echo "usage: sign-run.sh <rp|dr|pilot|rr> s3 <unsigned registration> | <rp|dr|pilot|rr> s4" >&2; exit 2;; esac
O=$HF/$NAME
need() { [ -f "$1" ] || { echo "missing $1" >&2; exit 2; }; }

case "$MODE" in
s3)
  U=${3:?usage: sign-run.sh $NAME s3 <unsigned registration>}
  need "$U"
  [ ! -e "$O/managed-live-root.json" ] && [ ! -e "$O/managed-live-root.ed25519" ] || { echo "$O already holds the S3 outputs (rule 4: new dir per signing)"; exit 2; }
  mkdir -p -m 0700 "$O"
  if [ -e "$O/managed-live-root.unsigned.json" ]; then cmp "$U" "$O/managed-live-root.unsigned.json"; else cp "$U" "$O/managed-live-root.unsigned.json"; fi
  "$TBP" managed-root-sign --registration "$O/managed-live-root.unsigned.json" --key "$H/admin.key" \
    --out-sig "$O/managed-live-root.ed25519" --out-registration "$O/managed-live-root.json" | tee "$O/s3.out"
  "$TBP" managed-root-verify --registration "$O/managed-live-root.json" --sig "$O/managed-live-root.ed25519" \
    --pub "$H/admin.pub" --runner-digest "$(cat "$HF/TBOUND_DIGEST")" | tee "$O/s3-verify.out"
  REG=$(sed -n 's/.*digest \(sha256:[0-9a-f]\{64\}\),.*/\1/p' "$O/s3-verify.out" | head -1)
  [[ $REG =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "no registration digest in the verify output" >&2; exit 3; }
  grep -q "runner $(cat "$HF/TBOUND_DIGEST")" "$O/s3-verify.out" || { echo "verify output does not name the $FREEZE runner" >&2; exit 3; }
  [ ! -e "$O/REGDIGEST" ] && printf '%s\n' "$REG" > "$O/REGDIGEST"
  [ "$(cat "$O/REGDIGEST")" = "$REG" ]
  echo "REGDIGEST-$NAME=$REG"
  sha256sum "$O/managed-live-root.json" "$O/managed-live-root.ed25519"
  finish_step
  ;;
s4)
  need "$O/REGDIGEST"; need "$HF/d09-cell.base.json"; need "$HF/BASE_TD"
  [ ! -e "$O/pi-host-profile.signed.json" ] && [ ! -e "$O/pi-host-profile.ed25519" ] && [ ! -e "$O/d09-cell.json" ] && [ ! -e "$O/d09-cell.route-probe.json" ] \
    || { echo "$O already holds the S4 outputs (rule 4)"; exit 2; }
  REG=$(cat "$O/REGDIGEST")
  python3 -I - "$NAME" "$HF/d09-cell.base.json" "$O" "$REG" <<'PY'
import json, re, sys
name, src, outdir, reg = sys.argv[1:5]
assert name in ("rp", "dr", "pilot", "rr")
assert re.fullmatch(r"sha256:[0-9a-f]{64}", reg)
t = json.load(open(src))
assert "managed_live_root_registration_digest" in t
assert "route_probe" not in t
t["managed_live_root_registration_digest"] = reg
json.dump(t, open(outdir + "/d09-cell.json", "x"), separators=(",", ":"))
if name == "dr":
    p = dict(t)
    p["route_probe"] = {"name": "direct-runtime/v1", "target": "/home/node/.profile", "control": "/var/lib/tbound/pi-worker/src/release.txt"}
    json.dump(p, open(outdir + "/d09-cell.route-probe.json", "x"), separators=(",", ":"))
PY
  tdparse() { # <template-digest output file>
    TD=$(sed -n 's/^D09 cell template canonical digest: \(sha256:[0-9a-f]\{64\}\)$/\1/p' "$1")
    BASE=$(sed -n 's/^D09 base template digest .*: \(sha256:[0-9a-f]\{64\}\)$/\1/p' "$1")
    [[ $TD =~ ^sha256:[0-9a-f]{64}$ && $BASE =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "cannot parse template-digest output $1" >&2; exit 3; }
    [ "$BASE" = "$(cat "$HF/BASE_TD")" ] || { echo "BASE_TD $BASE != pinned $(cat "$HF/BASE_TD") ($1)" >&2; exit 3; }
    [ "$TD" != "$BASE" ] || { echo "TD == BASE_TD: the registration digest is not in the template ($1)" >&2; exit 3; }
  }
  if [ "$NAME" = dr ]; then
    "$TBP" template-digest --template "$O/d09-cell.json" | tee "$O/td-prod.out"
    tdparse "$O/td-prod.out"; TD_PROD=$TD
    "$TBP" template-digest --template "$O/d09-cell.route-probe.json" | tee "$O/td.out"
    tdparse "$O/td.out"
    [ "$TD" != "$TD_PROD" ] || { echo "probe TD == production TD: route_probe is not in the digest" >&2; exit 3; }
    printf '%s\n' "$TD_PROD" > "$O/TD_PROD"
    TPL=$O/d09-cell.route-probe.json
  else
    "$TBP" template-digest --template "$O/d09-cell.json" | tee "$O/td.out"
    tdparse "$O/td.out"
    TPL=$O/d09-cell.json
  fi
  ROUTE=(--provider-route engineering)
  [ "$NAME" != rr ] || { need "$O/research-profile.json"; ROUTE=(--provider-route research --research-profile "$O/research-profile.json"); }
  CGR=$("$TBP" cgroup-root-digest --uid 995 --cgroups-path /system.slice/tbound-pi.service/cells | grep -o 'sha256:[0-9a-f]\{64\}' | head -1)
  [ "$CGR" = "$(cat "$HF/CGROUP_ROOT_DIGEST")" ] || { echo "cgroup root digest changed: $CGR" >&2; exit 3; }
  "$TBP" init --out "$O/pi-host-profile.unsigned.json" --id "pi-host-profile-$NAME-${F:0:12}" \
    --node-digest "$(cat "$HF/NODE_DIGEST")" \
    --runtime-bundle-digest "$(cat "$HF/BUNDLE_DIGEST")" --template-digest "$TD" --cgroup-root-digest "$CGR" \
    --source-digest "$(cat "$HF/SRC_DIGEST")" --cosign "$COSIGN" --cosign-pub "$H/cosign.pub" "${ROUTE[@]}"
  "$TBP" sign --profile "$O/pi-host-profile.unsigned.json" --key "$H/admin.key" --out-sig "$O/pi-host-profile.ed25519" \
    --out-pub "$O/pi-host-profile.pub" --out-profile "$O/pi-host-profile.signed.json" | tee "$O/s4.out"
  cmp "$O/pi-host-profile.pub" "$H/admin.pub"
  "$TBP" verify --profile "$O/pi-host-profile.signed.json" --sig "$O/pi-host-profile.ed25519" --pub "$H/admin.pub" \
    --runtime-bundle-digest "$(cat "$HF/BUNDLE_DIGEST")" | tee "$O/s4-verify.out"
  PROF=$(sed -n 's/.*digest \(sha256:[0-9a-f]\{64\}\),.*/\1/p' "$O/s4-verify.out" | head -1)
  [[ $PROF =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "no profile digest in the verify output" >&2; exit 3; }
  grep -q "$TD" "$O/pi-host-profile.signed.json" || { echo "signed profile does not contain TD" >&2; exit 3; }
  printf '%s\n' "$TD" > "$O/TD"; printf '%s\n' "$PROF" > "$O/PROFILE_DIGEST"
  echo "NAME=$NAME REG=$REG TD(installed template)=$TD PROFILE=$PROF"
  if [ "$NAME" = dr ]; then
    sha256sum "$O/pi-host-profile.signed.json" "$O/pi-host-profile.ed25519" "$O/d09-cell.json" "$O/d09-cell.route-probe.json"
    finish_step
  else
    sha256sum "$O/pi-host-profile.signed.json" "$O/pi-host-profile.ed25519" "$O/d09-cell.json"
    finish_step
  fi
  ;;
*) echo "usage: sign-run.sh <rp|dr|pilot|rr> s3 <unsigned registration> | <rp|dr|pilot|rr> s4" >&2; exit 2;;
esac
