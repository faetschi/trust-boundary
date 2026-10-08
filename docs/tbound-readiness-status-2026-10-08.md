# TBound implementation and experiment-readiness status

**Date:** 2026-10-08. **Bottom line:** the corrected implementation contains
reviewed, independently tested governance components. It is **not yet a runnable
governed Pi deployment for ordinary users or thesis experiments**. Production
`tbound serve --pi` still refuses launch; containment is `not-established` and
G1 has not passed. No H1 result is claimed.

## 1. Review and evidence basis

The central reviewer separately concluded:

- **Code safety:** accept the corrections as bounded component increments;
  no remaining critical defect was identified in the reviewed corrections.
- **Integration readiness:** not ready; the real production composition is absent.
- **Thesis alignment:** aligned as component work, with normative gates open.

The review used committed baseline `efda19e` plus explicit overlays in
`/home/jeli2k/tbound-central-review.9EITMJ` (uncommitted at review time).
Its source-list SHA-256 is
`cc04bd6e78c37b59884be4dea0a5267ffcc667a1b8df43fa33f122e56252f719`.
See [the central review](central-review-corrections-2026-10-07.md) for findings,
traceability and scope. Acceptance of component code is **not** release acceptance.

The reviewed foundations were subsequently preserved as commit
`8d2e47833030b8c723049006252a24d2d6712be0`. Two policy-helper files were normalized
from CRLF to LF by Git; all other reviewed file bytes matched. The exact committed
snapshot passed full Go race/vet/CLI checks and committed-only native/adapter checks.
New vertical-slice work is separate and remains subject to renewed central review.

Coordinator verification of this combined source passed:

- Full Linux `go test -race -count=1 ./...`, `go vet ./...`, formatting and
  source-hash readback, with private ext4/mode-0700 temporary storage.
- Windows build and command/internal/e2e tests on an exact copy.
- Credential-free adapter `npm run check` and 17 native/policy/SDK tests;
  all 16 tested native source/artifact files matched the reviewed freeze.
- Actual Go CLI fixture: success with synthetic captures, durable generations
  and publication. Default/production/arbitrary-profile launches refuse.
- Independent Chrome fixture/replay/unavailable checks: linked records, hostile
  text, desktop/390px layouts and injected delay/gap recovery. These are client
  tests, not evidence of real-session observation or containment.

## 2. Successfully implemented capabilities

| Area | Implemented and tested | Important limit |
|---|---|---|
| Broker and policy | Captured-call correlation, exact identifiers/digests, deterministic four-tool policy and replay/mismatch refusal | Complete real Pi multi-turn provider composition is missing |
| IPC | Bounded strict framing, session/token binding, sequences and correlated results | This alone does not establish peer-authenticated production admission |
| Durable execution | Reads, writes, edits, command leases, approved deltas and sealed generations; durable decision/effect/result ordering | Real command execution still needs the admitted runner/profile |
| Audit and reconstruction | Durable journal, fault refusal, retained transitions, verification/reconstruction and quarantine/recovery components | Structural integrity does not establish external effect coverage or authenticity |
| Publication | Verified-generation application, internal one-use authority, per-object recovery and shared root quarantine | Production trust/runtime composition remains missing; no global filesystem atomicity |
| Runtime foundation | Required decision recording, exact launch-plan binding, immutable captured child configuration, bounded observers and UNKNOWN cleanup | Concrete trusted profile verifier, launcher and descendant settler are not supplied |
| Actual Pi harness seam | Real pinned Pi SDK/runtime and `InteractiveMode`; four proxy tools; result-dependent faux-provider continuations | Stock input is inert; guarded native input is an experimental private patch, not production integration |
| Native action-policy fixture | Default-deny startup/action handling, fixed admitted input surface, digest-bound results, lifecycle/cancellation tests | Checkout-specific imports and unpinned transitive runtime dependencies prevent a portable production claim |
| D06 private Git primitive | Pre-copy Git-metadata refusal, fresh private Git initialization, descriptor-bound execution, config/admin validation and worktree verification | Not connected to durable session provenance or demonstrated worker isolation; D06 is not satisfied |
| Governance companion | Read-only diagnostic timeline, correlation, source/gap semantics, safe document loading, bounded SSE and fixture/replay views | Live file sources refuse; no actual admitted session supplies its live observations yet |
| Evaluation index | Strict bounded obligation ledger, checked arithmetic, explicit incomplete states and citations | Planning/structural validation only; not an evidence attestor or completed study |

Historical accepted increments include publication integration `a977766`, the
SDK/browser fixture `9144d59`, and cleanup `7a80f5a`. A genuine provider-to-durable
write was previously demonstrated (`1bedc0e`), but **not** a complete real-Pi
four-tool conversation or a deployed containment profile.

## 3. What users cannot rely on yet

1. Launching Pi through TBound for a normal project task is not a delivered
   governed workflow. The Go `--native-fixture` route **does not launch Pi**.
2. Provider responses/results are not yet connected end to end between the real
   Pi process and the trusted broker over successive generation changes.
3. D06 initialization/provenance, disposable command views and publication are
   not assembled into one admitted production session lifecycle.
4. Real executable/environment/profile authenticity, command/descendant
   settlement and the frozen containment boundary have not been established.
5. The companion cannot yet observe that real session. Fixture completion and
   replay are diagnostic, not durable effect authority.
6. There is no current-source release qualification on the declared evaluation
   host, with reset, recovery and independent filesystem/network/process oracles.

The code is therefore suitable for controlled component development and review,
not unrestricted project use, adversarial trials on the workstation, or headline
thesis runs. Operator/signing/provisioning prerequisites are explicit blockers;
interfaces, caller assertions and test callbacks cannot replace them.

## 4. Thesis-guided route to readiness

The thesis-ready target is **one pinned Pi/model/Linux profile and one complete
workflow**, not a general SaaS or multi-agent platform. Pi must remain the actual
harness; TBound owns authority outside it. Keep the CLI-first thesis path and
the optional native TUI/companion product work distinct. If the TUI itself enters
the evaluated profile, reconcile D15/profile scope prospectively before exposure.

| Priority | Next implementation/qualification step | Exit evidence |
|---|---|---|
| 1 | Implement one concrete CLI-owned runner and actual pinned Pi process launch; protected profile loading, exact argv/cwd/env, authenticated bootstrap/IPC and owned stop/recovery | Real Pi starts through TBound without stock-tool/provider fallback; invalid identity/profile and uncertain cleanup refuse |
| 2 | Connect the trusted broker's provider stream to Pi and bind every result into subsequent turns; credentials remain outside Pi | E05 `read(g0) -> edit(g1) -> bash(g2) -> read(g2)`, including denial, cancellation, limits and ambient-auth rejection |
| 3 | Integrate private Git with the actual sealed source/session; persist provenance before exposure and create isolated command views | No live Git references or worker `.git` reachability; source-swap faults refuse; every accepted delta is attributed |
| 4 | Supply the real host/profile-backed runner, publication trust and surviving settlement/evidence owners | Profile-conforming effects and publication; durable pre-effect/result ordering; quarantine/recovery without replay |
| 5 | Provision and freeze the dedicated evaluation Linux environment through reviewed operator setup | Signed image/entrypoint, immutable profile/source identities, effective Podman/crun, namespace/cgroup/pidfd/Landlock/seccomp/NNP, no unauthorized egress and no surviving descendants |
| 6 | Run WP1/G1's five falsification spikes and E01/E04/E05/E06 plus E07 conformance on that host; qualify reset/offline reconstruction | Independently reconstructable attempt/decision/effect/outcome evidence, failure/cancellation coverage and exact source/profile commitments |
| 7 | Refresh the evidence inventory, resolve protocol ambiguity, freeze corpus/seeds/caps/oracles and register the evaluation | Reproducible clean-machine dry run and prospective protocol; only then expose headline datasets |

Environment provisioning and code integration have dependencies and can be
prepared in parallel, but operator authorization is not inferred. Do not start
untrusted guest trials before the relevant isolation/reset gates pass.

### Five feasibility spikes, not just a smoke test

The implementation todo requires **Pi closure**, **broker/correlation**,
**repository isolation/provenance**, **Tier-4 launcher/containment**, and
**audit failure** spikes, plus E04/E06 fixtures, on the declared host.
Windows/WSL component passes are useful development evidence but not substitutes.

The recovery specification declares **48** trials while enumerating points that
imply **51**. Resolve this by a dated protocol/specification reconciliation before
registration/execution. E07 publication faults are a separate conformance duty,
not replacement recovery trials. Do not silently change the denominator or the
associated workload budget.

## 5. When it is ready for the thesis

Call TBound **experiment-ready** only when the selected configuration can:

- admit a protected, reproducibly identified profile and launch actual Pi;
- govern the four-tool multi-turn chain through broker, durable execution and
  isolated command views without ambient authority;
- verify and publish attributed changes, settle descendants or retain UNKNOWN,
  and reconstruct outcomes without rerunning effects;
- pass the registered-host closure/provenance/containment/durability checks and
  produce complete independent raw evidence with reliable reset/recovery;
- expose the required CLI controls/status/audit and preserve frozen run identities.

That establishes an implementation and environment suitable for experiments.
It **does not predetermine H1**: security, friction and utility results come from
the later registered dataset runs. G0, calibration, protocol registration and
reviewer obligations also remain research work, not automatically completed by code.

## Guidance consulted

Authoritative sibling tree: `../MSE_MA_Thesis/agentic-harness/`.

- `tbound-thesis.md`: artifact/scope and authority model (lines 8–13, 111–121,
  156–158); CLI/governed admission (346–377, 449–469); scoped H1 (254–264).
- `tbound.md`: non-normative orientation, especially actual Pi/external authority
  (11–26, 51–84); not an implementation-result source.
- `todo-implementation-tbound.md`: non-normative tracking note anchored in WP1/G1;
  the complete workflow, five spikes and exit criteria (9–26, 44–60).
- `AH-technical/00-roadmap.md`, `02-process-model.md`,
  `02-1-recovery-and-continuation.md`, `03-architecture.md`,
  `06-1-language-decision.md`, `10-decision-register.md`,
  `11-evidence-contract.md`: normative technical/evaluation requirements as traced
  in the central review. No normative thesis file was changed for this summary.
