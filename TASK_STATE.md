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
