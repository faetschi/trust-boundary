# Research records and study inputs

TBound is the artifact of a master thesis. This repository also holds the
study's test corpus, its frozen evidence, and its decision records. They sit
next to the product code, and this page explains which is which.

## What counts as a result

- Tests, fixtures, capability probes, and pilot reports are **not** thesis results.
- The main hypothesis (H1) has **not been executed**.
- A thesis claim comes only from the registered evaluation, once it is run.

## Where things are

| Path | What it is | Who may change it |
|---|---|---|
| [`corpus/`](../../corpus/) | Test cases: the pilot set, the G1 engineering case, sampling tools, and digests. | Corpus owner. The `TODO` strings in `corpus/pilot/*.json` are fixture placeholders. |
| [`evidence/`](../../evidence/) | Evidence records from earlier freezes. | Nobody. Frozen. |
| [`infra/freeze/`](../../infra/freeze/) | Freeze scripts and the frozen environment files in `freezes/`. | Freeze owner only. |
| [`docs/evaluation/`](../evaluation/) | Owner decisions and protocol records, dated. | Nobody, for dated records. |
| [`docs/decisions/`](../decisions/) | Dated architecture decision records. | Nobody, for dated records. |
| [`docs/product-audit-2026-10-29.md`](../product-audit-2026-10-29.md), [`docs/product-contracts.md`](../product-contracts.md) | Product audit and contracts at a point in time. | Owner. |
| [`OVERVIEW.md`](../../OVERVIEW.md) | The only current-state file. | Owner. |

## Why these stay in this repository

The Go code, the scripts, and the freeze tooling refer to these paths directly.
Counted by path, `corpus/` is named 58 times under `supervisor/`, and `evidence/`
168 times under `supervisor/`, `scripts/`, and `infra/freeze/`. A clone of the
product repository alone would then fail to build or test, and every pinned
path would change. For now they stay where they are.

Moving `evidence/` to the thesis repository is possible later, as a separate
change once the freeze and the thesis are final. It needs updates to the
freeze tooling and a new pin. Do not do it during an active freeze.

## The thesis repository

The thesis text, hypotheses, methods, and decision register live in a separate
repository, `faetschi/MSE_MA_Thesis`:

- `tbound/tbound-thesis.md`
- `tbound/AH-technical/10-decision-register.md`

A local checkout is kept at `thesis/` in this repository. The directory is
gitignored, so the product repository does not track it. The
[thesis overview](../thesis/tbound-overview.html) is a copy of the overview from
that repository.

## Reproducing a result

See [reproduction](../guides/reproduce.md#7-reproduce-the-evaluation-workflow) for
the pilot, review, evidence, and pre-registration sequence.
