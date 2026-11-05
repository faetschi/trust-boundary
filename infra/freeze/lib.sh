# Generic TBound freeze kit shared helpers. SOURCED by the step scripts (not run directly). No secrets here.
# One reusable kit: every freeze-specific value comes from freezes/$KIT_FREEZE.env (selected by KIT_FREEZE).
umask 077
set -E

. "$(dirname -- "${BASH_SOURCE[0]}")/paths.sh"
A=$KIT_DIR
F0COMMIT=c7f9e9ce2516f0377c4ca59a84eea3c55aa18fc5
F1COMMIT=b42b920e9ad3f70e0082aa26d26fe4a27df64e4e
F2COMMIT=feb5c7bc26749705af80cddc82f22b65914e84b4
F3COMMIT=ea5970694252d33f44305b0f9c97afad4164085f
F4COMMIT=cb163038e64bee59b550d23e4b0a68581465ff76
F5COMMIT=6749cc6d13e0e6be39a27349f9ebaca67b41411a   # F5 (ancestor)
F6COMMIT=e37d5ad418423cf20dae3ada4c1607028d8775be   # F6 (ancestor)
F7COMMIT=608f397be19d994bbe37538ab027001b2f057396   # F7 (ancestor)
F8COMMIT=54c578e51e8af303d125a435b18781179d9aee21   # F8 ancestor
F9COMMIT=af6f38f0bc97a3e62650404bded5b632c9f452c0   # F9 (older freeze, ancestor)
F10COMMIT=bcac4cdfdc797ede41b69a32fdaa00053565b89e  # F10 (ancestor)

die() { echo "FATAL: $*" >&2; exit 1; }

# --- the one values file ----------------------------------------------------------------------------------------
# KIT_FREEZE selects freezes/<KIT_FREEZE>.env. There is no fallback: a missing file or a missing value is a stop.
. "$(dirname -- "${BASH_SOURCE[0]}")/freeze-guard.sh" || exit 2
# shellcheck disable=SC1090
. "$VALUES"
[ "$FREEZE" = "$KIT_FREEZE" ] || die "$VALUES says FREEZE=$FREEZE but KIT_FREEZE=$KIT_FREEZE"
for v in FREEZE FREEZE_COMMIT PREV_FREEZE DR_ADMISSION_ID DR_ADMISSION_REFUSE GUEST_IP; do
  [ -n "${!v:-}" ] || die "$VALUES lacks $v"
done
[ "$PREV_FREEZE" != "$FREEZE" ] || die "$VALUES: PREV_FREEZE equals FREEZE"
# PREV_FREEZE=none is the first freeze of a new chain: there is nothing to compare with, so the comparisons read empty values.
has_prev() { [ "$PREV_FREEZE" != none ]; }
if has_prev; then
  for v in PREV_COMMIT PREV_W PREV_NAMES PREV_G1_RID PREV_RP_RID PREV_DR_RID PREV_DR_RUNID PREV_DR_CHECKS_OK PREV_DR_ADMISSION_ID; do
    [ -n "${!v:-}" ] || die "$VALUES lacks $v"
  done
  [[ $PREV_COMMIT =~ ^[0-9a-f]{40}$ ]] || die "$VALUES: PREV_COMMIT is not a 40-hex lowercase commit: '$PREV_COMMIT'"
else
  PREV_COMMIT= PREV_TBOUND_DIGEST= PREV_TBP_DIGEST= PREV_ENTRYPOINT_DIGEST= PREV_SRC_DIGEST= PREV_BUNDLE_DIGEST= PREV_IMGDIGEST= PREV_CFG= PREV_LOAD_TAR_DIGEST= PREV_TEST_PROCORACLE= PREV_TEST_ENTRYPOINT=
fi
FREEZE_LC=$(printf '%s' "$FREEZE" | tr 'A-Z' 'a-z')
PREV_LC=$(printf '%s' "$PREV_FREEZE" | tr 'A-Z' 'a-z')
HF=$OLDH/$FREEZE_LC
CF=$OLDH/c$FREEZE_LC
PREV_HF=$OLDH/$PREV_LC
PREV_CF=$OLDH/c$PREV_LC
OUTROOT=$TBOUND_WORK/receipts/$FREEZE
LOG=$OUTROOT/logs
F=$FREEZE_COMMIT

# Public inputs from infra/workspace/fetch-inputs.sh, verified against their pins there.
INPUTS=$TBOUND_WORK/inputs
CELL_BASE_CACHE=$INPUTS/base-cache
COSIGN_TOOL=$INPUTS/cosign-v3.1.3-linux-amd64

# Values measured in F0 (F0-receipt.md). Used only as pins for the reuse proof and as "same as F0?" comparisons.
F0_BUNDLE_DIGEST=sha256:8e405ef0f2a3a3ed725dba2815d71bd8e5db007fe993f4ac3979ad59016d4f48
F0_BUNDLE_TAR_SHA=sha256:d0abdaf300ea8e6eacfc0c93df1f5032cf2bf84f686122bc4289c820e97e0471
F0_BUNDLE_TAR_SIZE=429660160
F0_BASH_DIGEST=sha256:55b89ab22bee4792a210f493a53fb066accd5d30b69837c28d98be5ff863efcf
F0_NODE_DIGEST=sha256:7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c
F0_IMGDIGEST=sha256:d02a7543041a52d864fb097f636a591ff1d0d86bfc38fedce645c1537176bd3d

D() { echo "sha256:$(sha256sum "$1" | cut -d' ' -f1)"; }

# require_frozen_commit: FREEZE_COMMIT must be a real 40-hex commit. Runs when this file is sourced, so every script that sources it stops here.
require_frozen_commit() {
  [ "$FREEZE_COMMIT" != FROZEN_COMMIT_TBD ] || die "FREEZE_COMMIT is still the placeholder FROZEN_COMMIT_TBD: set it in $VALUES (line 'FREEZE_COMMIT=') once the $FREEZE commit is known"
  [[ $FREEZE_COMMIT =~ ^[0-9a-f]{40}$ ]] || die "FREEZE_COMMIT is not a 40-hex lowercase commit: '$FREEZE_COMMIT'"
  [ "$FREEZE_COMMIT" != "$PREV_COMMIT" ] && [ "$FREEZE_COMMIT" != "$F10COMMIT" ] && [ "$FREEZE_COMMIT" != "$F9COMMIT" ] && [ "$FREEZE_COMMIT" != "$F8COMMIT" ] && [ "$FREEZE_COMMIT" != "$F7COMMIT" ] && [ "$FREEZE_COMMIT" != "$F6COMMIT" ] && [ "$FREEZE_COMMIT" != "$F5COMMIT" ] && [ "$FREEZE_COMMIT" != "$F4COMMIT" ] && [ "$FREEZE_COMMIT" != "$F3COMMIT" ] && [ "$FREEZE_COMMIT" != "$F2COMMIT" ] && [ "$FREEZE_COMMIT" != "$F1COMMIT" ] && [ "$FREEZE_COMMIT" != "$F0COMMIT" ] || die "FREEZE_COMMIT equals an earlier freeze: nothing to re-freeze"
}
require_frozen_commit

# disk_preflight: stop if the local root or the Linux disk (/) has less than MIN_FREE_GIB GiB free. A df that does not answer
# within 20 s is a stop too (a stalled disk once produced 0-byte output files that the scripts reported as DONE).
MIN_FREE_GIB=10
disk_preflight() {
  local p kb need=$((MIN_FREE_GIB * 1024 * 1024))
  for p in "$TBOUND_LOCAL" /; do
    kb=$(timeout 20 df -P -k "$p" | awk 'NR==2 {print $4}') || die "df $p did not answer within 20 s (disk stall?): stop"
    [[ $kb =~ ^[0-9]+$ ]] || die "cannot read the free space of $p"
    [ "$kb" -ge "$need" ] || die "less than ${MIN_FREE_GIB} GiB free on $p ($((kb / 1024 / 1024)) GiB): free space first"
    echo "disk $p: $((kb / 1024 / 1024)) GiB free (need ${MIN_FREE_GIB})"
  done
}
disk_preflight

# finish_step [path...]: last action of every owner-side step. sync, then no regular file under $HF (nor under any directory given as an argument)
# and no file given as an argument may be 0 bytes. A directory argument is searched recursively; a file argument must be non-empty. A 0-byte file is a stop.
finish_step() {
  local p z
  sync
  for p in "$HF" "$@"; do
    if [ -d "$p" ]; then
      z=$(find "$p" -type f -size 0 ! -name '*.err' ! -path "$HF/dr/command-audit.jsonl" ! -path "$HF/dr/provider-audit.jsonl" -print | head -20 || true)
      [ -z "$z" ] || die "0-byte output file(s) under $p (disk stall?): stop and look, do not continue:"$'\n'"$z"
    elif [ -f "$p" ]; then
      [ -s "$p" ] || die "0-byte output file $p (disk stall?): stop and look, do not continue"
    else
      die "finish_step: expected output $p is missing"
    fi
  done
  sync
  echo "OK sync done; no 0-byte output under $HF${*:+ nor in: $*}"
}

# logto <name>: from here on stdout+stderr go to the console and to $LOG/<name>.log (append).
# Never call this in a script that runs under xtrace with secrets in scope (40-sign.sh refuses xtrace).
logto() {
  mkdir -p "$LOG"
  exec > >(tee -a "$LOG/$1.log") 2>&1
  echo "== $1 start $(date -u +%FT%TZ) as $(id -un) freeze=$FREEZE"
  trap 'echo "== FAILED ${0##*/}:$LINENO rc=$?" >&2' ERR
}

# git_env: git against $REPO without touching global config. Needs $REPO.
git_env() {
  export GIT_OPTIONAL_LOCKS=0 GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0="$REPO"
}

# load_env: read $TBOUND_WORK/env.sh written by 10-freeze.sh and sanity-check it.
load_env() {
  [ -f "$TBOUND_WORK/env.sh" ] || die "no $TBOUND_WORK/env.sh: run 10-freeze.sh first"
  . "$TBOUND_WORK/env.sh"; env_paths_ok
  for v in W B H F REPO HF CF LOG; do [ -n "${!v:-}" ] || die "env.sh lacks $v"; done
  git_env
  [ -d "$W" ] && [ -d "$B" ] && [ -d "$HF" ] || die "W/B/HF directory missing"
  [ "$HF" = "$H/$FREEZE_LC" ] || die "env.sh HF ($HF) != \$H/$FREEZE_LC"
  [ "$CF" = "$H/c$FREEZE_LC" ] || die "env.sh CF ($CF) != \$H/c$FREEZE_LC"
  [ "$LOG" = "$OUTROOT/logs" ] || die "env.sh LOG ($LOG) != $OUTROOT/logs"
  [ "$(cat "$HF/FROZEN-COMMIT")" = "$F" ] || die "FROZEN-COMMIT != F in env.sh"
  [ "$F" = "$FREEZE_COMMIT" ] || die "env.sh F ($F) != FREEZE_COMMIT in $VALUES ($FREEZE_COMMIT)"
}

# check_prev_values: the PREV_* constants must equal the recorded outputs of the previous freeze ($PREV_HF/<NAME>). Names only are read: public digests.
check_prev_values() {
  has_prev || return 0
  local n v
  for n in $PREV_NAMES; do
    v="PREV_$n"
    [ "$(cat "$PREV_HF/$n")" = "${!v}" ] || die "$PREV_HF/$n differs from $v in $VALUES"
  done
  [ "$(cat "$PREV_HF/FROZEN-COMMIT")" = "$PREV_COMMIT" ] || die "$PREV_HF/FROZEN-COMMIT is not $PREV_FREEZE"
  [ "$(cat "$PREV_HF/g1/RID")" = "$PREV_G1_RID" ] || die "$PREV_HF/g1/RID is not PREV_G1_RID"
  [ "$(cat "$PREV_HF/rp/RID")" = "$PREV_RP_RID" ] || die "$PREV_HF/rp/RID is not PREV_RP_RID"
  [ "$(cat "$PREV_HF/dr/RID")" = "$PREV_DR_RID" ] || die "$PREV_HF/dr/RID is not PREV_DR_RID"
  [ "$(cat "$PREV_HF/dr/RUNID")" = "$PREV_DR_RUNID" ] || die "$PREV_HF/dr/RUNID is not PREV_DR_RUNID"
  [ "$(cat "$PREV_HF/dr/CHECKS_OK")" = "$PREV_DR_CHECKS_OK" ] || die "$PREV_HF/dr/CHECKS_OK is not '$PREV_DR_CHECKS_OK'"
  [ "$(cat "$PREV_HF/dr/ADMISSION_ID")" = "$PREV_DR_ADMISSION_ID" ] || die "$PREV_HF/dr/ADMISSION_ID is not PREV_DR_ADMISSION_ID"
  echo "OK PREV_* constants equal the $PREV_HF records"
}

# jget <json-file> <key>: print one top-level string value.
jget() { python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$1" "$2"; }

# expect_same <label> <got> <want>: hard fail on mismatch.
expect_same() { [ "$2" = "$3" ] || die "$1 mismatch: got $2 want $3"; echo "OK $1 = $2"; }

# note_f0 / note_prev <label> <value> <earlier value>: informational "same as / NEW".
note_f0() { if [ "$2" = "$3" ]; then echo "$1: $2  (same as F0)"; else echo "$1: $2  (NEW; F0 was $3)"; fi; }
note_prev() { if ! has_prev; then echo "$1: $2  (first freeze of this chain)"; elif [ "$2" = "$3" ]; then echo "$1: $2  (same as $PREV_FREEZE)"; else echo "$1: $2  (NEW; $PREV_FREEZE was $3)"; fi; }
