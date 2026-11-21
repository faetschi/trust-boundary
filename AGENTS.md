# tbound agent instructions

Read CONTRIBUTING.md before opening a pull request, and run the checks it
lists. This file adds one rule to that guide.

## Tests

Do not add unit tests. The project owner asks for a test when one is wanted.

The existing gate suites carry this repo's proof: the oracle packages, the
broker protocol tests, the freeze and packaging gates, and the CI script
regressions. Extend them only when the project owner asks.

When a change seems to need a new test, name the behavior and the credible
regression in the PR description, then wait for the project owner. Red-test
commits (`test: red tests for ...`) are the owner's tool, not a default step
of landing a change.
