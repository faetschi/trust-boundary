# TBound

TBound is a Linux supervisor prototype for running Pi coding tasks through a
fixed set of four tools: `read`, `write`, `edit`, and `bash`. Go owns policy,
durable state, command isolation, publication, and evidence; Pi and model
output stay untrusted.

## Status

The live state is in [OVERVIEW.md](OVERVIEW.md); the detailed log is in
[TASK_STATE.md](TASK_STATE.md). In short:

- Freeze **F12 = `eed7722`**. **G1 passes** on the real guest: read, edit,
  bash, and publish, plus the route and direct-runtime probes.
- The pilot is not ready. The EVQ dry run passed 18 of 22 items, and the rest
  need fresh artifacts. **H1 is unexecuted**; a build, fixture receipt, or
  capability probe is not a thesis result.
- The engineering provider route is **OpenCode Go**.

Gates and priorities: [docs/status.md](docs/status.md). Design:
[docs/architecture.md](docs/architecture.md).

## Quick start

Run TBound inside Linux. On Windows use the Ubuntu Hyper-V guest; native
Windows is not the isolation boundary. For new hosts see
[reproducible setup](docs/reproducible-setup.md); installing Pi is in
[install](docs/install.md).

The Go-only fixture exercises the native lifecycle without Pi. It is not
claim-bearing:

```sh
cd supervisor
go build -o /tmp/tbound ./cmd/tbound
/tmp/tbound serve --pi --native-fixture
```

The offline Pi CLI fixture and the managed-service client are described in
[launch](docs/launch.md#offline-pi-cli-fixture). `serve --pi` is a client for a
registered local service and returns `service unavailable` when none exists.
The provisioning and signing procedure for the guest is in
[infra/guest/PROVISIONING-SIGNING.md](infra/guest/PROVISIONING-SIGNING.md).

## Repository map

- `supervisor/`: Go CLI (`cmd/tbound/`) and internals (broker, gate, session
  repository, workflow, publication, Pi runtime, sandbox).
- `adapter/`: Pi CLI extension sources.
- `infra/guest/`, `infra/repro/`, `infra/hyperv/`: offline checks, capability
  probes, and host/VM setup.
- `corpus/`: evaluation corpus inputs.
- `docs/`: guides; start at [docs/README.md](docs/README.md). Retired sources
  are mapped in [docs/evidence/README.md](docs/evidence/README.md).
- `extensions/webchat/`: optional web chat, see below.

## Web chat (frozen)

`extensions/webchat/` is frozen for the thesis: CLI-only support, no new
features, and it will move to its own repository after the thesis. It is not
part of the thesis claims. See [its README](extensions/webchat/README.md).
