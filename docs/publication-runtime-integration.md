# Trusted publication lifecycle integration

## Scope and status

This increment adds a presentation-independent workflow finalizer, a private
`workspace_commit` gate, a durable live-root lifecycle ledger, and a TBound
composition API around the existing `DurableExecutor`. The Linux path is
implemented under `supervisor/internal/workflow`,
`supervisor/internal/gate/internal_commit_linux.go`, and
`supervisor/cmd/tbound/publication_runtime_linux.go`.

The composition API is not a Pi replacement or a chat client. The product
direction remains the actual Pi Harness running through TBound (for example,
the eventual `tbound serve --pi` host route). The internal finalizer does not
depend on a TUI, web UI, Pi SDK, provider UI, or model-visible commit tool. A
host composition root supplies the already-registered broker, authenticated
IPC `Supervisor`, durable session, live-root descriptor, and trusted runtime
attestations. This increment does **not** enable a production CLI launch path:
the current executable remains fail-closed because no trusted production
closure/profile/source/offline-verifier/settlement configuration is registered.

## Runtime lifecycle

`NewPublicationRuntime` accepts an existing `DurableExecutor` and a
`workflow.FinalizerOptions` value. It pins the finalizer to that executor's
actual `sessionrepo.Store` and synchronized current-tip accessor; a caller
cannot substitute a separate generation ledger. `PublicationRuntime.Serve`
then:

1. reconciles every pending shared live-root lifecycle record before admission;
2. opens the current workflow publication repository only after reconciliation;
3. requires the trusted host admission check and a durable decision recorder;
4. wraps the registered broker so each accepted captured generation must equal
   the current `DurableExecutor` tip;
5. tracks each proposal from broker correlation through the durable executor,
   releasing the admission lease on denial, error, or completion; and
6. installs the existing `DurableExecutor` as the only effect executor.

`PublicationRuntime.Run` closes proposal admission and waits for admitted
effects before calling the finalizer. A late proposal cannot reach the executor
or receive an allow result from the closed runtime. Hosts may call `Serve` and
`Finalize` separately when their runner owns the session boundary.

The finalizer calls the trusted runtime's quiescence attestor; then calls
`Store.Verify`, `Store.Evidence`, and `Store.EvidenceBundle`; requires those
views to agree on the baseline, complete ordered transition ledger, and current
tip; authenticates every transition and the final proposal-origin receipt;
checks the trusted publication-readiness policy; obtains and scans baseline and
tip generation descriptors; registers the live-root pending record; and invokes
the existing descriptor-relative publication API. The verified generation
descriptors are retained for the full publication call. Their public
`ReadOnlyMountSource` handles are O_PATH descriptors and are reopened relative
to those descriptors for scanning; they are never resolved from a caller path.

`OriginProposalID` comes from the final approved proposal-call transition. The
trusted origin verifier receives the reconstructable evidence bundle, which
contains the session journal bytes, and must authenticate its exact durable
provider/gate receipt and binding to that transition. A lease-only terminal
transition, missing verifier, or unmatched receipt fails closed. Each earlier
transition is independently re-verified before publication. There is no
browser/model boolean or caller-supplied origin string authority.

## Internal workspace-commit authority

`gate.InternalCommitGate` is deliberately separate from `gate.Policy` and its
registered `read`, `write`, `edit`, and `bash` surface. It is not model-visible.
It validates the exact publication binding digest, requires a nonnil trusted
binding verifier, rejects reused publication/binding identities, and durably
appends `workspace_commit_authorized` before it returns an authorization to the
publication package. The publication package then durably records and consumes
its own exact one-use token before any live mutation. Both records must use the
same lifecycle audit journal. Gate decisions and tokens are not cross-database
transactions with filesystem effects; interruption is reconciled from durable
records and live per-object state, never by replay.

`Trust` is a trusted-host composition interface, not an authorization source in
itself. Implementations must provide all of `CheckAdmission`,
`CheckPublicationPrerequisites`, `AttestQuiescent`, `VerifyTransition`, and
`VerifyOrigin`. Missing dependencies reject construction; callback refusal
withholds publication. The production readiness callback must verify the
registered Pi/tool closure, actual containment/runtime profile, immutable
source/offline-verifier provenance, command settlement and host filesystem
profile. A test callback that returns nil does not establish any of those
claims.

## Crash and cross-workflow quarantine

`RootLedger` writes `workflow_publication_pending`, transaction, and resolved
records into the same hash-chained lifecycle journal. The pending record pins
the workflow owner epoch and workspace profile. A shared in-process active-root
reservation is installed atomically with that durable marker and held through
publication and terminal classification. Another ledger instance cannot
reconcile or begin work on that root while the publisher is still running. The
reservation is released on every return path; `UNKNOWN` keeps its durable
pending quarantine. After process death the in-memory reservation is absent,
and the journal's exclusive OS flock prevents a replacement owner from
recovering until the previous journal owner is gone. The journal must be one
trusted, supervisor-owned shared ledger for **all** workflows that can address
the registered live-root inode.
Before new proposal admission, pending work is resolved as follows:

- A pending root marker with no `publication_prepare` is safe to close as
  `FAILED`, because the publication API cannot perform a live effect before its
  durable prepare.
- A matching terminal `SUCCEEDED` or `FAILED` record resolves the root marker.
- A prepared nonterminal transaction is passed to
`publication.Repository.Recover` through the trusted workflow-repository
opener only after its read-only repository binding matches the recorded
workflow, a strictly newer owner epoch, the shared journal, and the exact
recorded workspace profile. Recovery observes and classifies; it does not
  reissue authority, replay remaining operations, or roll back over later
  effects.
- `UNKNOWN`, journal damage, identity mismatch, missing repository opener, or
  failed recovery leaves the shared root quarantined and blocks other
  workflows.

The small `publication.Repository.Binding()` read-only accessor was added for
these checks; it exposes only workflow ID, owner epoch, journal identity, and
workspace options. The current-workflow opener must return a nonnil repository whose binding
matches the finalizer's exact workflow, owner epoch, journal, and workspace
profile. A recovery opener must return a nonnil repository for the recorded
workflow on that same journal and exact recorded workspace profile, with an
owner epoch strictly newer than the pending record. The opener must recover the
workflow's original private state root. The live-root descriptor must still
identify the recorded inode. If the host cannot provide the shared journal,
original workflow repository state, or identity-matched live descriptor after
a crash, it must refuse admission; this API does not guess or discover them.
The publication package's own live-root flock is process-lifetime coordination,
not a durable cross-workflow registry; the shared lifecycle journal supplies
that missing barrier only when the host actually shares it.

## Limits and evidence

### Independent coordinator acceptance — 2026-10-06

The first draft passed its full Linux race suite but was withheld after review
identified active-owner recovery, repository/journal binding and concurrent
finalization gaps. The corrected snapshot
`/home/jeli2k/tbound-coordinator-publicationcorrected.N85jr8` passed the full
supervisor `go test -race -count=1 ./...` suite on private ext4 with TMPDIR mode
0700, plus focused `go vet` and empty gofmt output. Windows exact-copy
`go build ./...` and internal/e2e tests passed. Linux-only publication code is
not exercised by Windows tests. Modules/adapter dependency files are unchanged;
the adapter was not touched. No provider network tests were opted into.

The four new runtime regression groups (active owner before/after prepare,
concurrent Close with UNKNOWN, finalization draining a blocked durable command
and refusing late proposals, and current repository binding mismatch) also
passed ten repetitions under the race detector. Nil/typed-nil and mismatched
recovery repository cases are included in the full suite. These fixtures remain
synthetic; this is component integration acceptance, not production admission.

One initial coordinator vet invocation failed at shell parsing because inherited
PATH contained spaces/parentheses; no Go check ran in that invocation. A private
script with quoted PATH corrected it and vet exited 0.

Retained scratch evidence SHA-256:

- `race.log`: `5a6a679d96fa68ad59021f6805a23393819c908dbe13f67c64afec099af29872`
- `source-hashes.txt`: `c07016d1feb12bb7ed14cbc9b0fe21f537f5b18990daa37c57e96511ed470276`
- `repeated-regressions.log`: `5eac6d620ac8e0c6dc6fe5b584575cbac772df8eaf5b5c8518b613a38c39b8fd`

Post-commit exact-source comparison is recorded in `TASK_STATE.md`.

The published unit is a logical per-object transaction, not a globally atomic
filesystem transaction. Cooperative writers must honor the live-root lock and
quiescent apply window. The supported object slice and type/directory-removal
limits remain those in `docs/publication-contract.md`. This runtime increment
does not add private Git initialization, access, or verification. The missing
private-Git invariant is the documented **D06 waiver/divergence, not
compliance** with process-model §6.1 or D06.

The focused integration fixture uses the actual `protocol.Stream` correlation,
IPC `Supervisor`, `DurableExecutor`, and public `sessionrepo` APIs. It carries a
read, write, edit, synthetic descriptor-bounded Bash runner, and a final write
through the session chain, verifies the real evidence bundle, and publishes the
resulting tip. Its Bash runner never launches a host process; its settlement is
synthetic test data and must not be described as containment. The provider
capture and host `Trust` implementation are fixtures on a private disposable
filesystem. They establish API data flow and fail-closed behavior only, not a
real provider exchange, Pi Harness closure, container/cgroup profile, offline
verification, or production authorization.

The package tests cover stale captured generation refusal, missing trusted
readiness refusal, denied origin before prepare/authorization, a pre-token
crash reconciled without effects, and a post-rename `UNKNOWN` crash that stays
quarantined across workflow identities without replay or rollback. Existing
publication-package tests remain the evidence for the complete individual
filesystem fault matrix. These are focused conformance tests, not the E07
registered recovery study or a production-profile admission result. E07 still
requires per-object source/stage/original identities, every supported durability
boundary, externally observed outcomes and independent effect counts; recovery
§8 remains a separate registered study.

The implementation is bounded by process-model §5.4 and single-use authority
§3.1, recovery §§4 and 6.6/8, and evidence contract E07. It does not claim
cross-filesystem atomicity, power-loss qualification, or safety against
non-cooperating/privileged host writers.
