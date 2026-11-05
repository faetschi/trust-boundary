#!/bin/bash
# Copies one freeze's receipts from the Linux scratch into evidence/, for committing. Run on the Windows side, or on native Linux.
set -euo pipefail
. "$(dirname -- "${BASH_SOURCE[0]}")/../paths.sh"
. "$(dirname -- "${BASH_SOURCE[0]}")/../freeze-guard.sh"
SRC=$TBOUND_WORK_VIEW/receipts/$FREEZE
[ -d "$SRC" ] || { echo "FATAL: no $SRC" >&2; exit 2; }
case $(uname -s) in MINGW* | MSYS*) ;; *) linux_safe "$EVIDENCE" || { echo "FATAL: EVIDENCE=$EVIDENCE is not a plain Linux path outside /mnt" >&2; exit 2; } ;; esac
DEST=$EVIDENCE/$FREEZE
[ ! -e "$DEST" ] || { echo "FATAL: $DEST exists: evidence is never overwritten" >&2; exit 2; }
STAGE=$DEST.partial
[ ! -e "$STAGE" ] || { echo "FATAL: $STAGE exists from a failed run: inspect and remove it" >&2; exit 2; }
mkdir -p "$STAGE"
# Gate archives are regenerable from git, and keys never enter evidence/.
(cd "$SRC" && tar -cf - --exclude='*.tar' --exclude='*.key' --exclude='*.pem' --exclude='*ed25519' --exclude='*passphrase*' .) | tar -xf - -C "$STAGE"
mv "$STAGE" "$DEST"
echo "copied to $DEST"
