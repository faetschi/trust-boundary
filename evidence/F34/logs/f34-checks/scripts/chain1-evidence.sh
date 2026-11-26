#!/bin/bash
# F34 chain 1 evidence off the guest before the chain-2 restore (owner condition 1): read-only collection, sha256 on the guest, tar, verify on the host.
set -u
K=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb; O=/f/trust-boundary/.local/f34-chain1-evidence
export GUEST_IP=172.29.57.244 TBOUND_LOCAL=/f/trust-boundary/.local MSYS_NO_PATHCONV=1
[ ! -e $O ] || { echo "exists $O"; exit 2; }; mkdir -p $O
G(){ timeout 300 sh $K/infra/freeze/tools/gssh.sh "$@" </dev/null; }; OP='sudo -n /opt/tbound/operator/tbound-op'
date -u +%FT%TZ > $O/collected-at.txt
{ G "$OP collect requests"; echo "rq=$?"; G "$OP collect catalog"; echo "cat=$?"; G "$OP collect l4" > $O/l4-stdout.txt; echo "l4=$?"
  for r in production-run-17a51f41fddc4d2a75870579 production-run-dcf2532f6d0997dae8ba0f8e; do G "$OP collect audit $r" > /dev/null; echo "audit $r=$?"; done; } > $O/collect.txt 2>&1
G "journalctl -u tbound-pi.service -u tbound-pi.socket --since '2026-11-24 21:50:00' --no-pager -o short-iso > /home/tboundadmin/f34-chain1-service-journal.txt; sha256sum /home/tboundadmin/f34-chain1-service-journal.txt" > $O/journal-guest-sha256.txt 2>&1
G "cat /home/tboundadmin/f34-chain1-service-journal.txt" > $O/service-journal.txt
G "ls -la /var/tmp/tbound-op/ /var/tmp/tbound-op/rp /var/tmp/tbound-op/g1 2>&1" > $O/guest-listing.txt 2>&1
G "cd /var/tmp/tbound-op && find _ rp g1 -type f -print0 2>/dev/null | sort -z | xargs -0 sha256sum" > $O/guest-sha256.txt 2>&1
G "cd /var/tmp/tbound-op && tar -cf - _ rp g1 2>/dev/null" > $O/guest-collect.tar 2> $O/guest-collect.err
mkdir -p $O/files && tar -C $O/files -xf $O/guest-collect.tar
( cd $O/files && sha256sum -c --quiet ../guest-sha256.txt ) > $O/guest-sha256-verify.txt 2>&1; echo "verify rc=$?" >> $O/guest-sha256-verify.txt
( cd $O && echo "$(sha256sum service-journal.txt | cut -d' ' -f1)  guest: $(cut -d' ' -f1 journal-guest-sha256.txt)" ) > $O/journal-verify.txt
( cd $O && sha256sum guest-collect.tar ) > $O/COLLECTION-SUMS
echo "files=$(wc -l < $O/guest-sha256.txt)"; cat $O/collect.txt; tail -1 $O/guest-sha256-verify.txt; cat $O/journal-verify.txt
