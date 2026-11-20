#!/bin/bash
# F31 local freeze, second EVQ capture (attempt 1 failed 3 tests on node-subprocess timeouts under host CPU load) then signing, under the Linux-line lock.
# The failed capture directory and console log are moved aside, never deleted.
set -euo pipefail
cd /home/tbound
source /home/tbound/env.sh
T=/home/tbound/tbound-work; H=/home/tbound/tbound-h; L=$T/receipts/F31/logs
exec 9>/home/tbound/.linux-line.lock
flock -w 7200 9
pgrep -f 'go test' >/dev/null && { echo "FATAL: a go test is running; WSL not idle"; exit 1; }
u=$(date -u +%Y%m%dT%H%M%SZ)
[ -d $H/cf31 ] && mv -T -n -- $H/cf31 $H/cf31.failed-$u
[ -e $L/console-35-evq-capture.sh.txt ] && mv -n -- $L/console-35-evq-capture.sh.txt $L/console-35-evq-capture.sh-attempt1.txt
ls -d $H/cf31* ; ls $L | grep console-35
cd $T/f31-src/infra/freeze
for s in 35-evq-capture.sh 40-sign.sh; do
  echo "##### $s $(date -u +%FT%TZ) load=$(cut -d' ' -f1-3 /proc/loadavg)"
  rc=0; ( export KIT_FREEZE=F31; nice -n 19 bash $s; r=$?; sync; exit $r ) > $L/console-$s.txt 2>&1 || rc=$?
  tail -4 $L/console-$s.txt
  [ $rc = 0 ] || { echo "STOP rc=$rc at $s"; exit $rc; }
done
echo "tail end $(date -u +%FT%TZ)"
