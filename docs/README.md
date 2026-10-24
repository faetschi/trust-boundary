# TBound documentation

Start with [USAGE](../USAGE.md) for the user workflow. TBound runs one fixed,
signed Pi task per service configuration, or registered cases in corpus mode.
There is no interactive Pi chat mode yet. The web chat is frozen and outside
the thesis.

## [Guides](guides/): install, run, reproduce

- [Setup](guides/setup.md): host requirements, Pi and TBound installation, Doctor, fixtures.
- [Governed host setup](guides/governed-setup.md): host, profile, template, and live-root contracts; retains older design notes.
- [Reproduction](guides/reproduce.md): source freeze, release gate, signing, operator cycle, evidence, pilot.
- [Release](guides/release.md): package formats and release acceptance checks.
- [Panel guide](guides/panel-run.md): blinded model review and independent human audit.
- [Private git staging](guides/private-git-staging.md): local staging repository for frozen sources.
- [Guest assets](../infra/guest/README.md): provisioning and offline verification.
- [Provisioning and signing](../infra/guest/PROVISIONING-SIGNING.md): measurement, signing, deployment, corpus mode; replace reference values with your own measurements.
- [Operator dispatcher](../infra/guest/operator/README.md): fixed maintenance verbs.

## [Runtime](runtime/): contracts and components

- [Architecture](runtime/architecture.md): components and trust boundaries.
- [Production Pi session](runtime/production-pi-session.md): service composition and receipts.
- [Governed session composition](runtime/governed-session-composition.md): legacy embedded SDK reference.
- [Delta admission](runtime/delta-admission.md): the post-operation publication decision.
- [Publication contract](runtime/publication-contract.md): controlled live-root update rules.
- [Publication runtime integration](runtime/publication-runtime-integration.md): contract to code.
- [Providers](runtime/providers.md): provider profiles and the durable conversation bridge.
- [Provider durable evidence](runtime/provider-durable-evidence.md): the recorded durable bridge evidence.
- [Design notes](design/): denial hints and bounded storage capacity.

## [Evaluation](evaluation/): gates and research records

- [Status and gates](evaluation/status.md): verified scope and remaining evaluation gates.
- [Candidate experiment manifest](evaluation/experiment-manifest.md): an older candidate, not a registered protocol.
- [Evaluation evidence index](evaluation/evaluation-evidence-index-2026-10-07.json): source-only ledger of gaps.
- [Pilot corpus](../corpus/pilot/README.md): adapted cases, licenses, artifacts, quotas, review fields, draft limits.
- [G1 case](../corpus/g1/README.md): the engineering read, edit, Bash, and publish check.
- [Master thesis](../README.md#master-thesis): the separate protocol repository, hypotheses, methods, and decision register.

G1, component tests, capability probes, fixtures, and pilot outputs are not H1
results. The current runner reports use a synthetic empty USD ledger and are
non-claim-bearing. Claims require the registered evaluation.

## Engineering records

- [Overview](../OVERVIEW.md) and [working log](../TASK_STATE.md): current checkpoint and decisions; includes deployment history.
- [Decision records](decisions/): retained architecture decisions.
- [Evidence receipts](evidence/README.md) and [historical source map](evidence/historical-sources.json).
- [Archive](archive/README.md): finished audits and post-thesis proposals.
