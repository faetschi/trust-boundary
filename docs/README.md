# TBound documentation

## Start here

- [Architecture](architecture.md): intended authority boundaries and current
  implementation limits.
- [Status](status.md): current gates and what the available checks establish.
- [Install](install.md): install Pi inside Linux and build/install TBound.
- [Launch](launch.md): Go-only and offline Pi CLI fixtures, managed-service
  behavior, and existing Hyper-V VM resume steps.
- [Setup routes](setup.md): choose native, VM, fixture, or offline-verification
  instructions.
- [Reproducibility setup](reproducible-setup.md): prototype native Linux and
  Hyper-V provisioning paths and their limits.
- [Doctor](doctor.md): local diagnostic behavior.
- [CLI reproduction decision](decisions/2026-10-12-cli-reproduction.md):
  official Pi RPC/extension direction and its unqualified status.

## Runtime and security contracts

- [Production Pi session](production-pi-session.md)
- [Governed session composition](governed-session-composition.md)
- [Offline Pi CLI fixture and production launch limits](launch.md#offline-pi-cli-fixture)
- [Private Git staging](private-git-staging.md)
- [Publication contract](publication-contract.md)
- [Publication runtime integration](publication-runtime-integration.md)
- [Provider conversation bridge](provider-conversation-bridge.md)
- [Provider profiles](provider-profiles.md)
- [Provider durable evidence](provider-durable-evidence.md)
- [Native runtime composition](native-runtime-composition.md)
- [Governed Linux adoption plan](governed-linux-adoption.md)
- [Release and packaging](release.md)

## Presentation / frontend

- [Pi web chat workspace](web-chat.md): untrusted, read-only chat workspace for
  the Pi CLI/RPC harness; claim boundary, projection contract, security posture,
  and the fixes for the previously reviewed frontend defects.
- [Web chat projection service contract](web-chat-contract.md): proposed
  supervisor-owned projection/stream API needed for live read and interactive
  integration.

These documents describe code or intended contracts. Read the current status
page before treating any design or fixture as a qualified production result.

## Evaluation

- [Candidate experiment manifest](experiment-manifest.md): not a registered
  protocol or G1 result.
- [Future multi-profile scope](tbound-future-work-multi-profile.md).
- [Historical evidence and source map](evidence/README.md), with the
  [machine-readable source identities](evidence/historical-sources.json).

The following records are retained as immutable history in Git, not as current
status or setup authority: dated readiness reviews, blocker inventories,
autonomous setup attempts, and engineering benchmark notes. The source map
identifies their original Git paths and hashes.
