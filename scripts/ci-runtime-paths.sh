#!/bin/sh
# Prints one decision for the diff between two commits.
#
#   ci-runtime-paths.sh BASE HEAD MODE runtime
#     "true" when the diff touches an input of the core runtime suite.
#   ci-runtime-paths.sh BASE HEAD MODE docs-only
#     "true" when the diff touches documentation only.
#
# A missing, malformed or unreadable range fails open: runtime prints "true" and
# docs-only prints "false", so CI runs the work rather than guessing it is safe.
#
# The pathspec is deliberate: a rename out of one of these trees still matches
# the deleted side, so it cannot hide a runtime change. Every path the suite
# reads is listed here: the Go module, adapter contracts, corpus fixtures, guest
# scripts and runbooks, the installer, evaluation metadata, this workflow, and its timing helper.
set -eu

base=${1:-}
head=${2:-}
mode=${3:-pr}
field=${4:-runtime}

case "$field" in
  runtime|docs-only) ;;
  *) echo "ci-runtime-paths: unknown field '$field'" >&2; exit 2 ;;
esac

if [ -z "$base" ] || [ -z "$head" ] || [ "$base" = "0000000000000000000000000000000000000000" ]; then
  if [ "$field" = "docs-only" ]; then echo false; else echo true; fi
  exit 0
fi

root=$(git rev-parse --show-toplevel) || { echo true; exit 0; }
cd "$root"

if [ "$mode" = "push" ]; then
  # A force-push or reset can make the old tip unreachable from the new one.
  # Three-dot would compare the wrong side and could hide a revert.
  if ! git merge-base --is-ancestor "$base" "$head"; then
    echo true
    exit 0
  fi
  range="$base..$head"
else
  range="$base...$head"
fi

if [ "$field" = "docs-only" ]; then
  # Documentation is Markdown, anything under docs/, and the repository's
  # top-level prose files. Everything else needs the code checks.
  if git diff --quiet "$range" -- . ':(exclude)*.md' ':(exclude)docs/**' ':(exclude)CITATION.cff' ':(exclude)LICENSE'; then
    echo true
  else
    echo false
  fi
  exit 0
fi

if git diff --quiet "$range" -- \
  supervisor adapter corpus infra install docs/evaluation .github/workflows/ci.yml scripts/ci_go_test_timing.py; then
  echo false
else
  # 1 means a runtime input changed; any other status is an error, so run.
  echo true
fi
