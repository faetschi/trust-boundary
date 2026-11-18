#!/bin/bash
# F30 local freeze (10 20 25 30 session-env 35 40 via tools/freeze.sh) inside the Linux-line lock; WSL must be idle.
set -euo pipefail
cd /home/tbound
source /home/tbound/env.sh
T=/home/tbound/tbound-work
exec 9>/home/tbound/.linux-line.lock
flock -w 7200 9
grep -qx 'TEST_RC=0' $T/gf30-gate.log || { echo "FATAL: gate gf30 did not end TEST_RC=0; freeze not started"; exit 1; }
pgrep -f 'go test' >/dev/null && { echo "FATAL: a go test is running; WSL not idle"; exit 1; }
echo "freeze start $(date -u +%FT%TZ)"
cd $T/f30-src/infra/freeze
KIT_FREEZE=F30 nice -n 19 bash tools/freeze.sh > $T/f30-freeze-driver.log 2>&1
echo "freeze end $(date -u +%FT%TZ)"
