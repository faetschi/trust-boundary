#!/bin/sh
# Runs one shard of the Go race suite as `go test -json`. Run it from supervisor/.
#
#   ci-race-shard.sh product   cmd/tbound tests named TestProduct*
#   ci-race-shard.sh cmd       every other cmd/tbound test
#   ci-race-shard.sh rest      every package except cmd/tbound and internal/evalreport
#
# cmd/tbound is one package of about 970 tests and took 20 minutes under -race, so it is
# split by test name. The two cmd/tbound filters are the same regex, one as -run and one
# as -skip, so a new test lands in exactly one shard. The exit status is go test's.
set -eu

pure=internal/evalreport # shortcut: pure computation, race adds 18x time and no signal; a guard below fails if it gains concurrency, then drop the exclusion.

case "${1:-}" in
  product) set -- ./cmd/tbound -run '^TestProduct' ;;
  cmd) set -- ./cmd/tbound -skip '^TestProduct' ;;
  rest)
    if grep -rEq 'sync"|go func|go [a-zA-Z_.]+\(|chan ' --include='*.go' "$pure"; then
      echo "ci-race-shard: $pure now uses goroutines, channels or sync; remove it from the exclusion" >&2
      exit 1
    fi
    pkgs=$(go list ./... | grep -v -e '/cmd/tbound$' -e "/$pure\$")
    # shellcheck disable=SC2086 # one package path per word
    set -- $pkgs
    ;;
  *) echo "usage: ci-race-shard.sh product|cmd|rest" >&2; exit 2 ;;
esac

exec go test -race -mod=readonly -count=1 -timeout 40m -json "$@"
