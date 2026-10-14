# Candidate clean Linux guest qualification

Preparation template only: this is not image registration, containment evidence,
or a G1 manifest. G1 remains false and H1 remains unexecuted. Keep the related
[candidate experiment manifest](../../docs/experiment-manifest.md) as the
existing cross-reference; this worksheet does not create registration evidence.

## Prepare and identify the candidate

Use a new dedicated amd64 Ubuntu 24.04 guest. The cloud-image verification
block below applies only to cloud artifacts supported by their publisher on
the intended hypervisor. It does not qualify an Azure VHD for local Hyper-V.
For a local Hyper-V installation, use the separate ISO procedure below.
Select and record
the exact release serial and versioned publisher URL; do not record the mutable
`noble/release/` alias as the immutable input. Preserve the artifact filename
and format, signed checksum and signature files, signing-key fingerprint and
trust source, signature-verification result, and the artifact SHA-256 from the
authenticated checksum list. For an archive, also record the extracted disk
SHA-256 and its relation to that archive. A bootstrap's caller-supplied
`ExpectedImageSha256` check establishes equality to that value, not publisher
authenticity.

Cloud-artifact verification instructions (run later in a trusted Ubuntu
environment; keep the selected artifact and checksum files in the same
directory):

```sh
set -eu
BASE_URL='<exact versioned Ubuntu release URL selected for this build>'
ARTIFACT='<exact artifact filename supported by the chosen hypervisor>'
curl -fSLO "$BASE_URL/SHA256SUMS"
curl -fSLO "$BASE_URL/SHA256SUMS.gpg"
curl -fSLO "$BASE_URL/$ARTIFACT"
gpg --no-default-keyring \
  --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg \
  --list-keys --with-fingerprint
gpg --no-default-keyring --status-fd 1 \
  --verify --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg \
  SHA256SUMS.gpg SHA256SUMS
test "$(awk -v f="$ARTIFACT" \
  '$2 == f || $2 == "*" f {n++} END {print n+0}' SHA256SUMS)" -eq 1
sha256sum --check --ignore-missing SHA256SUMS
sha256sum "$ARTIFACT"
```

Record the signing fingerprint, trust source and verification output. The
cloud-image keyring above is not the ISO signing keyring. Canonical documents
Azure VHDs as Azure-specific and nonfunctional outside Azure, including local
Hyper-V; do not use an Azure VHD as this local input. `CloudVhdx` remains a
compatibility path, not evidence that a given publisher supports an artifact
on local Hyper-V.

### Local Hyper-V input: authenticated Server ISO and generic VHDX

Use the versioned
[`ubuntu-24.04.5-live-server-amd64.iso`](https://releases.ubuntu.com/24.04.5/).
For a later point release, record its exact filename and versioned release
URL. Do not substitute a mutable series or `latest` alias.

The authenticated local copy for this qualification is
`F:\TBoundAssets\ubuntu-24.04.5\ubuntu-24.04.5-live-server-amd64.iso`.
Its recorded length is 4,080,486,400 bytes and SHA-256 is
`97f3d7ffb032c3eb3b23d2c8be9cc76e60c2c1f2c0146ba5ba9fe01cafae0fd8`.
The versioned subdirectory is part of the path.

Run the following in a trusted Ubuntu environment with `ubuntu-keyring`
installed. Ubuntu’s image-signing key is included in the trusted
`/usr/share/keyrings/ubuntu-archive-keyring.gpg` keyring. Record the
`ubuntu-keyring` package version and trust source, the `VALIDSIG` output, and
the ISO SHA-256. Do not use the cloud-image keyring for this verification.

```sh
set -eu
BASE_URL='https://releases.ubuntu.com/24.04.5'
ARTIFACT='ubuntu-24.04.5-live-server-amd64.iso'
dpkg-query -W -f='${binary:Package}\t${Version}\n' ubuntu-keyring
curl -fSLO "$BASE_URL/SHA256SUMS"
curl -fSLO "$BASE_URL/SHA256SUMS.gpg"
curl -fSLO "$BASE_URL/$ARTIFACT"
gpg --no-default-keyring \
  --keyring /usr/share/keyrings/ubuntu-archive-keyring.gpg \
  --list-keys --with-fingerprint
gpgv --status-fd 1 \
  --keyring /usr/share/keyrings/ubuntu-archive-keyring.gpg \
  SHA256SUMS.gpg SHA256SUMS
test "$(awk -v f="$ARTIFACT" \
  '$2 == f || $2 == "*" f {n++} END {print n+0}' SHA256SUMS)" -eq 1
sha256sum --check --ignore-missing SHA256SUMS
sha256sum "$ARTIFACT"
```

Install xorriso in the WSL Ubuntu distribution if it is not already available.
The qualification helper fails closed when the tool is missing:

~~~powershell
wsl.exe --distribution Ubuntu --user root -- apt-get update
wsl.exe --distribution Ubuntu --user root -- apt-get install -y xorriso
~~~

Run the unattended staging helper through the fixed source-pinned UAC launcher
from Windows PowerShell 5.1. The launcher accepts only `WhatIf` or `Create`;
it pins the candidate script and installed Hyper-V module before requesting
elevation. First run the bounded read-only preflight and review its per-run
receipt. Then request creation once:

~~~powershell
.\infra\repro\launch-reviewed-abi8-candidate-elevated.ps1 -Action WhatIf
.\infra\repro\launch-reviewed-abi8-candidate-elevated.ps1 -Action Create
~~~

The launcher reports the UAC child's PID and unique Desktop run directory
before waiting on that same process handle. `Create` uses the seed helper's
120-minute installer limit plus bounded setup slack. The helper never kills
the VM or removes partial files on timeout. Do not rerun while the child is
active or a partial seed exists. The candidate source pin in the launcher must
match the reviewed `new-abi8-hyperv-candidate.ps1`; a mismatch fails closed.

It verifies the exact ISO length and SHA-256 above, checks the expected 24.04.5
GRUB layout, and writes only under
F:\TBoundAssets\abi8-qualification-2026-10-14 and
%LOCALAPPDATA%\TBound\abi8-qualification. The private Ed25519 key is created
with an ACL for the current Windows identity; its contents are never printed
or placed in the repository. The NoCloud seed contains an ephemeral random
password hash and that key's public key. SSH password authentication is
disabled in the installed guest.

The helper derives ubuntu-24.04.5-autoinstall.iso from the authenticated
Canonical ISO, adding the autoinstall and local NoCloud arguments to both
installer boot entries. It leaves the signed EFI boot files intact and replays
the source ISO boot metadata with xorriso. The derived ISO is a local
modification, so Canonical's signature does not cover it; its SHA-256,
xorriso version, and relation to the verified source are recorded in the
receipt. The helper creates a separate TBound-Ubuntu-ABI8-Seed installer VM
(Generation 2, 2 vCPUs, fixed 4 GiB RAM, dynamic 40 GiB VHDX, Default Switch,
Secure Boot with the Microsoft UEFI Certificate Authority template, no
automatic checkpoints). Its disposable root uses the installer's `direct`
storage layout, producing a plain ext4 root partition and avoiding LVM-volume
activation during read-only report extraction. The installer selects the
Ubuntu 24.04 HWE kernel and powers off automatically. The helper waits up to
120 minutes, never forces
poweroff, detaches the DVD after a successful poweroff, rejects checkpoints or
a parent-linked disk, and records the standalone VHDX hash in
F:\TBoundAssets\abi8-qualification-2026-10-14\abi8-seed-receipt.json.
A failed run leaves its partial state for inspection; it does not clean up
files or VMs. It does not touch TBound-Ubuntu-2404.

The HWE kernel selector follows Ubuntu's linux-generic-hwe-24.04 track. As
of this procedure date, Ubuntu lists HWE 7.0 in Noble updates
([Ubuntu Noble HWE 7.0 source package](https://packages.ubuntu.com/en/source/noble-updates/linux-meta-hwe-7.0)).
Package candidates can change. Record the installed package version, running
kernel, kernel config digest, and package manifest. Neither the HWE label nor
kernel release establishes Landlock ABI 8. The guest must be live-probed and
the D09 contract requires exactly ABI 8.

After the helper completes successfully, create the fresh final candidate by
cloning the recorded powered-off seed through the existing guarded bootstrap.
The bootstrap verifies the Windows WSL, command-shell, and OpenSSH executables
against pinned SHA-256 values, TrustedInstaller ownership, and Microsoft
signatures. It uses a locked empty SSH configuration and checks the network
host key against a fingerprint generated from the powered-off disk after the
offline injector creates fresh guest SSH host keys. The host-tool pins describe
the current Windows image and require review if a Windows update changes them.
This removes the guest-console fingerprint prompt while keeping strict host-key
checking.

~~~powershell
$SeedVhdx = 'F:\TBoundAssets\abi8-qualification-2026-10-14\seed-vm\Virtual Hard Disks\TBound-Ubuntu-ABI8-Seed.vhdx'
$SeedSha256 = (Get-FileHash -LiteralPath $SeedVhdx -Algorithm SHA256).Hash
$PublicKeyFile = Join-Path $env:LOCALAPPDATA 'TBound\abi8-qualification\id_ed25519.pub'
.\infra\repro\bootstrap-hyperv-windows.ps1 -ImageKind GoldenVhdx -ImageSourcePath $SeedVhdx -ExpectedImageSha256 $SeedSha256 -PublicKeyFile $PublicKeyFile -VmName 'TBound-Ubuntu-2404-ABI8' -VmRoot 'F:\TBoundVMs\TBound-Ubuntu-2404-ABI8' -SwitchName 'Default Switch'
~~~

The clone uses the existing bootstrap's final profile of Generation 2,
8 vCPUs, fixed 16 GiB RAM, and a 40 GiB disk. It performs the offline
trial-user/SSH injection, boots the candidate for online maintenance, runs
bootstrap-linux.sh, and captures the guest capability probe.

Preserve the bootstrap output and its exit code. The bootstrap runs the full
capability probe and requires the exact D09 ABI. For an independent live ABI
measurement, use the reported guest IPv4 address and the ED25519 fingerprint
printed after the offline injection:

~~~powershell
.\infra\repro\probe-live-landlock-abi8-ssh.ps1 -HostName '<reported guest IPv4>' -GuestHostKeyFingerprint '<SHA256 fingerprint printed by bootstrap>'
~~~

This probe uses strict host-key checking against that offline fingerprint and
uses the pinned Windows OpenSSH client with an empty task-specific SSH
configuration. It exits successfully only when the live Landlock VERSION
query returns exactly ABI 8. Retain its output with the bootstrap capability
report.

Require the landlock-abi row to report ABI 8 and the d09-landlock-profile
row to pass. Any other ABI fails the registered profile and requires protocol
reconciliation; do not weaken the exact-equality check. Keep the connected
network only for this maintenance phase. Complete the independent offline
preflight below before freezing the candidate. The old development VM remains
available until the replacement has passed its required measurements and
needed artifacts have been preserved.

During that connected maintenance phase, install the pinned toolchains before
freezing the baseline. Use
the existing bootstrap's pinned Go 1.27.1, Node 24.21.0 and Cosign 3.1.3,
retaining artifact digests and checksum/signature verification records. Install
Pi separately during maintenance using the [installation guide](../../docs/install.md):
```sh
export PATH="/opt/tbound/toolchains/node-v24.21.0-linux-x64/bin:$HOME/.local/bin:$PATH"
node --version
npm --version
command -v node npm
npm_config_prefix="$HOME/.local" npm install --global --ignore-scripts @earendil-works/pi-coding-agent@0.87.1
```
APT holds applied after installation prevent later upgrades; they do not pin
the initial dependency resolver. Record the package source configuration,
resolver inputs and full installed package manifest with the other guest
measurements. Reproducing the build requires frozen package sources and pinned
inputs. Repeating deployment from the exact qualified, frozen offline disk
reuses that measured image; it does not demonstrate a reproducible rebuild.

Create a non-root trial account with a subordinate-ID range of at least 65,536
IDs for each UID and GID mapping and start a fresh login session. Review the bootstrap's system-wide
user-namespace policy changes and include them in the recorded host profile.
Record the running kernel release, package version, kernel config digest and
live Landlock ABI. The registered D09 contract requires **exactly ABI 8**;
neither a kernel release name nor a capability probe's minimum threshold proves
this identity. A different ABI requires explicit protocol reconciliation.

As the trial account, capture identities without copying credentials:

```sh
set -eu
export PATH="/opt/tbound/toolchains/go1.27.1/bin:/opt/tbound/toolchains/node-v24.21.0-linux-x64/bin:$HOME/.local/bin:$PATH"
id
awk -F: -v user="$(id -un)" -v uid="$(id -u)" \
  '$1 == user || $1 == uid {print FILENAME ":" $0}' /etc/subuid /etc/subgid
uname -r
sha256sum "/boot/config-$(uname -r)"
dpkg-query -W -f='${binary:Package}\t${Version}\n'
apt-mark showhold
for tool in go node npm podman crun cosign pi; do
  path=$(command -v "$tool")
  resolved=$(readlink -f "$path")
  printf '%s: %s -> %s\n' "$tool" "$path" "$resolved"
  case "$tool" in go|cosign) "$path" version;; *) "$path" --version;; esac
  sha256sum "$resolved"
done
```

Also record the actual tool executable paths, versions and SHA-256 digests;
OCI image/signer; seccomp profile; cgroup root; entrypoint; source snapshot;
and signed runtime-profile identities. This worksheet supplies no fabricated
registration values. Retain the full package manifest with the source-media
checksum and offline disk identity.

## Offline preflight

The current unattended identity has a random local password that is not
retained, and SSH password authentication is disabled. The bootstrap injects a
key for `tboundadmin`, but the offline steps below require running commands
after the virtual NIC is disconnected. They cannot be performed through that
SSH key once the NIC is disconnected, and the current workflow does not create
a console login. Keep the candidate unfrozen until a guest-side offline runner
has been prepared during the connected maintenance phase and its report can be
retrieved after the disconnected run. Do not mark the offline preflight passed
based on the live SSH probe.

The shell commands below are the bounded payload for that guest-side runner;
they are not a console-login procedure. The runner should execute them as the
trial account, retain output and exit codes on the guest disk, and power off
when finished so the host can collect the report before freezing the image.

Prepare module/npm caches first. Shut down, disconnect every virtual NIC at the
hypervisor, then run the bounded guest-side offline audit. Set `TBOUND_TREE` to
the identified source tree in that audit. Do not pass provider variables, pull
images or call a provider.

```sh
set -eu
test "$(id -u)" -ne 0
test -n "${TBOUND_TREE:?set the identified source tree}"
env -i HOME="$HOME" USER="$(id -un)" LOGNAME="$(id -un)" \
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  /usr/bin/python3 -I "$TBOUND_TREE/infra/guest/verify-offline.py" \
  --host-adapter-disconnected
env -i PATH=/usr/bin:/bin /usr/bin/python3 - <<'PY'
import ctypes, sys
libc = ctypes.CDLL(None, use_errno=True)
libc.syscall.restype = ctypes.c_long
abi = libc.syscall(444, None, ctypes.c_size_t(0), ctypes.c_uint(1))
print(f'Landlock ABI: {abi}; errno: {ctypes.get_errno() if abi < 0 else 0}')
sys.exit(0 if abi == 8 else 1)
PY
env -i HOME="$HOME" USER="$(id -un)" LOGNAME="$(id -un)" \
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  XDG_RUNTIME_DIR="/run/user/$(id -u)" GOTOOLCHAIN=local \
  bash "$TBOUND_TREE/infra/guest/probe-capabilities.sh"
env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  bash "$TBOUND_TREE/scripts/check-governed-host.sh"
```

Retain outputs and exit codes, including failures. The offline verifier checks
network disconnection; the capability probe does not enforce it. Confirm the
reported ABI is exactly 8. Require cgroup v2/domain, user-namespace creation,
rootless Podman/crun and live UID/GID mapping evidence. The probe intentionally
leaves `cgroup.kill-write` and `uidmap-helpers` UNVERIFIED. Exercise teardown
only in a disposable cgroup and retain independent observations. The host
checker remains advisory and may fail until a signed host profile exists.

Passing preflight does not establish G1. Registration and all required WP1
containment, audit, independent effect, recovery and durability checks remain
necessary before headline trials.
