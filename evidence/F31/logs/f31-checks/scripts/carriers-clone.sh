#!/bin/bash
set -eu
D=/home/tbound/tbound-work/f31-carriers
[ ! -e $D ] || { echo "exists $D"; exit 2; }
git -c core.autocrlf=false init -q $D
git -C $D -c core.autocrlf=false fetch -q /mnt/c/Users/Admin/AppData/Local/Temp/claude/1bdbf0b7223717e2/300b9e68-97e7-4a01-aa27-ad0df38df07f/scratchpad/f31w/f31-carriers.bundle refs/heads/f31-carriers:refs/heads/f31-carriers
git -C $D config core.autocrlf false; git -C $D config core.eol lf
git -C $D checkout -q --detach cad7307a9c28b1ceabc459bb8417eda85dc68afe
git -C $D rev-parse HEAD; git -C $D status --porcelain | wc -l
for c in proc-qual-dev-01 rehearsal-dev-03 soak-dev-01; do echo "$c $(sha256sum $D/corpus/sampling/carriers/$c/manifest.draft.json | cut -c1-64) cr=$(grep -rlI $'\r' $D/corpus/sampling/carriers/$c | wc -l)"; done
