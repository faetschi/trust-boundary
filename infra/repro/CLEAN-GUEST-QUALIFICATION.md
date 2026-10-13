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

Create a **seed VM** in elevated PowerShell. The seed is installer-only:
2 vCPUs, fixed 4 GiB RAM and a 40 GiB disk. The bootstrap’s existing
follow-on VM configuration remains 8 vCPUs, fixed 16 GiB RAM and a 40 GiB
disk; those settings are not qualified by this recipe. Ubuntu Server’s
[Hyper-V guide](https://ubuntu.com/server/docs/how-to/virtualisation/ubuntu-on-hyper-v/)
and Microsoft’s [support matrix](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/supported-ubuntu-virtual-machines-on-hyper-v)
document Ubuntu Server installation and Ubuntu 24.04 Hyper-V support.

```powershell
$BaseRoot = 'F:\TBoundAssets\TBound-Ubuntu-ISO-Seed'
$BaseVmName = 'TBound-Ubuntu-ISO-Seed'
$VhdPath = Join-Path $BaseRoot 'ubuntu-24.04.5-server.vhdx'
$IsoPath = 'F:\TBoundAssets\ubuntu-24.04.5-live-server-amd64.iso'
$SwitchName = 'Default Switch'

if (Get-VM -Name $BaseVmName -ErrorAction SilentlyContinue) {
  throw "Seed VM already exists: $BaseVmName"
}
New-Item -ItemType Directory -Force -Path $BaseRoot | Out-Null
if (Test-Path -LiteralPath $VhdPath) {
  throw "Seed disk already exists: $VhdPath"
}
New-VHD -Path $VhdPath -SizeBytes 40GB -Dynamic | Out-Null
New-VM -Name $BaseVmName -Generation 2 -MemoryStartupBytes 4GB `
  -NoVHD -Path $BaseRoot -SwitchName $SwitchName | Out-Null
Set-VMProcessor -VMName $BaseVmName -Count 2
Set-VMMemory -VMName $BaseVmName -DynamicMemoryEnabled $false `
  -StartupBytes 4GB
Set-VM -VMName $BaseVmName -AutomaticCheckpointsEnabled $false `
  -CheckpointType ProductionOnly
Add-VMHardDiskDrive -VMName $BaseVmName -Path $VhdPath | Out-Null
$Dvd = Add-VMDvdDrive -VMName $BaseVmName -Path $IsoPath -PassThru
Set-VMFirmware -VMName $BaseVmName -EnableSecureBoot On `
  -SecureBootTemplate MicrosoftUEFICertificateAuthority `
  -FirstBootDevice $Dvd
Start-VM -Name $BaseVmName
vmconnect.exe localhost $BaseVmName
```

In the installer, use the whole 40 GiB disk and create the first account with
the same name passed later as `-GuestUser` (default `tboundadmin`). Select
OpenSSH Server. At the installer reboot prompt, eject the ISO and boot the
installed disk. Record `id -u tboundadmin` from the guest; the bootstrap
requires a non-root account and does not require UID 1000. The offline
injector will replace the installer’s network configuration with DHCP matched
to the Hyper-V network driver, so the recipe does not assume an interface name
such as `eth0`.

Shut down the seed through the guest OS, wait for `Off`, and reject checkpoints
or a parent-linked disk before hashing or cloning. `Stop-VM` without `-Force`
requests guest shutdown. The bounded wait below refuses to continue if that
does not complete; it never forces power-off.

```powershell
Stop-VM -Name $BaseVmName
$StopDeadline = (Get-Date).AddMinutes(10)
while ((Get-VM -Name $BaseVmName -ErrorAction Stop).State -ne 'Off' -and
       (Get-Date) -lt $StopDeadline) {
  Start-Sleep -Seconds 2
}
if ((Get-VM -Name $BaseVmName -ErrorAction Stop).State -ne 'Off') {
  throw 'Seed VM did not shut down; refusing to hash or copy its disk.'
}
if (@(Get-VMSnapshot -VMName $BaseVmName -ErrorAction Stop).Count -ne 0) {
  throw 'Seed VM has checkpoints; refusing to hash or copy its disk.'
}
$BaseDisks = @(Get-VMHardDiskDrive -VMName $BaseVmName -ErrorAction Stop)
if ($BaseDisks.Count -ne 1) {
  throw "Expected one seed disk; found $($BaseDisks.Count)."
}
$SeedVhdx = $BaseDisks[0].Path
$SeedVhdInfo = Get-VHD -Path $SeedVhdx -ErrorAction Stop
if ($SeedVhdInfo.ParentPath) {
  throw "Seed disk has a parent; refusing to hash or copy it: $SeedVhdx"
}
$SeedSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $SeedVhdx).Hash

.\infra\repro\bootstrap-hyperv-windows.ps1 `
  -ImageKind GoldenVhdx `
  -ImageSourcePath $SeedVhdx `
  -ExpectedImageSha256 $SeedSha256 `
  -PublicKeyFile "$env:USERPROFILE\.ssh\id_ed25519.pub" `
  -VmName 'TBound-Ubuntu-2404-ISO' `
  -VmRoot 'F:\TBoundAssets\TBound-Ubuntu-2404-ISO' `
  -SwitchName $SwitchName
```

This is manual image preparation, not a scripted ISO rebuild. Record the
installer choices and actual seed and follow-on VM settings, then measure the
booted guest and retain its package manifest, kernel, live Landlock ABI, and
disk/checkpoint-chain identities. Freeze a measured offline disk for reuse
only after the required checks. Reusing that frozen clone does not demonstrate
a reproducible rebuild, register the image, establish containment, or establish
G1. Record the actual follow-on VM resource settings rather than treating the
bootstrap’s current 8/16/40 configuration as a registered profile.

Disable shared folders and clipboard integration. The existing development VM
and WSL remain component-test environments; they do not substitute for a
registered clean guest.

Provision in a connected maintenance phase before freezing the baseline. Use
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

Prepare module/npm caches first. Shut down, disconnect every virtual NIC at the
hypervisor, boot from the candidate baseline, and use the guest console as the
credential-free trial account. Set `TBOUND_TREE` to the identified source tree.
Do not pass provider variables, pull images or call a provider.

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
