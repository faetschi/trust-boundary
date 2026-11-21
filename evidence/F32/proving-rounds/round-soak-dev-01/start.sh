#!/bin/bash
# Starts round soak-dev-01 on F32 detached: recover.sh (kit) starts runner chunk-01 and supervises it; its Windows PID in recover.pid.
M=/f/trust-boundary/.local/round-soak-dev-01-f32; export MSYS_NO_PATHCONV=1 GUEST_IP=172.18.247.10 TBOUND_LOCAL=/f/trust-boundary/.local
cd $M || exit 2
[ ! -e runs/chunk-01.txt ] || { echo "chunk-01 exists" >> recover.log; exit 2; }
date -u +%FT%TZ > runs/full.start
cat /proc/$$/winpid > recover.pid
exec bash /c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb/infra/freeze/tools/recover.sh $M > $M/recover.out 2> $M/recover.err
