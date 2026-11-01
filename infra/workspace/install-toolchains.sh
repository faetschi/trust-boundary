#!/usr/bin/env bash
# Unpacks the Go and Node that fetch-inputs.sh verified into <prefix> and prints the PATH to use. Touches nothing else.
# Usage: install-toolchains.sh <inputs-dir> <prefix>. Also needs gcc and make from the system:
#   sudo apt-get install -y gcc make        (in the dedicated distro: wsl -d tbound-dev -u root -- apt-get install -y gcc make)
set -euo pipefail
inputs=${1:?usage: install-toolchains.sh <inputs-dir> <prefix>}
prefix=${2:?usage: install-toolchains.sh <inputs-dir> <prefix>}
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
pinned() { sed -n "s/^$1=//p" "$repo/infra/guest/setup-ubuntu-24.04.sh" | head -n 1; }
go_version=$(pinned GO_VERSION)
node_version=$(pinned NODE_VERSION)

mkdir -p -- "$prefix/go$go_version" "$prefix/node-v$node_version"
# The audit tests reject a world-writable /tmp and 0755 parents, so tests get a private TMPDIR and umask 077 (as the gate does).
mkdir -p -m 700 -- "$prefix/tmp"
tar -xzf "$inputs/go$go_version.linux-amd64.tar.gz" --strip-components=1 -C "$prefix/go$go_version"
tar -xJf "$inputs/node-v$node_version-linux-x64.tar.xz" --strip-components=1 -C "$prefix/node-v$node_version"
"$prefix/go$go_version/bin/go" version >&2
"$prefix/node-v$node_version/bin/node" --version >&2
echo "umask 077; export TMPDIR=$prefix/tmp GOTOOLCHAIN=local PATH=$prefix/go$go_version/bin:$prefix/node-v$node_version/bin:\$PATH"
