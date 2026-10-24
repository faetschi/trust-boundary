# Set up TBound and Pi

Use this guide to install TBound and try its development fixtures. For a
governed service and the thesis evaluation, continue with
[reproduction](reproduce.md). Commands below use Bash inside Linux. Replace
`/absolute/path/to/tbound` with your checkout path. Run commands as your own
unprivileged user unless a step explicitly uses `sudo`.

TBound currently runs one fixed, signed task per service configuration. Corpus
mode selects tasks from a signed, frozen manifest. There is no interactive Pi
chat mode yet. The [web chat](../extensions/webchat/README.md) is frozen and
outside the thesis.

## Choose a host

Use Linux amd64 for governed runs. Native Linux and a Linux VM on Windows use
the same service and signing workflow. Native Windows and macOS are not
governed runtime platforms. Linux arm64 is not supported by the cell entrypoint.
WSL can build and test the code, but is not a qualified evaluation host.

The governed host needs:

- A running kernel that reports **exactly Landlock ABI 8**, including TSYNC.
  A newer kernel version alone does not prove this requirement.
- A unified cgroup v2 hierarchy, with delegated `cpu`, `memory`, and `pids`
  controllers, `cgroup.kill`, and observable teardown.
- systemd, rootless Podman with crun, and an offline Cosign verifier.
  Record and pin their exact versions and executable digests.
- An ext4 managed live root, protected runtime files on read-only mounts, and
  a dedicated non-login service identity. The trial login and service identity
  have different privileges. See [governed host setup](governed-setup.md).
- Git, Bash, Python 3, Make, archive and checksum tools, and GCC with cgo for
  the race tests.

The reproducibility pins are Go **1.27.1**, Node.js **24.21.0**, and Pi CLI
**0.87.1**. Pi's Node minimum is 22.19. Use the pinned versions when reproducing
the evaluation. The [guest assets](../infra/guest/README.md) install the Go and
Node toolchains and prepare dependencies. They target Ubuntu 24.04 amd64.

### Native Linux

On a dedicated Ubuntu 24.04 amd64 host, review the
[bootstrap procedure](../infra/repro/README.md) before running it. Replace
`TRIAL_USER` with the non-root trial login:

```sh
sudo bash infra/repro/bootstrap-linux.sh TRIAL_USER
```

The script can change packages, subordinate ID mappings, and the system-wide
AppArmor user-namespace restriction after confirmation. Review that change for
your host. Bootstrap does not provision the governed service or qualify it.

### Windows with a Linux VM

Create a dedicated Linux amd64 VM. Follow the
[Hyper-V setup guide](../infra/hyperv/SETUP.md) for Hyper-V, or provide the same
Linux prerequisites with your hypervisor. Keep personal repositories, host
shares, credentials, and redirected devices outside the guest. Prepare and
test a clean snapshot and restore procedure before trials.

Run installation, fixtures, and service commands inside Linux. Build scratch
directories must be on a Linux filesystem with real ownership and modes. Do
not use a Windows shared mount for runtime state. If you use SSH, obtain the
guest address and verify its host key through a trusted channel first. Replace
the uppercase values below with your own connection details:

```sh
ssh -o StrictHostKeyChecking=yes -o HostKeyAlias=GUEST_ALIAS \
  -o UserKnownHostsFile=/absolute/path/to/known_hosts TRIAL_USER@GUEST_HOST
```

Keep the host-key alias stable if DHCP changes the address. The
[guest runbook](../infra/guest/README.md) separates online dependency preparation
from disconnected offline verification. A governed provider exchange uses the
separately reviewed provider route; do not confuse it with an offline test run.

## Prepare the shell

Start in a Linux checkout. Keep this shell for the commands below. Use a
private temporary directory so filesystem checks do not inherit a shared
temporary parent:

```sh
set -euo pipefail
umask 077
export TBOUND_REPO=/absolute/path/to/tbound
cd "$TBOUND_REPO"
export TMPDIR="$(mktemp -d "$HOME/tbound-tmp.XXXXXX")"
install -d -m 0700 "$HOME/.cache/tbound-build"
go version
node --version
```

## Install Pi

Install the pinned CLI in a user-owned prefix:

```sh
export PATH="$HOME/.local/bin:$PATH"
npm_config_prefix="$HOME/.local" npm install --global --ignore-scripts \
  --no-audit --no-fund @earendil-works/pi-coding-agent@0.87.1
pi --version
```

The expected Pi version is `0.87.1`. This user installation is enough for the
offline Pi fixture. Governed use needs a separately measured, protected runtime
bundle. Installing Pi does not create that bundle or admit the service.

## Build and install TBound

From the repository root:

```sh
cd "$TBOUND_REPO"
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist \
  --prefix "$HOME/.local/share/tbound" --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
```

The installer checks the local archive and its file manifest. It installs
`tbound`, `tbound-doctor`, `tbound-profile`, and runtime assets without `sudo`.
It does not install trust anchors, service accounts, systemd units, or signed
profiles. Public release hosting and published npm packages are not available.
See [release packaging](release.md) for the supported archive targets and checks.

## Check prerequisites with Doctor

```sh
tbound-doctor --pi "$(command -v pi)"
tbound-doctor --json
```

Without `--pi`, Doctor looks up Pi on `PATH`. It checks version 0.87.1 and
prints the executable path and SHA-256. On Linux it also reports Landlock,
cgroup v2, Podman, crun, Cosign, and the fixed signed-profile paths.

Doctor is advisory. Its profile check tests path presence, not ownership,
signatures, or admission. JSON includes `dev_ready`, `governed_ready`,
`governed_prereqs_advisory`, and individual checks. `governed_ready` is always
false because runtime admission makes that decision. See
[the signed host profile](governed-setup.md#4-signed-host-profile) for the trust
files and [reproduction](reproduce.md) for the remaining host checks.

## Try the native fixture

This Go-only fixture needs no Pi or provider:

```sh
tbound serve --pi --native-fixture
```

A successful run prints a JSON lifecycle receipt with `claim_bearing=false`,
`provider_exchange=false`, and `containment=not-established`. The fixture
exercises supervised IPC, durable state, and publication with synthetic inputs.
It does not test a governed security boundary.

## Offline Pi CLI fixture

This fixture runs real Pi over RPC with the TBound extension and a scripted
provider. It needs an owned mode-0700 directory on ext4 and absolute paths.
Create a new fixture root for each invocation:

```sh
cd "$TBOUND_REPO"
export TBOUND_DEV_FIXTURE_PI="$(realpath "$(command -v pi)")"
export TBOUND_DEV_FIXTURE_NODE="$(realpath "$(command -v node)")"
export TBOUND_DEV_FIXTURE_ADAPTER="$TBOUND_REPO/adapter"
export TBOUND_DEV_FIXTURE_ROOT="$(mktemp -d "$HOME/tbound-dev-fixture.XXXXXX")"
test "$(stat -f -c %T "$TBOUND_DEV_FIXTURE_ROOT")" = ext2/ext3
test "$(stat -c %a "$TBOUND_DEV_FIXTURE_ROOT")" = 700
tbound serve --pi --dev-fixture
```

GNU `stat` reports ext4 as `ext2/ext3`. No provider credentials or network calls
are needed. The expected receipt has four tool proposals and results, five
provider turns, `worker_ready=true`, `prompt_admitted=true`, and
`turn_completed=true`. It also has `provider_exchange=false`,
`settlement=UNKNOWN`, `containment=not-established`,
`publication=not-attempted`, and `claim_bearing=false`. These limits are
intentional. See [architecture](architecture.md) for the authority boundaries.

## Managed Linux client

Ordinary `tbound serve --pi` connects to the installed local service. Without
that service and a valid signed registration it refuses. The client UID must
be admitted by the registration. Profile, source, workspace, provider, and
approved prompt are service-owned. A different task needs a new service
configuration and signing cycle. There is no prompt loop.

After [provisioning](reproduce.md), supply the exact registered task ID, prompt,
and Bash scope. Replace the example values with those of that configuration:

```sh
tbound serve --pi --task-id REGISTERED_TASK_ID \
  --task-request-id UNIQUE_REQUEST_ID \
  --bash-output out/result.txt \
  --task 'EXACT_REGISTERED_PROMPT'
```

`--bash-output` is repeatable, with at most 128 exact relative paths. Include a
directory itself if Bash creates it. A path does not authorize a subtree.
Use `--bash-no-outputs` when Bash may run but change no paths. Do not combine
the two options. With neither option, Bash is disabled. Read, write, and edit
retain their own gate checks. See [delta admission](delta-admission.md).

The internal `--service` route belongs to systemd socket activation. It is not
a standalone launch command. Read [reproduction](reproduce.md) for the first
governed task, evidence, and evaluation sequence.
