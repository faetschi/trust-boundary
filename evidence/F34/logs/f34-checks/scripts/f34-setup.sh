#!/bin/bash
# F34 setup in tbound-dev: Linux checkouts at the freeze commit and the kit commit (fetched from GitHub), F33 env.sh backup. No deletes.
# usage: f34-setup.sh <FREEZE_COMMIT> <KIT_COMMIT>
set -euo pipefail
T=/home/tbound/tbound-work; U=https://github.com/faetschi/trust-boundary.git
F=${1:?}; K=${2:?}
[[ $F =~ ^[0-9a-f]{40}$ && $K =~ ^[0-9a-f]{40}$ ]] || { echo "FATAL: bad commit args"; exit 1; }
for d in $T/f34-src $T/f34-kit $T/env.f33.sh; do [ ! -e "$d" ] || { echo "FATAL: $d exists"; exit 1; }; done
grep -q '^F=4e8ac37d8f2cbb5a67a8a5c8b965c8345ece4e36$' $T/env.sh || { echo "FATAL: env.sh is not the F33 env"; exit 1; }
cp -p $T/env.sh $T/env.f33.sh
for d in f34-src f34-kit; do git -c core.autocrlf=false init -q $T/$d; git -C $T/$d config core.autocrlf false; git -C $T/$d config core.eol lf; done
git -C $T/f34-src -c core.autocrlf=false fetch -q $U main
git -C $T/f34-src -c core.autocrlf=false checkout -q --detach $F
git -C $T/f34-kit -c core.autocrlf=false fetch -q $U codex/tbound-prototype
git -C $T/f34-kit -c core.autocrlf=false checkout -q --detach $K
for d in f34-src f34-kit; do mkdir -p $T/$d/.local; cp -p $T/f33-src/.local/freeze-host.env $T/$d/.local/; grep -q '^OLDH=/home/tbound/tbh$' $T/$d/.local/freeze-host.env; done
cp -p $T/f34-kit/infra/freeze/freezes/F34.env $T/F34.env.in
cp -p $T/F34.env.in $T/f34-src/infra/freeze/freezes/F34.env
git -C $T/f34-src log --oneline -1; git -C $T/f34-kit log --oneline -1
echo "src tree $(git -C $T/f34-src rev-parse HEAD^{tree})"
git -C $T/f34-src status --short
git -C $T/f34-src diff --quiet HEAD && echo "src clean vs HEAD (F34.env untracked)"
echo "CR files in src: $(grep -rlI $'\r' $T/f34-src/infra $T/f34-src/supervisor 2>/dev/null | wc -l)"
git -C $T/f34-kit diff --quiet $F -- infra/freeze ':!infra/freeze/freezes' ':!infra/freeze/guest/05-runtime-swap.sh' ':!infra/freeze/README.md' && echo "kit == freeze in infra/freeze except values, 05 pair, README" || git -C $T/f34-kit diff --stat $F -- infra/freeze
