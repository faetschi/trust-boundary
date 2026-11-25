set -euo pipefail
TS=20261124T1253Z
T=/home/tbound/tbound-work; OH=/home/tbound/tbound-h; NH=/home/tbound/tbh
pgrep -f '[g]o test|[t]ools/freeze.sh' >/dev/null && { echo "FATAL busy"; exit 1; }
[ "$(sed -n 's/^F=//p' $T/env.sh)" = 4e8ac37d8f2cbb5a67a8a5c8b965c8345ece4e36 ] || { echo "FATAL env.sh not the F33 attempt"; exit 1; }
grep -q '^EXIT=1$' $T/run-f33-freeze.out || { echo "FATAL attempt-1 not EXIT=1"; exit 1; }
mv -n -- $T/env.sh $T/env.sh.f33-attempt1-$TS
mv -n -- $OH/f33 $OH/f33.failed-$TS
mv -n -- $OH/cf33 $OH/cf33.failed-$TS
mv -n -- $T/receipts/F33 $T/receipts/F33.failed-$TS
mv -n -- $T/run-f33-freeze.out $T/run-f33-freeze.out.attempt1
mv -n -- $T/run-f33-freeze.out.done $T/run-f33-freeze.out.done.attempt1 2>/dev/null || true
mv -n -- $T/f33-freeze-driver.log $T/f33-freeze-driver.log.attempt1
for k in f33-src f33-kit; do
  e=$T/$k/.local/freeze-host.env
  cp -p -- $e $e.attempt1
  sed -i 's#^OLDH=/home/tbound/tbound-h$#OLDH=/home/tbound/tbh#' $e
  grep -c '^OLDH=/home/tbound/tbh$' $e
done
ls -ld $OH/*.failed-$TS $T/receipts/F33.failed-$TS
ls -d $OH/f33 $OH/cf33 $T/env.sh $T/receipts/F33 2>&1 | sed 's/^/gone? /'
