#!/bin/bash
# F33 local freeze (10 20 25 30 session-env 35 40 via tools/freeze.sh) inside the Linux-line lock; WSL must be idle.
set -euo pipefail
cd /home/tbound
source /home/tbound/env.sh
T=/home/tbound/tbound-work
exec 9>/home/tbound/.linux-line.lock
flock -w 7200 9
grep -qx 'TEST_RC=0' $T/g33b-gate.log || { echo "FATAL: gate g33b did not end TEST_RC=0; freeze not started"; exit 1; }
pgrep -f 'go test' >/dev/null && { echo "FATAL: a go test is running; WSL not idle"; exit 1; }
mountpoint -q /home/tbound/tbh && grep -qx OLDH=/home/tbound/tbh $T/f33-src/.local/freeze-host.env || { echo "FATAL: short freeze home not ready"; exit 1; }
echo "freeze start $(date -u +%FT%TZ) load=$(cut -d' ' -f1-3 /proc/loadavg)"
cd $T/f33-src/infra/freeze
KIT_FREEZE=F33 nice -n 19 bash tools/freeze.sh > $T/f33-freeze-driver.log 2>&1
echo "freeze end $(date -u +%FT%TZ)"
