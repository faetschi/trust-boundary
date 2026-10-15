#!/usr/bin/env bash
# Regression: the governed D09 cell image must pin its base images to the exact
# toolchain versions that the host provisioning installs. A floating tag such as
# node:24-bookworm-slim can resolve to a different Node build than the admitted
# host Node, which breaks the in-image /usr/local/bin/node byte-identity check.
# Read-only: parses packaging/governed/Containerfile and infra/guest/setup-ubuntu-24.04.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONTAINERFILE="$ROOT/packaging/governed/Containerfile"
SETUP="$ROOT/infra/guest/setup-ubuntu-24.04.sh"

node_version="$(sed -n 's/^NODE_VERSION=\([0-9.]*\)$/\1/p' "$SETUP")"
go_version="$(sed -n 's/^GO_VERSION=\([0-9.]*\)$/\1/p' "$SETUP")"
if [ -z "$node_version" ] || [ -z "$go_version" ]; then
  echo "verify-governed-containerfile-pins: could not read NODE_VERSION/GO_VERSION from $SETUP" >&2
  exit 2
fi

want_go="FROM golang:${go_version}-bookworm AS entrypoint"
want_node="FROM node:${node_version}-bookworm-slim"

failures=0
for want in "$want_go" "$want_node"; do
  if ! grep -qxF "$want" "$CONTAINERFILE"; then
    echo "FAIL: $CONTAINERFILE lacks exact line: $want" >&2
    failures=$((failures + 1))
  fi
done

# Every FROM must be pinned to an exact version tag (no floating major/minor-only tags).
while IFS= read -r line; do
  if ! [[ "$line" == "$want_go" || "$line" == "$want_node" ]]; then
    echo "FAIL: unexpected or floating base image line: $line" >&2
    failures=$((failures + 1))
  fi
done < <(grep -E '^FROM ' "$CONTAINERFILE")

if [ "$failures" -ne 0 ]; then
  echo "verify-governed-containerfile-pins: FAIL ($failures problem(s))" >&2
  exit 1
fi
echo "verify-governed-containerfile-pins: PASS (go ${go_version}, node ${node_version})"
