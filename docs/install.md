# Install TBound and Pi

Install and run Pi inside Linux. On a Windows host, use an Ubuntu VM. TBound
does not create the isolation boundary by itself: the production Linux profile
and signed managed service remain unqualified.

## Pi CLI

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

## Build and install TBound from source

Go 1.27.1 is the current reproducibility pin. From a Linux checkout:

```sh
make build VERSION=dev
bash install/install.sh --version dev --local-dist dist --no-path
export PATH="$HOME/.local/share/tbound/bin:$PATH"
tbound-doctor --pi "$(command -v pi)"
```

The install script verifies the local release archive and installs the Go
commands and packaged runtime assets into a user-owned prefix. Release hosting
and public package publication are not available. The package and container
templates are distribution work, not a governed runtime.

To build only the CLI for a local fixture, choose a private output path:

```sh
mkdir -m 700 -p "$HOME/.cache/tbound-build"
cd supervisor
go build -o "$HOME/.cache/tbound-build/tbound" ./cmd/tbound
```

Avoid building into the source tree: it contains a tracked historical ELF and
an ignored local Windows executable.

## Linux host and Windows VM

For the existing reference VM, follow [launch.md](launch.md) to start and
connect to Ubuntu. For a new host, read [reproducible-setup.md](reproducible-setup.md)
and the linked infrastructure runbooks first. Those procedures are prototypes
and have not been qualified end to end on a clean machine.

## Doctor

`tbound-doctor` checks the explicit Pi CLI or resolves `pi` from `PATH`.
It verifies the version and reports the resolved executable fingerprint:

```sh
tbound-doctor --pi "$(command -v pi)"
tbound-doctor --json
```

Doctor is a diagnostic. `dev_ready` does not mean that the Pi extension,
managed service, or Linux containment profile is admitted. `governed_ready`
remains false.
