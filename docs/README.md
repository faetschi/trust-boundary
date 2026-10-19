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

## Presentation / frontend

- [Pi web chat workspace](../extensions/webchat/docs/web-chat.md): untrusted, read-only chat
  workspace for the Pi CLI/RPC harness; claim boundary, projection contract, security posture,
  and the fixes for the previously reviewed frontend defects. The React client at `/app/` is the
  default UI (`/` redirects there); the legacy workspace at `/legacy/` is deprecated.
- [Web chat extension README](../extensions/webchat/README.md): what the extension contains, how
  to run and test it, and its dependency rule.
- [Web chat projection service contract](../extensions/webchat/docs/web-chat-contract.md): proposed
  supervisor-owned projection/stream API needed for live read and interactive integration.
- [Web chat extension decision record (ADR-0001)](../extensions/webchat/docs/adr-0001-optional-extension.md):
  the web chat is an optional standalone extension in `extensions/webchat/`; the core neither
  requires nor imports it. It also records the frontend toolchain direction and the P0 to P3 phase
  status, with commits and receipts.
- [Web chat frontend](../extensions/webchat/docs/frontend.md): the React and TypeScript client under
  `/app/`. Phases P1 to P3 are committed (through `57e6406`), the React client is the default UI,
  and the Linux gates and open items are listed there.
- [Web chat frontend dependencies](../extensions/webchat/frontend/DEPENDENCIES.md): the pinned direct
  dependencies, the licence census, and the install-script policy.
- Web chat phase evidence (dated snapshots; the current state is in `frontend.md`):
  [Phase 2a parity](../extensions/webchat/docs/phase2-parity.md),
  [Phase 2b markdown AST](../extensions/webchat/docs/phase2-markdown.md) and
  [Phase 3 parity table](../extensions/webchat/docs/phase3-parity.md).
- [Web chat design language](../extensions/webchat/docs/design/design-language.md): the visual spec
  (tokens and contrast receipts). The current static design mockup is
  [v3](../extensions/webchat/docs/webchat/mockups/tbound-redesign-v3.html), with demo data and
  offline. [v2](../extensions/webchat/docs/webchat/mockups/tbound-redesign-v2.html) is the earlier
  layout mockup, kept until sign-off.
  In v3, buttons inside the desktop frames jump to the frame that shows the result.
- [Modular architecture vision](modular-architecture-vision.md): VISION / FUTURE-WORK, not
  part of the thesis claim. A trust-tiered plugin/extension model, the headless CLI direction
  (`serve --headless` is future work), and decision records for the open questions.

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
