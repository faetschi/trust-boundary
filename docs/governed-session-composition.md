# Governed session and D06 composition — 2026-10-08

## Status and claim boundary

This increment adds a concrete sessionrepo→D06 source-binding→private-Git
composition and a provider-audit-backed callback set for the existing
`DurableExecutor`. It is **not** a claim-bearing TBound release, actual Pi/Node
launch, real provider exchange, D09/D10/D11 host profile, G1 publication, or
thesis acceptance. The current Linux test injects a synthetic audit prefix and
uses the measured `dev-wsl-non-claim-bearing` command sandbox. It does not use
credentials or contact OpenRouter.

Production `tbound serve --pi` must remain refusal-only. The launcher owner has
published its typed worker/channel API and FD-role map, but the current contract
requires the inherited CWD to be the same inode as the verified private Git
root and gives that directory descriptor to Node as FD 4. That makes the
supervisor-private Git/admin namespace reachable by the worker, violating D06.
This composition deliberately does not pass `Repository.Root`, the admin
parent, or another private Git handle into `NewPiWorkerChannels` or
`LaunchPiSDKWorker`; a separate verified sealed-generation/exposure-view
descriptor contract is required first. No source file owned by that worker or
by the broker bridge was modified here.

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

**Known publication blocker:** the current `workflow.Finalizer.Finalize` in
`supervisor/internal/workflow/publication_linux.go` rejects a terminal
`delta.OperationLease` before invoking `Trust.VerifyOrigin` (the explicit
lease-only refusal at the finalizer origin check). The E05 chain ends in Bash,
whose durable operation identity is a lease. `SessionOperationAuthority` can
verify the authentic lease→provider proposal association and
`CheckFinalizerOrigin` reports `ErrLeaseOriginUnsupported`, but this increment
does not modify the workflow/delta owner or fabricate a final write. Therefore
the tested E05 source/operation chain is **not yet publishable by the existing
finalizer**.

Before changing the workflow contract, authorize a narrow follow-up in
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

- The launcher has published `NewPiWorkerChannels`, `LaunchPiSDKWorker`,
  `Admission`, `PID`, `NextEvent`, `SendAdmittedPrompt`, and `Close`. Its
  documented worker map is FD 0 control, FD 3 held Node ELF, FD 4 opened session
  CWD, FD 5 bootstrap, FD 6 TBound IPC, FD 7 provider bridge, FD 8 lifecycle
  events, and FD 9 runtime bundle. The D06 blocker is that FD 4/CWD currently
  aliases the verified private Git root. This package does not pass that root,
  guess/reorder roles, or reinterpret the separate provider-bridge FD 3
  description as the worker map. The launcher owner is refining a distinct
  sealed/exposure-view descriptor contract.
- Once a D06-safe read-only sealed-generation/registered-exposure descriptor
  can be handed to the worker without revealing Store or private Git admin
  roots, the CLI owner can build the exact fresh
  session graph: secure host profile → private audit/session root → filtered
  sealed-source bind → private Git admin root → D06 provenance → real provider
  conversation → pre-prompt admission → registered proxy IPC → real
  `DurableExecutor` → broker result journal → actual Pi worker → shutdown and
  publication/recovery. The production path must still refuse until host
  executable/environment/terminal identity, containment, process closure,
  mount detach, and crash-recovery evidence are backed by the admitted profile.
- The current providerbridge tests intentionally use a synthetic Doer; this
  increment makes no real provider call and does not load credentials. Operator
  approval and the secure loader are separate prerequisites.
- A separate coordinator compatibility snapshot reports that the Pi SDK
  `AgentSession` ran with the installed Linux Node 24.15 executable and already
  present pinned package assets. That is WSL development compatibility only; it
  does not bind those bytes to an admitted profile or provide the missing
  launcher/descriptor-role integration.
- No CLI fallback to the Go-only fixture, stock unrestricted Pi CLI, Windows
  Node binary, or direct host command execution is introduced.

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
returned the explicit lease-only finalizer blocker. It does **not** prove Pi,
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
the narrow workflow lease-origin extension described above plus an actual
registered `workflow.Trust` implementation. Until those interfaces and host
proof exist, production `serve --pi` must keep refusing admission.
