#!/bin/bash
# Read-only: the service journal of the round in journalctl's default format (round_checks.py reads diag/final/service-journal.txt), as the rehearsal-dev-01 diag held.
set -u
M=/f/trust-boundary/.local/round-rehearsal-dev-02-f30; F=$M/diag/final; K=/f/trust-boundary/.local/worktrees/round30-kit
export GUEST_IP=172.19.180.82 TBOUND_LOCAL=/f/trust-boundary/.local MSYS_NO_PATHCONV=1
G(){ sh $K/infra/freeze/tools/gssh.sh "$@" </dev/null; }
since=$(cat $M/runs/full.start)
G "journalctl -u tbound-pi.service --since '$since' --until '$(cat $F/collected-at.txt)' --no-pager" > $F/service-journal.txt 2>&1
