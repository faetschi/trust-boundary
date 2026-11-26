#!/bin/bash
# rw33-aside.sh <round R> <label of the round $HF/pilot holds now>: ROUND.md 1 aside for F33, WSL fed on stdin; a rename only.
set -u
R=${1:?}; PREV=${2:?}; M=/f/trust-boundary/.local/round-$R-f33
mkdir -p $M/logs $M/runs $M/diag
MSYS_NO_PATHCONV=1 wsl.exe -d tbound-dev --cd / --exec bash -s -- "$PREV" <<'EOS' | tr -d '\r' | tee $M/logs/aside.txt
set -eu
H=/home/tbound/tbh/f33; L=${1:?label}; u=$(date -u +%Y%m%dT%H%M%SZ)
[ -d $H/pilot ] || { echo "no $H/pilot"; exit 0; }
cat $H/pilot/CARRIER 2>/dev/null || echo "no CARRIER record (the chain's T5 pilot)"
mv -T -n -- $H/pilot $H/pilot.$L-$u
ls -d $H/pilot* ; echo "aside $H/pilot.$L-$u files=$(find $H/pilot.$L-$u -type f | wc -l)"
EOS
