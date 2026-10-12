# Reproducible evaluation-host setup (native Linux / Hyper-V)

This guide is the UX-facing entry point for the scripted reproducibility
infrastructure in [`infra/repro/`](../infra/repro/README.md). It tells a
researcher how to host the tbound evaluation Linux environment with as few
manual steps as possible, what to expect at each step, and how the result maps
to the thesis G1 profile freeze.

**Qualification status:** the setup scripts are new prototype tooling. Their
syntax and fixture checks passed, but they have not been run end-to-end on a
clean native Linux host or a newly created Hyper-V VM. The command paths below
describe the intended flow, not a validated one-command installation.

**Claim boundary.** Everything here is reproducibility infrastructure for the
evaluation profile. It is not production hardening. A completed bootstrap and a
passing capability probe are a *host capability snapshot* — they are **not**
containment evidence, do not admit a D09 profile, and do not establish G1
(`G1=false`, containment `not-established` remain the current state). The
offline evaluation phase itself is unchanged and is governed by
[`infra/guest/README.md`](../infra/guest/README.md).

**Already using the reference VM?** Resume the existing
`TBound-Ubuntu-2404` guest using the [Windows start/connect/resume steps in the
repository quickstart](../README.md#existing-windows-hyper-v-vm-start-connect-resume).
Do not run `bootstrap-hyperv-windows.ps1` for that VM: it creates a new VM and
intentionally stops if the default VM name already exists.

## What "reproducible" means here

The scripts reproduce the *inputs* the profile freeze needs, and one acceptance
check:

- The verified Hyper-V VM profile: Generation 2, 8 vCPUs, 16 GiB fixed memory,
  40 GiB maximum dynamic disk, Secure Boot on with the Microsoft UEFI
  Certificate Authority template, ProductionOnly checkpoints with automatic
  checkpoints disabled, exactly one network adapter (connected only during
  provisioning).
- The pinned runtime floor from [`infra/guest/setup-ubuntu-24.04.sh`](../infra/guest/setup-ubuntu-24.04.sh):
  rootless Podman + crun with `uidmap` helpers from signature-checked Ubuntu
  APT (versions held), pinned Node.js `v24.21.0` and Go `go1.27.1` under
  `/opt/tbound/toolchains/`, and 65536-ID subordinate UID/GID ranges for the
  trial account.
- Pinned Cosign `v3.1.3` (Cosign is not packaged for Ubuntu 24.04; the static
  release binary is installed with its SHA-256 pinned in the bootstrap).
- The Ubuntu 24.04 unprivileged-user-namespace fix
  (`kernel.apparmor_restrict_unprivileged_userns=0`), applied only after an
  explicit operator confirmation. This is a system-wide sysctl that relaxes an
  AppArmor restriction for unprivileged user namespaces; prefer a scoped
  AppArmor profile on a shared/daily-driver host, and use the sysctl only on a
  dedicated evaluation machine after reviewing that security trade-off.
- Acceptance check: `infra/guest/probe-capabilities.sh` (reused by path, never
  modified) reports `PASS` on every row, with only its two by-design
  `UNVERIFIED` rows remaining, and the running kernel reports Landlock ABI 8
  (`LANDLOCK_RESTRICT_SELF_TSYNC`).

## Choose a path

| Path | Command | Steps | Expected duration (estimates) |
|---|---|---|---|
| A: native Linux (Ubuntu 24.04 amd64) | 1 bootstrap command | 1 command + confirmations | 15–40 min (APT + pinned toolchain downloads dominate) |
| B1: Hyper-V VM from the Ubuntu 24.04 cloud image | 1 PowerShell command | 1 command (script does preflight → VM → offline injection → boot → guest bootstrap) | 45–75 min (image download ~809 MB, extraction/conversion, first boot, provisioning) |
| B2: Hyper-V VM from a golden VHDX | 1 PowerShell command | 1 command (merge/copy → offline injection → boot → guest bootstrap) | 15–30 min |

Path B1 has one notable advantage: the Ubuntu 24.04 *cloud image* ships the
`linux-azure` **7.0** kernel, which already reports Landlock **ABI 8** — the
entrypoint requirement is satisfied without a kernel upgrade. The golden VHDX
copied from the existing verified VM runs the GA `6.8` kernel (ABI 4) and needs
the HWE fix below.

## Path A: native Linux

```sh
sudo bash infra/repro/bootstrap-linux.sh <trial-user>
# unattended: TBOUND_ASSUME_YES=1 sudo -E bash infra/repro/bootstrap-linux.sh <trial-user>
```

The bootstrap is idempotent and fail-closed (exit `0/1/2/3/4`; every
confirmation is explicit, or `TBOUND_ASSUME_YES=1` accepts the printed plan).
Steps: trial user + subordinate ID ranges → Podman/crun/pinned toolchains
(delegating to the guarded provisioner when they are missing) → pinned Cosign →
user-namespace sysctl fix → cgroup v2 / Landlock prechecks → capability probe
as the trial user. The full log lands in `/var/log/tbound-repro-bootstrap.log`.

## Path B: Hyper-V VM from Windows

```powershell
# elevated PowerShell, from the checkout
.\infra\repro\bootstrap-hyperv-windows.ps1 `
  -ImageKind CloudVhdx `
  -ImageSourcePath <downloaded .tar.gz or .vhdx> `
  -ExpectedImageSha256 <64-hex SHA-256> `
  -PublicKeyFile $env:USERPROFILE\.ssh\id_ed25519.pub `
  -SwitchName 'Default Switch'
```

- **CloudVhdx**: download the dated Hyper-V-capable cloud image, for example
  `ubuntu-24.04-server-cloudimg-amd64-azure.vhd.tar.gz` from
  [cloud-images.ubuntu.com/releases/noble/release/](https://cloud-images.ubuntu.com/releases/noble/release/)
  (verified 2026-10-11: the archive holds a single 30 GiB VHD,
  `livecd.ubuntu-cpc.azure.vhd`, with the `linux-azure` 7.0 kernel). Verify the
  archive against the directory's signed `SHA256SUMS` (`SHA256SUMS.gpg`) per
  [Ubuntu's download verification guide](https://ubuntu.com/tutorials/how-to-verify-ubuntu)
  and pass the verified hash as `-ExpectedImageSha256`. The script extracts the
  disk, converts VHD→VHDX (Generation 2 requires the VHDX format), resizes it
  to 40 GiB, and grows the root partition plus filesystem offline during the
  injection step — the 24.04 cloud-image layout places the tiny BIOS/ESP/`/boot`
  partitions first and the 2.5 GiB root partition last, so `growpart` extends
  it into the resized disk. The image's `datasource_list` is `[ Azure ]`, which
  is why the trial user and SSH key are injected offline instead of via a
  cloud-init seed disk; its Hyper-V KVP daemon reports the guest IP to the host.
- **GoldenVhdx**: pass a copy of an existing verified VM disk and its SHA-256.
  If the source VM has checkpoints, pass the **leaf `.avhdx`** — the script
  merges the chain into a standalone VHDX (`Merge-VHD`) before use, because a
  base `.vhdx` under an active checkpoint chain is stale state.
- The script then creates the VM with the verified profile, and — with the VM
  off — injects your SSH key and the trial account offline via
  `wsl --mount --vhd --bare` (WSL2 required on the Windows host). For golden
  copies it also fixes the installer netplan MAC binding, regenerates SSH host
  keys, and regenerates the machine-id so the clone is an independent machine.
- After boot it resolves the guest IP (Hyper-V IP reporting via the guest KVP
  daemon where present, otherwise an ARP sweep of the switch subnet; or pass
  `-GuestIpAddress`), verifies the scanned ED25519 SSH host key against the
  fingerprint from the VMConnect guest console (prompt or
  `-GuestHostKeyFingerprint`), copies `infra/repro/bootstrap-linux.sh` and the two
  `infra/guest/` scripts into the guest, and runs the same Path A bootstrap
  inside the guest over SSH.
- The PowerShell confirmation covers the entire VM creation, offline injection,
  and unattended guest bootstrap plan. The guest bootstrap applies its printed
  package, toolchain, and sysctl changes without separate prompts.
- The injected `NOPASSWD` sudo rule exists only to run the guarded provisioning
  bootstrap. The script removes it automatically after a successful bootstrap.
  If provisioning fails before completion, the rule remains to permit repair;
  remove `/etc/sudoers.d/90-tbound-<trial-user>` before running untrusted
  evaluation tasks. Never treat the bootstrap trial account as a separate root
  boundary.
- **Prerequisites**: Hyper-V + module, admin elevation, OpenSSH client
  (`ssh.exe`), WSL2 with `wsl --mount` support (the verified host runs Windows
  10 + Store WSL2 and supports it). The verified reference host additionally
  guards `F:\TBoundAssets` / `F:\TBoundVMs` and free-space budgets — those
  host-specific guards stay in [`infra/hyperv/`](../infra/hyperv/SETUP.md); this
  script only requires sane free space on the target drive.

## The one-command capability report

```powershell
# a running VM or a native Linux host over SSH
.\infra\repro\probe-host.ps1 -Target Ssh -HostName <ip> -UserName <trial-user> `
  -KeyFile $env:USERPROFILE\.ssh\id_ed25519 -OutFile host-report.txt
# a WSL distro (development smoke only — not the D09/D10 evaluation profile)
.\infra\repro\probe-host.ps1 -Target Wsl -Distro <distro>
# a saved capture of the probe output
.\infra\repro\probe-host.ps1 -Target File -ReportFile probe-capture.txt
```

The report reuses `infra/guest/probe-capabilities.sh` by path, adds a live
Cosign check (the probe does not cover Cosign), prints every row with
PASS/FAIL/UNVERIFIED, and ends with a plain-language "what to fix" footer.
SSH mode requires the host key to already be in `known_hosts`. Before the first
report, connect once with the quickstart SSH command, compare the displayed
fingerprint with the value on the guest console, and accept it only if they
match. The report uses batch mode and rejects unknown or changed host keys.
Exit codes: `0` all PASS (at most the two by-design UNVERIFIED rows), `1` any
FAIL, `2` tool/invocation error, `3` Landlock ABI < 8, `4` unexpected
UNVERIFIED rows. Save the report with `-OutFile`; it belongs next to the
profile-freeze evidence.

## Kernel and Landlock ABI (profile-freeze mapping)

- The tbound entrypoint expects **Landlock ABI 8** (`LANDLOCK_RESTRICT_SELF_TSYNC`).
- ABI 8 is available on **Linux 7.0** (ABI 6 = 6.12, ABI 7 = 6.15 per the
  kernel's Landlock documentation); always query the ABI and test the required
  TSYNC operation on the running kernel rather than inferring it from a version
  string or installed package.
- The Ubuntu 24.04 *cloud image* path ships `linux-azure` 7.0 → ABI 8 out of
  the box.
- The verified evaluation VM runs the GA `6.8` kernel → ABI 4. The documented
  fix is Ubuntu's supported HWE stack, which for 24.04 currently provides
  current HWE kernel (observed on the reference host as `7.0.0-38` in October
  2026):

  ```sh
  sudo apt-get install --install-recommends linux-generic-hwe-24.04
  sudo reboot        # then re-run the probe and record the new kernel
  ```

  Follow the guest runbook's caution: do **not** install an HWE kernel
  preemptively — first save the probe output and `uname -r`, apply the fix,
  re-run the probe, and record the kernel and package versions before and
  after; the HWE track changes over the LTS lifetime.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `user-namespace: unshare failed ... write /proc/self/uid_map: Operation not permitted` | Ubuntu 24.04 AppArmor restriction (`kernel.apparmor_restrict_unprivileged_userns=1`) | Prefer a scoped AppArmor profile on shared/daily-driver systems. On a dedicated evaluation machine, `bootstrap-linux.sh` Step 4 applies the sysctl after confirmation; manual equivalent: write `/etc/sysctl.d/99-tbound-userns.conf` with `kernel.apparmor_restrict_unprivileged_userns = 0`, run `sudo sysctl --system`, then re-probe. This is a system-wide security trade-off. |
| `landlock-abi: ... ABI 4` (or any ABI < 8) | GA 6.8 kernel on the reference VM | HWE fix above; reboot; re-probe; record before/after. |
| `cosign-cli: FAIL ... missing` | Cosign is not in the Ubuntu 24.04 archive | `bootstrap-linux.sh` Step 3 installs the pinned `v3.1.3` static binary under `/opt/tbound/toolchains/` with a `/usr/local/bin/cosign` symlink. |
| Offline edit of the golden VM sees raw partitions, not `/home/tboundadmin` | The VM root is LVM (`sdc3` → VG `ubuntu-vg`, LV `ubuntu-lv`) | Inside WSL after `wsl --mount --vhd --bare`: `pvscan --cache`, `vgchange -ay ubuntu-vg`, then mount `/dev/ubuntu-vg/ubuntu-lv`. The bootstrap injection script does this automatically. |
| Offline edit changes the wrong disk state | Checkpoints present: the base `.vhdx` is stale; the live state is the leaf `.avhdx` | Resolve the leaf via the `Get-VHD ... ParentPath` chain; edit the leaf only. For a reusable clone, `Merge-VHD -Path <leaf.avhdx> -DestinationPath <merged.vhdx>` (the Hyper-V bootstrap does this when given an `.avhdx`). |
| `Mount-VHD` and `wsl --mount` fail on the same file | The two mechanisms conflict on the same VHDX | Never combine them in one session; use exactly one. The bootstrap only ever uses `wsl --mount --vhd --bare`. |
| `Get-VMNetworkAdapter` shows no IP address | The reference VM has no KVP daemon; IP reporting needs the guest KVP integration service | Use the ARP sweep (the bootstrap falls back to it), or read the IP from the console and pass `-GuestIpAddress`. The cloud image path normally reports IPs (it ships Hyper-V cloud tools). |
| Guest IP changes after every reboot | `Default Switch` DHCP | Re-resolve or pass `-GuestIpAddress`; pin a static netplan config offline if you need stability. |
| Cloned golden VM has no network | Installer netplan pins the interface to the original MAC | The bootstrap's golden-mode injection renames the installer netplan file (`.installer-orig`) and writes a DHCP config for `eth0`. Manual: same edit offline. |
| Two VMs share SSH host keys / machine-id | A golden copy duplicates identity material | The bootstrap's golden-mode injection regenerates SSH host keys (`ssh-keygen -A`) and clears `/etc/machine-id` on the copy only. |
| `probe-host.ps1` output looks garbled in WSL mode | `wsl.exe` historically emits UTF-16 for its own messages | The script strips NUL padding; if a message still looks mangled it affects only diagnostics, not the parsed rows. |
| Probe run as root fails the user-namespace row | The probe must run as the non-root trial account | Run it as the trial user (`bootstrap-linux.sh` does this via `sudo -u`). |
| WSL distro shows cgroup/namespace FAILs | WSL2 is not the frozen evaluation profile | Expected: WSL is development/smoke only (D09/D10). Run the profile on Path A/B hosts. |

## What this does *not* establish

- **D09 (frozen rootless runtime profile):** the bootstrap prepares the host
  capability floor. The frozen profile additionally needs the image digest plus
  offline Cosign verification with an authorized public key, the signed
  rootless UID/GID map contract, and the profile identity review — none of
  which these scripts perform. No container is started and no image is pulled
  at any point.
- **D10 (dedicated pinned VM):** Path B produces the VM shell of the verified
  profile. Keep the NIC disconnected for all untrusted runs, keep at most one
  ProductionOnly checkpoint, and do not treat a copy of a checkpoint chain as a
  fresh baseline.
- **D11 (containment mechanisms):** the probe verifies the kernel mechanisms
  live. `cgroup.kill-write` stays `UNVERIFIED` by design until a dedicated
  disposable-cgroup test; `uidmap-helpers` presence is not behavior.
- **G1:** nothing here is containment, provider, or durability evidence. Freeze
  evidence = bootstrap log (`/var/log/tbound-repro-bootstrap.log`), the
  provisioner log (`/var/log/tbound-guest-provision.log`), the saved
  `probe-host.ps1` report, and the kernel version before/after any HWE change.

## Relation to the existing runbooks

[`infra/hyperv/SETUP.md`](../infra/hyperv/SETUP.md) remains the verified manual
Hyper-V runbook (including this host's `F:` storage guards and the partial-VM
repair flow); [`infra/guest/README.md`](../infra/guest/README.md) remains the
authoritative offline-phase runbook. This guide and `infra/repro/` automate the
*provisioning phase* only — the online/trusted phase before the NIC is
disconnected and the golden baseline checkpoint is created. Everything after
`bootstrap-linux.sh` prints `Bootstrap complete` is governed by the guest
runbook, unchanged.
