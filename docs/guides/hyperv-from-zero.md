# Create a product VM on Windows

These steps take a Windows PC to a Hyper-V Linux VM that runs the TBound
product. The linked guides hold the detail. Nothing here is thesis evidence.
The whole chain has not run on a clean machine yet. If a step fails, report it.

WSL cannot host the product. The product needs Landlock ABI 8, cgroup v2,
rootless Podman and systemd, and the WSL 2 kernel tested here reported ABI 1.
Use WSL to build and test ([quickstart](quickstart.md)).

You need Hyper-V, an elevated PowerShell, Git Bash, a default WSL 2 distro for
the bootstrap's SSH key injection, 60 GB of free disk, 16 GiB of free host
memory, and an OpenCode Go API key. You also need an SSH key pair without a
passphrase. Create it in the checkout, where the step 4 probe looks for it.
Press Enter at both passphrase prompts:

```powershell
New-Item -ItemType Directory -Force .\.local\secrets\abi8-qualification | Out-Null
ssh-keygen -t ed25519 -f .\.local\secrets\abi8-qualification\id_ed25519
```

1. Download `ubuntu-24.04.5-live-server-amd64.iso` from
   <https://releases.ubuntu.com/24.04.5/> and verify it as in
   [CLEAN-GUEST-QUALIFICATION](../../infra/repro/CLEAN-GUEST-QUALIFICATION.md#local-hyper-v-input-authenticated-server-iso-and-generic-vhdx).
   Use your own paths and record your own SHA-256.

2. In Hyper-V Manager, create a seed VM: generation 2, 2 vCPUs, fixed 4 GiB
   memory, dynamic 40 GiB disk, Secure Boot with the Microsoft UEFI Certificate
   Authority template, no automatic checkpoints. Install from the ISO with a
   plain ext4 root and no LVM. Take the installer defaults otherwise. Remember
   the login and password you create, because step 5 needs them. Then install
   the HWE kernel and reboot:

   ```sh
   sudo apt-get install --install-recommends linux-generic-hwe-24.04
   sudo reboot
   ```

   Power the VM off, detach the DVD, confirm the disk has no checkpoint, and
   hash it:

   ```powershell
   (Get-FileHash -Algorithm SHA256 '<path-to-seed>.vhdx').Hash
   ```

3. In an elevated PowerShell in the checkout, create the working VM:

   ```powershell
   .\infra\repro\bootstrap-hyperv-windows.ps1 `
     -ImageKind GoldenVhdx `
     -ImageSourcePath '<path-to-seed>.vhdx' `
     -ExpectedImageSha256 '<sha256-from-step-2>' `
     -PublicKeyFile .\.local\secrets\abi8-qualification\id_ed25519.pub `
     -PrivateKeyFile .\.local\secrets\abi8-qualification\id_ed25519
   ```

   It adds your key for the user `tboundadmin`, asks you to confirm the plan
   once, and runs `bootstrap-linux.sh` in the guest. Exit code 3 means the
   kernel is not at ABI 8. Details: [infra/repro](../../infra/repro/README.md).

4. Check the kernel. Continue only when it reports ABI 8. The probe reads the
   key from the folder above and pins the Windows OpenSSH build, so a Windows
   update can make it fail.

   ```powershell
   .\infra\repro\probe-live-landlock-abi8-ssh.ps1 -HostName '<guest-ip>' `
     -GuestHostKeyFingerprint '<SHA256 fingerprint printed by the bootstrap>'
   ```

5. `tboundadmin` has a locked password and key-only SSH login. It has
   passwordless sudo for provisioning, so do not use it for the product steps.
   Open the VM in the Hyper-V console and log in as the user you created in
   step 2. Run
   everything below as that user, including the root steps of the next guide.
   Clone this repository and install as that user, never as root. The download
   is checked against `SHA256SUMS` only, which detects corruption and not a
   replaced file.

   ```sh
   git clone https://github.com/faetschi/trust-boundary.git
   cd trust-boundary
   bash install/install.sh --version 0.1.0 --no-path
   ```

   Then follow [Run a first governed product turn](product-first-turn.md),
   steps 1 to 10, and [Enable Bash](product-bash.md) if you want `bash`.
   To chat over SSH from Windows as `tboundadmin`, see
   [product demo from Windows](product-demo-windows.md).

If SSH times out after a restart, or a test says `broader permissions than
allowed`, see [product demo from Windows](product-demo-windows.md#1-connect-to-the-vm)
and [quickstart](quickstart.md#4-check-the-machine).
