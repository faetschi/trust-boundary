#!/bin/bash
# vfix-test.sh <commit> <label>: fetch the fix branch commit from GitHub into a WSL checkout and run the V forwarder tests (Linux-line lock).
set -uo pipefail
C=${1:?commit}; N=${2:?label}
D=/home/tbound/tbound-work/f34-vfix
[ -d $D/.git ] || { git -c core.autocrlf=false init -q $D; git -C $D config core.autocrlf false; git -C $D config core.eol lf; }
git -C $D -c core.autocrlf=false fetch -q https://github.com/faetschi/trust-boundary.git fix/v-forwarder-bash-no-outputs || exit 2
git -C $D -c core.autocrlf=false checkout -q --detach $C || exit 2
echo "HEAD $(git -C $D rev-parse HEAD) dirty=$(git -C $D status --porcelain | wc -l) cr=$(grep -rlI $'\r' $D/supervisor/cmd/tbound | wc -l)"
source /home/tbound/env.sh
cd $D/supervisor
echo "start $(date -u +%FT%TZ) $N"
flock -w 3600 /home/tbound/.linux-line.lock nice -n 10 go test -count=1 -timeout 20m -run 'TestVForwarderCommandRequestPassesTheCorpusAdmissionScreen|TestVForwarderClientRunsUnderNoNewPrivs|TestVRoleAdmission|TestServe' -v ./cmd/tbound/ 2>&1 | grep -E "^(=== RUN|--- |ok|FAIL|PASS|panic)|_test.go:[0-9]+:" | head -60
echo "rc=${PIPESTATUS[0]} end $(date -u +%FT%TZ)"
