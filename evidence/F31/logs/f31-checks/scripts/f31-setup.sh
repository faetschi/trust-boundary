#!/bin/bash
# F31 setup in tbound-dev: Linux checkouts at the freeze commit and the kit commit, F30 env.sh backup. No deletes.
# usage: f31-setup.sh <FREEZE_COMMIT> <KIT_COMMIT>
set -euo pipefail
S=/mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/300b9e68-97e7-4a01-aa27-ad0df38df07f/scratchpad/f31w
T=/home/tbound/tbound-work
F=${1:?}; K=${2:?}
[[ $F =~ ^[0-9a-f]{40}$ && $K =~ ^[0-9a-f]{40}$ ]] || { echo "FATAL: bad commit args"; exit 1; }
for d in $T/f31-src $T/f31-kit $T/env.f30.sh $T/f31.bundle $T/f31-kit.bundle; do [ ! -e "$d" ] || { echo "FATAL: $d exists"; exit 1; }; done
grep -q '^F=5626e34fdbf40983949cde67f17238b82800665b$' $T/env.sh || { echo "FATAL: env.sh is not the F30 env"; exit 1; }
cp -p $T/env.sh $T/env.f30.sh
cp -- $S/f31.bundle $T/f31.bundle; cp -- $S/f31-kit.bundle $T/f31-kit.bundle
git -c core.autocrlf=false init -q $T/f31-src
git -C $T/f31-src -c core.autocrlf=false fetch -q $T/f31.bundle refs/heads/f31-main:refs/heads/f31-main
git -C $T/f31-src -c core.autocrlf=false checkout -q --detach $F
git -c core.autocrlf=false init -q $T/f31-kit
git -C $T/f31-kit -c core.autocrlf=false fetch -q $T/f31-kit.bundle refs/heads/f31-kit:refs/heads/f31-kit
git -C $T/f31-kit -c core.autocrlf=false checkout -q --detach $K
for d in f31-src f31-kit; do git -C $T/$d config core.autocrlf false; mkdir -p $T/$d/.local; cp -p $T/f30-src/.local/freeze-host.env $T/$d/.local/; done
cp -p $T/f31-kit/infra/freeze/freezes/F31.env $T/F31.env.in
cp -p $T/F31.env.in $T/f31-src/infra/freeze/freezes/F31.env
git -C $T/f31-src log --oneline -1; git -C $T/f31-kit log --oneline -1
echo "src tree $(git -C $T/f31-src rev-parse HEAD^{tree})"
git -C $T/f31-src status --short
git -C $T/f31-src diff --quiet HEAD && echo "src clean vs HEAD (F31.env untracked)"
git -C $T/f31-kit diff --quiet $F -- . ':!infra/freeze' && echo "kit == freeze outside infra/freeze" || git -C $T/f31-kit diff --stat $F -- . ':!infra/freeze'
