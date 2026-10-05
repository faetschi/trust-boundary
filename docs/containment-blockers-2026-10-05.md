# Slice 5: frozen containment blocked (2026-10-05)

**Not implemented/passed. Containment: `not-established`; G1: `false`.**

Requirements inspected: thesis technical `06-4-sandboxing-decision.md`
“Frozen implementation profile” and offline authorized-signer paragraph, `07-1-runtime-flow.md`
§§1,6,9 (signed in-image entrypoint, static launcher-plus-target seccomp, verified
Landlock, surviving lifecycle owner), process §3.1 and recovery §4.1. Individual
capability successes or digest-only image pinning cannot satisfy this profile.

## Independently observed maintenance prerequisites

Pinned-key SSH to the guest rediscovered by MAC `00-15-5D-0C-40-00` returned exit
0 on 2026-10-05. ED25519 host-key enforcement, dedicated known_hosts,
HostKeyAlias `172.19.207.142`, BatchMode, no agent/forwarding, and the authorized
maintenance identity were used. No credentials were displayed or transferred.

| Probe | Actual result |
| --- | --- |
| kernel | `6.8.0-146-generic` |
| guest maintenance UID | 1000 (`tboundadmin`) |
| `command -v cosign` | exit 1: absent |
| SSH process cgroup | `/user.slice/user-1000.slice/session-13.scope` |
| `test -w /sys/fs/cgroup/cgroup.kill` | exit 1: not writable |
| `kernel.apparmor_restrict_unprivileged_userns` | `1` |
| `sudo -n true` | exit 1: password required |

SSH exit 0 only confirms transport/maintenance access; it does not turn the
individual failing probes into passes. Root kill-file non-writability is not a
complete delegation inventory. A fixed delegated non-threaded runner root and
its effective limits/recursive settlement must still be established explicitly.

The delegated read-only capability investigation additionally returned exit 1
(12 PASS, 1 FAIL, 2 UNVERIFIED): Landlock ABI 4, pidfd-open and a harmless child
seccomp-filter probe succeeded; raw user-namespace mapping failed with EPERM;
current cgroup type was `domain`; kill-file writability remained unverified.
`podman info` reported rootless/crun and crun 1.14.1; no container was started.
These are mechanism probes, not the frozen entrypoint/static-profile/teardown
proof. ABI 4 cannot satisfy a profile requiring ABI ≥5 rights.

## Exact unlock and remaining implementation

1. Supply/review an offline signing toolchain, authorized **public** signer/key
   identity and policy, a signed image with the selected in-image entrypoint, and
   retained offline-verifiable signature material. Do not put private signing
   keys or provider credentials in the guest or repository.
2. Approve the narrow privileged setup (or provide a refreshed authorized sudo
   session) for the fixed non-threaded cgroup-v2 runner subtree with writable
   `cgroup.kill`, enforced bounds and verified recursive `populated=0`. No
   persistent broad sudo exception or AppArmor relaxation is authorized here.
3. Implement/freeze the single entrypoint, exact static seccomp source/digest,
   generated OCI identity, Landlock rights and process-wide application, pidfd
   behavior, descendant/mount settlement and surviving authoritative observer.
4. Freeze the entire VM/filesystem/kernel/runtime/image/mapping/profile identity
   and pass the required evaluation-guest conformance/fault evidence before G1.

No signing tool was installed, key authorization invented, cgroup hierarchy
modified, UAC/sudo prompt raised, image replaced, baseline restored, or guest
security restriction weakened. Existing Podman/WSL successes remain bounded
non-claim-bearing evidence, not substitutes for these requirements.
