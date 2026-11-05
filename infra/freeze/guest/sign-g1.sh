#!/bin/bash
# [OWNER SIGN, WSL] G1 registration and host-profile signatures for $FREEZE, into the NEW directory $HF/g1 (rule 4: never overwritten, no --force).
# Modeled on F1 guest/smoke-r2-sign.sh, split in two calls because the guest installs the registration (tbound-op reg-install g1) between them.
#   bash sign-g1.sh s3 <unsigned-registration.json>   RG-4: S3g. Signs with $H/admin.key (by path), verifies with --runner-digest TBOUND_DIGEST,
#                                                         records REGDIGEST-g1 and the registration files.
#   bash sign-g1.sh s4                                 RG-6: builds the g1 cell template from the base template ($HF/d09-cell.base.json, made by
#                                                         10-upgrade.sh) with managed_live_root_registration_digest = REGDIGEST-g1, computes TD,
#                                                         checks BASE_TD == the pinned BASE_TD, signs the host profile (S4g), verifies it.
# Both modes run from 30-reg-g1.sh through wsl.exe; they can also be run by hand (same arguments). The admin key is only passed to tbound-profile by path.
set -euo pipefail
umask 077
case "$-" in *x*) echo "FATAL: refusing to run under xtrace" >&2; exit 1;; esac
. "$(dirname -- "${BASH_SOURCE[0]}")/../lib.sh"      # guard: dies while FREEZE_COMMIT is the placeholder
. "$TBOUND_WORK/env.sh"; env_paths_ok
. "$W/session-env.sh"; env_paths_ok
[ "$F" = "$FREEZE_COMMIT" ] || { echo "env.sh F ($F) != FREEZE_COMMIT in $V" >&2; exit 2; }
[ "$HF" = "$H/$FREEZE_LC" ] || { echo "HF ($HF) is not \$H/$FREEZE_LC: refusing (never sign into an earlier freeze directory)" >&2; exit 2; }
O=$HF/g1
MODE=${1:-}
need() { [ -f "$1" ] || { echo "missing $1" >&2; exit 2; }; }

case "$MODE" in
s3)
  U=${2:?usage: sign-g1.sh s3 <unsigned registration>}
  need "$U"
  [ ! -e "$O/managed-live-root.json" ] && [ ! -e "$O/managed-live-root.ed25519" ] || { echo "$O already holds the S3g outputs (rule 4: new dir per signing)"; exit 2; }
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
  echo "REGDIGEST-g1=$REG"
  sha256sum "$O/managed-live-root.json" "$O/managed-live-root.ed25519"
  finish_step
  ;;
s4)
  need "$O/REGDIGEST"; need "$HF/d09-cell.base.json"; need "$HF/BASE_TD"
  [ ! -e "$O/pi-host-profile.signed.json" ] && [ ! -e "$O/pi-host-profile.ed25519" ] && [ ! -e "$O/d09-cell.json" ] || { echo "$O already holds the S4g outputs (rule 4)"; exit 2; }
  REG=$(cat "$O/REGDIGEST")
  python3 -I - "$HF/d09-cell.base.json" "$O/d09-cell.json" "$REG" <<'PY'
import json, re, sys
src, dst, reg = sys.argv[1:4]
assert re.fullmatch(r"sha256:[0-9a-f]{64}", reg)
t = json.load(open(src))
assert "managed_live_root_registration_digest" in t
t["managed_live_root_registration_digest"] = reg
json.dump(t, open(dst, "x"), separators=(",", ":"))
PY
  "$TBP" template-digest --template "$O/d09-cell.json" | tee "$O/td.out"
  TD=$(sed -n 's/^D09 cell template canonical digest: \(sha256:[0-9a-f]\{64\}\)$/\1/p' "$O/td.out")
  BASE=$(sed -n 's/^D09 base template digest .*: \(sha256:[0-9a-f]\{64\}\)$/\1/p' "$O/td.out")
  [[ $TD =~ ^sha256:[0-9a-f]{64}$ && $BASE =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "cannot parse template-digest output" >&2; exit 3; }
  [ "$BASE" = "$(cat "$HF/BASE_TD")" ] || { echo "BASE_TD $BASE != pinned $(cat "$HF/BASE_TD")" >&2; exit 3; }
  [ "$TD" != "$BASE" ] || { echo "TD == BASE_TD: the registration digest is not in the template" >&2; exit 3; }
  CGR=$("$TBP" cgroup-root-digest --uid 995 --cgroups-path /system.slice/tbound-pi.service/cells | grep -o 'sha256:[0-9a-f]\{64\}' | head -1)
  [ "$CGR" = "$(cat "$HF/CGROUP_ROOT_DIGEST")" ] || { echo "cgroup root digest changed: $CGR" >&2; exit 3; }
  "$TBP" init --out "$O/pi-host-profile.unsigned.json" --id "pi-host-profile-g1-${F:0:12}" \
    --node-digest "$(cat "$HF/NODE_DIGEST")" \
    --runtime-bundle-digest "$(cat "$HF/BUNDLE_DIGEST")" --template-digest "$TD" --cgroup-root-digest "$CGR" \
    --source-digest "$(cat "$HF/SRC_DIGEST")" --cosign "$COSIGN" --cosign-pub "$H/cosign.pub" --provider-route engineering
  "$TBP" sign --profile "$O/pi-host-profile.unsigned.json" --key "$H/admin.key" --out-sig "$O/pi-host-profile.ed25519" \
    --out-pub "$O/pi-host-profile.pub" --out-profile "$O/pi-host-profile.signed.json" | tee "$O/s4.out"
  cmp "$O/pi-host-profile.pub" "$H/admin.pub"
  "$TBP" verify --profile "$O/pi-host-profile.signed.json" --sig "$O/pi-host-profile.ed25519" --pub "$H/admin.pub" \
    --runtime-bundle-digest "$(cat "$HF/BUNDLE_DIGEST")" | tee "$O/s4-verify.out"
  PROF=$(sed -n 's/.*digest \(sha256:[0-9a-f]\{64\}\),.*/\1/p' "$O/s4-verify.out" | head -1)
  [[ $PROF =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "no profile digest in the verify output" >&2; exit 3; }
  grep -q "$TD" "$O/pi-host-profile.signed.json" || { echo "signed profile does not contain TD" >&2; exit 3; }
  printf '%s\n' "$TD" > "$O/TD"; printf '%s\n' "$PROF" > "$O/PROFILE_DIGEST"
  echo "REG=$REG TD=$TD PROFILE=$PROF"
  sha256sum "$O/pi-host-profile.signed.json" "$O/pi-host-profile.ed25519" "$O/d09-cell.json"
  finish_step
  ;;
*) echo "usage: sign-g1.sh s3 <unsigned registration> | s4" >&2; exit 2;;
esac
