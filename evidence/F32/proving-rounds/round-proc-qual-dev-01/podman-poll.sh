#!/bin/bash
# Read-only: the guest service's container list every 15 s while the round runs (no-reap container life after Run returns).
M=/f/trust-boundary/.local/round-proc-qual-dev-01-f32; export GUEST_IP=172.18.247.10 TBOUND_LOCAL=/f/trust-boundary/.local MSYS_NO_PATHCONV=1
G=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb/infra/freeze/tools/gssh.sh
end=$(( $(date +%s) + 7200 ))
while [ $(date +%s) -lt $end ]; do
  echo "== $(date -u +%FT%TZ)"; timeout 40 sh $G 'sudo -n /opt/tbound/operator/tbound-op diag podman-ps' < /dev/null 2>&1 | tr -d '\r' | grep -v "^CONTAINER ID"
  grep -q '^finished true' $M/runs/chunk-*.txt 2>/dev/null && break
  sleep 15
done >> $M/diag/podman-poll.txt
