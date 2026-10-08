#!/bin/sh
# Minimal, deterministic in-image entrypoint for the TBound cell.
# Validates its own identity and prints a structured status. No network access.
set -eu

emit() { printf '{"schema":"tbound-cell-status/v1","status":"%s","detail":"%s"}\n' "$1" "$2"; }

if [ "$#" -eq 0 ]; then
  emit refused "no command supplied"
  exit 2
fi

# Prove identity is stable before executing anything.
if [ ! -x /usr/local/bin/tbound-cell-entrypoint ]; then
  emit refused "entrypoint identity missing"
  exit 3
fi

emit ready "entrypoint verified"
exec "$@"
