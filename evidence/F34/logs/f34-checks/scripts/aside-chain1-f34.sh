#!/bin/bash
# Move the F34 guest-chain-1 owner-side records and receipt logs aside (nothing deleted) before chain 2 on the restored pre-F31 guest (F31 precedent).
set -eu
H=/home/tbound/tbh/f34; R=/home/tbound/tbound-work/receipts/F34; u=chain1-20261124
A=$H/$u; [ ! -e $A ] || { echo "exists $A"; exit 2; }
mkdir $A
for f in g1-prior CONTAINERS_CONF_DIGEST STORAGE_CONF_DIGEST xattr-measure-runtime.json XATTR_MOPT XATTR_MAGIC LANDLOCK_DIGEST XATTR_VIS_DIGEST CGROUP_ROOT_DIGEST d09-cell.base.json BASE_TD d09-xattr-enumeration-calibration-readonly.json d09-xattr-enumeration-calibration-readonly.ed25519 CAL_RO_DIGEST d09-xattr-enumeration-calibration.json d09-xattr-enumeration-calibration.ed25519 CAL_RW_DIGEST cal-ro.unsigned.json cal-rw.unsigned.json R1-MODE g1 rp dr evq evq-report pilot; do
  [ -e $H/$f ] && mv -n -- $H/$f $A/ ; done
echo "moved $(ls $A | wc -l) items to $A"; find $H -maxdepth 1 -newermt "2026-11-24 21:50:50Z" ! -path $A -printf "%p\n" | grep -v "^$H$" || echo "no chain-1 item left at the top level"
[ -e $R/logs.$u ] && { echo "exists $R/logs.$u"; exit 2; }
mv -n -- $R/logs $R/logs.$u && mkdir -m 700 $R/logs && cp -p $R/logs.$u/console-* $R/logs.$u/10-freeze.log $R/logs.$u/20-p1.log $R/logs.$u/25-p2-rebuild.log $R/logs.$u/30-p3.log $R/logs.$u/35-evq-capture.log $R/logs.$u/40-sign.log $R/logs.$u/session-env.log $R/logs/ 2>/dev/null; ls $R/logs | wc -l
