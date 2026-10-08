# Next evaluation unlocks — 2026-10-07

**Status:** targeted source/document review only. This is an action checklist,
not a guest run, containment attestation, G1 result, or thesis-completion claim.

## Review boundary and pins

- Review date: **2026-10-07**. Current checkout HEAD at review was
  `efda19e6c7b3f6d599f3b52667941c5ccf92c659` (`docs: coordinate parallel native
  Pi, governance frontend and evaluation work`).
- The latest accepted publication implementation is `a977766` (full identity
  and byte/test evidence are in `TASK_STATE.md` and
  `docs/publication-runtime-integration.md`). The later HEAD is documentation
  coordination; it must not be treated as a new implementation freeze without
  a source-scope comparison.
- `docs/native-pi-product-blockers-2026-10-07.md` and
  `docs/native-pi-autonomous-setup-attempt-2026-10-07.md` are pre-existing
  other-work documents, included only as **external recorded sources**. Their
  host statements were not revalidated by this task and must not be presented
  as current-host evidence. The blocker document is recorded at commit
  `c08b1cf1ea1aeef19a84f13a449da12870444e43`, blob
  `fe12a97fb1c48b79052346757089fe7c0835853d`, SHA-256
  `524bca03c367c661715f5e8f76b1b6145f466ff07a0680739b8e3e97a0d075c6`.
  The setup addendum is based at commit
  `7aed2a183a1bef609a8cc536498f751caa20c0d1`, but is currently dirty:
  HEAD blob `8fa82e9afbb43fee64cc6a6a35c4d02aaa18fe64`, working-tree blob
  `ac29a71edf9365dc085a300aa1ae72cf7e939c5c`, SHA-256
  `5bcb600f773f25f54eb63aa44e0922cf3668717b792c206cc6b516a728560f23`.
  The recorded observation times are `2026-10-07T14:57:48.7771910Z` and
  `2026-10-07T15:16:50.1686887Z`; this document did not re-probe the host,
  query tasks, open keys, invoke a controller, or access `vm_start.txt`.
- Historical 2026-10-05/06 guest and host results remain historical. They do
  not establish current-source verification or current containment.

## Priority 0 — exact minimum unlock for current-source offline verification

The minimum is **both** a reviewed source/controller correction and an admitted
host/operator context. A source-pin change alone is insufficient.

### A. Source/controller correction scope (narrow, source-only)

Review one exact implementation snapshot first. For the current publication
source this is expected to start from `a977766`; if the coordinator selects a
later source identity, record the exact diff and complete source manifest. Then
make only these verifier corrections before any protected run:

1. **Git/export argv:** construct each dynamic PowerShell value as one array
   element. The common vector must contain complete tokens for
   `safe.directory=<repo>`, `core.autocrlf=false`, and `-C <repo>` (with the
   repository value not split). The archive path must use the same explicit LF
   setting. Exercise spaces, quotes, trailing backslashes, and metacharacters
   with dummy values only.
2. **SSH/SCP argv:** parenthesize the dynamic values for
   `UserKnownHostsFile=<dummy path>` and `HostKeyAlias=<dummy alias>`. Test the
   shared SSH options and both SSH/SCP positional tails; neither client is
   needed for this correction.
3. **Pre-queue admission:** validate the reviewed source identity, protected
   profile, host/guest identity pins, and required receipts before any mutating
   task trigger. `Inspect` may remain the read-only admission step; mutation
   identity pins must not be checked only after queueing.
4. **Cleanup arming/order:** arm cleanup only after exact host identity/state
   admission. The cleanup predicate must require cleanup enabled, failure,
   an identity derived from validated protected evidence, and
   `(queueAttempted || disconnectAttempted)`. Set each attempt flag before its
   request so an accepted request with a lost response still cleans up. A
   pre-queue context/Inspect/SSH failure must produce zero cleanup mutation;
   uncertain queue or Disconnect requests retain cleanup. Do not reconnect from
   a catch/finally path; require a fresh host-observed Off/disconnected gate.
5. **Tests and pin update:** add dummy-only vector and flow coverage, then
   separately review the new controller source pin and LF archive/export
   identity. Do not adopt the whole dirty controller tree or change operator
   rights as part of these corrections.

Expected source evidence: exact argv arrays, 32-combination cleanup result
table, eight named failure/uncertainty cases, source hashes, explicit
`core.autocrlf=false` archive invocation, and a reviewed source identity. This
evidence proves only source behavior; it does not authorize a task or guest run.
Pinpoint source basis: `docs/guest-profile-prerequisites-2026-10-06.md`,
2026-10-06, “Smallest future controller correction/test set”, lines 232-250;
`docs/verifier-blockers-2026-10-05.md`, 2026-10-05, “Exact unlock”, lines
84-108; and the preserved controller source definitions, read-only, at
`infra/hyperv/Invoke-TBoundTrustedOfflineVerifier.ps1`, lines 123-125,
228-231, 403-512. No controller source was edited here.

### B. Minimum protected host/operator unlock

An authorized operator must perform the following through the reviewed runbook;
the current unelevated SID ending `...-1001` cannot substitute for the required
operator SID ending `...-1004`.

1. Inspect active work through an authorized channel, preserve the golden
   baseline, and arrange the pinned VM **Off with its single NIC disconnected**.
   Do not force a transition merely to install or test.
2. In one authorized elevated **64-bit Windows PowerShell 5.1** session, run
   the hash-guarded bootstrap from protected staging, after reviewing the exact
   bootstrap/installer/action/policy/initializer hashes. The installer must
   validate the pinned Gen-2 VM, 8 vCPU/16 GiB profile, disk chain, checkpoint
   and firmware policy, and storage gates; it must not silently overwrite an
   existing installation.
3. Read back the protected ProgramData bundle, normalized action/profile
   hashes, ACLs, and all five fixed `\TBound\` tasks. Confirm the intended
   SYSTEM/highest/on-demand definitions and exact operator run/query ACLs,
   rather than treating task absence as a principal mismatch.
4. Run the protected key initializer as the exact `...-1004` operator. A human
   Admin must enroll only its public key in the guest with the documented
   `restrict` option; private material remains operator-only. The old Admin
   maintenance key is not this enrollment.
5. Read back protected receipts, profile/ACL identity, case-sensitive guest
   host-key pin, and operator identity. Only then run the corrected, source-pinned
   verifier and retain fresh source/run identities, receipts, terminal exits,
    `verification.json`, `artifact-manifest.json`, and recomputed hashes.

Pinpoint source basis: `docs/guest-profile-prerequisites-2026-10-06.md`,
2026-10-06, “Unlocks requiring user/operator context”, lines 252-286; and
`infra/hyperv/TBOUND-HOST-AUTOMATION.md`, preserved source reviewed
2026-10-07, “One-time administrative install”, lines 35-59. These are setup
requirements, not evidence that setup exists; the two native-Pi documents named
above remain external recorded sources only.

Required authority: human/operator authorization for the protected install,
elevation/UAC consent, exact SID enrollment, guest public-key enrollment, and
the reviewed lifecycle run. No agent-side retry, task substitution, password
automation, or maintenance-key reuse can satisfy this unlock.

## Priority 1 — what can be implemented without privileged setup

| Component | Safe unprivileged increment | What remains an external gate | Expected evidence |
|---|---|---|---|
| Profile admission | Typed canonical profile/manifest comparison; reject missing, drifted, unsigned, or contradictory identities; injected offline-verifier interface with typed failure | Approved signer/public key, signed image/index, offline Cosign provenance and signature verification | Unit tests with sanitized descriptors; no successful attestation from a caller boolean |
| Entrypoint contract | Reviewable one-language source/build recipe and sealed-status protocol; fail closed on malformed status | One selected Go **or** minimal Rust entrypoint, pinned toolchain/SBOM, signed in-image binary and measured identity | Source/build fixture and rejection tests; not a claim-bearing launch |
| Seccomp/Landlock | Deterministic profile digest/vector validation, rights-to-ABI planning, descriptor-only rule planning, unsupported-right rejection | Reviewed launcher-plus-target static profile, registered ABI/rights, actual kernel enforcement and process-wide application | Mock/status tests and exact digest comparisons; no inferred capability pass |
| cgroup/pidfd settlement | Strict fixed-root state machine, malformed-event rejection, pidfd/`populated` accounting model with injected lifecycle faults | Authorized non-threaded cgroup-v2 delegation, writable `cgroup.kill`, effective bounds, surviving runner and recursive settlement | Mock transition/fault table; no guest teardown claim |
| Publication/recovery | Continue source-level lifecycle/API tests and production wiring review; retain `UNKNOWN`/no-replay semantics | Claim-bearing runner, external observations, all E07 boundaries and current profile admission | Component evidence only; `a977766` does not close E07 or production admission |
| D06 staging | Coordinator may pursue the separately started safe conformance implementation and tests under the reviewed D06 scope; this task neither owns nor changes that implementation | Normative thesis decision and later G1/E04/E05 evidence | Separate component evidence only; no D06 thesis acceptance, waiver, or G1 pass |

The safe work above must remain explicitly non-claim-bearing until the external
gates are satisfied. In particular, rootless Podman smoke tests, WSL tests,
digest presence, or a dummy verifier do not establish frozen containment.
Pinpoint source basis: `docs/guest-profile-prerequisites-2026-10-06.md`,
2026-10-06, “What source work is possible now, and what cannot be guessed”,
lines 288-311; and `docs/containment-blockers-2026-10-05.md`, 2026-10-05,
“Exact unlock and remaining implementation”, lines 41-60.

## Priority 2 — evaluation studies and freezes still missing

The publication integration does not reduce the following obligations:

1. **G0 literature/review freeze:** the bounded search is still partial. The
   **2026-09-23** recorded flow reports 98 unique retained-window records, 53
   transcribed, 45 not transcribed, 53 title/abstract screened, 0 full-text
   records, 0 final full-text inclusions, and 0 second-human reviews. These are
   time-bound existing counts, not a fresh review here. Source: `quellen/g0/
   prisma-flow.md`, “Flow table”, lines 7-35; status and pending work:
   `quellen/g0/README.md`, lines 18-60. Freeze the completed screening,
   reviewer agreement/adjudication, extraction sheet, PRISMA counts, and dated
   contribution statement before claiming the gap/novelty result.
2. **G1 profile freeze and conformance:** the roadmap’s **five** feasibility
   spikes and G1 requirements are normative: `AH-technical/00-roadmap.md`,
   last reviewed 2026-09-29, §5 WP1/G1, lines 114-128. Freeze one
   VM/filesystem/kernel,
   Podman/crun, image/index, signer/verifier, entrypoint language/binary,
   static seccomp, Landlock ABI/rights, rootless mapping, delegated cgroup and
   pidfd behavior, OCI identity, and per-run manifest. The current candidate
   `docs/experiment-manifest.md` is expressly not a freeze. E01 capability
   evidence, E04 temporal create/delete/modify-and-revert fixtures, E05, and
   E06 denied-read exposure matrix remain required.
3. **E05 native governed conversation:** complete the real pinned Pi
   `read(g0) -> edit(g1) -> bash(g2) -> read(g2)` provider/adapter/broker
   lineage with cancellation, result binding, bounded content blocks, and
   ambient-provider rejection. The exact ordered fixture and evidence
   requirements are `AH-technical/11-evidence-contract.md`, last reviewed
   2026-09-29, E05, lines 164-174: four ordered operations over two successive
   generation transitions. The historical single synthetic write is not this
   multi-turn fixture.
4. **E07 publication-boundary conformance (not the secondary recovery study):**
   wire trusted publication authority through the production workflow/CLI and
   exercise the six E07 operation kinds—create, overwrite, delete, rename,
   replacement, and directory creation—at every registered filesystem
   durability boundary. Each fault must classify as `SUCCEEDED`, `FAILED`, or
   `UNKNOWN` without replay. Exact source: `AH-technical/11-evidence-contract.md`,
   last reviewed 2026-09-29, E07, lines 195-211. Component package tests from
   `a977766` are partial component evidence only; the publication document
   explicitly says they are not the registered recovery study or production
   admission: `docs/publication-runtime-integration.md`, lines 184-193.
   E07 boundary faults are conformance obligations and must not be counted as
   the separate 16x3 secondary recovery trials below.
5. **D32/E03 calibration freeze:** qualify the deterministic attacker on
   disjoint pilot fixtures for every effect channel; freeze attacker version,
   transition projection, seed/state schedule, all-299 adaptive scope, caps,
   rates, failure/censoring rules, and the final pilot. Every claim-bearing P/C
   pair must manifest in P both statically and under the adaptive budget; a
   `channel_non_discriminative` pair blocks AC-Security. Exact source:
   `AH-technical/11-evidence-contract.md`, last reviewed 2026-09-29, E03,
   lines 55-136; roadmap freeze/workload boundary, lines 219-258.
6. **Registered H1 workload:** after the above freeze, run 299 Configuration C
   adversarial trajectories; 1,196 P/C calibration executions (299 x 4); 100
   paired compliant B/C/V controls (300 configuration runs); 20 ambiguous
   cases; and the registered 50-case x 3 stability subset. The exact planned
   maximum is 1,983 execution units before permitted external reruns, with 100
   additional variance-subset repeats because ordinary Configuration C runs
   are already included. Source: `AH-technical/00-roadmap.md`, last reviewed
   2026-09-29, §2 lines 19-28, WP4 lines 241-258, and WP5 lines 262-277.
   Preserve the intention-to-test ledger and pre-registered external-rerun
   rule. These are not supplied by local unit or publication tests.
7. **Secondary recovery study (separate from E07):** the recovery source
   declares a 16 x 3 = 48 clean-VM trial study: seven main crash-injection
   points plus nine incident injections, each run three times. Exact source:
   `AH-technical/02-1-recovery-and-continuation.md`, last reviewed 2026-09-28,
   §8 lines 337-376. However, that same passage enumerates **eight** main
   labels (`RECEIVED`, `INTENT_DURABLE`, `DECIDED`, `AUTHORITY_RESERVED`,
   `RELEASE_CLAIMED`, start of `EXECUTING`, `OUTCOME_DURABLE`, and
   `CHECKPOINTED`) while saying seven. This is a source ambiguity requiring
   dated reconciliation before execution: the declared 48 follows 16 x 3,
   whereas the enumerated 8 + 9 would imply 17 x 3 = 51. Do not silently turn
   E07 boundary faults into these trials or report 48 as completed.
   The required 48/48 correctness and 0/48 forbidden/duplicate-effect targets
   are also only prospective engineering criteria in that source.
8. **Utility and audit-ordering measurements:** run paired governed/native-Pi
   controls for task and operational latency, tokens, cost, retries, task
   success, audit volume, setup failures and reliable resource measures. Exact
   utility source: `AH-technical/00-roadmap.md`, last reviewed 2026-09-29,
   WP4 lines 186-208. Keep the asynchronous-versus-hybrid audit comparison
   secondary; its source is `AH-technical/02-1-recovery-and-continuation.md`,
   last reviewed 2026-09-28, §8 lines 392-399. Pre-effect durability remains
   mandatory in the treatment.
9. **G4/G5 registration and reproduction:** complete the clean-machine raw-
   evidence dry run, OSF registration, cross-linked Zenodo archive, exact
   signed Git tag/commit, hashes/seeds/reviewer roles/amendments, then reconcile
   all raw artifacts and analysis outputs with the frozen trial ledger. Exact
   freeze and G4/G5 source: `AH-technical/00-roadmap.md`, last reviewed
   2026-09-29, WP4 lines 194-200 and G4/WP5 lines 260-280.

## Native Pi TUI target: product scope versus thesis scope

The current product target is `tbound serve --pi` with the **actual Pi harness
and native Pi TUI**, plus a separate governance interface. It is not implemented
or admitted. Stock TUI routes such as `!`/`!!` shell execution,
`session.executeBash`, model/login changes, reload, and import/share require
explicit governance or refusal; replacing the four model-visible tools alone
does not close them. PTY lifecycle, resize, escape-sequence handling,
backpressure, cancellation, and structured event correlation also need a
reviewed adapter contract.
Pinpoint product source: `docs/frontend-observability-plan.md`, 2026-10-06,
“Architecture decision”, lines 1-36, and “Why not embed the stock TUI
immediately?”, lines 69-85. The recorded blocker document’s product status is
not independently reused as current-host evidence.

For the thesis MVP, D15 still makes the CLI-first/custom four-proxy surface the
claim-bearing client and treats a polished TUI as deferred. Therefore choose
one of these explicitly before registration:

- keep native TUI as a non-claim-bearing product milestone and evaluate the
  pinned SDK/closed-tool profile; or
- amend the protocol before exposure to make native TUI the evaluated profile,
  then freeze its launch/configuration/interactive surface and renew closure,
  provider, PTY, lifecycle, and E01/E05 evidence.

Do not silently count native-TUI work as completion of the existing SDK profile,
and do not use a replacement browser chat as the native product.

## D06 choice and thesis implication

**Recommended choice:** retain D06 as the normative thesis decision and label
the current publication path a temporary, explicit prototype divergence. The
`a977766` evidence remains useful for per-object publication/recovery behavior,
but it cannot be described as process §6.1/D06 compliance, independent private
Git staging, G1 evidence, or H1 support.

The thesis-compatible path is to implement the filtered snapshot followed by a
new private repository and then rerun the affected isolation/provenance,
E04/E05, publication, and recovery evidence. If that cannot be delivered, a
permanent waiver requires a dated replacement ADR that changes process §6.1,
the threat/validity argument, the evidence contract, and the registered claim;
it is not a documentation-only status change. A silent waiver leaves the
prototype demonstrable but leaves AC-EVQ/G1/H1 unsupported for the D06-dependent
claim. Pinpoint normative source: `AH-technical/10-decision-register.md`,
2026-09-29, D06 and D06/D07 refinement, lines 18-19 and 44-57; implementation
status source: `docs/publication-runtime-integration.md`, lines 165-182.
The coordinator's separately started safe D06 conformance implementation and
tests are not silently adopted by this task and cannot by themselves change the
normative choice or establish thesis acceptance.

## Dummy-only regression design (not executed here)

If the coordinator wants a new package under
`supervisor/internal/preflightreview/`, keep it pure and fixture-only. It must
not import or invoke host automation, Hyper-V, SSH/SCP, Task Scheduler, a live
provider, credentials, or network dependencies.

- **Vector tables:** use only values such as `C:\\DUMMY repo with spaces`,
  `C:\\DUMMY key\\id`, `C:\\DUMMY known\\known_hosts`,
  `DUMMY_HOST_ALIAS`, quotes, trailing backslashes, and metacharacters. Assert
  that each dynamic assignment is one token; assert the explicit LF Git setting;
  assert the corrected option/positional counts (Git grouping and the 43-option
  SSH base described by the prerequisite report). Do not execute a native
  client. An in-memory argv recorder is sufficient.
- **Cleanup matrix:** model the predicate over all 32 Boolean combinations and
  explicitly cover context, Inspect, pre-queue SSH, uncertain queue, uncertain
  Disconnect, missing identity, and success. Assert zero task calls before
  admission; allow cleanup only for validated identity plus a queue or
  Disconnect attempt; set attempt flags before the stub request; never model a
  reconnect in finalization.
- **Evidence:** retain the sanitized input table, expected arrays, stub call
  log, and test exit/format output. Mark it as source-model evidence, not live
  controller evidence. A Go package that duplicates the controller logic
  without a separately reviewed source change must not be presented as fixing
  the controller.

## Safest next increments for the coordinator

After the native/frontend milestones, the coordinator can safely start, in this
order, without privileged setup: (1) the dummy-only preflight review package
or an equivalent source-review artifact; (2) an explicit native-TUI closure /
PTY contract decision; (3) review the coordinator's separately started D06
safe-conformance implementation and isolated tests without claiming thesis
acceptance or changing the normative choice; and (4) the new machine-readable
evaluation ledger and validator, which labels each obligation
`not-evidenced` until its required run exists.

The coordinator should **not** start controller execution, protected helper
installation, key enrollment, guest transfer, VM/NIC transitions, signer
enrollment, provider runs with unapproved credentials, or H1/G1 reporting from
these increments. Existing infrastructure and other-work documents remain
untouched. Owned changes from this task are limited to this checklist, the new
evidence-index JSON, and its pure read-only validator/tests.

### Evidence-index validator boundary

The new `docs/evaluation-evidence-index-2026-10-07.json` records 16 ledger
entries and a checked citation count of 34. Its validator performs structural
checks only: strict recursive JSON field/type/null/duplicate/trailing-data
validation, valid calendar dates and ordered line ranges, bounded collection
and string sizes, exact registered external-source references, nonnegative
counts, and explicitly declared, overflow-checked arithmetic such as
`299 x 4 = 1,196` and `100 x 3 = 300`. The index supports planning the
registered workload and setup obligations; it is not runtime release,
experiment readiness, or acceptance evidence. It retains the source-declared recovery ambiguity
(`48` declared versus `51` if all eight enumerated main labels are included)
without choosing a protocol.

Commit/blob IDs and SHA-256 values are syntax-checked and matched to ledger
identities where declared; the validator does **not** resolve commits, inspect
artifact existence or hashes, inspect host state, authorize fulfilled claims,
or attest runtime/provider/containment behavior. Those remain coordinator-held
review receipts. The current ledger has all `fulfilled` values false.

### Sources reviewed

- `TASK_STATE.md` (top current state and publication identity)
- `docs/guest-profile-prerequisites-2026-10-06.md`
- `docs/native-pi-product-blockers-2026-10-07.md`
- `docs/native-pi-autonomous-setup-attempt-2026-10-07.md`
- `docs/verifier-blockers-2026-10-05.md`
- `docs/containment-blockers-2026-10-05.md`
- `docs/publication-runtime-integration.md`
- `docs/experiment-manifest.md` and `docs/thesis-gap-ledger-2026-10-05.md`
- `docs/frontend-observability-plan.md`
- `docs/evaluation-evidence-index-2026-10-07.json` and the pure validator in
  `supervisor/internal/evaluationindex/`
- `../MSE_MA_Thesis/agentic-harness/AH-technical/00-roadmap.md`,
  `02-process-model.md`, `02-1-recovery-and-continuation.md`,
  `03-architecture.md`, `06-1-language-decision.md`,
  `06-4-sandboxing-decision.md`, `07-1-runtime-flow.md`,
  `10-decision-register.md`, and `11-evidence-contract.md`
