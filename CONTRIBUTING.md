# Contributing to TBound

Thank you for looking at TBound. It is a research prototype with some frozen
parts. This page covers how to change it safely.

## Before you start

- Read the [glossary](docs/glossary.md) for the study and runtime terms.
- [OVERVIEW.md](OVERVIEW.md) is the only current-state file. Other docs can be records of
  past decisions.
- Set up a Linux checkout with the [quickstart](docs/guides/quickstart.md).

## Branches and pull requests

- Branch from `codex/tbound-prototype`. Open pull requests into it.
  `main` is the release line.
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

CI runs more checks, including the race-detector suite, Python regression tests,
the workflow linter, and a Linux runtime matrix. Its results are the reference
when a local result differs.

## Frozen and evidence paths

Do not edit these without the owner of the relevant study thread:

- `evidence/` and `infra/freeze/freezes/`: frozen records.
- `corpus/`: the test-case corpus and its manifests. Changes change study inputs.
- Dated files under `docs/evaluation/` and `docs/decisions/`: decision records.
- Anything that changes a pinned binary, image, or signed profile. These changes
  need a new freeze. See [reproduction](docs/guides/reproduce.md).

A documentation pull request must not change code or pinned files.

## Documentation

- Write for a developer who has not read the thesis. Define a term the first time
  you use it, or link to the [glossary](docs/glossary.md).
- Keep claims to what a command or test shows. Fixtures, probes, and pilot
  reports are not thesis results.

## Licence

TBound is released under the MIT licence in [LICENSE](LICENSE). By contributing you
agree that your change is released under the same licence.
