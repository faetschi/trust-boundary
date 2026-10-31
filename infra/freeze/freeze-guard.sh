#!/bin/bash
# SOURCED before creating output directories, opening logs, or starting work.
# FREEZE and FREEZE_COMMIT are literal, unquoted assignments in the values files.
[[ ${KIT_FREEZE:-} =~ ^F[0-9]+$ ]] || { echo "FATAL: KIT_FREEZE must match ^F[0-9]+$ (e.g. F13)" >&2; exit 2; }
FREEZE_KIT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd) || exit 2
VALUES=$FREEZE_KIT_DIR/freezes/$KIT_FREEZE.env
[ -f "$VALUES" ] || { echo "FATAL: no values file $VALUES" >&2; exit 2; }
FREEZE=$(sed -n 's/^FREEZE=//p' "$VALUES") || exit 2
[ "$FREEZE" = "$KIT_FREEZE" ] || { echo "FATAL: $VALUES says FREEZE=$FREEZE but KIT_FREEZE=$KIT_FREEZE" >&2; exit 2; }
FREEZE_COMMIT=$(sed -n 's/^FREEZE_COMMIT=//p' "$VALUES") || exit 2
[[ $FREEZE_COMMIT =~ ^[0-9a-f]{40}$ ]] || { echo "FATAL: FREEZE_COMMIT in $VALUES must be 40 lowercase hex, not FROZEN_COMMIT_TBD" >&2; exit 2; }
# Match historical numbers without octal parsing or integer overflow (including leading zeros).
[[ ! $FREEZE =~ ^F0*([0-9]|1[0-2])$ ]] || { echo "FATAL: $FREEZE is historical and read-only (F12 and older)" >&2; exit 2; }
