# TBound task state

Updated 2026-10-13. Keep current state concise. User documentation: README.md and
docs/status.md. Historical sources: docs/evidence/historical-sources.json.

## Scope and claim boundary

- Branch codex/tbound-prototype; pre-cleanup HEAD29196d664316886e929c963ce2a404a876e4c6cf.
  Reviewed Windows ACL increment committed as a281699; the coherent CLI
  prototype checkpoint committed as 178acdd. Production remains unqualified.
  User explicitly requested incremental
  commits on 2026-10-13; commit reviewed, verified, coherent scopes.
- User authorized reductions, normal Linux/adapter CI, clearer documentation,
  orchestration/fixture separation and qualification work.
- User replaced embedded/private Pi SDK installation with the official Linux Pi
  CLI beside tbound. Pin CLI0.87.1; RPC plus explicit mediation extension.
  No SDK or unmediated CLI fallback may be described as governed execution.
- G1=false; containment not established; H1 not executed. Component/fixture,
  packaging and capability checks are not thesis or containment results.
- CLI-first sequential read/write/edit/bash. Browser/chat/TUI experiments are
  outside the default thesis artifact.
- Preserve broker lineage, deterministic gate, durable authority before effects,
  sealed generations, independent evidence, validated host publication and
  UNKNOWN/quarantine recovery. D06 remains conformance-only until independently
  qualified. Publication is per-object reconciliation, not global atomicity.
- Recovery denominator48 (16 scenarios x3); eighth crash point belongs to E07
  conformance. Carry this dated decision into registration; old conflicting
  snapshots remain historical.

## Current batch

- Resumed on codex/tbound-prototype; no branch change. Fresh component
  records: ../review-artifacts/tbound-resume-2026-10-13/. Linux Go 1.27.1 on WSL
  ext4: unit/race each 812 passes, zero failures, seven skips; vet/gofmt clean;
  nine staging regressions passed. Use a short private 0700 TMPDIR: long paths
  exceeded AF_UNIX limits in three service socket subtests (logs preserved).
  This Linux snapshot precedes the Windows-only ACL and documentation updates.
  Post-fix Windows full Go tests and vet passed. Isolated Node 24.21.0/Pi 0.87.1
  adapter check passed all 17; actual Go offline CLI fixture passed. Real provider and host-containment
  checks remain skipped/unqualified; these are component results only.
- Windows credential ACL parsing now retains GetAce pointers and checks the
  complete SID length and maximum 15 subauthorities before conversion; boundary
  tests added. Critical reviewer accepted this bounded increment; committed
  as a281699 with only the two ACL files. Tests do not
  exercise live DACL APIs. Architecture/decision text now agrees with the
  implemented official CLI argv contract and retained production refusal.
- User authorized preparing a reproducible ABI 8 guest setup. Preparation is
  not image registration or qualification. Hyper-V inspection is denied to the
  current process; current SSH address is not recoverable from saved preflights.
- Actual isolated runtime staging passed (14,103 files). The unchanged production
  tree verifier accepted its read-only bind mount inside a private WSL mount
  namespace: sha256:f6f1900633a22071fad7ecfcb4c2f21ef6046c6820231bab3d22c75d9b3a21fd.
  This is a component tree check, not signed-profile/D09 source admission or
  containment. The staged closure remains user-owned, unsealed and unadmitted.
  Candidate clean-guest worksheet: infra/repro/CLEAN-GUEST-QUALIFICATION.md;
  reviewed with pinned tool paths and exact ABI gate. No clean VM was created or registered.
- Added `tbound-profile runtime-bundle-digest --root` using the unchanged
  read-only production tree verifier. Focused refusal tests and positive
  staged-tree command check passed; critical review accepted the increment.
  A new Windows full run failed intermittently in
  TestSessionCloseDeadlineRetriesSharedCleanup with TerminateProcess access
  denied and UNKNOWN settlement, after earlier full passes. Fixed exit-vs-Kill
  race by waiting for actual completion within caller context before settlement;
  UNKNOWN remains without exit evidence. Deterministic delayed-exit/deadline/
  retry tests passed; reviewer accepted. Original failing run remains recorded.
- Production/dev CLI RPC initialization now validates and permits exactly the
  official CLI profiles; production no longer misses its RPC client due to a
  developer-only guard. Legacy profiles rejected; production launch refusal
  unchanged. Critical reviewer accepted this bounded increment.
- Earlier accepted Go checks: Linux unit/race each 820 passes, zero failures, seven
  skips; Windows 278 passes, zero failures, 13 skips; vet clean on both platforms.
  Final actual Go offline CLI fixture passed. Scratch verifier is only in the
  isolated WSL copy; original source gofmt is clean. These checks preceded the
  later closure checks below; both accepted code increments are committed.
- Remaining critical review finding: failed RPC responses in pumpEvents must
  match command=prompt and the active request ID; a late abort response must
  not settle another prompt. Correlation fix applied: only failed prompt
  responses matching an active request ID emit a worker error. Corrected the
  regression fixture's missing error string while retaining pipe-controlled
  response delivery after lease initialization. Red reproduced all three
  wrong-response cases; green and 30 race-enabled repetitions passed. Linux
  piruntime package unit/race/vet passed. Critical reviewer accepted safety,
  integration scope and thesis alignment; original
  bad-fixture and corrected red/green logs are retained with the checkpoint.
  D09 launcher candidate preparation finished, but the proposed replacement of
  the refusal stub is NOT applied. Independent critical review rejected safety
  and integration: failed-response correlation, ignored/sticky child-end close
  errors, double RPC-input closure, nil-context cleanup and insufficient full
  launch/proof/settlement tests must be resolved first. Thesis claim boundary
  remains aligned; host qualification is a separate gate.
  Preserve every unproved admission gate. The saved 313-file acceptance
  checkpoint predates the new correlation regression test.
  Correlation and closure implementation/review tasks completed and committed.
- RPC input-close code passed critical safety review. Atomic closed state and
  once-only closure avoid writeMu; old code reproduced 16 underlying closes.
  Required test fixes applied: bounded failure cleanup and explicit sticky
  retry. Windows count30, Linux race count30 and package unit/vet passed.
- Shared endpoint owner/readiness patch applied after repairing the proposals:
  close before c.mu to unblock sendControl, retain shared/child/full close
  errors, stop failed launch before readiness, and propagate RPC close errors.
  No raw-FD fault injection or ownership-reference clearing. Focused tests and
  race count30 passed. Full Linux unit/race each 834 passes, zero failures,
  seven skips; Windows 280 passes, zero failures, 13 skips; both vet clean.
  Actual pinned offline Go CLI fixture passed after these changes.
  Critical review required joining partial-constructor cleanup errors; all
  six failure paths now join c.Close(), with ipcParent.Close() also joined on
  IPC initialization failure. Post-correction package unit/race/vet passed.
  Final reviewer accepted safety, prototype integration and thesis alignment.
  Full-suite/actual-CLI results above precede the small constructor correction;
  they are not claimed as fresh full checks of that final correction.
- Commit-boundary audit found the remaining CLI cleanup is a coherent first
  integration checkpoint (the new RPC client is required). Review completed;
  178acdd includes the intentional ELF untracking without deleting local
  binaries and excludes the five preexisting doc deletions. Commit each increment.
- D09 preflight increment applied: exact source-stage channel/capability/asset
  evidence and descriptor binding helpers, Store-only bootstrap evidence, and
  refusal cleanup even for nil context. Closed/child-closed channels are
  rejected under the channel mutex; consumer view/mount fields remain unproved
  and cannot be prefilled. Production execution is still always refused after
  preflight; no process, bootstrap or readiness is released.
  Focused tests passed; an isolated red test against the committed baseline
  reproduced the nil-context owned-descriptor leak. Linux full unit/race each
  853 passes, zero failures, seven skips; vet clean; focused gate race count30
  passed. Critical reviewer accepted safety, preflight scope and thesis alignment
  without required fixes. These are preflight tests, not full launch proofs;
  Windows was not rerun for this Linux-only increment (last pass 280/13 skips).
- Consumer-proof binding increment applied from the launch-chain preparation:
  launcher PID and captured proof fields must match admitted Store/runtime
  evidence before consumer view/mount identity enters bootstrap. Internal
  launch helper routes mismatches into failed-launch settlement cleanup.
  Focused positive/negative binding contract tests passed, but are synthetic;
  they do not exercise authentic admission, proof capture, readiness, journaling,
  settlement or publication. Public production launch remains always refused.
  Linux full unit/race each 861 passes, zero failures, seven skips; vet clean;
  focused contract race count30 passed. Critical reviewer accepted the bounded
  gate under the required StartChild-validated Process.LaunchProof ordering.
  Template/namespace digest domains, PID provenance and source/runtime mount
  semantics were checked against the lower launcher. Full launch not accepted.
- Test-only StartChild/helper ordering increment applied. StaticHostAttestor
  and a simulated per-command launcher exercise real AdmitProduction/StartChild
  dispatch but not signed host admission or D09 entrypoint proof-pipe capture.
  Added image/template/PID rejection and Store audit/runtime mismatch cleanup
  cases with an empty-bootstrap-pipe assertion, plus more binding mismatches.
  Initial fixture failed the source-bootstrap precondition; corrected it by
  binding admitted Store evidence before the helper call. Focused tests and
  30 race-enabled repetitions passed; Linux full unit 874 passes, zero failures,
  seven skips; vet clean. Full race was not rerun for this test-only increment
  (the new cases ran under race count30). Reviewer held this test-only commit
  for bounded pipe reads, mock Wait ownership and fallback child cleanup.
  Repairs applied; pipe probe now uses a fresh pollable nonblocking duplicate
  after launch. An initial deadline attempt failed on a nonpollable descriptor.
  New UNKNOWN-settlement regression exposed a real early channel close in the
  StartChild error branch. Removed that close so the existing defer retains
  D09 owners on UNKNOWN and closes them on safe failures. Focused race count30
  now passes including the regression; full Linux unit/race each 877 passes,
  zero failures, seven skips; vet clean. All three Go source hashes match the
  isolated tested copy. Logs: d09-unknown-retention-{unit,race}.jsonl,
  d09-unknown-retention-race30.log and d09-unknown-retention-vet.log in the
  resume review-artifacts directory. Critical reviewer accepted this bounded
  increment: single Wait ownership, bounded probe, proof-before-bootstrap
  rejection and UNKNOWN channel retention. Committed as 11da49b; receipt
  d09-unknown-retention-receipt-11da49b.json binds tested source hashes/results.
  Separate production-session admission lease retention gap is being prepared
  before enabling launch. Public refusal unchanged. Active read-only task:
  `tbound-d09-admission-applied-critical-review-20261013-r3` (next repair), using
  the same delegated-task prefix as recorded above. Candidate review rejected a
  state-only typed launch-error check: complete STOPPED/exit/descendant evidence
  and nil CleanupErr are required. Actual BeginSession lease and binder owner
  quarantine tests now pass after the corrected three-file repair was applied.
  Focused race count10 passed; full Linux unit/race each 891 passes, zero
  failures, seven skips; vet clean. All three Go source hashes match the tested
  isolated copy; logs d09-admission-retention-* in resume review-artifacts.
  Critical reviewer accepted the bounded repair. Nonblocking limitation: the
  typed-error check observes the first PiWorkerLaunchError; current launch paths
  produce only one typed launch failure. Committed as 2b5673e; receipt
  d09-admission-retention-receipt-2b5673e.json records hashes/results. The session
  wrapper now reaches the binder's guarded quarantine on unsafe cleanup.
  Guest runbook audit
  also found >=8 bootstrap/positive-ABI probe inconsistencies against the exact
  ABI 8 profile. Production host probe explicitly compares observed ABI against
  the exact registered value (containment_linux.go); this is not merely a
  requested ruleset ABI. Prepared guest patch preserves broad Landlock
  availability and proposes a separate exact D09 row. Critical review
  found the proposed special exit 3 could mask unexpected UNVERIFIED rows.
  Guest candidate is applied for verification. Fixed parsed PowerShell rows to
  remain a mutable list for subsequent tool checks. Linux offline Python/shell
  regressions pass (five methods, one PowerShell skip); Windows PowerShell
  cases pass separately. Bash syntax and four PowerShell parser checks pass.
  No VM/provisioning commands ran. Review held the guest increment: Hyper-V
  did not stage the new shell helper and invocation errors mapped to FAIL.
  Helper staging/preflight and exit-2 classification are corrected; integration
  regressions now pass (Linux six methods, one PowerShell skip; required Windows
  PowerShell cases pass separately). Invalid/contradictory invocation results
  use exit 2; actual FAIL retains precedence. Applied review r7 held missing or
  duplicate gate rows that could falsely pass. Added exact row completeness and
  consistency checks plus shell/PowerShell regressions. Duplicate rows without
  a final newline initially exposed a shell read-loop omission; both loops now
  process the final line. Checks pass again; fresh critical review:
  r8 held unset shell variables under strict mode and Cosign UNVERIFIED being
  expected on live targets. Initialized parsed ABI variables; shell regressions
  now run with production set -eu; Cosign exception now applies only to File.
  Required Windows cases include live-versus-saved Cosign behavior. Tests,
  syntax/parser checks pass; r9 accepted code safety and thesis claims, held only
  an inaccurate README promise of an ISO recipe. Corrected that reference to
  artifact verification/qualification guidance; the recipe remains a separate
  increment. Guest gates committed as 22c7cfd; receipt
  guest-abi-gates-receipt-22c7cfd.json binds checked sources/results. No VM created.
  Input custody worksheet now distinguishes signed image metadata from caller
  checksum equality and frozen-image deployment from reproducible rebuild.
  Official Ubuntu artifact guidance excludes Azure VHDs on local Hyper-V;
  removed that unsupported example. Local guest media/recipe still needs review.
  Read-only supported local recipe preparation:
  tbound-local-hyperv-supported-guest-recipe-20261013-r1.
  User also requested a parallel evaluation of a simpler Windows-hosted image
  alongside Ubuntu. Read-only task tbound-linux-guest-options-evaluation-20261013-r1
  compares component-test convenience with D09 qualification requirements.
  Evaluation completed: keep existing WSL2 for component loops and prefer a
  minimal Ubuntu Server guest for qualification. Linux runtime is not inherently
  Ubuntu-specific, but current provisioning/offline verification require Noble;
  Debian/Alpine would add porting and remeasurement work rather than a shortcut.
  Local ISO/GoldenVhdx recipe candidate remains unapplied pending review
  tbound-local-guest-recipe-critical-review-20261013-r2 found unsupported
  Stop-VM -Shutdown, shutdown/hash ordering, assumed NIC/account identities and
  oversized installer seed. Corrections prepared by
  ISO seed recipe is now applied: installer-only 2-vCPU/4-GiB settings,
  authenticated ISO signing-keyring path, bounded graceful shutdown and disk
  parent/checkpoint checks before hashing. GoldenVhdx clone networking now uses
  hv_netvsc driver matching instead of eth0 and disables all installer YAML.
  No new helper deployment layer: patch stays in the existing injector.
  Script and two PowerShell documentation blocks parse cleanly. Offline Netplan
  regression prepared by tbound-hyperv-netplan-inline-regression-20261013-r1;
  fresh review tbound-local-guest-applied-critical-review-20261013-r4 pending.
  Netplan fixture now passes both cases using actual get/generate under a
  temporary root; no host networking applied. Initial fixture failures were
  corrected (extracted closing fi, root-dir CLI placement, quoted driver output).
  r4 accepted prior functional recipe fixes and held one stale README reference;
  corrected it. Fixture review r2 then found dangling Netplan symlink write
  redirection. Scoped fix now rejects symlinked etc/netplan parents and handles
  dangling file/backup symlinks explicitly before rename/write. Existing two
  cases plus dedicated private-fixture symlink regressions now pass: five methods
  using Netplan get/generate and actual extracted injector, no host network apply.
  Script and two PowerShell doc blocks parse cleanly; logs hyperv-netplan-offline.log.
  Final review tbound-netplan-symlink-applied-review-20261013-r4 ACCEPT across
  safety/integration/thesis boundaries; guest recipe committed as 282bb5e.
  Receipt supported-guest-recipe-receipt-282bb5e.json records source hashes and
  five offline Netplan passes; no booted guest qualification performed.
  The separate corrected D09 stdout/public wiring and single-reader positive
  lower-helper fixture candidate is now applied in the worktree for verification,
  not committed or host-qualified. Initial partial application tested only old
  cases; complete fixture now passes focused checks and race count30. Windows
  full unit 280 passes, zero failures, 13 skips; vet clean. Full Linux unit/race
  each 912 passes, zero failures, seven skips; vet clean. All nine Go source
  hashes match the isolated tested copy. Logs d09-positive-launch-* in resume
  review-artifacts. Applied review r8 ACCEPTED lower-helper integration and claim
  boundaries but HELD stdout ownership: cmd.Wait closes StdoutPipe before the
  RPC pump can drain a final buffered record. Task r9 returned requirements only,
  no patch. Concrete patch preparation split into tbound-owned-rpc-stdout-patch-20261013-r10
  (owned pipe plus exit-before-read regression) and tbound-rpc-drain-close-patch-20261013-r1
  (bounded pump-drain/close, errors and UNKNOWN retention). Current test passes
  precede that repair; they do not resolve the lifecycle race.
  Factory owned_stdout.go, fixture owned pipe, exit-before-RPC-read regression,
  and worker drain-before-close/retry/error tests are now applied. Focused unit
  checks pass; ten focused race repetitions running. The initial phase-two run
  still using StdoutPipe failed on an already-closed reader, confirming the
  ownership mismatch; switching the fixture to the owned pipe resolves it.
  Concrete launcher integration is incomplete: phase-one draft had stale hunks
  and an unreachable UNKNOWN reader. Complete exact command cleanup/retention
  patch prepared by tbound-owned-stdout-launcher-integration-20261013-r11.
  r11 patch now applied: pending per-command reader retained before proof,
  one bounded failed-start cleanup/wait path, measured scope settlement before
  reader close and UNKNOWN propagation into admission/quarantine guards.
  Focused checks pass. Full Linux unit/race/vet and focused ten-race repetitions,
  plus Windows unit/vet running; fresh applied review
  tbound-owned-stdout-applied-critical-review-20261013-r12 pending. No launch commit.
  r12 held remaining cleanup observation: workerClose predicate lacked observed
  exit/nil Session error (now corrected); failed-command Wait cached errors were
  discarded; cleanupFailedLaunch ignored pidfd-close errors and deleted ownership.
  Exact code/tests prepared by tbound-launch-cleanup-observation-repair-20261013-r13.
  Prior919-pass snapshot precedes these corrections; new checks required.
  Integrated owned-stdout snapshot now passes full Linux unit/race each919 tests,
  zero failures, seven skips; vet clean, focused race10 passes. Windows281 passes,
  zero failures, 13 skips; vet clean. All ten piruntime Go hashes match the tested
  copy. Fresh audit subprocess deadline change verified separately below.
  No launch commit until that lifecycle race is fixed and reverified.
  Public route retains
  concrete launcher/profile/source/proof checks; no production execution tested.
  User requested independent thesis plus standalone-product direction review:
  tbound-thesis-product-alignment-audit-20261013-r1 (actual thesis files included).
  Audit completed: internal normative thesis direction ON TRACK, completion AT
  RISK; standalone governed product not usable yet. Authority is the workspace
  tbound-thesis.md and governance-indexed contracts, not the institutional LaTeX
  template. No final manuscript or frozen external registration verified.
  Priorities: admitted positive/denied vertical path with durable evidence,
  clean ABI-8 qualification, then frozen corpus/calibration/oracle/analysis and
  registered H1 protocol. Product release additionally needs fresh-host install,
  recovery/upgrade/custody and authenticated artifacts. No TUI/browser expansion.
  Prepare consistency-only E07 eighth-point wording/stale status correction via
  tbound-e07-recovery-authority-alignment-20261013-r1; keep recovery denominator48.
  Follow-up authority audit found no resolution: the normative specification
  labels seven lifecycle scenarios but lists eight hooks. User clarification
  requested asynchronously; user then explicitly authorized autonomous evaluation
  and implementation of the best pre-freeze design, including missing hooks.
  Design tbound-recovery-protocol-design-20261013-r3 and independent invariant
  audit tbound-recovery-invariant-independent-audit-20261013-r1 are running.
  Evaluate distinct durable authority/effect states before choosing the mapping;
  preserve 48 unless a scientifically justified draft amendment needs otherwise.
  Design candidate keeps seven post-intent LC scenarios plus nine incidents,
  RECEIVED-gap mandatory pre-durability conformance outside48, and refines the
  EXECUTING hook to an independently observed effect before OUTCOME_DURABLE.
  E07 publication conformance remains separate and does not resolve the
  lifecycle-list count. Independent review instead recommends51=8 lifecycle+9
  incidents, and found atomic intent/decision/lease wording incompatible with
  intermediate persisted-state hooks. User authorized best-design choice; do
  not force48 or register noninjectable boundaries. Operational amendment
  tbound-recovery-operational-amendment-20261013-r4 will resolve actual persistence
  graph, denominator and an effect-before-outcome crash regression before adoption.
  Operational candidate r4 proposes51=8 lifecycle+9 incident cases, explicitly
  distinguishing journal commits from callback cutpoints and visible effect.
  Normative review tbound-recovery-amendment-critical-review-20261013-r5 checks
  that no internal-commit/authority gate is removed to fit incomplete code.
  Only the narrow audit marker-before-outcome test is applied so far, with a
  ten-second deadline for both crash/recovery subprocesses and fresh-process
  assertions against old and later effect replay. Ten race repetitions running.
  Protocol review r5 accepts51 as a proposed denominator but requires corrected
  Store authorization binding, Bash-only lease terminology, internal-commit/
  publication gates, read/checkpoint distinction and explicit sync-failure
  conformance. Normative patch preparation tbound-recovery-amendment-corrected-patch-20261013-r6.
  Audit regression deemed valid for process-crash/no-replay by r5; each helper
  now has its own ten-second deadline. Ten race repetitions, audit package unit
  and vet pass after that adjustment. Narrow test committed as ace9592; receipt
  recovery-marker-receipt-ace9592.json binds the tested source hash and scope.
  The separate clean thesis repo now uses codex/tbound-recovery-protocol for
  draft amendments; main content remains unchanged. Prototype branch remains
  codex/tbound-prototype. Corrected six-document draft now applied: proposed51,
  durable Store authorization cross-binding, Bash-only lease terminology,
  preserved internal-commit/publication gates, read/checkpoint distinction,
  mandatory pre-effect sync conformance and unimplemented end-to-end hook flags.
  Both repos pass diff checks; manifest section9 follows complete8. Final review
  r7 accepted proposed51 consistency but held two wording issues, now corrected:
  isolated post-operation delta gate is normative and not established by the
  existing final-publication gate; status describes historical/current proposals.
  Thesis draft committed as 2c7bf4d on codex/tbound-recovery-protocol. Prototype
  companion draft commit follows; no freeze or registration.
  External main-branch thesis repository remains untouched and unfrozen.
  Code gap identified: audit crash fixture currently exits before making any
  external effect; add an observable-effect-before-outcome regression and
  verify UNKNOWN/no replay before treating LC-05 as covered.
  No external registration/freeze or main-branch thesis edits are authorized by
  a draft design choice. Implementation work continues independently.
  Prior guest review r4 and launch/input-custody tasks failed at provider usage
  limit without results. Fresh read-only tasks use delegate prefix
  node:delegated-task:command%3Amcp%3A6561c5c6-604c-45e4-8da9-aacc905e367f%3Adelegate-task%3A
  with guest review tbound-guest-abi-complete-candidate-review-20261013-r5.
  Launch completion map confirms the existing guarded core can serve D09 after
  admission ownership repair; public D09 wiring/contract-doc patch preparation
  tbound-guarded-d09-complete-files-20261013-r3 must retain concrete launcher and
  signed profile/closure checks. ABI/profile registration and independent
  qualification remain environmental gates, not simulated test results.
  Additional guest input custody/reproducibility documentation preparation:
  tbound-guest-input-custody-repro-plan-20261013-r2 (publisher-authenticated base
  image and recorded resolver/frozen template distinctions; no host mutations).

- Baseline archive, initial working-tree patch and tracked-file SHA-256 manifest:
  ../review-artifacts/tbound-baseline-2026-10-12/. Includes the five document
  deletions preceding this work; preserve them.
- Removed optional explorer/governance viewer dependency bundle, fixture chat/
  sessioncontrol and unused legacy internal/podman. Actual OCI runtime and used
  internal/sandbox remain. Removed tracked closure output. ELF supervisor/tbound
  is untracked but preserved locally; never overwrite it or tbound.exe.
- README/setup/status consolidation and dated phase-document retirement landed.
  Evidence receipts/index remain immutable; history map resolves retired sources.
- Normal CI/test entrypoints and same-package orchestration splits landed.
  Official Pi CLI dev route is implemented; final3 Linux and Windows component
  checks pass (scoped results below).
- Publication settlement now requires STOPPED, process exit, descendant quiescence
  and no close error before evidence/publication; admission stays retained otherwise.
- Private SDK installer replaced by exact-version official CLI discovery. The
  profile scaffold and runtime bindings describe CLI RPC; this review follow-up
  aligns D09 and legacy argv guards with `PiWorkerCellArguments()`. The D09
  launch refusal remains, and production is unqualified; signing alone is not
  sufficient. The stager now binds installed dependency versions to exact
  shrinkwrap locations, records the lock digest from verified staged bytes, and
  materializes in-package file links as regular files; directory and escaping
  links remain refused. The production verifier still rejects symlinks. No
  profile freeze, registration, signing or headline trial.

## Available Linux environment and qualification gates

- Existing authorized Hyper-V VM TBound-Ubuntu-2404 is running and reachable via
  strict SSH as tboundadmin. Dynamic IP is not a product configuration.
- Go1.27.1 and Node24.21.0 available under /opt/tbound/toolchains.
  Official Pi CLI0.87.1 installed by npm --global --ignore-scripts in user .local
  prefix and version-checked. No global Pi/Node changes on Windows.
- Fresh repository probe: PASS13 / FAIL0 / UNVERIFIED2, exit2 (native SSH status).
  Kernel6.8.0-146-generic; LandlockABI4, below requiredABI8.
  cgroup.kill write/cleanup and UID-map helper behavior remain unverified.
- Existing development guest has prior boot/provisioning history, not a clean
  registered image. No boot/disk/kernel/account/systemd/trust/signing change in
  this batch. Noninteractive sudo unavailable to the SSH account.
- Signed CLI executable/extension closure and managed-service profile/admission,
  source exposure, D09 containment/settlement and independent effect observations
  require qualification. Registered corpus/calibration/analysis artifacts are
  absent from this repository. Do not freeze or execute H1 ahead of these gates.
- Preserved Final3 Linux records from before this review follow-up: source hash
  readback 306/306; Go unit and race each 781 passed, 0 failed, 7 skipped; vet
  and gofmt clean; npm ci and the required actual-CLI check exited 0; native and
  actual Go CLI offline fixtures passed. Windows supervisor go test
  -mod=readonly -count=1 ./... passed. These are prior component/offline results,
  not fresh full validation of this follow-up. Nine focused Python staging
  regressions passed in an isolated copy under local WSL. Linux-only Go argv
  tests and the full Linux suite were not run against this follow-up. Saved
  review logs include passing publication-recovery tests in one run and a
  separate run that stopped at the audit-journal guard because a temp ancestor
  was mode 0777; that run did not reach recovery. The logs support a test-temp
  setup failure, not a publication code defect, and do not establish a
  concurrent-state collision. Rerun Linux checks with a private 0700 TMPDIR
  under HOME and umask 077. Component/offline checks do not qualify production
  launch or containment; the actual-CLI check remains gated in CI.
- Local validation records: ../review-artifacts/tbound-cleanup-2026-10-12/.

## Verification and preservation rules

- Coordinator owns combined review/final state; disjoint worker ownership.
  No blanket staging, reset/discard or unrelated edits.
- Before acceptance, central read-only review must return separate code-safety,
  integration-readiness and thesis-alignment verdicts; reverify fixes.
- Relevant gates: full Linux tests/race (private0700 ext4 TMPDIR, umask077), vet/
  gofmt, Windows build/tests, intended adapter checks, installation checks and
  source-hash readback. Report skipped/unrun checks explicitly.
- Explicit temporary -o build paths; never overwrite preserved local binaries.
  Preserve immutable receipts/index bytes and pinned Git source identities.
- Never access vm_start.txt or inspect/log/copy provider key material.
  No real provider use in this batch; prior $1 authorization is not qualification.
- User now requested incremental commits; commit coherent verified scopes after
  critical review, preserving unrelated changes. Keep failures and unresolved
  gates visible; component passes do not imply readiness.
