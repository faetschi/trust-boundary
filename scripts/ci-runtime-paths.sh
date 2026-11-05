#!/bin/sh
# Prints "true" when the diff between two commits touches an input of the core
# runtime suite, "false" otherwise. A missing, malformed or unreadable range
# prints "true": run the suite rather than guess.
#
# The pathspec is deliberate: a rename out of one of these trees still matches
# the deleted side, so it cannot hide a runtime change. Every path the suite
# reads is listed here: the Go module, adapter contracts, corpus fixtures, guest
# scripts and runbooks, the installer, evaluation metadata, and this workflow.
set -eu

base=${1:-}
head=${2:-}
mode=${3:-pr}

if [ -z "$base" ] || [ -z "$head" ] || [ "$base" = "0000000000000000000000000000000000000000" ]; then
  echo true
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

if git diff --quiet "$range" -- \
  supervisor adapter corpus infra install docs/evaluation .github/workflows/ci.yml; then
  echo false
else
  # 1 means a runtime input changed; any other status is an error, so run.
  echo true
fi
