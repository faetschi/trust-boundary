#!/bin/bash
# vcap-test.sh <commit> <label>: fetch fix/v-request-cap at <commit> from GitHub into a WSL checkout; run the V service and forwarder tests (Linux-line lock).
set -uo pipefail
C=${1:?commit}; N=${2:?label}
D=/home/tbound/tbound-work/f34-vcap
[ -d $D/.git ] || { git -c core.autocrlf=false init -q $D; git -C $D config core.autocrlf false; git -C $D config core.eol lf; }
git -C $D -c core.autocrlf=false fetch -q https://github.com/faetschi/trust-boundary.git fix/v-request-cap || exit 2
git -C $D -c core.autocrlf=false checkout -q --detach $C || exit 2
echo "HEAD $(git -C $D rev-parse HEAD) dirty=$(git -C $D status --porcelain | wc -l) cr=$(grep -rlI $'\r' $D/supervisor/cmd/tbound $D/supervisor/internal/researchroute | wc -l)"
source /home/tbound/env.sh
cd $D/supervisor
echo "start $(date -u +%FT%TZ) $N"
run() { flock -w 3600 /home/tbound/.linux-line.lock nice -n 10 go test -race -count=1 -timeout 20m "$@" 2>&1 | grep -E "^(--- FAIL|--- PASS|ok|FAIL|PASS|panic)|_test.go:[0-9]+:" | head -80; echo "rc=${PIPESTATUS[0]}"; }
run -v -run 'TestVService|TestVForwarder|TestVRoleAdmission|TestServeV' ./cmd/tbound/
run ./internal/researchroute/ ./internal/broker/openrouter/
echo "end $(date -u +%FT%TZ)"
