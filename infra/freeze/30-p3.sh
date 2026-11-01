#!/bin/bash
# P3 [READ-ONLY] cell image from $FREEZE, twice, offline; measure; diff; record the image digests.
set -euo pipefail
umask 077
. "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
load_env
logto 30-p3

[ -x "$W/bin/tbound-cell-entrypoint" ] && [ -d "$W/src/packaging/governed" ] || die "run 20-p1.sh first"
[ ! -e "$B/oci" ] || die "P3 already ran in $B (oci exists); start a new freeze"

du -sh "$CELL_BASE_CACHE"
CACHE=$B/base-cache; cp -r "$CELL_BASE_CACHE" "$CACHE"
EPOCH=$(git -C "$REPO" show -s --format=%ct "$F")
[[ $EPOCH =~ ^[0-9]+$ ]] || die "bad epoch '$EPOCH'"
echo "EPOCH=$EPOCH (commit time of $F)"

for pass in 1 2; do
  OUT=$B/oci; [ $pass = 2 ] && OUT=$B/oci2
  python3 -I "$W/src/packaging/governed/cell_image.py" build --lock "$W/src/packaging/governed/base-images.lock.json" --cache "$CACHE" \
    --entrypoint "$W/bin/tbound-cell-entrypoint" --adapter "$W/src/adapter" --epoch "$EPOCH" --out "$OUT"
  echo "== measure pass $pass"
  python3 -I "$W/src/packaging/governed/cell_image.py" measure --layout "$OUT" | tee "$B/measure$pass.out"
done
diff -r "$B/oci" "$B/oci2" || die "OCI layouts differ between passes"
echo "OCI LAYOUTS IDENTICAL (diff -r empty)"
cmp "$B/measure1.out" "$B/measure2.out" || die "measure outputs differ between passes"
echo "MEASURE OUTPUTS IDENTICAL"

IMGDIGEST=$(jget "$B/measure1.out" image_manifest_digest)
CFG=$(jget "$B/measure1.out" config_digest)
ENTRYPOINT_DIGEST=$(jget "$B/measure1.out" entrypoint_sha256)
BASH_DIGEST=$(jget "$B/measure1.out" bash_sha256)
NODE_DIGEST=$(jget "$B/measure1.out" node_sha256)
for v in IMGDIGEST CFG ENTRYPOINT_DIGEST BASH_DIGEST NODE_DIGEST; do
  [[ ${!v} =~ ^sha256:[0-9a-f]{64}$ ]] || die "bad $v: ${!v}"
done
expect_same "entrypoint in image == P1 tbound-cell-entrypoint" "$ENTRYPOINT_DIGEST" "$(D "$W/bin/tbound-cell-entrypoint")"
expect_same "entrypoint == P1 record" "$ENTRYPOINT_DIGEST" "$(cat "$HF/ENTRYPOINT_P1_DIGEST")"

# bash/node come from the pinned base image. If packaging/ is byte-identical to F0 they cannot change: hard check.
if git -C "$REPO" diff --quiet --no-ext-diff "$F0COMMIT" "$F" -- packaging; then
  echo "packaging/ unchanged since F0: base image pins must equal F0"
  expect_same "bash_sha256 == F0" "$BASH_DIGEST" "$F0_BASH_DIGEST"
  expect_same "node_sha256 == F0" "$NODE_DIGEST" "$F0_NODE_DIGEST"
else
  echo "WARNING: packaging/ changed since F0; bash/node digests are informational only"
  note_f0 bash_sha256 "$BASH_DIGEST" "$F0_BASH_DIGEST"; note_f0 node_sha256 "$NODE_DIGEST" "$F0_NODE_DIGEST"
fi
note_f0 IMGDIGEST "$IMGDIGEST" "$F0_IMGDIGEST"
note_prev IMGDIGEST "$IMGDIGEST" "$PREV_IMGDIGEST"
note_prev ENTRYPOINT_DIGEST "$ENTRYPOINT_DIGEST" "$PREV_ENTRYPOINT_DIGEST"
note_prev CFG "$CFG" "$PREV_CFG"
if [ "$IMGDIGEST" = "$PREV_IMGDIGEST" ]; then echo "IMGDIGEST UNCHANGED vs $PREV_FREEZE: guest image/cell-src/I1 need no change (10-upgrade.sh verifies); 40-sign.sh still signs into $HF for a uniform record"; else echo "IMGDIGEST CHANGED vs $PREV_FREEZE: 10-upgrade.sh installs the new layout, 50-i1-i2.sh must run I1"; fi

printf '%s\n' "$IMGDIGEST"         > "$HF/IMGDIGEST"
printf '%s\n' "$CFG"               > "$HF/CFG"
printf '%s\n' "$ENTRYPOINT_DIGEST" > "$HF/ENTRYPOINT_DIGEST"
printf '%s\n' "$BASH_DIGEST"       > "$HF/BASH_DIGEST"
printf '%s\n' "$NODE_DIGEST"       > "$HF/NODE_DIGEST"
cp "$B/measure1.out" "$HF/p3-measure.json"
find "$HF" -maxdepth 1 -type f -exec chmod 600 {} +
finish_step "$B/measure1.out" "$B/measure2.out"
echo "== DONE 30-p3"
for f in IMGDIGEST CFG ENTRYPOINT_DIGEST BASH_DIGEST NODE_DIGEST; do echo "$f=$(cat "$HF/$f")"; done
