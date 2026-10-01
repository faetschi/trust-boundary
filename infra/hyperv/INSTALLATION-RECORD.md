# Ubuntu guest installation record

**Recorded:** 2026-10-01 (Europe/Vienna)
**VM:** `TBound-Ubuntu-2404`
**Installer:** Ubuntu Server 24.04.5 AMD64

## Installation choices reported by the operator

- Booted the installer entry **Try or Install Ubuntu Server**.
- Chose guided storage using the entire 40 GiB virtual disk, with LVM enabled.
- Set the profile display name to `tboundadmin` and username to `tboundadmin`.
- The operator reported the server name as `tboundubuntu2404`. The login prompt was reported to show a different spelling; the guest's actual hostname is unconfirmed until checked inside Ubuntu.

## Completion observations

The operator selected **Reboot Now**. During reboot, the installer showed a `cdrom.mount` failure while asking to remove the installation medium and press Enter. The operator pressed Enter and reports reaching a login session as `tboundadmin`. This observation does not establish whether the ISO was ejected from the VM.

Host-side output reported one attached virtual disk at `F:\TBoundVMs\TBound-Ubuntu-2404\Virtual Hard Disks\TBound-Ubuntu-2404.vhdx`, with a blank `DiskNumber`. The read-only `Created` verification had passed before installation; that earlier result is not a post-installation baseline verification.

## Pending checks

- Capture guest output for `hostname`, `cat /etc/os-release`, and `uname -r` to confirm hostname, Ubuntu release, and kernel.
- Confirm the VMConnect session is Basic Session and inspect the current network and DVD/ISO attachment state.
- Run the documented final read-only `Verify` checks with the VM off, network disconnected, ISO ejected, and Basic Session observed. No secure baseline is claimed; no checkpoint creation is reported.
- No API or application/runtime tests are reported as passed.

Credentials and secret values are intentionally omitted from this record.
