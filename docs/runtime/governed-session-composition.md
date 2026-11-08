# Governed session and D06 composition

**Status (2026-10-24):** historical embedded-SDK composition detail, still cited
by code. It records an earlier increment; G1=false statements below describe that
time. G1 later passed at F12. See [OVERVIEW](../../OVERVIEW.md).

**Worker migration:** this document retains implementation details from the
former embedded Pi SDK worker. The selected worker is the official Pi 0.87.1
CLI in RPC mode with the TBound extension. The offline CLI/RPC fixture and
isolated extension have passed development checks; SDK worker names and test
receipts below do not qualify production.
See the [CLI reproduction decision](../decisions/2026-10-12-cli-reproduction.md)
and [OVERVIEW](../../OVERVIEW.md).

## Status and claim boundary

This increment adds a concrete sessionrepo→D06 source-binding→private-Git
composition and a provider-audit-backed callback set for the existing
`DurableExecutor`. It is **not** a claim-bearing TBound release, production
Pi/Node launch, real provider exchange, D09/D10/D11 host profile, G1 publication, or
thesis acceptance. The current Linux test injects a synthetic audit prefix and
uses the measured `dev-wsl-non-claim-bearing` command sandbox. It does not use
credentials or contact OpenRouter.

Production `tbound serve --pi` must remain refusal-only until the D09 path proves
both trusted source staging and the actual read-only consumer mount before the
Pi CLI starts. A host O_PATH descriptor is not itself a read-only view. The CLI must
never pass `privategit.Repository.Root`, `AdminParent.root`, the Store root or
parent, or a live-root descriptor to Pi. The D09 source capability below is
Go/TCB-only staging material; the entrypoint must relocate FD 4 into the private
container namespace, verify its consumer mount, and replace/close the raw source
descriptor before Node exec.

## Concrete D06 APIs and evidence format

### Store and source identity

`cmd/tbound.SessionOperationAuthority.CreateStore` constructs the
`sessionlaunch.ManagedStore` using that authority's concrete journal-backed
callbacks. `BindBaseline` refuses a Store not created through the authority,
requires a single seeded `g0`, compares `Store.Verify()` with
`Store.EvidenceBundle()`, checks policy and metadata commitments, and retains
the exact `Store` and `Generation` pointers.

The sessionrepo Store journal and providerbridge Conversation journal are
separate protected `audit.Journal` instances. `sessionrepo.Options.Journal` is
dedicated to Store effects/recovery; raw provider request/response audit is not
mixed into its replay stream. Before the Store begins any effect,
`AuthorizeOperation` appends and syncs one
`governed_operation_authorized` Store-journal event containing the exact
`EffectID`, `DecisionID`, operation identity, normalized typed-argument digest,
input generation/tree, gate-decision receipt, and the provider journal's gate
record sequence/hash. A Store effect intent must follow this authorization
record. This cross-journal link is rechecked against the final Store operation
evidence and the provider journal during origin verification.

`sessionlaunch.Prepare(ctx, managed, generation, adminParent, pinnedGit,
gitProfile)` performs this sequence before it can return a prepared session:

1. Reverify the sessionrepo chain/bundle, exact protected journal bytes,
   generation manifest/tree, metadata policy, xattr profile, and held
   generation descriptor `(device,inode)`.
2. Derive a domain-separated `sha256:` binding from the canonical retained
   journal bytes/head, generation ID/tree digest, store policy and metadata
   policy, held descriptor identity, xattr profile digest, and bounded scan
   limits. The digest is derived from verified evidence, not accepted from the
   caller.
3. Append and sync `sessionlaunch_source_binding` in the same session audit
   journal **before** filtered copy. Its canonical payload contains the D06
   binding digest and source manifest, but no source pathname.
4. Call existing `privategit.Create` against the same retained directory
   descriptor. Its trusted `SourceBindingVerifier` checks the held descriptor,
   generation, policies, xattr profile, and derived binding before import,
   after import, and immediately before return. Existing reserved-Git filtering
   rejects `.git` components before copying/opening them.
5. Verify the newly initialized private Git repository, then append and sync
   `sessionlaunch_private_git_ready`, including privategit's transfer/filter,
   source manifest/tree, actual pinned Git digest/execution-boundary label,
   private worktree/admin inode identities, limits, and provenance digest.
   Verify the journal prefix before returning.

The core data structures are `sessionlaunch.SourceProfile`,
`sessionlaunch.GitProfile`, `sessionlaunch.Provenance`, `ManagedStore`,
`AdminParent`, and `PreparedSession`. Store options are not synthesized from a
success boolean: the production-shaped authority supplies the exact
`AuthorizeOperation`, `VerifyDecision`, and `VerifySettlement` methods. Their
availability is still not an OS/host attestation; the production CLI has no
trusted profile loader and remains refused.

### Worker and command-view descriptors

`PreparedSession.ExposeReadOnly` re-verifies the store, retained D06 records,
private Git boundary, and selected generation before creating an existing
`sessionrepo.ExposedView`. Its `WorkerView.Descriptor` returns only a CLOEXEC
O_PATH descriptor for that read-only selected generation. It never returns the
session Store root, generations/admin directory, `privategit.Repository.Root`,
or a live project descriptor. The descriptor itself is not a proof that a
consumer mounted or observed it; the launcher must keep its own descriptor-role
map and settle/close every inherited copy.

For Bash, `sessionrepo.RunBash` already creates a disposable ordinary-byte-copy
command view from the current sealed generation. The concrete
`sessionlaunch.DevelopmentCommandRunner` takes only that `CommandView`, checks a
one-use session-authority lease, and invokes the existing `sandbox.Launch`
preflight/settlement path with a fixed minimal command environment. It retains
the returned `sandbox.Settlement` in a one-use receipt consumed by
`SessionOperationAuthority.VerifySettlement`; caller-constructed settlement
booleans are not accepted. The runner always labels itself
`dev-wsl-non-claim-bearing` and refuses unless its current probe establishes the
mechanisms required by `sandbox.Launch`. It does **not** establish the frozen
Podman/crun, signed-image, delegated-cgroup, host-profile, D09/D10/D11 or G1
contract. In this WSL inventory, a successful sandbox test is development
evidence only.

`SessionOperationAuthority` derives Store decisions from the exact
providerbridge audit records (`provider_exchange`, `provider_gate_decision`,
`provider_tool_result`) and validates call/response IDs, issuers, raw argument
digest, gate digest/verdict, typed Store argument digest, input generation/tree,
operation identity, view/lease context, effect ID, strict durable result schema,
and transition generation/tree/effect/sequence. It appends no replacement
provider protocol and does not read provider credentials. The configured
`providerbridge.NewExecutor` still wraps the real `DurableExecutor`, preserving
the exact result bytes and updating canonical provider history only after its
journal append succeeds.

The offline `--dev-fixture` now launches the official Pi CLI/RPC worker and
TBound extension against a scripted provider. It completed four mediated tool
turns and five provider turns on the existing VM, but reports
`provider_exchange=false`, `settlement=UNKNOWN`,
`containment=not-established`, and `claim_bearing=false`; it does not qualify
the production CLI profile or launcher. The separate `--native-fixture` is
Go-only and does not start Pi; see [launch](../guides/setup.md).

`SessionOperationAuthority.AdmitPrompt` verifies D06/store identity and that
the current `DurableExecutor.Tip()` matches the verified latest Store generation
before calling `Conversation.AdmitPrompt`. The caller must forward that same
prompt to Pi only after this call succeeds. `NewProviderConversation` uses the
existing secure Go credential loader and approved provider profile; merely
constructing the function does not load credentials in this increment.

## E05 chain and publication boundary

The synthetic-provider E05 regression runs the actual Go `DurableExecutor` and
session repository over:

```text
read(g0) -> edit(g1) -> Bash(g2) -> read(g2)
```

It verifies a synthetic but exact provider capture/decision/result audit shape,
actual Store operation identities and output generations, private Git source
binding, the disposable Bash view, and durable `g0/g1/g2` evidence. It also
verifies that the Bash lease maps back to the exact provider proposal using the
durable response/call/sequence/digest records—no caller-supplied origin string
is used. The provider audit is synthetic, so it is not evidence of provider
authenticity or actual Pi operation.

**Terminal Bash lease origin (component-level, non-claim-bearing):** `workflow.Finalizer.Finalize` authenticates a terminal `delta.OperationLease` through the optional `TerminalOriginResolver` and persists a `TerminalLeaseOriginBinding`. The synthetic read->edit->bash chain publishes through a fixture `Trust`. This is not production publication evidence.

**Historical plan (implemented in 87e969a; kept for history):** before changing the workflow contract, authorize a narrow follow-up in
`supervisor/internal/workflow/publication_linux.go` and its tests: add an
optional lease-origin resolver that derives a typed proposal-origin receipt
from the exact session evidence bundle's `OperationEvidence.DecisionID` and the
providerbridge's synced result/correlation record; validate the lease ID,
response/call issuers and IDs, sequence, argument digest, generations, effect
ID, transition ID and transition sequence; and pass the resolved proposal ID to
publication. Preserve current proposal-call behavior and fail closed when a
lease resolver is absent or mismatched. Add compatibility tests for existing
proposal-call finalization plus Bash-last resolution, stale/replayed/mismatched
lease receipts and missing audit records. Do not add a model-visible commit or
Git tool, a synthetic trailing write, or a caller-controlled proposal ID.

Separately, a production `workflow.Trust` must still prove registered host
profile/admission, source/origin receipts, actual quiescence, transition
decisions, and recovery. Nil/no-op callbacks and the WSL development sandbox
cannot satisfy that contract. No publication result or G1 claim is produced by
this component test.

## Remaining vertical-slice contracts

- The pre-migration SDK launcher published `NewPiWorkerChannels`, `LaunchPiSDKWorker`,
  `Admission`, `PID`, `NextEvent`, `SendAdmittedPrompt`, and `Close`. The generic
  `VerifiedWorkerExposure` still means a pre-established consumer read-only view;
  B's `VerifiedD09ExposureSource` is deliberately a separate source-staging API
  and must not satisfy that generic interface. In D09 the trusted rootless
  Podman/crun setup consumes the one exported source FD at FD 4, while the
  entrypoint verifies the actual consumer mount and closes/replaces the source
  FD before Node. The remaining FD map is FD 0 control, FD 3 held Node ELF, FD 5
  bootstrap, FD 6 TBound IPC, FD 7 provider bridge, FD 8 lifecycle events, FD 9
  runtime bundle, and FD 10 TCB proof. No Store/admin/private-Git/live-root FD is
  passed to Pi.
- C's current production binder creates a fresh Store and calls `Seed` under
  managed-root writer authority; it must not configure `LinuxReadOnlyBinder` in
  the non-mount-capable service. After `Prepare` persists/verifies D06 and
  private Git, C calls `OpenVerifiedD09ExposureSource` for the exact current
  sealed tip and binds it through A's distinct D09-source channel API. Source
  issuance is current-tip-only, but an already-issued source can remain valid as
  an immutable retained generation while the Store advances, after checking its
  captured durable audit prefix. Channel-owned exported FD closes first; D09
  settlement/scope removal follows; then the source capability and PreparedSession
  close. UNKNOWN retains the entire cleanup stack. This is interface/code work,
  not a provisioned consumer-mount proof.
- The current providerbridge tests intentionally use a synthetic Doer; this
  increment makes no real provider call and does not load credentials. Operator
  approval and the secure loader are separate prerequisites.
- A previous compatibility snapshot reports that the embedded Pi SDK
  `AgentSession` ran with Linux Node 24.15 and pre-existing package assets.
  This historical result does not bind the official CLI or extension to an
  admitted profile.
- The official CLI path must not fall back to the Go-only fixture, an
  unrestricted stock Pi tool surface, Windows Node, or direct host execution.

## Verification

Linux checks used a private mode-0700 ext4 copy of the committed
`8d2e478` supervisor foundation, the existing providerbridge component files,
and only the new sessionlaunch/governed-session overlays. The active, uncommitted
piruntime launcher worker file was excluded because it was in progress and is
owned by the separate launcher agent; no source file was edited to make the
snapshot compile. `TMPDIR` was also a separate mode-0700 ext4 directory. The
measured sandbox test skips with its precise missing-mechanism reason if the
current environment cannot establish its development profile; it never falls
back to direct execution.

```text
go test -count=1 ./internal/sessionlaunch ./internal/providerbridge ./cmd/tbound
go vet ./internal/sessionlaunch ./internal/providerbridge ./cmd/tbound
CGO_ENABLED=1 go test -race -count=1 ./internal/sessionlaunch ./internal/providerbridge ./cmd/tbound
```

All three passed on that isolated Linux overlay. A Linux binary built only to
the private temp path preserved current CLI behavior:

```text
tbound serve --pi                         -> exit 2 (missing attested profile)
tbound serve --pi --native-host-profile … -> exit 2 (untrusted path refused)
tbound serve --pi --native-fixture        -> exit 0 (Go-only fixture, claim_bearing=false)
```

The fixture receipt reported `provider_exchange=false` and
`containment=not-established`. The E05 test passed actual `sessionrepo` and
`DurableExecutor` transitions over `g0 → g1 → g2` and proved the Bash lease's
proposal origin from synthetic synced providerbridge-shaped records, then
published through the fixture finalizer with the authenticated Bash-call origin. It does **not** prove Pi,
real provider capture, production isolation, publication, G1, H1, or thesis
acceptance.

The passing private-ext4 verification snapshot used commit `8d2e478`, the
current providerbridge component directory, and the explicit new sessionlaunch
and governed-session overlays. In-progress native launcher/profile files were
excluded rather than edited or adopted. A separate build attempt against the
full live worktree currently stops in the uncommitted launcher-owned
`supervisor/internal/piruntime/worker_channels_linux.go` at lines 238 and 585:
`undefined: DescriptorWorkingDirectory`. That file is outside this increment's
ownership and was not modified here. The current Windows `go test ./cmd/tbound`
also stops in launcher-owned `internal/piruntime/child.go` at lines 237 and 359
because `HostRuntimeBindings.WorkingDirectoryHandle` is undefined. These are
uncommitted launcher API integration errors, not changes made by this slice.
Therefore the passing results below prove the owned composition against the
published base/providerbridge APIs, not a passing full-current-worktree
launcher build.

The current minimum external integration blockers are: (1) the launcher owner
must split the worker-visible sealed/read-only source view from private Git
administration and publish the matching typed descriptor binding. The current
FD 4/CWD-to-private-Git-root identity requirement is unsafe for D06; no such
root is passed by this package; (2) the admitted host profile must authenticate
executable, argv/cwd/environment, terminal identity,
mount/process scope and shutdown settlement; the current runner is only
`dev-wsl-non-claim-bearing`; (3) native host-control must call
`SessionOperationAuthority.AdmitPrompt` before forwarding the same user prompt
to actual Pi; (4) the broker owner must supply the real Go-owned provider turn
loop without exposing credentials/history to Node; and (5) publication needs
an actual registered `workflow.Trust` implementation. The managed-service source-capture
code path now exists, but remains unqualified on Linux with a provisioned
service. No positive governed run has occurred, so production admission is not
established and `G1=false` remains the status.

## Two-phase D09 source and consumer capabilities — worker B

The production service runs in an initial user namespace without mount
authority. It must not configure `sessionrepo.LinuxReadOnlyBinder` or call
`Generation.Expose` on the host. `Prepare` still creates the filtered sealed
Store generation and fresh private-Git/admin snapshot. The D09 path then has two
distinct trust phases:

1. **Trusted Go source authorization.** sessionrepo issues an opaque capability
   for the exact retained, current Store tip. sessionlaunch returns it only after
   checking the exact Store chain/bundle, full current manifest/content, exact
   journal bytes, durable D06 source-binding/private-Git-ready records, and
   `privategit.Repository.Verify`. No host mount is attempted. No path, Binder,
   callback, or caller boolean mints the source capability.
2. **D09 consumer proof.** Only trusted Podman/crun setup consumes the source
   descriptor as a bind source. The entrypoint verifies the actual read-only
   consumer mount in the cell namespace, compares its device/inode with source
   evidence, records its own nonzero consumer mount ID, and replaces/closes the
   raw source FD before Node starts. Missing/mismatched proof must refuse before
   worker readiness or prompt admission. Source `MountID` and consumer
   `MountID` are distinct; they are never equated.

### Opaque sessionrepo source capability

`sessionrepo.VerifiedSealedSource` is the concrete, opaque issuer retained by the
trusted host composition. It has no exported fields and can only be created from
an exact `sessionrepo.Generation` through:

```go
type sessionrepo.SealedSourceEvidence struct {
    GenerationID, TreeDigest, ManifestDigest string
    SourceBindingDigest, ProvenanceDigest string
    StoreAuditHead string
    StoreAuditRecordCount uint64
    SourceIdentityDigest string
    SourceDevice, SourceInode, SourceMountID uint64 // host SOURCE identity only
}

type sessionrepo.VerifiedSealedSource // opaque; zero value invalid

func (g *sessionrepo.Generation) ExportSealedSource(ctx context.Context) (*sessionrepo.VerifiedSealedSource, error)
func (s *sessionrepo.VerifiedSealedSource) IsForGeneration(store *sessionrepo.Store, g *sessionrepo.Generation) bool
func (s *sessionrepo.VerifiedSealedSource) Evidence() (sessionrepo.SealedSourceEvidence, error)
func (s *sessionrepo.VerifiedSealedSource) DuplicateForD09(ctx context.Context) (*os.File, sessionrepo.SealedSourceEvidence, error)
func (s *sessionrepo.VerifiedSealedSource) Revalidate(ctx context.Context, evidence sessionrepo.SealedSourceEvidence) error
func (s *sessionrepo.VerifiedSealedSource) Close() error
func (s *sessionrepo.Store) OwnsGeneration(g *sessionrepo.Generation) bool
```

`ExportSealedSource` requires the exact current tip and a passing Store chain,
bundle, complete manifest/content scan, and a matching durable D06 source and
private-Git-ready event pair in that Store journal. It captures store audit
head/count and source fd `(device,inode,mount ID)`. Export is one-use; the
returned O_PATH descriptor is caller-owned and must be closed by the trusted
channel/launcher owner. The capability retains a separate private descriptor
until `Close`, and refuses `Close` while its one exported staging FD is still
open. `Store.Close` fails while the capability is live. Neither the capability
nor its evidence asserts that the source has been mounted read-only in the
consumer.

Issuance is current-tip-only. After ordinary tool effects advance the Store,
`Revalidate` permits this exact generation as a retained immutable ancestor if
the full Store still verifies and the captured audit prefix through the stored
head/count is unchanged. It refuses a missing generation, quarantine, modified
content, D06-record mismatch, or truncated/rewritten prefix; it does not require
the original Pi source generation to remain the newest tip forever.

sessionlaunch wraps the Store cap only from a live `PreparedSession`:

```go
func (p *sessionlaunch.PreparedSession) OpenVerifiedD09ExposureSource(
    ctx context.Context, generation *sessionrepo.Generation,
) (*sessionlaunch.VerifiedD09ExposureSource, error)

func (s *sessionlaunch.VerifiedD09ExposureSource) SourceCapability() *sessionrepo.VerifiedSealedSource
func (s *sessionlaunch.VerifiedD09ExposureSource) SourceEvidence() (sessionrepo.SealedSourceEvidence, error)
func (s *sessionlaunch.VerifiedD09ExposureSource) ExportD09WorkerSource(ctx context.Context) (*os.File, sessionrepo.SealedSourceEvidence, error)
func (s *sessionlaunch.VerifiedD09ExposureSource) VerifyD09WorkerSource(ctx context.Context, evidence sessionrepo.SealedSourceEvidence) error
func (s *sessionlaunch.VerifiedD09ExposureSource) Close() error
```

This type intentionally does **not** implement the legacy
`piruntime.VerifiedWorkerExposure`; that interface describes a pre-established
read-only consumer view. A has a separate D09-source interface using the exact
`ExportD09WorkerSource`/`VerifyD09WorkerSource` method names above. Its channel
constructor must use the descriptor returned by the single export as the
Podman source for FD 4; it must not call `DuplicateForD09` again. Channel close
owns and may close that returned staging FD; channel/transport closure only
initiates drain and is not kernel-observed STOP. Keep the source cap and sealed
generation/private-Git ownership alive through actual D09 worker and command
settlement plus scope and mount cleanup. Only after those independent terminal
checks may C close the source capability, then B's PreparedSession/private-Git
and Store. On UNKNOWN, retain every object required for reconciliation—including
the source capability and generation lease—and never trigger source-generation
GC or claim STOP.

The requested generation must be the exact retained pointer in this
PreparedSession Store and the independently reverified current tip; it is not a
path/label authority input. The wrapper revalidates the durable D06 binding and
private-Git provenance before each export/verify. The historical
`OpenSealedWorkerExposure(generation)` API was removed because its name implied
a consumer view. Production composition must call the explicit
`OpenVerifiedD09ExposureSource(ctx,generation)` constructor. In existing
`piruntime.WorkerExposureEvidence`,
`Device`/`Inode`/`MountID` refer to the host SOURCE descriptor; `ViewIdentity` is
a digest of the source capability. They do not encode a consumer view.

### Immutable runtime-asset source

`OpenVerifiedRuntimeAssets(root, spec)` remains content-only fixture/development
inspection. Production should use:

```go
func sessionlaunch.OpenVerifiedD09RuntimeAssetSource(
    root *os.File, spec sessionlaunch.RuntimeAssetSpec,
) (*sessionlaunch.VerifiedD09RuntimeAssetSource, error)
func (s *sessionlaunch.VerifiedD09RuntimeAssetSource) ExportD09RuntimeAssetSource(ctx context.Context) (*os.File, piruntime.WorkerRuntimeAssetEvidence, error)
func (s *sessionlaunch.VerifiedD09RuntimeAssetSource) VerifyD09RuntimeAssetSource(ctx context.Context, evidence piruntime.WorkerRuntimeAssetEvidence) error
```

This opaque production type additionally requires the bundle to be root-owned on
an actually read-only host mount, verifies the complete pinned content digest,
rejects symlinks/special files/nested mounts, and exports at most one caller-owned
source descriptor. The cell entrypoint must still independently prove FD 9's
consumer-side read-only mount and report its distinct consumer mount ID before
Node starts. The production D09 runtime source intentionally does **not**
implement legacy `piruntime.VerifiedWorkerRuntimeAssets`; content-only
`VerifiedRuntimeAssets` remains the non-production fixture API.
Its caller-owned FD 9 staging duplicate must close before the strict runtime
source capability closes; it refuses early close rather than releasing the
host bundle lease while a consumer may still hold it.

### Tests and current limits

Component tests use actual sessionrepo.Store/audit/private-Git fixtures and do
not mount, launch a container, install, use a provider, or change host state.
They cover Store-without-D06 refusal, source-vs-Store/admin-root identity,
capability/evidence round-trip, caller-owned descriptor close, one-use export,
stale generation-at-issuance refusal, retained-ancestor revalidation after a
normal `g0 -> g1` Store advance, captured-audit-prefix rewrite, sealed-tree and
private-Git snapshot tamper, quarantine refusal, and rejection of writable
runtime-asset sources. A negative structural assertion proves the D09 source
and content-only fixture types do not implement the legacy generic consumer-view
interfaces. These fixtures establish only component behavior, not consumer
mount proof or G1 evidence.

The current held edits are not yet a frozen combined A/B/C candidate. In
particular C must use `ExposureBinder: nil`, capture the current managed live-root
into the fresh Store under its held writer lease (never export that live-root
handle to Pi), then call `Prepare` and `OpenVerifiedD09ExposureSource` for the
sealed current tip. A must require and revalidate the concrete
`*sessionrepo.VerifiedSealedSource` in admitted D09 bindings, and withhold
Node/ready/prompt until its actual consumer-mount proof is captured and
validated. No full current-tree Linux acceptance result is inferred from the
earlier frozen `aehHOH` snapshot.

Remaining operator prerequisites are the protected read-only runtime bundle,
signed image/profile and offline Cosign trust, rootless Podman/crun, delegated
cgroup-v2 subtree, and a provisioned host that can exercise the positive source
bind/consumer proof. No mount was attempted from this unprivileged development
service. This code does not establish admission, G1, H1, or a governed launch.
