#!/usr/bin/env bash
# Fetches the public build inputs into <dest-dir>. A file is kept only after its SHA-256 matches its pin.
# Usage: fetch-inputs.sh <dest-dir> [--iso]. Run it on Linux or in WSL (curl, sha256sum, python3).
# Re-running it fetches only what is missing or fails its pin. The Go, Node and cosign versions, and the Go and
# cosign hashes, are read from the scripts that already pin them. The Node hash is the nodejs.org SHASUMS256.txt
# line for that release, GPG-checked against nodejs/release-keys. The ISO hash is in
# evidence/thesis-inputs/abi8-seed-receipt.json.
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
guest=$repo/infra/guest/setup-ubuntu-24.04.sh
boot=$repo/infra/repro/bootstrap-linux.sh
# pinned <file> <NAME>: the value of the top-level NAME=value line.
pinned() { sed -n "s/^$2=//p" "$1" | head -n 1; }
GO_VERSION=$(pinned "$guest" GO_VERSION)
GO_SHA256=$(pinned "$guest" GO_SHA)
NODE_VERSION=$(pinned "$guest" NODE_VERSION)
NODE_SHA256=fd8e59d5a511510f6a298afb548f18c7d2b1be404d8b4a27d94fbe49f56cb2d6
COSIGN_VERSION=$(pinned "$boot" COSIGN_VERSION)
COSIGN_SHA256=$(pinned "$boot" COSIGN_DEFAULT_SHA256)
ISO=ubuntu-24.04.5-live-server-amd64.iso
ISO_SHA256=97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8

download() { curl --fail --location --proto '=https' --tlsv1.2 --retry 2 -o "$2" "$1"; }

# fetch_one <dest-dir> <name> <sha256> <url>: keeps the file only when its SHA-256 matches.
fetch_one() {
  local path=$1/$2 got
  if [ -f "$path" ] && [ "$(sha256sum -- "$path" | cut -d' ' -f1)" = "$3" ]; then
    echo "ok       $2"
    return 0
  fi
  rm -f -- "$path" "$path.part"
  if ! download "$4" "$path.part"; then
    rm -f -- "$path.part"
    echo "FAIL     $2: download failed" >&2
    return 1
  fi
  got=$(sha256sum -- "$path.part" | cut -d' ' -f1)
  if [ "$got" != "$3" ]; then
    rm -f -- "$path.part"
    echo "FAIL     $2: SHA-256 $got is not the pin $3. Not kept." >&2
    return 1
  fi
  mv -- "$path.part" "$path"
  echo "fetched  $2"
}

main() {
  local dest=${1:?usage: fetch-inputs.sh <dest-dir> [--iso]}
  [ -z "${2:-}" ] || [ "$2" = --iso ] || { echo "usage: fetch-inputs.sh <dest-dir> [--iso]" >&2; exit 2; }
  mkdir -p -- "$dest"
  fetch_one "$dest" "go$GO_VERSION.linux-amd64.tar.gz" "$GO_SHA256" "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz"
  fetch_one "$dest" "node-v$NODE_VERSION-linux-x64.tar.xz" "$NODE_SHA256" "https://nodejs.org/download/release/v$NODE_VERSION/node-v$NODE_VERSION-linux-x64.tar.xz"
  fetch_one "$dest" "cosign-v$COSIGN_VERSION-linux-amd64" "$COSIGN_SHA256" "https://github.com/sigstore/cosign/releases/download/v$COSIGN_VERSION/cosign-linux-amd64"
  if [ "${2:-}" = --iso ]; then
    fetch_one "$dest" "$ISO" "$ISO_SHA256" "https://releases.ubuntu.com/24.04.5/$ISO"
  fi
  # shortcut: cell_image.py keeps an existing manifest or index without re-hashing it, so cached blobs that fail their digest are dropped first, upgrade when write_blob verifies the files it finds.
  for b in "$dest"/base-cache/blobs/sha256/*; do
    [ -f "$b" ] && [ "$(sha256sum -- "$b" | cut -d' ' -f1)" = "${b##*/}" ] || rm -f -- "$b"
  done
  python3 -I "$repo/packaging/governed/cell_image.py" pull --lock "$repo/packaging/governed/base-images.lock.json" --cache "$dest/base-cache"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
