# TBound documentation

Start with [USAGE](../USAGE.md) for the user workflow. TBound runs one fixed,
signed Pi task per service configuration, or registered cases in corpus mode.
There is no interactive Pi chat mode yet. The web chat is frozen and outside
the thesis.

Product development: the [product implementation plan](product-plan.md) describes the
proposed interactive Pi product. It is a plan, not a list of shipped capabilities.
On the product branch, [product status](product-status.md) records what is implemented
and [product contracts](product-contracts.md) fixes the first milestone's contracts.

## [Guides](guides/): install, run, reproduce

- [Setup](guides/setup.md): host requirements, Pi and TBound installation, Doctor, fixtures.
- [Governed host setup](guides/governed-setup.md): host, profile, template, and live-root contracts; retains older design notes.
- [Self-contained workspace](guides/self-contained-workspace.md): what lives where, and continuing on another machine.
- [Reproduction](guides/reproduce.md): source freeze, release gate, signing, operator cycle, evidence, pilot.
- [Developer tools](guides/developer-tools.md): source-only provider probe, web chat UI QA, and similar tools, with prerequisites and status.
- [Release](guides/release.md): package formats and release acceptance checks.
- [First governed product turn](guides/product-first-turn.md): product service setup order, terminal and browser chat on one Linux host; not thesis evidence.
- [Enable Bash in the product service](guides/product-bash.md): offline image and command bundle, format-3 setup, qualification, review, and recovery.
- [Product demo from Windows](guides/product-demo-windows.md): chat with a product VM over SSH from Git Bash or VS Code.
- [Panel guide](guides/panel-run.md): optional supplementary blinded model analysis; agreement statistics are diagnostics.
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
- [Design notes](design/): denial hints, bounded storage capacity, and the [supervisor ownership map](design/supervisor-ownership.md).

## [Evaluation](evaluation/): gates and research records

- [Owner protocol decisions, 2026-10-29](evaluation/owner-protocol-decisions-2026-10-29.md): current D35, D26, T5, T7, and AMB-02 policy, superseding advisor prerequisites.
- [Status and gates](evaluation/status.md): verified scope and remaining evaluation gates.
- [Pilot corpus](../corpus/pilot/README.md): adapted cases, licenses, artifacts, quotas, optional review metadata, and pending construct evidence.
- [G1 case](../corpus/g1/README.md): the engineering read, edit, Bash, and publish check.
- [Master thesis](../README.md#master-thesis): the separate protocol repository, hypotheses, methods, and decision register.

The critical proof path is full deterministic construct checks, including
applicable guest witnesses, then preregistered H1. The supervisor is
administrative only, with no input or sign-off. No advisor confirmation, human
audit, signature, or replacement mandatory owner acceptance gate is required.
Independent human review is not claimed. Operational owner cryptographic
signing remains unchanged. A digest or preregistration record does not run the
full construct-check path.

G1, component tests, capability probes, fixtures, and pilot outputs are not H1
results. The current runner reports use a synthetic empty USD ledger and are
non-claim-bearing. Claims require the registered evaluation.

## Engineering records

- [Overview](../OVERVIEW.md): current checkpoint and decisions; includes deployment history.
- [Decision records](decisions/): retained architecture decisions.
