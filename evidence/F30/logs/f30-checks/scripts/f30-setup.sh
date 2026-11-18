#!/bin/bash
# F30 setup in tbound-dev: Linux checkouts at the freeze commit and the kit commit, F29 env.sh backup. No deletes.
# usage: f30-setup.sh <FREEZE_COMMIT> <KIT_COMMIT>
set -euo pipefail
S=/mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/5885a9d4-6837-49d0-8bb9-191b48098c96/scratchpad/f30w
T=/home/tbound/tbound-work
F=${1:?}; K=${2:?}
[[ $F =~ ^[0-9a-f]{40}$ && $K =~ ^[0-9a-f]{40}$ ]] || { echo "FATAL: bad commit args"; exit 1; }
for d in $T/f30-src $T/f30-kit $T/env.f29.sh $T/f30.bundle $T/f30-kit.bundle; do [ ! -e "$d" ] || { echo "FATAL: $d exists"; exit 1; }; done
grep -q '^F=28f407b84291222f386134c95620c71114f6b0ed$' $T/env.sh || { echo "FATAL: env.sh is not the F29 env"; exit 1; }
cp -p $T/env.sh $T/env.f29.sh
cp -- $S/f30.bundle $T/f30.bundle; cp -- $S/f30-kit.bundle $T/f30-kit.bundle
git -c core.autocrlf=false init -q $T/f30-src
git -C $T/f30-src -c core.autocrlf=false fetch -q $T/f30.bundle refs/heads/f30-main:refs/heads/f30-main
git -C $T/f30-src -c core.autocrlf=false checkout -q --detach $F
git -c core.autocrlf=false init -q $T/f30-kit
git -C $T/f30-kit -c core.autocrlf=false fetch -q $T/f30-kit.bundle refs/heads/f30-kit:refs/heads/f30-kit
git -C $T/f30-kit -c core.autocrlf=false checkout -q --detach $K
for d in f30-src f30-kit; do git -C $T/$d config core.autocrlf false; mkdir -p $T/$d/.local; cp -p $T/f29-src/.local/freeze-host.env $T/$d/.local/; done
cp -p $T/f30-kit/infra/freeze/freezes/F30.env $T/F30.env.in
cp -p $T/F30.env.in $T/f30-src/infra/freeze/freezes/F30.env
git -C $T/f30-src log --oneline -1; git -C $T/f30-kit log --oneline -1
echo "src tree $(git -C $T/f30-src rev-parse HEAD^{tree})"
git -C $T/f30-src status --short
git -C $T/f30-src diff --quiet HEAD && echo "src clean vs HEAD (F30.env untracked)"
