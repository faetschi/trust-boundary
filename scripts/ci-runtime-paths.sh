#!/bin/sh
# Prints one decision for the diff between two commits.
#
#   ci-runtime-paths.sh BASE HEAD MODE runtime
#     "true" when the diff touches an input of the core runtime suite.
#   ci-runtime-paths.sh BASE HEAD MODE docs-only
#     "true" when the diff touches documentation only.
#
# A missing, malformed or unreadable range, a push whose old tip is not an
# ancestor of the new one, a call outside a git checkout and any git error fail
# open: runtime prints "true" and docs-only prints "false", so CI runs the work
# rather than guessing it is safe.
#
# The pathspec is deliberate: a rename out of one of these trees still matches
# the deleted side, so it cannot hide a runtime change. Every path the suite
# reads is listed here: the Go module, adapter contracts, corpus fixtures, guest
# scripts and runbooks, the installer, evaluation metadata, this workflow, and its timing and race-shard helpers.
set -eu

base=${1:-}
head=${2:-}
mode=${3:-pr}
field=${4:-runtime}

case "$field" in
  runtime|docs-only) ;;
  *) echo "ci-runtime-paths: unknown field '$field'" >&2; exit 2 ;;
esac

run_all() {
  if [ "$field" = "docs-only" ]; then echo false; else echo true; fi
  exit 0
}

if [ -z "$base" ] || [ -z "$head" ] || [ "$base" = "0000000000000000000000000000000000000000" ]; then
  run_all
fi

root=$(git rev-parse --show-toplevel 2>/dev/null) || run_all
cd "$root"

if [ "$mode" = "push" ]; then
  # A force-push or reset can make the old tip unreachable from the new one.
  # Three-dot would compare the wrong side and could hide a revert.
  if ! git merge-base --is-ancestor "$base" "$head"; then
    run_all
  fi
  range="$base..$head"
else
  range="$base...$head"
fi

# Exit status 1 means a listed input changed; any other non-zero status is an error.
# Both callers below treat every non-zero status as "changed".
runtime_unchanged() {
  git diff --quiet "$range" -- \
    supervisor adapter corpus infra install docs/evaluation .github/workflows/ci.yml scripts/ci_go_test_timing.py scripts/ci-private-tmpdir.sh scripts/ci-race-shard.sh
}

if [ "$field" = "docs-only" ]; then
  # Documentation is top-level Markdown, docs/ outside the runtime inputs, and the
  # top-level prose files. Markdown anywhere else (digest-pinned corpus rules and
  # rubrics, testdata, evidence, runbooks) is an input and needs the code checks.
  if git diff --quiet "$range" -- . ':(exclude,glob)*.md' ':(exclude)docs' ':(exclude)CITATION.cff' ':(exclude)LICENSE' \
    && runtime_unchanged; then
    echo true
  else
    echo false
  fi
  exit 0
fi

if runtime_unchanged; then
  echo false
else
  echo true
fi
