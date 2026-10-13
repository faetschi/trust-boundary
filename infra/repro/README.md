# Reproducible evaluation-environment bootstrap

These scripts make it simple for another researcher to host the tbound
evaluation Linux environment in two supported ways:

- **Path A — native Linux** (Ubuntu 24.04 amd64, bare metal or any hypervisor):
  `bootstrap-linux.sh` prepares the environment and runs the capability probe.
- **Path B — Windows Hyper-V VM** (targets the reference host profile):
  `bootstrap-hyperv-windows.ps1` creates the VM from an Ubuntu 24.04 cloud
  image or from a golden VHDX, enables SSH key auth, and runs the same
  `bootstrap-linux.sh` inside the guest.

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

- **Path A:** Ubuntu 24.04 (Noble) amd64 with root access (sudo) and network
  access for the provisioning downloads. Nothing else.
- **Path B:** Windows 10/11 or Windows Server with Hyper-V enabled and its
  PowerShell module; the OpenSSH client (`ssh.exe`, optional feature); a
  default WSL2 distro (used only for the offline SSH-key injection step);
  a generic install VHDX prepared from authenticated Ubuntu Server media, plus
  its SHA-256. See
  [CLEAN-GUEST-QUALIFICATION.md](CLEAN-GUEST-QUALIFICATION.md) for the
  artifact-verification and qualification guidance. The local ISO-to-VHDX
  procedure is being prepared separately; `GoldenVhdx` reuses an existing disk.
- **Both paths:** the `infra/guest/probe-capabilities.sh` script from this
  repository (the bootstrap resolves it relative to its own location, so copy
  the whole `infra/` tree, not just `infra/repro/`).

## Path A: native Linux

```sh
# from the checkout (or after copying infra/ to the host)
sudo bash infra/repro/bootstrap-linux.sh <trial-user>       # interactive confirmations
TBOUND_ASSUME_YES=1 sudo -E bash infra/repro/bootstrap-linux.sh <trial-user>   # unattended
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
4. **Unprivileged user namespace fix** — Ubuntu 24.04 sets
   `kernel.apparmor_restrict_unprivileged_userns=1`, which breaks rootless
    user namespaces (`write /proc/self/uid_map: Operation not permitted`).
    After an explicit operator confirmation it writes
    `/etc/sysctl.d/99-tbound-userns.conf` and applies
    `kernel.apparmor_restrict_unprivileged_userns=0`. This relaxes a system-wide
    AppArmor restriction; prefer a scoped AppArmor profile on shared or daily-
    driver hosts, and use the sysctl only on a dedicated evaluation machine.
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

## Path B: Hyper-V VM from Windows

```powershell
# elevated PowerShell, from the checkout; generic VHDX from authenticated media
.\infra\repro\bootstrap-hyperv-windows.ps1 `
  -ImageKind GoldenVhdx `
  -ImageSourcePath '<verified generic Ubuntu Server install VHDX>' `
  -ExpectedImageSha256 <64-hex-sha256-of-the-image> `
  -PublicKeyFile $env:USERPROFILE\.ssh\id_ed25519.pub
```

What it does, in order:

1. **Preflight** — checks Hyper-V and its module, admin elevation, VM name
   and target directory collisions, free space, and the image SHA-256.
2. **VM creation** — Generation 2, 8 vCPUs, 16 GiB fixed memory, 40 GiB disk
   (the verified VM profile), Secure Boot on with the Microsoft UEFI
   Certificate Authority template, one network adapter on the switch you name.
3. **Offline injection (WSL2)** — with the VM off, it bare-mounts the disk via
   `wsl --mount --vhd <path> --bare`, grows the root filesystem (cloud images),
   creates the trial user with your SSH public key and provisioning sudo, and
   (for golden copies) fixes the installer netplan MAC binding, regenerates SSH
   host keys, and regenerates the machine-id. It never combines `Mount-VHD`
   and `wsl --mount` on the same file.
4. **Boot and verify SSH** — resolves the guest IP, prompts for the ED25519
    fingerprint shown by `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256`
    in VMConnect (or accepts `-GuestHostKeyFingerprint`), checks the scanned
    key against it, then copies
    `infra/repro/bootstrap-linux.sh`, `infra/guest/setup-ubuntu-24.04.sh`, and
    `infra/guest/probe-capabilities.sh` to the guest and runs the same Path A
    bootstrap inside the guest over SSH.
    The single PowerShell confirmation approves the complete VM creation,
    offline injection, and unattended guest bootstrap plan. The guest bootstrap
    does not ask again before package, toolchain, or sysctl changes.
    The injected `NOPASSWD` sudo rule is temporary: successful provisioning
    removes it automatically. If bootstrap fails before completion, remove
    `/etc/sudoers.d/90-tbound-<trial-user>` before untrusted evaluation; the
    trial account is not a separate root boundary.
5. **Report** — prints next steps: disconnect the NIC, create the golden
   baseline, and re-run the probe offline per the guest runbook.

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
  verification and qualification limits; an ISO installation recipe is pending.

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
- `bootstrap-hyperv-windows.ps1` — Hyper-V VM creation and guest provisioning.
- `probe-host.ps1` — one-command capability report (SSH / WSL / saved capture).

The full guide with step counts, expected duration, troubleshooting table, and
the G1 profile-freeze mapping is
[../../docs/reproducible-setup.md](../../docs/reproducible-setup.md).
