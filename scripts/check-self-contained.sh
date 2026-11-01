#!/bin/bash
# Fails when the repo depends on a path outside itself, or when the freeze kit's safety checks stop working.
# Usage: check-self-contained.sh [--self-test]
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

FORBIDDEN='F:[/\\]+[A-Za-z]|/mnt/[def]/|/f/TBoundAssets|TBoundAssets|TBoundVMs|AppData[/\\]+Local[/\\]+TBound|/home/jeli2k'
# Historical records and files measured byte-for-byte on the guest keep the paths they were written with.
hits=$(git grep -nIE "$FORBIDDEN" -- . \
  ':!evidence' ':!infra/freeze/freezes' \
  ':!infra/guest/systemd/var-lib-tbound-live.mount' ':!infra/guest/systemd/var-lib-tbound-runtime.mount' \
  ':!scripts/check-self-contained.sh' || true)
if [ -n "$hits" ]; then
  printf '%s\n' "$hits" | cut -c1-160 >&2
  echo "FAIL: tracked files depend on a path outside the repo. Use .local/ or evidence/ (docs/guides/self-contained-workspace.md)." >&2
  exit 1
fi

git check-ignore -q .local/x || { echo "FAIL: .local/ is not gitignored" >&2; exit 1; }
if git ls-files evidence | grep -E '\.(key|pem|tar|exe|vhdx|iso)$|ed25519$'; then
  echo "FAIL: binary or key file tracked under evidence/" >&2
  exit 1
fi
echo "OK: no outside paths"

[ "${1:-}" = --self-test ] || exit 0

for script in infra/freeze/*.sh infra/freeze/tools/*.sh infra/freeze/guest/*.sh infra/workspace/*.sh; do bash -n "$script"; done

SANDBOX=$(mktemp -d)
trap 'rm -rf -- "$SANDBOX"' EXIT
mkdir -p "$SANDBOX/local" "$SANDBOX/home/gates/keep"
GATE=$ROOT/infra/freeze/tools/gate.sh

# Without a host file the kit refuses to start.
if HOME=$SANDBOX/home TBOUND_LOCAL=$SANDBOX/local KIT_FREEZE=F14 bash "$GATE" a.tar label >/dev/null 2>&1; then
  echo "FAIL: gate ran without a host file" >&2; exit 1
fi
printf 'TBOUND_WORK=/w\nOLDH=/h\nGO=/g/bin/go\nPASSPHRASE_FILE=/p\n' > "$SANDBOX/local/freeze-host.env"

# A missing or unsafe label must not delete anything: this is the empty-variable failure of 2026-10-31.
for label in '' '../..' '.' '..' '-rf' 'a/b'; do
  if HOME=$SANDBOX/home TBOUND_LOCAL=$SANDBOX/local KIT_FREEZE=F14 bash "$GATE" a.tar "$label" >/dev/null 2>&1; then
    echo "FAIL: gate accepted label '$label'" >&2; exit 1
  fi
  [ -d "$SANDBOX/home/gates/keep" ] || { echo "FAIL: label '$label' deleted a gate" >&2; exit 1; }
done
if HOME=$SANDBOX/home TBOUND_LOCAL=$SANDBOX/local KIT_FREEZE=F14 bash "$GATE" >/dev/null 2>&1; then
  echo "FAIL: gate ran with no arguments" >&2; exit 1
fi
[ -d "$SANDBOX/home/gates/keep" ] || { echo "FAIL: no-argument gate deleted a gate" >&2; exit 1; }

# The checker itself must catch an outside path.
PROBE=$SANDBOX/probe
mkdir -p "$PROBE/scripts" "$PROBE/docs"
cp "$ROOT/scripts/check-self-contained.sh" "$PROBE/scripts/"
printf '/.local/\n' > "$PROBE/.gitignore"
printf 'clean\n' > "$PROBE/docs/a.md"
git -C "$PROBE" init -q
git -C "$PROBE" add .
bash "$PROBE/scripts/check-self-contained.sh" >/dev/null 2>&1 || { echo "FAIL: checker rejected a clean repo" >&2; exit 1; }
printf 'see F:\\Videos\n' > "$PROBE/docs/a.md"
git -C "$PROBE" add .
if bash "$PROBE/scripts/check-self-contained.sh" >/dev/null 2>&1; then
  echo "FAIL: checker missed an outside path" >&2; exit 1
fi

# Receipt export skips gate archives and keys, and never overwrites evidence.
WORK=$SANDBOX/work/receipts/F14/logs
mkdir -p "$WORK"
printf 'log\n' > "$WORK/a.log"; printf 'tar\n' > "$WORK/gate-x.tar"; printf 'key\n' > "$WORK/x.key"
printf 'TBOUND_WORK=/w\nOLDH=/h\nGO=/g/bin/go\nPASSPHRASE_FILE=/p\n' > "$SANDBOX/local/freeze-host.env"
PULL=$ROOT/infra/freeze/tools/pull-receipts.sh
TBOUND_LOCAL=$SANDBOX/local TBOUND_EVIDENCE=$SANDBOX/ev TBOUND_WORK_VIEW=$SANDBOX/work KIT_FREEZE=F14 bash "$PULL" >/dev/null
[ -f "$SANDBOX/ev/F14/logs/a.log" ] || { echo "FAIL: receipts were not exported" >&2; exit 1; }
[ ! -e "$SANDBOX/ev/F14/logs/gate-x.tar" ] && [ ! -e "$SANDBOX/ev/F14/logs/x.key" ] || { echo "FAIL: export kept a tar or key" >&2; exit 1; }
if TBOUND_LOCAL=$SANDBOX/local TBOUND_EVIDENCE=$SANDBOX/ev TBOUND_WORK_VIEW=$SANDBOX/work KIT_FREEZE=F14 bash "$PULL" >/dev/null 2>&1; then
  echo "FAIL: export overwrote evidence" >&2; exit 1
fi
# fetch-inputs.sh keeps only a download that matches its pin. A local copy stands in for the network.
(
  . "$ROOT/infra/workspace/fetch-inputs.sh"
  download() { cp -- "$1" "$2"; }
  dl=$SANDBOX/fetch; mkdir -p "$dl"; printf 'pinned\n' > "$SANDBOX/src"
  right=$(sha256sum "$SANDBOX/src" | cut -d' ' -f1); wrong=$(printf other | sha256sum | cut -d' ' -f1)
  printf 'corrupt\n' > "$dl/a.bin"
  fetch_one "$dl" a.bin "$right" "$SANDBOX/src" >/dev/null && [ "$(cat "$dl/a.bin")" = pinned ] || { echo "FAIL: a corrupt file was not replaced" >&2; exit 1; }
  if fetch_one "$dl" b.bin "$wrong" "$SANDBOX/src" >/dev/null 2>&1 || [ -e "$dl/b.bin" ]; then echo "FAIL: a download that misses its pin was kept" >&2; exit 1; fi
)
echo "OK: self-test"
