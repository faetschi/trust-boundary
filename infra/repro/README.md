# Reproducible evaluation-environment bootstrap

These scripts support three distinct paths. **Native Linux is first**; a VM is
optional for tests, not a prerequisite.

1. **Native Linux (recommended, no VM)** — Ubuntu 24.04 amd64, bare metal or any
   hypervisor: `bootstrap-linux.sh` prepares the host and runs the capability
   probe. This is the shortest path for a fresh PC that already runs Linux.
2. **Windows WSL2 (development only)** — an Ubuntu 24.04 WSL2 distro is useful
   for the CLI/fixtures, but it is **not** the governed qualification profile.
   WSL2 does not establish the D09 cgroup/Landlock/KVM boundary; use the WSL
   probe mode only as a development smoke.
3. **Linux VM (optional, for tests/qualification)** — only when a VM is wanted:
   - **Hyper-V on Windows:** `bootstrap-hyperv-windows.ps1` creates the VM from
     a generic install VHDX or an existing VHDX, enables SSH key auth, and runs
     the same guest bootstrap.
   - **QEMU on Linux (including a Linux host with no usable KVM):**
     `bootstrap-qemu-linux.sh` creates an Ubuntu 24.04 cloud-image VM, runs the
     guest bootstrap, and installs/smoke-tests TBound. KVM is used when usable;
     otherwise TCG is used with a clear performance warning.

Do not start with the VM path on a native Linux host. The VM path is for tests
that need a disposable guest or a separately measured kernel; it does not make
WSL2 a governed profile.

`probe-host.ps1` turns `infra/guest/probe-capabilities.sh` (reused by path,
never modified) into a one-command capability report for any of those hosts,
with a plain-language "what to fix" footer.

Everything here is reproducibility infrastructure for the thesis evaluation
profile. It is not production hardening, and a passing probe is a capability
snapshot — not containment evidence and not G1 (see
[../../docs/reproducible-setup.md](../../docs/reproducible-setup.md)).

Bootstrap prepares a candidate host and records capability results; a zero
exit does not register or qualify a clean guest, prove containment, or
establish G1.

These are new setup scripts: syntax and fixture checks have passed, but neither
path has been end-to-end qualified on a clean host. Treat them as review-needed
prototype tooling until that qualification is completed.

If you already use the reference VM `TBound-Ubuntu-2404`, resume it with the
[repository quickstart's Windows steps](../../README.md#existing-windows-hyper-v-vm-start-connect-resume).
Do not run the VM bootstrap for it: that script creates a new VM and stops if
the default VM name already exists.

## Prerequisites

- **Path 1 (native Linux):** Ubuntu 24.04 (Noble) amd64 with root access (sudo)
  and network access for provisioning downloads. Nothing else.
- **Path 2 (Windows WSL2, development only):** Windows 10/11 with WSL2 and an
  Ubuntu 24.04 distro. This is a development smoke path, not a governed
  qualification environment.
- **Path 3 (Linux VM, optional):**
  - **Hyper-V:** Windows 10/11 or Windows Server with Hyper-V enabled and its
    PowerShell module; the OpenSSH client (`ssh.exe`, optional feature); a
    default WSL2 distro (used only for offline SSH-key injection); a generic
    install VHDX prepared from authenticated Ubuntu Server media plus its
    SHA-256. See [CLEAN-GUEST-QUALIFICATION.md](CLEAN-GUEST-QUALIFICATION.md).
  - **QEMU:** Linux amd64 with `qemu-system-x86`, `qemu-img`,
    `cloud-image-utils`, `curl`, and OpenSSH. No sudo is required on the host;
    the guest bootstrap uses the cloud-init account's passwordless sudo.
- **All paths:** the `infra/guest/probe-capabilities.sh` script from this
  repository (the bootstrap resolves it relative to its own location, so copy
  the whole `infra/` tree, not just `infra/repro/`).

## Path 1: native Linux (recommended, no VM)

```sh
# from the checkout (or after copying infra/ to the host)
sudo bash infra/repro/bootstrap-linux.sh <trial-user>       # interactive confirmations
TBOUND_ASSUME_YES=1 sudo -E bash infra/repro/bootstrap-linux.sh <trial-user>   # unattended, no userns sysctl

# Only on a dedicated disposable host after reviewing the system-wide effect:
TBOUND_APPLY_USERNS_SYSCTL=1 TBOUND_ASSUME_YES=1 sudo -E \
  bash infra/repro/bootstrap-linux.sh <trial-user>
```

Do not set `TBOUND_ASSUME_YES=1` on a daily-driver or shared host: it accepts
the printed plan, including system-wide host changes. Use unattended mode only
for a dedicated disposable evaluation machine after reviewing the script.

What it does, in order, and what each step verifies:

1. **Trial account and subordinate ID ranges** — verifies or creates the
   non-root trial user and a 65536-ID subordinate UID/GID range (rootless
   Podman fails without it). Idempotent: existing ranges are kept.
2. **Podman, crun, pinned toolchains** — if the pinned Go 1.27.1 / Node
   v24.21.0 toolchains under `/opt/tbound/toolchains/` are missing, it
   delegates to the guarded provisioner `infra/guest/setup-ubuntu-24.04.sh`
   (signature-checked APT, pinned archive hashes, package holds). If they are
   already present (e.g. a copied evaluation VM disk), it verifies them and
   only checks that Podman/crun/uidmap are installed.
3. **Cosign** — not packaged for Ubuntu 24.04; installs the pinned static
   binary `v3.1.3` (SHA-256 pinned in the script) under
   `/opt/tbound/toolchains/` with a `/usr/local/bin/cosign` symlink.
4. **Unprivileged user namespace policy** — Ubuntu 24.04 sets
   `kernel.apparmor_restrict_unprivileged_userns=1`, which breaks rootless
   user namespaces (`write /proc/self/uid_map: Operation not permitted`).
   The bootstrap leaves this host-wide setting unchanged by default. Only an
   explicit `TBOUND_APPLY_USERNS_SYSCTL=1` opts in to writing
   `/etc/sysctl.d/99-tbound-userns.conf` and setting the sysctl to 0. Do not
   treat that unconfined workaround as governed-profile compatible; prefer a
   scoped AppArmor profile on shared or daily-driver hosts, and use it only on
   a dedicated disposable evaluation machine.
5. **cgroup v2 and Landlock prechecks** — verifies a cgroup v2 mount and
   reports broad Landlock availability plus a separate exact D09 profile
   result. Production source freezes D09 to ABI 8
   (`LANDLOCK_RESTRICT_SELF_TSYNC`); other positive ABI values remain generic
   capability evidence only.
6. **Capability probe** — runs `infra/guest/probe-capabilities.sh` as the
   trial user and prints a plain-language fix footer for any FAIL.

Exit codes: `0` complete with no FAIL and no unexpected UNVERIFIED; `1` any
probe FAIL (this keeps precedence over UNVERIFIED); `2` precondition error;
`3` the sole FAIL is a measured ABI mismatch with the exact D09 ABI 8 profile,
with no unexpected UNVERIFIED; `4` unexpected UNVERIFIED when there is no
FAIL. HWE may provide ABI 8 on an older GA installation, but the live ABI must
be measured after reboot. `cgroup.kill-write` and `uidmap-helpers` are
UNVERIFIED by design of the read-only probe.

## Path 2: Windows WSL2 (development only)

WSL2 is a convenient development host for the CLI and fixtures, but it is **not**
the D09/G1 qualification profile. Use the read-only probe in WSL mode for a
development smoke:

```powershell
.\infra\repro\probe-host.ps1 -Target Wsl -Distro <distro>
```

For a local TBound build inside WSL, follow the native Linux build/install steps
only if the distro has a usable cgroup v2 mount; otherwise use WSL for source
editing and run TBound in native Linux or the optional VM path. Do not present a
WSL probe pass as governed qualification, and do not apply the host-wide
AppArmor/userns workaround merely to make WSL pass.

## Path 3: Linux VM (optional, for tests/qualification)

Use one of the following only when a disposable guest is actually wanted.
Native Linux remains the first choice.

### QEMU on Linux (Linux VM path)

```sh
# from the checkout; the host needs QEMU, cloud-image-utils, curl, and OpenSSH
bash infra/repro/bootstrap-qemu-linux.sh --vm-dir "$HOME/.local/share/tbound-vm"
```

The script downloads and SHA-256 verifies the Ubuntu 24.04 cloud image, creates
a cloud-init seed and a 40 GiB overlay, boots the VM with KVM when usable (TCG
otherwise), runs `bootstrap-linux.sh`, installs the pinned Pi CLI, installs
TBound from source (or from `--local-dist`), and runs `tbound-doctor` plus the
native fixture. The default is the GA kernel and a light install: no adapter
check, no Go race, and no `make verify`. `--developer` opts into those checks;
`--hwe` opts into the ABI-8 HWE kernel, which is currently unqualified.
AppArmor/user-namespace relaxation is also opt-in
(`--allow-userns-sysctl`). The script stores the VM, SSH key, console log, and
guest provisioning logs under `--vm-dir`.

Re-running with `--start`, `--stop`, `--ssh`, or `--status` manages the existing
VM. The default memory is 3 GiB because TCG hosts are often memory-constrained;
the script adds a 2 GiB guest swapfile. User-prefix installation is independent
of capability/qualification results: a failed probe is reported but does not
undo the install. The signed host profile, D09 image, and managed service still
require the operator signing session.

### Hyper-V on Windows (Linux VM path)

```powershell
# elevated PowerShell, from the checkout; use a generic VHDX prepared from
# authenticated Ubuntu Server installation media
.\infra\repro\bootstrap-hyperv-windows.ps1 `
  -ImageKind GoldenVhdx `
  -ImageSourcePath '<verified generic Ubuntu Server install VHDX>' `
  -ExpectedImageSha256 '<64-hex-sha256-of-the-seed-vhdx>' `
  -PublicKeyFile $env:USERPROFILE\.ssh\id_ed25519.pub `
  -PrivateKeyFile $env:USERPROFILE\.ssh\id_ed25519
```

What it does, in order:

1. **Preflight** — checks Hyper-V and its module, admin elevation, VM name
   and target directory collisions, free space, and the image SHA-256.
2. **VM creation** — the current bootstrap configuration is Generation 2,
   8 vCPUs, 16 GiB fixed memory, 40 GiB disk, Secure Boot with the Microsoft
   UEFI Certificate Authority template, and one network adapter on the named
   switch. These are configuration values, not a qualified or registered
   resource profile; record the actual VM settings used for an evaluation.
3. **Offline injection (WSL2)** — with the VM off, it bare-mounts the disk via
   `wsl --mount --vhd <path> --bare`, grows the root filesystem (cloud images),
   creates the trial user with your SSH public key and provisioning sudo, and
   (for GoldenVhdx) replaces installer Netplan files with DHCP matched to the
   Hyper-V network driver, regenerates SSH
   host keys, and regenerates the machine-id. It never combines `Mount-VHD`
   and `wsl --mount` on the same file.
4. **Boot and verify SSH** — resolves the guest IP, prompts for the ED25519
    fingerprint shown by `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256`
    in VMConnect (or accepts `-GuestHostKeyFingerprint`), checks the scanned
    key against it, then copies
    `infra/repro/bootstrap-linux.sh`, `infra/guest/setup-ubuntu-24.04.sh`, and
    `infra/guest/probe-capabilities.sh` to the guest and runs the same Path 1
    bootstrap inside the guest over SSH.
    The single PowerShell confirmation approves the complete VM creation,
    offline injection, and unattended guest bootstrap plan. The guest bootstrap
    does not ask again before package, toolchain, or sysctl changes.
    The injected `NOPASSWD` sudo rule is temporary: successful provisioning
    removes it automatically. If bootstrap fails before completion, remove
    `/etc/sudoers.d/90-tbound-<trial-user>` before untrusted evaluation; the
    trial account is not a separate root boundary.
5. **Report** — prints next steps for recording the candidate, disconnecting
   the NIC, freezing the measured disk, and running the offline checks. These
   steps do not register or qualify the candidate.

Windows host-tool verification is portable across Windows builds: the script
requires the fixed `System32`/`OpenSSH` paths, TrustedInstaller ownership, and a
valid Microsoft Authenticode signature. It no longer embeds one host's
`wsl.exe`/`cmd.exe`/OpenSSH hashes. Pass `-WindowsToolManifestPath` with a JSON
object of `{ "tool.exe": "<64-hex sha256>" }` for an additional
operator-reviewed pin; a supplied manifest must cover every tool used and fails
closed on missing or unknown entries.

For the two Hyper-V image modes:

- **CloudVhdx** — compatibility mode for cloud-layout VHD/VHDX inputs. Ubuntu's
  Azure VHD artifacts are Azure-only and do not function on local Hyper-V.
  Do not use the Azure `.vhd.tar.gz` artifact in this mode for a local guest.
  The mode is retained for inputs whose publisher explicitly supports local
  Hyper-V; the script warns that its SHA-256 check proves byte equality, not
  publisher support.
- **GoldenVhdx** — a copy of an existing verified VM disk. Pass the leaf
  `.avhdx` if the source VM has checkpoints: the script merges the chain into
  a standalone VHDX (`Merge-VHD`) before use, because a base `.vhdx` under an
  active checkpoint chain is stale state. For a local install, create a generic
  VHDX from supported authenticated media, then pass it through this mode.
  [CLEAN-GUEST-QUALIFICATION.md](CLEAN-GUEST-QUALIFICATION.md) describes input
  verification, the manual ISO seed procedure and qualification limits.

After provisioning, produce the capability report from Windows any time:

```powershell
.\infra\repro\probe-host.ps1 -Target Ssh -HostName <guest-ip> -UserName <trial-user> -KeyFile <key> -OutFile host-report.txt
.\infra\repro\probe-host.ps1 -Target Wsl -Distro <distro>        # development smoke only, not the D09/D10 profile
```

For SSH mode, first connect once with the quickstart SSH command, compare the
displayed host-key fingerprint with the value on the guest console, and accept
it only if they match. `probe-host.ps1` requires that verified key in
`known_hosts`; it rejects unknown or changed host keys.

## Latest reference-VM observation

On 2026-10-12, the existing Ubuntu 24.04.5 Hyper-V guest ran kernel
`6.8.0-146-generic`. The fresh probe reported `PASS=13 FAIL=0 UNVERIFIED=2`:
user-namespace creation now passes, but Landlock is still ABI 4. The two
UNVERIFIED rows are `cgroup.kill-write` and `uidmap-helpers` (shown below).
The HWE 7.0 package is installed but is **not** the active kernel; boot behavior
has not been qualified. This is not the result of a clean bootstrap and must not
be presented as an expected clean-host result. Query and record the running ABI
and required TSYNC behavior; do not infer them from an installed or reported
kernel version.

```
UNVERIFIED   cgroup.kill-write: ... behavior is untested because writing kills processes
UNVERIFIED   uidmap-helpers: ... their mapping behavior is not inferred from presence
```

The probe process returns nonzero when UNVERIFIED rows exist. The bootstrap
distinguishes the two expected UNVERIFIED checks from additional ones; read the
summary rather than treating the raw exit code alone as a capability result.

## Files

- [CLEAN-GUEST-QUALIFICATION.md](CLEAN-GUEST-QUALIFICATION.md) — candidate
  image identity and offline qualification worksheet; not a G1 registration.

- `bootstrap-linux.sh` — native Linux bootstrap (idempotent, fail-closed).
- `bootstrap-qemu-linux.sh` — Linux-host QEMU VM creation and guest provisioning.
- `test_bootstrap_package_status.sh` — regression for held-package detection.
- `test_windows_tool_manifest.ps1` — mock test for the portable Windows tool
  manifest parser (runs without inspecting Windows binaries).
- `bootstrap-hyperv-windows.ps1` — Hyper-V VM creation and guest provisioning.
- `probe-host.ps1` — one-command capability report (SSH / WSL / saved capture).
- [DISPOSABLE-CGROUP-TEARDOWN.md](DISPOSABLE-CGROUP-TEARDOWN.md) — operator procedure and trust boundary for the destructive `cgroup_teardown_witness.py` component probe; not qualification evidence.

The full guide with step counts, expected duration, troubleshooting table, and
the G1 profile-freeze mapping is
[../../docs/reproducible-setup.md](../../docs/reproducible-setup.md).
