# ABI8 guest: owner-approvable provisioning and signing sequence

Status: final draft for owner approval, 2026-10-17. Based on the throwaway-key dry run (`F:\TBoundAssets\recovery-20261015\sign-dryrun-20261017\owner-sequence.sh.txt`) and the code fixes in `23a65ba`. Nothing here has been run on the guest except steps the dry run marked DRYRUN-OK. This runbook supersedes `docs/governed-setup.md` section 225-236 for the ABI8 guest (that section is stale: it predates the single `tbound-pi` service identity, the ro-mounted executables, the split calibration roles and the signed host profile flow).

## Tags and rules

- `[READ-ONLY]` changes nothing on the guest or the owner machine except new private scratch files.
- `[PRIVILEGED WRITE]` changes the guest (root through `sudo -n` until Z1; owner console after). Each has a rollback line.
- `[OWNER SIGN]` uses a private key (`<ADMIN_KEY_PATH>` or `<COSIGN_KEY_PATH>`). An agent never reads, copies or logs a key or its passphrase.

1. Every `[PRIVILEGED WRITE]` and `[OWNER SIGN]` step needs explicit owner approval, one step at a time. Approval of one is not approval of the next.
2. Passwordless sudo is revoked at Z1, after provisioning and before the first cell launch, and is never restored. Nothing after Z1 uses sudo.
3. Part L (first launch) is a separate approval; it is not part of provisioning.
4. Signature output paths must be new absolute canonical paths; in-place signing is refused.
5. The frozen commit `F` (step F0) is the only source. Any change to a build input after F0 invalidates every binary, digest, image and signature below; start again from F0.

## Owner signing operations (7 key operations: 5 signatures, 2 public-key derivations)

| ID | Step | Key | Artifact |
| --- | --- | --- | --- |
| K1 | P4 | admin key `<ADMIN_KEY_PATH>` | read only: derive `<ADMIN_PUB_PATH>` (no signature) |
| K2 | P5 | cosign key `<COSIGN_KEY_PATH>` | read only: export `cosign.pub` (no signature) |
| S1 | R1-7 | admin key | read-only-source xattr calibration `d09-xattr-enumeration-calibration-readonly` |
| S2 | R1-7 | admin key | managed-live-root xattr calibration `d09-xattr-enumeration-calibration` |
| S3 | R2-4 | admin key | managed live root registration `managed-live-root` |
| S4 | R5-2 | admin key | host profile `pi-host-profile` |
| S5 | P6 | cosign key | cell image, by manifest digest `IMGDIGEST` (`cosign sign`) |

The admin key is the raw 64-byte Ed25519 key. S1 to S4 are domain-separated. The calibration and registration signatures are v2 (`authority` field); old v1 calibrations no longer verify.

## Blockers (nothing in the repo resolves these; they are not invented away)

- B1 Network. The image build needs network: `packaging/governed/Containerfile` pulls `golang:1.27.1-bookworm` and `node:24.21.0-bookworm-slim` by mutable tag and runs `npm install --omit=dev` inside the build; the signing helper pulls `registry:2`. The guest podman store holds only the old `localhost/tbound/pi-cell:latest` (read-only check 2026-10-17); no base image is cached. There is no offline image build path in the repo. P3 and P6 cannot run until the owner approves a network window or supplies the base images and npm cache.
- B2 Signing with the owner key is unproven. The only procedure is the helper `F:\TBoundAssets\recovery-20261015\abi8-runtime-provisioning-20261015\helpers\image-sign-offline-verify.sh`, which generated an ephemeral key on the guest and signed through a loopback `registry:2` (cosign v3 cannot sign an `oci:` layout directly). The owner key must not be copied to the guest, so P6 runs on the owner machine against a loopback registry there. Dry-run it with a throwaway key first.
- B3 The image must exist in the service's podman store. `selectImage` (`containment_linux.go:1244`) needs `localhost/tbound/pi-cell:latest` in `/var/lib/tbound/podman-storage/graphroot` with a RepoDigest equal to `IMGDIGEST`. That directory is empty today (checked read-only 2026-10-17) and no repo step loads it. I1 states the requirement; the load mechanism must be defined and proven on a throwaway store before approval. It must happen before Z1.
- B4 The client invocation for the positive launch and the drain observation (Part L) are not documented anywhere in the repo; the owner supplies them.

## Placeholders

```
GSSH            = sh /f/TBoundAssets/recovery-20261015/remeasure-20261017/gssh2.sh   (Git Bash ssh, tboundadmin@172.22.198.27, HostKeyAlias=172.18.155.19, pinned known_hosts, StrictHostKeyChecking=yes)
SVC             = sudo -n setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs --
TBP             = the frozen-commit tbound-profile on the owner machine (P1)
F               = the frozen commit (40 hex, F0)
W               = owner-side build dir, absolute, canonical, mode 0700 (unique name, removed at the end)
H               = owner-side signing dir, absolute, canonical, mode 0700
SCR             = /var/tmp/tbound-cal-20261017 (guest scratch, 995:986 0700)
BR              = F:\TBoundAssets\recovery-20261015\bundle-rebuild-20261017  (/f/... in Git Bash, /mnt/f/... in WSL)
IMGDIGEST       = signed image manifest digest (P6)
<ADMIN_KEY_PATH>, <ADMIN_PUB_PATH>, <COSIGN_KEY_PATH>  owner-held; never read by an agent
```

## F0 [READ-ONLY] freeze

Pick one commit, record it, build everything from it.

```
git status --porcelain -- supervisor adapter scripts packaging infra        # must print nothing
F=$(git rev-parse --verify 'HEAD^{commit}'); echo $F > $H/FROZEN-COMMIT       # owner records the 40-hex hash here
```

Use `$F`, never `HEAD`, from here on. Before each later part run `git diff --quiet $F HEAD -- supervisor adapter scripts packaging infra`; any difference voids the run. Doc-only changes outside those paths do not. Every `git archive` uses `-c core.autocrlf=false -c core.eol=lf`.

## Part P: preparation

### P1 [READ-ONLY] build the binaries from F

```
git -c core.autocrlf=false -c core.eol=lf archive --format=tar $F supervisor scripts adapter/src adapter/package.json adapter/package-lock.json adapter/pi-runtime-package.json packaging infra | (mkdir -p $W/src && tar -x -C $W/src)
cd $W/src/supervisor && for c in tbound tbound-profile tbound-cell-entrypoint; do
  GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=file:///home/jeli2k/go/pkg/mod/cache/download GOSUMDB=off CGO_ENABLED=0 \
    /home/jeli2k/opt/go1.27.1/bin/go build -trimpath -o $W/bin/$c ./cmd/$c ; done
sha256sum $W/bin/*        # build twice (separate dirs, umask 077); the two passes must be byte-identical
```

Record `tbound` (runner) and `tbound-profile` digests; the runner digest is bound by R2. `$W/bin/tbound-cell-entrypoint` is the offline reference for the in-image entrypoint (P3); the toolchain build matched the container build byte-for-byte for the previous image.

### P2 [READ-ONLY] rebuild the runtime bundle from F (LF)

Scripts and tooling are in `BR` (`10-setup.sh`, `20-bundle.sh`, `40-go-and-digest.sh`, `50-package.sh`, `99-cleanup.sh`); they run in WSL as jeli2k, offline, with Node v24.21.0 (sha256 `7fde7b8a...df4c`), a private copy of the npm cache and `scripts/prepare_official_pi_runtime.py`. They are pinned to 8c2a3cb: regenerate `inputs/head-src.tar` from `$F` with the LF archive command in P1 (do not use `head-src-autocrlf-default-config.tar`), and rename the output tar in `50-package.sh`/`51-verify-extract.sh` for `$F`. Build twice; the tars must be byte-identical; measure with the real `tbound-profile runtime-bundle-digest` under a read-only bind. Dirs 0555, files 0555/0444, deterministic tar (`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner`).

Record `BUNDLE_DIGEST` and the tar sha256. Cross-check only: at 8c2a3cb the LF build was `sha256:8e405ef0f2a3a3ed725dba2815d71bd8e5db007fe993f4ac3979ad59016d4f48`; no bundle input (`adapter/src`, `adapter/pi-runtime-package.json`, `scripts/prepare_official_pi_runtime.py`) differs between 8c2a3cb and 4a668e7, so the same value is expected at F if they still do not. A different value is not an error, but it is the value to pin. The currently installed bundle (`sha256:d7367e1b...6884`) predates the model pin and is replaced in P11.

### P3 [READ-ONLY] build the cell image from F (new private scratch only; BLOCKED by B1)

Procedure: `packaging/governed/Containerfile` plus the previous helper `abi8-runtime-provisioning-20261015/helpers/image-build.sh`, re-pointed at a new scratch dir `/home/tboundadmin/tbound-build-<F first 8 hex>`. Rootless podman as tboundadmin, no sudo; it writes only that directory, tboundadmin's own rootless store and (if absent) tboundadmin's `~/.config/containers/registries.conf` for short-name resolution. Needs owner approval of the network window (B1).

```
# stage the F source (same LF tree as P1) into the guest scratch dir
tar -C $W/src -c supervisor adapter packaging | GSSH 'umask 077; B=$HOME/tbound-build-<F8>; mkdir $B $B/src && tar -x -C $B/src'
GSSH 'cd $HOME/tbound-build-<F8>/src && podman build --file packaging/governed/Containerfile --tag localhost/tbound/pi-cell:latest --layers .'
# measure from a created (not run) container
GSSH 'CID=$(podman create localhost/tbound/pi-cell:latest); for p in usr/local/bin/tbound-pi-entrypoint usr/local/bin/node bin/bash; do podman cp $CID:/$p $B/measure/$(basename $p); done; podman rm $CID; sha256sum $B/measure/*'
GSSH 'podman save --format oci-dir -o $B/pi-cell-oci localhost/tbound/pi-cell:latest'
```

Record from the measurement:

- `ENTRYPOINT_DIGEST` = sha256 of `/usr/local/bin/tbound-pi-entrypoint` in the image. It must equal `sha256sum $W/bin/tbound-cell-entrypoint` from P1 (independent offline cross-check). If they differ, stop.
- `NODE` in the image must equal the host Node `7fde7b8a...df4c` (`verifyImageWorkerExecutable`).
- `BASH_DIGEST` = sha256 of `/bin/bash` in the image. The base image is a mutable tag, so this may differ from the previous `55b89ab2...`; the new value is pinned as `command_executable_digest`.

The image digest is NOT reproducible (mutable base tags, npm, timestamps). Only the one image that is signed in P6 is pinned.

The previous entrypoint digest `12679b05b6f5fc12ac86459b9aaa062f17d11e7bb2426e32e5f5646c48ba1ad7` belonged to the old image (built from 93ede26) and is not used going forward. The 62-hex value in `abi8-runtime-provisioning-20261015.json:241` is a copy error (two characters dropped); that receipt is not edited.

### P4 [OWNER SIGN] K1: derive the admin public key (key read, no signature)

```
tail -c 32 <ADMIN_KEY_PATH> > <ADMIN_PUB_PATH>; stat -c %s <ADMIN_PUB_PATH>        # expect 32
```

### P5 [OWNER SIGN] K2: export the cosign public key (key read, no signature)

```
cosign public-key --key <COSIGN_KEY_PATH> --outfile $H/cosign.pub        # passphrase entered by the owner only
sha256sum $H/cosign.pub        # record as COSIGN_PUB_DIGEST (replaces the old ephemeral-key value b1179231...)
```

### P6 [OWNER SIGN] S5: sign the cell image, export the offline layout (BLOCKED by B1, B2)

Runs on the owner machine, never on the guest. Flags follow the previous helper; push `$B/pi-cell-oci` from P3 (transferred to the owner machine) into a loopback registry, sign by digest, save the layout.

```
podman run -d --name tbound-sign-registry -p 127.0.0.1:5000:5000 docker.io/library/registry:2
podman push --tls-verify=false --format oci --digestfile $H/pushed.digest <image from the transferred oci-dir> 127.0.0.1:5000/tbound/pi-cell:latest
IMGDIGEST=$(cat $H/pushed.digest)
cosign sign --key <COSIGN_KEY_PATH> --tlog-upload=false --use-signing-config=false --allow-http-registry=true --allow-insecure-registry=true --yes 127.0.0.1:5000/tbound/pi-cell@$IMGDIGEST
cosign save --dir $H/pi-cell-oci --allow-http-registry=true --allow-insecure-registry=true 127.0.0.1:5000/tbound/pi-cell@$IMGDIGEST
podman rm -f tbound-sign-registry
```

`IMGDIGEST` is the registry manifest digest and the only image digest that is pinned (it differs from the guest store digest; the previous pair was `20d55339...` vs `ac72b2d7...`). The loaded service-store image must carry the same RepoDigest (B3).

### P7 [READ-ONLY] verify the signed layout

Run the exact form the launcher uses (`containment_linux.go:1299`), offline, with the pinned guest cosign v3.1.3 where possible:

```
cosign verify --offline --key $H/cosign.pub --local-image $H/pi-cell-oci        # docker-manifest-digest must equal IMGDIGEST
grep -c $IMGDIGEST $H/pi-cell-oci/index.json                                    # manifest digest present in the layout index
```

### P8 [PRIVILEGED WRITE] install the trust anchor before Row 1

Writes `/etc/tbound/pi-host-profile.pub` (root:root 0644) and the scratch dir.

```
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/pi-host-profile.pub' < <ADMIN_PUB_PATH>
GSSH 'sudo -n sha256sum /etc/tbound/pi-host-profile.pub'; sha256sum <ADMIN_PUB_PATH>        # [READ-ONLY] expect equal
GSSH 'sudo -n install -d -o 995 -g 986 -m 0700 /var/tmp/tbound-cal-20261017'
```

Rollback: `sudo rm /etc/tbound/pi-host-profile.pub; sudo rm -rf /var/tmp/tbound-cal-20261017`.

### P9 [PRIVILEGED WRITE] make the live root enumerable by uid 995

uid 995 cannot open `lost+found` (root 0700), so every 995 enumeration of the live root fails (`xattr_profile_linux.go:724`).

```
GSSH 'sudo -n chown 995:986 /var/lib/tbound/live/lost+found && sudo -n stat -c "%U:%G %a" /var/lib/tbound/live/lost+found'      # expect tbound-pi:tbound-pi 700
```

Rollback: `sudo chown root:root /var/lib/tbound/live/lost+found`.

### P10 [PRIVILEGED WRITE] executables and cell assets on read-only mounts

All fail closed through `requireReadOnlyMount` (`launcher_linux.go:1007`):

| Item | Pinned path | Checked at |
| --- | --- | --- |
| runner | `/usr/local/bin/tbound` | `d09_template_linux.go:269`; launch `managed_live_root_linux.go:1276` |
| podman | `/usr/bin/podman` | `containment_linux.go:995` -> `:2498` |
| crun | `/usr/bin/crun` | `containment_linux.go:1008` -> `:2498` |
| cosign | `/opt/tbound/cell/cosign` | `containment_linux.go:1022` -> `:2498` |
| cosign.pub | `/opt/tbound/cell/cosign.pub` | `containment_linux.go:1296` |
| seccomp.json | `/opt/tbound/cell/seccomp.json` | `containment_linux.go:1517` |
| OCI layout dir | `/opt/tbound/cell/pi-cell-oci` | `containment_linux.go:1373` |

Not required: `tbound-profile`, `conmon`, `/bin/bash` (measured inside the image). Binding over `/usr/bin/podman` and `/usr/bin/crun` blocks dpkg/apt from replacing them (both are apt-held); upgrading means stopping the unit and re-measuring and re-signing everything that binds their digests.

Owner decision 2026-10-17: the cell assets live in `/opt/tbound/cell`, a single read-only directory bind (`opt-tbound-cell.mount`, from `/opt/tbound/cell-src`). Units come verbatim from `$W/src/infra/guest/systemd/`: `usr-local-bin-tbound.mount`, `usr-bin-podman.mount`, `usr-bin-crun.mount`, `opt-tbound-cell.mount`.

```
GSSH 'sudo -n install -d -o root -g root -m 0755 /opt/tbound/tbound /opt/tbound/podman /opt/tbound/crun /opt/tbound/cell /opt/tbound/cell-src /opt/tbound/bin'
GSSH 'sudo -n cp -p /usr/local/bin/tbound /opt/tbound/bin/tbound.old && sudo -n cp -p /usr/local/bin/tbound-profile /opt/tbound/bin/tbound-profile.old'
GSSH 'sudo -n install -o root -g root -m 0555 /dev/stdin /opt/tbound/tbound/tbound' < $W/bin/tbound
GSSH 'sudo -n install -o root -g root -m 0755 /dev/stdin /usr/local/bin/.tbound-profile.new && sudo -n mv -f /usr/local/bin/.tbound-profile.new /usr/local/bin/tbound-profile' < $W/bin/tbound-profile
GSSH 'sudo -n install -o root -g root -m 0555 /usr/bin/podman /opt/tbound/podman/podman && sudo -n install -o root -g root -m 0555 /usr/bin/crun /opt/tbound/crun/crun'
GSSH 'sudo -n install -o root -g root -m 0555 /opt/tbound/toolchains/cosign-v3.1.3/cosign /opt/tbound/cell-src/cosign && sudo -n install -o root -g root -m 0444 /etc/tbound/seccomp.json /opt/tbound/cell-src/seccomp.json'
GSSH 'sudo -n install -o root -g root -m 0444 /dev/stdin /opt/tbound/cell-src/cosign.pub' < $H/cosign.pub
tar -C $H -c pi-cell-oci | GSSH 'sudo -n tar -x --no-same-owner -C /opt/tbound/cell-src'
GSSH 'sudo -n chown -R root:root /opt/tbound/cell-src/pi-cell-oci && sudo -n find /opt/tbound/cell-src/pi-cell-oci -type d -exec chmod 0755 {} + && sudo -n find /opt/tbound/cell-src/pi-cell-oci -type f -exec chmod 0444 {} +'
GSSH 'sudo -n sha256sum /opt/tbound/podman/podman /opt/tbound/crun/crun /opt/tbound/cell-src/cosign /opt/tbound/cell-src/cosign.pub /opt/tbound/cell-src/seccomp.json /opt/tbound/tbound/tbound'
#   expect podman 859cb7e2..., crun bd8ad7e7..., cosign 4629c757..., seccomp cc374cf2..., tbound == P1, cosign.pub == COSIGN_PUB_DIGEST
for u in usr-local-bin-tbound usr-bin-podman usr-bin-crun opt-tbound-cell; do
  GSSH "sudo -n install -o root -g root -m 0644 /dev/stdin /etc/systemd/system/$u.mount" < $W/src/infra/guest/systemd/$u.mount ; done
GSSH 'cd /tmp && sudo -n systemctl daemon-reload && for u in usr-local-bin-tbound usr-bin-podman usr-bin-crun opt-tbound-cell; do systemd-analyze verify /etc/systemd/system/$u.mount && sudo -n systemctl enable --now $u.mount; done'
GSSH 'findmnt -n -o TARGET,OPTIONS /usr/local/bin/tbound /usr/bin/podman /usr/bin/crun /opt/tbound/cell; sha256sum /usr/local/bin/tbound /usr/bin/podman /usr/bin/crun /opt/tbound/cell/cosign /opt/tbound/cell/cosign.pub /opt/tbound/cell/seccomp.json'        # [READ-ONLY] every line ro,nosuid,nodev,relatime; hashes equal the sources
GSSH 'sudo -n /opt/tbound/cell/cosign verify --offline --key /opt/tbound/cell/cosign.pub --local-image /opt/tbound/cell/pi-cell-oci'        # [READ-ONLY] same as P7, now from the mount; docker-manifest-digest == IMGDIGEST
```

Rollback per unit: `sudo systemctl disable --now <unit>; sudo rm /etc/systemd/system/<unit>; sudo systemctl daemon-reload`; restore `/opt/tbound/bin/tbound.old` and `tbound-profile.old` over the live files; remove `/opt/tbound/cell-src/*` and the new directories if empty. The old ephemeral-key assets `/etc/tbound/cosign.pub` and `/etc/tbound/pi-cell-oci` are left untouched and unreferenced. Reboot persistence is not verified; after the first reboot check that all four units and the runtime/live units are active before relying on them.

### P11 [PRIVILEGED WRITE] reinstall the rebuilt runtime bundle, then re-measure

Replaces `/var/lib/tbound/runtime-src` (bound read-only at `/var/lib/tbound/runtime`). The tar was built with owner 0:0, so extracting as root gives root-owned files with the exact modes.

```
sha256sum <bundle tar from P2>                                    # [READ-ONLY] equals the P2 record
GSSH 'sudo -n systemctl stop var-lib-tbound-runtime.mount && sudo -n mv /var/lib/tbound/runtime-src /var/lib/tbound/runtime-src.old-d7367e1b && sudo -n install -d -o root -g root -m 0555 /var/lib/tbound/runtime-src'
GSSH 'sudo -n tar -x -p --numeric-owner -C /var/lib/tbound/runtime-src -f -' < <bundle tar from P2>
GSSH 'sudo -n systemctl start var-lib-tbound-runtime.mount; findmnt -n -o TARGET,OPTIONS /var/lib/tbound/runtime'        # ro,nosuid,nodev
GSSH 'SVC tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime'        # [READ-ONLY] must equal BUNDLE_DIGEST (P2), not d7367e1b
GSSH 'sudo -n sha256sum /var/lib/tbound/runtime/closure-manifest.json'                # [READ-ONLY] equals the file inside the tar
```

Rollback: stop the mount, `rm -rf runtime-src`, `mv runtime-src.old-d7367e1b runtime-src`, start the mount (digest `d7367e1b...6884`). Remove `runtime-src.old-d7367e1b` only after the signed profile is installed and verified (it is outside every measured root).

## Row 1: calibration (B2 runtime read-only, B3 managed live root read-write)

Both calibrations are made in place by the root writer against the live trees (R1-2, R1-5). That replaces the cell leg; D2 (cell-based calibration) is out of scope. The calibrations carry `authority = root-attested-bounded-no-hidden-trusted/v1`, a bounded claim: a non-root scanner cannot list `trusted.*`, so silence about `trusted.*` rests on root's attestation, not on enumeration. It is not a complete-enumeration claim, and that is the only authority string the code accepts.

### R1-1 [READ-ONLY] baselines (`xattr-census-r9.py` is in `abi8-xattr-source-scope-r9/helpers/`)

```
GSSH 'sudo -n python3 -I - /var/lib/tbound/runtime' < xattr-census-r9.py > $H/census-runtime-base.jsonl ; sha256sum $H/census-runtime-base.jsonl
GSSH 'sudo -n python3 -I - /var/lib/tbound/live'    < xattr-census-r9.py > $H/census-live-base.jsonl
GSSH 'SVC tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime'                  # expect BUNDLE_DIGEST
GSSH 'SVC tbound-profile xattr-measure --source /var/lib/tbound/runtime'                         # expect read_only true, root_owned true
```

### R1-2 [PRIVILEGED WRITE] probe xattr on runtime-src (negative control first)

```
GSSH "sudo -n python3 -I -c \"import os;os.setxattr('/var/lib/tbound/runtime/.','user.tbound.calibration-probe',b'x')\""      # expect OSError 30 EROFS
GSSH "sudo -n python3 -I -c \"import os;os.setxattr('/var/lib/tbound/runtime-src','user.tbound.calibration-probe',b'abi8-ro-calibration-20261017')\""
```

Rollback: R1-4.

### R1-3 [READ-ONLY] read-only calibration measure (scratch write)

```
GSSH 'sudo -n python3 -I - /var/lib/tbound/runtime' < xattr-census-r9.py > $H/census-runtime-probe.jsonl
PCD_RO=sha256:$(sha256sum $H/census-runtime-probe.jsonl | cut -d' ' -f1)       # owner-reviewed; summary must show {"user":1} only
GSSH "SVC tbound-profile xattr-calibration-measure --source /var/lib/tbound/runtime --access-profile readonly-sealed-source/v1 --probe-corpus-digest $PCD_RO --out $SCR/cal-ro.unsigned.json"
```

### R1-4 [PRIVILEGED WRITE] remove the probe, prove zero

```
GSSH "sudo -n python3 -I -c \"import os;os.removexattr('/var/lib/tbound/runtime-src','user.tbound.calibration-probe')\""
GSSH 'sudo -n python3 -I - /var/lib/tbound/runtime' < xattr-census-r9.py | sha256sum        # [READ-ONLY] expect == census-runtime-base.jsonl
```

Then repeat the three checks of R1-1; `xattr-calibration-measure` must refuse again. Rollback: this step is the rollback of R1-2; on failure retry the same command (no other state changed).

### R1-5 [PRIVILEGED WRITE] read-write calibration on the live root (after P9)

```
GSSH 'sudo -n python3 -I - /var/lib/tbound/live' < xattr-census-r9.py > $H/census-live-base2.jsonl
GSSH "sudo -n sh -c 'set -e; D=/var/lib/tbound/live/tbound-xattr-probe-20261017; install -d -o 995 -g 986 -m 0700 \$D; install -o 995 -g 986 -m 0600 /dev/null \$D/plain.txt; python3 -I -c \"import os;os.setxattr(\\\"\$D/plain.txt\\\",\\\"user.tbound.calibration-probe\\\",b\\\"x\\\")\"'"
GSSH 'sudo -n python3 -I - /var/lib/tbound/live' < xattr-census-r9.py > $H/census-live-probe.jsonl
PCD_RW=sha256:$(sha256sum $H/census-live-probe.jsonl | cut -d' ' -f1)
GSSH "SVC tbound-profile xattr-calibration-measure --source /var/lib/tbound/live --access-profile managed-live-root-rw-under-recovered-lease/v1 --probe-corpus-digest $PCD_RW --out $SCR/cal-rw.unsigned.json"
GSSH "sudo -n rm -rf /var/lib/tbound/live/tbound-xattr-probe-20261017"
GSSH 'sudo -n python3 -I - /var/lib/tbound/live' < xattr-census-r9.py | sha256sum        # [READ-ONLY] expect == census-live-base2.jsonl
```

Rollback: `sudo rm -rf /var/lib/tbound/live/tbound-xattr-probe-20261017` (the in-step cleanup).

### R1-6 [READ-ONLY] fetch the unsigned calibrations

```
GSSH "sudo -n cat $SCR/cal-ro.unsigned.json" > $H/cal-ro.unsigned.json
GSSH "sudo -n cat $SCR/cal-rw.unsigned.json" > $H/cal-rw.unsigned.json ; sha256sum $H/cal-*.unsigned.json
```

### R1-7 [OWNER SIGN] S1, S2: sign and verify both calibrations

```
TBP xattr-calibration-sign --calibration $H/cal-ro.unsigned.json --key <ADMIN_KEY_PATH> --out-sig $H/d09-xattr-enumeration-calibration-readonly.ed25519 --out-calibration $H/d09-xattr-enumeration-calibration-readonly.json
TBP xattr-calibration-sign --calibration $H/cal-rw.unsigned.json --key <ADMIN_KEY_PATH> --out-sig $H/d09-xattr-enumeration-calibration.ed25519          --out-calibration $H/d09-xattr-enumeration-calibration.json
TBP xattr-calibration-verify --calibration $H/d09-xattr-enumeration-calibration-readonly.json --sig $H/d09-xattr-enumeration-calibration-readonly.ed25519 --pub <ADMIN_PUB_PATH>   # [READ-ONLY]
TBP xattr-calibration-verify --calibration $H/d09-xattr-enumeration-calibration.json          --sig $H/d09-xattr-enumeration-calibration.ed25519          --pub <ADMIN_PUB_PATH>   # [READ-ONLY]
```

A verify with the wrong public key now fails with the distinct signature error ("does not verify under the supplied public key"), not the generic template error; treat it as a key mix-up. Record the RW calibration digest (bound by Row 2).

### R1-8 [PRIVILEGED WRITE] install the four calibration files in /etc/tbound (root:root 0644), then verify

```
for f in d09-xattr-enumeration-calibration.json d09-xattr-enumeration-calibration.ed25519 d09-xattr-enumeration-calibration-readonly.json d09-xattr-enumeration-calibration-readonly.ed25519; do
  GSSH "sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/$f" < $H/$f ; done
GSSH 'tbound-profile xattr-calibration-verify --calibration /etc/tbound/d09-xattr-enumeration-calibration.json --sig /etc/tbound/d09-xattr-enumeration-calibration.ed25519 --pub /etc/tbound/pi-host-profile.pub'      # [READ-ONLY]; same for -readonly
```

Rollback: `sudo rm /etc/tbound/d09-xattr-enumeration-calibration{,-readonly}.{json,ed25519}`.

## Row 2+6: managed live root measure and registration

### R2-1 [PRIVILEGED WRITE] create the one parent cgroup (temporary, for measurement only)

`<service>/cells` is created by the runner at service start and is not pre-created here.

```
GSSH "sudo -n sh -c 'set -e; C=/sys/fs/cgroup/system.slice/tbound-pi.service; test ! -e \$C; mkdir \$C; echo \"+cpu +memory +pids\" > \$C/cgroup.subtree_control; chown 995:986 \$C \$C/cgroup.procs \$C/cgroup.threads \$C/cgroup.subtree_control; cat \$C/cgroup.type \$C/cgroup.subtree_control; wc -c < \$C/cgroup.procs'"      # expect domain / cpu memory pids / 0
```

Rollback: R2-3.

### R2-2 [READ-ONLY] measure (scratch write; needs P10, the runner check passes only with the ro bind)

```
GSSH "sudo -n /usr/local/bin/tbound-profile managed-root-measure --root /var/lib/tbound/live --service-name tbound-pi --service-group tbound-pi --service-unit /etc/systemd/system/tbound-pi.service --mount-unit /etc/systemd/system/var-lib-tbound-live.mount --runner /usr/local/bin/tbound --service-cgroup /system.slice/tbound-pi.service --allowed-client-uids 994 --allowed-client-primary-gid 985 --out $SCR/managed-live-root.unsigned.json"
```

Expect `runner_digest` == P1 `tbound`, `managed_root_xattr_calibration_digest` == RW digest from R1-7, `service_uid` 995.

### R2-3 [PRIVILEGED WRITE] roll back R2-1, then fetch

```
GSSH 'sudo -n rmdir /sys/fs/cgroup/system.slice/tbound-pi.service'
GSSH "sudo -n cat $SCR/managed-live-root.unsigned.json" > $H/managed-live-root.unsigned.json        # [READ-ONLY]
```

Rollback: none needed; it removes only the empty directory R2-1 made (systemd recreates the unit cgroup at service start).

### R2-4 [OWNER SIGN] S3: sign and verify the registration

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

Rollback: `sudo rm /etc/tbound/managed-live-root.json /etc/tbound/managed-live-root.ed25519`.

## Row 5: D09 template, host profile, install

### R5-1 [READ-ONLY] build the measured template

```
TBP template-init --out $H/d09-cell.json
TBP landlock-digest --read-only /usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev --read-write /work,/tmp      # expect sha256:6bd3b65e...
TBP xattr-digest --filesystem-magic 61267 --mount-options-digest sha256:4ae60254afe183f9f8b49d2956ca8ee68ba00380a20111b8ccb12fed35581cb3 --uid 995 --gid 986      # recompute xattr_visibility_profile_digest with the frozen build; previous aca635c0...
TBP cgroup-root-digest --uid 995 --cgroups-path /system.slice/tbound-pi.service/cells        # => CGROUP_ROOT_DIGEST
```

`cgroup-root-digest` is derived, not measured. Label `tbound-d09-cgroup-root/v2`; it hashes the canonical cells path, `cgroup2`, the service uid and `cpu,memory,pids`, and binds no inode, device or mount id, so it stays valid across service restarts. `--cgroups-path` defaults to `/system.slice/tbound-pi.service/cells`. Hand-derived expectation for uid 995: `sha256:8c64df4047d2e49832d3fd5334c06eca57a9812c2bb374c26ab8a947ac2e03ce` (the tool output is authoritative; a mismatch means the frozen build differs from what this runbook assumes).

Fill every other field as in the dry run (`20-signflow.sh`, block S3). Fields that change from the dry run:

| Template field | Dry-run value | Final value |
| --- | --- | --- |
| `cosign_path` | `/opt/tbound/toolchains/cosign-v3.1.3/cosign` | `/opt/tbound/cell/cosign` |
| `cosign_public_key_path` | `/etc/tbound/cosign.pub` (default) | `/opt/tbound/cell/cosign.pub` |
| `seccomp_profile_path` | `/etc/tbound/seccomp.json` (default) | `/opt/tbound/cell/seccomp.json` |
| `oci_layout_path` | `/etc/tbound/pi-cell-oci` (default) | `/opt/tbound/cell/pi-cell-oci` |
| `cosign_public_key_digest` | `sha256:b1179231...` (ephemeral key) | `COSIGN_PUB_DIGEST` (P5) |
| `expected_image_digest` | `sha256:20d55339...` | `IMGDIGEST` (P6) |
| `entrypoint_digest` | `sha256:12679b05...` (old image) | `ENTRYPOINT_DIGEST` (P3, built from F) |
| `command_executable_digest` | `sha256:55b89ab2...` | `BASH_DIGEST` (P3) |
| `xattr_visibility_authority` | complete-enumeration string (refused now) | `root-attested-bounded-no-hidden-trusted/v1` |
| `managed_live_root_registration_digest` | dry-run value | REGDIGEST (R2-4) |

Unchanged: `cosign_digest` `4629c757...`, `seccomp_profile_digest` `cc374cf2...`, `podman_path` `/usr/bin/podman` (`859cb7e2...`), `crun_path` `/usr/bin/crun` (`bd8ad7e7...`), `image` `localhost/tbound/pi-cell:latest`, `cgroups_path` `/sys/fs/cgroup/system.slice/tbound-pi.service/cells`. The `cosign_path` digest is the same binary; only its path moves.

```
TBP template-digest --template $H/d09-cell.json              # => TD
```

### R5-2 [OWNER SIGN] S4: host profile

```
TBP init --out $H/pi-host-profile.json     # set ID, ExecutableDigest=sha256:7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c (Node), PiRuntimeBundleDigest=BUNDLE_DIGEST, ContainmentProfileDigest=TD, CgroupRootDigest=CGROUP_ROOT_DIGEST
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

Rollback: `sudo rm /etc/tbound/pi-host-profile.json /etc/tbound/pi-host-profile.ed25519 /etc/tbound/d09-cell.json`.

### R5-4 [READ-ONLY] verify on the guest

```
GSSH 'tbound-profile verify --profile /etc/tbound/pi-host-profile.json --sig /etc/tbound/pi-host-profile.ed25519 --pub /etc/tbound/pi-host-profile.pub; tbound-profile template-digest --template /etc/tbound/d09-cell.json'
```

The printed template digest must equal `ContainmentProfileDigest` in the profile.

## Closing provisioning

### I1 [PRIVILEGED WRITE] load the signed image into the service podman store (BLOCKED by B3)

Required result, as the service identity (`SVC`, storage per `/etc/tbound/podman-config/storage.conf`, graphroot `/var/lib/tbound/podman-storage/graphroot`): `podman image exists localhost/tbound/pi-cell:latest` succeeds and its `RepoDigests` contain `@IMGDIGEST`. The mechanism is not defined in the repo and must be proven first on a throwaway store; it needs no network. It must run before Z1 because it needs the service uid.

Rollback: the graphroot was empty (995:986 0700) before this step; `sudo find /var/lib/tbound/podman-storage/graphroot -mindepth 1 -delete`.

### Z0 [PRIVILEGED WRITE] remove scratch

```
GSSH 'sudo -n rm -rf /var/tmp/tbound-cal-20261017'
```

Rollback: none needed; regenerable scratch (R1-3 and R2-2 outputs are already fetched to `$H`). Also remove `$W`, and the guest build dir `/home/tboundadmin/tbound-build-<F8>` (tboundadmin-owned, no sudo) once the image is in the service store.

### Z1 [PRIVILEGED WRITE] revoke passwordless sudo (last sudo command, before the first cell launch)

The owner names the tboundadmin NOPASSWD drop-in under `/etc/sudoers.d/`; no agent reads other sudoers files.

```
GSSH 'sudo -n rm /etc/sudoers.d/<TBOUNDADMIN_NOPASSWD_DROPIN>'
GSSH 'sudo -n true'        # [READ-ONLY] must now fail ("a password is required")
```

Rollback: none, by rule. Passwordless sudo is never restored. Every later privileged change is an owner console session, followed by a re-measure and, where a bound digest changes, a re-sign.

## Part L: first launch (SEPARATE APPROVAL; owner console as root, no sudo)

Each `[PRIVILEGED WRITE]` here changes runtime state only; none changes a signed or measured artifact. Abort on the first unexpected result.

### L1 [PRIVILEGED WRITE] start the service

```
systemctl start tbound-pi.service        # socket activation via tbound-pi.socket is an alternative; the runner creates <service>/cells before any launch (e8a2004), fail closed
journalctl -u tbound-pi.service -n 50 --no-pager        # [READ-ONLY] no admission/profile/mount refusal
```

Rollback: `systemctl stop tbound-pi.service tbound-pi.socket` (systemd removes the unit cgroup).

### L2 [READ-ONLY] cgroup layout check

```
C=/sys/fs/cgroup/system.slice/tbound-pi.service
ls $C                                                    # runner (DelegateSubgroup) and cells
stat -c '%U:%G %a' $C/cells                              # tbound-pi:tbound-pi
cat $C/cells/cgroup.type $C/cells/cgroup.subtree_control $C/cells/cgroup.procs        # domain / cpu memory pids / empty
cat $C/runner/cgroup.procs                               # the service main PID is here, not in cells
tbound-profile cgroup-root-digest --uid 995              # equals CgroupRootDigest in /etc/tbound/pi-host-profile.json (R5-2)
findmnt -n -o TARGET,OPTIONS /usr/local/bin/tbound /usr/bin/podman /usr/bin/crun /opt/tbound/cell /var/lib/tbound/runtime        # all ro
```

### L3 [PRIVILEGED WRITE] positive launch

One governed run of the fixed workflow through the registered client (uid 994, group `tbound-pi-clients`). The client invocation is not defined in this repo (B4); the owner supplies it. Expect admission to pass every pin (profile signature, template digest, image digest, entrypoint digest, ro mounts, cgroup root) and the run to complete under the registered resource bounds.

Rollback: stop the service (L1 rollback); remove the run's live-root output only by an owner-supervised console action.

### L4 [READ-ONLY] drain

After the run ends: `cells` holds no child scope and `cgroup.procs` is empty; no container remains in the service store (`SVC podman ps -a` empty); no cell mount remains in `/proc/*/mountinfo`; the audit log records the scope as drained. The service accepts the next workflow.

### L5 [PRIVILEGED WRITE] negative cases (each must fail closed; restore afterwards)

- N1: a second workflow while one is active is refused.
- N2: a connection from a uid outside `tbound-pi-clients` is refused at the socket.
- N3: `systemctl stop opt-tbound-cell.mount`, then attempt a launch: it must be refused (cosign, key, seccomp and layout are no longer on a read-only mount). Restore: `systemctl start opt-tbound-cell.mount`; re-run L2.
- N4: network egress from inside the cell fails (`network-none`).

Rollback: restore every mount stopped in N3 and re-run L2; stop the service if any case was not refused.
