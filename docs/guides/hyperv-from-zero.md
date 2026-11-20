# Create a product VM on Windows (Hyper-V)

This page is the shortest path from a Windows PC to a Linux VM where you can
run the TBound product. It links the guides that hold the detail. Nothing here
is thesis evidence.

**Status.** Each part below was run on its own. The whole chain has not yet run
on a clean machine. If a step fails, note the step and open an issue. That is
the result this page is waiting for.

## Why a VM and not WSL

The product service needs a kernel with exactly Landlock ABI 8, cgroup v2 with
`cgroup.kill`, rootless Podman with crun, and systemd. A WSL 2 kernel does not
provide that. One WSL 2 Ubuntu here reported kernel 5.15 and Landlock ABI 1.

WSL is still the right place to build the code, run the tests, and look at the
read-only web chat demo. See the [quickstart](quickstart.md).

## What you need

- Windows 10 or 11 with Hyper-V enabled, an elevated PowerShell, and the
  OpenSSH client and Git for Windows (use its **Git Bash** for SSH; see
  [product demo from Windows](product-demo-windows.md)).
- A default WSL 2 distro. The bootstrap uses it only to put your SSH key into
  the disk while the VM is off.
- At least 60 GB free on the drive that holds the VM.
- An OpenCode Go API key for real chat turns. You type it into a hidden
  prompt on the VM.

## 1. Get the install disk

Download `ubuntu-24.04.5-live-server-amd64.iso` from
<https://releases.ubuntu.com/24.04.5/> and verify it against the signed
`SHA256SUMS`. The commands are in
[CLEAN-GUEST-QUALIFICATION](../../infra/repro/CLEAN-GUEST-QUALIFICATION.md#local-hyper-v-input-authenticated-server-iso-and-generic-vhdx).
That page shows the owner's local paths and recorded hash; use your own paths
and record your own SHA-256.

## 2. Install Ubuntu by hand into a seed VM

In Hyper-V Manager, create a new VM with these settings (from the same page):

- Generation 2, 2 vCPUs, fixed 4 GiB memory, dynamic 40 GiB disk.
- Secure Boot on, with the **Microsoft UEFI Certificate Authority** template.
- No automatic checkpoints.
- Install from the ISO with a **plain ext4 root, no LVM**. The other installer
  answers are not recorded; take the defaults.

After the first boot, install the HWE kernel, reboot, and check it:

```sh
sudo apt-get install --install-recommends linux-generic-hwe-24.04
sudo reboot
# after the reboot:
uname -r
```

The kernel name does not prove Landlock ABI 8. Step 4 measures it.

Power the seed VM off, detach the DVD, confirm the disk has no checkpoint or
parent, and record the SHA-256 of the `.vhdx`:

```powershell
(Get-FileHash -Algorithm SHA256 '<path-to-seed>.vhdx').Hash
```

## 3. Create the working VM from the seed

In an elevated PowerShell, from a checkout of this repository. Use your own
SSH key pair, and the hash from step 2:

```powershell
.\infra\repro\bootstrap-hyperv-windows.ps1 `
  -ImageKind GoldenVhdx `
  -ImageSourcePath '<path-to-seed>.vhdx' `
  -ExpectedImageSha256 '<sha256-from-step-2>' `
  -PublicKeyFile $env:USERPROFILE\.ssh\id_ed25519.pub `
  -PrivateKeyFile $env:USERPROFILE\.ssh\id_ed25519 `
  -VmName 'TBound-Product-Clean'
```

It clones the seed, adds your key for the user `tboundadmin`, boots the VM,
asks you to confirm the SSH host-key fingerprint, and runs
`bootstrap-linux.sh` inside the guest. It creates the VM with 8 vCPUs and
16 GiB fixed memory. Details and exit codes: [infra/repro](../../infra/repro/README.md).

Exit code `3` means the kernel is not at Landlock ABI 8 yet. Install the HWE
kernel, reboot, and rerun the check in step 4.

## 4. Check Landlock ABI 8

```powershell
.\infra\repro\probe-live-landlock-abi8-ssh.ps1 -HostName '<guest-ip>' `
  -GuestHostKeyFingerprint '<SHA256 fingerprint the bootstrap printed>'
```

It succeeds only when the live kernel reports exactly ABI 8. Do not go on
before it does.

## 5. Install TBound in the VM and run the first governed turn

Connect with Git Bash and pinned host keys, as in
[product demo from Windows](product-demo-windows.md#1-connect-to-the-vm).

Clone the repository in the VM, then install the `tbound` binary as your login
user, never as root. Either install the published release:

```sh
git clone https://github.com/faetschi/trust-boundary.git
cd trust-boundary
bash install/install.sh --version 0.1.0 --no-path
```

or build from the checkout as in [USAGE section 3](../../USAGE.md#3-installation).
The release download is checked against its `SHA256SUMS` only. That detects
corruption, not a replaced download.

Then follow [Run a first governed product turn](product-first-turn.md), steps
1 to 10. To allow `bash` commands, follow [Enable Bash](product-bash.md)
after that.

## 6. Chat from a Windows terminal

`tbound chat` runs on the VM, so a terminal on Windows is an SSH session to it.
[Product demo from Windows](product-demo-windows.md) shows the terminal chat
and the browser chat through a forwarded port.

## When something breaks

| Symptom | Cause | Fix |
|---|---|---|
| SSH times out after a restart | The Default Switch gave the VM a new address | Find it with `Get-NetNeighbor -LinkLayerAddress <VM MAC>`; keep the host-key alias |
| Bootstrap exits `3` | Kernel is not at Landlock ABI 8 | Step 2 kernel install and reboot, then step 4 |
| A test or the fixture says `broader permissions than allowed` | The shell has umask 022 or a shared `/tmp` | `umask 077` and a private `TMPDIR`; see [quickstart](quickstart.md) step 4 |
