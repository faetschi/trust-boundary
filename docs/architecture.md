# Architecture

TBound is a Linux host supervisor prototype for a single Pi coding task. Its
design keeps authorization and durable execution state outside the model
worker. The supported tool surface is `read`, `write`, `edit`, and `bash`.

## Intended authority flow

```text
Official Pi CLI 0.87.1 (untrusted worker, RPC mode)
  └─ TBound extension exposes four proxy tools
       └─ bounded session IPC
            └─ Go supervisor
                 ├─ correlate registered provider calls
                 ├─ apply deterministic policy
                 ├─ record authority and results durably
                 ├─ manage sealed session generations
                 ├─ execute Bash in an isolated Linux cell
                 └─ validate and publish approved changes
```

The isolated extension and offline CLI/RPC fixture have passed development
checks on the existing VM. This diagram still describes an unqualified
production runtime: profile tooling and production bindings now use the official
CLI argument contract, but signed closure admission and D09 launch qualification
remain incomplete. The D09 launcher still refuses production launch. Earlier SDK receipts are historical
transition artifacts, not evidence for the official CLI composition.

## Responsibility boundaries

- **Pi and model output:** propose work and consume tool results. They are
  untrusted and do not own filesystem or command authority.
- **TBound extension:** intended to register only the four mediated tools and
  forward calls over the session channel. It must not add direct file access,
  shell execution, provider access, or extra Pi tools.
- **Go supervisor:** correlates proposals to registered provider exchanges,
  evaluates deterministic policy, records claim-critical state before effects,
  and validates results before release.
- **Session repository:** holds supervisor-owned generations. Writes and edits
  create new generations; Bash runs against a disposable view. A live workspace
  is changed only through the separate publication path.
- **Linux runtime profile:** supplies process, filesystem, network, cgroup,
  namespace, and syscall restrictions. The profile must be signed and admitted
  by the runtime verifier; a tool-level deny alone is not containment evidence.

## Current limits

The service handoff, journal, and source-capture implementation exist, but
provisioned service operation and the real signed-profile path are unqualified.
Production Pi CLI launch, D06 sealed-source boundary, production command-cell
containment, runtime settlement, publication, and independent effect observers
also remain unqualified. See [current status](status.md).
