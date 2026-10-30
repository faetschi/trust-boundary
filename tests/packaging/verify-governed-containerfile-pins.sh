#!/usr/bin/env bash
# Regression: the governed D09 cell image must pin its base images to the exact
# toolchain versions that the host provisioning installs AND by immutable
# digest. A floating tag such as node:24-bookworm-slim can resolve to a different
# Node build than the admitted host Node, which breaks the in-image
# /usr/local/bin/node byte-identity check; a tag-only pin can still move.
# Also: the build must not run a package manager (no network, no unpinned input).
# Read-only: parses packaging/governed/Containerfile, packaging/governed/base-images.lock.json
# and infra/guest/setup-ubuntu-24.04.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONTAINERFILE="$ROOT/packaging/governed/Containerfile"
LOCK="$ROOT/packaging/governed/base-images.lock.json"
SETUP="$ROOT/infra/guest/setup-ubuntu-24.04.sh"

node_version="$(sed -n 's/^NODE_VERSION=\([0-9.]*\)$/\1/p' "$SETUP")"
go_version="$(sed -n 's/^GO_VERSION=\([0-9.]*\)$/\1/p' "$SETUP")"
if [ -z "$node_version" ] || [ -z "$go_version" ]; then
  echo "verify-governed-containerfile-pins: could not read NODE_VERSION/GO_VERSION from $SETUP" >&2
  exit 2
fi

# index_digest of one image in the lock file (one key per line).
lock_digest() {
  awk -v name="\"$1\": {" 'index($0, name) {on=1} on && /"index_digest"/ {gsub(/[",]/, "", $2); print $2; exit}' "$LOCK"
}
go_digest="$(lock_digest golang)"
node_digest="$(lock_digest node)"
case "$go_digest$node_digest" in
  sha256:[0-9a-f]*sha256:[0-9a-f]*) ;;
  *) echo "verify-governed-containerfile-pins: could not read digests from $LOCK" >&2; exit 2 ;;
esac

want_go="FROM docker.io/library/golang:${go_version}-bookworm@${go_digest} AS entrypoint"
want_node="FROM docker.io/library/node:${node_version}-bookworm-slim@${node_digest}"

failures=0
for want in "$want_go" "$want_node"; do
  if ! grep -qxF "$want" "$CONTAINERFILE"; then
    echo "FAIL: $CONTAINERFILE lacks exact line: $want" >&2
    failures=$((failures + 1))
  fi
done

# Every FROM must be one of the two digest-pinned lines (no floating or tag-only bases).
while IFS= read -r line; do
  if ! [[ "$line" == "$want_go" || "$line" == "$want_node" ]]; then
    echo "FAIL: unexpected, floating or tag-only base image line: $line" >&2
    failures=$((failures + 1))
  fi
done < <(grep -E '^FROM ' "$CONTAINERFILE")

# No in-build package installation (npm/yarn/pnpm/apt/pip): every input is pinned or copied.
if grep -E '^\s*RUN\b.*\b(npm|yarn|pnpm|apt-get|apt|pip|pip3)\b +(install|ci|add|update)' "$CONTAINERFILE" >/dev/null; then
  echo "FAIL: $CONTAINERFILE runs a package manager install in the build" >&2
  failures=$((failures + 1))
fi

if [ "$failures" -ne 0 ]; then
  echo "verify-governed-containerfile-pins: FAIL ($failures problem(s))" >&2
  exit 1
fi
echo "verify-governed-containerfile-pins: PASS (go ${go_version} ${go_digest:0:19}, node ${node_version} ${node_digest:0:19})"
