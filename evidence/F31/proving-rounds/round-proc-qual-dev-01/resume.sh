#!/bin/bash
M=/f/trust-boundary/.local/round-proc-qual-dev-01-f31; export MSYS_NO_PATHCONV=1 GUEST_IP=172.18.247.10 TBOUND_LOCAL=/f/trust-boundary/.local
cd $M || exit 2
exec bash /c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb/infra/freeze/tools/recover.sh $M >> $M/recover.out 2>> $M/recover.err
