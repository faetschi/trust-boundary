#!/usr/bin/env bash
# Fills the Go module and npm caches in $HOME that the offline freeze steps read. Needs the network and the env from install-toolchains.sh.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
(cd "$repo/supervisor" && go mod download)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# The version and its package hashes are checked again offline in infra/freeze/25-p2-rebuild.sh.
npm install --prefix "$tmp" --ignore-scripts --no-audit --no-fund --save-exact @earendil-works/pi-coding-agent@0.87.1 </dev/null >/dev/null
du -sh "$HOME/go/pkg/mod/cache/download" "$HOME/.npm/_cacache"
