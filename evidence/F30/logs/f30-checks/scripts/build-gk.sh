#!/bin/bash
# Test binaries for the guest-kernel run of effectoracle and publication (F30 freeze tree, same flags as the guest-tests capture: -trimpath).
set -euo pipefail
source /home/tbound/env.sh
O=/home/tbound/tbound-work/f30-guestkernel
[ ! -e $O ] || { echo "FATAL: $O exists"; exit 1; }
mkdir -m 700 $O
cd /home/tbound/tbound-work/f30-src/supervisor
[ "$(git rev-parse HEAD)" = 5626e34fdbf40983949cde67f17238b82800665b ] && git diff --quiet HEAD || { echo FATAL src; exit 1; }
exec 9>/home/tbound/.linux-line.lock; flock -w 7200 9
for p in effectoracle publication; do GOFLAGS=-mod=readonly nice -n 19 go test -c -trimpath -o $O/$p.test ./internal/$p; done
sha256sum $O/*.test
