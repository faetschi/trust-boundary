#!/bin/bash
# carrier-check.sh: [READ-ONLY, WSL or native Linux] verify a screening-round carrier tree against the commit that delivered it. 33-corpus-pilot.sh runs it
# (from TBOUND_WGA) on the checkout (--checkout) and on the owner-side copy $HF/pilot/carrier, the only tree phases 2 and 3 read. Standalone: no kit env.
#   bash carrier-check.sh <checkout root> <commit> <round> <dir> <sha256:digest of manifest.draft.json> [--checkout]
# Refuses unless <dir> holds only directories and non-empty regular files, their paths are exactly the files of corpus/sampling/carriers/<round> at <commit>
# (each a 100644 blob with a plain name), every file's raw bytes are that blob (git hash-object --no-filters: no CRLF or other filter drift), manifest.draft.json
# and a flat non-empty artifacts/ exist, and sha256(manifest.draft.json) is the given digest. --checkout also requires that <checkout root> is the top of a git
# checkout whose HEAD is <commit> and that <dir> is <checkout root>/corpus/sampling/carriers/<round> with no symlink on the way.
set -euo pipefail
die() { printf 'carrier-check: %s\n' "$*" >&2; exit 1; }
[ $# -eq 5 ] || { [ $# -eq 6 ] && [ "$6" = --checkout ]; } || die "usage: carrier-check.sh <checkout root> <commit> <round> <dir> <sha256:digest> [--checkout]"
repo=$1 commit=$2 round=$3 dir=$4 digest=$5
[[ $commit =~ ^[0-9a-f]{40}$ ]] || die "the commit is not 40 lowercase hex: '$commit'"
[[ $round =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "the round is not a plain name: '$round'"
[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || die "the manifest digest is not sha256:<64 hex>: '$digest'"
rel=corpus/sampling/carriers/$round
if [ $# -eq 6 ]; then
  [ "$(git -C "$repo" rev-parse --show-toplevel)" = "$repo" ] || die "$repo is not the top of a git checkout"
  [ "$(git -C "$repo" rev-parse --verify HEAD)" = "$commit" ] || die "the checkout HEAD is $(git -C "$repo" rev-parse --verify HEAD), not $commit"
  [ "$(realpath -e -- "$dir")" = "$repo/$rel" ] || die "$dir is not $repo/$rel (or a symlink is on the way)"
fi
git -C "$repo" cat-file -e "$commit^{commit}" 2>/dev/null || die "$commit is not a commit of $repo"
want=$(git -C "$repo" ls-tree -r "$commit:$rel" 2>/dev/null) && [ -n "$want" ] || die "$rel is not a tree at $commit"
bad=$(printf '%s\n' "$want" | awk -F'\t' '{split($1, a, " "); if (a[1] != "100644" || a[2] != "blob" || $2 !~ /^[A-Za-z0-9._\/-]+$/) print}')
[ -z "$bad" ] || die "entries at $commit that are not 100644 blobs with plain names:"$'\n'"$bad"
cd -P -- "$dir" || die "no directory $dir"
odd=$(find . -mindepth 1 ! -type d ! -type f -printf '%y %P\n')
[ -z "$odd" ] || die "entries that are neither a directory nor a regular file (symlinks are refused):"$'\n'"$odd"
empty=$(find . -type f -empty -printf '%P\n')
[ -z "$empty" ] || die "0-byte files (the kit refuses 0-byte files under \$HF):"$'\n'"$empty"
have=$(find . -type f -printf '%P\n' | LC_ALL=C sort)
paths=$(printf '%s\n' "$want" | cut -f2 | LC_ALL=C sort)
[ "$have" = "$paths" ] || die "the files differ from $rel at $commit (< commit, > $dir):"$'\n'"$(diff <(printf '%s\n' "$paths") <(printf '%s\n' "$have") || true)"
exp=$(printf '%s\n' "$want" | awk -F'\t' '{split($1, a, " "); print $2 " " a[3]}' | LC_ALL=C sort)
got=$(paste -d' ' <(printf '%s\n' "$paths") <(printf '%s\n' "$paths" | sed "s|^|$PWD/|" | git -C "$repo" hash-object --no-filters --stdin-paths) | LC_ALL=C sort)
[ "$exp" = "$got" ] || die "file bytes differ from the blobs at $commit (CRLF or other drift; < commit, > $dir):"$'\n'"$(diff <(printf '%s\n' "$exp") <(printf '%s\n' "$got") || true)"
[ -f manifest.draft.json ] && [ -d artifacts ] || die "no manifest.draft.json or artifacts/ in $dir"
[ -z "$(find artifacts -mindepth 1 ! -type f -print -quit)" ] || die "artifacts/ is not flat (it holds a directory)"
n=$(find artifacts -type f | wc -l)
[ "$n" -gt 0 ] || die "artifacts/ is empty"
m="sha256:$(sha256sum manifest.draft.json | cut -c1-64)"
[ "$m" = "$digest" ] || die "sha256(manifest.draft.json) is $m, not $digest"
echo "carrier ok: $round $commit, $(printf '%s\n' "$paths" | grep -c .) files == blobs, $n artifacts, manifest $m"
