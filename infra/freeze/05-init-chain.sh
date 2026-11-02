#!/bin/bash
# [OWNER SIGN] Starts a new chain (PREV_FREEZE=none): creates $H with its own admin and cosign keys, and the passphrase file.
# Same recipe as K0, P4 and P5 in infra/guest/PROVISIONING-SIGNING.md, without a prompt. Never overwrites, prints no secret, and runs once per chain.
set -euo pipefail
umask 077

case "$-" in *x*) echo "FATAL: refusing to run under xtrace (it would print the passphrase)" >&2; exit 1;; esac
case ":${SHELLOPTS:-}:" in *:xtrace:*) echo "FATAL: SHELLOPTS has xtrace" >&2; exit 1;; esac

. "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
logto 05-init-chain

has_prev && die "$FREEZE continues $PREV_FREEZE: restore its freeze home instead of creating keys"
H=$OLDH
[ ! -e "$H" ] || die "$H exists: this chain already has keys"
[ ! -e "$PASSPHRASE_FILE" ] || die "$PASSPHRASE_FILE exists: refusing to overwrite it"
[ -f "$COSIGN_TOOL" ] || die "no $COSIGN_TOOL (run infra/workspace/fetch-inputs.sh $INPUTS)"
T=$(mktemp -d -p /dev/shm tbound-init.XXXXXX); trap 'rm -rf "$T"' EXIT
[ "$(stat -f -c %T "$T")" = tmpfs ] || die "/dev/shm is not tmpfs"

mkdir -p "$(dirname -- "$H")"; mkdir -m 700 "$H"
case "$(stat -f -c %T "$H")" in ext2/ext3|ext4) ;; *) rmdir "$H"; die "H must be on ext4";; esac
P=$(dirname -- "$PASSPHRASE_FILE")
mkdir -p -m 700 "$P"
[ "$(stat -c %a "$P")" = 700 ] || die "$P is not 0700"
openssl rand -hex 32 > "$PASSPHRASE_FILE"

# The admin seed passes only through tmpfs: raw 64-byte Ed25519 key, seed then public key.
openssl genpkey -algorithm ED25519 -outform DER -out "$T/seed.der"
openssl pkey -inform DER -in "$T/seed.der" -pubout -outform DER | tail -c 32 > "$T/pub.raw"
tail -c 32 "$T/seed.der" | cat - "$T/pub.raw" > "$H/admin.key"
[ "$(stat -c %s "$H/admin.key")" = 64 ] || die "admin.key is not 64 bytes"
tail -c 32 "$H/admin.key" > "$H/admin.pub"

install -m 700 "$COSIGN_TOOL" "$H/.cosign"
( cd "$H"; HOME="$T" COSIGN_PASSWORD="$(cat "$PASSPHRASE_FILE")" ./.cosign generate-key-pair --output-key-prefix "$H/cosign-gen" >/dev/null )
rm -f "$H/.cosign"
cp "$H/cosign-gen.pub" "$H/cosign.pub"
D "$H/admin.pub" > "$H/ADMIN_PUB_DIGEST"
D "$H/cosign.pub" > "$H/COSIGN_PUB_DIGEST"
sync
echo "ADMIN_PUB_DIGEST=$(cat "$H/ADMIN_PUB_DIGEST")"
echo "COSIGN_PUB_DIGEST=$(cat "$H/COSIGN_PUB_DIGEST")"
echo "== DONE 05-init-chain: back up $H and $P, they cannot be recreated"
