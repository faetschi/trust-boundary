# TBound

TBound is a Linux supervisor prototype for running Pi coding tasks through a
fixed set of four tools: `read`, `write`, `edit`, and `bash`. Its intended
boundary keeps policy decisions, durable execution state, command isolation,
publication, and evidence in Go; Pi and model output remain untrusted.

**Current status:** the implementation is not a qualified governed Pi
deployment. `G1=false`; containment is `not-established`. The managed Linux
service and source-capture path have not been qualified with a provisioned,
signed profile. The official Pi CLI/RPC development fixture now passes, but
production profile and launcher migration remain incomplete and unqualified.
A build, fixture receipt,
or capability probe does not establish containment or a thesis result. See
[current status](docs/status.md) and [architecture](docs/architecture.md).

## What you can run

Two local fixtures are available on Linux. The Go-only fixture exercises the
native lifecycle without launching Pi. The offline Pi CLI fixture starts the
official CLI with an isolated extension and scripted provider; it uses a
mock-host deny fixture, makes no real provider exchange, and does not establish
containment:

```sh
cd supervisor
go build -o /tmp/tbound ./cmd/tbound
/tmp/tbound serve --pi --native-fixture
```

The Pi CLI route requires absolute paths to Pi, Node, and this repository's
adapter, plus an existing owned mode-0700 directory on ext4 for private
temporary data. From the repository root, set `TBOUND_DEV_FIXTURE_ROOT` to
that directory and run:

```sh
export TBOUND_DEV_FIXTURE_PI="$(realpath "$(command -v pi)")"
export TBOUND_DEV_FIXTURE_NODE="$(realpath "$(command -v node)")"
export TBOUND_DEV_FIXTURE_ADAPTER="$(pwd)/adapter"
export TBOUND_DEV_FIXTURE_ROOT="$HOME/.local/state/tbound-dev-fixture"
mkdir -m 700 -p "$TBOUND_DEV_FIXTURE_ROOT"
test "$(stat -f -c %T "$TBOUND_DEV_FIXTURE_ROOT")" = ext2/ext3
test "$(stat -c %a "$TBOUND_DEV_FIXTURE_ROOT")" = 700
mkdir -m 700 -p "$HOME/.cache/tbound-build"
cd supervisor
go build -o "$HOME/.cache/tbound-build/tbound" ./cmd/tbound
"$HOME/.cache/tbound-build/tbound" serve --pi --dev-fixture
```

Install Pi 0.87.1 and use the pinned Node 24.21.0 toolchain as described in
[install](docs/install.md). The fixture does not require `NODE_MODULES` or
provider credentials. Its receipt must remain `claim_bearing=false`,
`provider_exchange=false`, `containment=not-established`, `settlement=UNKNOWN`,
and `publication=not-attempted`; it records four tool proposals/results, five
scripted provider turns, and generations g0→g1→g2. The production-shaped
`serve --pi` command is a client for a registered local service and currently
returns `service unavailable` when that service is absent. The official Pi
CLI fixture is development evidence only. Its implementation does not yet
enable production admission; see [current status](docs/status.md).

## Pi and Linux setup

Run TBound inside Linux. On Windows, use the existing Ubuntu Hyper-V guest or a
separately qualified Linux VM; native Windows is not the isolation boundary.
The reference VM is development infrastructure, not a clean G1 baseline.

For the pinned Pi version, install Pi inside Linux with Node.js 22.19 or newer:

```sh
PATH="$HOME/.local/bin:$PATH" npm_config_prefix="$HOME/.local" \
  npm install --global --ignore-scripts @earendil-works/pi-coding-agent@0.87.1
export PATH="$HOME/.local/bin:$PATH"
pi --version
```

Node.js 24.21.0 is the reproducibility toolchain pin; 22.19 is Pi's documented
minimum. The pinned CLI was installed and version-checked on the existing
Ubuntu reference VM. That confirms the user installation step only; the VM is
not a clean qualified host, and TBound's production launch remains unavailable.
The ordinary Pi installer tracks its own release and is not a substitute for
this exact experiment pin. See [installation](docs/install.md),
[launch](docs/launch.md), and [reproducibility setup](docs/reproducible-setup.md).

## How the intended workflow fits together

Pi runs as an untrusted worker in RPC mode. A TBound extension is intended to
expose only the four proxy tools and send each proposal to the Go supervisor.
Go owns deterministic authorization, session generations, durable audit,
isolated Bash execution, and later publication. The provider and approved model
exchange must also be bound to the registered profile. These are design
responsibilities; the CLI/extension composition and Linux security boundary
still need qualification.

```text
Pi CLI (untrusted, RPC)
  └─ TBound extension: read / write / edit / bash proposals
       └─ Go supervisor: policy, durable state, isolated execution, evidence
            └─ registered Linux profile and managed service
```

## Repository map

- `supervisor/cmd/tbound/`: Go CLI and Linux launch composition.
- `supervisor/internal/broker/`, `ipc/`, `gate/`: provider correlation,
  proposal transport, and policy decisions.
- `supervisor/internal/sessionrepo/`, `delta/`, `workflow/`,
  `publication/`: generations, validated changes, workflow recovery, and
  per-object publication.
- `supervisor/internal/piruntime/`, `sandbox/`: Pi process and Linux runtime
  boundary code; production qualification remains open.
- `adapter/`: Pi CLI extension and transition-era worker sources; earlier SDK
  receipts are historical and do not qualify the CLI route.
- `infra/guest/`, `infra/repro/`, `infra/hyperv/`: offline checks,
  capability probes, and host/VM setup procedures.
- `docs/`: active setup, operation, architecture, and status guides.

The optional web chat workspace lives in `extensions/webchat/`, as its own Go
module with its own Node tests. It is not required: tbound core builds, tests,
installs and runs without that directory, and the core's build and test gate
need no Node. (The Pi harness the core launches is a separate Node-based runtime
and is unchanged.) The extension depends
only on the core's service contract, and the core never imports it. The boundary
guard `scripts/check-extension-boundary.sh` runs in `make test` and in CI; see
[`extensions/webchat/README.md`](extensions/webchat/README.md).

For historical source removed from the active documentation set, see
[`docs/evidence/README.md`](docs/evidence/README.md) and its
[`historical-sources.json`](docs/evidence/historical-sources.json) map.
Historical evidence and the candidate experiment manifest are kept separate
from current status.
