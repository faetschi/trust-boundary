# TBound documentation

## What TBound is

TBound is a Linux supervisor that runs one Pi coding task under a fixed tool surface.
Pi and the model are untrusted; a Go process owns policy, durable state, command isolation, publication, and evidence.
Every tool call goes through a small Pi extension to the Go gate, which checks deterministic policy before any effect.
The gate journals the decision and the result in a hash-chained audit log, and it fails closed when a binding is missing or stale.
`read` is mediated; `write` and `edit` create sealed generations instead of touching the live workspace.
`bash` runs in an isolated rootless Podman cell with filesystem, process, network, and syscall limits.
Approved changes reach the live repository only through publication, which checks the exact delta, quiescence, and one-use authority.
Independent oracles observe the host and the workspace to measure forbidden effects that the supervisor may have missed.
The thesis treats H1 as two measurements: PVR (forbidden effects in adversarial runs) and ORR (friction on compliant tasks).
The prototype has passed G1 on the real guest, but the pilot and H1 are not finished; this repository is not a qualified product.

## Map

Product development: [product implementation plan](product-plan.md) describes the proposed interactive Pi product, implementation packages, release checks, and product-first README/USAGE requirements. It is a plan, not a list of shipped capabilities.

Read the first two before trusting any other page. They carry the claim boundary and the current checkpoint.

- [Current state](../OVERVIEW.md) and [working log](../TASK_STATE.md): where the thesis is, what changed, what is next.
- [Architecture](architecture.md): authority boundaries and runtime seams.
- [Status and gates](status.md): the short gate summary and claim boundary.

Setup and operation:

- [Install](install.md): Pi, Go, and TBound installation.
- [Reproducibility setup](reproducible-setup.md): native Linux and Hyper-V host paths.
- [Governed host setup](governed-setup.md): operator prerequisites, signed profile, and CLI routes.
- [Launch](launch.md): Go-only fixture, offline Pi fixture, and managed-service client.
- [Doctor](doctor.md): local prerequisite checks.
- [Release](release.md): build, package, and publish artifacts.

Runtime and security:

- [Production Pi session](production-pi-session.md): `serve --pi` service-side composition.
- [Governed session composition](governed-session-composition.md): legacy embedded-SDK detail, still cited by code.
- [Delta admission](delta-admission.md): the post-operation decision that precedes publication.
- [Publication contract](publication-contract.md) and [runtime integration](publication-runtime-integration.md): how approved changes reach the live root.
- [Providers](providers.md) and [provider durable evidence](provider-durable-evidence.md): profiles, conversation bridge, and the dated real-exchange receipt.
- [Design notes](design/): denial hints for H1 and the parked C-DH provider design.

Research:

- [Panel guide](panel-run.md): how the owner runs the blinded three-model construct panel.
- [Evidence receipts](evidence/README.md): bounded receipts and the historical source map.
- [Decision records](decisions/): the three ADRs, kept as history.

## History and archive

Finished audits, post-thesis proposals, and one-off working notes are in
[archive/](archive/README.md). Dated engineering receipts are in
[evidence/](evidence/README.md); [historical-sources.json](evidence/historical-sources.json)
maps retired documents to commit and hash. Git history is the archive for deleted
notes. The [candidate experiment manifest](experiment-manifest.md) is an old
candidate, not a registered protocol; it is kept because an infrastructure
worksheet cites it.

A page describes code or an intended contract, never a result. Read
[status](status.md) and [OVERVIEW](../OVERVIEW.md) before treating a design,
fixture, or receipt as evidence.
