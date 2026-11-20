# Create a product VM on Windows

These steps take a Windows PC to a Hyper-V Linux VM that runs the TBound
product. The linked guides hold the detail. Nothing here is thesis evidence.
The whole chain has not run on a clean machine yet. If a step fails, report it.

WSL cannot host the product. It needs Landlock ABI 8, cgroup v2, rootless
Podman and systemd, and the WSL 2 kernel tested here reported ABI 1. Use WSL to
build, test and view the web chat demo ([quickstart](quickstart.md)).

You need Hyper-V, an elevated PowerShell, Git Bash, a default WSL 2 distro for
the bootstrap's SSH key injection, 60 GB of free disk, and an OpenCode Go API
key.

1. Download `ubuntu-24.04.5-live-server-amd64.iso` from
   <https://releases.ubuntu.com/24.04.5/> and verify it as in
   [CLEAN-GUEST-QUALIFICATION](../../infra/repro/CLEAN-GUEST-QUALIFICATION.md#local-hyper-v-input-authenticated-server-iso-and-generic-vhdx).
   Use your own paths and record your own SHA-256.

2. In Hyper-V Manager, create a seed VM: generation 2, 2 vCPUs, fixed 4 GiB
   memory, dynamic 40 GiB disk, Secure Boot with the Microsoft UEFI Certificate
   Authority template, no automatic checkpoints. Install from the ISO with a
   plain ext4 root and no LVM. Take the installer defaults otherwise. Then
   install the HWE kernel and reboot:

   ```sh
   sudo apt-get install --install-recommends linux-generic-hwe-24.04
   sudo reboot
   ```

   Power the VM off, detach the DVD, confirm the disk has no checkpoint, and
   hash it:

   ```powershell
   (Get-FileHash -Algorithm SHA256 '<path-to-seed>.vhdx').Hash
   ```

3. In an elevated PowerShell in a checkout of this repository, create the
   working VM:

   ```powershell
   .\infra\repro\bootstrap-hyperv-windows.ps1 `
     -ImageKind GoldenVhdx `
     -ImageSourcePath '<path-to-seed>.vhdx' `
     -ExpectedImageSha256 '<sha256-from-step-2>' `
     -PublicKeyFile $env:USERPROFILE\.ssh\id_ed25519.pub `
     -PrivateKeyFile $env:USERPROFILE\.ssh\id_ed25519
   ```

   It adds your key for the user `tboundadmin`, asks you to confirm the SSH
   host-key fingerprint, and runs `bootstrap-linux.sh` in the guest. Exit code
   3 means the kernel is not at ABI 8. Details: [infra/repro](../../infra/repro/README.md).

4. Check the kernel. Continue only when it reports ABI 8:

   ```powershell
   .\infra\repro\probe-live-landlock-abi8-ssh.ps1 -HostName '<guest-ip>' `
     -GuestHostKeyFingerprint '<SHA256 fingerprint printed by the bootstrap>'
   ```

5. Connect from Git Bash as in
   [product demo from Windows](product-demo-windows.md#1-connect-to-the-vm).
   In the VM, clone this repository and install as your login user, not root.
   The download is checked against `SHA256SUMS` only, which detects corruption
   and not a replaced file.

   ```sh
   git clone https://github.com/faetschi/trust-boundary.git
   cd trust-boundary
   bash install/install.sh --version 0.1.0 --no-path
   ```

   Then follow [Run a first governed product turn](product-first-turn.md),
   steps 1 to 10, and [Enable Bash](product-bash.md) if you want `bash`.
   `tbound chat` runs in the VM, so a Windows terminal is an SSH session to it.

If SSH times out after a restart, the Default Switch gave the VM a new address.
Find it with `Get-NetNeighbor -LinkLayerAddress <VM MAC>`. If a test or the
fixture says `broader permissions than allowed`, set `umask 077` and a private
`TMPDIR` ([quickstart](quickstart.md#4-check-the-machine)).
