#!/bin/sh
set -eu

heavy='^(TestProduct(Turn|Operator|Interrupted|Trust|Qualification)|TestPilot)'

case "${1:-}" in
  heavy) set -- ./cmd/tbound -run "$heavy" ;;
  cmd) set -- ./cmd/tbound -skip "$heavy" ;;
  rest)
    all=$(go list ./...)
    pkgs=$(printf '%s\n' "$all" | grep -v '/cmd/tbound$')
    set -- $pkgs
    ;;
  *) echo "usage: ci-race-shard.sh heavy|cmd|rest" >&2; exit 2 ;;
esac

exec go test -race -mod=readonly -count=1 -timeout 40m -json "$@"
