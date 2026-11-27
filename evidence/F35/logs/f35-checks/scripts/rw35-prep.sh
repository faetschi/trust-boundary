#!/bin/bash
# rw35-prep.sh <round R> <carrier commit> <manifest sha256 hex> <aside label>: ROUND.md steps 1 to 4c for one F35 re-witness round
# (carrier checkout, aside, 33 in carrier mode, pack, run.json, plan checks, clock probe, leftover check, start.sh and resume.sh).
# It stops before the runner start; start.sh is started detached from PowerShell.
set -euo pipefail
case "$-" in *x*) echo "refusing xtrace"; exit 1;; esac
R=${1:?round}; C=${2:?carrier commit}; MD=${3:?manifest sha256 hex}; PREV=${4:?aside label}
export MSYS_NO_PATHCONV=1 GUEST_IP=172.29.57.244 TBOUND_LOCAL=/f/trust-boundary/.local
K=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
T=/f/trust-boundary/.local/f35-rewitness-tools
M=/f/trust-boundary/.local/round-$R-f35
[ ! -e $M ] || { echo "FATAL: $M exists"; exit 2; }
[ "$(git -C "$(cygpath -w $K)" rev-parse HEAD)" = 09d4a5fee0fa9e8e401323abebad20eb877d09a9 ] || { echo "FATAL: kit not 09d4a5fe"; exit 2; }
[ "$(tasklist /FI "IMAGENAME eq tbound-run.exe" | grep -c tbound-run.exe || true)" = 0 ] || { echo "FATAL: a tbound-run.exe is running"; exit 2; }
echo "== 1a guest"; timeout 60 sh $K/infra/freeze/tools/gssh.sh 'id -un; uname -r; sudo -n true && echo BROAD-SUDO || echo narrowed' </dev/null
echo "== 2 carrier checkout at $C"
wsl.exe -d tbound-dev --cd / --exec bash -s -- "$C" "$R" "$MD" <<'EOF' | tr -d '\r'
set -eu; D=/home/tbound/tbound-work/f33-rw-carriers; C=$1; R=$2; MD=$3
git -C $D -c core.autocrlf=false cat-file -e "$C^{commit}" 2>/dev/null || git -C $D -c core.autocrlf=false fetch -q https://github.com/faetschi/trust-boundary.git pilot-walk
git -C $D -c core.autocrlf=false checkout -q --detach $C
[ "$(git -C $D rev-parse HEAD)" = "$C" ] && [ "$(git -C $D status --porcelain | wc -l)" = 0 ] || { echo "FATAL: carrier checkout"; exit 1; }
[ "$(sha256sum $D/corpus/sampling/carriers/$R/manifest.draft.json | cut -d' ' -f1)" = "$MD" ] || { echo "FATAL: manifest digest"; exit 1; }
[ "$(grep -rlI $'\r' $D/corpus/sampling/carriers/$R | wc -l)" = 0 ] || { echo "FATAL: CR in carrier"; exit 1; }
echo "carrier $R at $C, manifest $MD, no CR"
EOF
echo "== 1 aside"; bash $T/rw35-aside.sh $R $PREV
echo "== 3 33 in carrier mode"; bash $T/rw35-33.sh $M $R $MD $C 1
[ "$(grep -c 'CYCLE_OK = ok' $M/logs/33-pass1.console.txt)" = 1 ] || { echo "FATAL: no CYCLE_OK in 33 pass 1"; grep -n 'FATAL\|STOP' $M/logs/33-pass1.console.txt | head; exit 3; }
echo "== 4a pack"
wsl.exe -d tbound-dev --cd / --exec bash -s > $M/logs/pack.tar 2> $M/logs/pack.err <<'EOF'
set -eu
H=/home/tbound/tbh/f35; W=$(sed -n "s/^W=//p" /home/tbound/tbound-work/env.sh | head -1); [ -n "$W" ] || exit 2
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
mkdir -p $T/bin $T/inputs $T/vals
cp $W/bin/tbound-run.exe $T/bin/; cp $H/pilot/manifest.json $H/evq-report/evq_results.json $H/evq-report/route_report.json $T/inputs/
cp -r $H/pilot/carrier/artifacts $T/inputs/artifacts
cp $H/evq/ga-run.json $T/vals/
for f in FROZEN-COMMIT TBOUND_DIGEST TBOUND_RUN_EXE_DIGEST SRC_DIGEST BUNDLE_DIGEST IMGDIGEST BASE_TD ADMIN_PUB_DIGEST pilot/PROFILE_DIGEST pilot/TD pilot/CORPUS_DIGEST pilot/CARRIER; do echo "$f=$(cat $H/$f)"; done > $T/vals/values.txt
tail -4 $H/evq-report/evq.stdout >> $T/vals/values.txt; tail -2 $H/evq-report/route-report.stdout >> $T/vals/values.txt
( cd $T && find bin inputs vals -type f | sort | xargs sha256sum ) > $T/SUMS
tar -C $T -cf - bin inputs vals SUMS
EOF
tar -C $M -xf $M/logs/pack.tar && (cd $M && sha256sum -c --quiet SUMS && echo sums-ok)
cp /f/trust-boundary/.local/round-rehearsal-dev-02-f30/known_hosts $M/known_hosts
grep -qx "pilot/CARRIER=$R $C sha256:$MD" $M/vals/values.txt && echo "values CARRIER == $R $C" || { echo "FATAL: CARRIER record"; grep pilot/CARRIER $M/vals/values.txt; exit 4; }
echo "== 4b run.json and plan"
py -3 -I "$(cygpath -w $T/rw35-mkrun.py)" "$(cygpath -m $M)" 300 172.29.57.244 construct-witness
(cd $M && ./bin/tbound-run.exe plan --config run.json > logs/plan.txt 2> logs/plan.err); echo "plan rc=$?"; head -14 $M/logs/plan.txt; cat $M/logs/plan.err
echo "== 4c clock and leftover"
for f in start.sh resume.sh clock-probe.sh scripts/collect.sh scripts/collect-audits.sh scripts/collect-sj.sh scripts/decode-run-ids.js; do mkdir -p "$(dirname $M/$f)"; sed "s/@R@/$R/g" $T/round-template/$f > $M/$f; done
mkdir -p $M/runs $M/diag
bash $M/clock-probe.sh 172.29.57.244 40 8 $M/diag/clock-pre-run.txt
awk -F'offset_guest_minus_host=' '/^host=/{split($2,a," "); print a[1]}' $M/diag/clock-pre-run.txt | sort -n | sed -n '1p;$p'
timeout 90 sh $K/infra/freeze/tools/gssh.sh 'sudo -n /opt/tbound/operator/tbound-op diag podman-ps' < /dev/null > $M/diag/leftover-pre-run.txt 2>&1 || true
echo "leftover tbound-(accept|v)- lines: $(grep -cE 'tbound-(accept|v)-' $M/diag/leftover-pre-run.txt || true)"
bash -n $M/start.sh && bash -n $M/resume.sh && echo "PREP_OK $R $(date -u +%FT%TZ)"
