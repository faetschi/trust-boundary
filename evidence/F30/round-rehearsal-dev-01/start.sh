#!/bin/bash
# Starts the round detached: runner chunk-01 (nohup), then becomes the recover.sh supervisor (its Windows PID in recover.pid).
M=/f/trust-boundary/.local/round-rehearsal-dev-01-f30; export MSYS_NO_PATHCONV=1
cd $M || exit 2
[ ! -e runs/chunk-01.txt ] || { echo "chunk-01 exists" >> recover.log; exit 2; }
date -u +%FT%TZ > runs/full.start
nohup ./bin/tbound-run.exe run --config run.json > runs/chunk-01.txt 2> runs/chunk-01.err &
sleep 3
tasklist /FI "IMAGENAME eq tbound-run.exe" /FO CSV /NH | head -1 | cut -d, -f2 | tr -d '"' > runs/full.pid
cat /proc/$$/winpid > recover.pid
exec bash $M/recover.sh > $M/recover.out 2> $M/recover.err
