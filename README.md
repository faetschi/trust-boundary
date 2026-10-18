# TBound

TBound is a Linux supervisor prototype for running Pi coding tasks through a
fixed set of four tools: `read`, `write`, `edit`, and `bash`. Its intended
boundary keeps policy decisions, durable execution state, command isolation,
publication, and evidence in Go; Pi and model output remain untrusted.

**Current status:** the implementation is not a qualified governed Pi
deployment. `G1=false`; containment is `not-established`. The managed Linux
service and source-capture path have not been qualified with a provisioned,
signed profile. The official Pi CLI/RPC development fixture passes, but
production profile and launcher migration remain incomplete. A build, fixture
receipt, or capability probe does not establish containment or a thesis result.
See [current status](docs/status.md) and [architecture](docs/architecture.md).

## What you can run

Two local fixtures are available on Linux; neither is claim-bearing. The
Go-only fixture exercises the native lifecycle without launching Pi:

```sh
cd supervisor
go build -o /tmp/tbound ./cmd/tbound
/tmp/tbound serve --pi --native-fixture
```

The offline Pi CLI fixture starts the official CLI (Pi 0.87.1, Node 24.21.0
pin) with an isolated extension and a scripted provider. Its environment and
expected receipt are in [launch](docs/launch.md#offline-pi-cli-fixture);
installing Pi is in [install](docs/install.md). The production-shaped
`serve --pi` command is a client for a registered local service and returns
`service unavailable` when that service is absent.

Run TBound inside Linux. On Windows, use the existing Ubuntu Hyper-V guest or a
separately qualified Linux VM; native Windows is not the isolation boundary.
The reference VM is development infrastructure, not a clean G1 baseline. For
new hosts see [reproducibility setup](docs/reproducible-setup.md).

## How the intended workflow fits together

```text
Pi CLI (untrusted, RPC)
  └─ TBound extension: read / write / edit / bash proposals
       └─ Go supervisor: policy, durable state, isolated execution, evidence
            └─ registered Linux profile and managed service
```

Go owns deterministic authorization, session generations, durable audit,
isolated Bash execution, and later publication, and binds the provider exchange
to the registered profile. These are design responsibilities; the
CLI/extension composition and Linux security boundary still need qualification.

## Repository map

- `supervisor/cmd/tbound/`: Go CLI and Linux launch composition.
- `supervisor/internal/broker/`, `ipc/`, `gate/`: provider correlation,
  proposal transport, and policy decisions.
- `supervisor/internal/sessionrepo/`, `delta/`, `workflow/`,
  `publication/`: generations, validated changes, workflow recovery, and
  per-object publication.
- `supervisor/internal/piruntime/`, `sandbox/`: Pi process and Linux runtime
  boundary code; production qualification remains open.
- `adapter/`: Pi CLI extension and transition-era worker sources.
- `infra/guest/`, `infra/repro/`, `infra/hyperv/`: offline checks,
  capability probes, and host/VM setup procedures.
- `docs/`: setup, operation, architecture, and status guides; start at
  [docs/README.md](docs/README.md). Historical sources removed from the active
  set are mapped in [`docs/evidence/README.md`](docs/evidence/README.md).
