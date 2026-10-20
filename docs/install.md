# Install TBound and Pi

Choose the Linux environment that fits your host:

- **Native Linux:** build, install, and run directly; no VM is required.
- **Windows with WSL2:** run the Linux commands inside your WSL2 distribution,
  with the checkout and fixture directories on its Linux filesystem rather than
  under `/mnt/c`. This is a development route, not a qualified governed host.
- **Separate Linux VM:** optionally use a dedicated VM for test infrastructure.
  It is an alternative to native Linux or WSL2. See the existing reference VM
  steps in [launch](launch.md#existing-windows-hyper-v-vm) and the canonical
  [reproducible setup](reproducible-setup.md) and linked infrastructure runbooks
  before creating or provisioning a VM.

Start with the Go-only native fixture below. Node and Pi are optional until you
want the Pi integration. General installation does not provision or qualify the
production Linux profile or signed managed service.

## Build and install TBound from source

Building requires Bash, `make`, and the pinned Go 1.27.1 installed on `PATH`,
plus access to any Go modules not already cached. Run `go version` to confirm
the selected toolchain. From the repository root on Linux amd64:

```sh
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
umask 077
mkdir -m 700 -p "$HOME/.cache/tbound-tmp"
export TMPDIR="$HOME/.cache/tbound-tmp"
tbound serve --pi --native-fixture
```

The fixture needs no Node, Pi, credentials, or provider access. Its receipt
reports `claim_bearing=false` and `containment=not-established`. Protected audit
paths reject any group- or other-writable ancestor, including sticky `/tmp`;
use a private `TMPDIR` on native Linux, WSL2, and a Linux VM alike. Existing
directories must already have suitable permissions; `mkdir -m` does not change
them.

The install script verifies the local release archive and installs the Go
commands and packaged runtime assets into a user-owned prefix. Installing an
already-built matching Linux artifact needs no Go or `make`; see the
[installer reference](../install/README.md) for `--local-dist`, version, and
prefix options. Release hosting and public package publication are not available. The package and container
templates are distribution work, not a governed runtime.

To build only the CLI for a local fixture, choose a private output path:

```sh
umask 077
mkdir -m 700 -p "$HOME/.cache/tbound-build" "$HOME/.cache/tbound-tmp"
export TMPDIR="$HOME/.cache/tbound-tmp"
cd supervisor
go build -o "$HOME/.cache/tbound-build/tbound" ./cmd/tbound
```

Avoid building into the source tree: it contains a tracked historical ELF and
an ignored local Windows executable. Continue with [launch](launch.md) to run
the fixture.

## Optional Pi CLI

The offline development integration uses the official Pi CLI at 0.87.1. Install it inside
the Linux user environment with Node.js 22.19 or newer:

```sh
export PATH="$HOME/.local/bin:$PATH"
npm_config_prefix="$HOME/.local" npm install --global --ignore-scripts \
  @earendil-works/pi-coding-agent@0.87.1
pi --version
```

Node.js 24.21.0 is the reproducibility toolchain pin used by this repository.
Pi's upstream installer (`curl -fsSL https://pi.dev/install.sh | sh`) follows
the upstream release; use the exact npm version above when matching the
prototype's experiment pin. See the official [Pi quickstart](https://pi.dev/docs/latest/quickstart).

The pinned CLI was installed and returned version 0.87.1 on the existing
Ubuntu reference VM. This verifies that install step on that VM only; it does
not qualify a clean host or a TBound launch.

This installs Pi only. The offline TBound RPC development fixture runs with
the isolated extension and scripted provider; production profile tooling and
launcher integration remain blocked, and no signed production profile admits
this route. See
[launch](launch.md) for its required environment. Do not put provider
credentials in fixtures or source control.

## Doctor

Optionally run `tbound-doctor` for an advisory host report. After installing Pi,
use `tbound-doctor --pi "$(command -v pi)"`. Its Pi and governed-host findings
are separate from the Go-only fixture check; see [doctor](doctor.md).
