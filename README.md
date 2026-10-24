# TBound

TBound is a Linux supervisor prototype for Pi coding tasks.
It mediates four tools: `read`, `write`, `edit`, and `bash`.
Pi and model output remain untrusted; Go owns policy, durable state, isolation,
publication, and evidence.
Writes and edits create private workspace generations, while Bash runs in a
fresh rootless Podman command cell under a measured Linux profile.
Approved changes reach the live repository only through controlled publication.
The managed service admits one fixed, signed task per configuration, or cases
from a frozen manifest in corpus mode.
There is no interactive Pi chat mode yet.

## Current status

- The governed read, edit, Bash, and publish smoke path has real-host engineering
  evidence. Each new deployment must repeat it with fresh evidence.
- The pilot still needs review, complete evidence, and guest qualification checks.
- H1 is unexecuted. Tests, fixtures, probes, and pilot reports are not thesis results.
- The optional [web chat](extensions/webchat/README.md) is frozen and outside the thesis.

See [OVERVIEW.md](OVERVIEW.md) for the engineering checkpoint and
[status and gates](docs/status.md) for verified scope and remaining work.

## Start here

- [Use TBound](USAGE.md): installation, tasks, operation, and evaluation commands.
- [Set up TBound and Pi](docs/setup.md): native Linux or Windows with a Linux VM,
  installation, Doctor, and development fixtures.
- [Reproduce TBound](docs/reproduce.md): build, gate, offline signing, governed
  run, and the thesis evaluation workflow.
- [Documentation index](docs/README.md): guides and detailed references.
- [Architecture](docs/architecture.md): components and trust boundaries.

## Repository map

- `supervisor/`: Go commands, policy gate, broker, session state, Linux runtime,
  publication, independent effect checks, and evaluation tools.
- `adapter/`: the Pi CLI extension and broker transport.
- `install/`, `scripts/`, `packaging/`: local installation, build, and release assets.
- `infra/`: host preparation, guest provisioning, operator dispatcher, and evidence mappings.
- `corpus/`: engineering smoke fixtures and the draft benchmark-adapted pilot corpus.
- `docs/`: setup, reproduction, runtime contracts, and engineering records.
- `extensions/webchat/`: the frozen optional web chat extension.

## Master thesis

TBound is the artifact of a master thesis. The thesis text, hypotheses,
methods, and decision register live in the separate `MSE_MA_Thesis` repository:

- `tbound/tbound-thesis.md`
- `tbound/AH-technical/10-decision-register.md`

The thesis evaluates policy enforcement and usability with adapted benchmark
cases, compliant tasks, independent effect checks, and reviewed corpus labels.
H1 concerns PVR, the rate of forbidden effects in adversarial runs, and ORR,
the rate of rejection of compliant tasks.
Thesis claims come only from the registered evaluation, not from tests,
fixtures, capability probes, or engineering smoke runs.
See [reproduction](docs/reproduce.md#7-reproduce-the-evaluation-workflow) for the
pilot, review, evidence, and pre-registration sequence.
