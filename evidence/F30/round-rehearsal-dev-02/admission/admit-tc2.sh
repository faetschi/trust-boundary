#!/bin/bash
# Second test-carrier.sh run: the phase-2 dry run compares against $HF/pilot, which now holds the rehearsal-dev-01 round, not the T5 run it expects.
# Read-only view of the F30 records with pilot -> the T5 run renamed aside on 2026-11-18 (pilot.t5-20261118T131323Z), as $HF/pilot was in the rehearsal-dev-01 admission.
set -u
O=/home/tbound/tbound-work/round30b-cc; H=/home/tbound/tbound-h/f30; W=/home/tbound/tbound-w.9Gnmqk
V=/home/tbound/tbound-work/round30b-tmp/hf-t5view
export PATH=/home/tbound/toolchains/node-v24.21.0/bin:$PATH TMPDIR=/home/tbound/tbound-work/round30b-tmp
mkdir -m 700 $V || exit 2
ln -s $H/pilot.t5-20261118T131323Z $V/pilot; for f in d09-cell.base.json BUNDLE_DIGEST NODE_DIGEST; do ln -s $H/$f $V/$f; done
{ echo "start $(date -u +%FT%TZ) view $V: $(ls -l $V | tail -n +2 | awk '{print $9,$10,$11}' | tr '\n' ';')"
  bash /home/tbound/tbound-work/f30-kit/infra/freeze/tools/test-carrier.sh $W $V; echo "RC=$? end $(date -u +%FT%TZ)"; } > $O/tc2.out 2>&1
touch $O/tc2.done
