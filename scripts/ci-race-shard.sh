#!/bin/sh
# Runs one shard of the Go race suite as `go test -json`. Run it from supervisor/.
#
#   ci-race-shard.sh heavy   cmd/tbound tests in the slowest families (the regex below)
#   ci-race-shard.sh cmd     every other cmd/tbound test
#   ci-race-shard.sh rest    every package except cmd/tbound
#
# cmd/tbound is one package of about 970 tests and took 20 minutes under -race, so it is
# split by test name. The two cmd/tbound filters are the same regex, one as -run and one
# as -skip, so a new test lands in exactly one shard. The exit status is go test's.
set -eu

# shortcut: families picked from measured race times (about 630 s heavy, 680 s cmd); a new slow family
# lands in cmd, so re-measure from the go-race-suite-timing artifacts when the two legs drift apart.
heavy='^TestProduct(Turn|Operator|Interrupted|Trust|Qualification)'

case "${1:-}" in
  heavy) set -- ./cmd/tbound -run "$heavy" ;;
  cmd) set -- ./cmd/tbound -skip "$heavy" ;;
  rest)
    all=$(go list ./...) # a failed list stops here instead of leaving a partial package list
    pkgs=$(printf '%s\n' "$all" | grep -v '/cmd/tbound$')
    # shellcheck disable=SC2086 # one package path per word
    set -- $pkgs
    ;;
  *) echo "usage: ci-race-shard.sh heavy|cmd|rest" >&2; exit 2 ;;
esac

exec go test -race -mod=readonly -count=1 -timeout 40m -json "$@"
