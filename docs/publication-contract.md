# Linux publication contract — prototype slice 2

**Status:** implementation contract for `supervisor/internal/publication`; not
evidence that the end-to-end thesis profile is admitted or compliant.

## Scope and authority

This package publishes the exact net delta produced by the ordered approved
generation-transition chain. It is a logical, recoverable **per-object**
transaction, not a globally atomic filesystem transaction. The caller must pass
already-open live-root and sealed-generation descriptors, the complete
`delta.ChainSpec` and transition list, a trusted transition-decision verifier,
and a trusted internal-commit authorizer. The package recomputes the chain and
complete source/live manifests; caller-supplied digests alone do not establish
filesystem identity.

The implementation follows process-model §§3.1 and 5.4, recovery §§2, 4, 6.6,
and evidence contract E07:

1. `Acquire` takes an exclusive, nonblocking Linux `flock` for the workflow and
   persists a strictly increasing `owner_epoch`. `Publish` and `Recover` also
   take an identity-keyed flock file, opened descriptor-relatively in the
   trusted live-root parent. Its key is the live-root device/inode identity; the
   lock descriptor remains held until `Repository.Close`, not merely until the
   method returns. Different workflows and state roots therefore cannot mutate
   the same live-root inode concurrently while either repository owner remains
   open. Participating host writers must use these locks; non-cooperating
   writers remain outside the guarantee.
2. `Publish` verifies the complete baseline, sealed generation, ordered ledger,
   transition policy receipts, and composed baseline-to-final delta. The
   approved-delta validator, rather than a set-union of changes, supplies the
   exact net changes (including edit/revert composition).
3. Every changed source object is opened descriptor-relatively with Linux
   `openat2(RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS |
   RESOLVE_NO_MAGICLINKS)`. Open descriptors are checked with `fstat` for file
   type, owner, mode, link count and size; file bytes are rehashed against the
   sealed manifest. Those validated source descriptors are retained through
   staging and apply; staging never reopens a source pathname. Staging rechecks
   descriptor identity and complete content digest before accepting the copy.
   Unsupported links, special files, metadata, path forms, mounts or limits fail
   closed.
4. After complete baseline validation, replacements and retained originals are
   staged under a random, private transaction directory in the live root's
   parent. The parent and live root must be on the same verified mount. Each
   file and directory entry is synchronized before its preparation checkpoint.
5. The authorizer receives a binding over workflow, owner epoch, fresh
   publication effect identity, originating proposal, ordered chain digest,
   composed-delta digest, baseline/sealed digests and policy/metadata-policy
   digests. The opaque single-use commit token and policy decision are durably
   recorded before the first live mutation. The token is consumed even if no
   object mutation follows.
6. Each target parent is opened beneath the retained live-root descriptor,
   without following links or magic links. Parent identity and object preimage
   are checked before mutation. Create/overwrite uses a same-filesystem staged
   rename; delete first retains a verified original copy, then unlinks the
   target; directory creation uses `mkdirat`. Affected parent directories (and
   the staging directory after rename) are synchronized before the per-object
   terminal checkpoint.
7. A complete post-apply scan must reproduce the sealed tree digest before the
   transaction is `SUCCEEDED`. Per-object state, source/staged/original
   identities, parent identity, operation index, durability boundary and
   terminal classification are retained in the hash-chained audit journal.

`OriginProposalID` is accepted only if it exactly matches the `ProposalID` of an
approved `OperationProposalCall` transition in the validated chain. This is the
bounded origin-membership rule implemented here; no independent registered
session-origin or lease-origin verifier API is available in this slice. A
lease-only chain, or an origin not present among its approved proposal calls,
fails closed before preparation or authorization. Mixed chains are admissible
only when the supplied origin also appears as an approved proposal call.

The journal is durable and access-controlled within the TCB, not tamper-proof.
Successful `fsync` calls are not asserted as an environment-independent power
loss guarantee.

## Operations and composition

Supported effective operations are regular-file **create, overwrite, delete**,
paired regular-file delete/create interpreted as a **rename**, and **directory
creation**. A rename is materialized from the sealed destination object and
removes the old path separately; it does not promise preservation of the old
inode. Regular-file replacement is staged and renamed over the target while a
verified copy of the old bytes remains retained. Explicit directory removal or
directory/type replacement is rejected. The implementation does not infer
rename intent from names alone: it pairs net-removed/net-created regular files
only when their complete object states are equal; content-equal duplicates are
paired deterministically in UTF-8 path order.

Net-reverted paths produce no live operation. Parent directories are applied
shallow-first; every published regular-file object follows the validated
composed delta. A maximum of 1,024 net changed objects and 6 MiB per publication
recovery record are enforced in addition to `delta` and `workspace` limits.
There is no weaker fallback when a bound is exceeded.

## Token and recovery rules

`publication_token_consumed` records the fresh opaque token ID, authorizing
decision ID, exact binding and binding digest. `publication_prepare`,
`publication_checkpoint`, `publication_terminal`, and
`publication_aborted` form the durable per-object recovery record. Each event
is appended and synchronized before its result is relied upon. Decision IDs and
publication effect identities cannot be reused within the journal history.

After interruption, a later owner must acquire a larger epoch and call
`Recover`. Recovery observes the live object, retained original, staged identity,
parent identity and latest durable checkpoint. It does not invoke the authorizer
again, apply remaining objects, replay a consumed token, restore originals, or
roll back. The consumed token stays consumed even when inspection establishes
that an object was untouched. Exact baseline state without an apply checkpoint
is `FAILED`; exact final state with a durable apply checkpoint is `SUCCEEDED`;
an operation whose visibility or durability cannot be established is `UNKNOWN`.
Any `UNKNOWN` blocks further publication for the workflow. Known partial apply
is retained and reported per object; a fresh effect requires a fresh correlated
proposal, authority, baseline and remaining workflow budget.

`RestoreDisposableCheckpoint` and `RecoverDisposableCheckpoint` provide the
separate checkpoint-restoration path. The caller supplies an already-open,
supervisor-owned retained checkpoint descriptor and its complete manifest,
digest, and checkpoint ID. Publication re-verifies that source and materializes
it into a newly generated private directory under `StateRoot`; it accepts no
destination pathname and never writes into the live tree. The result is a
disposable independent materialization, not a live restore and not publication
authority. Recovery rechecks the recorded source directory identity and exact
manifest, then either admits a complete materialization or removes an incomplete
owned disposable tree descriptor-relatively. An identity or ownership mismatch
stays `UNKNOWN`. Checkpoint retention/authorization and any later promotion or
freshly authorized live effect remain caller/integration responsibilities.

Fault injection points exposed by `FaultInjector` are `after-source-validation`,
`after-prepare-checkpoint`, `after-artifact-create`,
`after-artifact-parent-sync`, `after-stage-write`, `after-stage-sync`,
`after-backup-write`, `after-backup-sync`, `after-stage-checkpoint`,
`after-token-checkpoint`, `after-parent-checkpoint`,
`after-object-start-checkpoint`, `after-rename`, `after-unlink`, `after-mkdir`,
`after-parent-sync`, `after-object-checkpoint`, `after-checkpoint`, and
`after-terminal-checkpoint`. Disposable checkpoint restoration separately
exposes `after-restore-intent-checkpoint`,
`after-restore-directory-checkpoint`, and `after-checkpoint-materialization`.
The focused suite injects each named boundary, including pre-token cleanup and
terminal handling. A fault before token consumption can discard only private
preparation artifacts. A fault after token consumption is never retried
in-process; recovery classifies from recorded progress and filesystem
observation. If the journal cannot persist a checkpoint, or the observation is
ambiguous, the outcome stays `UNKNOWN`.

## Assumptions and explicit limits

- The current scanner profile requires private supervisor-owned roots with
  normalized ownership/modes and a complete trusted xattr-visibility
  attestation. This prototype does not publish arbitrary developer workspaces
  with broader root permissions.
- Claim-bearing synthetic targets are exclusively supervisor-owned during
  apply. For a developer workspace, the `flock` is cooperative: participating
  writers must honor a quiescent apply window. Baseline checks detect prior
  conflicts but do not guarantee safety against a privileged or non-cooperating
  actor that can replace parents or targets during application.
- A kernel `flock` is released when its owning process exits. Different
  workflow journals are not a shared cross-workflow recovery ledger; the host
  lifecycle must not admit a new workflow against a live root after an owner
  crash until the interrupted workflow has been reconciled. This package's
  root lock serializes live owners but cannot discover another workflow's
  unresolved journal by itself.
- `openat2`, descriptor-relative resolution and retained descriptors prevent
  pathname/symlink re-resolution escapes within the declared Linux boundary;
  they do not make separate filesystem operations globally atomic.
- There is no private Git repository initialization or verification in this
  slice. Process-model §6.1 requires an independently initialized private Git
  repository and forbids live `.git`/object-store reachability. Its absence is
  the explicit D06 implementation waiver/divergence, **not compliance** with
  §6.1 or D06. This package neither creates Git metadata nor grants access to
  live Git administration; repository initialization and an isolation proof
  remain outstanding integration work.
- The implementation owns only the publication package and this document. It
  is not wired into `cmd/tbound`; the host integration must supply the trusted
  gate/verifier, workspace attestations, session origin chain, workflow budget
  reservation, publication API exposure and lifecycle recovery. No webview or
  provider path is an authority source.
- The package suite includes a seeded `sessionrepo` integration fixture using
  its public `Create`, `Seed`, `Write`, `Verify`, `Evidence`, and generation-FD
  APIs, then publishes that verified ledger tip with a test authorization
  fixture. This proves package data flow only; it does not claim CLI or
  production-gate wiring.

## Independent verification — 2026-10-05

Coordinator snapshot `/home/jeli2k/tbound-coordinator-s2.9tscTx` contains
committed `1bedc0ec350edcbb2b7e3df4d877de50f5308c66` supervisor source plus
only `supervisor/internal/publication`, not concurrent explorer edits.
`findmnt` confirmed `/dev/sdc ext4`; private TMPDIR mode was 0700.
`go test -race -count=1 ./...` exited 0, including publication (3.614s).
Gofmt output was empty. Race-log SHA-256:
`bd250500d16fcf674ca0471a5c932dd9b0086c607f55193207e143ffcdac227d`.
Pre-test source-hash listing SHA-256:
`2a876c907dc827891c84d0d50de9edf164f35c3004b1135dc52d2cd6d7c954c3`.

An exact copy of that snapshot on Windows passed `go build ./...` and
`go test -count=1 ./internal/... ./e2e` (both exit 0). Publication is Linux-only;
its Windows `[no test files]` result is **not** verification of the Linux
implementation. Go module files were unchanged; adapter untouched; scoped diff
check exited 0. No current-source offline guest result or production integration
is inferred from these development checks. Commit identity/byte comparison is
recorded separately in `TASK_STATE.md` after commitment.

Final combined-source rerun uses committed explorer `b827199` plus publication
only in `/home/jeli2k/tbound-coordinator-s2final.m6uvfA`. Full race suite passed
(publication 3.576s), gofmt empty, `/dev/sdc ext4`, TMPDIR 0700; Windows exact-copy
build/internal/e2e passed (all exit 0). Final race-log SHA-256:
`c8b4abaffafcb3f19e3fe0585a5665b3c936201c1bc9d54676fcc369ef9d4e69`;
pre-test source-hash listing SHA-256:
`709ed664b1d0b44b4015d71c12b34656dcece90917a51786921982f35710d96d`.

## Conformance evidence scope

Focused Linux tests use disposable fixtures under a private ext4 `TMPDIR` and
demonstrate: create/overwrite/delete/rename-composition/directory creation;
baseline conflicts; symlink and hard-link rejection; held-source-descriptor
behavior under pathname replacement; origin membership rejection; exact
binding and token replay rejection; workflow locking, cross-workflow live-root
locking and owner-epoch fencing; every exposed preparation/write/sync/parent/
object/terminal fault hook; per-object `SUCCEEDED`/`FAILED`/`UNKNOWN`; mixed
partial outcomes with inode and audit-effect counts unchanged by recovery;
tampered-journal rejection and live-state mismatch quarantine; no automatic
replay after restart; no rollback over later known effects; and unchanged live
state before token authorization. A seeded public-API `sessionrepo` fixture
verifies and publishes actual store evidence. These tests are conformance
evidence only. They do not establish the separate private-Git invariant,
end-to-end CLI/supervisor admission, power-loss semantics, or arbitrary-host-
writer race safety.

Normative references: `02-process-model.md` §§3.1, 5.4 and 6;
`02-1-recovery-and-continuation.md` §§2, 4, 6.6 and 8; and
`11-evidence-contract.md` E07. The thesis's recoverable per-object publication
contract governs here; old all-or-nothing/idempotent language is not an
implementation promise.
