#!/usr/bin/env bash
# Regression test for scripts/ci-runtime-paths.sh. Builds throwaway repos so both
# decisions, runtime reachability and docs-only, are exercised, including
# renames, without GitHub Actions.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
FILTER="$ROOT/scripts/ci-runtime-paths.sh"
BASE="$(mktemp -d "${TMPDIR:-/tmp}/tbound-runtime-paths.XXXXXX")"
trap 'rm -rf "$BASE"' EXIT

repo="$BASE/repo"
git init -q "$repo"
git -C "$repo" checkout -q -b main
git -C "$repo" config user.email ci@example.invalid
git -C "$repo" config user.name ci
mkdir -p "$repo/supervisor" "$repo/docs"
printf 'package x\n' > "$repo/supervisor/a.go"
printf 'doc\n' > "$repo/docs/a.md"
git -C "$repo" add .
git -C "$repo" commit -qm initial
first="$(git -C "$repo" rev-parse HEAD)"

check() {
  name=$1 want=$2 base=$3 head=$4 mode=${5:-pr} field=${6:-runtime}
  got="$(cd "$repo" && sh "$FILTER" "$base" "$head" "$mode" "$field")"
  if [ "$got" != "$want" ]; then
    echo "FAIL: $name: got $got, want $want" >&2
    exit 1
  fi
  echo "PASS: $name"
}

printf 'more\n' >> "$repo/docs/a.md"
git -C "$repo" commit -qam docs
docs="$(git -C "$repo" rev-parse HEAD)"
check "docs only skips runtime" false "$first" "$docs"

prev="$docs"
for path in supervisor/added.go adapter/added.ts corpus/added.json infra/added.sh install/added.sh docs/evaluation/added.json .github/workflows/ci.yml scripts/ci_go_test_timing.py; do
  mkdir -p "$repo/$(dirname "$path")"
  printf 'x\n' > "$repo/$path"
  git -C "$repo" add "$path"
  git -C "$repo" commit -qm "add $path"
  next="$(git -C "$repo" rev-parse HEAD)"
  check "$path change runs runtime" true "$prev" "$next"
  prev="$next"
done

git -C "$repo" mv supervisor/a.go docs/moved.go
git -C "$repo" commit -qm rename
renamed="$(git -C "$repo" rev-parse HEAD)"
check "rename out of supervisor runs runtime" true "$prev" "$renamed"

check "push of a runtime change runs runtime" true "$first" "$renamed" push
check "push of a docs-only change skips runtime" false "$first" "$docs" push
check "force reset to an ancestor runs runtime" true "$renamed" "$first" push

check "missing range runs runtime" true "" ""
check "zero before runs runtime" true "0000000000000000000000000000000000000000" "$renamed"

check "docs only is docs-only" true "$first" "$docs" pr docs-only
check "push of a docs-only change is docs-only" true "$first" "$docs" push docs-only
check "runtime change is not docs-only" false "$prev" "$renamed" pr docs-only
check "mixed docs and code is not docs-only" false "$docs" "$prev" pr docs-only
check "missing range is not docs-only" false "" "" pr docs-only
check "zero before is not docs-only" false "0000000000000000000000000000000000000000" "$renamed" pr docs-only

if (cd "$repo" && sh "$FILTER" "$first" "$docs" pr unknown-field >/dev/null 2>&1); then
  echo "FAIL: unknown field was accepted" >&2
  exit 1
fi
echo "PASS: unknown field is refused"

echo "ci-runtime-paths: all checks passed"
