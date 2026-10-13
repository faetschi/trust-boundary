# Governed host setup (Phase 2)

This runbook covers what an **operator** must provision before `tbound serve --pi`
can be admitted. Nothing here is automated yet, and none of it should be done
without explicit operator authorization. Run the read-only checker first:

```sh
scripts/check-governed-host.sh
```

It never changes state and prints PASS/WARN/FAIL for each requirement.

## Scope and honesty boundary

`tbound serve --pi` stays **refused** until every item below exists on the
declared evaluation host (D09/D10/D11). WSL2 and workstation hosts are
development-only; they are not the confirmation profile. A capability probe, a
`null` callback, or a boolean is never a substitute for the real profile, signed
image, and containment evidence.

## 1. Host and filesystem (D10)

- A dedicated Linux VM/host with a recorded image, hypervisor, kernel/config,
  filesystem and mount options.
- No host-share mounts, clipboard, host credentials/sockets, or personal repos in
  the guest; synthetic repositories and canaries only.
- A clean snapshot plus a demonstrated restore, and a reset procedure per trial.

## 2. Container runtime and image (D09)

- Rootless **Podman** with **crun** at pinned versions.
- A digest-pinned cell image and an offline **Cosign** verifier with the
  authorized public key. Offline verification targets a **local OCI image
  layout** with `cosign verify --offline --key ... --local-image ...` and its
  offline signature bundle, never a registry reference. The launcher verifies every local OCI manifest,
  config and layer blob against its digest and checks the cosign critical image
  digest against the same pin. It runs cosign in a fresh unprivileged user+network
  namespace with no egress; an unavailable namespace refuses rather than falling
  back to host networking.
- A signed in-image entrypoint (one conforming entrypoint: Go or minimal Rust).
- No pulls at run time; identity drift must fail closed.
- The image digest, entrypoint, static seccomp profile, Landlock ruleset,
  exact root-owned/read-only podman/crun/cosign paths+digests, `/bin/bash`
  path+digest, rootless UID/GID, private namespace profile, cgroup-v2
  controllers, xattr-visibility profile digest, resource limits, and delegated
  cgroup root are recorded in the registered root-owned D09 cell template at
  `/etc/tbound/d09-cell.json` (see §4a). Each run passes the pinned crun path
  explicitly; production has no PATH runtime fallback.

## 3. Containment composition (D11)

- Namespaces, `no_new_privs`, process-wide **Landlock**, one static
  **seccomp-BPF** profile, and **pidfd**.
- A fixed, non-threaded, **delegated cgroup v2 subtree** with the required
  controllers (cpu/memory/pids) and writable `cgroup.kill`, observable
  `cgroup.events:populated`, and recursive `populated=0` teardown.
- A surviving runner that owns TBound, Pi, and every cell and settles descendants;
  unresolved cleanup must report `UNKNOWN`, never fake `STOPPED`.

`scripts/check-governed-host.sh` inspects these read-only.

## 4. Signed host profile

The production admission trust root is fixed and root-owned; the CLI can never
choose its own trust root. `tbound serve --pi` discovers the default profile and
the **fixed admin public key** at:

```
/etc/tbound/pi-host-profile.json
/etc/tbound/pi-host-profile.ed25519
/etc/tbound/pi-host-profile.pub      # the only admission trust root
```

`--native-host-profile <path>` may select a **protected** (root-owned,
non-group/world-writable) profile/signature pair, but the verification key is
still read only from `/etc/tbound/pi-host-profile.pub`. An operator cannot point
the CLI at their own adjacent `.pub` and self-sign their way to admission.

### 4a. Signing workflow

The `tbound-profile` tool is the single source of truth for the signature
domain, the canonical digest, and the fixed in-cell worker identities. It never
prints or copies private key material.

After installing the reviewed Pi runtime at `/var/lib/tbound/runtime` on its
final read-only mount, measure it with
`"$PRIVATE/tbound-profile" runtime-bundle-digest --root /var/lib/tbound/runtime`.
Build the tool first as shown below. Copy the printed `sha256:…` into the
profile's `PiRuntimeBundleDigest` before signing. This is the Go whole-tree
digest, which differs from the stager's `closure-manifest.sha256`. The command
requires the existing production tree verifier's read-only mount checks; it
does not verify profile signatures or host trust bindings and does not admit
or launch the worker.

```sh
# 1. Generate the canonical unsigned host profile template (placeholders).
go build -o "$PRIVATE/tbound-profile" ./cmd/tbound-profile
"$PRIVATE/tbound-profile" init --out pi-host-profile.json

# 2. Generate the registered D09 cell template and fill every measured identity
#    (image digest, OCI layout, cosign public-key digest, seccomp digest,
#    the `/usr/local/bin/node` executable bytes in the signed image must have the
#    same measured digest as the root-owned/read-only host Node opened on fd3,
#    Landlock ABI/ruleset, frozen
#    empty capability set, entrypoint digest, cpu/memory/pids bounds, cgroup
#    root) and the managed live-root registration digest. An unmeasured draft
#    or missing registration digest is rejected.
"$PRIVATE/tbound-profile" template-init --out d09-cell.json
# Measure the exact managed writable root and its dedicated, non-login systemd
# writer service/mount configuration on the prepared host. This measures policy
# identities only; it is not a quiescence permit and cannot prove no writer is
# currently active. Use only a dedicated service UID/GID that cannot be used by
# interactive or adjacent processes, and bind the resulting registration digest
# into d09-cell.json before computing the template digest.
"$PRIVATE/tbound-profile" managed-root-measure --root /var/lib/tbound/live \
  --service-name tbound-writer --service-group tbound-writer \
  --service-unit /etc/systemd/system/tbound-writer.service \
  --mount-unit /etc/systemd/system/var-lib-tbound-live.mount \
  --runner /usr/local/bin/tbound \
  --service-cgroup /system.slice/tbound-writer.service \
  --out managed-live-root.json
# Sign the registration with the same offline authorized key as the host profile;
# the command prints its digest, not key material. The key is never installed.
"$PRIVATE/tbound-profile" managed-root-sign --registration managed-live-root.json \
  --key admin.ed25519 --out-registration managed-live-root.json \
  --out-sig managed-live-root.ed25519 --force
# Copy the printed digest into managed_live_root_registration_digest in d09-cell.json.
# Measure the protected source filesystem/mount/scanner privilege profile and
# derive its xattr digest. Run the measurement as the dedicated non-root scanner
# identity, with zero effective/bounding/ambient capabilities, NoNewPrivs, and
# the initial host user namespace (for example through the host's audited
# `setpriv` utility). It refuses if that real privilege state is not present:
setpriv --reuid <service-uid> --regid <service-gid> --clear-groups \
  --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- \
  "$PRIVATE/tbound-profile" xattr-measure --source /path/to/protected-source
"$PRIVATE/tbound-profile" xattr-digest --filesystem-magic <measured-decimal> \
  --mount-options-digest <measured-sha256> --uid <measured-uid> --gid <measured-gid>
# Set xattr_scan_privilege_profile to the measured closed enum
# `service-nonroot-no-extra-groups-empty-caps-nnp-initial-userns/v1`; set xattr_visibility_authority
# to `root-profile-measured-complete-enumeration/v1` only after the offline host
# trust owner has independently approved the complete-enumeration basis.
# Derive the exact ABI-8 FS+network ruleset digest from the reviewed allowlist.
"$PRIVATE/tbound-profile" landlock-digest --read-only /usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev \
  --read-write /work,/tmp
# ... replace every empty field in d09-cell.json with the measured value ...
"$PRIVATE/tbound-profile" template-digest --template d09-cell.json
# ... set the profile's ContainmentProfileDigest to that digest ...

# 3. Build the signed cell image + entrypoint (see packaging/governed/README.md).
podman build -f packaging/governed/Containerfile -t localhost/tbound/pi-cell:latest .

# 4. Sign with the admin Ed25519 private key. The written Digest is exactly the
#    digest the verifier recomputes.
"$PRIVATE/tbound-profile" sign --profile pi-host-profile.json --key admin.ed25519 \
  --out-sig pi-host-profile.ed25519 --out-pub pi-host-profile.pub

# 5. Offline review (explicit key; tooling only).
"$PRIVATE/tbound-profile" verify --profile pi-host-profile.json \
  --sig pi-host-profile.ed25519 --pub pi-host-profile.pub
# Offline review of the registration (explicit key; tooling only):
"$PRIVATE/tbound-profile" managed-root-verify --registration managed-live-root.json \
  --sig managed-live-root.ed25519 --pub pi-host-profile.pub
```

Install the profile, its signature/public key, the managed live-root registration
and signature root-owned (no group/world write) under `/etc/tbound/`, and the D09
template at `/etc/tbound/d09-cell.json`. The signed host profile binds the D09
template digest; the D09 template in turn binds the managed-root registration
digest. Runtime admission checks the fixed `/etc/tbound/pi-host-profile.pub`
trust root, registration signature, exact root/parent device+inode+filesystem+
mount identities, writable mount options, local non-login service account,
protected passwd/group/NSS and systemd service/mount configuration (`User`/`Group`,
`ExecStart`, `NoNewPrivileges`, `ProtectSystem=strict`, `Delegate=yes`,
`KillMode=control-group`, exact `ReadWritePaths`, `Where`, filesystem `Type`, and
`rw,nosuid,nodev` mount options), actual runner executable, and exact service
cgroup path plus per-invocation device/inode/mount identity. `--workspace` only
selects a candidate descriptor; it cannot grant registration or writer authority.
The registration authenticates the installed writer-domain policy, not present
quiescence. The CLI's separate opaque `ManagedLiveRootAuthority` must still
acquire the writer epoch, drain/reconcile cells, and issue a one-use terminal
publication permit. The host trust assumption is that only the registered
trusted service uses the dedicated non-login writer UID; directory mode 0700
alone is not exclusive against an arbitrary same-UID process. Missing or mismatched
registration causes a typed pre-launch refusal; there is no cooperative developer
fallback in production mode.

The signed registration schema is `tbound-managed-live-root-registration/v1`
with the separate signature domain `tbound-managed-live-root-registration/v1\0`.
It records exact root and direct-parent path/device/inode/filesystem magic/mount
ID/options digest/owner/mode; the dedicated local service name/GID/non-login
shell and protected `/etc/passwd`, `/etc/group`, `/etc/nsswitch.conf`, shell,
service-unit and mount-unit digests; pinned runner path/digest; and the exact
`/system.slice/<service>.service` cgroup path. The service cgroup's device/inode/
mount ID are runtime measurements (the kernel recreates them after service
restart), exposed through `VerifiedD09ManagedLiveRoot.CgroupIdentity()` and pinned
unchanged during one authority epoch, rather than signed as permanent IDs.
Systemd drop-ins/overrides for the selected units are refused; the verified unit
must enforce a dedicated `User`/`Group`, `NoNewPrivileges=yes`,
`ProtectSystem=strict`, `Delegate=yes`, `KillMode=control-group`, an exact
`ReadWritePaths` grant, and a matching `rw,nosuid,nodev` mount unit. Local NSS
`files` resolution and a single non-login account/group are required. The runtime
trusts the root-owned service manager/configuration as host TCB; the registration
does not attempt to detect a root administrator or a malicious process already
running under the dedicated service UID.

The operator must never install an Ed25519 private key in `/etc/tbound` or in the
cell image. The offline tooling's explicit public key is for review only; runtime
always reads the fixed root-owned admission key.

An **unedited template** profile (id `pi-host-profile-template-v1`) is never
admissible, even if signed: the placeholder digests are not measured identities.

### 4b. Child descriptor contract and entrypoint

The D09 launcher implements the sealed `ExecutableLauncher.StartVerified`
adapter. It launches a real rootless Podman/crun container that re-births the
worker at the fixed descriptor numbers published by
`piruntime.D09ChildRoleMap()`:

| fd | role |
|----|------|
| 3  | held, digest-verified Node binary (exec target) |
| 4  | sealed worker exposure directory (read-only reopen inside the cell) |
| 5  | bootstrap pipe |
| 6  | proposal IPC socketpair |
| 7  | provider bridge socketpair |
| 8  | worker events pipe |
| 9  | runtime bundle directory (read-only reopen inside the cell) |
| 10 | dedicated typed TCB proof pipe (entrypoint writes then closes before Node) |

**Chosen Landlock profile (explicit):** the Go entrypoint uses
`LANDLOCK_RESTRICT_SELF_TSYNC` so the ruleset covers every thread of the
multi-threaded Go runtime. This requires the **registered ABI 8 (TSYNC)** kernel
profile: the signed template's `landlock_abi` must be exactly `8`, the launcher passes
it as `TBOUND_LANDLOCK_ABI`, and a kernel reporting a lower ABI is refused with an
exact ABI-8 prerequisite (no silent downgrade, no `LockOSThread`-only claim). The
launcher captures the entrypoint's typed TCB proof on fd10 and refuses to return
a launched cell unless uid/gid, empty capabilities, `no_new_privs`, seccomp
filter mode, `landlock_abi`, the ruleset digest, and cgroup-v2 are all proven.

> **Kernel prerequisite (operator):** the evaluation VM must provide and record a
> kernel with Landlock ABI `8` (TSYNC). ABI1 and ABI versions other than 8 are explicitly unsupported
> and refuses; this is a runtime-measurement/approval prerequisite, not a silent
> baseline. The opt-in integration test skips only on the unsupported-ABI case and
> fails otherwise.

The source directories are handed in as **Go-only directory descriptors**
(fd4/fd9), never as host paths; the signed in-image entrypoint
(`cmd/tbound-cell-entrypoint`) reopens them read-only with `O_NOFOLLOW`, rejects
non-directories, and never follows a relative-parent escape. No raw private-Git
or host-admin descriptor is passed. The launcher passes no host bind mount: the
sealed view arrives only as fd4.

The launcher also settles through a held, verified `LinuxCgroupScope`:
`cgroup.kill`, recursive `populated=0` with a deadline, and pidfd, with owned
finite-context cleanup on every start/run error or cancellation.

> **Remaining production blocker:** the sealed exposure binder and production
> session composition exist, and the official Pi CLI dev/RPC route is implemented
> and offline-tested. The production profile/launcher migration that connects an
> admissible signed profile to that CLI path remains pending. The current
> production entrypoint `LaunchD09PiCLIWorker` refuses launch; no G1 claim is made.

### 4c. Existing binder/session APIs and production CLI wiring

The sealed binder/session APIs exist. SDK-worker APIs shown below are legacy,
unqualified, and inadmissible for production; the official Pi CLI dev/RPC route
is implemented, while production profile/launcher migration remains pending.

```go
// Assemble bindings + channels from verified sealed inputs (worker C/B):
func BindProductionPiWorkerBindings(ctx context.Context, profile HostProfile,
    exposure VerifiedWorkerExposure, assets VerifiedWorkerRuntimeAssets,
    launcher ExecutableLauncher, settler DescendantSettler) (HostRuntimeBindings, *PiWorkerChannels, error)

// Verified D09 backend (worker C):
func NewD09PodmanCrunLauncher(config D09Config) *D09PodmanCrunLauncher
func (l *D09PodmanCrunLauncher) BindCgroupScope(scope *LinuxCgroupScope) error
func (l *D09PodmanCrunLauncher) BindManagedLiveRoot(v *VerifiedD09ManagedLiveRoot) error
func (l *D09PodmanCrunLauncher) Close(ctx context.Context) error // unbind/close its owned registration duplicate only
func (l *D09PodmanCrunLauncher) StartVerified(ctx context.Context, req ExecutableLaunchRequest) (*exec.Cmd, error)
func (l *D09PodmanCrunLauncher) StartVerifiedWithProof(ctx context.Context, req ExecutableLaunchRequest) (*exec.Cmd, D09TCBProof, error)
func (l *D09PodmanCrunLauncher) BoundSettlement() DescendantSettler
func (l *D09PodmanCrunLauncher) SettleCell(ctx context.Context, leaderPID int) (D09SettlementEvidence, error)
func CreateLinuxCgroupScope(delegatedRoot *os.File, uniqueChild, containmentProfile, settlementProfile string) (*LinuxCgroupScope, error)
func (s *LinuxCgroupScope) Remove(ctx context.Context) error // settle + descriptor-relative remove exact direct child

// Fixed child contract shared with the entrypoint:
func D09ChildRoleMap() []D09ChildRole // fd4 exposure, fd5 bootstrap, fd6 ipc, fd7 provider, fd8 events, fd9 bundle
const D09ExecChildFD = 3
const D09ProofChildFD = 10

// Registered, measured D09 template:
func LoadD09CellTemplate(ctx context.Context) (D09CellTemplate, string, error)
func DecodeD09CellTemplate(raw []byte) (D09CellTemplate, error)
func (t D09CellTemplate) Config() D09Config
func (t D09CellTemplate) Digest() (string, error)

// The fixed-anchor signed host profile and protected D09 template bind xattr
// visibility with XattrFilesystemMagic, XattrMountOptionsDigest, scanner UID/GID,
// the closed XattrVisibilityAuthority enum and domain-derived profile digest.
// Runtime remeasures the actual source descriptor and reloads the fixed signed
// profile; a caller-built profile/template or Complete:true cannot mint it.
func VerifyD09XattrVisibility(source *os.File, signedProfile HostProfile, template D09CellTemplate) (workspace.XattrVisibilityAttestation, error)

// Per-launch TCB proof (bound to the exact command identity, never global):
func (w *PiSDKWorker) D09LaunchProof() (D09TCBProof, bool)
func (l *D09PodmanCrunLauncher) TakeLaunchProof(cmd *exec.Cmd) (any, bool)
func (p *Process) LaunchProof() any
```

There is intentionally no caller-facing `AdmitProfile(profile)` method:
`OpenLinuxHostProfileVerifier` verifies the root-owned signature/template and
calls the package-private D09 admission gate while constructing the trusted
verifier. A D09 command runner cannot be constructed before that admission.

**Legacy SDK-worker proof wiring (reference only; unqualified and inadmissible for
production):** `LaunchPiSDKWorker` -> `StartChild` -> `StartVerified` no longer drops
the proof. The launcher registers the validated proof against the exact `*exec.Cmd`;
`StartChild` attaches it to the `*Process`; `LaunchPiSDKWorker` attaches it to the
`*PiSDKWorker`. C calls `worker.D09LaunchProof()` and MUST journal the returned
`D09TCBProof` before releasing any prompt/effect; a `false` return means no proof
(development, or a failed launch) and no release is permitted.

Legacy SDK-worker wiring sequence (reference only, not production-admissible):
load + verify the signed profile; load the registered
template and require `profile.ContainmentProfileDigest == template digest`;
construct the held `LinuxCgroupScope` and `BindCgroupScope`; build the
`HostRuntimeBindings`/channels via `BindProductionPiWorkerBindings`; admit through
the trusted verifier; then `LaunchPiSDKWorker`. On close, call
`SettleCell(ctx, leaderPID)` for the pidfd + `cgroup.kill` + `populated=0`
evidence. After successful session worker removal, call `scope.Remove(ctx)` to
settle and descriptor-relatively remove only the verified direct-child session
scope. If Remove fails, retain the held scope for UNKNOWN/reconciliation; do not
forget the ownership handle or unlink by path. A real positive run requires a
provisioned host and is UNVERIFIED here.

`D09Config` (from `Template.Config()`) pins the podman/crun/cosign binary paths and
digests; when set, the launcher verifies the exact root-owned, non-writable,
read-only-mount bytes and never resolves a user-PATH binary. It also passes the
registered crun path as Podman's effective `--runtime` argument, so verifying a
crun file that Podman does not actually use cannot satisfy admission. The
seccomp JSON hash is read from a held `O_NOFOLLOW` descriptor with same-file,
root-owner, non-writable, and read-only-mount checks before Podman consumes it.

`LinuxCgroupScope.Remove(ctx)` is the session-scope teardown API. It kills and
observes `populated=0`, then removes only that exact direct child with
`unlinkat(AT_REMOVEDIR)` against the held delegated-root fd. If it cannot prove
or remove the scope, it retains the handles for UNKNOWN/reconciliation; callers
must not use `Close()` as a substitute or discard the ownership record.

After worker settlement and command-scope removal, the CLI calls
`launcher.Close(ctx)` to close only the launcher's owned duplicate of the
verified managed-root registration and clear its admitted profile. It returns
`ErrUnknownSettlement` while a launch, pidfd/proof record, active operation, or
worker/session cgroup, command cgroup, or unconsumed command-proof receipt remains.
It deliberately does **not** close the C-owned verified
registration/permit object or the C-owned session cgroup scope; the caller closes
those through their own lifecycle handles. Repeated `Close` is idempotent.

### 4d. Production Bash runner API

`piruntime` exposes the descriptor-bound production `sessionrepo.CommandRunner`.
C must use it for production Bash and must **not** substitute the legacy
non-claim `podman.New` profile as if it were D09.

```go
func NewD09CommandRunner(leaseID string, launcher *D09PodmanCrunLauncher) (*D09CommandRunner, error)
func (r *D09CommandRunner) TakeCommandReceipt(view *sessionrepo.CommandView, spec sessionrepo.CommandSpec,
    result sessionrepo.CommandResult, settlement sessionrepo.CommandSettlement) (D09CommandReceipt, error)
func (r D09CommandReceipt) Validate() error // opaque live runtime receipt, including issuance seal
func (r D09CommandReceipt) ValidateSnapshot() error // structural journal readback only, not authorization
func (r D09CommandReceipt) Digest() (string, error)
const D09CommandRunnerProfile = "d09-rootless-podman-crun-frozen-cell/v1"
```

The runner opens only the typed, live `sessionrepo.CommandView` descriptor and
binds it read-write at `/work` (never a raw private-Git or caller path). It
creates a fresh child cgroup under the held D09 session scope, passes that exact
cgroup path to Podman, and verifies the entrypoint's host PID belongs to the
scope before acknowledging the command. The command binary is the exact
root-owned image path/digest registered in `d09-cell.json` (normally `/bin/bash`).

The command cell uses fd3 for the held disposable-view source, fd4 for the TCB
proof pipe, fd5 for the host start acknowledgement, and a private tmpfs for
`HOME`/`TMPDIR`. The signed entrypoint verifies fd3 is the same inode as the
consumer `/work` mount, replaces/closes the raw source descriptor, verifies the
command-binary digest, applies ABI-8 TSYNC Landlock, writes its first proof, and
waits for the host to verify cgroup membership and acquire a pidfd before exec.
After Bash exits, it writes a second proof with the actual exit code and exits.

`Run` only returns a result after Podman inspect reports the exited container,
the Podman process has completed and its exit agrees with inspect, the final
proof agrees, output stayed within the 1 MiB bound and its writers are drained,
the container is removed, the leader pidfd is readable, `cgroup.kill` reaches
`populated=0`, the command cgroup is removed by held-parent `unlinkat`, and a
bounded `/proc` scan finds no process or open namespace fd retaining the exact
command mount namespace. Immediately after successful `Run`, the caller MUST
call `TakeCommandReceipt` with that same view/spec/result/settlement and journal
the returned receipt before releasing any Bash effect. Receipt consumption is
one-use and refuses mismatched/stale inputs; its live issuance seal is not
serialized. `ValidateSnapshot` only verifies the public fields/digest on durable
journal readback—the trusted caller must also match that snapshot against the
retained live receipt and the exact lease/call/effect/input tree/output delta/run
identity. A profile label, caller-minted booleans, or decoded receipt by itself
is not authorization.
Cleanup uses an owned finite context even if the caller was cancelled. Any
missing proof, ambiguous CLI exit, cleanup uncertainty, or observer error returns
no result/settlement; an unresolved scope is retained by the launcher for
reconciliation.

**TCB proof journaling:** `StartVerifiedWithProof` returns a `D09TCBProof` bound to
the exact container name, leader pid, image ref/digest, template digest, and
consumer-mount proof. The legacy SDK-worker `LaunchPiSDKWorker` -> `StartChild` ->
`StartVerified` path attaches it to that worker; C reads it with
`worker.D09LaunchProof()`. C **must append it as a durable TCB record before
releasing any worker prompt or effect**; a missing proof or failed journal must
stop the worker. A real provisioned launch remains UNVERIFIED here; injected
unit tests do not count as a production run.

### 4e. Managed live-root registration contract

The static root registration is policy input, not a `Complete` assertion or
quiescence capability. Runtime A exposes:

```go
func MeasureD09ManagedLiveRootRegistration(rootPath, serviceName, serviceGroup,
    serviceUnitPath, mountUnitPath, runnerPath, serviceCgroupPath string) (D09ManagedLiveRootRegistration, error)
func SignD09ManagedLiveRootRegistration(reg D09ManagedLiveRootRegistration,
    privateKey ed25519.PrivateKey) (registrationJSON, signature []byte, digest string, err error)
func VerifyD09ManagedLiveRootRegistration(registrationJSON, signature []byte,
    publicKey ed25519.PublicKey) (D09ManagedLiveRootRegistration, error) // offline review helper
func LoadD09ManagedLiveRootRegistration(ctx context.Context) (D09ManagedLiveRootRegistration, error) // fixed production paths/key
func VerifyD09ManagedLiveRoot(ctx context.Context, candidate *os.File) (*VerifiedD09ManagedLiveRoot, error)
func (v *VerifiedD09ManagedLiveRoot) Registration() D09ManagedLiveRootRegistration
func (v *VerifiedD09ManagedLiveRoot) DuplicateRoot() (*os.File, error)
func (v *VerifiedD09ManagedLiveRoot) Revalidate(ctx context.Context, candidate *os.File) error
func (v *VerifiedD09ManagedLiveRoot) CgroupIdentity() (D09ManagedLiveRootCgroupIdentity, bool)
func (v *VerifiedD09ManagedLiveRoot) Close() error
func (l *D09PodmanCrunLauncher) BindManagedLiveRoot(v *VerifiedD09ManagedLiveRoot) error
```

Production always loads `/etc/tbound/managed-live-root.json` and
`/etc/tbound/managed-live-root.ed25519`, verifies with the fixed
`/etc/tbound/pi-host-profile.pub`, requires the signed D09 template's
`ManagedLiveRootRegistrationDigest` to match, then compares the caller's actual
descriptor and current host state. Offline `tbound-profile managed-root-measure`,
`managed-root-sign`, and `managed-root-verify` only create/review operator input;
they do not install services, acquire locks, stop writers, or issue permits. The
live directory is service-owned UID/GID mode 0700 under a root-owned protected
parent and a dedicated RW `nosuid,nodev` mount. The signed `service_cgroup_path`
is stable policy; kernel cgroup device/inode/mount ID are freshly measured and
held stable by the verified object for that service invocation. Missing or
mismatched registration refuses before launch. The CLI-owned opaque
`ManagedLiveRootAuthority` separately controls the session epoch, writer drain,
command-cell settlement, and one-use publication permit; no piruntime
registration API grants publication authority.





## 5. Provider

Credentials stay in the Go broker; Pi/cells never receive the key. The approved
test channel is `nvidia/nemotron-3.5-lightning:free` with the secure loader and
synthetic content. Genuine provider runs need separate approval.

## 6. Evidence and reset

- Freeze the profile: VM/filesystem/kernel, runtime/image identities, generated
  OCI + static seccomp source/digest, Landlock ABI/rights, rootless mappings,
  cgroup/pidfd behavior, entrypoint.
- Retain source, image, runtime, observer, and result hashes per trial.

## Checklist

- [ ] `scripts/check-governed-host.sh` exits 0
- [ ] Podman + crun + Cosign pinned and verified offline
- [ ] Signed cell image + signed entrypoint (local OCI layout + offline key)
- [ ] Delegated cgroup v2 subtree with `cgroup.kill`
- [ ] Landlock + seccomp + `no_new_privs` + pidfd effective
- [ ] Root-owned signed `/etc/tbound/pi-host-profile.*` (tooling: `tbound-profile`)
- [ ] Root-owned `/etc/tbound/d09-cell.json` bound to the profile digest
- [ ] Surviving runner with descendant settlement
- [ ] Profile frozen and recorded

Only when all of the above holds **and** the runtime composition and authoritative
verifier are implemented does `tbound serve --pi` have a path to admission. Provisioning
alone does not enable it: this runbook and the read-only checker are advisory, and the
runtime verifier — not a probe — decides admission.

> **Uncorroborated / UNVERIFIED:** no real D09 host (rootless Podman/crun, offline
> Cosign, local OCI layout, delegated cgroup) was available in the increment that
> added this tooling. The probe/refusal and command-construction paths are
> unit-tested with an injected runner, and an opt-in real test
> (`go test -tags d09integration ./internal/piruntime ./cmd/tbound`) skips with
> the exact refusal when the environment is absent. A real provisioned launch
> remains externally UNVERIFIED and needs separate operator approval; nothing
> here establishes containment or G1.
