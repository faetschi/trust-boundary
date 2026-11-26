#!/bin/bash
# sign-tags34.sh: SSH-sign the F34 build and corpus tags with the dedicated TBound tag key (local only; no push).
set -euo pipefail
S=$(cd "$(dirname "$0")" && pwd)
R=/c/Users/Admin/.t3/worktrees/trust-boundary/t3-d17d47cf-10f5da7c23f0b1fb
K=/c/Users/Admin/.ssh/tbound_tag_signing_ed25519
BUILD=${1:?build commit}
CORP=${2:?corpus commit}
KG=$(cygpath -w /usr/bin/ssh-keygen.exe)
cd "$R"
G() { git -c gpg.format=ssh -c user.signingkey="$(cygpath -w "$K")" -c gpg.ssh.program="$KG" "$@"; }
G tag -s f34-freeze "$BUILD" -F "$S/tag34-build.txt"
G tag -s f34-corpus-1 "$CORP" -F "$S/tag34-corpus.txt"
printf '%s namespaces="git" %s\n' "$(git config user.email)" "$(cut -d' ' -f1-2 "$K.pub")" > "$S/allowed_signers34"
for t in f34-freeze f34-corpus-1; do git -c gpg.ssh.allowedSignersFile="$(cygpath -w "$S/allowed_signers34")" -c gpg.ssh.program="$KG" verify-tag "$t"; done
[ "$(git rev-parse 'f34-freeze^{commit}')" = "$BUILD" ] && [ "$(git rev-parse 'f34-corpus-1^{commit}')" = "$CORP" ] && echo "tags resolve: ok"
out=$(git diff --no-renames --name-only f34-freeze f34-corpus-1 | grep -v '^corpus/third/' || true)
[ -z "$out" ] && echo "diff only corpus/third/: ok ($(git diff --no-renames --name-only f34-freeze f34-corpus-1 | wc -l) files)" || { echo "FAIL: outside corpus/third: $out"; exit 1; }
