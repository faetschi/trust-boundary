#!/bin/bash
set -euo pipefail
cd /home/tbound
S=/mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/5885a9d4-6837-49d0-8bb9-191b48098c96/scratchpad/f30w
T=/home/tbound/tbound-work/gf30.tar
O=/home/tbound/tbound-work/gf30.out
rm -f -- "$O.done"
cp -- "$S/gf30.tar" "$T"
sha256sum "$T"
V=/home/tbound/tbound-work/gf30-verify
case "$V" in /home/tbound/tbound-work/gf30-verify) rm -rf -- "$V";; *) exit 3;; esac
mkdir -p "$V" && tar -C "$V" -xf "$T"
bad=0; n=0
while read -r h p; do n=$((n+1)); g=$(git hash-object --no-filters -- "$V/$p"); [ "$g" = "$h" ] || { echo "MISMATCH $p"; bad=$((bad+1)); }; done < "$S/gf30-blobs.txt"
extra=$(( $(find "$V" -type f | wc -l) - n ))
echo "BLOBS checked=$n mismatched=$bad extra_files=$extra"; [ "$bad" -eq 0 ] && [ "$extra" -eq 0 ] || exit 4
rm -rf -- "$V"
source /home/tbound/env.sh
rc=0
flock -w 7200 /home/tbound/.linux-line.lock nice -n 19 env KIT_FREEZE=F18 bash /home/tbound/tbound-work/kf19/infra/freeze/tools/gate.sh "$T" gf30 > /home/tbound/tbound-work/gf30-gate.log 2>&1 || rc=$?
echo "GATE_RC=$rc"
