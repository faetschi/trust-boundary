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
- Next read-only Luna max increment adds StartChild-through-launch-helper proof
  rejection/capture and cleanup integration tests, including image/PID/source/
  runtime mismatches before bootstrap/readiness. Public refusal remains until
  the complete chain is implemented, reviewed and tested. Automatic task:
  `node:delegated-task:command%3Amcp%3A33dd25f5-74bf-4617-850e-3c26a7d50cf8%3Adelegate-task%3Atbound-d09-startchild-integration-20261013-r4`.

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
