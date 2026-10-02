# Ubuntu guest installation record

**Recorded:** 2026-10-01 (Europe/Vienna)
**VM:** `TBound-Ubuntu-2404`
**Installer:** Ubuntu Server 24.04.5 AMD64

## Installation choices reported by the operator

- Booted the installer entry **Try or Install Ubuntu Server**.
- Chose guided storage using the entire 40 GiB virtual disk, with LVM enabled.
- Set the profile display name to `tboundadmin` and username to `tboundadmin`.
- The operator reported the server name as `tboundubuntu2404`; the guest command output now confirms this hostname.

## Guest checks reported by the operator

- `hostname`: `tboundubuntu2404`.
- `/etc/os-release`: Ubuntu 24.04.5 LTS, Noble; `VERSION_ID=24.04`.
- `uname -r`: `6.8.0-146-generic`.
- VMConnect showed **Enhanced Session** (**Erweiterte Sitzung**) greyed out. This records that UI observation only; the separate clipboard and device-redirection checks are still pending.
- At initial installation, root `/dev/mapper/ubuntu--vg-ubuntu--lv` was ext4 and the reported `df` output was `19G` total, `2.7G` used, and `15G` available. After root extension, a later operator screenshot showed this ext4 root at `37G` total, `2.7G` used, and `32G` available.
- Initial LVM output reported VG `ubuntu-vg` smaller than `36.95G` with `18.47G` free and LV `ubuntu-lv` at `18.47G`. After root extension, the operator reported `vgs` showing VG size `<36.95G` and `VFree=0`, and `lvs` showing LV size `<36.95G`.
- At installation, `eth0` was UP with DHCP address `172.20.247.201/20` and link-local IPv6 address `fe80::215:5dff:fe0c:4000`. During temporary provisioning, the operator reported guest address `172.20.240.57`; this does not establish the final offline network state.
- The operator ran `sudo dpkg-reconfigure keyboard-configuration`, selecting **Generic 105-key PC** and German origin, then ran `sudo setupcon`; the operator reports German keyboard mappings.
- The operator confirms the previously disclosed weak password was changed. No credential value is recorded.
- The operator reports the public SSH Ed25519 host-key fingerprint `SHA256:22ZTQXN5L54AhovENJ0Eo7ZViomYVvlHRWfbG3izY5g` (256 bits; public-key comment `root@tboundubuntu2404`). The operator verified this fingerprint at the guest console. Codex pre-authentication network scan returned the same fingerprint; port `22` responded during temporary provisioning while the SSH service was observed inactive. No conclusion about socket-unit status is recorded.

Credentials and secret values are intentionally omitted.

## Installation completion observations

The operator selected **Reboot Now**. During reboot, the installer showed a `cdrom.mount` failure while asking to remove the installation medium and press Enter. The operator pressed Enter and reports reaching a login session as `tboundadmin`. This does not establish whether the ISO was ejected from the VM.

Host-side output reported one attached virtual disk at `F:\TBoundVMs\TBound-Ubuntu-2404\Virtual Hard Disks\TBound-Ubuntu-2404.vhdx`, with a blank `DiskNumber`. The read-only **Created** verification passed before installation; that earlier result is not a post-installation baseline verification. The host's non-admin context was denied VM access; use the operator's admin context for later Hyper-V controls. On 2026-10-01, the operator confirmed the elevated host query `Get-VM -Name 'TBound-Ubuntu-2404'` reported state `Off`.

## Pending provisioning and verification

- After root extension, a later operator screenshot and LVM reports showed the expanded root and VG values described above. These are operator-reported observations; guest-side SHA-256 verification, archive extraction, remaining provisioning, and offline tests are still pending.
- The SSH service was observed inactive during temporary provisioning. Port `22` responded and its Ed25519 host key matched the fingerprint above. Socket-unit status is not inferred; SSH behavior after final network isolation remains unverified.
- Codex completed the source export successfully from pinned commit `9a73af56e8a81cf20c1f3cfc960a891c4b545152`. The operator reported SCP progress reaching 100% for a 550 KB tar and a 348-byte manifest copied to `/home/tboundadmin/tbound-incoming`; the manifest identifies that commit. This documentation update is later than the source commit and does not change the transferred bundle. Guest-side SHA-256 verification and extraction are unverified.
- Any future source export requires a clean worktree, reviewed source, no VM or file/checkpoint writers during export, and the documented `F:` storage-budget gates.
- Confirm VMConnect Basic Session or Enhanced Session unavailable and inspect clipboard/device redirection state.
- With the VM off, disconnect the network adapter, eject the ISO, set the installed disk first in boot order, and run the documented final read-only **Verify** checks. No secure baseline is claimed; no checkpoint creation is reported.
- No API or application/runtime tests are reported as passed.
