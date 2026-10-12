# tbound documentation index

Navigation index for the current Markdown and JSON documentation under docs/.
It points to existing paths and does not upgrade any implementation or evidence
claim.

## How to read the entries

- Entries marked **dated snapshot** are point-in-time records:
  *dated snapshot — preserve as evidence, not current implementation contract*.
  Other records may cite them by path or digest, so they are never updated in
  place; a new finding gets a new dated record instead of rewriting these.
- Entries without the marker describe current components, contracts, or
  workflows. Where a document's own header says `not production/G1 verified`
  or similar, that boundary stands — this index does not upgrade any claim.
- Files under `docs/evidence/` and the evaluation-evidence index JSON are
  machine-validated, hash-pinned evidence artifacts. Preserve their paths and
  bytes; new evidence is written as a new versioned artifact, never appended
  to an old one.

Current status lives in [implementation-status.md](implementation-status.md),
[tbound-readiness-status-2026-10-08.md](tbound-readiness-status-2026-10-08.md),
and the handoff record [../TASK_STATE.md](../TASK_STATE.md) (outside `docs/`,
an active handoff rather than a newcomer guide).

## Product / readiness status

- [implementation-status.md](implementation-status.md) — narrow demonstration status: maps current evidence to the implementation checklist; does not claim a completed demonstration. *dated snapshot — preserve as evidence, not current implementation contract* (Reviewed: 2026-10-04).
- [tbound-readiness-status-2026-10-08.md](tbound-readiness-status-2026-10-08.md) — implementation and experiment-readiness status: reviewed, independently tested governance components, but not yet a runnable qualified product. *dated snapshot — preserve as evidence, not current implementation contract*.
- [production-candidate-verification-2026-10-10.md](production-candidate-verification-2026-10-10.md) — verification record for the governed Pi product candidate: a locally tested frozen snapshot only; central review holds product acceptance (no provisioned D09 launch, publication receipt, or G1 qualification). *dated snapshot — preserve as evidence, not current implementation contract*.
- [production-pi-session.md](production-pi-session.md) — production Pi session composition (`serve --pi`): CLI composition, command-receipt binding, and managed-root permit path are compiled/tested; real-host positive publication remains externally unverified.
- [thesis-gap-ledger-2026-10-05.md](thesis-gap-ledger-2026-10-05.md) — five-slice evidence and thesis-gap ledger; supplements rather than overwrites [implementation-status.md](implementation-status.md). *dated snapshot — preserve as evidence, not current implementation contract*.
- [next-evaluation-unlocks-2026-10-07.md](next-evaluation-unlocks-2026-10-07.md) — targeted source/document review listing what would unlock the next evaluation steps; an action checklist, not a guest run or containment attestation. *dated snapshot — preserve as evidence, not current implementation contract*.

## Architecture and contracts

- [publication-contract.md](publication-contract.md) — Linux publication contract: implementation contract for `supervisor/internal/publication` (prototype slice 2), not evidence of end-to-end thesis-profile admission.
- [publication-runtime-integration.md](publication-runtime-integration.md) — trusted publication lifecycle integration: presentation-independent workflow finalizer and private staging integration.
- [governed-session-composition.md](governed-session-composition.md) — governed session and D06 composition increment: sessionrepo–D06 source-binding–private-Git staging seam.
- [private-git-staging.md](private-git-staging.md) — implementation note for D06 private-Git staging (prototype increment).
- [native-runtime-composition.md](native-runtime-composition.md) — native Pi runtime composition: the importable native-host seam increment.
- [native-pi-integration.md](native-pi-integration.md) — native Pi host integration (Pi 0.87.1): a deliberately bounded native-TUI research seam, not a production CLI, provider broker, or contained executor.
- [governed-pi-launcher.md](governed-pi-launcher.md) — governed Pi SDK worker and Linux launcher: the actual Pi SDK worker and its Linux process contract; implementation evidence for an experiment harness, not production/G1.
- [governance-companion.md](governance-companion.md) — native-Pi governance companion: the separate governance viewer alongside the native Pi TUI, with the product-boundary statement.
- [pi-session-control.md](pi-session-control.md) — restricted Pi session control (first slice): browser-to-Pi bridge without changing the read-only observability explorer.
- [provider-conversation-bridge.md](provider-conversation-bridge.md) — provider conversation bridge: concrete Go/TypeScript component contract and offline component implementation; no production-launch integration claim.
- [provider-profiles.md](provider-profiles.md) — provider profiles: verified provider contracts and the planned trust boundary (primary profile OpenRouter); does not certify a live provider exchange. *dated snapshot — preserve as evidence, not current implementation contract* (Reviewed: 2026-10-03).
- [provider-durable-evidence.md](provider-durable-evidence.md) — real provider durable synthetic write: scope and reproducible test; source record for [evidence/real-provider-durable-2026-10-05.json](evidence/real-provider-durable-2026-10-05.json).
- [tbound-future-work-multi-profile.md](tbound-future-work-multi-profile.md) — forward-looking design note on multi-profile and pluggable Pi configurations; intended extension work, deliberately outside current capability and thesis scope.

## Install and development

- [launch.md](launch.md) — current run guide for the native Linux Go fixture and existing Hyper-V guest, with M1/M2 status and the present governed-service boundary.

- [install.md](install.md) — installing tbound: a host-side supervisor installed next to an existing Pi; does not modify the global Pi.
- [reproducible-setup.md](reproducible-setup.md) — scripted reproducible evaluation-host setup (native Linux or Hyper-V VM) with a capability-report probe; reproducibility infrastructure, not containment evidence.
- [setup.md](setup.md) — fresh-host setup and offline verification; coordinates the Hyper-V host runbook (`../infra/hyperv/SETUP.md`) and the Ubuntu guest runbook (`../infra/guest/README.md`).
- [dev-launch.md](dev-launch.md) — development launch route `tbound serve --pi --dev-fixture`: the explicit, non-claim-bearing way to run the pinned Pi worker against fixtures.
- [doctor.md](doctor.md) — `tbound-doctor`: standalone readiness check that never changes system state, never needs sudo, and installs nothing unless asked.
- [release.md](release.md) — releasing tbound: maintainer runbook for producing and publishing release artifacts; does not by itself enable governed `serve --pi`.
- [test-run-explorer.md](test-run-explorer.md) — loopback test-run explorer: read-only observability view; independent acceptance checks recorded against an isolated host snapshot (2026-10-05).

## Integration evidence

Machine-validated, hash-pinned evidence artifacts. Preserve paths and bytes;
new evidence goes into a new versioned artifact rather than editing these.

- [evaluation-evidence-index-2026-10-07.json](evaluation-evidence-index-2026-10-07.json) — machine-readable index of evaluation evidence (schema `tbound/evaluation-evidence-index/v1`). *dated snapshot — preserve as evidence, not current implementation contract*.
- [evidence/real-provider-durable-2026-10-05.json](evidence/real-provider-durable-2026-10-05.json) — durable synthetic-write evidence record (schema `tbound.real-provider-durable-evidence/v1`) with SHA-256-pinned test source and logs, produced by the test documented in [provider-durable-evidence.md](provider-durable-evidence.md). *dated snapshot — preserve as evidence, not current implementation contract*.
- [evidence/pi-sdk-fixture-2026-10-06.json](evidence/pi-sdk-fixture-2026-10-06.json) — offline Pi SDK fixture receipt (schema `tbound.pi-sdk-fixture-receipt/v1`, Pi 0.87.1): zero provider network requests, `claim_bearing: false`. *dated snapshot — preserve as evidence, not current implementation contract*.

## Dated review snapshots

Point-in-time review, blocker, and cleanup records; each entry below is a dated snapshot.

- [central-review-corrections-2026-10-07.md](central-review-corrections-2026-10-07.md) — central review: combined corrections and readiness boundary, completed 2026-10-08 against the frozen implementation marker `efda19e6c7b3f6d599f3b52667941c5ccf92c659`. *dated snapshot — preserve as evidence, not current implementation contract*.
- [containment-blockers-2026-10-05.md](containment-blockers-2026-10-05.md) — slice 5: frozen containment blocked; containment `not-established`, G1 `false`. *dated snapshot — preserve as evidence, not current implementation contract*.
- [verifier-blockers-2026-10-05.md](verifier-blockers-2026-10-05.md) — slice 4: reviewed guest offline verifier blocked; no current-source offline verification report or manifest exists from that execution. *dated snapshot — preserve as evidence, not current implementation contract*.
- [native-pi-product-blockers-2026-10-07.md](native-pi-product-blockers-2026-10-07.md) — native Pi product-slice admission stopped at prerequisite admission; no product slice delivered. *dated snapshot — preserve as evidence, not current implementation contract*.
- [native-pi-autonomous-setup-attempt-2026-10-07.md](native-pi-autonomous-setup-attempt-2026-10-07.md) — record of the native Pi autonomous setup attempt with the operator-supplied inputs of that date. *dated snapshot — preserve as evidence, not current implementation contract*.
- [economy-cleanup-2026-10-06.md](economy-cleanup-2026-10-06.md) — bounded, credential-free cleanup applied after the `00ce8849..HEAD` review; deliberately separate from `TASK_STATE.md`. *dated snapshot — preserve as evidence, not current implementation contract*.

## Experiment and infrastructure runbooks

- [governed-setup.md](governed-setup.md) — governed host setup (Phase 2): what an operator must provision before `tbound serve --pi` can be admitted; nothing here is automated.
- [governed-linux-adoption.md](governed-linux-adoption.md) — governed TBound on Linux: adoption and provisioning plan; a researched plan, not completed provisioning or a production launch receipt.
- [experiment-manifest.md](experiment-manifest.md) — candidate experiment manifest, explicitly not G1: a documentary record that makes no containment or provider claim.
- [experiment-host-prerequisites-2026-10-08.md](experiment-host-prerequisites-2026-10-08.md) — observed experiment-host prerequisite inventory (host-only environment observations from 2026-10-08). *dated snapshot — preserve as evidence, not current implementation contract*.
- [guest-profile-prerequisites-2026-10-06.md](guest-profile-prerequisites-2026-10-06.md) — guest/profile prerequisite recheck: current-source guest offline verification remains blocked; host-only recheck. *dated snapshot — preserve as evidence, not current implementation contract*.
- [frontend-observability-plan.md](frontend-observability-plan.md) — recommended plan for the native Pi harness inside tbound (architecture decision 2026-10-06). User-authored plan, preserved as written; do not edit it without its owner.

## Related resources outside `docs/`

- [../README.md](../README.md) — repository overview.
- [../TASK_STATE.md](../TASK_STATE.md) — active handoff record and binding preservation rules.
- [../Makefile](../Makefile) — developer/release helper targets (`build`, `release`, `verify`, `test`, `fmt`, `clean`, and more).
- [../infra/hyperv/SETUP.md](../infra/hyperv/SETUP.md) — Hyper-V host runbook referenced by [setup.md](setup.md).
- [../infra/guest/README.md](../infra/guest/README.md) — Ubuntu guest runbook referenced by [setup.md](setup.md).
