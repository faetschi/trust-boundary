# ABI8 guest: owner provisioning and signing session

Status: draft for owner approval, 2026-10-18, checked against code at `1dfca4e` (branch `codex/tbound-prototype`). Nothing here has run on the guest except steps the 2026-10-17 throwaway-key dry run (`F:\TBoundAssets\recovery-20261015\sign-dryrun-20261017\owner-sequence.sh.txt`) marked DRYRUN-OK. Code references are symbols, not line numbers. This runbook supersedes `docs/governed-setup.md` section 225-236 for the ABI8 guest.

Purpose: ONE signing session that prepares the guest for (1) the first E2E smoke over OpenCode Go (v3 `fresh` config, task `smoke-read-readme`) and (2) the pilot corpus run (corpus mode, `corpus_quota_profile: pilot`, engineering route). The pilot corpus digest exists only after the owner and advisor reviews, so the corpus-mode config comes later (Part D) and costs two more signatures (S3', S4'). Everything else is done once.

## Tags and rules

- `[READ-ONLY]` changes nothing on the guest or the owner machine except new private scratch files.
- `[PRIVILEGED WRITE]` changes the guest (root through `sudo -n` until Z1, owner root console after). Each has a rollback line.
- `[OWNER SIGN]` uses a private key (`<ADMIN_KEY_PATH>` or `<COSIGN_KEY_PATH>`). An agent never reads, copies or logs a key or passphrase.

1. Every `[PRIVILEGED WRITE]` and `[OWNER SIGN]` step needs explicit owner approval, one step at a time.
2. Passwordless sudo is revoked at Z1, after provisioning and before the first cell launch, and is never restored. Nothing after Z1 uses sudo.
3. Part L (first launch) is a separate approval.
4. Signature output paths must be new absolute canonical paths; in-place signing is refused.
5. The frozen commit `F` (F0) is the only source. Any change to a build input after F0 voids every binary, digest, image and signature below; start again from F0. This includes any code fix the Blockers or Spike B force.
6. Prototyping uses the engineering route (OpenCode Go, `provider_route: "engineering"`) only. Research-route steps (OpenRouter, USD ledger) are "NOT NOW" until the owner switches.

## Signing operations

| ID | Step | Key | Artifact |
| --- | --- | --- | --- |
| K1 | P4 | admin | read only: derive `<ADMIN_PUB_PATH>` (no signature) |
| K2 | P5 | cosign | read only: export `cosign.pub` (no signature) |
| S1 | R1-7 | admin | read-only-source xattr calibration `d09-xattr-enumeration-calibration-readonly` |
| S2 | R1-7 | admin | managed-live-root xattr calibration `d09-xattr-enumeration-calibration` |
| S3 | R2-4 | admin | managed live root registration `managed-live-root` (pins the smoke config digest) |
| S4 | R5-2 | admin | host profile `pi-host-profile` (pins the cell template digest TD) |
| S5 | P6 | cosign | cell image by manifest digest `IMGDIGEST` (one `cosign sign-blob` inside `sign_cell_image.py`) |
| S3' | D4 | admin | registration over the corpus-mode config (DEFERRED) |
| S4' | D6 | admin | host profile over the new template digest (DEFERRED) |

This session: 5 signatures (S1 to S5) and 2 public-key derivations. Deferred to Part D, after the pilot reviews: S3' and S4'. S3' is needed because the registration pins the service config digest and the corpus config differs from the smoke config. S4' follows from S3': the cell template binds the registration digest (`managed_live_root_registration_digest`, checked in `piruntime` against `registration.Digest`), so the new registration changes TD, and the signed host profile binds TD (`ContainmentProfileDigest`). S1, S2 and S5 stay valid (they bind calibrations and the image). The B2 delta templates need no signature: `D09CellDelta.TemplateDigest` derives them from the signed base TD, and `tbound-profile template-digest` prints all three. Avoiding S3'/S4' means waiting for the advisor review and running the smoke as a corpus case instead; that is an owner choice, not assumed here.

The admin key is the raw 64-byte Ed25519 key. S1 to S4 are domain-separated; the calibrations are v2 (`authority` = `root-attested-bounded-no-hidden-trusted/v1`, a bounded claim).

## Session order

1. F0 freeze (needs owner approval to pick `F`). P1 to P3 build (read-only, built twice). P4 to P6 [OWNER SIGN]. P7 verify.
2. Guest: P8 to P13 (trust anchor, ownership, ro mounts, bundle, key, B2 and corpus files).
3. R1 calibration (S1, S2), R2 smoke config and registration (S3), R5 template, manifest pins (R5-1b) and host profile (S4).
4. SSH-1 to SSH-3 (three forced-command keys: evidence, trial, client), NS-1 (net sink unit), Spike A.
5. I1 (image into the service store), Z0, Z1 (revoke sudo).
6. Part L smoke (separate approval).
7. Part D after the reviews (S3', S4'), Spike B, then the pilot run.

## Blockers (BL-1 and BL-4 resolved in code and in this text; the rest open)

Both resolutions are code (`cmd/tbound/client_forced_linux.go`, `cmd/tbound-profile/corpus_pins_linux.go`, `gate.ProductionProfile`) that is not yet in a commit: commit it, then take `F0` from that commit. Every digest and signature in this runbook is taken after that commit.

- BL-1 RESOLVED. The client key has a forced-command entry, `tbound client-forced`. `runner.SSHTransport` sends the client as `setpriv --reuid 994 --regid 985 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- /usr/local/bin/tbound serve --pi --task-id <case_id> --role <role> --attempt <1|2> --task-request-id <id> --bash-no-outputs` (`DefaultClientCommand`, `GovernedRequest.Argv`); the entry accepts exactly that grammar and nothing else, then runs it. The transport is unchanged (`SSHTransport.RemoteCommand`, an exported accessor of its quoting, is only for the parity test). The key logs in as `root`, not as `tbound-client`: only root can `setpriv` to uid 994, and `tbound-client` has `/usr/sbin/nologin`, which sshd would run for any command of that account, so a `Match User tbound-client` or `ForceCommand` for that user cannot work. Exact lines are in SSH-1 and SSH-2. The smoke does not need the key (Part L runs at the root console).
- BL-2 `tbound trial reset|accept|v-run` need a corpus-mode service config. They resolve the case through the signed registration, the service config and the pinned corpus manifest (`trialPinnedFixtureDigest`, `loadProductionCorpus`); under the smoke config they refuse ("not a corpus-mode config"). So the net-sink, no-reap and V spikes, and the real `nft` load, run only after Part D (Spike B). A defect found there that needs code (for example the `nft -j` shape that `verifyNetSinkRuleset` expects, unmeasured on the guest's nft) means a new `F` and a new session.
- BL-3 `nft` and the kernel. `trialNftPath` is `/usr/sbin/nft`; the rule needs `socket cgroupv2` support. No repo evidence shows nft installed or the kernel module available on the guest, and the guest is offline with no documented install route. Spike A checks it read-only; if missing, net-sink is impossible and A-03, B-04, C-03 are `channel_non_discriminative`.
- BL-4 RESOLVED. The manifest pins (`environment_digest`, `model_digest`, `profile_digest`, `policy_digest`) are checked only for format (`evalcorpus.Validate`); nothing compares them to a profile (receipt identity is pinned in `run.json pins`, which stays as it is). They must not bind anything downstream of the corpus digest: corpus digest, S3' config, registration, template digest TD, S4' host profile digest. `tbound-profile corpus-pins` defines each pin from values measured before any of those exist (section "Manifest pins", run at R5-1 and again at D1 and D6). The manifest schema is unchanged.
- BL-5 closed: `tbound-run evq-capture`, `evq` and `route-report` generate `evq_results` and `route_report` from one controlled capture of `F` (see "EVQ results and route report" in the pilot run). They are code in `F`; the pinned mapping and route matrix live in `infra/conformance/` (also in `F`). Of 22 EVQ obligations, 15 pass from named tests of the capture, 4 need a guest artifact (`tbound-profile guest-evidence`, Spike A, Spike B, a G1 run) and 3 have no evidence yet and stay not passed with a stated reason; the route report is complete when its four E06 cells pass on the capture host. `Attest` reports every not passed obligation as such; the pilot is non-claim-bearing either way.
- BL-6 After Z1 the owner moves files with `GSSH` to a staging directory of `tboundadmin` and installs from the root console; there is no scripted root channel (the three forced-command keys of Part K run only their own verbs). Part D steps are marked accordingly.
- Unproven, not blockers: I1 podman load keeps the manifest digest and the `no-new-privs` service reads the store; L0 accepts the seeded file in the xattr census; guest egress to `opencode.ai:443`; V `--uidmap` under `setpriv --no-new-privs`.

## Placeholders

```
GUEST_IP   = current guest address, discovered (below); never copied from an older note
GSSH       = Git Bash ssh as tboundadmin@$GUEST_IP: StrictHostKeyChecking=yes, HostKeyAlias=172.18.155.19, pinned known_hosts, IdentitiesOnly, BatchMode (helper gssh2.sh in F:\...\remeasure-20261017; it hardcodes an old address, make it read $GUEST_IP)
KSSH <key> = the same options with -i <key> and root@$GUEST_IP (forced-command keys, SSH-3)
SVC        = sudo -n setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs --   (after Z1: the same line without sudo -n, at the root console)
TBP        = the frozen-commit tbound-profile on the owner machine (P1)
F          = frozen commit (40 hex)
W, B, H    = owner-side build dir, image build dir, signing dir; absolute, canonical, mode 0700, unique names, removed at the end
SCR        = /var/tmp/tbound-cal-20261017 (guest scratch, 995:986 0700)
CI         = F:\TBoundAssets\recovery-20261015\cell-image-20261017 (base-cache, cosign-v3.1.3 tool)
BR         = F:\TBoundAssets\recovery-20261015\bundle-rebuild-20261017
COSIGN     = owner-side copy of CI\tools\cosign-v3.1.3 (sha256 4629c757...), file name kept `cosign`-compatible, chmod 700
IMGDIGEST, BUNDLE_DIGEST, COSIGN_PUB_DIGEST, ENTRYPOINT_DIGEST, BASH_DIGEST, REGDIGEST, TD, CGROUP_ROOT_DIGEST = recorded values
<ADMIN_KEY_PATH>, <ADMIN_PUB_PATH>, <COSIGN_KEY_PATH>, <EVKEY>, <TRKEY>, <CLKEY> = owner-held keys, never read by an agent
```

Guest address discovery. The guest has a DHCP address on the Hyper-V Default Switch that changes after host restarts; the MAC is `00-15-5D-0C-40-02`. [READ-ONLY, owner machine, PowerShell] `Get-NetNeighbor -LinkLayerAddress 00-15-5D-0C-40-02 | Select-Object IPAddress,State`; an empty result means the VM is off or the neighbor cache is cold (the owner starts the VM). Connect with `-o HostKeyAlias=172.18.155.19` against the pinned key file. Never relax host-key checking and never edit `known_hosts` to fit; a key mismatch is a stop.

## F0 [READ-ONLY] freeze

```
git status --porcelain -- supervisor adapter scripts packaging infra        # must print nothing
F=$(git rev-parse --verify 'HEAD^{commit}'); echo $F > $H/FROZEN-COMMIT       # owner records it
```

Use `$F`, never `HEAD`. Before each later part run `git diff --quiet $F HEAD -- supervisor adapter scripts packaging infra`; any difference voids the run. Every `git archive` uses `-c core.autocrlf=false -c core.eol=lf`. Commit this runbook and the corpus-pilot template first (needs owner approval).

## Part P: preparation (owner machine)

### P1 [READ-ONLY] build the binaries from F (build twice, byte-identical)

```
git -c core.autocrlf=false -c core.eol=lf archive --format=tar $F supervisor scripts adapter/src adapter/package.json adapter/package-lock.json adapter/pi-runtime-package.json packaging infra | (mkdir -p $W/src && tar -x -C $W/src)
cd $W/src/supervisor
export GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=file:///home/jeli2k/go/pkg/mod/cache/download GOSUMDB=off CGO_ENABLED=0
for c in tbound tbound-profile tbound-cell-entrypoint; do /home/jeli2k/opt/go1.27.1/bin/go build -trimpath -o $W/bin/$c ./cmd/$c ; done
GOOS=windows GOARCH=amd64 /home/jeli2k/opt/go1.27.1/bin/go build -trimpath -o $W/bin/tbound-run.exe ./cmd/tbound-run
sha256sum $W/bin/*
```

`tbound-run.exe` is the host runner (Part Pilot); it is not installed on the guest. Checked at `1dfca4e`: two Windows builds were byte-identical. The runner digest (`tbound`) is bound by R2-2; `tbound-cell-entrypoint` is the reference for the in-image entrypoint (P3 must reproduce its digest).

### P2 [READ-ONLY] rebuild the runtime bundle from F (LF)

Scripts in `BR` (`10-setup.sh`, `20-bundle.sh`, `40-go-and-digest.sh`, `50-package.sh`, `99-cleanup.sh`) run in WSL as jeli2k, offline, with Node v24.21.0 (sha256 `7fde7b8a...df4c`) and `scripts/prepare_official_pi_runtime.py`. Regenerate `inputs/head-src.tar` from `$F` with the LF archive command of P1, rename the output tar for `$F`, build twice (byte-identical), measure with the real `tbound-profile runtime-bundle-digest` under a read-only bind. Dirs 0555, files 0555/0444, deterministic tar (`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner`). Record `BUNDLE_DIGEST` and the tar sha256. Last known value `sha256:8e405ef0...d4f48` (unchanged at 08eef85); a different value is not an error, it is the value to pin. The installed bundle (`sha256:d7367e1b...6884`) is replaced in P11.

### P3 [READ-ONLY] build the cell image from F (offline; rebuilt because the entrypoint changed)

`packaging/governed/cell_image.py` (Python 3 stdlib, `python3 -I`) assembles the OCI layout from the digest-pinned base layers in `base-images.lock.json`, the P1 entrypoint and the F adapter sources. Use the cache `CI\artifacts\base-cache` copied into a private dir (`pull` refills it once, network, only if lost).

```
python3 -I $W/src/packaging/governed/cell_image.py build --lock $W/src/packaging/governed/base-images.lock.json --cache $CACHE \
  --entrypoint $W/bin/tbound-cell-entrypoint --adapter $W/src/adapter --epoch $(git show -s --format=%ct $F) --out $B/oci
python3 -I $W/src/packaging/governed/cell_image.py measure --layout $B/oci
```

Build twice into two new dirs; `diff -r` must print nothing. Record from `measure`: `IMGDIGEST` (`image_manifest_digest`, the only pinned image digest), `ENTRYPOINT_DIGEST` (`entrypoint_sha256`, must equal `sha256sum $W/bin/tbound-cell-entrypoint`, else stop), `node_sha256` (must equal the host Node `7fde7b8a...df4c`, `verifyImageWorkerExecutable`), `BASH_DIGEST` (`bash_sha256`, `55b89ab2...efcf` at the last build), and the image config digest `CFG` (needed by I1). Last build (08eef85): `IMGDIGEST` `sha256:52f3f2e3...`. The image has no git, python3, curl, wget, nc or openssl (see `corpus/pilot/README.md`).

### P4 [OWNER SIGN, needs owner approval] K1: derive the admin public key

```
tail -c 32 <ADMIN_KEY_PATH> > <ADMIN_PUB_PATH>; stat -c %s <ADMIN_PUB_PATH>        # expect 32
```

### P5 [OWNER SIGN, needs owner approval] K2: export the cosign public key

```
cosign public-key --key <COSIGN_KEY_PATH> --outfile $H/cosign.pub        # passphrase typed by the owner only
sha256sum $H/cosign.pub        # COSIGN_PUB_DIGEST
```

### P6 [OWNER SIGN, needs owner approval] S5: sign the cell image, export the layout

Owner machine, offline. One `COSIGN sign-blob --key` call on the simple-signing payload for `IMGDIGEST`; `cosign save` runs against a throwaway in-process loopback registry. The owner types the passphrase; the helper never reads it. The output path must not exist.

```
python3 -I $W/src/packaging/governed/sign_cell_image.py sign --layout $B/oci --cosign $COSIGN --key <COSIGN_KEY_PATH> --out $H/pi-cell-oci
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -C $B/oci -cf $H/pi-cell-load.tar oci-layout index.json blobs
sha256sum $H/pi-cell-load.tar        # LOAD_TAR_DIGEST (loadable single-image archive for I1)
```

### P7 [READ-ONLY] verify the signed layout; a wrong key must fail

```
python3 -I $W/src/packaging/governed/sign_cell_image.py verify --layout $H/pi-cell-oci --cosign $COSIGN --pub $H/cosign.pub --expect $IMGDIGEST        # prints IMGDIGEST
python3 -I $W/src/packaging/governed/sign_cell_image.py verify --layout $H/pi-cell-oci --cosign $COSIGN --pub <other public key> --expect $IMGDIGEST   # must fail
```

Launcher form (`verifyOfflineSignature`): `cosign verify --offline --insecure-ignore-tlog=true --key <pub> --local-image <layout>`. There is no transparency log; trust is the pinned `cosign.pub` digest plus `IMGDIGEST`.

## Part G: guest provisioning (needs sudo; before Z1)

### P8 [PRIVILEGED WRITE, needs owner approval] trust anchor and scratch

```
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/pi-host-profile.pub' < <ADMIN_PUB_PATH>
GSSH 'sudo -n sha256sum /etc/tbound/pi-host-profile.pub'; sha256sum <ADMIN_PUB_PATH>        # [READ-ONLY] equal
GSSH 'sudo -n install -d -o 995 -g 986 -m 0700 /var/tmp/tbound-cal-20261017'
```

Rollback: `sudo rm /etc/tbound/pi-host-profile.pub; sudo rm -rf /var/tmp/tbound-cal-20261017`.

### P9 [PRIVILEGED WRITE, needs owner approval] make the live root enumerable by uid 995

uid 995 cannot open `lost+found` (root 0700), so every 995 enumeration fails (`xattr_profile_linux.go`).

```
GSSH 'sudo -n chown 995:986 /var/lib/tbound/live/lost+found && sudo -n stat -c "%U:%G %a" /var/lib/tbound/live/lost+found'      # tbound-pi:tbound-pi 700
```

Rollback: `sudo chown root:root /var/lib/tbound/live/lost+found`.

### P10 [PRIVILEGED WRITE, needs owner approval] executables and cell assets on read-only mounts

All fail closed through `requireReadOnlyMount`.

| Item | Pinned path | Checked by (symbol) |
| --- | --- | --- |
| runner | `/usr/local/bin/tbound` | `hashProtectedFile`; `verifyPinnedToolBinary` (`MeasureD09ManagedLiveRootRegistrationWithOptions`, `verifyD09ManagedWriterProcess`) |
| podman, crun | `/usr/bin/podman`, `/usr/bin/crun` | `Probe` -> `verifyPinnedToolBinary` -> `requireReadOnlyMount` |
| cosign, cosign.pub, seccomp.json | `/opt/tbound/cell/{cosign,cosign.pub,seccomp.json}` | `verifyOfflineSignature`, `verifyD09SeccompProfile` -> `hashProtectedFile` |
| OCI layout | `/opt/tbound/cell/pi-cell-oci` | `verifyLocalOCIIdentity` -> `requireReadOnlyMount` |
| Node | `/usr/local/bin/node` | `verifyImmutableLinuxExecutable`; already installed (d99ced8) |

Not required: `tbound-profile`, `conmon`, `/bin/bash`. Binding over podman and crun blocks apt from replacing them; an upgrade means stop, re-measure and re-sign everything binding their digests.

Node (already installed, do not reinstall). [READ-ONLY] verify: `GSSH 'findmnt -no OPTIONS /usr/local/bin/node'` contains `ro`; `GSSH 'sha256sum /usr/local/bin/node'` = `7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c`. If either fails, redo the d99ced8 install as `docs/evidence/abi8-node-mount-units-20261017.json` records it (unit `systemd/usr-local-bin-node.mount`). Rollback: `sudo systemctl disable --now usr-local-bin-node.mount; sudo rm /etc/systemd/system/usr-local-bin-node.mount; sudo systemctl daemon-reload`.

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
GSSH 'findmnt -n -o TARGET,OPTIONS /usr/local/bin/tbound /usr/local/bin/node /usr/bin/podman /usr/bin/crun /opt/tbound/cell; sha256sum /usr/local/bin/tbound /usr/bin/podman /usr/bin/crun /opt/tbound/cell/cosign /opt/tbound/cell/cosign.pub /opt/tbound/cell/seccomp.json'   # [READ-ONLY] every line ro,nosuid,nodev,relatime; hashes equal the sources
GSSH 'sudo -n /opt/tbound/cell/cosign verify --offline --insecure-ignore-tlog=true --key /opt/tbound/cell/cosign.pub --local-image /opt/tbound/cell/pi-cell-oci'   # [READ-ONLY] docker-manifest-digest == IMGDIGEST
```

Rollback per unit: `sudo systemctl disable --now <unit>; sudo rm /etc/systemd/system/<unit>; sudo systemctl daemon-reload`; restore `/opt/tbound/bin/tbound.old` and `tbound-profile.old`; remove `/opt/tbound/cell-src/*`. Old assets `/etc/tbound/cosign.pub` and `/etc/tbound/pi-cell-oci` stay untouched and unreferenced. After the first reboot check all mount units are active.

### P11 [PRIVILEGED WRITE, needs owner approval] reinstall the runtime bundle, then re-measure

`var-lib-tbound-runtime.mount` (ro bind of `runtime-src`) and `var-lib-tbound-live.mount` (loop ext4 `live.img`) already exist; do not edit them (R2-2 digests the live unit and the registration pins it; guest sha256 runtime `2bc03ead...0ea3`, live `9de139e3...bff2`).

```
GSSH 'findmnt -no TARGET,FSTYPE,OPTIONS /var/lib/tbound/runtime /var/lib/tbound/live'        # [READ-ONLY] runtime ro,nosuid,nodev; live ext4 rw,nosuid,nodev
GSSH 'sha256sum /etc/systemd/system/var-lib-tbound-runtime.mount /etc/systemd/system/var-lib-tbound-live.mount'        # [READ-ONLY]
sha256sum <bundle tar from P2>                                    # [READ-ONLY] equals the P2 record
GSSH 'sudo -n systemctl stop var-lib-tbound-runtime.mount && sudo -n mv /var/lib/tbound/runtime-src /var/lib/tbound/runtime-src.old-d7367e1b && sudo -n install -d -o root -g root -m 0555 /var/lib/tbound/runtime-src'
GSSH 'sudo -n tar -x -p --numeric-owner -C /var/lib/tbound/runtime-src -f -' < <bundle tar from P2>
GSSH 'sudo -n systemctl start var-lib-tbound-runtime.mount; findmnt -n -o TARGET,OPTIONS /var/lib/tbound/runtime'        # ro,nosuid,nodev
GSSH 'SVC tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime'        # [READ-ONLY] == BUNDLE_DIGEST
```

Rollback: stop the mount, `rm -rf runtime-src`, `mv runtime-src.old-d7367e1b runtime-src`, start the mount. Remove the `.old` copy only after the signed profile is installed and verified.

### P12 [PRIVILEGED WRITE, needs owner approval] OpenCode Go key (engineering route)

Writes `/etc/tbound/provider` (root:root 0755) and `/etc/tbound/provider/opencode-go.key` (995:986 0400). The owner types the key in an interactive guest root shell; it is never on a command line, logged or shown to an agent. Not digest-bound. The same file is the key V mounts read-only (`trial v-run`). Read at the first conversation (`D09ManagedPiProviderCredentialPath`, `serviceProviderConversationFactory`); file rules in `internal/broker/openrouter/credential_file_linux.go`: regular file, no symlink, at most 4096 bytes, one printable token, owner = service uid, mode 0400 or 0600; every ancestor from `/` a real root-owned directory not group- or other-writable.

```
GSSH 'sudo -n install -d -o root -g root -m 0755 /etc/tbound/provider'
# owner, interactive guest root shell:
umask 077; IFS= read -rs K; printf '%s' "$K" | install -o 995 -g 986 -m 0400 /dev/stdin /etc/tbound/provider/opencode-go.key; unset K
GSSH 'stat -c "%U:%G %a %F" /etc/tbound/provider/opencode-go.key'        # [READ-ONLY] metadata only; tbound-pi:tbound-pi 400 regular file
GSSH 'namei -mo /etc/tbound/provider/opencode-go.key'                    # [READ-ONLY] ancestors root-owned, no group/other write, no symlink
```

Check before L3: the one-use admission is reserved before the provider conversation is built, so a key problem found only at launch burns the smoke admission id. Rollback: `sudo rm -f /etc/tbound/provider/opencode-go.key; sudo rmdir /etc/tbound/provider`.

### P13 [PRIVILEGED WRITE, needs owner approval] B2 and pilot files

Four independent installs; none is signed. All from `$W/src` (the F archive).

1. Maintenance lock (REQUIRED FOR THE SMOKE): the service takes `/run/tbound/maintenance.lock` when it admits any workflow, in every mode, and refuses with `maintenance-lock-unavailable` if the file is absent, not regular, not root-owned or writable by group/others (`serviceMaintenanceLock`). `tbound trial reset` takes the same lock (`flock`, never waits).
   ```
   GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tmpfiles.d/tbound-maintenance.conf' < $W/src/infra/guest/tmpfiles.d/tbound-maintenance.conf
   GSSH 'sudo -n systemd-tmpfiles --create /etc/tmpfiles.d/tbound-maintenance.conf; stat -c "%U:%G %a" /run/tbound/maintenance.lock'        # root:root 644
   ```
   Rollback: `sudo rm /etc/tmpfiles.d/tbound-maintenance.conf /run/tbound/maintenance.lock`.
2. Net-sink rule file: `/etc/tbound/net-sink.nft` (root:root 0644, LF, sha256 must equal `D09NetSinkRuleDigest`, `b2a5618f...78e4`). `tbound trial reset` loads it; nothing loads it here.
   ```
   GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/net-sink.nft' < $W/src/infra/guest/nftables/tbound-net-sink.nft
   GSSH 'sha256sum /etc/tbound/net-sink.nft; ls -l /usr/sbin/nft; sudo -n /usr/sbin/nft -c -f /etc/tbound/net-sink.nft && echo syntax-ok'        # [READ-ONLY] digest as above; the binary exists; syntax-ok (nft may complain that the cgroup path does not exist yet, which is expected before the first reset; only a syntax error counts; unmeasured)
   ```
   Rollback: `sudo rm /etc/tbound/net-sink.nft` (a guest without the file resets without net-sink and the service refuses net-sink).
3. Corpus artifacts: `/opt/tbound/corpus/artifacts` (`trialArtifactDir`), root:root, dirs 0755, files 0644: `fixture-<hex>.json` (read by reset and v-run), `accept-<hex>.json`, `solution-<hex>.json` (read by accept and the corpus check), `attacker-<hex>.json` (read by the service for the adaptive roles, `attackerArtifacts`), `rubric-<hex>.md`. Install the whole `corpus/pilot/artifacts` directory of `$F`; file names are digest-addressed, so a later artifact change in Part D only adds files.
   ```
   GSSH 'sudo -n install -d -o root -g root -m 0755 /opt/tbound/corpus /opt/tbound/corpus/artifacts'
   tar -C corpus/pilot/artifacts -c . | GSSH 'sudo -n tar -x --no-same-owner --no-same-permissions -C /opt/tbound/corpus/artifacts'
   GSSH 'sudo -n chown -R root:root /opt/tbound/corpus && sudo -n find /opt/tbound/corpus -type d -exec chmod 0755 {} + && sudo -n find /opt/tbound/corpus -type f -exec chmod 0644 {} +; ls /opt/tbound/corpus/artifacts | wc -l'        # 36 at 1dfca4e
   ```
   Rollback: `sudo rm -rf /opt/tbound/corpus`.
4. Corpus manifest location: `/etc/tbound/corpus/manifest.json` (`D09ManagedPiCorpusManifestPath`, the only path the v4 config accepts) is installed in Part D (D2), not now; the manifest is not final.

## Row 1: calibration

Both calibrations are made in place by the root writer against the live trees (R1-2, R1-5). The helper is `infra/guest/helpers/xattr-census-r9.py` (sha256 `2daf06f5...3e4b`); run the commands from `infra/guest/helpers/` of `$W/src`.

### R1-1 [READ-ONLY] baselines

```
GSSH 'sudo -n python3 -I - /var/lib/tbound/runtime' < xattr-census-r9.py > $H/census-runtime-base.jsonl
GSSH 'sudo -n python3 -I - /var/lib/tbound/live'    < xattr-census-r9.py > $H/census-live-base.jsonl
GSSH 'SVC tbound-profile runtime-bundle-digest --root /var/lib/tbound/runtime'                  # BUNDLE_DIGEST
GSSH 'SVC tbound-profile xattr-measure --source /var/lib/tbound/runtime'                         # read_only true, root_owned true
```

### R1-2 [PRIVILEGED WRITE, needs owner approval] probe xattr on runtime-src (negative control first)

```
GSSH "sudo -n python3 -I -c \"import os;os.setxattr('/var/lib/tbound/runtime/.','user.tbound.calibration-probe',b'x')\""      # expect OSError 30 EROFS
GSSH "sudo -n python3 -I -c \"import os;os.setxattr('/var/lib/tbound/runtime-src','user.tbound.calibration-probe',b'abi8-ro-calibration-20261017')\""
```

Rollback: R1-4.

### R1-3 [READ-ONLY] read-only calibration measure (scratch write)

```
GSSH 'sudo -n python3 -I - /var/lib/tbound/runtime' < xattr-census-r9.py > $H/census-runtime-probe.jsonl
PCD_RO=sha256:$(sha256sum $H/census-runtime-probe.jsonl | cut -d' ' -f1)       # owner-reviewed; summary {"user":1} only
GSSH "SVC tbound-profile xattr-calibration-measure --source /var/lib/tbound/runtime --access-profile readonly-sealed-source/v1 --probe-corpus-digest $PCD_RO --out $SCR/cal-ro.unsigned.json"
```

### R1-4 [PRIVILEGED WRITE, needs owner approval] remove the probe, prove zero

```
GSSH "sudo -n python3 -I -c \"import os;os.removexattr('/var/lib/tbound/runtime-src','user.tbound.calibration-probe')\""
GSSH 'sudo -n python3 -I - /var/lib/tbound/runtime' < xattr-census-r9.py | sha256sum        # [READ-ONLY] == census-runtime-base.jsonl
```

Repeat the R1-1 checks; `xattr-calibration-measure` must refuse again. Rollback: this is the rollback of R1-2; retry the same command.

### R1-5 [PRIVILEGED WRITE, needs owner approval] read-write calibration on the live root (after P9)

```
GSSH 'sudo -n python3 -I - /var/lib/tbound/live' < xattr-census-r9.py > $H/census-live-base2.jsonl
GSSH "sudo -n sh -c 'set -e; D=/var/lib/tbound/live/tbound-xattr-probe-20261017; install -d -o 995 -g 986 -m 0700 \$D; install -o 995 -g 986 -m 0600 /dev/null \$D/plain.txt; python3 -I -c \"import os;os.setxattr(\\\"\$D/plain.txt\\\",\\\"user.tbound.calibration-probe\\\",b\\\"x\\\")\"'"
GSSH 'sudo -n python3 -I - /var/lib/tbound/live' < xattr-census-r9.py > $H/census-live-probe.jsonl
PCD_RW=sha256:$(sha256sum $H/census-live-probe.jsonl | cut -d' ' -f1)
GSSH "SVC tbound-profile xattr-calibration-measure --source /var/lib/tbound/live --access-profile managed-live-root-rw-under-recovered-lease/v1 --probe-corpus-digest $PCD_RW --out $SCR/cal-rw.unsigned.json"
GSSH "sudo -n rm -rf /var/lib/tbound/live/tbound-xattr-probe-20261017"
GSSH 'sudo -n python3 -I - /var/lib/tbound/live' < xattr-census-r9.py | sha256sum        # [READ-ONLY] == census-live-base2.jsonl
```

Rollback: `sudo rm -rf /var/lib/tbound/live/tbound-xattr-probe-20261017`.

### R1-6 [READ-ONLY] fetch the unsigned calibrations

```
GSSH "sudo -n cat $SCR/cal-ro.unsigned.json" > $H/cal-ro.unsigned.json
GSSH "sudo -n cat $SCR/cal-rw.unsigned.json" > $H/cal-rw.unsigned.json ; sha256sum $H/cal-*.unsigned.json
```

### R1-7 [OWNER SIGN, needs owner approval] S1, S2

```
TBP xattr-calibration-sign --calibration $H/cal-ro.unsigned.json --key <ADMIN_KEY_PATH> --out-sig $H/d09-xattr-enumeration-calibration-readonly.ed25519 --out-calibration $H/d09-xattr-enumeration-calibration-readonly.json
TBP xattr-calibration-sign --calibration $H/cal-rw.unsigned.json --key <ADMIN_KEY_PATH> --out-sig $H/d09-xattr-enumeration-calibration.ed25519          --out-calibration $H/d09-xattr-enumeration-calibration.json
TBP xattr-calibration-verify --calibration $H/d09-xattr-enumeration-calibration-readonly.json --sig $H/d09-xattr-enumeration-calibration-readonly.ed25519 --pub <ADMIN_PUB_PATH>   # [READ-ONLY]
TBP xattr-calibration-verify --calibration $H/d09-xattr-enumeration-calibration.json          --sig $H/d09-xattr-enumeration-calibration.ed25519          --pub <ADMIN_PUB_PATH>   # [READ-ONLY]
```

A verify with the wrong key fails with "does not verify under the supplied public key": a key mix-up. Record the RW calibration digest (bound by R2).

### R1-8 [PRIVILEGED WRITE, needs owner approval] install the four calibration files (root:root 0644), verify

```
for f in d09-xattr-enumeration-calibration.json d09-xattr-enumeration-calibration.ed25519 d09-xattr-enumeration-calibration-readonly.json d09-xattr-enumeration-calibration-readonly.ed25519; do
  GSSH "sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/$f" < $H/$f ; done
GSSH 'tbound-profile xattr-calibration-verify --calibration /etc/tbound/d09-xattr-enumeration-calibration.json --sig /etc/tbound/d09-xattr-enumeration-calibration.ed25519 --pub /etc/tbound/pi-host-profile.pub'      # [READ-ONLY]; same for -readonly
```

Rollback: `sudo rm /etc/tbound/d09-xattr-enumeration-calibration{,-readonly}.{json,ed25519}`.

## Row 2: smoke service config and registration

### R2-0 [PRIVILEGED WRITE, needs owner approval] install the smoke config (before R2-2: the registration pins its digest)

`/etc/tbound/serve-pi-service.json` is still the 2026-10-15 fixture (sha256 `40db4c92...`). Replace it with `infra/guest/serve-pi-service.smoke.json`: schema v3, `provider_route: "engineering"`, `workflow_admission_mode: "fresh"`, admission id `smoke-2026-10-17`, task `smoke-read-readme`, ceilings 4 host calls / 10000 tokens / 1200 s. The service must be inactive. `provider_route` is required and covered by the digest. Template validity is held by `TestSmokeServiceConfigTemplateStaysValidOnTheLegacySchema`.

```
CD=sha256:$(GSSH 'sha256sum /etc/tbound/podman-config/containers.conf' | cut -d' ' -f1)        # [READ-ONLY] 2026-10-17: 8627b6ed...
SD=sha256:$(GSSH 'sha256sum /etc/tbound/podman-config/storage.conf' | cut -d' ' -f1)           # [READ-ONLY] 2026-10-17: 9c571a18...
sed -e "s|\"containers_conf_digest\": \"<MEASURE>\"|\"containers_conf_digest\": \"$CD\"|" -e "s|\"storage_conf_digest\": \"<MEASURE>\"|\"storage_conf_digest\": \"$SD\"|" infra/guest/serve-pi-service.smoke.json > $H/serve-pi-service.json
! grep -q MEASURE $H/serve-pi-service.json && grep -c '"sha256:[0-9a-f]\{64\}"' $H/serve-pi-service.json        # expect 2
GSSH 'sudo -n cat /etc/tbound/serve-pi-service.json' > $H/serve-pi-service.json.prev        # [READ-ONLY]
GSSH 'sudo -n systemctl is-active tbound-pi.service'        # [READ-ONLY] inactive
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/serve-pi-service.json' < $H/serve-pi-service.json
GSSH 'sha256sum /etc/tbound/serve-pi-service.json'; sha256sum $H/serve-pi-service.json        # [READ-ONLY] equal
```

Rollback: reinstall `$H/serve-pi-service.json.prev` the same way.

### R2-1 [PRIVILEGED WRITE, needs owner approval] create the one parent cgroup (measurement only)

`<service>/cells` is created by the runner at service start.

```
GSSH "sudo -n sh -c 'set -e; C=/sys/fs/cgroup/system.slice/tbound-pi.service; test ! -e \$C; mkdir \$C; echo \"+cpu +memory +pids\" > \$C/cgroup.subtree_control; chown 995:986 \$C \$C/cgroup.procs \$C/cgroup.threads \$C/cgroup.subtree_control; cat \$C/cgroup.type \$C/cgroup.subtree_control; wc -c < \$C/cgroup.procs'"      # domain / cpu memory pids / 0
```

Rollback: R2-3.

### R2-2 [READ-ONLY] measure (scratch write; needs P10)

```
GSSH "sudo -n /usr/local/bin/tbound-profile managed-root-measure --root /var/lib/tbound/live --service-name tbound-pi --service-group tbound-pi --service-unit /etc/systemd/system/tbound-pi.service --mount-unit /etc/systemd/system/var-lib-tbound-live.mount --runner /usr/local/bin/tbound --service-cgroup /system.slice/tbound-pi.service --allowed-client-uids 994 --allowed-client-primary-gid 985 --out $SCR/managed-live-root.unsigned.json"
```

Expect `runner_digest` == P1 `tbound`, `managed_root_xattr_calibration_digest` == RW digest (R1-7), `service_uid` 995.

### R2-3 [PRIVILEGED WRITE, needs owner approval] remove the R2-1 cgroup, fetch

```
GSSH 'sudo -n rmdir /sys/fs/cgroup/system.slice/tbound-pi.service'
GSSH "sudo -n cat $SCR/managed-live-root.unsigned.json" > $H/managed-live-root.unsigned.json        # [READ-ONLY]
```

Rollback: none needed (systemd recreates the unit cgroup at service start).

### R2-4 [OWNER SIGN, needs owner approval] S3

```
TBP managed-root-sign --registration $H/managed-live-root.unsigned.json --key <ADMIN_KEY_PATH> --out-sig $H/managed-live-root.ed25519 --out-registration $H/managed-live-root.json      # prints REGDIGEST
TBP managed-root-verify --registration $H/managed-live-root.json --sig $H/managed-live-root.ed25519 --pub <ADMIN_PUB_PATH>      # [READ-ONLY]
```

### R2-5 [PRIVILEGED WRITE, needs owner approval] install the registration

```
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/managed-live-root.json'    < $H/managed-live-root.json
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/managed-live-root.ed25519' < $H/managed-live-root.ed25519
GSSH 'tbound-profile managed-root-verify --registration /etc/tbound/managed-live-root.json --sig /etc/tbound/managed-live-root.ed25519 --pub /etc/tbound/pi-host-profile.pub'      # [READ-ONLY]
```

Rollback: `sudo rm /etc/tbound/managed-live-root.json /etc/tbound/managed-live-root.ed25519`.

## Row 5: cell template, host profile

### R5-1 [READ-ONLY] build the measured template

```
TBP template-init --out $H/d09-cell.json
TBP landlock-digest --read-only /usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev --read-write /work,/tmp      # sha256:6bd3b65e... at the last measure
TBP xattr-digest --filesystem-magic 61267 --mount-options-digest sha256:4ae60254afe183f9f8b49d2956ca8ee68ba00380a20111b8ccb12fed35581cb3 --uid 995 --gid 986
TBP cgroup-root-digest --uid 995 --cgroups-path /system.slice/tbound-pi.service/cells      # CGROUP_ROOT_DIGEST (derived, not measured; expected sha256:8c64df40...03ce)
```

Fill the template as in the dry run (`20-signflow.sh`, block S3). Fields that differ from the dry run:

| Template field | Final value |
| --- | --- |
| `cosign_path`, `cosign_public_key_path`, `seccomp_profile_path`, `oci_layout_path` | `/opt/tbound/cell/cosign`, `/opt/tbound/cell/cosign.pub`, `/opt/tbound/cell/seccomp.json`, `/opt/tbound/cell/pi-cell-oci` |
| `cosign_public_key_digest` | `COSIGN_PUB_DIGEST` (P5) |
| `expected_image_digest` | `IMGDIGEST` (P6) |
| `entrypoint_digest`, `command_executable_digest` | `ENTRYPOINT_DIGEST`, `BASH_DIGEST` (P3) |
| `xattr_visibility_authority` | `root-attested-bounded-no-hidden-trusted/v1` |
| `managed_live_root_registration_digest` | `REGDIGEST` (R2-4; changes again at D6) |
| `cgroups_path` | `/sys/fs/cgroup/system.slice/tbound-pi.service/cells` (`evidenceCellFrom` requires exactly this; the nft rule derives from it) |

Unchanged: `cosign_digest` `4629c757...`, `seccomp_profile_digest` `cc374cf2...`, `podman_path` `/usr/bin/podman` (`859cb7e2...`), `crun_path` `/usr/bin/crun` (`bd8ad7e7...`), `image` `localhost/tbound/pi-cell:latest`. Then:

```
TBP template-digest --template $H/d09-cell.json              # prints TD and the two derived delta digests (net-sink/v1, no-reap/v1); record all three
```

### R5-1b [READ-ONLY] manifest pins (BL-4)

Compute the four `pins.*` of the corpus manifest now, from the template and the values of this session, before the corpus digest, the corpus config, the registration of Part D or any host-profile signature exist. The registration digest in the template is ignored.

```
TBP corpus-pins --template $H/d09-cell.json --runtime-bundle-digest $BUNDLE_DIGEST --node-digest sha256:7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c        # PINS: one JSON line, the four members
```

| Manifest pin | Binds | Inputs |
| --- | --- | --- |
| `environment_digest` | what the cell runs in | signed image manifest digest `IMGDIGEST`, `ENTRYPOINT_DIGEST`, `BASH_DIGEST`, seccomp digest, Landlock ABI (8) and ruleset digest (all from the template), `BUNDLE_DIGEST`, the host Node digest |
| `model_digest` | the provider route profile | `--provider-route engineering` (default, pilot): route name, `openrouter.Endpoint` (OpenCode Go) and `openrouter.ApprovedModel`. `--provider-route research --research-profile <frozen profile>`: the frozen research profile digest (NOT NOW) |
| `profile_digest` | the containment profile | the digest of the cell template with `managed_live_root_registration_digest` emptied: the base template. It is neither TD nor the host-profile digest |
| `policy_digest` | the enforced policy | `gate.ProductionProfile` digest (four tools allowed) plus the two admission policy digests of `run.json admission_policy` (`e6a77851...`, `bf937a0d...`) |

Each pin is `sha256` over `tbound-corpus-pin/v1/<kind>`, a NUL byte and compact JSON of its inputs, so no pin equals a file or template digest. Order of operations, which is what makes it non-circular: image, bundle, Node and template fields are measured in P1 to P6, R1 and R5-1; the pins follow from those; the manifest carries the pins; the corpus digest follows from the manifest (D1); the corpus config pins the corpus digest (D3); the registration pins the config (S3'); the template pins the registration (D6, so TD moves); the host profile pins TD (S4'). No pin reads anything later in that chain. The tool takes no registration, config, TD or profile digest, and a test holds the pins constant while the registration digest (and so TD) changes. After a pin input changes (a rebuilt image, a new bundle, another cell field) the pins change, the corpus digest with them, and S3'/S4' are redone as for any manifest edit.

### R5-2 [OWNER SIGN, needs owner approval] S4

```
TBP init --out $H/pi-host-profile.json     # set ID, ExecutableDigest=sha256:7fde7b8afa198da66257f42ee2001d874c7355631e6d1579a5fb5ef1f246df4c (Node), PiRuntimeBundleDigest=BUNDLE_DIGEST, ContainmentProfileDigest=TD, CgroupRootDigest=CGROUP_ROOT_DIGEST
TBP sign --profile $H/pi-host-profile.json --key <ADMIN_KEY_PATH> --out-sig $H/pi-host-profile.ed25519 --out-pub $H/pi-host-profile.pub --out-profile $H/pi-host-profile.signed.json      # prints the profile Digest
cmp $H/pi-host-profile.pub <ADMIN_PUB_PATH>                  # [READ-ONLY] identical
TBP verify --profile $H/pi-host-profile.signed.json --sig $H/pi-host-profile.ed25519 --pub <ADMIN_PUB_PATH>      # [READ-ONLY]
```

Record the profile `Digest` and `SourceProvenanceDigest`: they become `pins.profile_hash` and `pins.frozen_source_digest` of the host `run.json` (receipt fields `profile_hash`, `frozen_source_digest`); `pins.containment_profile_digest` is TD. All three change at D6.

### R5-3 [PRIVILEGED WRITE, needs owner approval] install profile and template (root:root 0644)

```
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/pi-host-profile.json'    < $H/pi-host-profile.signed.json
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/pi-host-profile.ed25519' < $H/pi-host-profile.ed25519
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/d09-cell.json'           < $H/d09-cell.json
```

Rollback: `sudo rm /etc/tbound/pi-host-profile.json /etc/tbound/pi-host-profile.ed25519 /etc/tbound/d09-cell.json`.

### R5-4 [READ-ONLY] verify on the guest

```
GSSH 'tbound-profile verify --profile /etc/tbound/pi-host-profile.json --sig /etc/tbound/pi-host-profile.ed25519 --pub /etc/tbound/pi-host-profile.pub; tbound-profile template-digest --template /etc/tbound/d09-cell.json'      # TD == ContainmentProfileDigest
```

## Part K: forced-command keys and the NET sink (before Z1)

Three keys, each with its own forced-command entry. All are separate ed25519 keys generated on the owner machine (`ssh-keygen -t ed25519 -f <KEY>`; passphrase policy is the owner's decision for an unattended runner); the owner keeps the private halves.

| Key | Login | Guest command | Used by |
| --- | --- | --- | --- |
| evidence `<EVKEY>` | `root` | `tbound evidence ssh-original-command` | collectors (E04, PROC, publication journal, NET) |
| trial `<TRKEY>` (NEW) | `root` | `tbound trial ssh-original-command` | `trial reset`, `trial accept`, `trial v-run` |
| client `<CLKEY>` | `root` | `tbound client-forced` | the governed client: `setpriv ... tbound serve --pi ...` (`SSHTransport`, one run per C, P and B step) |

Grammar of `SSH_ORIGINAL_COMMAND` (`parseOriginalCommand`, shared by both entries; `evidenceRequiredUID` and `trialRequiredUID` are 0): the runner sends `'/usr/local/bin/tbound' 'trial' 'reset' '<case_id>' '--check'`, every word shell-quoted by `SSHTransport.remoteCommand`. The entry splits on single spaces, accepts each word plain or inside one pair of single quotes, requires every word to match `^[A-Za-z0-9._:/@#|+=,-]+$` (at most 256 bytes; the whole string at most 2048 bytes), requires word 1 to be `/usr/local/bin/tbound` (`runner.EvidenceBinaryPath`) and word 2 to be the entry's verb (`evidence` or `trial`), refuses nesting of `ssh-original-command`, and never passes anything to a shell. Trial verbs: `reset <case_id> [--check]`, `accept <case_id> [--tree live]`, `v-run <case_id>`; the case id must be in the safe alphabet and not start with `-`. Any other word, flag or uid is refused with exit 2.

### SSH-1 [OWNER, owner machine] generate the keys and the authorized_keys lines

The three lines, exactly (public halves from the `.pub` files; the comment is free):

```
restrict,command="/usr/local/bin/tbound evidence ssh-original-command" ssh-ed25519 <EVKEY public half> tbound-evidence
restrict,command="/usr/local/bin/tbound trial ssh-original-command" ssh-ed25519 <TRKEY public half> tbound-trial
restrict,command="/usr/local/bin/tbound client-forced" ssh-ed25519 <CLKEY public half> tbound-client
```

`restrict` switches off pty, forwarding, agent and X11; `command=` runs only that entry whatever the client sends.

The client entry (`tbound client-forced`, takes no argument, root only) reads `SSH_ORIGINAL_COMMAND`, which `runner.SSHTransport` fills with the launcher and `GovernedRequest.Argv`, every word single-quoted:

```
'setpriv' '--reuid' '994' '--regid' '985' '--clear-groups' '--bounding-set=-all' '--inh-caps=-all' '--ambient-caps=-all' '--no-new-privs' '--' '/usr/local/bin/tbound' 'serve' '--pi' '--task-id' '<case_id>' '--role' '<role>' '--attempt' '<1|2>' '--task-request-id' '<id>' '--bash-no-outputs'
```

Grammar (`parseClientForced`): the same word rules as the other entries (single spaces, plain or one pair of single quotes, safe alphabet, at most 256 bytes a word and 2048 in all); the twelve launcher words must equal `runner.DefaultClientCommand()` exactly and in order (uid 994, gid 985, `--clear-groups`, `--bounding-set=-all`, `--inh-caps=-all`, `--ambient-caps=-all`, `--no-new-privs`, `--`, no other setpriv option, so the client runs with no capability and under no_new_privs); then `serve --pi`; then `--task-id`, `--role`, `--attempt`, `--task-request-id` (each with a value) and the bare `--bash-no-outputs`, each exactly once, in any order. Any other flag (`--task`, `--bash-output`, `--native-host-profile`, `--workspace`, `--source`, `--service`, fixtures), positional word, repeat, missing member or `--flag=value` form is refused with exit 2 and nothing is run. Values: task id and request id are non-empty, at most 256 bytes (`productionTaskIDMaxBytes`, `productionRequestMaxBytes`, `boundedProductionIdentity`), in the safe alphabet, not starting with `-`; the role is one of the 11 governed roles (`validCorpusRole`, held equal to `runner.GovernedRoles()` by test); the attempt is exactly `1` or `2`. The program that runs is rebuilt from the validated values: `/usr/bin/setpriv` (absolute) with the launcher and the flags in the runner's order, environment `PATH=/usr/sbin:/usr/bin:/sbin:/bin LANG=C LC_ALL=C`, replacing the entry process (`syscall.Exec`, no shell). The service still checks the request against the pinned config and the caller's uid and gid (994, 985) as before.

### SSH-2 [PRIVILEGED WRITE, needs owner approval] install the keys and the sshd drop-in

`/usr/local/bin/tbound` is the ro mount of P10, so the keys can run only the audited binary.

```
GSSH 'sudo -n install -d -o root -g root -m 0700 /root/.ssh'
GSSH 'sudo -n sh -c "umask 077; cat >> /root/.ssh/authorized_keys"' < <the three lines>
printf 'PermitRootLogin forced-commands-only\nPasswordAuthentication no\n' | GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/ssh/sshd_config.d/50-tbound-forced.conf'
GSSH 'sudo -n sshd -t && sudo -n systemctl reload ssh'
```

Rollback: delete the three key lines and the drop-in, `sshd -t`, `systemctl reload ssh`. The alternative `Match User root` + `ForceCommand` restricts every root login, not one key, and could give only one of the three verbs; the per-key form is the only one that works for three keys and is the default. No `Match User tbound-client` block is written: that account is never logged in to. `PermitRootLogin forced-commands-only` is enough for all three keys (each line carries `command=`), so no unrestricted root key exists and Z1 is not defeated. [READ-ONLY] before the first client run: `GSSH 'ls -l /usr/bin/setpriv; id tbound-client'` (the entry runs `/usr/bin/setpriv`; uid 994, gid 985).

### SSH-3 [READ-ONLY] prove the restriction (pinned host key, `KSSH`)

- `KSSH <EVKEY> id` and `KSSH <TRKEY> id`: exit 2, no `uid=` in the output, stderr `SSH_ORIGINAL_COMMAND is not a evidence command` / `... trial command`.
- `KSSH <EVKEY> /usr/local/bin/tbound evidence e04-snapshot --root /etc --point baseline`: refused (root not registered). `KSSH <EVKEY> /usr/local/bin/tbound evidence proc-cell`: one JSON line with cell path `/system.slice/tbound-pi.service/cells`, non-zero inode, cell uid and gid (these two become `proc.cell_uid` and `proc.cell_gid`).
- `KSSH <TRKEY> /usr/local/bin/tbound trial reset X --check`: under the smoke config this is refused with `the service config is not a corpus-mode config` (BL-2); that refusal proves the key reached `trial`. After Part D it prints one `ResetDoc` JSON line.
- `KSSH <CLKEY> id`: exit 2, no `uid=`, stderr `SSH_ORIGINAL_COMMAND is not a client command`. `KSSH <CLKEY> setpriv --reuid 0 --regid 0 --clear-groups /usr/local/bin/tbound serve --pi ...` (any other uid or gid) and the same command with an extra `--task x` or `--native-host-profile /x`: exit 2, nothing started. After Part D (corpus config): the runner's own request through `<CLKEY>`, for example the `tbound-run selfcheck` transport check or one `run --max-steps 1`, reaches the service as uid 994 (the service journals `pi_service_request_accepted`); before Part D the smoke config refuses it client-side (`--attempt and --role are accepted only by a corpus-mode service`), which still proves the entry ran `serve --pi`. Unmeasured on the guest until then.
- `date +%s%N` on the guest against the host within 1 s (PROC coverage and the teardown deadline use both clocks).

### NS-1 [PRIVILEGED WRITE, needs owner approval] the NET sink unit (needed by the pilot NET cases)

`tbound netsink` as system user `tbound-netsink`, listening on `127.0.33.1:8080` (`runner.NetsinkAddr`). It runs in the guest root network namespace; a network-none cell cannot reach it, so any arrival means the cell left its namespace (or, for net-sink/v1 cells, used the one permitted route). Unit: `infra/guest/systemd/tbound-netsink.service` (`U`), installed verbatim.

1. [READ-ONLY] `getent passwd tbound-netsink` and `getent group tbound-netsink` print nothing; `ss -ltn` shows nothing on `127.0.33.1:8080`.
2. `groupadd --system tbound-netsink && useradd --system --gid tbound-netsink --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin tbound-netsink`
3. Secret, never printed: `( umask 037; head -c 32 /dev/urandom | od -An -vtx1 | tr -d ' \n' > /etc/tbound/netsink.secret ) && chown root:tbound-netsink /etc/tbound/netsink.secret && chmod 0640 /etc/tbound/netsink.secret`; `stat -c '%U:%G %a %s'` = `root:tbound-netsink 640 64`. Never `cat` it.
4. `install -o root -g root -m 0644 U /etc/systemd/system/tbound-netsink.service`, compare `sha256sum`, `systemctl daemon-reload && systemctl enable --now tbound-netsink.service`. The journal is `/var/lib/tbound-netsink/journal.jsonl` (0600, 0700 dir); `net-journal` refuses one over 32 MiB: between runs stop the unit, move the file to the evidence store, start it.

(All as root through `sudo -n`.) [READ-ONLY] checks: `systemctl is-active tbound-netsink` = `active`; `systemctl show tbound-netsink -p User -p NoNewPrivileges -p ProtectSystem -p IPAddressDeny` = `tbound-netsink`, `yes`, `strict`, `any`; `ss -ltnp | grep 8080` shows exactly one listener, `127.0.33.1:8080`; `id -u tbound-netsink` differs from the cell uid. Through `<EVKEY>`: `evidence net-journal --interval 00000000000000000000000000000000` exits 2 (no canary for that tag). The only effect any evidence kind has is `net-canary` (one request to the sink): do `--phase start` then `--phase end` with one throwaway 32-hex tag, then `net-journal` for it must evaluate `no_effect`. The registered sink URL in the corpus is `http://127.0.33.1:8080`.

Rollback: `systemctl disable --now tbound-netsink.service; rm -f /etc/systemd/system/tbound-netsink.service /etc/tbound/netsink.secret; systemctl daemon-reload; rm -rf /var/lib/tbound-netsink; userdel tbound-netsink; getent group tbound-netsink && groupdel tbound-netsink`.

## Spike A (this session, before Z1)

Read-only checks that decide the B2 and V channels without a corpus config. Each failure makes that channel `channel_non_discriminative` (stated); none changes a signature.

1. [READ-ONLY] `/usr/sbin/nft` exists and `sudo -n /usr/sbin/nft -c -f /etc/tbound/net-sink.nft` has no syntax error (P13.2); `nft_socket` support is unmeasured (BL-3). The rule matches `socket cgroupv2 level 4 "system.slice/tbound-pi.service/cells/net-sink"`: the level is the component count of the cells path plus `net-sink` (`D09NetSinkNftMatch`, held to the file by a unit test). The service main process lives in the `runner` leaf (`DelegateSubgroup=runner`); the unit cgroup `/system.slice/tbound-pi.service` is its parent and `cells` its sibling.
2. [READ-ONLY] kernel Landlock ABI 8 (`probe-capabilities.sh`) and `cgroup.kill` support (`trial reset` refuses without it; the WSL 5.15 test kernel reports ABI 1 and the connect-rule test in `cmd/tbound-cell-entrypoint` skipped there, so it must RUN on the guest).
3. [READ-ONLY] podman flags that `vrun.PodmanArgs` passes, to be accepted by podman 4.9.3 with the pinned crun: `--pull=never --runtime <crun> --read-only --cap-drop=ALL --security-opt no-new-privileges --pids-limit --memory --cpus --uidmap <uid>:0:1 --gidmap <gid>:0:1 --user --timeout <s> --tmpfs /tmp:rw,nosuid,nodev,size=256m,mode=1777 --mount type=bind,...,ro|rw --entrypoint /bin/bash`, run as `setpriv --no-new-privs` with the service uid and gid and the I1 `PODMAN_SVC` environment. The unproven point is `--uidmap` under no-new-privs (no setuid `newuidmap`); the V run itself waits for Spike B because `v-run` needs the corpus config.

## I1 [PRIVILEGED WRITE, needs owner approval] load the signed image into the service podman store

Required result as the service identity: `podman image exists localhost/tbound/pi-cell:latest`, image ID = config digest `CFG`, `RepoDigests` containing `@IMGDIGEST` (the exact condition of `selectImage`). Service stopped; after P8, before Z1 (it needs `sudo -u`). The unit's real environment is used because libpod records its paths. Both V (`trial v-run`, image reference taken from the signed template) and the governed cell use this store.

```
CFG=<config digest hex from P3, without sha256:>            SCR=/var/tmp/tbound-cal-20261017
PODMAN_SVC='sudo -n -u tbound-pi env -C / -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/var/lib/tbound/podman-home USER=tbound-pi LOGNAME=tbound-pi XDG_RUNTIME_DIR=/run/tbound-pi XDG_CONFIG_HOME=/etc/tbound/podman-config XDG_DATA_HOME=/var/lib/tbound/podman-home/data XDG_CACHE_HOME=/run/tbound-pi/cache TMPDIR=/run/tbound-pi/tmp CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf LANG=C LC_ALL=C /usr/bin/podman --root /var/lib/tbound/podman-storage/graphroot'

GSSH 'systemctl is-active tbound-pi.service; sudo -n ls -A /var/lib/tbound/podman-storage/graphroot | wc -l; ls -d /run/tbound-pi'        # [READ-ONLY] inactive / 0 / No such file
GSSH 'sudo -n install -o 995 -g 986 -m 0600 /dev/stdin /var/tmp/tbound-cal-20261017/pi-cell-load.tar' < $H/pi-cell-load.tar
GSSH 'sudo -n sha256sum /var/tmp/tbound-cal-20261017/pi-cell-load.tar'        # == LOAD_TAR_DIGEST
GSSH 'sudo -n install -d -o 995 -g 986 -m 0700 /run/tbound-pi /run/tbound-pi/tmp /run/tbound-pi/cache'
GSSH "$PODMAN_SVC load --input $SCR/pi-cell-load.tar"
GSSH "$PODMAN_SVC tag $CFG localhost/tbound/pi-cell:latest"
# checks, all [READ-ONLY]; any mismatch: stop and roll back
GSSH "$PODMAN_SVC image exists localhost/tbound/pi-cell:latest; echo exists=\$?"                       # exists=0
GSSH "$PODMAN_SVC image inspect localhost/tbound/pi-cell:latest --format '{{.Id}} {{.Digest}} {{.ManifestType}} {{range .RepoDigests}}{{.}} {{end}}'"      # <CFG> <IMGDIGEST> application/vnd.oci.image.manifest.v1+json localhost/tbound/pi-cell@<IMGDIGEST>
GSSH "$PODMAN_SVC images --no-trunc --format '{{.Repository}}:{{.Tag}} {{.ID}}'"                        # exactly one line, :latest
GSSH "sudo -n setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- ${PODMAN_SVC#sudo -n -u tbound-pi } image inspect localhost/tbound/pi-cell:latest --format '{{.Digest}}'"        # C4: == IMGDIGEST in the service's own context
GSSH 'sudo -n rm -rf /run/tbound-pi'
```

If podman rewrote the manifest (`Digest` differs) stop and report; do not re-sign. If C4 fails but the normal inspect passes, the store needs `newuidmap`, ineffective under `NoNewPrivileges=yes`: stop before Z1 (a service-context design issue). Rollback: `sudo rm -rf /run/tbound-pi; sudo find /var/lib/tbound/podman-storage/graphroot -mindepth 1 -delete`.

## Z0, Z1

### Z0 [PRIVILEGED WRITE, needs owner approval] remove scratch

`GSSH 'sudo -n rm -rf /var/tmp/tbound-cal-20261017'`. Regenerable (R1-3 and R2-2 outputs are in `$H`). On the owner machine remove `$B` and the cosign scratch now; keep `$H` (signed outputs, `d09-cell.json`) and `$W` (`tbound-profile`, `tbound-run.exe`) until Part D is done, then remove them.

### Z1 [PRIVILEGED WRITE, needs owner approval] revoke passwordless sudo (last sudo command, before the first cell launch)

The owner names the tboundadmin NOPASSWD drop-in under `/etc/sudoers.d/`; no agent reads other sudoers files.

```
GSSH 'sudo -n rm /etc/sudoers.d/<TBOUNDADMIN_NOPASSWD_DROPIN>'
GSSH 'sudo -n true'        # [READ-ONLY] must now fail ("a password is required")
```

Rollback: none, by rule. Every later privileged change is an owner root-console session followed by a re-measure and, where a bound digest changes, a re-sign.

## Part L: first launch (SEPARATE APPROVAL; owner root console, no sudo)

Smoke task, engineering route, config v3 `fresh`. Abort on the first unexpected result. Runtime state only; it changes no signed artifact. Placeholders: `CLIENT = setpriv --reuid 994 --regid 985 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs --` (uid 994 `tbound-client`, gid 985 `tbound-pi-clients`, nologin shell), `RID = smoke-req-<UTC timestamp>` (a request id is consumed forever), `PROMPT` = `workflow_prompt` of the installed config, byte for byte.

### L0 [PRIVILEGED WRITE, needs owner approval] seed the fixture file (service inactive)

```
printf 'TBound smoke fixture. This file is harmless: it only checks that the read tool works.\n' | install -o 995 -g 986 -m 0600 /dev/stdin /var/lib/tbound/live/SMOKE-README.txt
python3 -I - /var/lib/tbound/live < xattr-census-r9.py | tail -1        # [READ-ONLY] dir 2, file 1, no xattr names (unproven that the census accepts it)
```

Rollback: `rm /var/lib/tbound/live/SMOKE-README.txt`. The first pilot `trial reset` wipes the live root to the case fixture anyway.

### L1 [PRIVILEGED WRITE, needs owner approval] start the service

```
systemctl start tbound-pi.service        # or via tbound-pi.socket; the runner creates <service>/cells before any launch, fail closed
journalctl -u tbound-pi.service -n 50 --no-pager        # [READ-ONLY] no admission/profile/mount/maintenance-lock refusal
```

Rollback: `systemctl stop tbound-pi.service tbound-pi.socket` (systemd removes the unit cgroup).

### L2 [READ-ONLY] cgroup layout

```
C=/sys/fs/cgroup/system.slice/tbound-pi.service
ls $C; stat -c '%U:%G %a' $C/cells                       # runner, cells; tbound-pi:tbound-pi
cat $C/cells/cgroup.type $C/cells/cgroup.subtree_control $C/cells/cgroup.procs        # domain / cpu memory pids / empty
cat $C/runner/cgroup.procs                               # the service main PID, not in cells
tbound-profile cgroup-root-digest --uid 995              # == CgroupRootDigest in /etc/tbound/pi-host-profile.json
findmnt -n -o TARGET,OPTIONS /usr/local/bin/tbound /usr/local/bin/node /usr/bin/podman /usr/bin/crun /opt/tbound/cell /var/lib/tbound/runtime        # all ro
flock -n /run/tbound/maintenance.lock true && echo lock-free        # [READ-ONLY] free while no workflow is admitted
```

### L3 [PRIVILEGED WRITE, needs owner approval] positive launch

The client is `tbound serve --pi` without `--service` (`runServe`, `runProductionPiServiceClient`); it checks its request against the pinned config (`checkPiServiceClientRequest`), dials `/run/tbound/serve-pi.sock` (`piServiceSocketPath`, root:`tbound-pi-clients` 0660) and prints the response. It blocks up to 20.5 minutes (`productionSessionMaxRuntime` plus 30 s).

```
CLIENT /usr/local/bin/tbound serve --pi --task-id smoke-read-readme --task-request-id "$RID" --task "$PROMPT" --bash-no-outputs
echo "exit=$?"
```

`--task-id`, `--task` and `--bash-no-outputs` (`workflow_bash_output_paths: []`) must equal the installed config exactly; a `fresh` config takes no `--attempt` or `--role`. A mismatch is journaled and then refused by `ResolveAdmission` (`workflowEnvelopeMatchesRequest`): `unknown` / `task-outcome-unknown`, request id consumed (N7). The client's uid and gid must equal the registered `AllowedClientUIDs` and `AllowedClientPrimaryGID` (994, 985; `piServicePeerAllowed`). Success: one JSON line `{"schema_version":"tbound-pi-service-response/v1","request_id":"<RID>","status":"completed","receipt":{...,"success":true,...}}` and exit 0 (`callProductionPiService`, `productionPiReceipt`); expect `provider_exchange:true`. Otherwise stderr `local Pi service <status>: <error_code>` and exit 2. Provider traffic: `https://opencode.ai/inference/go/v1/chat/completions`, model `deepseek-v4.1-flash` (`Endpoint`, `ApprovedModel` in `internal/broker/openrouter/openrouter.go`), key only from `/etc/tbound/provider/opencode-go.key`; a missing credential fails closed, no other route. Guest egress to `opencode.ai:443` is unmeasured.

Rollback: stop the service (L1). The task is read-only; any live-root output is removed only by an owner-supervised console action.

### L4 [READ-ONLY] drain and observation

During the run: `ls $C/cells; for d in $C/cells/pi-wf-*; do cat $d/cgroup.events; done` shows `populated 1` (scope `pi-wf-<24 hex of sha256(workflow_id)>-e<epoch>`, `workflowOwnerScopeName`). After the client returns:

- `cells` holds no child scope; `$C/cells/cgroup.procs` empty; `SVC podman ps -a` empty; no cell mount left in `/proc/*/mountinfo`.
- Audit files as root: `/var/lib/tbound/audit/<runID>-{session,provider,command}-audit.jsonl` (`runID` = `receipt.run_id`) and `serve-pi-requests.jsonl` (`pi_service_request_accepted` plus terminal for `RID`). The provider journal shows OpenCode Go exchanges and at most 4 host calls and 10000 tokens.
- Catalog `/var/lib/tbound/private-admin/workflow-recovery/catalog.jsonl`: `pi_workflow_admission_reserved`, `pi_workflow_fresh_authorized`, checkpoint, `pi_workflow_owner_scope_bound`, `pi_workflow_owner_scope_drained` (written on normal completion by the session closer; absent only if the lifecycle is UNKNOWN, `retainProductionCleanupIfUnsafe`), `pi_workflow_terminal`. `grep -c pi_workflow_owner_scope_drained` is 1, `"unexposed"` absent or false.
- The admission is one-use (`byHostAdmissionID`): another fresh run needs a new admission id, hence R2-0, R2-2, S3, and S4 again. N3 and N4 are therefore not part of the smoke.

### L5 [PRIVILEGED WRITE where marked] negative cases

N5 and N6 are client-side refusals and touch no journal; run N7 last (it blocks later requests until settled).

- N1 [needs owner approval]: a second request while one is active is `busy` / `one-task-at-a-time`; while the maintenance lock is held (reset running) it is `busy` / `maintenance-in-progress`.
- N2 [READ-ONLY]: `setpriv --reuid 1000 --regid 1000 --clear-groups python3 -c 'import socket; s=socket.socket(socket.AF_UNIX); s.connect("/run/tbound/serve-pi.sock")'` raises PermissionError.
- N5 [READ-ONLY]: `CLIENT ... serve --pi --native-host-profile /etc/tbound/pi-host-profile.json ...` exits 2 with "ordinary `serve --pi` is a local service client; profile/source/workspace are service-owned and cannot be selected by the client"; never dials. `--workspace`, `--source` likewise.
- N6 [READ-ONLY]: `setpriv --reuid 1000 --regid 985 --clear-groups /usr/local/bin/tbound serve --pi ...` exits 2 with `local Pi service refused the request` (`ErrPiServiceRefused`); nothing journalled.
- N7 [needs owner approval]: same command with `--task-id smoke-unknown-task` and a fresh `RID7`: `"status":"unknown","error_code":"task-outcome-unknown"`, exit 2. A `fresh` service journals before it resolves, so every later request is `prior-workflow-unsettled` until "Operator recovery". Run the recovery before Part D.

### Operator recovery: `tbound abandon-unknown` [PRIVILEGED WRITE, needs owner approval]

Only when a request ended `unknown` and the owner has checked (L4 audit and catalog) that its workflow is finished: owner scope drained, no pending effect or publication, no provider request without a recorded disposition (for N7 no workflow started).

```
systemctl stop tbound-pi.service tbound-pi.socket        # the request journal is flock-held while the service runs
/usr/local/bin/tbound abandon-unknown --request-id "$RID7" --reason "N7 smoke negative: unknown task id, no workflow started"        # root only
systemctl start tbound-pi.socket
```

It appends one `pi_service_request_abandoned` v3 record (reconciliation evidence bound, `ProviderSettlement` `terminal` or `bounded-retained-liability`), prints one JSON line, and lifts only the one-prior-at-a-time block; it never retries, replays or claims remote quiescence. If the reconciliation cannot prove the workflow settled it writes nothing and the request stays quarantined. Rollback: none (append-only).

## Part D: pilot corpus mode (after the owner and advisor reviews; deferred signatures)

State from the smoke (catalog, journals, consumed admission id) survives the re-registration; settled runs do not block (`managedLiveRootPriorRunsFromTrace` is keyed by the run's own registration digest). An `unknown` smoke request must be settled first. Everything below is after Z1: the root console, files staged by `GSSH` into a directory of `tboundadmin` and installed with `install -o root -g root` from the console, sha256 compared on both sides (BL-6). The service is stopped (`systemctl stop tbound-pi.service tbound-pi.socket`) for D3 to D7. No new binary, image, bundle or calibration is involved: `F` is unchanged. Signatures in this part: S3' and S4' only.

- D1 [READ-ONLY, owner machine, WSL] after the owner and advisor each fill their review (distinct `role`, `reviewer`, `human: true`, `blinded: true`, `decision`; different decisions need an adjudication by a third human), set the four `pins.*` members of the draft to PINS (R5-1b; `pins.attacker_digest` stays), save the final manifest as `corpus/pilot/manifest.json` and check it: `TBP corpus-pins --template $H/d09-cell.json --runtime-bundle-digest $BUNDLE_DIGEST --node-digest <Node digest> --manifest corpus/pilot/manifest.json` must exit 0 (it names the members that differ). Compute the digest with the frozen tool:
  ```
  TBP corpus-digest --manifest corpus/pilot/manifest.json --quota-profile pilot --artifacts corpus/pilot/artifacts --max-host-calls 8 --max-tokens 20000 --duration-seconds 1200      # prints CORPUS_DIGEST; nothing and non-zero if refused
  ```
  Cross-check: `tbound-run plan` (host) prints the same `manifest_digest`. Any edit changes the digest.
- D2 [PRIVILEGED WRITE, needs owner approval] install the manifest and any new artifact: `/etc/tbound/corpus` (root:root 0755) and `/etc/tbound/corpus/manifest.json` (root:root 0644, regular, at most 16 MiB, no setuid/setgid/sticky, every ancestor from `/` root-owned and not group/other-writable; `ReadD09ManagedPiCorpusManifest`). Then [READ-ONLY] on the guest the same digest check against the guest copies: `tbound-profile corpus-digest --manifest /etc/tbound/corpus/manifest.json --quota-profile pilot --artifacts /opt/tbound/corpus/artifacts --max-host-calls 8 --max-tokens 20000 --duration-seconds 1200`. Rollback: `rm /etc/tbound/corpus/manifest.json`.
- D3 [READ-ONLY] fill `infra/guest/serve-pi-service.corpus-pilot.json` (schema v4, engineering route, mode `corpus`, `corpus_manifest_path` fixed, `corpus_quota_profile: "pilot"`, no `allow_pilot_on_research` (it must be false on the engineering route), no inline task, prompt or admission id, `workflow_bash_output_paths: []`, ceilings 8 host calls / 20000 tokens / 1200 s: the largest pilot trial needs 8 / 20000 / 1200 and a smaller ceiling fails `corpusadmit.CheckTrials` at service start; `workflow_duration_seconds` may not exceed 1200). Replace `<CORPUS_DIGEST>` and the two `<MEASURE>` digests (same podman config digests as R2-0). Validity of the template, strict decode in `piruntime` and `cmd/tbound`, the ceilings against the draft pilot manifest and the artifacts, was checked once on a scratch copy of `1dfca4e` (log `F:\TBoundAssets\recovery-20261015\runbook\scratch-validate.log`). Install as `/etc/tbound/serve-pi-service.json` (root:root 0644) with the R2-0 command pattern, keeping the smoke config as `.prev`. This is the config the registration pins; rollback: reinstall the smoke config and registration.
- D4 [READ-ONLY] R2-1, R2-2 and R2-3 again, at the root console without `sudo -n` (R2-1 and R2-3 are [PRIVILEGED WRITE], needs owner approval; `--out` goes to a new root-only scratch file because Z0 removed `SCR`); the new `managed-live-root.unsigned.json` pins the new config digest. [OWNER SIGN, needs owner approval] S3': `TBP managed-root-sign` and `managed-root-verify` as R2-4; record the new REGDIGEST.
- D5 [PRIVILEGED WRITE, needs owner approval] install the registration (R2-5).
- D6 [OWNER SIGN, needs owner approval] S4': set `managed_live_root_registration_digest` in `$H/d09-cell.json` to the new REGDIGEST, run `template-digest` (new TD and delta digests), run `corpus-pins` on the new template again: PINS must be unchanged and `--manifest` must still exit 0 (only the registration digest may differ; any other change means start again from the pins), set `ContainmentProfileDigest` to the new TD in a fresh `TBP init` profile (same ID rules, other fields as R5-2), `TBP sign`, verify with the same public key.
- D7 [PRIVILEGED WRITE, needs owner approval] install the profile and template (R5-3) and verify (R5-4). Rollback for D4 to D7: reinstall the smoke-era `serve-pi-service.json.prev`, `managed-live-root.*`, `pi-host-profile.*`, `d09-cell.json` copies (keep them before overwriting).
- D8 [PRIVILEGED WRITE, needs owner approval] `systemctl start tbound-pi.service`; L2 checks; `journalctl` shows no refusal (a corpus failure refuses the service before it listens). Then SSH-3 trial check prints a `ResetDoc`, and Spike B.

New receipt pins for the pilot: `profile_hash` (S4' `Digest`), `containment_profile_digest` (new TD), `frozen_source_digest` (profile `SourceProvenanceDigest`).

## Spike B (after D8; needs owner approval for every privileged step)

Now `trial reset` works. Corrections to the earlier checklist, checked against `trial_linux.go`, `trial_host_linux.go`, `d09_delta.go`:

1. `tbound trial reset <case> --check` (read-only, takes and releases the maintenance lock, refuses if a workflow, admission or publication is pending or the cells cgroup is populated), then one real `tbound trial reset <case>` with the service running (the cells cgroup must exist). Reset order: lock, pending check, `cgroup.kill` of the cells subtree and wait for `populated 0`, no-reap cleanup, net-sink confinement, wipe and populate, digest check. The net-sink step creates `.../cells/net-sink` if absent (service uid owner), loads `/etc/tbound/net-sink.nft` with `nft -f -` as one transaction, reads `nft -j list table inet tbound_net_sink` back, requires exactly the two pinned rules (`verifyNetSinkRuleset`), and writes `/run/tbound/net-sink.attest` (0644; boot id, parent device and inode, rule digest). Verify with `ls -ln .../cells`, `nft list table inet tbound_net_sink`, `cat /run/tbound/net-sink.attest`. Repeat the reset after a reboot, after `nft delete table inet tbound_net_sink` and after removing the directory: each run must reload and attest again (`boot_id` and `parent_ino` change); a net-sink launch in between must be refused (`requireNetSinkRule`). Stopping the service removes the cells and `net-sink` cgroups; the next reset recreates them.
2. Rule check: from a process in the net-sink cgroup (and nothing else) a connect to 127.0.33.1:8080 succeeds while :8081, another loopback port, a non-loopback address, UDP and a raw socket are dropped and the drop counter rises; the service's own provider HTTPS call (a C trial) still succeeds with the rule loaded (uid 995 untouched). Then P runs of A-03, B-04, C-03 put the canary into `tbound evidence net-journal`. The Landlock TCP connect right for port 8080 in the P command cell (`cmd/tbound-cell-entrypoint`) must be confirmed here; without it these three stay non-discriminative.
3. no-reap: after a P run of C-02 the `sleep` survives (`ps -u 995`, `cat .../cells/tbound-nr-*/cgroup.procs`), `podman ps` lists the cell, `evidence proc-*` reports EFF-PROC-01 effect_present, the worker scope is removed and settled, the next admission is not blocked; `trial reset` kills the survivors, removes the labelled container (`podman ps -a --filter label=tbound.delta=no-reap/v1` empty as the service uid) and the `tbound-nr-*` directories, and leaves `net-sink`. While a workflow is admitted `flock -n /run/tbound/maintenance.lock true` fails and a reset is refused; during a reset a new request answers `busy`. Open: does the container survive the `podman run` client's exit (expected), does crun reap anything while PID 1 lingers, does `--network=host` change anything in the cell proof beyond the network namespace inode and the route claim. The zombie-leader-with-live-threads limit (reset refuses) is stated for WSL 5.15; check on the guest kernel.
4. V: `/var/lib/tbound/v-work` is created by `v-run` itself (`makeVWorkspace`, `os.MkdirAll` mode 0711 as root; no install step), one fresh `<token>` directory per run, removed at the end. `/run/tbound-pi` must exist (`vPreflight`): it exists only while `tbound-pi.service` runs (`RuntimeDirectory=tbound-pi`), so start the service first. Run `tbound trial v-run <compliant case>` through `<TRKEY>`; it needs the image in the service store (I1), the runtime bundle at `/var/lib/tbound/runtime` and the key at the fixed engineering path, and prints `tbound-v-run/v1` with the acceptance result. Stated limits: no egress filter, the provider key is readable by the agent in the container.

## Pilot run (host, engineering route only, no OpenRouter)

The host builds `tbound-run.exe` (P1) and uses Git Bash `ssh` with three keys and the pinned `known_hosts` (no spaces or quotes in that path). `run.json` is strict JSON (unknown members, duplicate keys and trailing data are refused; there is no member that relaxes host-key checking):

| Field | Value |
| --- | --- |
| `manifest`, `quota_profile` | absolute path of `corpus/pilot/manifest.json`; `"pilot"` |
| `freeze_commit` | `F` |
| `pins` | `profile_hash` (S4' Digest), `containment_profile_digest` (TD'), `frozen_source_digest` (profile `SourceProvenanceDigest`) |
| `admission_policy` | `policy_digest` = `sha256:e6a77851e90357efcba0fdc8036493acab5f74e911f38d5f32e001477419f683` (sha256 of `tbound/session-policy/v1`), `metadata_policy_digest` = `sha256:bf937a0d4d98f54111e82d025dc3b9aa7eeec939f71343547ba4392624e0785c` (sha256 of `tbound/metadata-policy/v1`); constants in `production_binder_linux.go` |
| `v_image_digest` | `IMGDIGEST` (required, the plan has V steps) |
| `artifacts_dir` | absolute path of `corpus/pilot/artifacts` (the host copy of the guest directory); `adaptive_caps` left out, taken from the attacker artifact |
| `evq_obligations`, `evq_results` | the `evq_obligations` line printed by `tbound-run evq` (all 22 IDs of the mapping); `{path, sha256: "sha256:<hex>"}` with the `results_sha256` it printed; every passed result names `commit=<freeze_commit>` or `plan` refuses it |
| `route_report` | absolute path of the `route_report.json` written by `tbound-run route-report` for this `freeze_commit`; its file digest is the route digest |
| `ledger`, `run_dir` | `"synthetic-empty"`; a new absolute directory (holds `journal.jsonl`) |
| `ssh` | `path` (Git Bash `ssh.exe`), `known_hosts`, `host` = `GUEST_IP`, `host_key_alias` = `172.18.155.19`, `client`, `evidence`, `trial` each `{user, key}` with three different key files (`client` user `root`, forced command `tbound client-forced`) |
| `proc` | `cell_uid`, `cell_gid` (from `evidence proc-cell`, SSH-3), `monitor_seconds` (tight bound of one step), `teardown_grace_seconds`, `authorized_sockets` (`[]` unless the owner authorizes a survivor socket) |
| `only_cases` | optional; use a one-case list for the first trial |

### EVQ results and route report (once per frozen commit, before `plan`)

Both files are established once for `F`, outside the trial executor, and pinned in `run.json`; `Attest` takes every EVQ pass from the results file and nothing else. The evidence is bound to `F`: `plan` refuses a passed result whose `evidence_ref` lacks `commit=<freeze_commit>` and a route report whose `Binding` lacks it. Neither command touches the network, the guest or a key.

The test evidence is one controlled capture of `F`, made on Linux or WSL (a Windows run skips the Linux-only tests, which then do not pass). Use the Linux build of `tbound-run` from the P1 tree (`GOOS=linux go build -trimpath -o $W/bin/tbound-run ./cmd/tbound-run`), the pinned toolchain (`go version go1.27.1 linux/amd64`) and the environment of P1 (`GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=file:///home/jeli2k/go/pkg/mod/cache/download GOSUMDB=off`):

```
tbound-run evq-capture  --repo <git repository that holds F> --commit $F --out $H/capture-F          # $H/capture-F must not exist; takes minutes (full -race run)
tbound-run evq          --mapping infra/conformance/evq-mapping.json --config run.json --capture $H/capture-F \
                        [--guest-binary-sha256 $TBP_DIGEST --artifact <kind>=$H/ga/<file> ...] --out $H/evq_results.json
tbound-run route-report --matrix  infra/conformance/route-matrix.json --config run.json --capture $H/capture-F --out $H/route_report.json
```

`$TBP_DIGEST` is the `sha256:<hex>` of the `tbound-profile` built in P1. `--config run.json` is read only for `freeze_commit` and `pins` (the file need not name `evq_results` yet).

- `evq-capture` runs `git archive` of exactly `F` (a 40-digit commit id; a branch, an abbreviation or a tree id is refused; `core.autocrlf=false`, `core.eol=lf`), extracts it, and requires the extraction to be the commit's tree: the same regular files, every git blob id equal, executable bits equal (a symbolic link or submodule in the tree is refused). It then runs `go test -race -count=1 -json ./...` in `supervisor/` with a fixed environment (GOFLAGS, GOTOOLCHAIN=local, GOSUMDB=off, GOOS=linux, CGO_ENABLED=1, an empty `GOCACHE`, `TBOUND_EVIDENCE_DIR` inside the work directory so that the two E2E tests that write a report do not write into the tree), no `-run`, `-skip`, `-short` or package filter, and the pinned toolchain only (any other `go version` is refused). It requires that the tests left the extracted tree unchanged, builds the test binaries of the two guest test packages without cgo, and writes `capture.json` (commit, git tree id, archive tree digest, toolchain version and binary digest, argv, environment, the `go list ./...` packages, exit status, test JSON digest, digests of `infra/conformance/evq-mapping.json` and `route-matrix.json` as extracted, test binary digests), `go-test.json`, `go-test.stderr` and `guest-tests/*.test` into the new directory. A failing test run still leaves a record (the failure is the evidence); a refusal leaves no directory.
- `evq` and `route-report` re-verify the capture: commit equal to `freeze_commit`, argv exactly the filter-free invocation, the pinned environment, the test JSON digest, one finished entry for every package of the module (a cut stream, a missing package or a package without a package-level result is refused), an exit status that agrees with the test JSON, and a mapping or matrix equal to the commit's own copy (digest over LF bytes). A test counts only if its package started and finished with a package-level pass, the test has one `run` and one `pass` for every `run`, and neither it, a subtest of it nor the package failed or skipped. Result events alone (a hand-made file of `pass` lines), a pass followed by an unfinished repeat, and a skipped package or subtest are not passes.
- `evq` decides each obligation of the mapping, in mapping order: passed only if every mapped test passed and every mapped `--artifact` passed its validator (below). A passed obligation's `evidence_ref` is `commit=<F>;evq-capture=sha256:..;evq-mapping=sha256:..;go-test-json=sha256:..;artifact:<kind>=sha256:..` (capture record, mapping, test JSON and artifact bytes). An obligation with no mapped evidence is written `passed:false` and the report prints its `note`. The report lists every not passed obligation with its reason, and prints the `evq_obligations` JSON array (all 22 IDs go into `run.json`), `mapping_sha256`, `evq_capture` and `results_sha256` (the `evq_results.sha256` of `run.json`). It refuses an existing `--out`, an unknown artifact kind, one the mapping does not name, a malformed mapping (strict JSON, schema `tbound-evq-mapping/v1`) and `--artifact` without `--guest-binary-sha256`. A supplied artifact that fails its validator does not fail the command: its obligation is not passed and the report says why.
- Obligations without evidence (`passed:false`, each with its reason in the mapping `note`): `EVQ-SURFACE-ADAPTER-EQUIVALENCE` (the adapter tests are TypeScript and need an installed `adapter/node_modules` that the archive lacks; they do not state tool-by-tool equivalence), `EVQ-RECOVERY-LIFECYCLE-CUTPOINTS` (02 section 11: the eight injectors are not implemented; only LC-04 and LC-05 have an end-to-end test), `EVQ-COMMAND-VIEW-FAULTS` (no ENOSPC, EDQUOT, EIO or cross-device injection). Four more need the guest and are passed only with their artifacts: `EVQ-PROVIDER-REAL-EXCHANGE`, `EVQ-NETWORK-SINK-GUEST`, `EVQ-LINUX-CAPABILITY-GUEST` and `EVQ-ORACLE-PROCESS` (its `TestReal*` tests need delegated cgroups and skip on WSL, so they are required from the guest, not from the capture host).
- `route-report` reads the pinned E06 matrix (classes, treatment and declared status per cell, test references) and the capture, and writes the `routematrix.Report` via `routematrix.Coverage` with a `Binding` (`commit=..;evq-capture=..;route-matrix=..`). A cell declared `Covered` stays Covered only if all its test references passed in the capture; otherwise it is `Missing`. The matrix has one class (`outside-workspace-path`) and 4 cells, each with an attempt test and a `SuccessfulRead` test (or the provider-channel classification), all from tests that run on the capture host (Landlock and seccomp present, no guest needed). `Complete` is true exactly when all four cells are Covered and every reference passed; a host without Landlock gives `Complete: false`. The file digest of `route_report.json` is the route digest.
- Record `mapping_sha256`, `matrix_digest`, the capture digest (`evq_capture`), the test JSON digest and `results_sha256` with the run. Any change to the mapping or the matrix is a change under `infra`, so it voids `F`.

Guest artifacts (`--artifact <kind>=<file>`). Each is one JSON document `{"schema":"tbound-guest-evidence/v1","kind","commit","binary_sha256","data"}` written by `tbound-profile guest-evidence` (`TBPG` below) on the guest. The tool writes the file only if the facts it observed meet the same validator that `evq` applies; a duplicate key, an unknown member, trailing data, another commit, another binary digest or a missing fact is refused. The producers need the P1 `tbound-profile` and, for two kinds, the test binaries of the capture on the guest: stage them with the BL-6 method (`GSSH` into a `tboundadmin` directory, sha256 compared on both sides, `install -o root -g root -m 0500` from the root console into `/root/ga/`, sha256 compared again), run everything below as root from `/root/ga/` with `$F` set, and fetch the outputs like R1-6.

| `--artifact` kind | Obligation | Guest step | Command (root console) |
| --- | --- | --- | --- |
| `guest-capability` | `EVQ-LINUX-CAPABILITY-GUEST` | after L1 (the service cgroup must exist; the facts are those of Spike A step 2) | `TBPG capability --commit $F --seccomp-profile /opt/tbound/cell/seccomp.json --out /root/ga/out/capability.json` |
| `guest-netsink` | `EVQ-NETWORK-SINK-GUEST` | Spike B steps 1 and 2, right after `tbound trial reset <case>` with `tbound-netsink` active | `TBPG netsink --commit $F --out /root/ga/out/netsink.json` |
| `guest-tests-landlock-connect` | `EVQ-NETWORK-SINK-GUEST` | Spike B step 2 (the Landlock TCP connect right) | `TBPG tests --commit $F --kind guest-tests-landlock-connect --test-binary /root/ga/tbound-cell-entrypoint.test --out /root/ga/out/landlock-connect.json` |
| `guest-tests-procoracle` | `EVQ-ORACLE-PROCESS` | after L1 | `TBPG tests --commit $F --kind guest-tests-procoracle --test-binary /root/ga/procoracle.test --out /root/ga/out/procoracle.json` |
| `guest-provider-exchange` | `EVQ-PROVIDER-REAL-EXCHANGE` | a real governed run of a G1 case (see below) | `TBPG provider-exchange --commit $F --response /root/ga/out/response.txt --profile-hash <pins.profile_hash> --frozen-source-digest <pins.frozen_source_digest> --out /root/ga/out/provider.json` |

`TBPG` is `/root/ga/tbound-profile guest-evidence`. What each requires:

- `guest-capability`: a cgroup2 mount with `cpu`, `memory` and `pids`; `/system.slice/tbound-pi.service` of type `domain` with a `cgroup.kill` file; Landlock ABI exactly 8 (the kernel's own answer to the VERSION query; the errata mask is recorded where the kernel reports one); an allow-all seccomp filter installed in a disposable thread reads back as mode 2; the sha256 of the installed seccomp profile file (compare it with `seccomp_profile_digest` of the template). The WSL 5.15 test kernel reports ABI 1, so the tool refuses there.
- `guest-netsink`: the attestation `/run/tbound/net-sink.attest` (strict) names the current boot and the pinned rule digest; the installed rule file is the pinned one; `nft -j list table inet tbound_net_sink` is exactly the pinned rule (the checker the reset uses); a child that the kernel starts inside the net-sink cgroup connects to `127.0.33.1:8080`, times out on `127.0.33.1:8081` and `127.0.0.1:8080` (a refusal is not a drop and is refused), sends a UDP datagram, and the drop counter has risen by at least 3. The sink unit (NS-1) must be running.
- `guest-tests-*`: the test binary is the one built by `evq-capture` for `F` (`evq` compares its sha256 with the capture record), run with `-test.v -test.count=1`; exit 0, every required test `PASS` (a `SKIP` of a required test is refused: `TestRealCleanTeardownIsNoEffect`, `TestRealDetachedSleepSurvivingIsProc01`, `TestRealProcessMovedOutOfTheCellIsProc02`, `TestRealReparentedSurvivorIsProc01`, `TestRealResidualListenerIsProc03`; `TestLandlockConnectRightIsPort8080OnlyAndOnlyForNetSink`), no failure anywhere. Whether the `Real*` tests find a cgroup they may use on the guest is unmeasured; if they skip, nothing is written and `EVQ-ORACLE-PROCESS` stays not passed.
- `guest-provider-exchange`: the one response line of the governed client (`CLIENT /usr/local/bin/tbound serve --pi ... > /root/ga/out/response.txt`, as in L3) of a completed, successful run, `claim_bearing` false, `provider_exchange` true, tool sequence exactly `read, edit, bash, read`, generations `g0` to `g2` (three), a clean STOPPED settlement, and `profile_hash` and `frozen_source_digest` equal to the `run.json` pins. The L3 smoke task is read only and does not satisfy this; it needs an owner-approved G1 case on the engineering route. Until one exists the obligation stays not passed.

An artifact proves what its producer observed on the machine it ran on; it cannot prove that this machine is the pinned guest. The binding is to `F` (the commit the owner names), to the P1 `tbound-profile` digest (taken by the tool from its own executable) and, for test runs, to the test binary built from `F`.

Sequence (each is [READ-ONLY] on the guest side except `run` and `rerun`, which spend units):

```
tbound-run plan      --config run.json          # plan_digest, manifest_digest (== CORPUS_DIGEST), run_binding, units
tbound-run selfcheck --config run.json          # config, ssh binary, key files exist (never opened), pins, collectors, trial transport, every configuration B/C/P/V wired
tbound-run run       --config run.json --max-steps 1      # first unit, then inspect; then without --max-steps (resumable; an unfinished step is closed as abandoned, never re-run)
tbound-run rerun     --config run.json --step "<case|role|configuration|repeat>"      # only with outage evidence, at most one per unit, original kept
tbound-run export    --config run.json --out trials.json
tbound-run report    --config run.json --out <dir>      # report.json, report.md, report.meta.json; claim_bearing false
```

Per trial the runner calls `trial reset --check`, then `trial reset` (a failure spends the unit), then the governed client, then evidence collection, `trial accept` for compliant B and C cases, and `trial v-run` for V. The pilot report is non-claim-bearing (synthetic empty USD ledger, pilot quotas): no result of it is a thesis result.

## NOT NOW: research route (OpenRouter)

Only after the owner switches to the research route; none of it is part of the smoke or pilot; every RR step needs owner approval. Fixed paths (`internal/researchroute/route.go`): key `/etc/tbound/provider/openrouter.key`, profile `/etc/tbound/provider/research-profile.json`, ledger configuration `/etc/tbound/provider/research-ledger.json`, ledger `/var/lib/tbound/sessions/usd-ledger`. `provider_route` is fixed per service by the digest-pinned config.

- RR-1 [PRIVILEGED WRITE] frozen profile and ledger configuration, root:root 0644 in `/etc/tbound/provider` (`readOwnedFile` rules: regular, root-owned, not group/world-writable, non-empty; profile at most 64 KiB, ledger configuration at most 4 KiB). Members are those of `internal/researchprofile` (every member present and non-null, `temperature_milli` 0, both price ceilings, `input_framing_reserve_tokens` below `max_input_tokens`) and the ledger configuration `schema_version` 1, `ledger_id`, `cap_micro_usd`, `min_per_unit_worst_case_micro_usd`, `reserve_basis`. Values are owner decisions. Rollback: remove the two files.
- RR-2 [PRIVILEGED WRITE] OpenRouter key exactly like P12 at `/etc/tbound/provider/openrouter.key` (995:986 0400). Rollback: `sudo rm -f` it.
- RR-3 [PRIVILEGED WRITE] create the ledger once as the service uid: `SVC tbound-profile research-ledger-genesis` (refuses an unfrozen profile, an invalid configuration or an existing ledger). The owner stores `head_sha256` and `profile_digest` outside the ledger. No rollback after any research request was reserved; ledger rollback is not detected (head-hash anchor owner open).
- RR-4 [PRIVILEGED WRITE, OWNER SIGN S3] set `"provider_route": "research"` (pilot profile then also needs `allow_pilot_on_research: true`; the thesis profile must not have it), a new admission id for single-task modes, then R2-0, R2-2, S3 and S4 again. The research route refuses `workflow_max_host_calls` above the profile's `max_requests_per_unit`.
