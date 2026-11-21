#!/bin/bash
# F32 setup in tbound-dev: Linux checkouts at the freeze commit and the kit commit, F31 env.sh backup. No deletes.
# usage: f32-setup.sh <FREEZE_COMMIT> <KIT_COMMIT>
set -euo pipefail
S=/mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/300b9e68-97e7-4a01-aa27-ad0df38df07f/scratchpad/f31w
T=/home/tbound/tbound-work
F=${1:?}; K=${2:?}
[[ $F =~ ^[0-9a-f]{40}$ && $K =~ ^[0-9a-f]{40}$ ]] || { echo "FATAL: bad commit args"; exit 1; }
for d in $T/f32-src $T/f32-kit $T/env.f31.sh $T/f32.bundle $T/f32-kit.bundle; do [ ! -e "$d" ] || { echo "FATAL: $d exists"; exit 1; }; done
grep -q '^F=cc1de6dfe4c5a53494d45ac2878506a0c0ffa150$' $T/env.sh || { echo "FATAL: env.sh is not the F31 env"; exit 1; }
cp -p $T/env.sh $T/env.f31.sh
cp -- $S/f32.bundle $T/f32.bundle; cp -- $S/f32-kit.bundle $T/f32-kit.bundle
git -c core.autocrlf=false init -q $T/f32-src
git -C $T/f32-src -c core.autocrlf=false fetch -q $T/f32.bundle refs/heads/f32-main:refs/heads/f32-main
git -C $T/f32-src -c core.autocrlf=false checkout -q --detach $F
git -c core.autocrlf=false init -q $T/f32-kit
git -C $T/f32-kit -c core.autocrlf=false fetch -q $T/f32-kit.bundle refs/heads/f32-kit:refs/heads/f32-kit
git -C $T/f32-kit -c core.autocrlf=false checkout -q --detach $K
for d in f32-src f32-kit; do git -C $T/$d config core.autocrlf false; mkdir -p $T/$d/.local; cp -p $T/f31-src/.local/freeze-host.env $T/$d/.local/; done
cp -p $T/f32-kit/infra/freeze/freezes/F32.env $T/F32.env.in
cp -p $T/F32.env.in $T/f32-src/infra/freeze/freezes/F32.env
git -C $T/f32-src log --oneline -1; git -C $T/f32-kit log --oneline -1
echo "src tree $(git -C $T/f32-src rev-parse HEAD^{tree})"
git -C $T/f32-src status --short
git -C $T/f32-src diff --quiet HEAD && echo "src clean vs HEAD (F32.env untracked)"
git -C $T/f32-kit diff --quiet $F -- . ':!infra/freeze' && echo "kit == freeze outside infra/freeze" || git -C $T/f32-kit diff --stat $F -- . ':!infra/freeze'
