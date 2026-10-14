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
production runtime. Profile tooling and production bindings use the official
CLI argument contract, but signed closure admission and D09 launch qualification
remain incomplete. Production launch is refused without an authentic signed
profile and a registered D09 template. Earlier SDK receipts are historical
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
- **Native host API:** the importable supervisor API (`piruntime`) should be
  constructed by the native host agent from its trusted composition root, not from
  Pi JSON. The coordinator must review any future API extension before adding
  adapter or frontend dependencies.
- **Future frontend:** may consume only bounded observation projections (session,
  generation, event, proposal/decision/result references, and explicit UNKNOWN). It
  must not own admission, correlation, durable authority, settlement, or publication.

## Runtime seams

These are the current contracts that the Go composition and the native and
adapter owners must preserve. They describe code, not qualification. The operator
CLI routes and exit codes are in [governed host setup](governed-setup.md#cli-routes-and-exit-codes).

- **Child lifecycle:** `piruntime.ChildSpec` starts a child with an exact
  environment allow-list (a nil environment is empty, never the parent's) and an
  absolute executable bound to the reviewed profile. The trusted launcher must
  verify the executable bytes through a handle-bound start. Hashing a path and then
  calling a path-based exec is not a TOCTOU proof. `IOModeNativeTUI` attaches the
  terminal streams, and `IOModeObservation` routes stdout and stderr to a bounded
  `ObservationSink`. The two modes cannot be mixed, and observations carry no
  authority.
- **Stop and settlement:** `Process.Stop` waits for direct exit and then calls the
  independently supplied `DescendantSettler`. A direct wait or a successful kill is
  not enough. Unsettled descendants yield `UNKNOWN`, not `STOPPED`. `Session.Close`
  closes admission, cancels leases, drains them, and only then settles the child. An
  uncertain cleanup stays `UNKNOWN` and is retryable. `NoDescendants` is for synthetic
  fixtures only; production admission never installs it.
- **Admission:** `ValidateHostAdmission` checks structure only. `AdmitProduction`
  with a trusted `TrustedHostVerifier` is the only path to the capability that
  `StartChild` accepts. A callback that returns nil, a client-supplied `ready` string,
  a provider model name, or a fixture receipt establishes none of the required host facts.
- **Decisions and effects:** an effect-capable `Supervisor.Serve` refuses a
  configuration without both a durable decision recorder and an `Executor` before it
  reads any proposal. `NoEffectSession` is mutually exclusive with both callbacks and
  can return only the fixed `stubbed-no-effect` result.
- **Broker continuation:** each continuation must match a broker-owned,
  pre-registered capture on tool-call ID, response ID, tool name, RFC 8785 (JCS)
  argument digest, strictly increasing sequence, and expected generation
  (`supervisor/internal/broker/protocol/arguments.go`). Unknown, duplicate, replayed,
  out-of-order, mismatched, and malformed calls fail closed (see the
  [protocol README](../supervisor/internal/broker/protocol/README.md)). Pi IPC cannot create
  captures. The broker and profile owner must append real multi-turn tool results with
  exact digests and generations before each next provider turn. Captures in the current
  fixtures are synthetic.
- **Adapter seam:** `adapter/src/tbound-cli-extension.ts` is the pinned Pi CLI
  extension (Pi 0.87.1). Pi owns the agent loop in `pi --mode rpc`. The extension
  registers the four broker tools and the Go-owned provider bridge, and it never
  creates an `AgentSession`. `adapter/src/broker-provider.ts` ignores Pi's transcript
  context: prompts are admitted by trusted session control, and tool results return to
  Go only through the executor path. `adapter/src/ipc-transport.ts` implements
  `tbound-ipc/v1` with bounded frames (1 MiB messages, 1,024 frames per direction, and
  16 MiB per stream). The earlier in-process `adapter/src/native-pi-host.ts` seam was
  removed in commit `178acdd` and is not part of the current contract.

## Current limits

The service handoff, journal, and source-capture implementation exist, but
provisioned service operation and the real signed-profile path are unqualified.
Production Pi CLI launch, D06 sealed-source boundary, production command-cell
containment, runtime settlement, publication, and independent effect observers
also remain unqualified. See [current status](status.md).

Production admission still needs these facts from the trusted host verifier. A
callback that returns nil, a client-supplied `ready` string, a model name, or a
fixture receipt cannot supply them:

- host identity and the reviewed complete runtime-profile digest;
- immutable child executable identity and digest, with the exact initial argv and
  working-directory identity;
- terminal-input endpoint identity, with the working-directory handle retained for
  the host launcher;
- the exact minimal child environment identity;
- peer-authenticated IPC binding and the terminal or stdio bridge identity;
- containment mechanism and effective configuration, and the descendant settlement
  mechanism and evidence;
- the bounded observation-channel profile;
- source provenance and offline-verifier identity; and
- the separately reviewed provider profile identity.

Open criteria, with the owner who must close each one:

- **Production recovery:** the D20 atomic reservation fix and its positive
  preparation-to-activation regression (recovery and workflow owner).
- **Broker continuation:** the registered provider exchange wired to real multi-turn
  history (broker owner). Current captures are synthetic.
- **Durable effects:** the admitted session store and durable recorder for every
  effect-capable run. A non-nil interface is not evidence of durable storage (host
  composition owner).
- **Publication trust:** reviewed source and offline-verifier provenance, quiescence,
  transition and origin verification, and owner-epoch recovery evidence (trust owner).
  The fixture's `nativeFixtureTrust` is synthetic.
- **Host and process proof:** immutable executable bytes, the exact minimal
  environment, peer identity, containment, and independent process-tree settlement
  bound to a reviewed profile (host and platform owner). The D09 Podman/crun launcher
  implements `ExecutableLauncher.StartVerified`, but no positive host launch has been
  demonstrated.
- **Interactive run:** before a claim-bearing interactive run, the native and adapter
  owner must supply a reviewed, pinned Pi terminal entrypoint and demonstrate actual
  PTY typing, ordinary chat, tool-result continuation, and denied direct
  effect, config, session, and shortcut routes. This is an open criterion, not a
  completed result.
- **Thesis workflow:** no dataset runner or claim-bearing evidence manifest exists.
  Map approved experiments onto the runtime only after the criteria above are reviewed
  (evaluation owner).

Operator prerequisites are separate from code proof. Approved pinned Pi and Node
artifacts, a supported interactive terminal, a reviewed host profile and platform
policy, and any provider access approved by the broker owner are all needed. Installing
artifacts or supplying credentials does not establish executable identity, containment,
provider authenticity, or publication trust.
