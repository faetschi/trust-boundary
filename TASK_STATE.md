# TBound task state

Updated 2026-10-13. This file records the current checkpoint. Historical
implementation detail is in Git history and immutable receipts under
../review-artifacts/tbound-resume-2026-10-13/.

## Scope and claim boundary

- Implementation branch: codex/tbound-prototype. Latest launch checkpoint:
  47a6d73. Commit coherent, reviewed and verified increments; no blanket staging.
- CLI-first, sequential read/write/edit/bash using official Pi CLI 0.87.1 and
  the mediation extension. No unmediated fallback or expanded MVP tool surface.
- G1=false; containment and production qualification are not established.
  H1 is unexecuted. Component, simulated-launch and offline-CLI checks are
  not containment, registered trial or power-loss results.
- Public D09 launch is wired through concrete launcher, signed admission,
  exact source/runtime bindings and per-launch proofs. A qualified successful
  public launch and real provider exchange through it remain unexecuted.
- Development Hyper-V guest has Landlock ABI4; D09 requires exactly ABI8.
  WSL component environment has ABI1. Neither is a clean registered ABI8 host.
  No VM was created, provisioned, registered or qualified in this work.
- Thesis recovery draft 2c7bf4d is isolated on codex/tbound-recovery-protocol
  in ../MSE_MA_Thesis; thesis main remains unchanged. Companion prototype
  decision b2be8ec proposes51 trials (eight lifecycle + nine incidents, x3).
  This proposal is not frozen or registered; E07 conformance stays separate.

## Completed increments

| Commit | Implemented scope |
|---|---|
| a281699 | Windows ACL/SID bounds |
| 178acdd | Official CLI RPC integration and worker lifecycle checkpoint |
| 385982d | D09 preflight source/capability/channel/asset bindings |
| 91440d5 | Consumer proof binds actual PID, source and runtime before bootstrap |
| 11da49b | UNKNOWN StartChild resource retention and simulated proof ordering |
| 2b5673e | Preworker lease and binder quarantine retention |
| 22c7cfd | Exact guest ABI gates and cross-layer offline regressions |
| 282bb5e | Supported minimal ISO guest recipe and safe clone Netplan handling |
| ace9592 | Visible-effect crash and fresh-process quarantine/no-replay regression |
| 47a6d73 | Guarded public D09 CLI launch and single-owner drained RPC stdout |
| 3d30fcf | Trusted audit hooks and four independent native crash fixtures |

RPC failures require command=prompt and an exact active request ID. Descriptor
and stdin close errors remain observable; UNKNOWN settlement retains admission
and cleanup owners and blocks publication. Proofs precede bootstrap/readiness;
the production session journals the launch proof before sending the prompt.

Guest setup is preparation only. Minimal Ubuntu Server is the supported local
recipe; existing WSL remains suitable for component tests. Azure VHDs are not
the supported local Hyper-V source. Media/input custody and exact ABI checks
must precede image registration.

## Current recovery increment

- Audit has optional trusted-host hooks after intent sync and after callback
  return, before outcome append. Normal Open uses nil; provider RPC/profile
  data does not configure hooks. Hooks run under the journal writer lock.
- Four native crash fixtures distinguish intent durability, actual callback
  body entry, visible effect inside the callback, and callback return.
  Independent marker files plus a fresh recovery process verify unresolved
  intent and no replay. Callback-entry is fixture evidence, not a journal hook.
- Observer error/panic withholds the result and leaves unresolved intent;
  after-return failure preserves the callback cause. Intent sync failure
  invokes neither observer nor effect. Prior inside-effect crash coverage remains.
- Critical reviewer accepted corrected safety, integration and thesis boundaries.
  LC04/05/06 remain NOT IMPLEMENTED end to end; component hooks do not complete
  the provider/Store/checkpoint recovery harness or qualify filesystem durability.
- Corrected source: Linux full unit/race each935 passes, zero failures, seven
  skips across25 packages; focused race x3 gives36 passing test events; vet clean.
  Windows287 passes, zero failures,13 skips across15 packages; vet clean.
  Both audit Go files hash-match the tested WSL copy. Logs:
  recovery-hooks-corrected-{unit,race,focused}.jsonl and Windows/vet counterparts.
  Earlier preliminary recovery-hook logs are retained and superseded.
  Recovery increment committed as3d30fcf; receipt
  recovery-hooks-receipt-3d30fcf.json binds both source hashes and final logs.
- Prior committed launch receipt guarded-d09-launch-receipt-47a6d73.json records
  Linux925 unit/race passes,7 skips; Windows281 passes,13 skips; vet clean.

## Current installer increment

- Extracted updates require executable tbound, tbound-doctor and tbound-profile
  before moving aside an existing prefix. Missing tools must preserve all three
  installed binaries and the install marker.
- Offline regression builds a release, uses unique private fixtures with valid
  per-file and outer checksums, and rejects missing doctor/profile updates.
  Both failures reproduced before their fixes; corrected smoke passes10 checks,
  including native fixture, unqualified-production refusal, doctor and uninstall.
  Shell syntax passed; both shell sources match the isolated WSL test copy.
  Critical reviewer accepted safety, integration, product and thesis boundaries.
- Linux-only component installation test; no real prefix, Darwin/Windows installer,
  VM registration, provider exchange or production qualification.

## Remaining milestones

Fresh independent trajectory review: thesis/product direction remains on track;
current gate preparation risks design churn. Narrow the next increment to the
actual Store ALLOW/DENY mutation/reopen path. Defer API-only proposals and broad
provider-summary changes until required by that working slice.

Uncommitted WIP: scope fields/helpers, composite command argument digest and
strict v2 operation intents are applied in the three sessionrepo core files.
Linux sessionrepo compile-only check passes; no behavioral acceptance claim.
Admission journal/gate and caller/test migrations remain pending. The recorded
935/287 checks above validate3d30fcf, not this incomplete working tree.
Two focused scope/digest/canonical-intent tests pass with race detection.
Fresh implementation-only delegated task
tbound-postdelta-store-vertical-20261013-r1 prepares the actual Store gate;
prior reviewer-only gate proposals were not applied. Known remaining callers:
scope reinjection in Open/replay, host-authority composite digest, and separate
D09 execution-spec/composite-operation digest checks.

Resumed concrete dependency work: scope is now restored in Open/Reconstruct,
nil Bash scope is rejected before creating a view, and Store fixture authorities
check declared scopes. Admission record/policy and tests are applied (not yet
wired into mutation callbacks); coordinator fixed empty policy commitment,
exact primary-intent linkage and a placeholder test before verification.
Linux sessionrepo unit/race each66 passing test events, zero failures/skips;
vet clean. Logs delta-admission-record-{unit,race}.jsonl and -vet.txt.
These package checks do not validate the whole product WIP.
Pending independent admission-record review: /root/scope_fixture_patch.
Review found missing derivable Bash argument/context digest checks and equal
generation labels; coordinator applied fixes and trust-boundary comments.
Existing focused admission race tests pass after these fixes; new discriminating
Bash/same-generation rejection tests are being prepared before re-review.
Those tests are now applied and pass; independent record review ACCEPT.
Corrected Linux sessionrepo unit/race each70 passing events, zero failures/skips;
vet clean. Logs delta-admission-record-corrected-{unit,race}.jsonl and -vet.txt
supersede the earlier66-event record snapshot. Record API remains unwired.
Pending mutation/journal wiring task:
node:delegated-task:command%3Amcp%3A63518ea0-a69e-4478-b9e8-30d481ded9ac%3Adelegate-task%3Atbound-postdelta-store-wire-mutations-20261013-r3.
That task returned a mutation/lifetime patch, not applied: missing imports/tests,
redundant second denial record and inconsistent denial labeling require repair.
Active correction task:
node:delegated-task:command%3Amcp%3A63518ea0-a69e-4478-b9e8-30d481ded9ac%3Adelegate-task%3Atbound-postdelta-mutation-repair-20261013-r4.
Remove redundant denial append: successful primary effect payload already binds
the decision reference. Use denied_after_effect for both known executed paths.
Mutation repair is now applied as WIP, with coordinator-corrected wrong-file
hunks and unused test import. Store owns the private admission journal and both
mutation callbacks append a decision before generation promotion. Primary
payloads carry exact references; known DENY leaves tip unchanged and returns
ErrDeltaDenied/denied_after_effect. Actual Bash outside-scope DENY test passes
with race detection (one effect, full retained candidate, no generation promotion).
Full package diagnostic now FAILS expected Open/bundle/revert checks because
their readers still expect pre-admission payloads; no accepted full-suite claim.
Log delta-admission-mutation-pre-recovery-unit.jsonl retains that run.
Active Open recovery task r5 and bundle/replay task r6:
node:delegated-task:command%3Amcp%3A63518ea0-a69e-4478-b9e8-30d481ded9ac%3Adelegate-task%3Atbound-postdelta-open-recovery-20261013-r5
node:delegated-task:command%3Amcp%3A63518ea0-a69e-4478-b9e8-30d481ded9ac%3Adelegate-task%3Atbound-postdelta-bundle-replay-20261013-r6.
Independent mutation review /root/scope_fixture_patch is pending; whole gate
commit waits for linked recovery/replay plus host/D09 integration and checks.
Open r5 patch applied with coordinator corrections: output snapshot variable
scope, generation-ID declaration and stale function/context anchors. Open now
verifies decision refs against primary intents/candidate/input, revalidates the
candidate chain, recovers known DENY without accepting its generation, and
rejects duplicate/unreferenced admission records. DENY reopen/orphan and missing
journal checks pass with race detection. Actual Store package unit now has only
TestSessionRepositoryEvidenceBundleReconstructE2E failing (bundle migration pending);
other lifecycle/reopen/revert checks pass. Logs delta-admission-open-focused-race.jsonl
and delta-admission-open-pre-bundle-unit.jsonl retain this incomplete snapshot.
Mutation review found ignored denied-stage cleanup; coordinator now checks
close/remove/sync and quarantines cleanup uncertainty via unresolved primary
outcome. Caller receives no Bash command output on DENY; audit retains it.
Re-review and additional reference-tamper tests /root/scope_fixture_patch pending.
Bundle r6 subagent edited four source/test files and ran tests despite its
read-only brief. Changes remain uncommitted; coordinator reran package checks
and requested independent review. Unit/race78 events/vet passed before follow-up
fixes. Root reproduced bugs: exported OperationEvidence admission ref was ignored
by reconstruction comparison and shallow-copied from Store state. Red regressions
reproduced changed/missing reference acceptance and pointer aliasing. Root fixed
reference nil/value comparison and deep copy; focused race green. Corrected
package unit/race/vet running; independent review pending. No full-product claim.
Pre-host whole Linux suite remains failing17 tests in command host/executor/
sessionlaunch fixture scope/digest integration; Store itself passes. Preserve
delta-admission-pre-host-unit.jsonl as an incomplete-state diagnostic.
Fresh READ-ONLY patch-author tasks:
node:delegated-task:command%3Amcp%3A63518ea0-a69e-4478-b9e8-30d481ded9ac%3Adelegate-task%3Atbound-host-scope-integration-20261013-r7
node:delegated-task:command%3Amcp%3A63518ea0-a69e-4478-b9e8-30d481ded9ac%3Adelegate-task%3Atbound-d09-digest-domains-20261013-r8.
They must return patches without edits/tests. Host nil scope disables Bash only;
protected preproof stays distinct from postdelta policy, and D09 execution-only
receipt digest stays distinct from scope-inclusive operation digest.
Corrected bundle Store unit/race/vet have passed; counts and logs recorded under
delta-admission-bundle-corrected-*. No scope/admission commit until independent
review plus remaining whole-product host/D09/caller migration checks pass.
Host-scope patch from /root/host_scope_binding held for correction: nil scope
must disable Bash only, not constructor/service creation or other tools.
Corrected host-scope patch is returned, not yet applied. Its nil/empty behavior
requires real caller/config tests before accepting changes to the governed path.
No scope/admission increment committed; replay/cross-journal gate, D09 digest
bridge and full caller integration still required before acceptance.

1. Implement separate post-operation G(internal_commit) on verified resulting
   deltas before generation acceptance. Current transition validation binds
   pre-operation approval; final-publication InternalCommitGate is a distinct
   boundary. Bash lacks declared output scope. A bounded scope enforcement
   patch is being prepared; fresh durable delta decisions/recovery are missing.
2. Complete trusted end-to-end lifecycle cutpoints, cross-journal recovery and
   independent oracles, including known-unexecuted retirement. Preserve no replay
   and explicit UNKNOWN quarantine; do not claim all eight cuts implemented.
3. Qualify clean registered ABI8 host, signed CLI closure/profile, managed-service
   and source admission, D09 containment/settlement and publication.
4. Freeze corpus/calibration/oracle matrix/analysis before registering or running H1.
5. Verify standalone fresh installation, recovery, upgrades, custody and release.

## Verification and preservation

- Read-only Luna max preparation/review; coordinator applies, tests and commits.
  Require separate safety, integration and thesis verdicts on increments.
- Isolated Linux copy: /home/jeli2k/tbound-resume-20261013.kRuoVd/source.
  Go1.27.1: /home/jeli2k/tbound-go1.27.1.zkiVXZ/bin.
  Use umask077 and private mode0700 TMPDIR=/home/jeli2k/tb.n2XBZV on WSL ext4.
  Long temporary paths exceed AF_UNIX limits. Preserve logs and source receipts.
- Preserve local supervisor/tbound and supervisor/tbound.exe; use explicit
  temporary build output paths. Never access vm_start.txt or inspect/copy/log
  provider/admin credentials or real signing keys. No real provider/headline run.
- Exclude these pre-existing deletions from increments:
  docs/native-pi-product-blockers-2026-10-07.md,
  docs/next-evaluation-unlocks-2026-10-07.md,
  docs/tbound-readiness-status-2026-10-08.md,
  docs/thesis-gap-ledger-2026-10-05.md, docs/verifier-blockers-2026-10-05.md.

## Resume: product doctor ABI diagnostic (2026-10-13)

- Applied live read-only Linux Landlock ABI diagnostic to tbound-doctor, exact
  D09 ABI 8, with observed mismatch/remediation. Non-Linux build stays supported.
  It remains advisory; governed_ready stays false. Independent Luna max scoped
  safety/integration/thesis review: accepted, no blockers.
- Windows doctor unit tests and vet passed. Linux doctor race tests, vet and
  isolated build passed. Live WSL report: ABI 1 warning, governed_ready=false,
  exit 1 (unavailable local prerequisites); this is not host qualification.
  Four tested doctor source hashes match the working tree.
- Store DENY replay regression reproduced acceptance of forged next generation
  and transition order. Shared validation now runs before DENY return; Store
  package race and vet passed. Independent re-review is pending.
- T3 host/D09 patch tasks r7/r8 were interrupted by the user after repeated
  approval prompts; they returned no patches. Replaced by native READ-ONLY
  Luna max host_scope_resume and digest_bridge_resume; parent owns edits/tests.
  Current scope/admission gate remains uncommitted until whole-product checks.