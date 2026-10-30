#!/usr/bin/env bash
# Build the tbound container image with podman or docker.
#
#   scripts/build-container.sh [tag] [--check]
#
# Prefers podman (matching the governed D09 story), falls back to docker. The
# build context is the repository root. This only builds an image; it never runs
# a container or touches a registry.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TAG="tbound:${1:-dev}"
CHECK=0
for a in "$@"; do [ "$a" = "--check" ] && CHECK=1; done
[ "$CHECK" = 1 ] && TAG="tbound:dev"
CONTAINERFILE="$ROOT/packaging/container/Containerfile"

die() { echo "build-container: $*" >&2; exit 1; }
[ -f "$CONTAINERFILE" ] || die "missing $CONTAINERFILE"

ENGINE=""
if command -v podman >/dev/null 2>&1; then ENGINE=podman
elif command -v docker >/dev/null 2>&1; then ENGINE=docker; fi

if [ "$CHECK" = 1 ]; then
  if [ -n "$ENGINE" ]; then
    echo "build-container: check ok ($ENGINE present)"
  else
    echo "build-container: check ok, but no podman/docker — build would be skipped"
  fi
  echo "build-container: would build $TAG from $CONTAINERFILE (context $ROOT)"
  exit 0
fi

[ -n "$ENGINE" ] || die "podman or docker is required to build the image"
echo "build-container: $ENGINE build -> $TAG"
"$ENGINE" build -f "$CONTAINERFILE" -t "$TAG" "$ROOT"
echo "build-container: built $TAG"
echo "build-container: run with  $ENGINE run --rm $TAG --help"
