# Contributing to TBound

Thank you for looking at TBound. It is a research prototype with some frozen
parts. This page covers how to change it safely.

## Before you start

- Read the [glossary](docs/glossary.md) for the study and runtime terms.
- [OVERVIEW.md](OVERVIEW.md) is the only current-state file. Other docs can be records of
  past decisions.
- Set up a Linux checkout with the [quickstart](docs/guides/quickstart.md).

## Branches and pull requests

- Branch from `codex/tbound-prototype`. Open pull requests into it. It is the
  development branch. `main` is the public-facing branch and the frozen baseline
  for engineering freezes.
- Keep one concern per pull request. Name the files and the reason in the description.
- In the description, list each command you ran and its result. Say which
  checks you could not run and why.

## Checks before you open a pull request

Run these from the repository root unless noted:

```sh
make fmt                           # gofmt for supervisor/; review the diff it produces
cd supervisor && go vet ./...      # run from supervisor/
make boundary                      # no Node needed
make test                          # Go unit suite; needs Node
python3 scripts/check-doc-links.py # run this for any documentation change
```

Full CI runs only for pull requests into `main` and merges to `main`. It adds the
race-detector suite, Python regression tests, the workflow linter, and a Linux
runtime matrix. Pushes to development branches, and pull requests into them, run
no workflow, so run the local checks above. A `workflow_dispatch` run gives you a
one-off full check.

The race-detector suite runs as three shards (`scripts/ci-race-shard.sh`), because
`cmd/tbound` alone took 20 minutes under `-race`. `internal/evalreport` is
excluded: it is pure computation, and the script fails if it gains goroutines.

## Writing tests

Prefer a test that runs the command or service as a user does and checks its output,
exit status, or evidence record, over one per internal function. Such a test breaks
only when behavior changes, and it needs no rewrite when the code is refactored.
Keep a test of an internal function when it pins a security or journaling invariant.
Before you add a test that takes more than a few seconds, say why it cannot be
smaller: the slowest tests set the race suite's run time.

## Frozen and evidence paths

Do not edit these without approval from the project owner:

- `evidence/` and `infra/freeze/freezes/`: frozen records.
- `corpus/`: the test-case corpus and its manifests. Changes change study inputs.
- Dated files under `docs/evaluation/` and `docs/decisions/`: decision records.
- Anything that changes a pinned binary, image, or signed profile. These changes
  need a new freeze. See [reproduction](docs/guides/reproduce.md).

Keep a documentation change separate from code and pinned-file changes.

## Documentation

- Write for a developer who has not read the thesis. Define a term the first time
  you use it, or link to the [glossary](docs/glossary.md).
- Keep claims to what a command or test shows. Fixtures, probes, and pilot
  reports are not thesis results.

## Licence

TBound is released under the MIT licence in [LICENSE](LICENSE). By contributing you
agree that your change is released under the same licence.
