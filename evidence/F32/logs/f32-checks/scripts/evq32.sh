#!/bin/bash
# F32 EVQ and route reports (the F30 commands, evidence/F30/logs/evq-report/commands.log, with F32 paths), inside the Linux-line lock.
set -uo pipefail
source /home/tbound/env.sh
. /home/tbound/tbound-work/env.sh
H30=/home/tbound/tbound-h/f32; CF=/home/tbound/tbound-h/cf32; R=$H30/evq-report; SRC=/home/tbound/tbound-work/f32-src
F=08f3c3ee4043171b55867dfd2dfe5258809cf40b
[ "$(cat $H30/FROZEN-COMMIT)" = $F ] || { echo FATAL commit; exit 1; }
case $R in /home/tbound/tbound-h/f32/evq-report) rm -rf -- "$R";; *) exit 3;; esac
exec 9>/home/tbound/.linux-line.lock; flock -w 7200 9
mkdir -m 700 $R && cp -a /home/tbound/tbound-h/f31/evq-report/npm-cache $R/npm-cache   # the F30 prepared offline cache; no package-lock.json changed in 5626e34f..08f3c3ee
cp $SRC/infra/conformance/evq-mapping.json $SRC/infra/conformance/route-matrix.json $R/
TBP=$(cat $H30/TBP_DIGEST); RUN=$W/bin/tbound-run
[ "sha256:$(sha256sum $RUN | cut -d' ' -f1)" = "$(cat $H30/TBOUND_RUN_DIGEST)" ] || { echo "FATAL tbound-run digest"; exit 1; }
[ "$(cat $H30/evq/staged.tbound-profile)" = "$TBP" ] || { echo "FATAL staged tbound-profile record != TBP_DIGEST"; exit 1; }
E=$H30/evq; N=/home/tbound/toolchains/node-v24.21.0
run() { local n=$1; shift; echo "$*" >> $R/commands.log; "$@" > $R/$n.stdout 2> $R/$n.stderr; echo $? > $R/$n.exit; echo "$n exit=$(cat $R/$n.exit)"; }
run adapter-capture $RUN evq-adapter-capture --repo $SRC --commit $F --node $N/bin/node --npm-cli $N/lib/node_modules/npm/bin/npm-cli.js --npm-cache $R/npm-cache --out $R/adapter-tests.json
run evq $RUN evq --mapping $R/evq-mapping.json --config $E/ga-run.json --capture $CF --out $R/evq_results.json --guest-binary-sha256 $TBP --artifact adapter-tests=$R/adapter-tests.json --artifact guest-capability=$E/capability.json --artifact guest-tests-procoracle=$E/procoracle.json --artifact guest-tests-landlock-connect=$E/landlock-connect.json --artifact guest-provider-exchange=$E/provider.json --artifact guest-netsink=$E/netsink.json
run route-report $RUN route-report --matrix $R/route-matrix.json --config $E/ga-run.json --capture $CF --out $R/route_report.json --guest-binary-sha256 $TBP --artifact guest-route-bash-view=$E/route-bash-view.json --artifact guest-route-direct-runtime=$E/route-direct-runtime.json

grep -E "^(passed|not_passed|unmapped) " $R/evq.stdout; grep -E "^(cells|covered|pending_guest|missing|complete) " $R/route-report.stdout
