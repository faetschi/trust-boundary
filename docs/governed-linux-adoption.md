# Governed TBound on Linux: adoption and provisioning plan

**Status:** researched plan, not completed provisioning or a production launch
receipt. No machine settings, VM, keys or container installation were changed.
The current production-profile/launcher increment must pass code review and real
registered-host tests before a provisioned launch is claimed.

## What “easy to adopt next to Pi” should mean

1. **Install the application without altering global Pi/Node.** Use the local
   release artifacts or reviewable packages; keep the pinned runtime side-by-side
   under TBound's managed prefix. Existing Pi may be detected for compatibility,
   but its mutable configuration/extensions cannot silently become an admitted profile.
2. **Run the existing `tbound-doctor`.** Show application prerequisites separately
   from governed-host prerequisites, with precise fixes. Doctor is advisory and
   cannot grant admission; “files present” is not a valid signed/contained profile.
3. **Make privileged setup a separate, explicit operator step.** Present a dry-run
   list of packages, delegation settings, trust material and destinations before
   the operator approves changes. Do not change Secure Boot, AppArmor, the kernel,
   VM state or system services implicitly during normal install/launch.
4. **Install one reviewed offline profile bundle.** Include image/runtime identities,
   public verification material, entrypoint/static policies and protected host profile.
   Signing keys and provider credentials never enter Pi or command cells. A local
   self-signed file is not automatically trusted just because its signature verifies.
5. **Launch normally once qualified.** The intended outcome is `tbound serve --pi`
   selecting its protected installed default profile and launching actual Pi. Invalid,
   stale or incomplete profiles refuse with actionable diagnostics, not a raw-host,
   Docker or development fallback. Updates invalidate the prior admission evidence.

There is no verified public `get.tbound.dev` service or published npm namespace
in this work. Do not present example download channels as available installation.
Development launch is useful but does not bypass these governed requirements.

## Candidate host choices

The thesis requires a dedicated pinned Linux evaluation environment; WSL is for
development, not the sole confirmation host (D10).

**User selection (2026-10-09): target the existing Hyper-V Ubuntu VM.** This is
approval of the setup plan only, not permission to install packages, update its
kernel, start/stop/reset the VM, change networking, or install trust anchors.
Inspect and preserve its current work/baseline before a separately authorized
setup. Prefer an appropriate supported HWE update if needed; do not automatically
replace it with a new VM or change Windows, Secure Boot or AppArmor.

| Candidate | Evaluation |
|---|---|
| Existing Ubuntu 24.04 VM with an appropriate supported Canonical HWE kernel | Least disruptive candidate; requires operator approval for kernel changes, runtime ABI measurements and a fresh profile freeze |
| Dedicated Ubuntu 26.04 LTS VM | Newer candidate; Canonical currently lists kernel 7.0 GA, but creating/replacing a VM is a separate operator decision |
| Current WSL setup | Real Pi SDK/development tests work; the recorded inventory lacks the declared Podman/crun/Cosign and delegation profile. Not a production/confirmation fallback |

Neither candidate is production-qualified by this research. Keep the user's
Windows 10/Hyper-V constraints intact and do not replace or disrupt the existing
VM without explicit approval. The old 6.8/ABI-4 guest observations are historical,
not a fresh guest inspection.

The normative floor remains the registered Landlock rights and ABI, including
the documented ABI >=5 requirement. Official kernel documentation says thread
synchronization is available from ABI 8; below that, a proven single-threaded
native entrypoint is a possible design. Choosing ABI 8/TSYNC is a profile/source
freeze decision, not an automatic amendment to the thesis. Measure the actual
ABI and effective restrictions; never infer admission from a kernel version.

**Current implementation choice:** the Go cell entrypoint uses ABI 8 TSYNC and
requires the signed template to request ABI >=8. Thus this implementation needs
a measured ABI-8-capable kernel on the selected VM. Its WSL ABI-1 enforcement
test skips/refuses as unsupported; it does not prove a qualified launch. This is
a specific implementation-profile requirement, not a change to the thesis's
general ABI >=5 floor or approval to update the VM/kernel.

## One-time operator prerequisites

- Rootless UID/subuid/subgid setup and pinned Podman/crun binaries.
- A fixed, non-threaded delegated cgroup-v2 subtree with the registered controllers,
  resource bounds, writable recursive kill and observable population/teardown.
  Inspect a live approved scope; checking the filesystem root or a scope that has
  already exited is not proof of delegation.
- Authorized signed cell image and one trusted in-image entrypoint; immutable
  runtime/Pi resources and static seccomp/Landlock policies bound to the profile.
- Offline public-key image verification. Sigstore documents an on-disk image saved
  with `cosign save`, verified using `cosign verify --key ... --local-image ...`.
  Test zero-network behavior and bind the verified content to the local image that
  Podman actually starts; an “offline” flag alone is not this evidence.
- Protected profile and trust-anchor installation, with signature, file ownership,
  parent-directory and immutable source checks performed by the actual verifier.
- The current FD3 executable model additionally requires the profile-pinned Node
  executable on the host, with protected ownership and an immutable read-only
  mount. Node inside the image alone does not satisfy this requirement. Install
  the admitted copy side-by-side; do not silently replace global Pi or Node.
- A registered managed publication root, owned by a dedicated local non-login
  writer identity under a root-owned direct parent, with the protected service,
  mount and service-cgroup policy required by the verifier. Its fixed registration
  is `/etc/tbound/managed-live-root.json` with `.ed25519` signature; the signed D09
  template binds `managed_live_root_registration_digest`. The profile tool exposes
  `managed-root-measure`, `managed-root-sign` and `managed-root-verify`; these are
  source implementation contracts pending combined review, not evidence that this
  host has been provisioned. Normal user-prefix installation does not create this
  authority, service account or trust material.
- Surviving runner, reset/recovery procedure and independent process/filesystem/
  network observations. `ready` from the untrusted harness is not containment proof.

These are proposed setup duties, not permission to run sudo, install packages,
operate the VM, generate signing keys or invoke a real provider now. A separately
authorized setup must keep Secure Boot and existing AppArmor restrictions intact;
use the supported confined runtime rather than disabling user-namespace protection.

Managed-root registration authenticates and checks the workspace/writer policy;
it is not a publication permit or a measurement of current quiescence. The trusted
runner must hold its root/run/epoch-bound writer lease, close admission, drain
registered writers and verify worker/command settlement before issuing a one-use
publication permit. That permit remains held through durable finalization. A
same-UID developer directory and an advisory lock alone cannot establish the
exclusive-writer property; any future cooperative-workspace mode must say so
explicitly rather than weakening the governed profile silently.

## Release acceptance

Keep three tests separate:

1. Unit tests with injected commands prove argument construction and negative paths.
2. Missing-prerequisite tests prove safe refusal and no worker/effect launch.
3. An opt-in registered-host test must actually start pinned Pi in the approved cell,
   reconcile broker/tool/generation lineage and durable publication, observe enforced
   boundaries, and settle descendants or retain UNKNOWN with recovery ownership.

Only the third can establish a provisioned governed launch. D06 provenance,
worker/admin separation and bash-lease publication remain part of the integrated
contract; a successful image verification or process spawn alone is not completion.

## Primary sources checked

- [Thesis deployment/runtime design](../../MSE_MA_Thesis/agentic-harness/tbound-thesis.md)
  and AH-technical D09/D10/D11, sandboxing decision and runtime-flow contracts.
- [Canonical supported-kernel table](https://ubuntu.com/security/livepatch/docs/client/reference/platform/supported-kernels)
  and [Ubuntu 26.04 release announcement](https://canonical.com/blog/canonical-releases-ubuntu-26-04-lts-resolute-raccoon).
- [Landlock documentation](https://docs.kernel.org/userspace-api/landlock.html).
- [systemd delegation](https://systemd.io/CGROUP_DELEGATION/)
  and [rootless cgroup setup](https://rootlesscontaine.rs/getting-started/common/cgroup2/).
- [Sigstore image verification](https://docs.sigstore.dev/cosign/verifying/verify/).

Public documentation was checked on 2026-10-08. These sources establish supported
design options, not the current configuration or measured safety of our VM.
