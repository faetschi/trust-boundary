# Governed Pi SDK worker and Linux launcher

This document describes the actual Pi SDK worker and its Linux process contract.
It is implementation evidence for an experiment harness, **not production/G1
admission evidence**. The current Linux host has no frozen D09 rootless
Podman/crun cell profile, root-owned signed profile/key, or production
sessionlaunch handoff. Production admission therefore remains refused.

## What runs

`adapter/src/governed-pi-worker.ts` constructs a real Pi `AgentSession` with the
pinned Pi packages (`@earendil-works/pi-coding-agent`, `@earendil-works/pi-ai`,
and `@earendil-works/pi-tui` at `0.87.1`, and `typebox` at `1.3.27`). It uses
the public Go-broker provider from `adapter/src/broker-provider.ts`, a closed
model runtime with no stored credentials, and exactly four proxy tools:
`read`, `write`, `edit`, and `bash`. It does not use Pi's unrestricted
`InteractiveMode.run()` and does not replace Pi with a custom model/tool
harness.

The worker's provider projection is created with
`createBrokerProviderFromFD(7)`. Go owns the provider `Conversation`, its
history and network access, prompt admission, provider/tool correlation,
durable decisions, and executor results. FD 7 accepts only the existing
provider-bridge `next`/`cancel` requests and returns bounded turn projections;
it has no operation for sending prompt text, transcript/history, tool results,
model selection, endpoint data, headers, or credentials. Tool proposals and
results use the existing TBound IPC on FD 6. The host must call
`Conversation.AdmitPrompt(taskID, prompt)` before releasing a matching Pi
control prompt.

The worker event stream is deliberately **lifecycle-only**. It does not send
assistant text, transcript/history, tool results, or credential data back to Go.
Its bounded strict-JSON events are `ready`, `turn_end`, `worker_error` (fixed
error code only), and `stopped`. A caller that needs a user-facing rendering
path must arrange it in its owning CLI/UI composition; this worker does not
invent one.

## Descriptor map

The map is fixed by `piruntime.PiWorkerDescriptorProfile`. Go's Linux
`exec.Cmd.ExtraFiles` behavior makes the first `ExtraFiles` item child FD 3;
the launcher orders and validates every following role before starting Node.

| Child FD | Role | Direction / content |
|---:|---|---|
| 0 | session control | Host-to-worker length-prefixed `prompt`, `abort`, or `stop` commands. Prompt text is sent only after Go admission. |
| 1, 2 | silent-worker output | Explicitly `io.Discard`; child stdout/stderr are not forwarded to Go and cannot become an alternate transcript channel. |
| 3 | held Node ELF executable | Opened without following symlinks, hashed through the held descriptor, then executed as `/proc/self/fd/3`. |
| 4 | opened private session CWD | The child verifies this descriptor identifies its actual current directory. |
| 5 | bootstrap pipe | One bounded strict-shape JSON frame, then EOF. Contains profile/source/session identities and channel binding only. |
| 6 | proposal/result IPC socketpair | Existing TBound `tbound-ipc/v1` frames; Node sends proxy proposals and receives host results. |
| 7 | Go provider duplex socketpair | Existing `tbound-provider-bridge/v1` `next`/`cancel` protocol. |
| 8 | worker event pipe | Bounded strict-JSON lifecycle events only; no transcript or text deltas. |
| 9 | opened Pi runtime bundle directory | Script entrypoint is `/proc/self/fd/9/src/governed-pi-worker.ts`; Pi package imports resolve relative to the bundle. |

FD 3 and FDs 4–9 are inherited through the descriptor-bound launch. The
working-directory and runtime-bundle handles are also retained by Go for the
session lifetime. The child verifies the CWD/bundle descriptors and Node version
before announcing readiness. The launcher does not put socket addresses,
prompts, credentials, or provider configuration in environment variables.

The child environment is exactly `HOME`, `LANG`, `PI_OFFLINE`, and `TMPDIR`;
the private `HOME`, `TMPDIR`, and CWD must be owned by the launching Linux UID
and mode `0700`. The signed environment digest is checked in both Go and Node.
Prompts are bounded to 16 KiB; bootstrap/control frames are bounded; worker
event frame, count, stream-byte, queue-count, and queue-byte limits are fixed
in the Go and TypeScript implementations.

## Sealed worker-exposure contract (composition gate)

**FD 4 is not `privategit.Repository.Root()` and is not the session Store root,
generations directory, private Git admin directory, or their parent.** The
current `piruntime.LaunchPiSDKWorker` API taking `*privategit.Repository` is
withdrawn as a composition contract; it must not be wired to an admin/root
descriptor or used to claim D06.

The sessionlaunch owner already has the right source object: a
`*sessionlaunch.WorkerView` returned by `PreparedSession.ExposeReadOnly`. The
small adapter contract requested from that owner is:

```go
type WorkerExposureEvidence struct {
    GenerationID, TreeDigest, ManifestDigest string
    BindingDigest, ProvenanceDigest, ViewIdentity string
    Device, Inode, MountID uint64
}

type VerifiedWorkerExposure interface {
    ExportVerifiedWorkerExposure(context.Context) (*os.File, WorkerExposureEvidence, error)
    VerifyWorkerExposure(context.Context, WorkerExposureEvidence) error
}
```

`WorkerView` should implement this adapter without exposing `PreparedSession`'s
private Git/admin handles. Export must re-verify the exact selected generation,
tree and manifest, D06 binding/provenance, registered view identity, and
descriptor device/inode/mount ID; verify must re-check that the same registered
view is still live. These fields bind together; `SameFile`/inode equality alone
is insufficient. The only session root descriptor returned is the separately
sealed generation view. The admin repository and provenance/journal handles
remain Go-owned and CLOEXEC.

The corresponding runtime-assets API is separate:

```go
type VerifiedWorkerRuntimeAssets interface {
    ExportVerifiedWorkerRuntimeAssets(context.Context) (*os.File, WorkerRuntimeAssetEvidence, error)
    VerifyWorkerRuntimeAssets(context.Context, WorkerRuntimeAssetEvidence) error
}
```

It must bind the runtime content digest and registered asset-view/mount identity.
The parent-side source descriptors for the workspace and runtime assets are
**not** child `ExtraFiles`. The future D09 Podman/crun launcher must mount the
approved workspace and read-only asset view in its controlled namespace, reopen
FD 4 (`/workspace`) and FD 9 (the asset root) from inside that namespace, and
pass only those reopened descriptors to Node. It must independently observe the
consumer mount IDs and read-only properties; a Node `ready` self-report is not
that evidence. The workspace and asset source descriptors must not permit
`openat(fd, "..")` to reach the private Git parent, session Store, live source,
or host runtime parent from the worker's namespace.

**Stability:** this is the typed API proposed for the sessionlaunch/host owners;
it is not yet a production-stable end-to-end contract. It becomes stable only
after the owner confirms the `WorkerView` adapter and the D09 launcher confirms
the namespace-side reopen/identity semantics. Until then, the production launch
entry remains refused and no composition may substitute raw root descriptors.
The current raw-directory constructor is for the explicit synthetic development
worker only; it is not the production FD 4/9 factory.

## Go APIs and owner wiring

The Linux channel factory currently used by the development test is an internal
raw-directory helper. It is not a production API. The future production factory
will take `VerifiedWorkerExposure` and `VerifiedWorkerRuntimeAssets`, retain
their source handles only in Go, and bind their evidence to the trusted runtime
profile.

The remaining session API is:

```go
worker, err := piruntime.LaunchPiSDKWorker(ctx, admissionCapability,
    verifiedExposure, verifiedRuntimeAssets, channels, bootstrap)
```

The returned channels/worker will expose the existing IPC server and provider
duplex, plus the child control/event channels. The sessionlaunch/CLI owner must
bind the same verified exposure and asset capabilities into the host profile;
it must not pass `privategit.Repository.Root()`, use a caller-set boolean, or
infer trust from path/inode equality. The worker exposes its
`Admission()` barrier, `PID()`, lifecycle `NextEvent`, Go-admitted
`SendAdmittedPrompt`, and `Close`. Effectful proposal handling remains the
existing Go `piruntime.Supervisor`/durable executor path over `IPCServer()`;
that executor must use the session admission barrier and retain its own
durability/containment authority.

The conversation owner starts the existing `providerbridge.Serve` over
`ProviderChannel()` and must not add a parallel provider RPC. The session/CLI
owner decides how to supervise both Go serve loops and how to bind its already
validated session handoff. Neither `providerbridge` nor `sessionlaunch` owner
files were changed for this worker batch.

## Production admission is intentionally refused

The Linux implementation has useful reviewed primitives, but they are not
equivalent to the required frozen production cell:

* `LinuxExecutableLauncher` opens and hashes a held Node ELF descriptor and
  uses `clone3(CLONE_INTO_CGROUP)` for an already-created cgroup-v2 scope. A
  production Node binary must additionally be root-owned and reside on a
  read-only mount; a digest followed by path exec is not accepted.
* `LinuxCgroupScope` is bound to an empty direct child of an explicitly
  delegated cgroup root, checks the `memory`/`pids` controllers, kills with
  `cgroup.kill`, and reports settlement only after `cgroup.events` says
  `populated 0`.
* `VerifyLinuxPiRuntimeBundle` refuses symlinks/special files and requires a
  read-only mount for a complete runtime tree. The separate developer digest
  helper is intentionally weaker and is never used as production evidence.
* `LinuxHostProfileVerifier` reads only root-owned, non-group/world-writable
  profile inputs from `/etc/tbound/pi-host-profile.json`,
  `/etc/tbound/pi-host-profile.ed25519`, and `/etc/tbound/pi-host-profile.pub`.
  `VerifyLinuxSignedHostProfile` checks strict JSON, the Ed25519 signature, and
  the canonical profile digest.

Go's current `os/exec` child setup applies `Cmd.Dir` before installing
`ExtraFiles`, so it cannot `fchdir` through FD 4 in this launcher. It uses the
signed private CWD path and the worker compares the actual directory identity
with FD 4 before emitting `ready` or accepting a prompt. This check is
fail-closed for a changed path, but it is not a descriptor-only `fchdir` proof;
that limitation is another reason not to promote this host primitive to a
production containment claim.

However, a cgroup-only host exec is **not** the D09 Podman/crun namespace,
seccomp, network, mount, and descendant-containment profile. The concrete
launcher therefore returns `ErrProductionLauncherUnavailable` from its frozen
profile admission check. The root-owned profile inputs are not provisioned in
this environment. A locally generated/test signature, fixture verifier,
container boolean, WSL process, or direct-child `Wait` must not be represented as
production evidence. Without verified production descendant settlement, cleanup
remains `UNKNOWN`.

The cross-process integration path below is explicitly development-only and
uses an `UNKNOWN` settler after the direct child exits. It cannot create an
`AdmissionCapability` or claim G1.

## Tests

Always-available checks:

```sh
cd adapter
npm exec tsc -- --noEmit
node --experimental-strip-types --test --test-concurrency=1 src/governed-pi-worker.test.ts

cd ../supervisor
go test -mod=readonly ./internal/piruntime
```

The actual Linux process round-trip is opt-in. It copies only the owned adapter
worker/bridge files into a unique mode-`0700` test directory under an
operator-supplied private ext4 root, links `node_modules` to an explicit
pre-existing dependency tree, and uses synthetic Go provider turns and a
synthetic IPC `DENY`. It makes no provider/network/credential call and removes
only the unique run directory it created. The inputs are:

The evidence run in this checkout used WSL2 with the private test root on ext4.
That is development evidence only; it is not a native host profile or G1
containment result.

```text
TBOUND_GOVERNED_PI_NODE          absolute Linux Node v24.15.0 executable
TBOUND_GOVERNED_PI_NODE_MODULES  absolute existing pinned node_modules directory
TBOUND_GOVERNED_PI_TEST_ROOT     existing current-UID mode-0700 ext4 directory
```

Run `go test -mod=readonly -run TestDevelopmentPiSDKWorkerInheritedFDProcessRoundTrip -v ./internal/piruntime`
with those three variables and a private `HOME`, `GOCACHE`, and `TMPDIR` in the
Linux environment. The test proves the real Pi SDK process starts, obtains a
synthetic Go provider tool-call projection, sends a real proxy proposal over
FD 6, receives the correlated synthetic Go `DENY`, requests the result-dependent
second Go provider turn, and reaches `turn_end`. It then asserts that
uncontained development cleanup is `UNKNOWN`. This is process/SDK compatibility
evidence only—not G1, a production peer/session binding, or production
descendant-settlement evidence.
