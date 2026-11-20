#!/usr/bin/env bash
# CI: create a private TMPDIR under $HOME and export it to later steps through $GITHUB_ENV.
# Fails if the directory or any parent is writable by group or others.
set -euo pipefail
umask 077
TMPDIR="$(mktemp -d "$HOME/tb-ci.XXXXXXXX")"
chmod 700 "$TMPDIR"
test "$(stat -c '%a' "$TMPDIR")" = 700
python3 - "$TMPDIR" <<'PY'
import os
import stat
import sys

path = os.path.realpath(sys.argv[1])
while True:
    mode = os.stat(path).st_mode
    assert not mode & (stat.S_IWGRP | stat.S_IWOTH), path
    if path == "/":
        break
    path = os.path.dirname(path)
PY
printf 'TMPDIR=%s\n' "$TMPDIR" >> "$GITHUB_ENV"
