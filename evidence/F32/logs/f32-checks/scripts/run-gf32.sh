#!/bin/bash
# F31 gate: copy the freeze-tree archive into tbound-work, verify every blob, then run the kit gate under the Linux-line lock.
set -euo pipefail
cd /home/tbound
S=/mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/300b9e68-97e7-4a01-aa27-ad0df38df07f/scratchpad/f31w
T=/home/tbound/tbound-work/gf32.tar
L=/home/tbound/tbound-work/gf32-gate.log
[ ! -e "$L" ] || { echo "FATAL: $L exists"; exit 1; }
cp -- "$S/gf32.tar" "$T"
sha256sum "$T"
V=/home/tbound/tbound-work/gf32-verify
case "$V" in /home/tbound/tbound-work/gf32-verify) rm -rf -- "$V";; *) exit 3;; esac
mkdir -p "$V" && tar -C "$V" -xf "$T"
bad=0; n=0
while read -r h p; do n=$((n+1)); g=$(git hash-object --no-filters -- "$V/$p"); [ "$g" = "$h" ] || { echo "MISMATCH $p"; bad=$((bad+1)); }; done < "$S/gf32-blobs.txt"
extra=$(( $(find "$V" -type f | wc -l) - n ))
echo "BLOBS checked=$n mismatched=$bad extra_files=$extra"; [ "$bad" -eq 0 ] && [ "$extra" -eq 0 ] || exit 4
K=/home/tbound/tbound-work/kf19/infra/freeze/tools/gate.sh
if cmp -s "$K" "$V/infra/freeze/tools/gate.sh"; then echo "GATE_TOOL kf19 == freeze tree"; else echo "GATE_TOOL differs from freeze tree:"; diff "$K" "$V/infra/freeze/tools/gate.sh" || true; fi
rm -rf -- "$V"
source /home/tbound/env.sh
echo "meta load=$(cut -d' ' -f1-3 /proc/loadavg) start=$(date -u +%FT%TZ)"
rc=0
flock -w 7200 /home/tbound/.linux-line.lock nice -n 19 env KIT_FREEZE=F18 bash "$K" "$T" gf32 > "$L" 2>&1 || rc=$?
echo "GATE_RC=$rc end=$(date -u +%FT%TZ)"
tail -4 "$L"
