#!/bin/bash
T=/home/tbound/tbound-work
[ ! -e $T/run-f34-freeze.out ] || { echo "FATAL: run-f34-freeze.out exists"; exit 1; }
cp -- /mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/300b9e68-97e7-4a01-aa27-ad0df38df07f/scratchpad/f31w/run-f34-freeze.sh $T/run-f34-freeze.sh
setsid -f nohup bash -c 'bash /home/tbound/tbound-work/run-f34-freeze.sh > /home/tbound/tbound-work/run-f34-freeze.out 2>&1; echo "EXIT=$?" >> /home/tbound/tbound-work/run-f34-freeze.out; touch /home/tbound/tbound-work/run-f34-freeze.out.done' > /dev/null 2>&1 < /dev/null
sleep 5
ps -ef | grep -v grep | grep -E "run-f34-freeze|freeze.sh" | head -3 | cut -c1-160
cat $T/run-f34-freeze.out
