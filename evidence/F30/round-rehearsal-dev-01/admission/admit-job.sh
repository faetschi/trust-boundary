#!/bin/bash
# F30 round admission checks: test-carrier.sh (f30-kit) then corpus-check --quota-profile pilot (F30 tbound-profile), one job under the Linux-line flock.
set -u
O=/home/tbound/tbound-work/round30-cc
W=/home/tbound/tbound-w.9Gnmqk; HF=/home/tbound/tbound-h/f30
C=/home/tbound/tbound-work/round-carrier/corpus/sampling/carriers/rehearsal-dev-01
NODE=/home/tbound/toolchains/node-v24.21.0/bin/node
export PATH=/home/tbound/toolchains/node-v24.21.0/bin:$PATH
mkdir -p /home/tbound/tbound-work/round30-tmp; chmod 700 /home/tbound/tbound-work/round30-tmp; export TMPDIR=/home/tbound/tbound-work/round30-tmp
echo "start $(date -u +%FT%TZ) carrier HEAD $(git -C /home/tbound/tbound-work/round-carrier rev-parse HEAD) dirty $(git -C /home/tbound/tbound-work/round-carrier status --porcelain | wc -l)" > $O/tc.out
echo "kit $(git -C /home/tbound/tbound-work/f30-kit rev-parse HEAD) dirty $(git -C /home/tbound/tbound-work/f30-kit status --porcelain | wc -l)" >> $O/tc.out
bash /home/tbound/tbound-work/f30-kit/infra/freeze/tools/test-carrier.sh $W $HF >> $O/tc.out 2>&1; echo "RC=$? end $(date -u +%FT%TZ)" >> $O/tc.out
echo "start $(date -u +%FT%TZ) tbound-profile $(sha256sum $W/bin/tbound-profile | cut -c1-64)" > $O/cc.out
$W/bin/tbound-profile corpus-check --manifest $C/manifest.draft.json --node $NODE --quota-profile pilot --out $O/report >> $O/cc.out 2>&1
echo "RC=$? end $(date -u +%FT%TZ)" >> $O/cc.out
touch $O/cc.done
