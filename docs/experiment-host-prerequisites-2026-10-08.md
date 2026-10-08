# Experiment-host prerequisite inventory — 2026-10-08

**Observed:** 2026-10-08 09:30–09:35 CEST (UTC+02:00), current developer
checkout and local Windows/WSL metadata only. This is an environment inventory,
not a VM/guest test, frozen-profile admission, G1 result, or authorization to
install/configure/run privileged infrastructure.

## Current local capability snapshot

| Item | Read-only observation | What it establishes / does not establish |
|---|---|---|
| Windows Node | `C:\Program Files\nodejs\node.exe`, Node `v24.15.0`; WSL's Windows-backed `npm --version` reported `12.0.2`. | The Windows developer Node is available and meets the adapter manifest's `>=22.19.0` engine constraint. It is **not** a Linux child-runtime or evaluation-host artifact. |
| WSL Linux Node | Ubuntu 24.04, kernel `5.15.153.1-microsoft-standard-WSL2`; a regular executable exists at `/home/jeli2k/node-v24.15.0-linux-x64/bin/node`, reporting `v24.15.0`. `command -v node` is absent. The WSL `npm` command resolves to `/mnt/c/Program Files/nodejs/npm` (Windows). | A local Linux Node binary exists, so “Linux Node absent” is not an accurate blanket statement. It is not on the normal WSL command path and no artifact digest/signature/profile binding was checked. Do not use the WSL Windows-backed `npm` path as the Linux production runtime. |
| WSL Node dependency assets | The scoped `node-v24.15.0-linux-x64` distribution exposes only global `npm` and `corepack` roots. Exact expected global Pi/typebox/TypeScript package paths were absent. No `node-sdk` directory was found in the approved `/home/jeli2k` name/depth search. | This does **not** establish that project dependencies are absent elsewhere: only the requested approved roots and exact paths were inspected. No package installation, registry request, or broad cache/home scan was performed. |
| Repository Node pins | `adapter/package.json` requires Node `>=22.19.0`; `package-lock.json` pins `@earendil-works/pi-coding-agent`, `pi-ai`, and `pi-tui` to `0.87.1`, `typebox` to `1.3.27`, `@types/node` to `22.19.19`, and TypeScript to `5.9.3`, with npm lockfile v3 integrity entries. | Lock metadata is not proof the packages are present, trusted as executable artifacts, or part of an admitted Linux child profile. The current Node bridge remains a different concern from frozen OS containment. |
| Windows Go | `C:\Program Files\Go\bin\go.exe`, `go1.27.1 windows/amd64`. | Windows development/build tool only; no cross-platform profile conclusion. |
| WSL Go | `/home/jeli2k/go-sdk/go/bin/go`, `go1.27.1 linux/amd64`; repository `supervisor/go.mod` declares language version Go 1.21 and `github.com/gowebpki/jcs v1.0.1`. Go reported module-cache path `/home/jeli2k/go/pkg/mod`; its contents were not enumerated. | Local Linux Go is available. No broad module-cache inspection, module download, or full experiment test was performed. |
| WSL user and filesystems | Ubuntu 24.04 WSL2, UID/GID `1000:1000` (`jeli2k`); supplementary groups include `sudo` and `docker`. `/home/jeli2k` is ext4, mode `0700`; `/tmp` is ext4, mode `1777`; `TMPDIR` is unset. Checkout `/mnt/c` is a `9p`/DrvFs mount. | A private ext4 workspace is available under the mode-0700 home, but default `/tmp` is shared and no private temp was configured for this inventory. Group membership is not proof of a safely unprivileged security boundary; no `sudo`, Docker/Podman daemon, or host privilege operation was attempted. For reproducible future Linux tests, use a private ext4 source snapshot and mode-0700 `TMPDIR`, not the DrvFs checkout/default shared temp. |
| WSL cgroups | `/proc/self/cgroup` places this process in `/init.scope`. A cgroup-v2 mount exists at `/sys/fs/cgroup/unified` (`rw` mount), but its directory is root-owned mode `0555`; its `cgroup.controllers` and `cgroup.subtree_control` were empty, root `cgroup.type`, `cgroup.events`, and `cgroup.kill` are absent. `/init.scope` is `domain`, reports `populated 1`, and has no `cgroup.kill`; checked root/scope paths are not writable. | No fixed delegated TBound subtree, usable controllers, or writable recursive kill interface was found. This WSL state does not meet the frozen cgroup-v2 delegation requirement. No cgroup was created, written, killed, or otherwise changed. |
| Podman / crun / Cosign | `podman`, `crun`, and `cosign` are absent from Windows and Ubuntu command paths; no matching binaries were found in `/usr/bin`, `/usr/local/bin`, or `/opt/tbound` to depth 3. `/opt/tbound` is absent. WSL's distro listing included a `podman-machine-default` registration, which was not entered or queried. | A registered distro name is not evidence of an installed usable CLI, running daemon, rootless Podman/crun profile, image, or signature. No daemon/container/version query was sent and no container was launched. |
| Kernel mechanisms | No Landlock ABI, pidfd, namespace-map, static seccomp, or network-containment capability probe was run. The WSL kernel identity is above. | These remain **unverified** here. A kernel version string, mounted cgroup2 filesystem, or unprivileged UID cannot establish the required composition. |

## Thesis profile and current implementation boundary

The normative sources are the sibling thesis files, not this environment:

- `MSE_MA_Thesis/agentic-harness/AH-technical/10-decision-register.md`
  records D06, D09, D10, and D11.
- `.../06-4-sandboxing-decision.md` is accepted, lists D09/D11, and was last
  reviewed 2026-09-28.
- `.../07-1-runtime-flow.md` specifies preflight, the signed/profile-bound
  entrypoint, broker isolation, cell teardown, and the evaluation runner.

| Decision | Frozen requirement | Current inventory relation |
|---|---|---|
| D06 | Quiescent filtered/verified byte snapshot into a newly initialized private repository; reject shared Git object stores/worktrees, live `.git`, alternates, mounts, special files, and unsafe links. | D06 is a separate staging-independence requirement, not a substitute for an admitted Linux host. This inventory did not run D06 snapshot/session tests or inspect session-owner private data. |
| D09 | One frozen rootless Podman + crun profile; image digest plus offline Cosign verification with an authorized public key; freeze VM/filesystem/kernel, Podman/crun, rootless mapping, image/signature and generated OCI identities; no pulls/profile drift; fail closed. Docker, WSL, and alternate runtimes are not fallbacks. | Current WSL lacks Podman/crun/Cosign binaries in the scoped checks and does not match the dedicated frozen VM profile. |
| D10 | A dedicated pinned Linux VM for confirmatory runs; WSL2 is development/smoke only. Image identity, kernel config, snapshot/reset procedure, no shared folders/clipboard, and uplink capture belong to each trial. | This session remains on Windows + WSL2. It is not the confirmatory host. No VM/guest was queried or operated. |
| D11 | Namespaces, delegated non-threaded cgroup v2, Landlock, one static seccomp-BPF profile, `no_new_privs`, pidfd and `cgroup.kill`; crun and signed entrypoint verify identity/postconditions; a surviving runner owns crash cleanup/reconciliation. | WSL metadata lacks the required writable delegation and required binaries/profile identity; none of the other mechanisms was proven. |

The checked-in working-tree note `docs/native-runtime-composition.md` describes
the Go `piruntime.Supervisor` seam and Linux fixture as non-claim-bearing. It
states that production `serve --pi` refuses without the reviewed host profile,
and that no production host verifier/launcher, trusted descendant settler, or
broker-owned real continuation is supplied by that fixture. This is a source
documentation statement, not an independent build/test receipt. The adapter
manifest provides useful Node/Pi pins but no authority to cross the OS boundary.

## Historical guest evidence is not current state

The last guest capability facts I found are in
`docs/containment-blockers-2026-10-05.md`, dated 2026-10-05—not freshly queried
on 2026-10-08. That report recorded guest kernel `6.8.0-146-generic`, maintenance
UID 1000, Cosign absent, `cgroup.kill` not writable, AppArmor's unprivileged
userns restriction set to `1`, noninteractive sudo requiring a password, and a
user-namespace mapping failure. It also recorded Landlock ABI 4 and rootless
Podman/crun (crun 1.14.1) as mechanism observations; no container was started.
Those observations do not satisfy D09/D11 and may have drifted. Older
`docs/implementation-status.md` VM-running/VFH statements are also historical;
they are not an Oct 8 VM-state assertion.

No current Hyper-V VM state, scheduled task, helper task, SID, guest, SSH
endpoint, or controller was queried. No saved `vm_start.txt` or credential
material was opened. No source-controller command was run, including a
policy-only action.

## Safe unprivileged work versus operator prerequisites

### Safe to continue without host-privilege authorization

- Develop typed Go/Node bridge and D06/session interfaces with synthetic,
  non-claim-bearing tests; preserve the fixed D06/D09/D11 semantics and make
  production admission fail closed when any identity/capability is missing.
- Use Windows Node for Windows-only compatibility checks. For Linux adapter
  checks, invoke the explicit Linux Node binary only with already-present,
  verified local dependencies; the current scoped inventory did not establish
  those Pi dependencies. Never let WSL silently resolve `node`/`npm` to Windows
  for a production Linux launch.
- Run pure Go/unit tests in WSL only from a private ext4 source snapshot with a
  private mode-0700 temp directory, and describe the result as development
  evidence. No such tests were run as part of this inventory.
- Implement read-only preflight/reporting and missing-profile refusals. A
  mechanism probe is not the frozen profile and cannot set G1 true.

### Requires separate explicit operator authorization and a declared evaluation host

1. Select and authorize the **specific dedicated Linux evaluation VM** and its
   exact image/filesystem/kernel/config identity, snapshot/reset method, storage
   budget, isolation boundary, and uplink/network-capture policy. WSL2 is not a
   substitute.
2. Authorize staging the exact reviewed/pinned Linux Node/Pi/adapter artifacts
   if required by the chosen runtime, with digests and provenance; authorize
   rootless Podman/crun builds and the Cosign verifier/public-key identity. Supply
   an offline-signed cell image and public verification material. Do not place
   private signing keys or provider credentials in the guest.
3. Approve a dedicated rootless UID/GID mapping and a **fixed, non-threaded,
   delegated cgroup-v2 subtree** with the required controllers, bounded
   resources, writable `cgroup.kill`, observable `cgroup.events: populated`, and
   recursive `populated=0` teardown. Authorize only the narrow host setup needed
   for this subtree; no broad persistent sudo exception or security-policy
   relaxation is inferred.
4. Freeze and independently verify the exact generated OCI config, static
   seccomp profile source/digest, Landlock ABI/rights, namespaces,
   `no_new_privs`, pidfd behavior, signed in-image entrypoint, executable/argv/
   cwd/environment identity, and rootless runtime/image identity. Any mismatch
   must refuse admission rather than fall back to Windows Node, WSL, Docker, or
   another runtime.
5. Authorize the surviving evaluation runner and its fixed lifecycle roots to
   own TBound, Pi, and every cell; verify descendant termination, mount cleanup,
   crash reconciliation, and explicit UNKNOWN/failure oracles under fault
   injection. Keep broker credentials outside Pi/cells and require separate
   operator approval for any genuine provider exchange.
6. Freeze the manifest and run the declared conformance/evaluation suite on that
   exact profile, retaining source, image, runtime, observer, and result hashes.
   Do not restore/reset or start a trial until its specific procedure is
   authorized.

These are prerequisites, not actions performed or a request to disclose secrets.
No signer, private key, provider authentication, guest access, VM operation,
network change, installation, container launch, task transition, elevation, or
profile change was attempted.

## Command and source record

Successful read-only queries (all exit 0 unless noted):

- Windows `Get-Command node/go` and `node --version` / `go version`:
  Node v24.15.0 and Go 1.27.1 at the paths above.
- WSL `id`, `uname`, `findmnt`, `stat`, bounded `find` under only
  `/usr/bin`, `/usr/local/bin`, `/opt/tbound`, and `/home/jeli2k` entries named
  `node-v*`/`node-sdk` to depth 3; cgroup file metadata/readability/writability;
  direct Linux Node/Go version commands; `command -v` for Podman/crun/Cosign.
- Read-only `Get-ChildItem` of the exact thesis technical directory's filenames,
  plus targeted reads of D06/D09/D10/D11, runtime-flow, native-runtime,
  adapter manifest/lockfile, and dated prerequisite reports.
- An ephemeral metadata-query shell script was written under the approved
  `C:\Users\Admin\AppData\Local\Temp\opencode` scratch root and removed after
  use. It did not modify repository or system state.

No Go/Node tests, installation, module download, cgroup mutation, container or
daemon operation, source-controller execution, guest/VM operation, provider
request, task query, or elevation was performed. Report path:
`docs/experiment-host-prerequisites-2026-10-08.md`.
