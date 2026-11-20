#!/bin/bash
set -euo pipefail
source /home/tbound/env.sh
cd /home/tbound/tbound-work/f30-src/supervisor
exec 9>/home/tbound/.linux-line.lock; flock -w 7200 9
CH="tbound/supervisor/internal/effectoracle tbound/supervisor/internal/runner tbound/supervisor/internal/providerbridge tbound/supervisor/internal/broker tbound/supervisor/internal/broker/openrouter tbound/supervisor/cmd/tbound"
for c in tbound tbound-profile tbound-cell-entrypoint; do
  d=$(GOFLAGS=-mod=readonly nice -n 19 go list -deps ./cmd/$c)
  hits=""; for p in $CH; do grep -qx "$p" <<<"$d" && hits="$hits ${p#tbound/supervisor/}"; done
  echo "$c changed-deps:${hits:- none}"
done
for p in internal/piruntime; do echo "piruntime imports changed: $(GOFLAGS=-mod=readonly go list -deps ./$p | grep -E 'effectoracle|runner|providerbridge|broker' | tr '\n' ' ')"; done
uname -r
