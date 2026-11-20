#!/bin/bash
# F31 gate rerun (gf31 had 2 load flakes): same verified archive, label gf31b, under the Linux-line lock.
set -euo pipefail
cd /home/tbound
T=/home/tbound/tbound-work/gf31.tar
L=/home/tbound/tbound-work/gf31b-gate.log
[ ! -e "$L" ] || { echo "FATAL: $L exists"; exit 1; }
echo "$(sha256sum "$T")"
[ "$(sha256sum < "$T" | cut -d' ' -f1)" = 6767b34181cdabbc723f4827591e2fa70744a194212e59928da583f4e26b8fb5 ] || { echo "FATAL: archive changed"; exit 4; }
source /home/tbound/env.sh
echo "meta load=$(cut -d' ' -f1-3 /proc/loadavg) start=$(date -u +%FT%TZ)"
ps -eo pid,etimes,args --sort=-pcpu | head -6
rc=0
flock -w 7200 /home/tbound/.linux-line.lock nice -n 19 env KIT_FREEZE=F18 bash /home/tbound/tbound-work/kf19/infra/freeze/tools/gate.sh "$T" gf31b > "$L" 2>&1 || rc=$?
echo "GATE_RC=$rc end=$(date -u +%FT%TZ) load=$(cut -d' ' -f1-3 /proc/loadavg)"
tail -4 "$L"
