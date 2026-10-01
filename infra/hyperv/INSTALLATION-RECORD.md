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
- Root is `/dev/mapper/ubuntu--vg-ubuntu--lv`, formatted ext4. The reported `df` output was 19G total, 2.7G used, and 15G available.
- LVM reported VG `ubuntu-vg` with size less than 36.95G and 18.47G free; LV `ubuntu-lv` is 18.47G. The free VG extents can extend the existing root LV and filesystem during provisioning; this does not expand the 40 GiB virtual disk.
- `eth0` is UP with DHCP address `172.20.247.201/20` and link-local IPv6 address `fe80::215:5dff:fe0c:4000`. This is the guest's current interface state, not verification of the host switch or offline state.
- The operator ran `sudo dpkg-reconfigure keyboard-configuration`, selecting **Generic 105-key PC** and German origin, then ran `sudo setupcon`; the operator reports German keyboard mappings.
- The operator confirms the previously disclosed weak password was changed. No credential value is recorded.
- The operator reports the SSH Ed25519 host-key fingerprint `SHA256:22ZTQXN5L54AhovENJ0Eo7ZViomYVv1HRWfbG3izY5g` (256 bits; public-key comment `root@tboundubuntu2404`). This fingerprint does not establish that the SSH service is active.

Credentials and secret values are intentionally omitted.

## Installation completion observations

The operator selected **Reboot Now**. During reboot, the installer showed a `cdrom.mount` failure while asking to remove the installation medium and press Enter. The operator pressed Enter and reports reaching a login session as `tboundadmin`. This does not establish whether the ISO was ejected from the VM.

Host-side output reported one attached virtual disk at `F:\TBoundVMs\TBound-Ubuntu-2404\Virtual Hard Disks\TBound-Ubuntu-2404.vhdx`, with a blank `DiskNumber`. The read-only **Created** verification passed before installation; that earlier result is not a post-installation baseline verification. The host's non-admin context was denied VM access; use the operator's admin context for later Hyper-V controls. On 2026-10-01, the operator confirmed the elevated host query `Get-VM -Name 'TBound-Ubuntu-2404'` reported state `Off`.

## Pending provisioning and verification

- Guest LV-extension and post-change filesystem/LV/VG outputs were not supplied. No successful root resize is claimed; verify those sizes during provisioning before SCP transfer.
- OpenSSH service status output (`systemctl is-active ssh`) was not supplied, so service-active state remains unverified. Before SCP, verify the service and guest IP, then compare the Ed25519 host-key fingerprint locally. Do not record passwords or private keys.
- The operator confirmed the VM is **Off** using elevated Hyper-V controls. Source export has not run since migration; retain the quiesced state and stop other file/checkpoint writers during export. The exporter must pass its clean-worktree, source-review, and F: storage-budget gates.
- Confirm VMConnect Basic Session or Enhanced Session unavailable and inspect clipboard/device redirection state.
- With the VM off, disconnect the network adapter, eject the ISO, set the installed disk first in boot order, and run the documented final read-only **Verify** checks. No secure baseline is claimed; no checkpoint creation is reported.
- No API or application/runtime tests are reported as passed.
