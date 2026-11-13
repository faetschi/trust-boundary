#!/bin/bash
# S5 + P7 for $FREEZE [OWNER SIGN: uses the cosign passphrase, writes only into $HF].
# Signs the $FREEZE layout with the reused $H/cosign-gen.key, builds the load tar, verifies, proves a fresh key fails.
# The passphrase is read from the owner secret file into a shell variable and handed to the one signing command
# through COSIGN_PASSWORD; it is never printed, never logged, never put in argv. Do NOT run this under `bash -x`.
# Usage (Linux terminal, in WSL from Windows, after 10, 20, 25, 30 and session-env.sh):  bash 40-sign.sh
set -euo pipefail
umask 077

case "$-" in *x*) echo "FATAL: refusing to run under xtrace (it would print the passphrase)" >&2; exit 1;; esac
case ":${SHELLOPTS:-}:" in *:xtrace:*) echo "FATAL: SHELLOPTS has xtrace" >&2; exit 1;; esac
unset BASH_XTRACEFD || true

. "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
load_env
[ -f "$W/session-env.sh" ] || die "run session-env.sh first"
. "$W/session-env.sh"; env_paths_ok
logto 40-sign

step() { printf '\n== %s\n' "$*"; }
PW=""; T=""
cleanup() {
  PW=""
  if [ -n "$T" ] && [ -d "$T" ]; then
    find "$T" -type f -exec shred -u {} + 2>/dev/null || true
    rm -rf "$T"
  fi
}
trap cleanup EXIT

step "preconditions (names, modes and public digests only)"
[ "$(cat "$HF/FROZEN-COMMIT")" = "$F" ]
expect_same "IMGDIGEST file == session env" "$(cat "$HF/IMGDIGEST")" "$IMGDIGEST"
for n in admin.key cosign-gen.key cosign.pub cosign-gen.pub admin.pub ADMIN_PUB_DIGEST COSIGN_PUB_DIGEST; do [ -e "$H/$n" ] || die "H lacks $n"; done
[ "$(stat -c %a "$H")" = 700 ] && [ "$(stat -c %a "$HF")" = 700 ] || die "H or HF not 0700"
[ "$(stat -c %a "$PASSPHRASE_FILE")" = 600 ] && [ "$(stat -c %a "$(dirname "$PASSPHRASE_FILE")")" = 700 ] || die "passphrase file/dir modes are not 0600/0700"
[ -r "$PASSPHRASE_FILE" ] || die "passphrase file unreadable"
for n in pi-cell-oci pi-cell-load.tar .pi-cell-oci.partial LOAD_TAR_DIGEST SIGNED_MANIFEST_DIGEST; do
  [ ! -e "$HF/$n" ] || die "$HF/$n exists: refusing (rule 4: a second signing goes to a new directory, earlier outputs stay untouched)"
done
case "$(stat -f -c %T "$H")" in ext2/ext3|ext4) ;; *) die "H not on ext4";; esac
[ "$(D "$COSIGN")" = "$(cat "$HF/COSIGN_TOOL_DIGEST")" ] || die "cosign tool changed since 20-p1"

step "keys are the K0 keys (public digests must equal the F0 records)"
ADMIN_PUB_DIGEST=$(D "$H/admin.pub");   expect_same "ADMIN_PUB_DIGEST == F0"  "$ADMIN_PUB_DIGEST"  "$(cat "$H/ADMIN_PUB_DIGEST")"
COSIGN_PUB_DIGEST=$(D "$H/cosign.pub"); expect_same "COSIGN_PUB_DIGEST == F0" "$COSIGN_PUB_DIGEST" "$(cat "$H/COSIGN_PUB_DIGEST")"
cmp "$H/cosign.pub" "$H/cosign-gen.pub"

step "the layout to sign is the P3 layout"
M=$(python3 -I "$W/src/packaging/governed/cell_image.py" measure --layout "$B/oci" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["image_manifest_digest"])')
expect_same "measure($B/oci) == IMGDIGEST" "$M" "$IMGDIGEST"

step "S5: sign into a partial dir (renamed to pi-cell-oci only after every check passes)"
PART=$HF/.pi-cell-oci.partial
PW=$(cat "$PASSPHRASE_FILE"); [ -n "$PW" ] || die "empty passphrase file"
COSIGN_PASSWORD="$PW" python3 -I "$W/src/packaging/governed/sign_cell_image.py" sign --layout "$B/oci" --cosign "$COSIGN" --key "$H/cosign-gen.key" --out "$PART"
PW=""

step "load tar (deterministic, same recipe as F0, from the unsigned layout; checked by building twice)"
mk() { tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -C "$B/oci" -cf "$1" oci-layout index.json blobs; }
mk "$HF/.pi-cell-load.tar.partial"
mk "$B/load-check.tar"
cmp "$HF/.pi-cell-load.tar.partial" "$B/load-check.tar" || die "load tar is not reproducible"
rm -f "$B/load-check.tar"
LOAD_TAR_DIGEST=$(D "$HF/.pi-cell-load.tar.partial")

step "P7: verify with cosign.pub against IMGDIGEST"
SIGNED=$(python3 -I "$W/src/packaging/governed/sign_cell_image.py" verify --layout "$PART" --cosign "$COSIGN" --pub "$H/cosign.pub" --expect "$IMGDIGEST")
expect_same "signed_manifest_digest == IMGDIGEST" "$SIGNED" "$IMGDIGEST"

step "P7: a fresh throwaway key must NOT verify"
T=$(mktemp -d -p /dev/shm tbound-$FREEZE_LC.XXXXXX); [ "$(stat -f -c %T "$T")" = tmpfs ] || die "/dev/shm is not tmpfs"
( cd "$T" && HOME="$T" COSIGN_PASSWORD="$(openssl rand -hex 16)" "$COSIGN" generate-key-pair --output-key-prefix "$T/throw" >/dev/null )
[ -s "$T/throw.pub" ] || die "throwaway key pair not created"
RC=0
ERR=$(python3 -I "$W/src/packaging/governed/sign_cell_image.py" verify --layout "$PART" --cosign "$COSIGN" --pub "$T/throw.pub" --expect "$IMGDIGEST" 2>&1) || RC=$?
[ "$RC" -ne 0 ] || die "throwaway key VERIFIED the signature: verification is broken"
case "$ERR" in *"no matching signatures"*) echo "OK throwaway key fails: no matching signatures (rc=$RC)";; *) die "throwaway key failed for another reason: $ERR";; esac
find "$T" -type f -exec shred -u {} +; rm -rf "$T"; T=""
echo "throwaway key shredded"

step "commit outputs"
mv "$PART" "$HF/pi-cell-oci"
mv "$HF/.pi-cell-load.tar.partial" "$HF/pi-cell-load.tar"
printf '%s\n' "$ADMIN_PUB_DIGEST"   > "$HF/ADMIN_PUB_DIGEST"
printf '%s\n' "$COSIGN_PUB_DIGEST"  > "$HF/COSIGN_PUB_DIGEST"
printf '%s\n' "$LOAD_TAR_DIGEST"    > "$HF/LOAD_TAR_DIGEST"
printf '%s\n' "$SIGNED"             > "$HF/SIGNED_MANIFEST_DIGEST"
find "$HF" -maxdepth 1 -type f -exec chmod 600 {} +
ls -la "$HF"
finish_step

step "DONE: public values (safe to share)"
echo "$FREEZE=$F"
echo "ADMIN_PUB_DIGEST=$ADMIN_PUB_DIGEST"
echo "COSIGN_PUB_DIGEST=$COSIGN_PUB_DIGEST"
echo "S5 signed_manifest_digest=$SIGNED (== IMGDIGEST)"
echo "LOAD_TAR_DIGEST=$LOAD_TAR_DIGEST"
note_prev LOAD_TAR_DIGEST "$LOAD_TAR_DIGEST" "$PREV_LOAD_TAR_DIGEST"
