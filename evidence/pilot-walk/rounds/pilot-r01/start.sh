#!/bin/bash
# Starts round pilot-r01 on F32 detached: recover.sh (kit) starts runner chunk-01 and supervises it; its Windows PID in recover.pid.
M=/f/trust-boundary/.local/round-pilot-r01-f32; export MSYS_NO_PATHCONV=1 GUEST_IP=172.29.57.244 TBOUND_LOCAL=/f/trust-boundary/.local
cd $M || exit 2
[ ! -e runs/chunk-01.txt ] || { echo "chunk-01 exists" >> recover.log; exit 2; }
date -u +%FT%TZ > runs/full.start
cat /proc/$$/winpid > recover.pid
exec bash /f/trust-boundary/.local/worktrees/kit-f32/infra/freeze/tools/recover.sh $M > $M/recover.out 2> $M/recover.err
