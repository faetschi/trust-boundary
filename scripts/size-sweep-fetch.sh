#!/usr/bin/env bash
# Fetch the pinned public repositories for the session-store size sweep
# (TestSizeSweepBenchmark in supervisor/internal/sessionrepo).
#
# Usage: scripts/size-sweep-fetch.sh <new directory under $HOME>
# Prints the SIZE_SWEEP_SOURCES value for the test on its last line.
#
# The session store refuses symbolic links, so they are removed here and
# counted in the output. Four repositories are SWE-bench repositories; t3code is
# the over-limit case (more than 20,000 files).
set -euo pipefail

dest=${1:?usage: size-sweep-fetch.sh <new directory under \$HOME>}
case "$dest" in
/*) ;;
*) echo "destination must be absolute" >&2; exit 2 ;;
esac
case "$dest" in
"$HOME"/?*) ;;
*) echo "destination must be under \$HOME" >&2; exit 2 ;;
esac
# A fresh directory only: mkdir fails if it exists.
mkdir -m 700 "$dest"

repos=(
	"flask pallets/flask 49943b5a94413a530761350f04e896f76e8d620e"
	"pytest pytest-dev/pytest fc9a7ef678f64d17f1c03b619f3842ea1d127e74"
	"astropy astropy/astropy 7732926be0c58aa8761f318cf89f4ad212ae6fe7"
	"django django/django 9600da0b98b98b00e310c66b964f259ad5932daa"
	"t3code pingdotgg/t3code f31ce3eeb5f4adbfe7f961403822cf90db05808f"
)

sources=""
for entry in "${repos[@]}"; do
	read -r name repo sha <<<"$entry"
	out="$dest/$name"
	mkdir -m 700 "$out"
	curl -fsSL "https://codeload.github.com/$repo/tar.gz/$sha" |
		tar -xz -C "$out" --strip-components=1 --no-same-owner --no-same-permissions
	symlinks=$(find "$out" -type l | wc -l)
	find "$out" -type l -delete
	hardlinks=$(find "$out" -type f -links +1 | wc -l)
	files=$(find "$out" -type f | wc -l)
	bytes=$(find "$out" -type f -printf '%s\n' | awk '{s += $1} END {print s + 0}')
	printf '%s %s@%s files=%d bytes=%d symlinks_removed=%d hardlinks=%d\n' \
		"$name" "$repo" "$sha" "$files" "$bytes" "$symlinks" "$hardlinks"
	sources="${sources:+$sources,}$name=$out"
done
echo "SIZE_SWEEP_SOURCES=$sources"
