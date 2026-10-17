# ABI8 guest: owner-signed provisioning sequence (Rows 1, 2+6, 5)

Status: prepared from the 2026-10-17 throwaway-key dry run (`F:\TBoundAssets\recovery-20261015\sign-dryrun-20261017\owner-sequence.sh.txt`) plus the repo-side fixes for its blockers. Nothing here has been run against the guest except the steps tagged DRYRUN-OK there. This runbook supersedes `docs/governed-setup.md` section 225-236 for the ABI8 guest (that section is stale: it predates the single `tbound-pi` service identity, the ro-mounted executables, the split calibration roles and the signed host profile flow).

Every step carries one tag:

- `[READ-ONLY]` changes nothing on the guest or the owner machine except new private scratch files.
- `[PRIVILEGED WRITE]` runs as root on the guest through `sudo -n` (tboundadmin). Each one needs explicit owner approval before it runs.
- `[OWNER SIGN]` uses the admin private key at `<ADMIN_KEY_PATH>`. Each one needs explicit owner approval. An agent never reads, copies or logs the key.

Rules that hold for the whole sequence:

1. Every `[PRIVILEGED WRITE]` and every `[OWNER SIGN]` step needs explicit owner approval, step by step. An approval for one step is not an approval for the next.
2. Passwordless sudo for tboundadmin is revoked before the first cell launch and is never restored (step Z1). Nothing after Z1 uses sudo.
3. No service start and no cell launch is part of this sequence.
4. Output paths for signatures must be new absolute canonical paths; in-place signing is refused.

Pending code fixes (owned by another change; not implemented here, the steps marked below cannot complete until they land and the binaries are rebuilt and re-pinned):

- Pending code fix A: the cgroup-root digest will bind only stable facts. Until then `CgroupRootDigest` in the host profile (R5-2) is UNRESOLVED.
- Pending code fix B: a bounded xattr authority `root-attested-bounded-no-hidden-trusted/v1`. Until then the code accepts only the `...complete-enumeration...` authority string, which the bounded claim does not support. The template field `xattr_visibility_authority` (R5-1) takes the bounded string only after fix B.

Placeholders:

```
GSSH            = sh /f/TBoundAssets/recovery-20261015/remeasure-20261017/gssh2.sh   (Git Bash ssh to tboundadmin@172.22.198.27, HostKeyAlias=172.18.155.19, pinned known_hosts, StrictHostKeyChecking=yes, identity by path)
SVC             = sudo -n setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs --
TBP             = the HEAD-built tbound-profile on the owner machine (P1)
<ADMIN_KEY_PATH>= the owner's raw 64-byte Ed25519 private key
<ADMIN_PUB_PATH>= 32-byte public key file derived in P3
H               = owner-side working dir, absolute, canonical, mode 0700
SCR             = /var/tmp/tbound-cal-20261017 (guest scratch, 995:986 0700)
BR              = F:\TBoundAssets\recovery-20261015\bundle-rebuild-20261017  (/f/TBoundAssets/... in Git Bash, /mnt/f/... in WSL)
```

## Part P: preparation

### P1 [READ-ONLY] build the binaries from the frozen commit

Use `core.autocrlf=false -c core.eol=lf` for every `git archive`. With the default Windows config (`autocrlf=true`) `git archive` emits CRLF for the adapter sources, which silently changes the runtime bundle digest (the previously installed bundle has CRLF extension files).

```
git -c core.autocrlf=false -c core.eol=lf archive --format=tar HEAD supervisor scripts adapter/src adapter/package.json adapter/package-lock.json adapter/pi-runtime-package.json packaging infra | (mkdir -p $W/src && tar -x -C $W/src)
cd $W/src/supervisor && GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=file:///home/jeli2k/go/pkg/mod/cache/download GOSUMDB=off CGO_ENABLED=0 \
  /home/jeli2k/opt/go1.27.1/bin/go build -trimpath -o $W/bin/tbound-profile ./cmd/tbound-profile  # and ./cmd/tbound
sha256sum $W/bin/*
```

Reference at 8c2a3cb (two passes byte-identical): `tbound` f6c089278e95ba76a937906457cf84d7a98e6f16ad4ad8bdf994565a558d513a, `tbound-profile` d0290598a578dcbbd15e1ad222e0e94a5039e7f3c9f6821a4ccebc30a3dd6e1b. Both change after the pending code fixes; re-pin at the frozen commit and use the same umask 077, uniquely named private dirs, and removal at the end.

### P2 [READ-ONLY] rebuild the runtime bundle from the frozen commit

The installed bundle (`sha256:d7367e1b58db53c35fa967c333340599ea30aa43471d3a61c3c8cbbb59246884`) predates the model pin (bd6de94). Rebuild it with the scripts in `BR` (`10-setup.sh`, `20-bundle.sh`, `40-go-and-digest.sh`, `50-package.sh`, `99-cleanup.sh`); they run in WSL as jeli2k, offline: Node v24.21.0 from the guest toolchain (sha256 7fde7b8a... checked), `npm install --offline` against a private copy of the npm cache, then `scripts/prepare_official_pi_runtime.py`, then dirs 0555 and files 0555/0444 (exec bit kept), then a deterministic tar (`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner`).

Result at 8c2a3cb (two independent builds byte-identical, digest by the real `tbound-profile runtime-bundle-digest` under a read-only bind):

- LF (canonical, from committed blobs): `sha256:8e405ef0f2a3a3ed725dba2815d71bd8e5db007fe993f4ac3979ad59016d4f48`, tar `BR\runtime-bundle-8e405ef0-lf-head8c2a3cb.tar` sha256 `d0abdaf300ea8e6eacfc0c93df1f5032cf2bf84f686122bc4289c820e97e0471`.
- With the old CRLF export conditions the digest is `sha256:15a15b8b0b930b0bb0bd8c54157c1bc97aef8ee07347ac844176b7ed4eae102d`; against the installed bundle only `extension/src/broker-provider.ts` and `closure-manifest.json/.sha256` differ (the model pin line). The LF build additionally differs in the four other extension files by line endings only.

Re-run P2 at the frozen commit if any of `adapter/src/{broker-provider,tbound-cli-extension,proxy-tools,ipc-transport}.ts`, `adapter/pi-runtime-package.json` or `scripts/prepare_official_pi_runtime.py` changed since 8c2a3cb, and re-pin the digest.

### P3 [OWNER SIGN] derive the public key (key read, no signature produced)

```
tail -c 32 <ADMIN_KEY_PATH> > <ADMIN_PUB_PATH>; stat -c %s <ADMIN_PUB_PATH>        # expect 32
```

### P4 [PRIVILEGED WRITE] install the trust anchor before Row 1

Writes `/etc/tbound/pi-host-profile.pub` (root:root 0644) and the scratch dir.

```
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/pi-host-profile.pub' < <ADMIN_PUB_PATH>
GSSH 'sudo -n sha256sum /etc/tbound/pi-host-profile.pub'; sha256sum <ADMIN_PUB_PATH>        # expect equal
GSSH 'sudo -n install -d -o 995 -g 986 -m 0700 /var/tmp/tbound-cal-20261017'
```

Rollback: `sudo rm /etc/tbound/pi-host-profile.pub; sudo rm -rf /var/tmp/tbound-cal-20261017`.

### P5 [PRIVILEGED WRITE] make the live root enumerable by uid 995 (blocker 1)

uid 995 cannot open `lost+found` (root 0700), so every 995 enumeration of the live root fails (`xattr_profile_linux.go:724`).

```
GSSH 'sudo -n chown 995:986 /var/lib/tbound/live/lost+found && sudo -n stat -c "%U:%G %a" /var/lib/tbound/live/lost+found'      # expect tbound-pi:tbound-pi 700
```

Rollback: `sudo chown root:root /var/lib/tbound/live/lost+found`.

### P6 [PRIVILEGED WRITE] executables and artifacts on read-only mounts (blocker A)

Code requirement (all fail closed, `requireReadOnlyMount` at `launcher_linux.go:1007`):

| Item | Pinned path | Checked at |
| --- | --- | --- |
| runner | `/usr/local/bin/tbound` | `d09_template_linux.go:269` via `managed_live_root_linux.go:574-578`; launch `managed_live_root_linux.go:1276` |
| podman | `/usr/bin/podman` | `containment_linux.go:995` -> `:2498` |
| crun | `/usr/bin/crun` | `containment_linux.go:1008` -> `:2498` |
| cosign | `/opt/tbound/cell/cosign` | `containment_linux.go:1022` -> `:2498` |
| cosign.pub | `/opt/tbound/cell/cosign.pub` | `containment_linux.go:1296` |
| seccomp.json | `/opt/tbound/cell/seccomp.json` | `containment_linux.go:1517` |
| OCI layout dir | `/opt/tbound/cell/pi-cell-oci` | `containment_linux.go:1373` |

Not required: `tbound-profile`, `conmon` (no code path checks them), `/bin/bash` (measured inside the image). Node and the runtime bundle are already on read-only mounts. Binding over `/usr/bin/podman` and `/usr/bin/crun` blocks dpkg/apt from replacing those files (both packages are already apt-held); upgrading them means stopping the unit and re-measuring and re-signing everything that binds their digests.

Units come verbatim from `infra/guest/systemd/`: `usr-local-bin-tbound.mount`, `usr-bin-podman.mount`, `usr-bin-crun.mount`, `opt-tbound-cell.mount`. The last one is a directory bind and needs the registered template to name the `/opt/tbound/cell/...` paths (R5-1). Owner decision: the alternative is three more file-bind units over `/etc/tbound/{cosign.pub,seccomp.json,pi-cell-oci}` with the template left at its defaults.

```
GSSH 'sudo -n install -d -o root -g root -m 0755 /opt/tbound/tbound /opt/tbound/podman /opt/tbound/crun /opt/tbound/cell /opt/tbound/cell-src /opt/tbound/bin'
# backups for rollback (old runner b7f738d8..., old tbound-profile)
GSSH 'sudo -n cp -p /usr/local/bin/tbound /opt/tbound/bin/tbound.old && sudo -n cp -p /usr/local/bin/tbound-profile /opt/tbound/bin/tbound-profile.old'
GSSH 'sudo -n install -o root -g root -m 0555 /dev/stdin /opt/tbound/tbound/tbound' < $W/bin/tbound
GSSH 'sudo -n install -o root -g root -m 0755 /dev/stdin /usr/local/bin/.tbound-profile.new && sudo -n mv -f /usr/local/bin/.tbound-profile.new /usr/local/bin/tbound-profile' < $W/bin/tbound-profile
GSSH 'sudo -n install -o root -g root -m 0555 /usr/bin/podman /opt/tbound/podman/podman && sudo -n install -o root -g root -m 0555 /usr/bin/crun /opt/tbound/crun/crun'
GSSH 'sudo -n install -o root -g root -m 0555 /opt/tbound/toolchains/cosign-v3.1.3/cosign /opt/tbound/cell-src/cosign && sudo -n install -o root -g root -m 0444 /etc/tbound/cosign.pub /etc/tbound/seccomp.json /opt/tbound/cell-src/'
GSSH 'sudo -n cp -a /etc/tbound/pi-cell-oci /opt/tbound/cell-src/pi-cell-oci && sudo -n chmod -R go-w /opt/tbound/cell-src/pi-cell-oci && sudo -n chmod 0755 /opt/tbound/cell-src/pi-cell-oci && sudo -n find /opt/tbound/cell-src/pi-cell-oci -type d -exec chmod 0755 {} +'
GSSH 'sudo -n sha256sum /opt/tbound/podman/podman /opt/tbound/crun/crun /opt/tbound/cell-src/cosign /opt/tbound/cell-src/cosign.pub /opt/tbound/cell-src/seccomp.json /opt/tbound/tbound/tbound'
#   expect podman 859cb7e2..., crun bd8ad7e7..., cosign 4629c757..., cosign.pub b1179231..., seccomp cc374cf2..., tbound == P1
for u in usr-local-bin-tbound usr-bin-podman usr-bin-crun opt-tbound-cell; do
  GSSH "sudo -n install -o root -g root -m 0644 /dev/stdin /etc/systemd/system/$u.mount" < infra/guest/systemd/$u.mount ; done
GSSH 'cd /tmp && sudo -n systemctl daemon-reload && for u in usr-local-bin-tbound usr-bin-podman usr-bin-crun opt-tbound-cell; do systemd-analyze verify /etc/systemd/system/$u.mount && sudo -n systemctl enable --now $u.mount; done'
GSSH 'findmnt -n -o TARGET,OPTIONS /usr/local/bin/tbound /usr/bin/podman /usr/bin/crun /opt/tbound/cell; sha256sum /usr/local/bin/tbound /usr/bin/podman /usr/bin/crun /opt/tbound/cell/cosign /opt/tbound/cell/cosign.pub /opt/tbound/cell/seccomp.json'
#   expect every line ro,nosuid,nodev,relatime and the same hashes as the sources.
```

Rollback per unit: `sudo systemctl disable --now <unit>; sudo rm /etc/systemd/system/<unit>; sudo systemctl daemon-reload`; restore the old binaries from `/opt/tbound/bin/*.old` for tbound and tbound-profile (the original files stay under the mounts). Reboot persistence is not verified; check after the first reboot that all four units and the runtime/live units are active before relying on them.

### P7 [PRIVILEGED WRITE] reinstall the rebuilt runtime bundle, then re-measure

Replaces the contents of `/var/lib/tbound/runtime-src` (bound read-only at `/var/lib/tbound/runtime`). The tar was built with owner 0:0, so extracting as root yields root-owned files with the exact modes.

```
sha256sum BR/runtime-bundle-8e405ef0-lf-head8c2a3cb.tar            # expect d0abdaf3...
GSSH 'sudo -n systemctl stop var-lib-tbound-runtime.mount && sudo -n mv /var/lib/tbound/runtime-src /var/lib/tbound/runtime-src.old-d7367e1b && sudo -n install -d -o root -g root -m 0555 /var/lib/tbound/runtime-src'
GSSH 'sudo -n tar -x -p --numeric-owner -C /var/lib/tbound/runtime-src -f -' < BR/runtime-bundle-8e405ef0-lf-head8c2a3cb.tar
GSSH 'sudo -n systemctl start var-lib-tbound-runtime.mount; findmnt -n -o TARGET,OPTIONS /var/lib/tbound/runtime'        # ro,nosuid,nodev
GSSH 'SVC tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime'        # [READ-ONLY] expect sha256:8e405ef0...4f48 (NOT d7367e1b)
GSSH 'sudo -n sha256sum /var/lib/tbound/runtime/closure-manifest.json'                # compare with the closure-manifest.json inside the tar
```

Rollback: stop the mount, `rm -rf runtime-src`, `mv runtime-src.old-d7367e1b runtime-src`, start the mount (digest d7367e1b...6884). Remove `runtime-src.old-d7367e1b` only after the signed profile is installed and verified (it sits outside every measured root).

The old and new digests, and the xattr census of the runtime tree (R1-1), must be taken after this step; the `PiRuntimeBundleDigest` in the host profile is the new value.

## Row 1: calibration (B2 runtime read-only, B3 managed live root read-write)

### R1-1 [READ-ONLY] baselines (xattr-census-r9.py is in `abi8-xattr-source-scope-r9/helpers/`)

```
GSSH 'sudo -n python3 -I - /var/lib/tbound/runtime' < xattr-census-r9.py > $H/census-runtime-base.jsonl ; sha256sum $H/census-runtime-base.jsonl
GSSH 'sudo -n python3 -I - /var/lib/tbound/live'    < xattr-census-r9.py > $H/census-live-base.jsonl
GSSH 'SVC tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime'                  # expect the P7 value
GSSH 'SVC tbound-profile xattr-measure --source /var/lib/tbound/runtime'                         # expect read_only true, root_owned true
```

### R1-2 [PRIVILEGED WRITE] probe xattr on runtime-src (negative control first)

```
GSSH "sudo -n python3 -I -c \"import os;os.setxattr('/var/lib/tbound/runtime/.','user.tbound.calibration-probe',b'x')\""      # expect OSError 30 EROFS
GSSH "sudo -n python3 -I -c \"import os;os.setxattr('/var/lib/tbound/runtime-src','user.tbound.calibration-probe',b'abi8-ro-calibration-20261017')\""
```

### R1-3 [READ-ONLY + scratch write] read-only calibration measure

```
GSSH 'sudo -n python3 -I - /var/lib/tbound/runtime' < xattr-census-r9.py > $H/census-runtime-probe.jsonl
PCD_RO=sha256:$(sha256sum $H/census-runtime-probe.jsonl | cut -d' ' -f1)       # owner-reviewed; summary must show {"user":1} only
GSSH "SVC tbound-profile xattr-calibration-measure --source /var/lib/tbound/runtime --access-profile readonly-sealed-source/v1 --probe-corpus-digest $PCD_RO --out $SCR/cal-ro.unsigned.json"
```

### R1-4 [PRIVILEGED WRITE] remove the probe, prove zero

```
GSSH "sudo -n python3 -I -c \"import os;os.removexattr('/var/lib/tbound/runtime-src','user.tbound.calibration-probe')\""
GSSH 'sudo -n python3 -I - /var/lib/tbound/runtime' < xattr-census-r9.py | sha256sum        # expect == census-runtime-base.jsonl
```

Then repeat the three checks of R1-1; `xattr-calibration-measure` must refuse again.

### R1-5 [PRIVILEGED WRITE] read-write calibration on the live root (after P5)

```
GSSH 'sudo -n python3 -I - /var/lib/tbound/live' < xattr-census-r9.py > $H/census-live-base2.jsonl
GSSH "sudo -n sh -c 'set -e; D=/var/lib/tbound/live/tbound-xattr-probe-20261017; install -d -o 995 -g 986 -m 0700 \$D; install -o 995 -g 986 -m 0600 /dev/null \$D/plain.txt; python3 -I -c \"import os;os.setxattr(\\\"\$D/plain.txt\\\",\\\"user.tbound.calibration-probe\\\",b\\\"x\\\")\"'"
GSSH 'sudo -n python3 -I - /var/lib/tbound/live' < xattr-census-r9.py > $H/census-live-probe.jsonl
PCD_RW=sha256:$(sha256sum $H/census-live-probe.jsonl | cut -d' ' -f1)
GSSH "SVC tbound-profile xattr-calibration-measure --source /var/lib/tbound/live --access-profile managed-live-root-rw-under-recovered-lease/v1 --probe-corpus-digest $PCD_RW --out $SCR/cal-rw.unsigned.json"
GSSH "sudo -n rm -rf /var/lib/tbound/live/tbound-xattr-probe-20261017"
GSSH 'sudo -n python3 -I - /var/lib/tbound/live' < xattr-census-r9.py | sha256sum        # expect == census-live-base2.jsonl
```

### R1-6 [READ-ONLY] fetch the unsigned calibrations

```
GSSH "sudo -n cat $SCR/cal-ro.unsigned.json" > $H/cal-ro.unsigned.json
GSSH "sudo -n cat $SCR/cal-rw.unsigned.json" > $H/cal-rw.unsigned.json ; sha256sum $H/cal-*.unsigned.json
```

### R1-7 [OWNER SIGN] sign and verify both calibrations

```
TBP xattr-calibration-sign --calibration $H/cal-ro.unsigned.json --key <ADMIN_KEY_PATH> --out-sig $H/d09-xattr-enumeration-calibration-readonly.ed25519 --out-calibration $H/d09-xattr-enumeration-calibration-readonly.json
TBP xattr-calibration-sign --calibration $H/cal-rw.unsigned.json --key <ADMIN_KEY_PATH> --out-sig $H/d09-xattr-enumeration-calibration.ed25519          --out-calibration $H/d09-xattr-enumeration-calibration.json
TBP xattr-calibration-verify --calibration $H/d09-xattr-enumeration-calibration-readonly.json --sig $H/d09-xattr-enumeration-calibration-readonly.ed25519 --pub <ADMIN_PUB_PATH>   # [READ-ONLY]
TBP xattr-calibration-verify --calibration $H/d09-xattr-enumeration-calibration.json          --sig $H/d09-xattr-enumeration-calibration.ed25519          --pub <ADMIN_PUB_PATH>   # [READ-ONLY]
```

Record the RW calibration digest (bound by Row 2). Pending code fix B changes the xattr authority these calibrations support; if it alters the calibration format, redo R1-3 to R1-7 with the rebuilt tools.

### R1-8 [PRIVILEGED WRITE] install the four calibration files in /etc/tbound (root:root 0644), then verify

```
for f in d09-xattr-enumeration-calibration.json d09-xattr-enumeration-calibration.ed25519 d09-xattr-enumeration-calibration-readonly.json d09-xattr-enumeration-calibration-readonly.ed25519; do
  GSSH "sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/$f" < $H/$f ; done
GSSH 'tbound-profile xattr-calibration-verify --calibration /etc/tbound/d09-xattr-enumeration-calibration.json --sig /etc/tbound/d09-xattr-enumeration-calibration.ed25519 --pub /etc/tbound/pi-host-profile.pub'      # [READ-ONLY]; same for -readonly
```

## Row 2+6: managed live root measure and registration

### R2-1 [PRIVILEGED WRITE] create the one parent cgroup

`<service>/cells` is created by the runner at service start (e8a2004) and is not pre-created.

```
GSSH "sudo -n sh -c 'set -e; C=/sys/fs/cgroup/system.slice/tbound-pi.service; test ! -e \$C; mkdir \$C; echo \"+cpu +memory +pids\" > \$C/cgroup.subtree_control; chown 995:986 \$C \$C/cgroup.procs \$C/cgroup.threads \$C/cgroup.subtree_control; cat \$C/cgroup.type \$C/cgroup.subtree_control; wc -c < \$C/cgroup.procs'"      # expect domain / cpu memory pids / 0
```

### R2-2 [READ-ONLY + scratch write] measure (needs P6; the runner check passed only with the ro bind)

```
GSSH "sudo -n /usr/local/bin/tbound-profile managed-root-measure --root /var/lib/tbound/live --service-name tbound-pi --service-group tbound-pi --service-unit /etc/systemd/system/tbound-pi.service --mount-unit /etc/systemd/system/var-lib-tbound-live.mount --runner /usr/local/bin/tbound --service-cgroup /system.slice/tbound-pi.service --allowed-client-uids 994 --allowed-client-primary-gid 985 --out $SCR/managed-live-root.unsigned.json"
```

Expect `runner_digest` == P1 `tbound`, `managed_root_xattr_calibration_digest` == RW digest from R1-7, `service_uid` 995. Pending code fix A changes what the cgroup-root digest covers; re-measure with the rebuilt `tbound-profile` after it lands.

### R2-3 [PRIVILEGED WRITE] roll back R2-1, then [READ-ONLY] fetch

```
GSSH 'sudo -n rmdir /sys/fs/cgroup/system.slice/tbound-pi.service'
GSSH "sudo -n cat $SCR/managed-live-root.unsigned.json" > $H/managed-live-root.unsigned.json
```

### R2-4 [OWNER SIGN] sign and verify the registration

```
TBP managed-root-sign --registration $H/managed-live-root.unsigned.json --key <ADMIN_KEY_PATH> --out-sig $H/managed-live-root.ed25519 --out-registration $H/managed-live-root.json      # prints REGDIGEST
TBP managed-root-verify --registration $H/managed-live-root.json --sig $H/managed-live-root.ed25519 --pub <ADMIN_PUB_PATH>      # [READ-ONLY]
```

### R2-5 [PRIVILEGED WRITE] install the registration

```
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/managed-live-root.json'    < $H/managed-live-root.json
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/managed-live-root.ed25519' < $H/managed-live-root.ed25519
GSSH 'tbound-profile managed-root-verify --registration /etc/tbound/managed-live-root.json --sig /etc/tbound/managed-live-root.ed25519 --pub /etc/tbound/pi-host-profile.pub'      # [READ-ONLY]
```

## Row 5: D09 template, host profile, install

### R5-1 [READ-ONLY] build the measured template

```
TBP template-init --out $H/d09-cell.json
TBP landlock-digest --read-only /usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev --read-write /work,/tmp      # expect sha256:6bd3b65e...
TBP xattr-digest --filesystem-magic 61267 --mount-options-digest sha256:4ae60254afe183f9f8b49d2956ca8ee68ba00380a20111b8ccb12fed35581cb3 --uid 995 --gid 986      # expect sha256:aca635c0...
```

Fill every field (values and sources: `20-signflow.sh`, block S3). Changes relative to the dry run:

- `cosign_path` `/opt/tbound/cell/cosign`, `cosign_public_key_path` `/opt/tbound/cell/cosign.pub`, `seccomp_profile_path` `/opt/tbound/cell/seccomp.json`, `oci_layout_path` `/opt/tbound/cell/pi-cell-oci` (P6). Digests unchanged: cosign 4629c757..., cosign.pub b1179231..., seccomp cc374cf2..., expected image `sha256:20d5533983555ad7170b5a2065fb49ba4efcab0b27acbbc522bc8a00b0360203`.
- `entrypoint_digest` = `sha256:12679b05b6f5fc12ac86459b9aaa062f17d11e7bb2426e32e5f5646c48ba1ad7` (64 hex). This is the sha256 of the file `/usr/local/bin/tbound-pi-entrypoint` inside the signed image (`verifyEntrypointInImage`, `containment_linux.go:1127-1151`). The value at `abi8-runtime-provisioning-20261015.json:241` has 62 hex characters because two characters were dropped when it was copied (`12679b05f6fc...` instead of `12679b05b6f5fc...`); the receipt is not edited. A HEAD build of `cmd/tbound-cell-entrypoint` is `sha256:ce7c494531ce9e53d50c9fed0d64227c27ad49103cbcc295eff1e83c5d886fda` and does NOT match the signed image; using it requires rebuilding and re-signing the image (new `expected_image_digest`, new OCI layout).
- `xattr_visibility_authority`: pending code fix B (bounded authority string). Today only the complete-enumeration string validates.
- `managed_live_root_registration_digest` = REGDIGEST from R2-4; `podman_path` `/usr/bin/podman`, `crun_path` `/usr/bin/crun`.

```
TBP template-digest --template $H/d09-cell.json              # => TD
```

### R5-2 [OWNER SIGN] host profile

```
TBP init --out $H/pi-host-profile.json     # set ID, ExecutableDigest=sha256:7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c (Node), PiRuntimeBundleDigest=<P7 value, 8e405ef0...>, ContainmentProfileDigest=TD, CgroupRootDigest (UNRESOLVED: pending code fix A)
TBP sign --profile $H/pi-host-profile.json --key <ADMIN_KEY_PATH> --out-sig $H/pi-host-profile.ed25519 --out-pub $H/pi-host-profile.pub --out-profile $H/pi-host-profile.signed.json
cmp $H/pi-host-profile.pub <ADMIN_PUB_PATH>                  # [READ-ONLY] expect identical
TBP verify --profile $H/pi-host-profile.signed.json --sig $H/pi-host-profile.ed25519 --pub <ADMIN_PUB_PATH>      # [READ-ONLY]
```

### R5-3 [PRIVILEGED WRITE] install the profile and template (root:root 0644)

```
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/pi-host-profile.json'    < $H/pi-host-profile.signed.json
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/pi-host-profile.ed25519' < $H/pi-host-profile.ed25519
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/d09-cell.json'           < $H/d09-cell.json
```

### R5-4 [READ-ONLY] verify on the guest

```
GSSH 'tbound-profile verify --profile /etc/tbound/pi-host-profile.json --sig /etc/tbound/pi-host-profile.ed25519 --pub /etc/tbound/pi-host-profile.pub; tbound-profile template-digest --template /etc/tbound/d09-cell.json'
```

The printed template digest must equal `ContainmentProfileDigest` in the profile.

## Closing

### Z0 [PRIVILEGED WRITE] remove scratch

```
GSSH 'sudo -n rm -rf /var/tmp/tbound-cal-20261017'
```

### Z1 [PRIVILEGED WRITE] revoke passwordless sudo (last sudo command, before the first cell launch)

The owner names the tboundadmin NOPASSWD drop-in under `/etc/sudoers.d/`; no agent reads other sudoers files.

```
GSSH 'sudo -n rm /etc/sudoers.d/<TBOUNDADMIN_NOPASSWD_DROPIN>'
GSSH 'sudo -n true'        # [READ-ONLY] must now fail ("a password is required")
```

Passwordless sudo is never restored. Every later privileged change is a console session with the owner present, followed by a re-measure and, where a bound digest changes, a re-sign.
