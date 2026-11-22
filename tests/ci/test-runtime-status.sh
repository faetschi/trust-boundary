#!/usr/bin/env bash
# Regression test for the runtime-tests matrix and the runtime check in .github/workflows/ci.yml.
# It reads the workflow as text (no YAML parser): which steps run on which leg, the sweep and
# evidence commands, and the decision step itself, run against each outcome it can receive.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/ci.yml"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[ -f "$WORKFLOW" ] || fail "missing $WORKFLOW"
# Drop CR so a CRLF checkout matches the same lines as an LF one.
workflow="$(tr -d '\r' < "$WORKFLOW")"

# Prints one job's block, from its "  <job>:" line up to the next job.
job_block() {
  awk -v job="$1" '
    $0 == "  " job ":" { inside = 1; print; next }
    inside && /^  [A-Za-z0-9_-]+:/ { inside = 0 }
    inside { print }
  ' <<<"$workflow"
}

# Prints each named step's name, condition and failure policy as tab-separated fields.
steps_with_conditions() {
  awk '
    /^      - / {
      if (step != "") print step "\t" condition "\t" allow_failure
      step = ""; condition = "none"; allow_failure = "false"
      if ($0 ~ /^      - name: /) { step = $0; sub(/^      - name: /, "", step) }
      next
    }
    /^        if: / { condition = $0; sub(/^        if: /, "", condition) }
    /^        continue-on-error: / { allow_failure = $0; sub(/^        continue-on-error: /, "", allow_failure) }
    END { if (step != "") print step "\t" condition "\t" allow_failure }
  '
}

has_line() { # <block> <exact line>
  grep -qxF -- "$2" <<<"$1" || fail "missing line in ci.yml: $2"
}

has_text() { # <block> <fixed text>
  grep -qF -- "$2" <<<"$1" || fail "missing text in ci.yml: $2"
}

runtime_tests_job="$(job_block runtime-tests)"
runtime_job="$(job_block runtime)"
[ -n "$runtime_tests_job" ] && [ -n "$runtime_job" ] || fail "ci.yml needs runtime-tests and runtime jobs"

# One matrix runs the unit suite and the race shards, behind the original eligibility condition.
has_line "$runtime_tests_job" "    needs: changes"
has_line "$runtime_tests_job" "    if: \${{ !cancelled() && (needs.changes.result != 'success' || needs.changes.outputs.runtime == 'true') }}"
has_line "$runtime_tests_job" '    name: runtime (${{ matrix.label }})'
has_line "$runtime_tests_job" '      fail-fast: false'
has_line "$runtime_tests_job" '          - { suite: unit, label: unit }'
for shard in product cmd rest; do
  has_line "$runtime_tests_job" "          - { suite: race, label: race-$shard, shard: $shard }"
done

# The required runtime check always runs, with a short timeout.
has_line "$runtime_job" '    needs: [changes, runtime-tests]'
has_line "$runtime_job" '    if: always()'
has_line "$runtime_job" '    timeout-minutes: 5'

# The sweeps must preserve exit status; a suffix such as || true is forbidden.
has_line "$runtime_tests_job" '          go test -mod=readonly -count=1 -json ./... > "$TMPDIR/go-test-unit.jsonl"'
has_line "$runtime_tests_job" '          sh ../scripts/ci-race-shard.sh "$SHARD" > "$TMPDIR/go-test-race.jsonl"'
has_line "$runtime_tests_job" '          name: go-race-suite-timing-${{ matrix.shard }}-${{ github.run_attempt }}'
has_line "$runtime_job" '          CHANGES_RESULT: ${{ needs.changes.result }}'
has_line "$runtime_job" '          CHANGES_RUNTIME: ${{ needs.changes.outputs.runtime }}'
has_line "$runtime_job" '          RUNTIME_TESTS_RESULT: ${{ needs.runtime-tests.result }}'

# Race detection and enforcement assertions keep their commands.
for fragment in \
  "grep -q -- '^--- PASS: TestRunGuestTestsRunsTheRealCgroupTestsInAFreshCgroupAndRemovesIt '" \
  "grep -q -- '^--- PASS: TestLandlockTSYNCReachesEnforcement '" \
  "grep -q -- '^--- PASS: TestLandlockConnectRightIsPort8080OnlyAndOnlyForNetSink '"; do
  has_text "$runtime_tests_job" "$fragment"
done

# Every named step must be classified, so a new step cannot silently run on the wrong leg.
steps="$(steps_with_conditions <<<"$runtime_tests_job")"
for step in "Go unit suite" "Go race suite"; do
  [ "$(awk -F '\t' -v name="$step" '$1 == name { print $3 }' <<<"$steps")" = false ] || fail "$step must fail the job"
done
for leg in unit race; do
  has_text "$runtime_tests_job" "--go-test-json \"\$TMPDIR/go-test-$leg.jsonl\""
  condition="        if: \${{ matrix.suite == '$leg' && always() && steps.go-$leg-suite.outcome != 'skipped' }}"
  [ "$(grep -cxF "$condition" <<<"$runtime_tests_job")" -eq 2 ] || fail "both $leg reports must run after failures"
done
[ "$(wc -l <<<"$steps")" -eq 14 ] || fail "runtime-tests should have 14 named steps; classify any new step in this test"

expect_gate() { # <step name> <unit | race | shared>
  local condition
  condition="$(awk -F '\t' -v name="$1" '$1 == name { print $2; exit }' <<<"$steps")"
  [ -n "$condition" ] || fail "runtime-tests has no step named: $1"
  case "$2" in
    unit | race) [[ "$condition" == *"matrix.suite == '$2'"* ]] || fail "'$1' must run only on the $2 leg: $condition" ;;
    shared) [[ "$condition" != *matrix.suite* ]] || fail "'$1' must run on both legs: $condition" ;;
  esac
}

for step in \
  "Go unit suite" "Go unit suite timing" "Upload Go unit suite timing" \
  "Real cgroup test in a delegated cgroup" "Landlock enforcement on this runner's kernel" \
  "Install adapter dependencies for the Pi fixture" "Official Pi CLI adapter integration" \
  "Offline official Pi CLI development fixture (non-claim-bearing)"; do
  expect_gate "$step" unit
done
for step in "Go race suite" "Go race suite timing" "Upload Go race suite timing"; do
  expect_gate "$step" race
done
for step in \
  "Prepare private temporary directory" "Install and verify official Pi CLI" \
  "Prepare hosted namespace test prerequisites"; do
  expect_gate "$step" shared
done

# The decision step is the only code that sets the runtime check: run that exact text.
decision="$(awk '
  /^      - name: / { in_run = 0; in_step = ($0 == "      - name: Decide runtime status"); next }
  in_step && /^        run: \|$/ { in_run = 1; next }
  in_run && /^$/ { print ""; next }
  in_run && /^          / { sub(/^          /, ""); print; next }
  in_run { exit }
' <<<"$runtime_job")"
[ -n "$decision" ] || fail "runtime job has no 'Decide runtime status' run block"

decide() { # <changes result> <changes runtime output> <runtime-tests result>
  env CHANGES_RESULT="$1" CHANGES_RUNTIME="$2" RUNTIME_TESTS_RESULT="$3" bash -c "$decision" >/dev/null 2>&1
}
expect_pass() {
  decide "$@" || fail "runtime check should pass: changes=$1 runtime=$2 runtime-tests=$3"
}
expect_fail() {
  if decide "$@"; then fail "runtime check should fail: changes=$1 runtime=$2 runtime-tests=$3"; fi
}

expect_pass success false skipped   # no runtime-relevant change, skipped on purpose
expect_pass success true success    # suites ran and passed
expect_pass failure "" success      # decision failed; suites still ran and passed
expect_fail success true failure    # a suite failed
expect_fail success true cancelled  # a suite was cancelled
expect_fail success true skipped    # suites were required but skipped
expect_fail success "" skipped      # missing decision output is not a clean skip
expect_fail success False skipped   # the output must be exactly false
expect_fail success false cancelled # a cancelled suite never passes
expect_fail failure "" skipped      # decision failed and nothing ran
expect_fail cancelled "" skipped    # decision was cancelled

echo "ci-runtime-status: all checks passed"
