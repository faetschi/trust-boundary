set -eu
H=/home/tbound/tbound-h/f29; W=/home/tbound/tbound-w.qWsOns
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
mkdir -p $T/bin $T/inputs $T/vals
cp $W/bin/tbound-run.exe $T/bin/; cp $H/pilot/manifest.json $H/evq-report/evq_results.json $H/evq-report/route_report.json $T/inputs/
cp -r $H/pilot/carrier/artifacts $T/inputs/artifacts
cp $H/evq/ga-run.json $T/vals/
for f in FROZEN-COMMIT TBOUND_DIGEST TBOUND_RUN_EXE_DIGEST SRC_DIGEST BUNDLE_DIGEST IMGDIGEST BASE_TD ADMIN_PUB_DIGEST pilot/PROFILE_DIGEST pilot/TD pilot/CORPUS_DIGEST pilot/CARRIER; do echo "$f=$(cat $H/$f)"; done > $T/vals/values.txt
tail -4 $H/evq-report/evq.stdout >> $T/vals/values.txt; tail -2 $H/evq-report/route-report.stdout >> $T/vals/values.txt
( cd $T && find bin inputs vals -type f | sort | xargs sha256sum ) > $T/SUMS
tar -C $T -cf - bin inputs vals SUMS
