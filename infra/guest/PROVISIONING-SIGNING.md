# ABI8 guest: owner-approvable provisioning and signing sequence

Status: final draft for owner approval, 2026-10-17, checked against code at `e568d1f`. Based on the throwaway-key dry run (`F:\TBoundAssets\recovery-20261015\sign-dryrun-20261017\owner-sequence.sh.txt`) and the code fixes in `23a65ba`. Nothing here has been run on the guest except steps the dry run marked DRYRUN-OK. P3 to P7 were re-dry-run on the owner machine on 2026-10-17 (see B1 to B3). Code line references are given as symbol names; line numbers drift. This runbook supersedes `docs/governed-setup.md` section 225-236 for the ABI8 guest (that section is stale: it predates the single `tbound-pi` service identity, the ro-mounted executables, the split calibration roles and the signed host profile flow).

## Tags and rules

- `[READ-ONLY]` changes nothing on the guest or the owner machine except new private scratch files.
- `[PRIVILEGED WRITE]` changes the guest (root through `sudo -n` until Z1; owner console after). Each has a rollback line.
- `[OWNER SIGN]` uses a private key (`<ADMIN_KEY_PATH>` or `<COSIGN_KEY_PATH>`). An agent never reads, copies or logs a key or its passphrase.

1. Every `[PRIVILEGED WRITE]` and `[OWNER SIGN]` step needs explicit owner approval, one step at a time. Approval of one is not approval of the next.
2. Passwordless sudo is revoked at Z1, after provisioning and before the first cell launch, and is never restored. Nothing after Z1 uses sudo.
3. Part L (first launch) is a separate approval; it is not part of provisioning.
4. Signature output paths must be new absolute canonical paths; in-place signing is refused.
5. The frozen commit `F` (step F0) is the only source. Any change to a build input after F0 invalidates every binary, digest, image and signature below; start again from F0.
6. Owner rule: PROTOTYPING uses the engineering route (OpenCode Go, `provider_route: "engineering"`) only. Every research-route step (OpenRouter, USD ledger) is marked "NOT NOW - only after the owner switches to the research route" and must not be run before that decision. Corpus mode is not part of the smoke either (see "Later: corpus mode").

## Owner signing operations (7 key operations: 5 signatures, 2 public-key derivations)

| ID | Step | Key | Artifact |
| --- | --- | --- | --- |
| K1 | P4 | admin key `<ADMIN_KEY_PATH>` | read only: derive `<ADMIN_PUB_PATH>` (no signature) |
| K2 | P5 | cosign key `<COSIGN_KEY_PATH>` | read only: export `cosign.pub` (no signature) |
| S1 | R1-7 | admin key | read-only-source xattr calibration `d09-xattr-enumeration-calibration-readonly` |
| S2 | R1-7 | admin key | managed-live-root xattr calibration `d09-xattr-enumeration-calibration` |
| S3 | R2-4 | admin key | managed live root registration `managed-live-root` |
| S4 | R5-2 | admin key | host profile `pi-host-profile` |
| S5 | P6 | cosign key | cell image, by manifest digest `IMGDIGEST` (one `cosign sign-blob` call inside `sign_cell_image.py`) |

The admin key is the raw 64-byte Ed25519 key. S1 to S4 are domain-separated. The calibration signatures are v2 (schema `tbound-d09-xattr-enumeration-calibration/v2`, `authority` = `root-attested-bounded-no-hidden-trusted/v1`); old v1 calibrations and the retired complete-enumeration authority no longer verify. The managed-root registration schema is unchanged (`tbound-managed-live-root-registration/v1`).

The count is for the smoke. Any later change of the service config (route switch, corpus mode, new admission id) re-runs R2-0, R2-2 and S3 (one more signature each time); that is not counted above.

## First E2E smoke over OpenCode Go

Scope: engineering route, v3 `fresh` config (`infra/guest/serve-pi-service.smoke.json`, validated against `e568d1f`), task `smoke-read-readme`. Nothing below has run on the guest. "needs owner approval" marks every step that changes the guest, uses a key, or needs an owner decision; unmarked steps are [READ-ONLY]. Step text is in the parts below.

1. Commit this runbook and any pending `supervisor`/`infra` changes: needs owner approval (F0 requires a clean `supervisor adapter scripts packaging infra`).
2. F0 freeze `F`: needs owner approval (owner picks and records `F`).
3. P1 binaries, P2 runtime bundle, P3 cell image (each built twice, byte-identical).
4. P4 K1, P5 K2, P6 S5: [OWNER SIGN], each needs owner approval.
5. P7 verify the signed layout (and a wrong key fails).
6. P8, P9, P10, P11: [PRIVILEGED WRITE], each needs owner approval.
7. P12 OpenCode Go key at `/etc/tbound/provider/opencode-go.key`: [PRIVILEGED WRITE], needs owner approval (the owner types the key; no agent reads it).
8. R1-1, R1-3, R1-6 read-only; R1-2, R1-4, R1-5, R1-8: [PRIVILEGED WRITE], each needs owner approval; R1-7 S1 and S2: [OWNER SIGN], needs owner approval.
9. R2-0 (smoke config), R2-1, R2-3, R2-5: [PRIVILEGED WRITE], each needs owner approval; R2-2 read-only; R2-4 S3: [OWNER SIGN], needs owner approval.
10. R5-1 and R5-4 read-only; R5-2 S4: [OWNER SIGN], needs owner approval; R5-3: [PRIVILEGED WRITE], needs owner approval.
11. I1 load the image into the service store, Z0 remove scratch, Z1 revoke sudo: [PRIVILEGED WRITE], each needs owner approval.
12. Part L is a separate approval: needs owner approval. L0 seed `SMOKE-README.txt`, L1 start the service: [PRIVILEGED WRITE], each needs owner approval; L2 cgroup check read-only.
13. L3 positive launch (one governed run, `completed`, `success:true`, `provider_exchange:true`): [PRIVILEGED WRITE], needs owner approval. L4 drain and audit observation read-only.
14. Negative cases (L5): N5, N6 and N2 read-only (client side or refused at the socket); N1 (second request while one runs): needs owner approval; N7 (unknown task id, run last): needs owner approval. N3 and N4 are deferred (each needs a new admission id, hence R2-0, R2-2, S3 again).
15. If N7 (or any request) ends `unknown`: "Operator recovery" below, needs owner approval.

Not in this sequence: the research route (OpenRouter), corpus mode, any second admission id.

## Blockers (B1, B2 resolved in the dry run; B5, B6 resolved in code; B3 steps written but unproven; B4 documented; B7 open)

B1 to B3 were worked on 2026-10-17 (evidence: `F:\TBoundAssets\recovery-20261015\cell-image-20261017\`, dry run at HEAD `3b1b183`, throwaway keys, nothing written to the guest).

- B1 Network and mutability: RESOLVED (dry-run proven). Both base images are pinned by index digest in `packaging/governed/base-images.lock.json` and in the `Containerfile`. The node digest is the one the current signed image was built from: its five base layers (`diff_ids` `e5898914...` to `b6a146dc...`, read from the guest `localhost/tbound/pi-cell:latest`) are identical to the current layers of `node:24.21.0-bookworm-slim`, and the golang config digest `5d12712a...` equals the one in the old build log. The build no longer runs `npm install` and no longer needs a container runtime: `packaging/governed/cell_image.py` assembles the image offline as an OCI layout from the digest-pinned base layers (one-time pull, every blob verified) plus one deterministic uncompressed layer. Two builds gave byte-identical layouts and the same manifest digest. The one-time pull is the only network use; it is already done into `F:\...\cell-image-20261017\artifacts\base-cache`, so P3 can run fully offline. Decision to confirm: the in-image `/usr/local/lib/tbound/runtime` keeps `package.json`, the lock file and `src/` but no `node_modules`; nothing reads that path (the Pi runtime bundle is mounted at `/runtime` as fd9), so the old npm step only added a network dependency.
- B2 Signing with an owner key that never touches the guest: RESOLVED in dry run, with one supervisor change (B5, now committed). `sign_cell_image.py` runs on the owner machine. cosign v3.1.3 cannot sign a layout and always writes a Sigstore bundle, for which `verify --local-image` prints an empty `docker-manifest-digest` (the earlier ephemeral-key receipt shows exactly that); the launcher needs that claim. The helper therefore signs the classic simple-signing payload with one `cosign sign-blob --key` call and lets `cosign save` write the layout from a throwaway in-process loopback registry (stdlib Python, no `registry:2`, no image to pin, no network). Throwaway key A signs; the launcher's own `verifyOfflineSignature` (real exec runner, private netns, scratch test) accepts it; key B and a wrong pinned digest are refused.
- B5 Launcher cosign form: RESOLVED in code (`4a6856c`, owner decision: offline, no transparency log). With cosign v3.1.3 `cosign verify --offline --key ... --local-image ...` fails for every key signature without a Rekor entry (`offline verification failed`), so `verifyOfflineSignature` (`containment_linux.go`) runs `cosign verify --offline --insecure-ignore-tlog=true --key <pub> --local-image <layout>`. Trust rests on the pinned `cosign.pub` digest plus the pinned image digest `IMGDIGEST` (the verified payload is bound to both), not on a transparency log. The runner digest therefore includes this change; F0 must be at or after `4a6856c`.
- B3 The image must exist in the service's podman store: STEPS WRITTEN, STILL OPEN (unproven). `selectImage` (`containment_linux.go:1244`) needs `localhost/tbound/pi-cell:latest` in `/var/lib/tbound/podman-storage/graphroot` with a RepoDigest equal to `IMGDIGEST`; the directory is empty today (checked read-only 2026-10-17). I1 below now has exact load and check commands. No podman exists on the owner machine and nothing may run on the guest, so they have not been executed anywhere. Two things are unproven: that podman 4.9.3 keeps the manifest digest on `load` (the checks catch it if not), and that the `no-new-privs` service context can read a store that a normal-context `sudo -u` load created (check C4 catches it). Run I1 once, before Z1.
- B4 The client invocation and the drain observation: DOCUMENTED (2026-10-17, from code, unrun). See L3 to L5 and the symbol references there. The first launch is a minimal read-only smoke task, owner decision 2026-10-17: task `smoke-read-readme`, admission id `smoke-2026-10-17`, fixture budgets (4 host calls, 10000 tokens, 1200 s), template `infra/guest/serve-pi-service.smoke.json` (installed at R2-0). Provider calls use the engineering route (OpenCode Go) only (rule 6).
- B6 Provider credential route: RESOLVED in code (`46e4a92`), key still to be created (P12). The service reads the engineering key only from the fixed file `/etc/tbound/provider/opencode-go.key` (`D09ManagedPiProviderCredentialPath`; `serviceProviderConversationFactory` in `cmd/tbound/service_provider_credentials_linux.go`); the environment is ignored and the closed unit needs no change. File rules (`internal/broker/openrouter/credential_file_linux.go`): regular file, never a symlink, at most 4096 bytes, one printable token, owner = the service uid (995), mode 0400 or 0600 (any other bit refused). Path rule (`5cc0bfd`): the loader walks every ancestor from `/` without following symlinks; each (`/`, `/etc`, `/etc/tbound`, `/etc/tbound/provider`) must be a real directory owned by root and not group- or other-writable. The same rules apply to `/etc/tbound/provider/openrouter.key` (research route, NOT NOW). The key is read at the first conversation, not at service start, so a missing or insecure key shows up as a failed first provider call (fail closed; there is no fallback route).
- B7 The managed live root is empty (checked read-only 2026-10-17: only `lost+found`, plus the R1-5 baselines), so the smoke prompt has nothing to read. L0 seeds one fixed harmless file; it is unproven that the xattr enumeration and the source capture accept it (L0 has a census check).

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
CI              = F:\TBoundAssets\recovery-20261015\cell-image-20261017  (dry-run scripts, logs; tools\cosign-v3.1.3 = the repo-pinned cosign, sha256 4629c757..., copied read-only from the guest; artifacts\base-cache = pulled base layers)
COSIGN          = owner-side cosign v3.1.3 binary (a copy of the file `cosign-v3.1.3` in the CI tools directory, chmod 700; sha256 4629c757...). The launcher treats a binary named `cosign` specially, so keep that file name for launcher-path checks.
B               = owner-side image build dir, absolute, canonical, mode 0700 (unique name, removed at the end)
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

Record `tbound` (runner) and `tbound-profile` digests; the runner digest is bound by R2. `$W/bin/tbound-cell-entrypoint` is the offline reference for the in-image entrypoint (P3); the image now embeds this very binary (P3 builds the image from it), and a second `go build` in another directory must match it.

### P2 [READ-ONLY] rebuild the runtime bundle from F (LF)

Scripts and tooling are in `BR` (`10-setup.sh`, `20-bundle.sh`, `40-go-and-digest.sh`, `50-package.sh`, `99-cleanup.sh`); they run in WSL as jeli2k, offline, with Node v24.21.0 (sha256 `7fde7b8a...df4c`), a private copy of the npm cache and `scripts/prepare_official_pi_runtime.py`. They are pinned to 8c2a3cb: regenerate `inputs/head-src.tar` from `$F` with the LF archive command in P1 (do not use `head-src-autocrlf-default-config.tar`), and rename the output tar in `50-package.sh`/`51-verify-extract.sh` for `$F`. Build twice; the tars must be byte-identical; measure with the real `tbound-profile runtime-bundle-digest` under a read-only bind. Dirs 0555, files 0555/0444, deterministic tar (`--sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner`).

Record `BUNDLE_DIGEST` and the tar sha256. Cross-check only: at 8c2a3cb the LF build was `sha256:8e405ef0f2a3a3ed725dba2815d71bd8e5db007fe993f4ac3979ad59016d4f48`; no bundle input (`adapter/src`, `adapter/pi-runtime-package.json`, `scripts/prepare_official_pi_runtime.py`) differs between 8c2a3cb and 4a668e7, so the same value is expected at F if they still do not. A different value is not an error, but it is the value to pin. The currently installed bundle (`sha256:d7367e1b...6884`) predates the model pin and is replaced in P11.

### P3 [READ-ONLY] build the cell image from F on the owner machine (offline; B1 resolved)

Nothing is built or pulled on the guest. `packaging/governed/cell_image.py` (Python 3 stdlib, run with `python3 -I`) assembles the image as an OCI layout from the digest-pinned base layers, the P1 entrypoint, the F adapter sources and the mount-target directories (see B1 for the `node_modules` decision). It runs as the owner user in WSL, writes only `$B`, and needs the base-layer cache `CACHE`. Use the cache from the 2026-10-17 pull (`CI\artifacts\base-cache`, copied into a private dir) for a fully offline build. Only if that cache is lost, refill it once with `pull` (network; fetches only the digests in `base-images.lock.json`, verifies every blob, writes `pull-record.json`).

```
# one-time and only if the cache is missing (network, digest-pinned public images only)
python3 -I $W/src/packaging/governed/cell_image.py pull --lock $W/src/packaging/governed/base-images.lock.json --cache $CACHE
# build (offline); --epoch is the frozen commit time, so equal inputs give an equal manifest digest
python3 -I $W/src/packaging/governed/cell_image.py build --lock $W/src/packaging/governed/base-images.lock.json --cache $CACHE \
  --entrypoint $W/bin/tbound-cell-entrypoint --adapter $W/src/adapter --epoch $(git show -s --format=%ct $F) --out $B/oci
python3 -I $W/src/packaging/governed/cell_image.py measure --layout $B/oci
```

Build twice into two new dirs; `diff -r` of the two layouts must print nothing. Dry run at HEAD `3b1b183`: both builds gave manifest `sha256:177197a0...a7e1`, config `d32a43b8...6166`, one added layer `4bf81bb2...a3af`.

Record from `measure`:

- `IMGDIGEST` = `image_manifest_digest`. Signing does not change it and no registry is involved; it is the only image digest that is pinned.
- `ENTRYPOINT_DIGEST` = `entrypoint_sha256`. It must equal `sha256sum $W/bin/tbound-cell-entrypoint` from P1 (an independent `go build` in another directory). If they differ, stop. Dry run: both `sha256:dda92159...63e5`.
- `node_sha256` must equal the host Node `7fde7b8a...df4c` (`verifyImageWorkerExecutable`). Dry run: equal.
- `BASH_DIGEST` = `bash_sha256` (the file behind `/bin/bash`; `/bin` is a symlink to `usr/bin`). The base is now pinned by digest, so it is stable: `55b89ab2...efcf`, unchanged from the previous image.

The image digest is reproducible from (lock file, F, epoch). The layer is an uncompressed tar with fixed mtime/owner/mode, so it does not depend on a zlib version. `packaging/governed/Containerfile` documents the same image for a podman host but is not the build of record and does not give the same digest.

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

### P6 [OWNER SIGN] S5: sign the cell image, export the offline layout (B2 resolved, see B5)

Runs on the owner machine, never on the guest, offline. One key operation: `sign_cell_image.py` calls `COSIGN sign-blob --key <COSIGN_KEY_PATH>` once on the simple-signing payload for `IMGDIGEST`, then runs `COSIGN save` against its own throwaway loopback registry (127.0.0.1, random port, in memory, torn down on exit; its environment holds only `PATH`, `TERM`, a temp `HOME` and the passphrase variable). The passphrase is typed by the owner at cosign's prompt (or exported by the owner as `COSIGN_PASSWORD`); the helper never reads it. The output path must not exist.

```
python3 -I $W/src/packaging/governed/sign_cell_image.py sign --layout $B/oci --cosign $COSIGN --key <COSIGN_KEY_PATH> --out $H/pi-cell-oci
# loadable single-image archive for I1 (same blobs, same manifest, index.json without the signature entry)
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -C $B/oci -cf $H/pi-cell-load.tar oci-layout index.json blobs
sha256sum $H/pi-cell-load.tar        # record as LOAD_TAR_DIGEST (dry run: 54eb3b28...; byte-identical across two builds)
```

The signed layout keeps the unsigned image manifest and blobs unchanged and adds the signature manifest (`sha256-<IMGDIGEST hex>.sig`); its `index.json` lists the image and the signature, so `podman load` cannot read it directly (hence the separate archive).

### P7 [READ-ONLY] verify the signed layout, and show a wrong key fails

The launcher form (`verifyOfflineSignature`, `containment_linux.go:1314`), offline, run by the helper with the same payload rule as `parseCosignImageDigest`:

```
python3 -I $W/src/packaging/governed/sign_cell_image.py verify --layout $H/pi-cell-oci --cosign $COSIGN --pub $H/cosign.pub --expect $IMGDIGEST      # prints IMGDIGEST
python3 -I .../sign_cell_image.py verify --layout $H/pi-cell-oci --cosign $COSIGN --pub <any other public key> --expect $IMGDIGEST             # must fail: invalid signature
grep -c $IMGDIGEST $H/pi-cell-oci/index.json                                    # manifest digest present in the layout index
```

The same call as a bare command: `cosign verify --offline --insecure-ignore-tlog=true --key $H/cosign.pub --local-image $H/pi-cell-oci`. Without `--insecure-ignore-tlog=true` it fails for this signature (B5). There is no transparency log: trust is the pinned `cosign.pub` digest plus `IMGDIGEST`.

Dry run (throwaway keys, 2026-10-17, scripts `CI\40-sign-dryrun.sh` and `CI\42-launcher-verify.sh`): verify under key A printed `sha256:177197a0...a7e1`; under key B: `invalid signature when validating ASN.1 encoded signature`. The repo's own `verifyOfflineSignature` (real exec runner, private user and network namespace, scratch test file outside the repo) returned ok for key A with the right pin and refused key B and a wrong pin (`local OCI layout does not contain the frozen manifest`).

### P8 [PRIVILEGED WRITE] install the trust anchor before Row 1

Writes `/etc/tbound/pi-host-profile.pub` (root:root 0644) and the scratch dir.

```
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/pi-host-profile.pub' < <ADMIN_PUB_PATH>
GSSH 'sudo -n sha256sum /etc/tbound/pi-host-profile.pub'; sha256sum <ADMIN_PUB_PATH>        # [READ-ONLY] expect equal
GSSH 'sudo -n install -d -o 995 -g 986 -m 0700 /var/tmp/tbound-cal-20261017'
```

Rollback: `sudo rm /etc/tbound/pi-host-profile.pub; sudo rm -rf /var/tmp/tbound-cal-20261017`.

### P9 [PRIVILEGED WRITE] make the live root enumerable by uid 995

uid 995 cannot open `lost+found` (root 0700), so every 995 enumeration of the live root fails (xattr enumeration, `xattr_profile_linux.go`).

```
GSSH 'sudo -n chown 995:986 /var/lib/tbound/live/lost+found && sudo -n stat -c "%U:%G %a" /var/lib/tbound/live/lost+found'      # expect tbound-pi:tbound-pi 700
```

Rollback: `sudo chown root:root /var/lib/tbound/live/lost+found`.

### P10 [PRIVILEGED WRITE] executables and cell assets on read-only mounts

All fail closed through `requireReadOnlyMount` (`launcher_linux.go:1003`):

| Item | Pinned path | Checked at |
| --- | --- | --- |
| runner | `/usr/local/bin/tbound` | `d09_template_linux.go:269`; launch `managed_live_root_linux.go:1357` |
| podman | `/usr/bin/podman` | `containment_linux.go:995` -> `:2500` |
| crun | `/usr/bin/crun` | `containment_linux.go:1008` -> `:2500` |
| cosign | `/opt/tbound/cell/cosign` | `containment_linux.go:1022` -> `:2500` |
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
GSSH 'sudo -n /opt/tbound/cell/cosign verify --offline --insecure-ignore-tlog=true --key /opt/tbound/cell/cosign.pub --local-image /opt/tbound/cell/pi-cell-oci'        # [READ-ONLY] same as P7, now from the mount; docker-manifest-digest == IMGDIGEST
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

### P12 [PRIVILEGED WRITE] install the OpenCode Go key for the engineering route (B6)

Writes `/etc/tbound/provider` (root:root 0755) and `/etc/tbound/provider/opencode-go.key` (995:986 0400). The owner enters the key in an interactive guest shell (sudo shell before Z1, root console after); it is never passed on a command line, logged, or shown to an agent. Not digest-bound: no signature or measurement depends on it.

```
GSSH 'sudo -n install -d -o root -g root -m 0755 /etc/tbound/provider'
# owner, interactive guest shell as root:
umask 077; IFS= read -rs K; printf '%s' "$K" | install -o 995 -g 986 -m 0400 /dev/stdin /etc/tbound/provider/opencode-go.key; unset K
# [READ-ONLY] metadata only; never cat or hash the file
GSSH 'stat -c "%U:%G %a %F" /etc/tbound/provider/opencode-go.key'        # expect tbound-pi:tbound-pi 400 regular file
GSSH 'namei -mo /etc/tbound/provider/opencode-go.key'                    # every ancestor a directory, owner root, no group/other write bit, no symlink
```

Check this before L3: the one-use admission is reserved before the provider conversation is built, so a key problem found only at launch can burn the smoke admission id. Rollback: `sudo rm -f /etc/tbound/provider/opencode-go.key; sudo rmdir /etc/tbound/provider`.

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

### R2-0 [PRIVILEGED WRITE] install the smoke service config (before R2-2: the registration pins its digest)

`/etc/tbound/serve-pi-service.json` on the guest is still the 2026-10-15 fixture (sha256 `40db4c92...`, admission id `host-task-2026-10-14`, task `approved-task`). Replace it with `infra/guest/serve-pi-service.smoke.json`, whose two digests are `<MEASURE>` placeholders. The service refuses a config whose digest differs from the registration, so any later edit means R2-2 and S3 again. The service must be inactive.

```
# [READ-ONLY] measure the two Podman config digests (each is "sha256:" + the sha256sum of the installed file; 2026-10-17 values: containers 8627b6ed..., storage 9c571a18...)
CD=sha256:$(GSSH 'sha256sum /etc/tbound/podman-config/containers.conf' | cut -d' ' -f1)
SD=sha256:$(GSSH 'sha256sum /etc/tbound/podman-config/storage.conf' | cut -d' ' -f1)
# [READ-ONLY] fill the template on the owner machine (new file under $H), then check it
sed -e "s|\"containers_conf_digest\": \"<MEASURE>\"|\"containers_conf_digest\": \"$CD\"|" -e "s|\"storage_conf_digest\": \"<MEASURE>\"|\"storage_conf_digest\": \"$SD\"|" infra/guest/serve-pi-service.smoke.json > $H/serve-pi-service.json
! grep -q MEASURE $H/serve-pi-service.json && grep -c '"sha256:[0-9a-f]\{64\}"' $H/serve-pi-service.json        # expect 2
# keep the old file, then install (root:root 0644)
GSSH 'sudo -n cat /etc/tbound/serve-pi-service.json' > $H/serve-pi-service.json.prev        # [READ-ONLY] expect sha256 40db4c92...
GSSH 'sudo -n systemctl is-active tbound-pi.service'        # [READ-ONLY] expect inactive (tbound-pi.socket may stay active)
GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/serve-pi-service.json' < $H/serve-pi-service.json
GSSH 'sha256sum /etc/tbound/serve-pi-service.json'; sha256sum $H/serve-pi-service.json        # [READ-ONLY] expect equal
```

The template is schema `tbound-pi-service-config/v3`, `provider_route: "engineering"`, `workflow_admission_mode: "fresh"` (the v3 single-task schema stays valid for `fresh` and `resume`; v4 is needed only for corpus mode). Re-validated against code at `e568d1f` (scratch test on a HEAD archive): strict decode and `Validate` of `D09ManagedPiServiceConfig`, the `cmd/tbound` `piServiceConfig` strict decode with `validateProductionPiServiceConfig`, `validatePiServiceRequest` and `checkPiServiceClientRequest`, and the documented client request matches it under `workflowEnvelopeMatchesRequest` (all passed once the two `<MEASURE>` digests were filled; the committed test `TestSmokeServiceConfigTemplateStaysValidOnTheLegacySchema` in `internal/piruntime` covers the template on every test run). With the placeholders left in, validation refuses it. `provider_route` is required (an absent member is refused), is covered by the config digest that S3 pins, and cannot be changed by a client.

Rollback: `GSSH 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound/serve-pi-service.json' < $H/serve-pi-service.json.prev`.

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

### I1 [PRIVILEGED WRITE] load the signed image into the service podman store (B3: steps written, NOT yet run anywhere)

Required result, as the service identity: `podman image exists localhost/tbound/pi-cell:latest` succeeds, the image ID is the config digest and its `RepoDigests` contain `@IMGDIGEST`. No network. Must run after P8 (scratch dir) and before Z1 (it needs `sudo -u`), with the service stopped. `selectImage` (`containment_linux.go:1244`) is the consumer.

The image comes from `pi-cell-load.tar` (P6), the single-image archive of the very blobs that were signed; the signed multi-entry layout in `/opt/tbound/cell` is not loadable. The store is used exactly as the unit configures it: the unit's `Environment=` list, `/etc/tbound/podman-config/{containers,storage}.conf`, graphroot `/var/lib/tbound/podman-storage/graphroot`, runroot `/run/tbound-pi/podman-runroot`. The runroot, `TMPDIR` and `XDG_*` paths are the unit's real ones, because libpod records them in its state database and a different set would be reported as a mismatch at service start. `/run/tbound-pi` does not exist while the service is stopped (checked read-only 2026-10-17); it is created here and removed at the end (systemd recreates it at service start).

```
IMGDIGEST=sha256:<from P3>            CFG=<config digest hex from P3, without sha256:>            SCR=/var/tmp/tbound-cal-20261017
PODMAN_SVC='sudo -n -u tbound-pi env -C / -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/var/lib/tbound/podman-home USER=tbound-pi LOGNAME=tbound-pi XDG_RUNTIME_DIR=/run/tbound-pi XDG_CONFIG_HOME=/etc/tbound/podman-config XDG_DATA_HOME=/var/lib/tbound/podman-home/data XDG_CACHE_HOME=/run/tbound-pi/cache TMPDIR=/run/tbound-pi/tmp CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf LANG=C LC_ALL=C /usr/bin/podman --root /var/lib/tbound/podman-storage/graphroot'

# C0 [READ-ONLY] preconditions: service inactive, store empty, no podman state yet
GSSH 'systemctl is-active tbound-pi.service; sudo -n ls -A /var/lib/tbound/podman-storage/graphroot | wc -l; ls -d /run/tbound-pi'        # inactive / 0 / No such file

# 1 transfer and check the archive (the scratch dir is 995:986 0700 from P8)
GSSH 'sudo -n install -o 995 -g 986 -m 0600 /dev/stdin /var/tmp/tbound-cal-20261017/pi-cell-load.tar' < $H/pi-cell-load.tar
GSSH 'sudo -n sha256sum /var/tmp/tbound-cal-20261017/pi-cell-load.tar'        # == LOAD_TAR_DIGEST

# 2 runtime dir exactly as RuntimeDirectory=tbound-pi would make it
GSSH 'sudo -n install -d -o 995 -g 986 -m 0700 /run/tbound-pi /run/tbound-pi/tmp /run/tbound-pi/cache'

# 3 load, then name it. podman load of an OCI archive leaves the image unnamed; the tag is a local name only.
GSSH "$PODMAN_SVC load --input $SCR/pi-cell-load.tar"
GSSH "$PODMAN_SVC tag $CFG localhost/tbound/pi-cell:latest"

# 4 checks (all [READ-ONLY]; any mismatch means stop and run the rollback)
GSSH "$PODMAN_SVC image exists localhost/tbound/pi-cell:latest; echo exists=\$?"                       # exists=0
GSSH "$PODMAN_SVC image inspect localhost/tbound/pi-cell:latest --format '{{.Id}} {{.Digest}} {{.ManifestType}} {{range .RepoDigests}}{{.}} {{end}}'"
#   expect: <CFG> <IMGDIGEST> application/vnd.oci.image.manifest.v1+json localhost/tbound/pi-cell@<IMGDIGEST>
GSSH "$PODMAN_SVC images --no-trunc --format '{{.Repository}}:{{.Tag}} {{.ID}}'"                        # exactly one line, :latest
# C4 the same inspect in the service's own context (no-new-privs, zero capabilities); the load above ran without NNP so that newuidmap works
GSSH "sudo -n setpriv --reuid 995 --regid 986 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs -- ${PODMAN_SVC#sudo -n -u tbound-pi } image inspect localhost/tbound/pi-cell:latest --format '{{.Digest}}'"        # == IMGDIGEST

# 5 clean up the transient runtime state (graphroot keeps the image)
GSSH 'sudo -n rm -rf /run/tbound-pi'
```

`selectImage` accepts the image only if some `RepoDigests` entry ends in `@IMGDIGEST` (case-insensitive); the check in step 4 is that exact condition. Because the archive is the signed manifest unchanged, `Digest` should equal `IMGDIGEST` (unlike the 2026-10-15 pair `20d55339...` / `ac72b2d7...`, where `podman build` and `podman push` each wrote their own manifest). If podman rewrote the manifest, `Digest` differs: stop, do not re-sign; report it. If C4 fails but the normal-context inspect passes, the store is readable only with `newuidmap` (setuid, ineffective under `NoNewPrivileges=yes`): stop before Z1; that is a service-context design issue, not a signing issue.

Rollback: the graphroot was empty (995:986 0700) before this step; `sudo rm -rf /run/tbound-pi; sudo find /var/lib/tbound/podman-storage/graphroot -mindepth 1 -delete`.

### Z0 [PRIVILEGED WRITE] remove scratch

```
GSSH 'sudo -n rm -rf /var/tmp/tbound-cal-20261017'
```

Rollback: none needed; regenerable scratch (R1-3 and R2-2 outputs are already fetched to `$H`). Also remove `$W`, `$B` and the private cosign scratch on the owner machine. Nothing is built on the guest, so there is no guest build dir.

### Z1 [PRIVILEGED WRITE] revoke passwordless sudo (last sudo command, before the first cell launch)

The owner names the tboundadmin NOPASSWD drop-in under `/etc/sudoers.d/`; no agent reads other sudoers files.

```
GSSH 'sudo -n rm /etc/sudoers.d/<TBOUNDADMIN_NOPASSWD_DROPIN>'
GSSH 'sudo -n true'        # [READ-ONLY] must now fail ("a password is required")
```

Rollback: none, by rule. Passwordless sudo is never restored. Every later privileged change is an owner console session, followed by a re-measure and, where a bound digest changes, a re-sign.

## Part L: first launch (SEPARATE APPROVAL; owner console as root, no sudo)

Each `[PRIVILEGED WRITE]` here changes runtime state only; none changes a signed or measured artifact (R2-0 and the P12 key are the provisioning-time exceptions, done before this part). Abort on the first unexpected result. This part is the minimal read-only smoke task (owner decision 2026-10-17) on the engineering route; it does not start before P12 is done and checked, because the service has no provider credential until then.

Placeholders for this part: `CLIENT = setpriv --reuid 994 --regid 985 --clear-groups` (uid 994 `tbound-client`, primary gid 985 `tbound-pi-clients`; its login shell is nologin, so use `setpriv` as root, as the runbook does for uid 995), `RID = smoke-req-<UTC timestamp>` (a request id is consumed forever; never reuse one), `PROMPT` = the `workflow_prompt` string of the installed `/etc/tbound/serve-pi-service.json`, copied byte for byte.

### L0 [PRIVILEGED WRITE] seed the one fixture file the smoke task reads (service inactive; B7)

```
printf 'TBound smoke fixture. This file is harmless: it only checks that the read tool works.\n' | install -o 995 -g 986 -m 0600 /dev/stdin /var/lib/tbound/live/SMOKE-README.txt
python3 -I - /var/lib/tbound/live < xattr-census-r9.py | tail -1        # [READ-ONLY] expect dir 2, file 1, no xattr names
```

Rollback: `rm /var/lib/tbound/live/SMOKE-README.txt`. The file name must equal the one in the template prompt (`SMOKE-README.txt`).

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

One governed run of the fixed smoke workflow through the registered client. The client is `tbound serve --pi` without `--service` (`runServe` in `cmd/tbound/serve_linux.go`, then `runProductionPiServiceClient`); it loads the signed registration and the pinned service config, refuses a request whose shape does not fit the config mode (`checkPiServiceClientRequest`), dials the socket and prints the service response.

```
CLIENT /usr/local/bin/tbound serve --pi --task-id smoke-read-readme --task-request-id "$RID" --task "$PROMPT" --bash-no-outputs
echo "exit=$?"
```

- All three values must equal the installed config exactly: `--task-id` = `workflow_task_id`, `--task` = `workflow_prompt`, and `--bash-no-outputs` = `workflow_bash_output_paths: []`. Omitting the flag sends no scope (null), which does not equal `[]`. A `fresh` config takes no `--attempt` or `--role` (client refusal; service screen `attempt-not-accepted`). Any mismatch of the three is accepted and journaled, then refused by `ResolveAdmission` (`envelopeFor`, `workflowEnvelopeMatchesRequest`): it ends as `unknown` / `task-outcome-unknown` and consumes the request id (see N7).
- Socket `/run/tbound/serve-pi.sock` (`piServiceSocketPath`), root:`tbound-pi-clients`, mode 0660. The client's effective uid must be in the registered `AllowedClientUIDs` and its gid must equal `AllowedClientPrimaryGID` (994 and 985 here, as passed to `managed-root-measure` in R2-2; `piServicePeerAllowed`, checked client side first in `callProductionPiService`). The client blocks until the run ends, up to 20.5 minutes (read deadline `productionSessionMaxRuntime` 20 min + 30 s).
- Success: exactly one JSON line on stdout, `{"schema_version":"tbound-pi-service-response/v1","request_id":"<RID>","status":"completed","receipt":{...,"success":true,...}}`, and exit 0 (`callProductionPiService`; receipt fields in `productionPiReceipt`, `production_session_linux.go`). Otherwise the JSON line (when the service answered) is still printed, stderr gets `local Pi service <status>: <error_code>` and the exit is 2 (`main` in `cmd/tbound/main.go` exits 2 on any error); `completed` with `success:false` prints `local Pi service completed with a non-success production disposition`.
- Provider traffic goes to `https://opencode.ai/inference/go/v1/chat/completions`, model `deepseek-v4.1-flash`, both constants in `internal/broker/openrouter/openrouter.go:20-21`, with the key from `/etc/tbound/provider/opencode-go.key` only (P12). Expect `provider_exchange:true` in the receipt. If the service cannot load the credential it fails closed; do not retry with another route. Guest egress to `opencode.ai:443` has not been measured (the unit sets no IP restriction).

Rollback: stop the service (L1 rollback); the run's live-root output (none expected, the task is read-only) is removed only by an owner-supervised console action.

### L4 [READ-ONLY] drain and observation

During the run (second shell): `C=/sys/fs/cgroup/system.slice/tbound-pi.service/cells; ls $C; for d in $C/pi-wf-*; do cat $d/cgroup.events; done` shows `populated 1`. The scope name is `pi-wf-<first 24 hex of sha256(workflow_id)>-e<epoch>` for the service path (`workflowOwnerScopeName` in `production_workflow_catalog_linux.go`, used by the binder in `production_binder_linux.go`); `pi-<token>` is the non-service path and does not occur here.

After the client returns:

- `cells` holds no child scope: the scope directory is removed, so `cgroup.events` can only be read before removal (`populated 1` then gone, or `populated 0` if the directory is read in the window before `rmdir`). `$C/cgroup.procs` is empty; no container remains in the service store (`SVC podman ps -a` empty); no cell mount remains in `/proc/*/mountinfo`.
- Audit files, as root: `/var/lib/tbound/audit/<runID>-session-audit.jsonl`, `<runID>-provider-audit.jsonl`, `<runID>-command-audit.jsonl` (opened in `production_binder_linux.go`), where `runID = production-run-<token>` is in the catalog entry and in the receipt `run_id`; plus `/var/lib/tbound/audit/serve-pi-requests.jsonl` (`piServiceRequestJournalPath`), which holds `pi_service_request_accepted` and its terminal record for `RID`. The provider journal should show the exchanges to OpenCode Go and at most 4 host calls and 10000 tokens.
- Catalog `/var/lib/tbound/private-admin/workflow-recovery/catalog.jsonl`: expect records of kind `pi_workflow_admission_reserved`, `pi_workflow_fresh_authorized`, `pi_workflow_checkpoint_bound` / `pi_workflow_checkpoint_advanced`, `pi_workflow_owner_scope_bound`, `pi_workflow_owner_scope_drained` and `pi_workflow_terminal` (their exact order is not asserted here).
- A NORMAL completion does write `pi_workflow_owner_scope_drained`: `runProductionPiServe` defers `session.close()`, which runs the closer stack in reverse (`session.Close` in `production_binder_linux.go`); its `removeScope` removes the owner cgroup and then calls `MarkOwnerScopeDrained(..., "session_cleanup_complete")` when the scope was bound. It is skipped, and the cleanup retained, only if the lifecycle is UNKNOWN (`retainProductionCleanupIfUnsafe`) or `scope.Remove` fails. The other writers are the host drain verifier for superseded owners (`production_workflow_catalog_linux.go`, `production_recovery_binder_linux.go`) and the unexposed-binder-failure variant (`MarkOwnerUnexposedDrained`, `Unexposed:true`). Check `grep -c pi_workflow_owner_scope_drained catalog.jsonl` is 1 and that the event has `"unexposed"` absent or false.
- The service now holds a one-use admission: the same host admission id cannot start a second fresh workflow (`byHostAdmissionID` check in `ResolveAdmission`). A further run needs a new `workflow_admission_id`, which changes the config digest and means R2-0, R2-2 and S3 again.

### L5 [PRIVILEGED WRITE] negative cases (each must fail closed; restore afterwards)

Order matters: N5 and N6 are client-side refusals that touch no journal and may run any time. N7 leaves a terminal `unknown` request and every later request is then refused as `prior-workflow-unsettled` (`begin` in `pi_service_handoff_linux.go`) until the operator settles it (see "Operator recovery"), so run N7 last, after L3 and L4.

- N1: a second workflow while one is active is refused (`busy` / `one-task-at-a-time`).
- N2 [READ-ONLY]: a connection from a uid outside `tbound-pi-clients` is refused at the socket: `setpriv --reuid 1000 --regid 1000 --clear-groups python3 -c 'import socket; s=socket.socket(socket.AF_UNIX); s.connect("/run/tbound/serve-pi.sock")'` raises PermissionError (socket mode 0660, group 985). The real client never gets that far (N6).
- N3 and N4 are NOT part of the first smoke. Each needs a launch, and a launch spends the one-use admission (see L4), so each needs its own admission id (a new config: R2-0, R2-2, S3). N3: `systemctl stop opt-tbound-cell.mount`, then attempt a launch: it must be refused (cosign, key, seccomp and layout are no longer on a read-only mount); restore with `systemctl start opt-tbound-cell.mount` and re-run L2. N4: network egress from inside the cell fails (`network-none`); it needs a Bash command in the cell, which the smoke prompt forbids.
- N5 [READ-ONLY, client side] `--native-host-profile` from the client: `CLIENT /usr/local/bin/tbound serve --pi --native-host-profile /etc/tbound/pi-host-profile.json --task-id smoke-read-readme --task-request-id "$RID" --task "$PROMPT" --bash-no-outputs` exits 2 with stderr ``ordinary `serve --pi` is a local service client; profile/source/workspace are service-owned and cannot be selected by the client`` (`runServe`). It never dials the socket. `--workspace` and `--source` give the same message.
- N6 [READ-ONLY, client side] wrong uid: `setpriv --reuid 1000 --regid 985 --clear-groups /usr/local/bin/tbound serve --pi ...` (any uid other than 994, or any primary gid other than 985) exits 2 with stderr `local Pi service refused the request` (`ErrPiServiceRefused`). The client checks its own effective uid and gid against the registration before it dials, so there is no stdout JSON and nothing is journalled. A server-side refusal (`refused` / `peer-not-authorized`) is reachable only by a modified client.
- N7 [PRIVILEGED WRITE] unknown task id: same command with `--task-id smoke-unknown-task` and a fresh `RID7`: stdout line has `"status":"unknown","error_code":"task-outcome-unknown"`, exit 2, stderr `local Pi service unknown: task-outcome-unknown`. In a `fresh` config the service screen (`screenRequest`) does not check the task id, so the service accepts and journals the request before resolving it; the request id is consumed, replaying it returns the stored terminal answer, and the service refuses all later requests as `prior-workflow-unsettled`. No workflow starts: `ResolveAdmission` refuses the envelope mismatch. (In corpus mode an unknown case is refused as `unknown-case` before journaling and consumes nothing.) Recovery: "Operator recovery" below.

Rollback: restore every mount stopped in N3 and re-run L2; stop the service if any case was not refused.

## Operator recovery: `tbound abandon-unknown` [PRIVILEGED WRITE]

Use only when a request ended `unknown`, every later request is refused as `prior-workflow-unsettled`, and the owner has checked (L4 audit files and catalog) that its workflow is finished: owner scope drained, no pending effect or publication, no provider request without a recorded disposition. For N7 no workflow ever started. If any of that is unclear, leave the request quarantined; restoring a clean state is then an owner decision.

```
systemctl stop tbound-pi.service tbound-pi.socket        # the request journal is flock-held while the service runs
/usr/local/bin/tbound abandon-unknown --request-id "$RID7" --reason "N7 smoke negative: unknown task id, no workflow started"        # root only, no other arguments
systemctl start tbound-pi.socket
```

- Root only (`abandon-unknown is root-only`); it takes the service uid and gid from the signed registration and drops to them before it opens `/var/lib/tbound/audit/serve-pi-requests.jsonl`. `--reason` is 1 to 512 printable bytes and stays in the journal.
- It appends one `pi_service_request_abandoned` record (settlement `abandoned-unknown`, bound to the exact terminal by digest) and prints one JSON line (`settlement`, `request_id`, `journal_sequence`, `journal_hash`). Nothing is rewritten. It refuses unless the id matches exactly one journaled request whose terminal is `unknown` and not yet settled.
- It lifts only the one-prior-at-a-time block on later requests. It never retries, replays, resumes or reruns the request or its workflow, and it is not a claim that the workflow had no effect, completed, or hit an outage. The original request id still replays its stored `unknown` terminal. A rerun of a corpus case is a separate admission (attempt 2) that the runner allows only with its own outage evidence.
- Reconciliation: builds that carry the reconciliation change additionally bind host reconciliation evidence into the record and refuse, writing nothing, when the host cannot prove the workflow quiescent. That change was not committed at `e568d1f` (the command there appends the settlement without the check), and its exact preconditions are being tightened: re-read this step against the frozen `F` before relying on it, and do not treat the check as a substitute for the owner's L4 review.

Rollback: none; the record is append-only. A mistaken settlement is corrected only by an owner console decision, not by this command.

## NOT NOW - research route (OpenRouter) steps

Every step here: NOT NOW - only after the owner switches to the research route. None is part of the smoke or of PROTOTYPING. Fixed paths (`internal/researchroute/route.go`): key `/etc/tbound/provider/openrouter.key`, profile `/etc/tbound/provider/research-profile.json`, ledger configuration `/etc/tbound/provider/research-ledger.json`, ledger `/var/lib/tbound/sessions/usd-ledger`. The service reads the research key only from that file (environment ignored). `provider_route` is `engineering` or `research` in the service config; it is fixed per service by the digest-pinned config, so switching means RR-4.

- RR-1 [PRIVILEGED WRITE] NOT NOW - only after the owner switches to the research route: install the frozen profile and the ledger configuration, root:root 0644, in `/etc/tbound/provider` (root:root 0755). Each must be a non-empty regular file owned by root, not group- or world-writable, in a root-owned directory that is not group- or world-writable (`readOwnedFile`); profile at most 64 KiB, ledger configuration at most 4 KiB. The profile (`internal/researchprofile`) must be frozen, every member present and non-null: `schema_version`, `endpoint`, `model`, `allowed_response_models` (optional, default the model alone; the exact response-model strings the provider may report), `routing` (`order`, `allow_fallbacks`, `data_collection`, `quantizations`, `require_parameters`, `zdr`), `input_micro_usd_per_1m_tokens`, `output_micro_usd_per_1m_tokens`, the price ceilings `max_price_micro_usd_per_1m_tokens_in` and `max_price_micro_usd_per_1m_tokens_out` (both always required, also with a provider order; sent on every request as `provider.max_price`), `max_input_tokens`, `input_framing_reserve_tokens` (positive and below `max_input_tokens`), `max_output_tokens`, `max_requests_per_unit`, `temperature_milli` (must be 0), `seed`, `reasoning` (exactly one of `enabled` or `effort`), `timeout_ms`, `frozen_at`. A request whose serialized body plus the framing reserve exceeds `max_input_tokens` is refused before anything is reserved or sent. The ledger configuration has exactly `schema_version` (1), `ledger_id`, `cap_micro_usd`, `min_per_unit_worst_case_micro_usd`, `reserve_basis` (`request` or `unit`). The values are owner decisions. Rollback: remove the two files.
- RR-2 [PRIVILEGED WRITE] NOT NOW - only after the owner switches to the research route: install the OpenRouter key exactly like P12 but at `/etc/tbound/provider/openrouter.key` (995:986 0400; same ancestor rule: every directory from `/` root-owned and not group- or other-writable). The owner enters it; an agent never reads it. Rollback: `sudo rm -f /etc/tbound/provider/openrouter.key`.
- RR-3 [PRIVILEGED WRITE] NOT NOW - only after the owner switches to the research route: create the ledger once, as the service uid (`SVC`; after Z1 the owner runs the same `setpriv` line without `sudo -n`), so the service owns the 0600 ledger file. `/var/lib/tbound/sessions` must exist (995:986 0700, created by the unit's `StateDirectory`).

  ```
  GSSH 'SVC tbound-profile research-ledger-genesis'        # defaults: --profile, --ledger-config, --ledger = the fixed paths above
  ```

  It refuses an unfrozen profile, an invalid ledger configuration and an existing ledger, and creates nothing then. It prints one JSON line (`ledger_id`, `cap_micro_usd`, `records`, `head_sha256`, `profile_digest`, `ledger_path`); the owner stores `head_sha256` and `profile_digest` outside the ledger. The service never creates a ledger and refuses a missing one, so a deleted ledger cannot reset the budget. Rollback: none after any research request has been reserved (genesis is a one-time act; deleting the file and re-running it would reset the cap, an owner decision). Rollback of the ledger to an earlier valid state is not detected (head-hash anchor is an open owner decision, `ledgerOptions` in `route.go`).
- RR-4 [PRIVILEGED WRITE, OWNER SIGN S3] NOT NOW - only after the owner switches to the research route: set `"provider_route": "research"` in the service config, use a new `workflow_admission_id` for single-task modes, then repeat R2-0, R2-2, R2-4 (S3) and R2-5 (the config digest changed). At service start the research route refuses unless the profile is frozen, the key and ledger configuration are secure and the genesis ledger exists, and it refuses `workflow_max_host_calls` above the profile's `max_requests_per_unit`. Rollback: reinstall the previous config and registration.

## Later: corpus mode (NOT PART OF THE SMOKE; needs owner approval)

Corpus mode serves the runner's governed trials from a frozen manifest instead of one inline task. It is not needed for the first smoke, which stays in v3 `fresh` mode. On the research route every step below is also NOT NOW - only after the owner switches to the research route.

- CM-1 [PRIVILEGED WRITE] install `/etc/tbound/corpus/manifest.json`: regular file owned by root, not group- or world-writable, no setuid/setgid/sticky bit, non-empty, at most 16 MiB; every directory from `/` to it root-owned and not group- or other-writable (read with `openat`, no symlink followed; `ReadD09ManagedPiCorpusManifest`). The file must be readable by the service uid (0644). Its digest is `evalcorpus.Digest` of the manifest; no `tbound` or `tbound-profile` subcommand prints it at `e568d1f`, so the owner takes it from the corpus freeze tooling. Rollback: remove the file.
- CM-2 service config schema `tbound-pi-service-config/v4` with `workflow_admission_mode: "corpus"`, `corpus_manifest_path: "/etc/tbound/corpus/manifest.json"`, `corpus_manifest_digest` (CM-1), `workflow_admission_id`, `workflow_task_id` and `workflow_prompt` absent or empty, `workflow_bash_output_paths: []`, `workflow_max_*` as host ceilings that no manifest trial may exceed, `corpus_quota_profile` (`thesis`, the default when absent, or `pilot`) and `allow_pilot_on_research`. `pilot` is for pilot and G4 dry runs and its results are not thesis results; `allow_pilot_on_research` must be `true` exactly when the profile is `pilot` and `provider_route` is `research`, and is refused in every other combination. The manifest is validated at service start (digest equal to the config, `evalcorpus.Validate` under the chosen quotas); a failure refuses the service before it listens. A new config means R2-0, R2-2, S3 and R2-5 again.
- CM-3 client argv, one invocation per trial: `tbound serve --pi --task-id <case_id> --role <role> --attempt <1|2> --task-request-id tbr-<32hex> --bash-no-outputs`. There is no `--task` (refused client side and by the service). `--role` is one of `trajectory`, `static-replay`, `adaptive-replay` (adversarial cases), `variance-1`, `variance-2` (adversarial cases in the variance subset), `compliant` (compliant tasks), `ambiguous` (ambiguous cases). `--attempt 2` is the one outage rerun. The request id is `tbr-` plus 32 hex (`GovernedRequestFor`, `internal/runner/executor.go`, derived from plan digest, step and attempt, so a replay returns the stored terminal). The admission id is `<case_id>/<role>/<attempt>` and is one-use. A request the manifest cannot admit is refused before it is journaled and consumes nothing: `prompt-not-accepted`, `attempt-required`, `invalid-attempt`, `role-required`, `invalid-role`, `corpus-unavailable`, `unknown-case`, `role-kind-mismatch`, `invalid-request`, `admission-already-used`. The runner's guest transport is not wired (`ErrNotWired`).

## Evidence collection (NOT PART OF THE SMOKE; every guest write here needs owner approval)

The runner's `GovernedC` collects E04, PROC and the publication journal from the guest through the read-only subcommand `tbound evidence <kind>` (`supervisor/cmd/tbound/evidence_linux.go`), and NET on the host through a sink next to the runner. Nothing in this section has been run, and none of it may run before the owner approves it. Items marked [PRIVILEGED WRITE] change the guest; items marked [HOST] change only the owner machine.

What the subcommand is: it writes exactly one JSON document to stdout, writes nothing to disk, takes no lock, and refuses any unknown kind, flag or value and any effective uid but root (`evidenceRequiredUID = 0`), so the key that reaches it logs in as root. Kinds: `e04-snapshot --root /var/lib/tbound/live --point <e04 point>` (the live root is the only root it walks), `proc-cell`, `proc-capture --registration <digest> --point post_teardown|final_cleanup --interval-start <ns> --deadline <ns>`, `proc-monitor --registration <digest> --interval-start <ns> --deadline <ns> --duration <s> --interval <ms>`, `publication-interval-journal --interval-ref <ref>`, and `ssh-original-command`, the forced-command entry that reads `$SSH_ORIGINAL_COMMAND`, accepts only `/usr/local/bin/tbound evidence <kind> ...` in words of the alphabet `A-Za-z0-9._:/@#|+=,-` (plain or single-quoted), and never passes it to a shell. The PROC kinds resolve the cell from the signed managed-root registration and the cell template (cgroup path, inode, uid, gid), never from an argument, and refuse a registration digest that is not the one they compute themselves.

- EC-1 [PRIVILEGED WRITE, needs owner approval] a dedicated evidence key, separate from the `tboundadmin` key and from any client key. The owner generates it on the owner machine (`ssh-keygen -t ed25519 -f <EVKEY>`, no passphrase only if the owner accepts that for the unattended runner) and keeps the private half; an agent never reads it. On the guest, as root at the console, install the public half in `/root/.ssh/authorized_keys` with exactly these options in front of the key: `restrict,command="/usr/local/bin/tbound evidence ssh-original-command"`. `restrict` switches off pty, forwarding, agent and X11; `command=` makes the key run only the evidence entry whatever the client sends. Root key login then needs `PermitRootLogin forced-commands-only` (a drop-in `/etc/ssh/sshd_config.d/50-tbound-evidence.conf`, `PasswordAuthentication no` stays) followed by `systemctl reload ssh`. The alternative is a `Match User root` block with `ForceCommand /usr/local/bin/tbound evidence ssh-original-command`, `AuthenticationMethods publickey`, `PermitTTY no`, `AllowTcpForwarding no`, `X11Forwarding no`; it is stricter for the user but restricts every root ssh login, not just this key, so the per-key `command=` form is the default and the choice is the owner's. `/usr/local/bin/tbound` is the read-only bind mount of P10, so the key can run only the audited binary. Rollback: delete the key line and the drop-in, `systemctl reload ssh`.
- EC-2 [READ-ONLY, after EC-1] prove the restriction from the owner machine with the pinned host key (`GSSH`-style options, `-i <EVKEY>`, `-o IdentitiesOnly=yes`): `ssh ... root@<guest> id` must exit 2 with `SSH_ORIGINAL_COMMAND is not an evidence command` and print no `uid=`; `... '/usr/local/bin/tbound evidence e04-snapshot --root /etc --point baseline'` must be refused (root not registered); `... '/usr/local/bin/tbound evidence proc-cell'` must print one JSON line with the cell path `/system.slice/tbound-pi.service/cells`, a non-zero inode and the cell uid and gid. Also compare the guest clock: `date +%s%N` on the guest against the host within 1 s (PROC coverage and the teardown deadline use both clocks; skew shows as lost coverage, never as none).
- EC-3 [HOST] the evidence transport is a second `runner.SSHTransport` with its own `User` (`root`), its own `IdentityFile` (`<EVKEY>`), the same pinned `KnownHostsFile` and `HostKeyAlias`, and `ClientCommand: runner.DefaultEvidenceCommand()`. `GovernedC.Prepare` refuses collectors whose transport is not that command (the client launcher cannot be used as the evidence channel). Wiring: `GovernedC{Transport: clientTransport, Collectors: runner.CollectorSet{E04From: sshc, JournalFrom: sshc, ProcFrom: procCollector, NetFrom: netCollector}}` with `sshc := runner.NewSSHCollectors(evidenceTransport)`, `procCollector := &procoracle.SSHCollector{Client: runner.EvidenceClient{Transport: evidenceTransport}, CellUID: <cell uid>, CellGID: <cell gid>, MonitorDuration: <step bound>, TeardownGrace: <grace>}` and `netCollector := &netoracle.Collector{Sink: sink}`. The PROC monitor runs for `MonitorDuration` and returns its polls when that time is up, so `Proc` waits for it: choose a tight upper bound of one step.
- EC-4 [HOST, needs owner decision] the host sink address. The sink (`netoracle.NewSink`) runs on the owner machine next to the runner and must be reachable from the guest, so it listens on the host's Default Switch address (read-only on the host: `Get-NetIPAddress -InterfaceAlias 'vEthernet (Default Switch)' -AddressFamily IPv4`) and the corpus' registered sink URL is `http://<that address>:<port>`. Two caveats the owner must settle: the Default Switch subnet is chosen by Windows and can change at host restart, so a corpus that freezes the sink URL needs a stable address (an internal switch with a fixed host address is the stable alternative); and `docs/experiment-manifest.md` keeps the guest NIC on Default Switch for trusted maintenance only, with an offline gate before untrusted execution, while NET evidence needs the guest to be able to reach the host sink during the run. Reaching the sink while the guest is otherwise isolated is a network-policy change, not a runner detail. Without a reachable sink NET stays absent and coverage incomplete.
- EC-5 [PRIVILEGED WRITE, needs owner approval] the client key for `tbound-client` (recorded in `TASK_STATE.md`): the runner currently reaches the client as root through `setpriv --reuid 994 --regid 985 --clear-groups /usr/local/bin/tbound` (`runner.DefaultClientCommand`), and sudo is revoked at Z1. A least-privilege alternative is a dedicated ssh key with `restrict` and a forced command that runs only `tbound serve --pi` with validated arguments, which needs a login shell other than nologin for uid 994 or a `Match User tbound-client` block with `ForceCommand`. Until the owner approves it, the client transport needs the root login of EC-1's kind, which is a second key (never the evidence key: its forced command is the evidence entry).
