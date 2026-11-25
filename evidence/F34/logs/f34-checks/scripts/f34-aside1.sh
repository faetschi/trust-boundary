set -euo pipefail
TS=20261124T2050Z; T=/home/tbound/tbound-work; H=/home/tbound/tbh
pgrep -f '[t]ools/freeze.sh|[g]o test' >/dev/null && { echo "FATAL busy"; exit 1; }
[ "$(sed -n 's/^F=//p' $T/env.sh)" = 8627adeb4ab3fb01728136d8b4995bf8d7c07b41 ] || { echo "FATAL env.sh not the F34 attempt"; exit 1; }
[ ! -e $H/cf34 ] || { echo "FATAL cf34 exists"; exit 1; }
mv -n -- $T/env.sh $T/env.sh.f34-attempt1-$TS
mv -n -- $H/f34 $H/f34.failed-$TS
mv -n -- $T/receipts/F34 $T/receipts/F34.failed-$TS
mv -n -- $T/run-f34-freeze.out $T/run-f34-freeze.out.attempt1
[ -e $T/run-f34-freeze.out.done ] && mv -n -- $T/run-f34-freeze.out.done $T/run-f34-freeze.out.done.attempt1 || true
mv -n -- $T/f34-freeze-driver.log $T/f34-freeze-driver.log.attempt1
ls -d $H/f34* $T/receipts/F34* $T/env.sh* | tail -6
