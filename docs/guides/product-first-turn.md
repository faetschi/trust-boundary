# Run a first governed product turn

Use this guide to install the TBound product service on one Linux host and run
a governed turn from the terminal and from the browser. The steps were run in
this order on a disposable Hyper-V VM (Ubuntu 24.04, Landlock ABI 8). For what
each command checks, see [product contracts](../product-contracts.md).

The product runs the `supervisor-loop` worker. It reads, writes and edits
project files through the gate. It has no contained runner, so a policy that
allows `bash` is refused and the model's bash calls are denied. Nothing here is
thesis evidence.

## Before you start

You need:

- A Linux amd64 host that passes `tbound product doctor`: Landlock ABI 8,
  cgroup v2 with `cgroup.kill`, rootless Podman and crun, an ext4
  `/var/lib`, and systemd. A Hyper-V VM made with
  [the reproduction bootstrap](../../infra/repro/README.md) qualifies.
- A `tbound` binary you trust, built from source (`make build`) or installed
  with `install.sh` under the publisher key. Setup installs the binary that
  runs it.
- A checkout of this repository on the host, for the census helper
  `infra/guest/helpers/xattr-census-r9.py`.
- An OpenCode Go API key. You type it into a hidden prompt; it is never an
  argument or a file you keep.
- An administrator machine that holds the registration key. The steps marked
  **offline** run there.

> [!WARNING]
> The registration private key must never be on the product host. On a
> disposable demo VM you can run the offline steps on the host itself. Delete
> the VM afterwards.

## Why this order

The signed registration pins digests that only the host can measure: the
project root's directory identity and the gate policy. The signed calibration
pins the scanner identity under the service's own sandbox. Setup refuses to
run until both are signed and in place. So the service account and the
project come first, setup comes last.

```mermaid
flowchart LR
  A[Account and dirs] --> B[Import project]
  B --> C[Measure, sign registration]
  C --> D[Measure, sign calibration]
  D --> E[product setup]
  E --> F[Provider key]
  F --> G[Start, doctor, chat]
```

## 1. Create the service account and directories

Run as root. Setup later finds these and reports them unchanged.

```sh
groupadd --system tbound-product
useradd --system --gid tbound-product --home-dir /var/lib/tbound-product \
  --no-create-home --shell /usr/sbin/nologin tbound-product
install -d -o tbound-product -g tbound-product -m 0700 \
  /var/lib/tbound-product /var/lib/tbound-product/projects
install -d -o root -g root -m 0755 /etc/tbound-product \
  /etc/tbound-product/policies /usr/local/lib/tbound-product
```

## 2. Install the binary, Git and the gate policy

```sh
install -o root -g root -m 0755 /path/to/tbound /usr/local/bin/tbound
install -o root -g root -m 0755 /usr/bin/git /usr/local/lib/tbound-product/git
sha256sum /usr/local/lib/tbound-product/git
```

Keep the Git digest for step 4. Write the policy. It must not allow `bash`:

```sh
cat > /etc/tbound-product/policies/demo.json <<'JSON'
{"version":"tbound-policy/v1","rules":[{"tool":"read","effect":"allow"},{"tool":"write","effect":"allow"},{"tool":"edit","effect":"allow"}]}
JSON
chmod 0644 /etc/tbound-product/policies/demo.json
```

## 3. Import the project

Only `tbound project import` creates a project the service accepts. It makes
the empty `lost+found` publication fence that the service requires. A
directory you create by hand fails at service start with `publication fence:
no such file or directory`.

As your admin user, bundle a source tree:

```sh
tbound project bundle --source ~/demo-source --out ~/demo.bundle
```

As root, hand the bundle to the service user and import it:

```sh
install -o tbound-product -g tbound-product -m 0600 ~admin/demo.bundle /tmp/demo.bundle
setpriv --reuid=tbound-product --regid=tbound-product --clear-groups --no-new-privs -- \
  tbound project import --bundle /tmp/demo.bundle --dest /var/lib/tbound-product/projects/demo
rm /tmp/demo.bundle
```

## 4. Measure and sign the registration

On the host, as the service user, print the two digests the service checks:

```sh
setpriv --reuid=tbound-product --regid=tbound-product --clear-groups --no-new-privs -- \
  tbound product registration measure \
  --project-dir /var/lib/tbound-product/projects/demo \
  --gate-policy /etc/tbound-product/policies/demo.json
```

It prints `root=sha256:…` and `policy=sha256:…`. Measure again whenever the
project directory is recreated or the policy changes; either change needs a new
signed registration.

**Offline**, create the key pair once, then write and sign the registration.
Replace `ROOT`, `POLICY` and `GIT` with the measured digests and `1000` with
the UID of the user who will chat:

```sh
tbound product registration keygen --out ~/tbound-admin
tbound product registration init --out ~/tbound-admin/registration.template.json \
  --registration-id demo-1 --service-identity tbound-product-demo --principal 1000 \
  --provider opencode-go-engineering \
  --project "id=demo,root=ROOT,policy=POLICY,modes=review+auto" \
  --max-turns 50 --review-timeout 600 \
  --turn-limit 50,200000,300 --session-limit 500,2000000,3600 \
  --git-digest sha256:GIT \
  --runtime-digest sha256:UNUSED --image-digest sha256:UNUSED --extension-digest sha256:UNUSED
tbound product registration sign --registration ~/tbound-admin/registration.template.json \
  --key ~/tbound-admin/registration.key --out ~/tbound-admin/registration.json
```

The `supervisor-loop` worker runs no Pi runtime, image or extension, but
`init` requires the three digests. Any valid `sha256:` value works; the demo
used the SHA-256 of the text `unused by the supervisor-loop worker`.

Copy `registration.json`, `registration.json.sig` and `registration.pub` to the
host. Do not copy `registration.key`.

## 5. Measure and sign the xattr calibration

The calibration pins the mount options the scanner sees. The service unit sets
`ProtectSystem=strict`, which changes them, so measure inside the same sandbox.
A calibration measured outside it fails at service start with `filesystem,
mount or scanner … differs from the signed xattr calibration`.

As root, from the repository checkout, place a probe corpus with one attribute
and take its census. The census digest replaces `CENSUS` below:

```sh
install -d -o tbound-product -g tbound-product -m 0700 /var/lib/tbound-product/calibration-probe
setpriv --reuid=tbound-product --regid=tbound-product --clear-groups -- \
  sh -c 'printf "probe\n" > /var/lib/tbound-product/calibration-probe/entry'
python3 -I -c "import os; os.setxattr('/var/lib/tbound-product/calibration-probe/entry', 'user.tbound.calibration-probe', b'demo')"
python3 -I infra/guest/helpers/xattr-census-r9.py /var/lib/tbound-product/calibration-probe | sha256sum
```

Write a temporary service config that names the project, then measure under the
unit's sandbox:

```sh
cat > /var/lib/tbound-product/calibration-measure.json <<'JSON'
{"schema_version":"tbound-product-service-config/v1","state_root":"/var/lib/tbound-product","projects":{"demo":"/var/lib/tbound-product/projects/demo"}}
JSON
chmod 0644 /var/lib/tbound-product/calibration-measure.json
systemd-run --wait --collect --pipe --uid=tbound-product --gid=tbound-product \
  -p NoNewPrivileges=yes -p ProtectSystem=strict -p ProtectHome=yes -p PrivateTmp=yes \
  -p UMask=0077 -p ReadWritePaths=/var/lib/tbound-product \
  /usr/local/bin/tbound product calibration measure \
  --source /var/lib/tbound-product/calibration-probe --probe-corpus-digest sha256:CENSUS \
  --config /var/lib/tbound-product/calibration-measure.json \
  --out /var/lib/tbound-product/calibration-unsigned.json
```

Copy `calibration-unsigned.json` off the host and sign it **offline**:

```sh
tbound product calibration sign --key ~/tbound-admin/registration.key \
  --public-key ~/tbound-admin/registration.pub \
  --in calibration-unsigned.json --out calibration.json
```

Back on the host, install the pair, remove the probe and the temporary files,
and confirm the census no longer sees the probe:

```sh
install -o root -g root -m 0644 calibration.json /etc/tbound-product/xattr-calibration.json
install -o root -g root -m 0644 calibration.json.sig /etc/tbound-product/xattr-calibration.json.sig
rm /var/lib/tbound-product/calibration-measure.json /var/lib/tbound-product/calibration-unsigned.json
rm -r /var/lib/tbound-product/calibration-probe
python3 -I infra/guest/helpers/xattr-census-r9.py /var/lib/tbound-product | grep calibration-probe
```

The last command must print nothing.

## 6. Run setup

Write the setup config. Paths to the registration files are where you copied
them on the host:

```json
{"schema_version":"tbound-product-setup-config/v1",
 "registration":"/home/admin/tbound-admin/registration.json",
 "registration_signature":"/home/admin/tbound-admin/registration.json.sig",
 "registration_public_key":"/home/admin/tbound-admin/registration.pub",
 "worker_mode":"supervisor-loop",
 "projects":{"demo":"/var/lib/tbound-product/projects/demo"},
 "gate_policy_paths":{"demo":"/etc/tbound-product/policies/demo.json"},
 "git_path":"/usr/local/lib/tbound-product/git",
 "git_digest":"sha256:GIT",
 "xattr_calibration_path":"/etc/tbound-product/xattr-calibration.json"}
```

Run it from the installed binary, first as a dry run:

```sh
sudo /usr/local/bin/tbound product setup --config setup.json --dry-run
sudo /usr/local/bin/tbound product setup --config setup.json
sudo usermod -aG tbound-product "$USER"
```

Add `--replace` when you rerun setup with a newly signed registration. The
group membership lets your user reach the service socket; log in again for it
to apply.

## 7. Add the provider key

```sh
sudo /usr/local/bin/tbound provider setup --profile opencode-go-engineering \
  --service-user tbound-product
```

Type the key at the hidden prompt. The command prints a fingerprint, never the
key. The service refuses to start without a credential.

## 8. Start the service and check it

```sh
sudo systemctl start tbound-product
sudo /usr/local/bin/tbound product doctor
```

Every check should pass. Without `release_public_key` in the setup config,
doctor warns that no release key is pinned; that is expected here and blocks
only `product update`.

## 9. Chat in the terminal

```sh
tbound chat --project demo --mode review
```

Type a prompt and press Enter. In review mode a turn that changes files stops
for you: the chat shows the diff, and `/publish` or `/discard` decides it.
`/status` shows the session and `/quit` detaches; the session keeps running.

A good first check is three prompts: read `README.md` (allowed), create a file
(held for review, then published), and run `ls` with bash (denied with
`policy_default_deny`).

## 10. Chat in the browser

Build the gateway and run it in a terminal you keep open. Do not redirect its
output to a file, because the pairing link is a credential:

```sh
cd extensions/webchat
go build -o ~/bin/tbound-webchat ./cmd/tbound-webchat
~/bin/tbound-webchat --live --socket /run/tbound-product/product.sock
```

It listens on `127.0.0.1:8791` only and prints a pairing link,
`http://127.0.0.1:8791/pair#TOKEN`. The link works once, within 10 minutes;
restart the gateway to pair again.

When the host is a VM, forward the port from your workstation and open the link
there:

```sh
ssh -N -L 127.0.0.1:8791:127.0.0.1:8791 admin@GUEST_HOST
```

From a Windows workstation, one Git Bash command starts the gateway on the VM
and forwards the port. Ctrl+click the printed link; Ctrl+C stops both:

```sh
ssh -t -L 127.0.0.1:8791:127.0.0.1:8791 admin@GUEST_HOST \
  '~/bin/tbound-webchat --live --socket /run/tbound-product/product.sock'
```

Create a session with project `demo` and profile `opencode-go-engineering`,
or click an existing session in the left panel; the chat stays empty until
one is selected. Then try the same three prompts. Publish and Discard appear
under the diff. A session whose earlier turn ended `unknown` is marked
`blocked` and refuses new prompts; start a new session.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `PROJECT_BLOCKED` / `PREDECESSOR_UNRESOLVED` after an unknown turn | Restart could not establish the outcome | Stop the service (`sudo systemctl stop tbound-product.service`), find the ID with `sudo -u tbound-product tbound product turns`, run `sudo tbound product turns resolve --turn ID --reason "provider failed; abandon inert turn" --dry-run`, then the same command without `--dry-run`, and start it (`sudo systemctl start tbound-product.service`). Resolution requires the unchanged admitted baseline, inert Store/workflow, no command activity and no publication record; it charges the full turn reservation and excludes its context. If any evidence check refuses, retain state for investigation. |
| Setup or doctor fails `cgroup-v2` on a host with cgroup v2 | Older builds looked for `cgroup.kill` at the cgroup root, where the kernel never creates it | Use a current build |
| Service exits with `publication fence: no such file or directory` | The project was not made by `project import` | Remove it, import it (step 3), measure and sign again (step 4) |
| Service exits with `… differs from the signed xattr calibration` | The calibration was measured outside the unit's sandbox | Measure again with `systemd-run` (step 5) and sign again |
| Setup or the service refuses a policy or project digest | The policy or project directory changed after measuring | Measure again, sign a new registration, then `setup --replace` |
| SSH to the VM times out after a host restart | The Hyper-V Default Switch gave the VM a new address | Look up the VM's MAC with `Get-NetNeighbor -LinkLayerAddress` and keep the host-key alias |
| The pairing page says the token is invalid | It was used or is older than 10 minutes | Restart `tbound-webchat` |
