# Production Pi session composition (`serve --pi`)

Status (2026-10-24): **G1, including managed-root publication, passes on the
real guest at F12. Each new freeze needs fresh qualification.** On Linux,
the ordinary CLI command is a managed-service client;
the internal --service endpoint is the systemd socket-activated server. This
document describes the service-side session composition, not a standalone
user launch command. See [current status](../evaluation/status.md) for the
remaining final-profile and evaluation gates.

**Worker migration:** the Pi worker API names in this implementation record
refer to the former embedded SDK composition. The selected design is the official Pi 0.87.1 CLI in RPC mode with a TBound
extension. The public D09 entrypoint revalidates admission, source/runtime
bindings, and Store source evidence before calling the guarded launch helper.
The lower-helper integration exercises bootstrap, readiness, one correlated RPC
failure, and settled close with a simulated launcher. Those component tests do
not establish public launch success, G1, signed host admission, authentic proof
capture, containment, or host qualification. H1 remains unexecuted.
See the [CLI reproduction decision](../decisions/2026-10-12-cli-reproduction.md).

## CLI entry point

The client sends a registered task ID, request ID, and task text to the local
service:

~~~
tbound serve --pi --task-id ID --task-request-id ID --task TEXT
~~~

The client cannot select profile, source, or workspace paths. The systemd
service owns those inputs and is expected to start through socket activation.

## Concrete binder (`production_binder_linux.go`)

1. Registered D09 template + protected signed profile, template digest bound to
   `ContainmentProfileDigest`; fixed `/etc/tbound/managed-live-root.json` and
   `.ed25519` registration verified by Runtime A against the fixed profile key,
   with the registration digest bound by the template.
2. Durable session: fresh per-run mode-0700 Store/private-admin directories below
   protected parents, run-unique protected Store/provider/command-evidence journals,
   `NewSessionOperationAuthority`,
   `CreateStore` (`sessionrepo.LinuxReadOnlyBinder`), `Seed`, `BindBaseline`,
   `OpenAdminParent`, pinned Git, `sessionlaunch.Prepare` (D06 private-Git),
   `BindPreparedSession`.
3. Sealed exposure/assets: `OpenSealedWorkerExposure`, `OpenVerifiedRuntimeAssets`.
4. Managed live-root authority: C verifies the actual candidate descriptor
   against A's opaque `VerifiedD09ManagedLiveRoot`, checks the registered digest
   and measured root identity, acquires a monotonic run epoch under the shared
   lifecycle-journal serialization lock, and retains an opaque lease before
   `RecoverBeforeAdmission` or task admission. The fixed registration binds the
   dedicated non-login service UID/GID, exact root/mount/filesystem and protected
   service/mount/runner configuration. The current service cgroup device/inode/
   mount ID is measured per invocation and pinned only for this authority epoch;
   it is not a static signed inode claim.
5. Launcher/cgroup: unique child cgroup under the registered delegated root,
   `NewLinuxCgroupScope`, `NewD09PodmanCrunLauncher`, `BindManagedLiveRoot`, then
   `BindCgroupScope`.
6. Worker channels/bindings via A's
   `piruntime.BindProductionPiWorkerBindingsWithSource(ctx, profile, source,
   sourceCapability, assets, launcher, settler)`. The binder checks the fixed
   official CLI argv and in-cell environment digests before exporting
   descriptors. Admission validates the profile and runtime assets. The binder
   does not establish upstream publisher custody.
7. Protected publication roots: live root, publication state root, shared
   lifecycle journal (distinct from the store/provider journals).
8. A run-unique command journal contains one versioned record binding the live
   D09 receipt snapshot to the actual Store `OperationEvidence`, provider call,
   full approved transition/delta, and run/session/workflow/conversation identity.

The service composition uses the public `LaunchD09PiCLIWorker` route. The D09
launcher captures proof for the exact process; `StartChild` transfers that
command's stdout reader and wait ownership to its `Process`. The worker binds
the proof to the admitted source/runtime evidence before writing bootstrap and
accepting readiness. The service must durably bind the exact proof before task
admission. Launch failures retain D09 owners when typed settlement is
incomplete, cleanup failed, or an UNKNOWN branch remains unresolved. A complete
STOPPED settlement requires observed process exit and settled descendants.

The positive lower-helper integration uses a static test attestor, simulated
per-command proof, and a bounded native protocol child. Its conversation is a
static admission stub. It verifies actual bootstrap/event/RPC channels and
single `Process` wait ownership; it does not verify authentic entrypoint proof,
provider admission, public launch success, signed host admission, or containment.
UNKNOWN cleanup retains the managed live-root lease, lifecycle journal lock,
and session/cgroup handles until process exit/recovery.

## Bash command runner

`productionCommandRunner` binds one fresh Runtime A
`piruntime.D09CommandRunner` to
each authenticated lease/view. It consumes only the writable disposable
`CommandView` descriptor. Immediately after that exact `inner.Run` succeeds, the
wrapper calls `TakeCommandReceipt(view, spec, result, settlement)`, requires the
live private issuance seal through `Validate()`/`Digest()`, and checks the exact
view descriptor device/inode against the captured proof and registered
image/template before retaining the opaque receipt. The proof's mount ID is the
cell's own consumer mount and is never equated with the host view descriptor's
mount ID. Each refusal names its failed check. It returns no
result on any receipt or cleanup failure.

Only after `sessionrepo.RunBash` has durably committed the effect result,
transition, generation, and complete `OperationEvidence` does `DurableExecutor`
bind the actual lease, captured provider call, input generation/tree, output
generation/tree, result digest, delta, and D09 snapshot in one
`production_d09_command_settled/v2` event. The event is appended and re-read,
strictly decoded, sequence/hash checked, and compared to both the retained live
receipt and Store audit bundle before the provider result is released. Publication
repeats that validation against the exact `OperationEvidence`, approved
transition, and provider-journal call origin. A JSON snapshot/known hash without
the retained live receipt is not authority.

The authority additionally sets `requireEvidencedBashContainment`: generic
`sessionrepo.OperationEvidence.CommandContainmentStatus` remains the neutral
`not-established` marker; it is never treated as proof. It passes only when the
concrete D09 runner retained for that exact lease/view has returned a valid live
receipt and the durable command record matches the exact run/session/workflow/
conversation/profile, provider call, effect/lease/view, spec/result/settlement,
input tree, output delta, and journal sequence/hash. Runtime A validates the
captured final TCB proof, actual inspect/wait/output drain, container removal,
pidfd exit, cgroup populated=0/removal, and mount-namespace release before issuing
the receipt. The legacy rootless Podman profile is never renamed or promoted to
satisfy this.

## Publication finalization (MED2)

`productionPiSession.buildPublicationRuntime` constructs the real
`NewPublicationRuntime(durable, workflow.FinalizerOptions{...})` from the
protected live/state roots, the shared lifecycle journal, `workflow.NewRootLedger`,
the bound `Store`/`Baseline`/`CurrentTip`, the verified workspace, and a concrete
`productionTrust`. `BeginSession` runs `RecoverBeforeAdmission`, opens the shared
live-root publication owner, activates workflow admission, and holds a session
lease until both governed loops and the worker are joined. It is assigned before
the task is released.

`CheckAdmission` revalidates the held registered-root lease after recovery and
re-verifies D06 source/store provenance. `AttestQuiescent` requires worker
STOPPED settlement, joined loops, zero pending command lease/receipt, exact
launch-proof sequence/hash/run/profile binding, unchanged held root identity, and
the registered single-service writer-domain measurement before issuing a one-use
permit bound to run/epoch/baseline/tip. `CheckPublicationPrerequisites` validates
every Bash receipt against the actual Store operation, approved delta, and
authenticated provider call, then consumes that permit before `RootLedger.Begin`.
The lease remains held through per-object publication and the durable RootLedger
terminal record. Only a known `SUCCEEDED`/`FAILED` terminal record or verified
no-publication path permits release. The shared journal lock serializes
cooperating TBound sessions; it is not treated as proof against arbitrary
writers. The registered non-login service identity and protected unit/mount/
account policy are the explicit host TCB boundary, and `--workspace` cannot
select or grant a root.

Publication outcome is mandatory and truthful in the receipt:
`publication_status` + `publication_transaction_id`; `success` is true only for a
verified `STOPPED` settlement **and** `publication_status == SUCCEEDED`.
Unavailable/failed/unknown publication is reported explicitly and never silently
receipted as ready.

## Authenticated terminal lease origin

`workflow.TerminalOriginResolver` resolves the **terminal Bash call itself** by
matching its exact lease ID and input/output generation/tree to the synced Go
decision/provider result journals; it never substitutes the preceding edit.
`publication` stores a versioned `TerminalLeaseOriginBinding` containing the full
terminal transition digest, lease operation, and trusted proposal/call identity.
That binding is persisted in the prepare/token/commit records, included in the
commit digest through a derived origin identifier, and checked again by recovery.
Legacy v1 proposal-call journals keep their original digest/decoder path. The
component regression `TestPublicationRuntimePublishesVersionedAuthenticatedTerminalLeaseOrigin` (fixture Trust, read->write->edit->bash) publishes with the Bash proposal identity. Production publication of a bash-last chain is not yet evidenced.

## Legacy SDK-worker TCB proof (not the current CLI dev path)

The legacy SDK-worker implementation retrieves
`PiSDKWorker.D09LaunchProof()` after the launcher captured and validated
the entrypoint's proof pipe on `D09ProofChildFD` (fd10), then appends a
run/profile/image/template-bound `production_tcb_proof` event to the durable Store
journal **before** constructing/admitting the task prompt or releasing control
fd0. Missing/foreign proof or append failure closes the worker and refuses the
task. Publication re-reads and checks the exact event sequence/hash and run
binding. This is compiled/unit tested evidence, not an external host proof: the
tagged D09 test still needs a provisioned host.

## Receipt

`productionPiReceipt` (schema `tbound-production-pi-receipt/v1`) records bounded,
non-secret facts: run id, mode, profile id/hash, containment profile/digest,
frozen source digest, provider profile, worker-ready/task-admitted/turn-completed,
proposal count, settlement state/exit/descendants, command runner profile and
command-cell settlement count, TCB proof refs (when available), publication
status/transaction, and `success`.

## Trusted operator inputs (protected defaults)

| Input | Default |
| --- | --- |
| Store parent | `/var/lib/tbound/sessions` (fresh mode-0700 per-run child) |
| Audit root | `/var/lib/tbound/audit` |
| Private-admin parent | `/var/lib/tbound/private-admin` (fresh mode-0700 per-run child) |
| Live root | Fixed signed managed-root registration; `--workspace` is only a candidate descriptor and cannot grant scope |
| Managed-root registration | `/etc/tbound/managed-live-root.json` + `/etc/tbound/managed-live-root.ed25519`, fixed-anchor verified |
| Publication state root | `/var/lib/tbound/publication-state` |
| Delegated cgroup root | registered D09 `cgroups_path` |
| Git approval | `/etc/tbound/pi-git-approval.json` (root-owned `{path,digest,boundary}`) |
| Runtime bundle root | signed profile `PiRuntimeBundleRoot` |
| Source snapshot | Required `--source` |

A missing protected artifact is a precise refusal, never a synthesized success.
The registered root itself is owned by its dedicated service UID/GID with exact
mode `0700`; its direct parent is root-owned and not group/world writable. The
non-login service account must not be shared with interactive or adjacent
processes. The registration signs this policy and protected systemd/mount/runner
configuration; Runtime A verifies the current process and per-invocation
cgroup-v2 identity. This is a host TCB precondition, not something C infers from
mode bits or a process scan.
Session workflow/conversation/publication IDs and journal names are unique per
run; the Store and private-admin parents are exact mode-0700 directories owned
by the launching UID/GID, and each run creates fresh private children. The source
root must be root-owned, non-writable, reached component-by-component without
following symlinks, and on a read-only filesystem; a writable checkout plus
`RootAttestation{Quiescent:true}` is not accepted as a production seed.

## Tests

* From `supervisor/`, `go test ./internal/piruntime`: exercises the lower-helper
  bootstrap/readiness/RPC/settled-close integration with a simulated launcher,
  plus the negative public simulated-launcher refusal and retention cases. It
  does not qualify public production launch.
* `go test ./cmd/tbound`: production refuses on an unprovisioned host without
  starting a worker; self-signed profile cannot root-admit; dev fixtures stay
  mutually exclusive; the production runner refuses legacy/unregistered Bash;
  this command-package test does not run the `piruntime` package tests;
  invalid/foreign/missing launch proof, running worker, pending command, unknown
  settlement, missing containment evidence, and unverified live-root quiescence
  all fail closed; the E05 authority resolves and the versioned publication
  binding preserves the actual terminal Bash call rather than an earlier edit.
* `//go:build linux && d09integration` + `TBOUND_D09_INTEGRATION=1`:
  `TestRealGovernedServeRoute` skips **only** when the env opt-in is absent; once
  set it must execute actual Pi `read->edit->bash->read` and assert tool,
  generation, per-command live receipt, worker STOPPED, and successful managed-root
  publication receipts. A positive run requires an operator-provisioned host; it
  was not performed in this environment.

## Release blockers vs operator prerequisites

**Code/API status:** the managed-root registration verifier and one-use
`D09CommandReceipt` API are wired in C. There is no unrestricted live-root path or
caller boolean for quiescence. Production publication still requires the fixed
registration/runtime checks, run-epoch drain, exact command receipts, unchanged
root identity, and durable terminal lifecycle. Xattr visibility uses A's
registered template measurements and `piruntime.VerifyD09XattrVisibility` against
the actual source descriptor (filesystem magic, mount ID/options, read-only state,
root ownership, scanner UID/GID); no fixed digest or caller `Complete:true` is
used.

Runtime A's one-use command-view reservation and launcher `Close(ctx)` lifecycle
are integrated. C calls `TakeCommandReceipt` once immediately after a successful
run, and cleanup calls launcher `Close` only after worker/command settlement and
successful C-owned `scope.Remove`; uncertain cleanup retains both authorities.

**Operator prerequisites for a new deployment:** provision the
existing fixed, root-owned admin public key at `/etc/tbound/pi-host-profile.pub`
and the signed production profile/template whose
`managed_live_root_registration_digest` matches the fixed-anchor registration;
the protected service and mount units; dedicated non-login service account;
registered managed live filesystem; pinned runner and D09 image/toolchain; and
protected Git approval. The component tests above do not perform signing,
host provisioning, or containment qualification. The F12 G1 result does not
qualify a new deployment, and H1 remains unexecuted.
