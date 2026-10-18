# TBound documentation

## Start here

- [Architecture](architecture.md): authority boundaries, runtime seams, and limits.
- [Status](status.md): current gates, checkpoint validation, and priorities.
- [Install](install.md): install Pi inside Linux and build/install TBound.
- [Launch](launch.md): Go-only and offline Pi CLI fixtures, managed-service
  behavior, and existing Hyper-V VM resume steps.
- [Reproducibility setup](reproducible-setup.md): prototype native Linux and
  Hyper-V provisioning paths; offline verification is in the guest
  [runbook](../infra/guest/README.md).
- [Governed host setup](governed-setup.md): operator prerequisites, profile trust,
  and CLI routes with exit codes for `serve --pi`.
- [Doctor](doctor.md): local diagnostic behavior.
- [CLI reproduction decision](decisions/2026-10-12-cli-reproduction.md):
  official Pi RPC/extension direction and its unqualified status.

## Runtime and security contracts

- [Production Pi session](production-pi-session.md)
- [Governed session composition](governed-session-composition.md)
- [Private Git staging](private-git-staging.md)
- [Publication contract](publication-contract.md)
- [Publication runtime integration](publication-runtime-integration.md)
- [Provider conversation bridge](provider-conversation-bridge.md)
- [Provider profiles](provider-profiles.md)
- [Provider durable evidence](provider-durable-evidence.md)
- [Release and packaging](release.md)

These documents describe code or intended contracts. Read the status page
before treating any design or fixture as a qualified production result.

## Evaluation

- [Candidate experiment manifest](experiment-manifest.md): not a registered
  protocol or G1 result.
- [Historical evidence and source map](evidence/README.md), with the
  [machine-readable source identities](evidence/historical-sources.json).
  Dated readiness reviews, blocker inventories, and superseded plans are
  retained in Git history only; the source map gives their paths, blob IDs,
  and hashes.
