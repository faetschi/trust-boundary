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

## Provisioning evidence — 2026-10-02

- After the DHCP change, the operator connected to the guest over SSH at `172.19.193.190`.
- The transferred source archive and manifest for project commit `9a73af56e8a81cf20c1f3cfc960a891c4b545152` passed guest-side SHA-256 verification. Safe extraction completed at `/home/tboundadmin/tbound-handoff-9a73af56e8a8/tbound`.
- The original installer hash matched, but its APT guard produced a false positive and stopped at preflight. The unchanged full-tree installer from the `9a73af56...` source was not rerun. A separate guest-side transfer directory at `/home/tboundadmin/tbound-apt-fix-68de01bfef86`, from fix commit `68de01bfef8628333f47a1760d44f7bca9412bf0`, supplied the corrected installer (SHA-256 `478484545bb73a531f20732de784b5a3edf2c816bd0b5ed3519e8546a16d5443`) and regression test artifact (SHA-256 `99e59bbc23c995bbc396e8133980ce99eb94042c4d65d483bebdbeb732dd301d`); both hashes were verified in the guest, and the seven regression tests passed.
- The guest provisioning command is reported complete at `2026-10-02T15:22:52Z`. Online Go cache preparation and `npm ci --ignore-scripts --no-audit --no-fund` completed at `2026-10-02T15:37:05Z`; npm reported 119 packages installed. Node.js `24.21.0` and Go `1.27.1` were used. `npm update` was not run.
- Reported guest runtime versions: Node.js `24.21.0`, Go `1.27.1 linux/amd64`, Podman `4.9.3`, crun `1.14.1`, and GCC `13.3.0`. Provisioning log: `/var/log/tbound-guest-provision.log`.
- `getent` did not support the subuid/subgid queries; this was non-fatal. Direct reads verified `/etc/subuid` and `/etc/subgid` each contain `tboundadmin:100000:65536`.
- The root filesystem was reported as `37G` total, `3.5G` used, and `32G` available.

## Offline evidence and host baseline — 2026-10-02

- The guest's private baseline evidence directory is `/home/tboundadmin/tbound-baseline-evidence-20261002`. The dependency-preparation log `/home/tboundadmin/tbound-baseline-evidence-20261002/tbound-dependency-preparation-9a73af56e8a8.log` has SHA-256 `977b59add025bd1f752f76406847c6807e76371d9f1924c7718071a7046bc715`; the provisioning log `/var/log/tbound-guest-provision.log` has SHA-256 `cdc632b4ab4610dd765b26682e155ba39392b2bc6478606b4eed85c05c5d8613`.
- The operator reported an orderly `sudo poweroff` at `2026-10-02T15:45:00Z`, and the host confirmed the VM was Off. Before poweroff, the operator reconfirmed that VMConnect **Enhanced Session** was greyed out. This visual observation is separate from the `BasicSessionConfirmed` operator attestation recorded by host Verify; the grey indicator alone does not establish clipboard or device-redirection state.
- During a later offline guest boot, the operator ran `uname -r`, `ip route`, `ip -6 route`, and read `/sys/class/net/eth0/carrier`. The reported output showed kernel `6.8.0-146-generic`, carrier `0`, and no routes printed by either route command. This observation covers the listed commands and `eth0` only.
- The offline runner result at `~/tbound-offline-verify.json` (647 bytes) was **FAIL before suites**. Its network gates passed with no IPv4 or IPv6 default routes and `eth0` carrier `0`; cleanup passed with `leftover_paths=[]`. The Go toolchain check failed because it reported `linux/amd64` without cgo enabled. The report had `checks={}` and all claims false; no test suites ran.
- A follow-up operator check confirmed the environment cause: with `PATH=/usr/bin:/bin`, `HOME=/tmp`, `GOENV=off`, and `GOTOOLCHAIN=local`, Go reported `linux/amd64` and `CGO_ENABLED=1`; with `PATH` omitted, it reported `linux/amd64` and `CGO_ENABLED=0`. The runner source fix is committed as `c8bdaa27749df2b46bfd992625eee646726f0e2f`; applying it in the guest and rerunning verification are pending.
- With the VM Off, the host disconnected the NIC, detached the DVD ISO, and set the installed disk first in boot order. The pre-checkpoint read-only **Verify** passed all checks. A `Checkpoint-VM` request using `ProductionOnly` with VSS disabled returned `ubuntu-24.04.5-golden-baseline`; its immediate post-check returned `Post-checkpoint verification failed.` with the cause undetermined. A later read-only inventory found exactly one checkpoint with that name, `SnapshotType=Standard`, and creation time `2026-10-02 18:00:12` Europe/Vienna. `SnapshotType` is the inventory category; it does not establish the checkpoint creation policy or freeze/thaw activity.
- The post-checkpoint read-only **Verify** passed all checks: checkpoint count 1; disk-chain depth 2 with the expected 40 GiB dynamic base under `F:`; `F:` free space 887566548992 bytes (826.6108 GiB) and tracked data 11211859557 bytes (10.4419 GiB); 8 CPUs and fixed 16 GiB RAM; VM identity on the allowlist; VSS disabled; NIC disconnected; ISO ejected; installed disk first; VM Off; and operator-attested Basic Session (`BasicSessionConfirmed`).

## Pending provisioning and verification

- Apply the committed runner fix in the guest and rerun offline verification; the prior run stopped before suites.
- Rootless-operation checks remain pending.
- Offline capability checks, Go/Pi-adapter tests, complete containment verification, destruction G1, disposable restore, and the actual TODO LLM demo remain unverified.
- No secure baseline or WP1 completion is established by this record.
- Any future source export requires a clean worktree, reviewed source, no VM or file/checkpoint writers during export, and the documented `F:` storage-budget gates.
