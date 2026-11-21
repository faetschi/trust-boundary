#!/bin/bash
# F31 round admission checks for one carrier: test-carrier.sh (f31-kit) then corpus-check --quota-profile pilot (F31 tbound-profile), under the Linux-line flock.
# usage: admit-f31.sh <carrier>
set -u
CAR=${1:?carrier}
O=/home/tbound/tbound-work/r31-cc-$CAR
W=/home/tbound/tbound-w.TKn59h; HF=/home/tbound/tbound-h/f31
D=/home/tbound/tbound-work/f31-carriers; C=$D/corpus/sampling/carriers/$CAR
NODE=/home/tbound/toolchains/node-v24.21.0/bin/node
export PATH=/home/tbound/toolchains/node-v24.21.0/bin:$PATH
[ -e $O ] && { echo "exists $O"; exit 2; }
mkdir -p $O $O/tmp; chmod 700 $O/tmp; export TMPDIR=$O/tmp
exec 9>/home/tbound/.linux-line.lock; flock -w 7200 9
echo "start $(date -u +%FT%TZ) carrier HEAD $(git -C $D rev-parse HEAD) dirty $(git -C $D status --porcelain | wc -l) manifest $(sha256sum $C/manifest.draft.json | cut -c1-64)" > $O/tc.out
echo "kit $(git -C /home/tbound/tbound-work/f31-kit rev-parse HEAD) dirty $(git -C /home/tbound/tbound-work/f31-kit status --porcelain | wc -l)" >> $O/tc.out
bash /home/tbound/tbound-work/f31-kit/infra/freeze/tools/test-carrier.sh $W $HF >> $O/tc.out 2>&1; echo "RC=$? end $(date -u +%FT%TZ)" >> $O/tc.out
echo "start $(date -u +%FT%TZ) tbound-profile $(sha256sum $W/bin/tbound-profile | cut -c1-64) TBP_DIGEST $(cat $HF/TBP_DIGEST)" > $O/cc.out
$W/bin/tbound-profile corpus-check --manifest $C/manifest.draft.json --node $NODE --quota-profile pilot --out $O/report >> $O/cc.out 2>&1
echo "RC=$? end $(date -u +%FT%TZ)" >> $O/cc.out
grep -E "^RC=|PASS|FAIL" $O/tc.out | tail -3; tail -3 $O/cc.out
