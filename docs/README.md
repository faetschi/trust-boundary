# TBound documentation

Start with [USAGE](../USAGE.md) for the user workflow. TBound runs one fixed,
signed Pi task per service configuration, or registered cases in corpus mode.
There is no interactive Pi chat mode yet. The web chat is frozen and outside
the thesis.

## Install, run, and reproduce

- [Setup](setup.md): host requirements, native Linux or Windows with a VM, Pi
  and TBound installation, Doctor, development fixtures, and the service client.
- [Reproduction](reproduce.md): source freeze, release gate, offline signing,
  operator cycle, G1 evidence, corpus review, pilot, and registration.
- [Governed host setup](governed-setup.md): host, profile, template, and
  managed live-root contracts. This detailed reference retains older design notes.
- [Guest assets](../infra/guest/README.md): provisioning and offline verification.
- [Provisioning and signing](../infra/guest/PROVISIONING-SIGNING.md): detailed
  measurement, signing, deployment, and corpus-mode procedure. Replace reference
  deployment values with your own measurements.
- [Operator dispatcher](../infra/guest/operator/README.md): fixed maintenance verbs.
- [Release](release.md): package formats and release acceptance checks.

## Understand the runtime

- [Architecture](architecture.md): components and trust boundaries.
- [Production Pi session](production-pi-session.md): service composition and receipts.
- [Governed session composition](governed-session-composition.md): legacy embedded
  SDK reference retained for code citations.
- [Delta admission](delta-admission.md): the post-operation publication decision.
- [Publication contract](publication-contract.md) and
  [runtime integration](publication-runtime-integration.md): controlled live-root updates.
- [Providers](providers.md) and [provider evidence](provider-durable-evidence.md):
  provider profiles and the durable conversation bridge.
- [Design notes](design/): denial hints and bounded storage capacity.

## Reproduce the research workflow

- [Pilot corpus](../corpus/pilot/README.md): adapted cases, licenses, artifacts,
  quotas, review fields, and draft limits.
- [Panel guide](panel-run.md): blinded model review and independent human audit.
- [G1 case](../corpus/g1/README.md): the engineering read, edit, Bash, and publish check.
- [Status and gates](status.md): verified scope and remaining evaluation gates.
- [Master thesis](../README.md#master-thesis): the separate protocol repository,
  hypotheses, methods, and decision register.

G1, component tests, capability probes, fixtures, and pilot outputs are not H1
results. The current runner reports use a synthetic empty USD ledger and are
non-claim-bearing. Claims require the registered evaluation.

## Find engineering records

- [Overview](../OVERVIEW.md) and [working log](../TASK_STATE.md): current engineering
  checkpoint and decisions. These records include deployment history.
- [Decision records](decisions/): retained architecture decisions.
- [Evidence receipts](evidence/README.md) and
  [historical source map](evidence/historical-sources.json): bounded engineering evidence.
- [Archive](archive/README.md): finished audits and post-thesis proposals.
- [Candidate experiment manifest](experiment-manifest.md): an older candidate,
  not a registered protocol.
