#!/bin/bash
# F33 setup in tbound-dev: Linux checkouts at the freeze commit and the kit commit, F31 env.sh backup. No deletes.
# usage: f33-setup.sh <FREEZE_COMMIT> <KIT_COMMIT>
set -euo pipefail
S=/mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/300b9e68-97e7-4a01-aa27-ad0df38df07f/scratchpad/f31w
T=/home/tbound/tbound-work
F=${1:?}; K=${2:?}
[[ $F =~ ^[0-9a-f]{40}$ && $K =~ ^[0-9a-f]{40}$ ]] || { echo "FATAL: bad commit args"; exit 1; }
for d in $T/f33-src $T/f33-kit $T/env.f32.sh $T/f33.bundle $T/f33-kit.bundle; do [ ! -e "$d" ] || { echo "FATAL: $d exists"; exit 1; }; done
grep -q '^F=08f3c3ee4043171b55867dfd2dfe5258809cf40b$' $T/env.sh || { echo "FATAL: env.sh is not the F32 env"; exit 1; }
cp -p $T/env.sh $T/env.f32.sh
cp -- $S/f33.bundle $T/f33.bundle; cp -- $S/f33-kit.bundle $T/f33-kit.bundle
git -c core.autocrlf=false init -q $T/f33-src
git -C $T/f33-src -c core.autocrlf=false fetch -q $T/f33.bundle refs/heads/f33-main:refs/heads/f33-main
git -C $T/f33-src -c core.autocrlf=false checkout -q --detach $F
git -c core.autocrlf=false init -q $T/f33-kit
git -C $T/f33-kit -c core.autocrlf=false fetch -q $T/f33-kit.bundle refs/heads/f33-kit:refs/heads/f33-kit
git -C $T/f33-kit -c core.autocrlf=false checkout -q --detach $K
for d in f33-src f33-kit; do git -C $T/$d config core.autocrlf false; mkdir -p $T/$d/.local; cp -p $T/f32-src/.local/freeze-host.env $T/$d/.local/; done
cp -p $T/f33-kit/infra/freeze/freezes/F33.env $T/F33.env.in
cp -p $T/F33.env.in $T/f33-src/infra/freeze/freezes/F33.env
git -C $T/f33-src log --oneline -1; git -C $T/f33-kit log --oneline -1
echo "src tree $(git -C $T/f33-src rev-parse HEAD^{tree})"
git -C $T/f33-src status --short
git -C $T/f33-src diff --quiet HEAD && echo "src clean vs HEAD (F33.env untracked)"
git -C $T/f33-kit diff --quiet $F -- . ':!infra/freeze' && echo "kit == freeze outside infra/freeze" || git -C $T/f33-kit diff --stat $F -- . ':!infra/freeze'
