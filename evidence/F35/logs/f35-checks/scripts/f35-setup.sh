#!/bin/bash
# F35 setup in tbound-dev: Linux checkouts at the freeze commit and the kit commit (fetched from GitHub), F34 env.sh backup. No deletes.
# usage: f35-setup.sh <FREEZE_COMMIT> <KIT_COMMIT>
set -euo pipefail
T=/home/tbound/tbound-work; U=https://github.com/faetschi/trust-boundary.git
F=${1:?}; K=${2:?}
[[ $F =~ ^[0-9a-f]{40}$ && $K =~ ^[0-9a-f]{40}$ ]] || { echo "FATAL: bad commit args"; exit 1; }
for d in $T/f35-src $T/f35-kit $T/env.f34.sh $T/F35.env.in; do [ ! -e "$d" ] || { echo "FATAL: $d exists"; exit 1; }; done
grep -q '^F=8627adeb4ab3fb01728136d8b4995bf8d7c07b41$' $T/env.sh || { echo "FATAL: env.sh is not the F34 env"; exit 1; }
cp -p $T/env.sh $T/env.f34.sh
for d in f35-src f35-kit; do git -c core.autocrlf=false init -q $T/$d; git -C $T/$d config core.autocrlf false; git -C $T/$d config core.eol lf; done
git -C $T/f35-src -c core.autocrlf=false fetch -q $U main
git -C $T/f35-src -c core.autocrlf=false checkout -q --detach $F
git -C $T/f35-kit -c core.autocrlf=false fetch -q $U codex/tbound-prototype
git -C $T/f35-kit -c core.autocrlf=false checkout -q --detach $K
for d in f35-src f35-kit; do mkdir -p $T/$d/.local; cp -p $T/f34-src/.local/freeze-host.env $T/$d/.local/; grep -q '^OLDH=/home/tbound/tbh$' $T/$d/.local/freeze-host.env; done
cp -p $T/f35-kit/infra/freeze/freezes/F35.env $T/F35.env.in
cp -p $T/F35.env.in $T/f35-src/infra/freeze/freezes/F35.env
git -C $T/f35-src log --oneline -1; git -C $T/f35-kit log --oneline -1
echo "src tree $(git -C $T/f35-src rev-parse HEAD^{tree})"
git -C $T/f35-src status --short
git -C $T/f35-src diff --quiet HEAD && echo "src clean vs HEAD (F35.env untracked)"
echo "CR files in src: $(grep -rlI $'\r' $T/f35-src/infra $T/f35-src/supervisor 2>/dev/null | wc -l)"
git -C $T/f35-kit diff --stat $F -- infra/freeze
