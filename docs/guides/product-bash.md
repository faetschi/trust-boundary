# Enable Bash in the product service

Use this guide after [the first governed turn](product-first-turn.md) works with
Bash denied. It enables Bash through the signed D09 Podman/crun command cell.
The conversation still uses the supervisor loop, not Pi. Nothing here is thesis
evidence. The base image provides Bash and Node, not a Go development toolchain.

The demo VM passed qualification, Bash turns, review publication, scope refusal,
and predecessor replay on 2026-11-04 with build `d75ac188`. Later fixes for crashes
during Bash and publication await the next live run. See the
[installed contract](../product-contracts.md#installed-bash-unit-and-measurements)
for the fixed unit, environment, file checks, and evidence limits.

## Before you start

Use a qualified Linux amd64 product host with Landlock ABI 8, delegated cgroup
v2, rootless Podman/crun, and the existing signed scanner calibration. Keep the
service account and managed project from the Bash-deny install. The service
account must have no subordinate UID or GID allocation. It must differ from
the signed in-cell UID and GID.

Keep the existing registration private key on the offline owner machine.
Transfer public artifacts only. Use the same product registration key for the
command host profile. Do not rotate it across retained history.

The image needs its own cosign key pair. If you have none, create it once
**offline**, in an owner-only directory, and keep `cosign.key` there:

```sh
install -d -m 0700 /path/to/owner
COSIGN_PASSWORD= cosign generate-key-pair --output-key-prefix /path/to/owner/cosign
chmod 0600 /path/to/owner/cosign.key
```

`cosign generate-key-pair` prompts for a password unless `COSIGN_PASSWORD` is
set. An empty one suits a disposable demo only.

Replace uppercase measurement placeholders before running commands. Digests
include `sha256:`. Replace `/path/to/...` with absolute paths. Commands marked
**offline** run on the Linux build or owner machine, not on the service host.
Use trusted tools from the chosen frozen source commit. The build host needs a
local Go dependency cache and the digest-pinned base-image cache. Fill the base
cache once from the frozen repository root, because `build` fails on missing
blobs:

```sh
python3 -I packaging/governed/cell_image.py pull \
  --lock packaging/governed/base-images.lock.json --cache /path/to/base-cache
```

Create the private build output directories before running the recipes. Run each shell
procedure with `set -euo pipefail`; stop at a failed comparison or command.

Stop the service before replacing installed inputs. Preserve a stopped backup
of its configs, unit, both installed binaries, catalog, retained turn evidence,
and managed project before this change. Keep the original deny-only setup and
policy. Do not restore a catalog separately from its matching project and
retained evidence.

## 1. Build and sign the image offline

In a checkout frozen at the explicit release commit, build the tools twice with
the offline Go cache. For example, from `supervisor/`:

```sh
export GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOPROXY=file:///path/to/go/pkg/mod/cache/download GOSUMDB=off CGO_ENABLED=0
for tool in tbound tbound-profile tbound-run tbound-cell-entrypoint; do
  go build -trimpath -o /path/to/build/bin/$tool ./cmd/$tool
  go build -trimpath -o /path/to/build/bin/$tool.repeat ./cmd/$tool
  cmp /path/to/build/bin/$tool /path/to/build/bin/$tool.repeat
  sha256sum /path/to/build/bin/$tool
done
```

`cmp` must print nothing. From the frozen repository root, build the OCI layout
twice into new directories. Use the frozen commit time as `EPOCH`, the integer
printed by `git log -1 --format=%ct FROZEN_40_HEX_COMMIT`:

```sh
python3 -I packaging/governed/cell_image.py build \
  --lock packaging/governed/base-images.lock.json --cache /path/to/base-cache \
  --entrypoint /path/to/build/bin/tbound-cell-entrypoint --adapter adapter \
  --epoch EPOCH --out /path/to/build/oci
python3 -I packaging/governed/cell_image.py build \
  --lock packaging/governed/base-images.lock.json --cache /path/to/base-cache \
  --entrypoint /path/to/build/bin/tbound-cell-entrypoint --adapter adapter \
  --epoch EPOCH --out /path/to/build/oci-repeat
diff -r /path/to/build/oci /path/to/build/oci-repeat
python3 -I packaging/governed/cell_image.py measure --layout /path/to/build/oci
```

The diff must be empty. Record `image_manifest_digest`, `config_digest`,
`node_sha256`, `bash_sha256`, and `entrypoint_sha256`. Verify that the entrypoint
measurement equals the binary just built. Do not substitute a container tag or
the archive checksum for `image_manifest_digest`.

**Offline**, sign that exact layout and make the deterministic load archive:

```sh
python3 -I packaging/governed/sign_cell_image.py sign \
  --layout /path/to/build/oci --cosign /usr/local/bin/cosign \
  --key /path/to/owner/cosign.key --out /path/to/release/signed-layout
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu \
  -C /path/to/build/oci -cf /path/to/release/cell.tar oci-layout index.json blobs
python3 -I packaging/governed/sign_cell_image.py verify \
  --layout /path/to/release/signed-layout --cosign /usr/local/bin/cosign \
  --pub /path/to/owner/cosign.pub --expect IMAGE_MANIFEST_DIGEST
sha256sum /path/to/release/cell.tar
```

Verification prints `IMAGE_MANIFEST_DIGEST`. Record transfer checksums for the
archive and public files. Keep the original archive and signed layout. A Podman
re-export can rewrite the manifest and invalidate the four-way digest equality.
Do not pull an image on the service host.

## 2. Transfer public files and measure the host

Transfer the archive, signed layout, cosign public key, and new binaries over
the operator's authenticated transport. Stage public files below
`/etc/tbound-product/incoming/RELEASE/`. Every input and ancestor must be
root-owned, non-symlink, and not writable by group or others. Use 0755 directories
and 0644 public files. For example, from the build machine (`sudo -n` needs
passwordless sudo for the admin; otherwise run the `sudo` part on the host):

```sh
ssh admin@PRODUCT_HOST 'sudo -n install -d -o root -g root -m 0755 /etc/tbound-product/incoming/RELEASE'
ssh admin@PRODUCT_HOST 'sudo -n install -o root -g root -m 0644 /dev/stdin /etc/tbound-product/incoming/RELEASE/cell.tar' < /path/to/release/cell.tar
tar -C /path/to/release -c signed-layout | ssh admin@PRODUCT_HOST 'sudo -n tar -x --no-same-owner -C /etc/tbound-product/incoming/RELEASE'
```

Install the candidate separately from both active binaries on the product host:

```sh
sudo install -d -o root -g root -m 0755 /usr/local/libexec
sudo install -o root -g root -m 0755 /path/to/tbound /usr/local/libexec/tbound-candidate
sudo sha256sum /usr/local/libexec/tbound-candidate
sudo sha256sum /etc/tbound-product/incoming/RELEASE/cell.tar
```

Compare the checksums with the offline release. The pinned tools at
`/usr/bin/podman`, `/usr/bin/crun`, and `/usr/local/bin/cosign` must be regular
root-owned files. A cosign symlink fails even if it points at the correct bytes.
Replace a symlink with a regular copy, then measure, before signing the pins.
The cell's seccomp profile is the host Podman package's default,
`/usr/share/containers/seccomp.json`, and its digest is pinned too. Also record
the service account's numeric IDs:

```sh
if test -L /usr/local/bin/cosign; then
  sudo install -o root -g root -m 0755 "$(readlink -f /usr/local/bin/cosign)" /usr/local/bin/cosign.regular
  sudo mv -f /usr/local/bin/cosign.regular /usr/local/bin/cosign
fi
sudo sha256sum /usr/bin/podman /usr/bin/crun /usr/local/bin/cosign \
  /usr/share/containers/seccomp.json
id -u tbound-product; getent group tbound-product | cut -d: -f3
```

Write a new root-owned 0644 policy at
`/etc/tbound-product/policies/demo-bash.json`:

```json
{"version":"tbound-policy/v1","rules":[{"tool":"read","effect":"allow"},{"tool":"write","effect":"allow"},{"tool":"edit","effect":"allow"},{"tool":"bash","effect":"allow"}]}
```

Measure the existing project and this policy as the service user:

```sh
sudo setpriv --reuid=tbound-product --regid=tbound-product --clear-groups --no-new-privs -- \
  /usr/local/libexec/tbound-candidate product registration measure \
  --project-dir /var/lib/tbound-product/projects/demo \
  --gate-policy /etc/tbound-product/policies/demo-bash.json
```

Keep the printed `root=sha256:...` and `policy=sha256:...`. Transfer these public
measurements, the service UID/GID, the tool digests, a copy of
`/usr/share/containers/seccomp.json`, and the installed scanner calibration
`/etc/tbound-product/xattr-calibration.json` to the offline owner. Do not reuse another host's IDs or mount measurements.

## 3. Sign the registration and command bundle offline

Create a new registration with the measured root, policy, private Git digest,
and actual image manifest digest. Keep the allowed principal and service
identity appropriate to this installation. This example permits writes only
under `bash-output`, keeps `README.md` read-only, and protects `.git`:

```sh
tbound product registration init --out registration.template.json \
  --registration-id product-bash-1 --service-identity SERVICE_ID --principal CLIENT_UID \
  --provider opencode-go-engineering \
  --project 'id=demo,root=ROOT_DIGEST,policy=POLICY_DIGEST,modes=review+auto,writable=bash-output,read-only=README.md,protected=.git' \
  --max-turns 50 --review-timeout 600 \
  --turn-limit 50,200000,300 --session-limit 500,2000000,3600 \
  --git-digest GIT_DIGEST \
  --runtime-digest sha256:a0b7daad8b52356c44c8cc98e5cca6f1242b2012af1a59a6b3e09f5c55c977cf \
  --image-digest IMAGE_MANIFEST_DIGEST \
  --extension-digest sha256:a0b7daad8b52356c44c8cc98e5cca6f1242b2012af1a59a6b3e09f5c55c977cf
tbound product registration sign --registration registration.template.json \
  --key /path/to/owner/registration.key --out registration.json
tbound product registration verify --registration registration.json \
  --signature registration.json.sig --public-key /path/to/owner/registration.pub
```

Prepare the measured product `d09-cell.json`. The
[D09 template schema](../../supervisor/internal/piruntime/d09_template_linux.go)
describes the measured fields. The thesis [image recipe](../../packaging/governed/README.md)
and the `template-init` hint set `managed_live_root_registration_digest` and
install under `/etc/tbound/`. The product refuses both, so follow this list
instead. A `tbound-profile template-init` draft is unmeasured and is not an
installable product template. Before computing its
product digest, fill every measurement and set:

- `product_registration_digest` to the new signed registration's `digest`,
  `expected_image_digest` to `IMAGE_MANIFEST_DIGEST`, and
  `managed_live_root_registration_digest` to the empty string. Omit `route_probe`.
- `image` to `localhost/tbound/pi-cell:latest`, `oci_layout_path` to
  `/etc/tbound-product/podman/cell-oci`, `cosign_public_key_path` to
  `/etc/tbound-product/podman/cosign.pub`, and `seccomp_profile_path` to
  `/etc/tbound-product/podman/seccomp.json`.
- The measured tool paths/digests, image entrypoint and `/bin/bash` digests,
  service scanner IDs/calibration, and the one-own-ID mapping to the in-cell IDs.
  `command_executable` is `/bin/bash` with `bash_sha256` from `measure`, and
  `entrypoint_digest` is `entrypoint_sha256`. The image's `node` user makes
  `cell_uid` and `cell_gid` 1000. `seccomp_profile_digest` is the digest of the
  host's `seccomp.json`. `frozen_capabilities` is `0000000000000000`.
- The seven `xattr_*` fields, copied from the signed calibration:
  `visibility_profile_digest`, `authority`, `filesystem_magic`,
  `mount_options_digest`, `scan_uid`, `scan_gid`, and `scan_privilege_profile`
  become `xattr_visibility_profile_digest`, `xattr_visibility_authority`, and
  so on.
  Use `/sys/fs/cgroup/system.slice/tbound-product.service/cells` for
  `cgroups_path`. The cgroup-root digest below uses the hierarchy path without
  `/sys/fs/cgroup`.
- Explicit `cpus`, `pids_max`, and `memory_mb` limits. The live demo used
  `pids_max=256` and `memory_mb=1024`.
- Landlock ABI 8, read-only paths
  `/usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev`, and
  read-write paths `/work,/tmp`. Do not shorten the read-only list: the
  entrypoint needs `/proc/net/route` after applying Landlock.

Set `user_namespace_mapping_profile` to `rootless-single-own-uid-gid/v1`.
Derive `user_namespace_mapping_digest` from the four positive numeric IDs with
this recipe, matching `DigestD09UserNamespaceMapping`:

```sh
python3 -I - CELL_UID CELL_GID SERVICE_UID SERVICE_GID <<'PY'
import hashlib, sys
cell_uid, cell_gid, service_uid, service_gid = map(int, sys.argv[1:])
assert min(cell_uid, cell_gid, service_uid, service_gid) > 0
assert cell_uid != service_uid and cell_gid != service_gid
parts = [
    'tbound-d09-user-namespace-map/v1', 'rootless-single-own-uid-gid/v1',
    f'uid-inside={cell_uid}:0:1', f'gid-inside={cell_gid}:0:1',
    f'uid-host={cell_uid}:{service_uid}:1', f'gid-host={cell_gid}:{service_gid}:1',
    'parent=initial-userns/v1',
]
print('sha256:' + hashlib.sha256('\x00'.join(parts).encode('ascii')).hexdigest())
PY
```

`SERVICE_UID` and `SERVICE_GID` are the IDs recorded in section 2.

A refused template prints only `registered D09 cell template is missing or
invalid`, without the field, so fill it with a script. This one starts from
the draft and fills the fields above from the signed registration, the
transferred calibration and seccomp file, and your measurements. Replace the
uppercase values. `LANDLOCK_DIGEST` and `USERNS_DIGEST` come from the commands
around it:

```sh
tbound-profile template-init --out template-draft.json
python3 -I - <<'PY'
import hashlib, json, pathlib
def digest(path): return 'sha256:' + hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()
t = json.loads(pathlib.Path('template-draft.json').read_text())
r = json.loads(pathlib.Path('registration.json').read_text())
c = json.loads(pathlib.Path('xattr-calibration.json').read_text())
t.update(
    image='localhost/tbound/pi-cell:latest', expected_image_digest=r['image_digest'],
    product_registration_digest=r['digest'], managed_live_root_registration_digest='',
    cosign_public_key_path='/etc/tbound-product/podman/cosign.pub',
    cosign_public_key_digest=digest('/path/to/owner/cosign.pub'),
    oci_layout_path='/etc/tbound-product/podman/cell-oci',
    seccomp_profile_path='/etc/tbound-product/podman/seccomp.json',
    seccomp_profile_digest=digest('seccomp.json'),
    landlock_abi=8, landlock_ruleset_digest='LANDLOCK_DIGEST',
    landlock_ro_path='/usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev',
    landlock_rw_path='/work,/tmp', frozen_capabilities='0000000000000000',
    entrypoint_digest='ENTRYPOINT_SHA256',
    cgroups_path='/sys/fs/cgroup/system.slice/tbound-product.service/cells',
    cpus='1', pids_max=256, memory_mb=1024,
    podman_path='/usr/bin/podman', podman_digest='PODMAN_DIGEST',
    crun_path='/usr/bin/crun', crun_digest='CRUN_DIGEST',
    cosign_path='/usr/local/bin/cosign', cosign_digest='COSIGN_DIGEST',
    command_executable='/bin/bash', command_executable_digest='BASH_SHA256',
    cell_uid=1000, cell_gid=1000,
    xattr_visibility_profile_digest=c['visibility_profile_digest'],
    xattr_visibility_authority=c['authority'], xattr_filesystem_magic=c['filesystem_magic'],
    xattr_mount_options_digest=c['mount_options_digest'],
    xattr_scan_uid=c['scan_uid'], xattr_scan_gid=c['scan_gid'],
    xattr_scan_privilege_profile=c['scan_privilege_profile'],
    user_namespace_mapping_profile='rootless-single-own-uid-gid/v1',
    user_namespace_mapping_digest='USERNS_DIGEST')
t.pop('route_probe', None)
pathlib.Path('d09-cell.json').write_text(json.dumps(t, separators=(',', ':')))
PY
```

Derive the Landlock and cgroup pins with the existing commands:

```sh
tbound-profile landlock-digest \
  --read-only /usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev \
  --read-write /work,/tmp
tbound-profile cgroup-root-digest --uid SERVICE_UID \
  --cgroups-path /system.slice/tbound-product.service/cells
tbound-run source-digest --repo /path/to/frozen.git --commit FROZEN_40_HEX_COMMIT
tbound product image template-digest --template d09-cell.json
```

The Landlock digest is
`sha256:6c7bcda1461c17b7847cd124a0bba03ba4d7779bb3b07c2bc83805e7859a06af`.
Use `source_digest` for `SourceProvenanceDigest`, not the commit ID or tar checksum.
Use the printed product template digest for `ContainmentProfileDigest`:

```sh
tbound-profile init --out host-profile-unsigned.json --id product-command-1 \
  --node-digest NODE_DIGEST \
  --runtime-bundle-digest sha256:a0b7daad8b52356c44c8cc98e5cca6f1242b2012af1a59a6b3e09f5c55c977cf \
  --template-digest TEMPLATE_DIGEST --cgroup-root-digest CGROUP_ROOT_DIGEST \
  --source-digest SOURCE_DIGEST --cosign /usr/local/bin/cosign \
  --cosign-pub /path/to/owner/cosign.pub --provider-route engineering
```

The runtime sentinel means `unused by the supervisor-loop worker`. It grants
no Pi authority and leaves `unverified_inputs` unchanged. Node must still match
the signed image. The cosign executable and public-key bytes used here must
match the host pins.

`tbound-profile sign` takes a raw Ed25519 private key, whereas the product key
file is base64. On the offline owner machine only, decode it into an owner-only
temporary file, sign, verify the public key, and wrap the signature as base64.
In an owner-only signing directory:

```sh
umask 077
python3 -I - <<'PY'
import base64, os, pathlib
key = base64.b64decode(pathlib.Path('/path/to/owner/registration.key').read_bytes().strip(), validate=True)
assert len(key) == 64
fd = os.open('registration.raw', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'wb') as f:
    f.write(key)
PY
trap 'rm -f registration.raw' EXIT
tbound-profile sign --profile host-profile-unsigned.json --key registration.raw \
  --out-sig host-profile.ed25519 --out-pub host-profile.pub.raw --out-profile host-profile.json
tbound-profile verify --profile host-profile.json --sig host-profile.ed25519 \
  --pub host-profile.pub.raw \
  --runtime-bundle-digest sha256:a0b7daad8b52356c44c8cc98e5cca6f1242b2012af1a59a6b3e09f5c55c977cf
base64 --decode /path/to/owner/registration.pub | cmp - host-profile.pub.raw
base64 -w 0 host-profile.ed25519 > host-profile.json.sig
printf '\n' >> host-profile.json.sig
rm registration.raw
trap - EXIT
```

The key comparison must succeed. Transfer only `registration.json`, its `.sig`,
`registration.pub`, and the five bundle files: `host-profile.json`, its `.sig`,
`d09-cell.json`, `seccomp.json`, and `cosign.pub`. Never transfer
`registration.raw` or either private key. Setup reads the five bundle files by
these names from one root-protected directory. On the host, stage them and the
registration as root-owned files, from wherever you copied them:

```sh
sudo install -d -o root -g root -m 0755 /etc/tbound-product/incoming/RELEASE/command-bundle
sudo install -o root -g root -m 0644 host-profile.json host-profile.json.sig \
  d09-cell.json seccomp.json cosign.pub /etc/tbound-product/incoming/RELEASE/command-bundle/
sudo install -o root -g root -m 0644 registration.json registration.json.sig \
  registration.pub /etc/tbound-product/incoming/RELEASE/
```

## 4. Replace setup with format 3

Copy the original setup input to a new file. Point its registration fields at
the new public registration in `/etc/tbound-product/incoming/RELEASE/`, its `gate_policy_paths.demo` at the new policy,
and add `command_bundle`:

```json
"command_bundle": "/etc/tbound-product/incoming/RELEASE/command-bundle"
```

Keep the working project's path, Git pin, calibration, and worker mode. Stage
this as `/etc/tbound-product/incoming/RELEASE/setup-bash.json`. Run setup from
the candidate, not the old format-2 binary:

```sh
sudo systemctl stop tbound-product
sudo setpriv --reuid=tbound-product --regid=tbound-product --clear-groups --no-new-privs -- \
  /usr/local/libexec/tbound-candidate product turns
sudo /usr/local/libexec/tbound-candidate product setup \
  --config /etc/tbound-product/incoming/RELEASE/setup-bash.json --replace --dry-run
sudo /usr/local/libexec/tbound-candidate product setup \
  --config /etc/tbound-product/incoming/RELEASE/setup-bash.json --replace
```

Resolve unsettled turns before replacing the registration. Review the dry-run
paths first. The installed setup record, `/etc/tbound-product/setup.json`, must show
`"installation_format": 3`. The unit includes `DelegateSubgroup=runner` and one
`BindReadOnlyPaths=` line that lists the read-only paths.
Setup installs `/usr/local/lib/tbound-product/tbound`; update the terminal
client separately to the same bytes:

```sh
sudo install -o root -g root -m 0755 /usr/local/libexec/tbound-candidate /usr/local/bin/tbound.new
sudo mv -f /usr/local/bin/tbound.new /usr/local/bin/tbound
sudo sha256sum /usr/local/bin/tbound /usr/local/lib/tbound-product/tbound
```

The two checksums must match. Setup does not start the service.

## 5. Provision the signed image

Keep the service stopped. Run the dry run, then the real import:

```sh
sudo /usr/local/bin/tbound product image provision \
  --archive /etc/tbound-product/incoming/RELEASE/cell.tar \
  --signed-layout /etc/tbound-product/incoming/RELEASE/signed-layout --dry-run
sudo /usr/local/bin/tbound product image provision \
  --archive /etc/tbound-product/incoming/RELEASE/cell.tar \
  --signed-layout /etc/tbound-product/incoming/RELEASE/signed-layout
```

The success line is:

```text
provisioned sha256:...; registration == template == signed OCI manifest == imported Digest/RepoDigest; service remains stopped
```

Dry-run runs no cosign or Podman and establishes static validity only. Provision
never pulls or resets a store. If it refuses an incompatible nonempty store,
preserve the retained evidence and follow its explicit relocation instruction.
Do not erase it to retry.

Run provision again after every re-signed host profile, even when the image is
unchanged. It retains `/etc/tbound-product/podman/hosts/<profile-digest>/` for
history verification. Setup reminds you when replacing a profile; doctor
reports a missing retained snapshot as `FAIL`.

## 6. Start and wait for qualification

```sh
sudo systemctl start tbound-product
sudo journalctl -u tbound-product -f -o cat --no-pager
```

Wait for `product service listening`. The demo VM needed about three minutes.
A successful `systemctl start` alone does not prove readiness. Every start runs
all seven qualification phases before opening the socket. Inspect the retained
`/var/lib/tbound-product/turns/qualification/<run-id>/evidence.json` if startup
fails. Do not move a failed run aside to bypass ownership checks.

`Restart=on-failure` retries a failed start after five seconds. Stop the unit
before repairing inputs. Qualification needs service-side DNS and provider
TCP/443 positive controls; an outage fails closed before listening. IPv6 is
skipped when the service cannot reach an IPv6 destination.

After the listening line, stop following the journal with Ctrl+C and run:

```sh
sudo /usr/local/bin/tbound product doctor
```

Expect passing unit, socket, static command inputs, and retained snapshot
checks. The static Bash line says `configured, not qualified`. A retained
qualification `PASS` is advisory and is not a shortcut for the next start.
`governed readiness: NOT established` is expected without a configured self-test.
Only `doctor --self-test` runs that registered-client synthetic turn. Round 7
did not run it. Its incorrect missing-evidence warning was fixed after that run.

A refusal occurs before `product service listening`. Examples observed live:

```text
product host profile is not bound to the product cell template
product host profile does not verify under the fixed product key
protected input cannot be opened without following links: /etc/tbound-product/podman/d09-cell.json: no such file or directory
```

Restore verified signed inputs and provision again as needed. Do not bypass
signature, digest, or mount checks. The cell cannot write `/dev/null`; commands
that redirect there can fail. Changing entrypoint or image access to
`/dev/null` is deferred.

## 7. Run a Bash turn and publish it

As the registered client:

```sh
tbound chat --project demo --mode review
```

First ask for one call:

```text
Make exactly one tool call: bash with the command ls . Do not make parallel tool calls.
```

Expect `tool bash: ALLOW` and `no_diff`. Then ask:

```text
Make exactly one tool call: bash with the command mkdir -p bash-output && printf 'hello from bash\n' > bash-output/result.txt && cat bash-output/result.txt . Do not make parallel tool calls.
```

Review the candidate diff, then enter `/publish`. Expect `awaiting_review`
followed by `published`. The live file contains `hello from bash` and a newline.
Ask for `cat bash-output/result.txt` in a later turn to exercise predecessor
replay.

For the scope above, `echo outside-scope > notes-outside.txt` returns
`scope_path_not_covered`; `echo changed >> README.md` returns
`scope_protected_path`. Neither publishes. `/discard` rejects a review candidate.

When piping chat input, the review prompt consumes the next input line. Put
`/publish` or `/discard` immediately after the prompt that produces a candidate,
then `/quit`. Prefer interactive review for the first turn. A model can return
`multiple or malformed tool calls`; retry the prompt asking for exactly one
call after checking the prior outcome.

## 8. Recover a turn killed during Bash

A killed command without a verified settlement ends `UNKNOWN`. The project
stays blocked. Starting a new session cannot bypass that block. Let restart
reconciliation drain recorded command scopes and containers first. It does not
create a successful command receipt. If startup refuses cleanup, retain the
evidence and investigate the named prerequisite.

Once restart reconciliation has run, stop the service and list unsettled turns:

```sh
sudo systemctl stop tbound-product.service
sudo setpriv --reuid=tbound-product --regid=tbound-product --clear-groups --no-new-privs -- \
  tbound product turns
sudo tbound product turns resolve --turn TURN_ID \
  --reason "command killed; abandon inert turn after drain" --dry-run
sudo tbound product turns resolve --turn TURN_ID \
  --reason "command killed; abandon inert turn after drain"
sudo systemctl start tbound-product.service
```

Do not let another process write the project during resolution. It requires
the unchanged admitted baseline, protected retained evidence, and durable proof
that the exact Bash owner was drained, or that every command settled. Missing
evidence still refuses. Resolution charges the full reservation and records
terminal `abandoned`. It never makes this turn a predecessor. Keep the journals.

A kill during publication of a review candidate leaves the project blocked in
the same way. The same commands resolve it when the project still equals the
admitted baseline, which is the state after a kill before the first file was
applied. The real run closes the pending publication record and ends any
prepared transaction as failed before it abandons the turn. A kill right after the
staging directory was created ends the transaction failed too, but the directory
stays in place and the command prints its path, for you to remove. The dry run
names that and writes nothing. If the project holds exactly the candidate, the
publication applied and only its terminal record was lost. The same commands
then record the turn as `published`: they close the pending record as applied
through publication recovery (nothing is applied again), charge the full
reservation, and print the result baseline. The turn is not context-eligible.
A partial or mixed tree (`not provably unapplied`) refuses and changes nothing.
Inspect that project by hand.
These recovery fixes are code-backed and await the next live run.

## 9. Return to Bash denied

For an existing catalog, keep the current format-3 binary and restore the
deny-only policy. Resolve all unsettled turns first. Repeat section 3 with that
policy's measured digest and a new signed registration. Rebind and re-sign the
command template/profile to the new registration, keeping the image pin. A
deny-only service with retained command-owner journals still loads the command
host to reconcile them. Removing the bundle or keeping a template bound to the
old registration cannot bypass that check.

Prepare `setup-deny.json` with the deny-only policy and these new public
artifacts. Stop the service, replace setup, and provision the newly signed
profile with the same archive and layout:

```sh
sudo systemctl stop tbound-product
sudo tbound product setup --config /etc/tbound-product/incoming/RELEASE/setup-deny.json --replace --dry-run
sudo tbound product setup --config /etc/tbound-product/incoming/RELEASE/setup-deny.json --replace
sudo tbound product image provision \
  --archive /etc/tbound-product/incoming/RELEASE/cell.tar \
  --signed-layout /etc/tbound-product/incoming/RELEASE/signed-layout --dry-run
sudo tbound product image provision \
  --archive /etc/tbound-product/incoming/RELEASE/cell.tar \
  --signed-layout /etc/tbound-product/incoming/RELEASE/signed-layout
sudo systemctl start tbound-product
sudo tbound product doctor
```

Preserve command artifacts and retained history for earlier Bash turns. New
Bash requests return `policy_default_deny`.

To restore the exact former format-2 installation, use its stopped backup as a
matched installation and state set. Binary-only `product rollback` refuses a
format change. Older binaries also refuse catalogs containing the new
`command_owner_head` abandonment field. A format-2 registration cannot reopen
while a turn remains unresolved, and an older project snapshot cannot match a
newer catalog baseline.

Stop the service. Preserve the current format-3 installation, catalog, retained
turns, and managed project before restoring anything. List and validate the
backup members first. Relocate the product Podman config/home/storage directories
to a root-only archive directory instead of deleting them. Restore the matched
backup, then reload and check the service:

```sh
sudo systemctl daemon-reload
sudo systemctl start tbound-product
sudo journalctl -u tbound-product -n 20 --no-pager
sudo tbound product doctor
tbound chat --project demo --mode review
```

Expect `product service listening`, an allowed read, and Bash denied with
`policy_default_deny`. The demo round restored format 2 with catalog and project
state repairs. Those were disposable-VM state surgery, not a general recovery
procedure. Do not truncate a live catalog to make an older binary accept it.

### Rolling state back instead of restoring a whole backup

A format-2 binary cannot replay a catalog that holds format-3 records: it stops
with `CATALOG_CORRUPT`. That is why a restore to a Bash-deny format-2 install
must roll the state back. The catalog is not the only state. Each project keeps
its own publication audit, `/var/lib/tbound-product/turns/projects/<hash>/publication-audit.jsonl`
(the catalog is `/var/lib/tbound-product/catalog.jsonl`). `<hash>` is the lowercase
hex SHA-256 of the exact project ID bytes, with no prefix and no trailing newline:
`printf '%s' demo | sha256sum | cut -d' ' -f1` gives the directory name for project `demo`. If only the catalog is
cut back, the audit stays ahead of it. Its newer or UNKNOWN publication
transaction then fails every later admission with `live root has an unresolved
publication lifecycle record`. No `tbound` command rolls either file back, so
do it by hand, in this order:

1. `systemctl stop tbound-product`, and check that no process of the service
   account is left.
2. Copy the catalog, every project's `publication-audit.jsonl`, and the managed
   project files aside in full. Never erase the originals.
3. Choose the record boundary in the catalog (a byte offset just after a
   newline) and cut the catalog there: move the full file aside, then write its
   first N bytes back with the same owner and mode 0600.
4. Cut each project's audit to the matching boundary: the byte offset and
   record count the audit had when the catalog had the size you chose. Use only
   recorded offsets: the `wc -lc` of the catalog and of every audit, taken
   together from a backup made while the service was stopped. Turn membership
   cannot find the cutoff: a turn can exist at the catalog boundary while its
   publication begins after it, and cutting before that turn's pending record
   keeps its later pending or UNKNOWN records and recreates the admission
   quarantine. Without such a backup, do not guess; do not cut. Check that the
   prefix ends at a newline and holds whole records only.
5. Move aside every published file (and directory) that postdates the
   rolled-back baseline. The project must equal the baseline of the last
   catalog record kept.
6. `systemctl start tbound-product`, then `tbound product doctor`.

Round 9 did this with a catalog prefix and an audit cut to its first 664
records, with the full audit kept beside it.
