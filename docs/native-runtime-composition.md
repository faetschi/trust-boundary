# Native Pi runtime composition

## Status and claim boundary

This increment adds the importable native-host seam under
`supervisor/internal/piruntime` and a deliberately explicit Linux fixture route
to `supervisor/cmd/tbound`. It does **not** claim a governed Pi launch, a real
provider exchange, containment, or G1 publication evidence. The fixture is
non-claim-bearing even though it uses the real IPC, gate, durable executor,
session repository, and publication finalizer.

The normal product route remains the native Pi harness:

```text
tbound serve --pi
```

It fails closed when the host does not supply an installed and attested native
profile. It never falls back to direct Pi or to the browser/SDK fixture.

## Importable construction contract

The minimum extracted core is `piruntime.Supervisor`. Its exported fields and
`Serve(context.Context) error` semantics are source-compatible with the former
`cmd/tbound` implementation; `cmd/tbound` aliases the interfaces and type.
The loop is still strictly:

```text
bounded IPC proposal -> registered broker correlation -> gate -> durable
decision record -> executor -> bounded strict-JSON result
```

`piruntime.Broker`, `piruntime.Executor`, and
`piruntime.DurableDecisionRecorder` are
the proposal-loop composition interfaces. An effect-capable `Supervisor.Serve`
refuses configuration without both a recorder and executor before it reads any
proposal. A non-nil callback is a configuration invariant, not proof that
decisions are durably recorded: the production root must supply its actual
durable audit/session-repository recorder. The explicit `NoEffectSession` mode
is mutually exclusive with both callbacks; it can return only a fixed
`stubbed-no-effect` fixture result and never invokes an executor. The supervisor
owns no provider credentials, browser state, frontend type, or host assertion.

The native host agent should construct this API from its trusted composition
root rather than from Pi JSON. The coordinator must review any future API
extension before adding adapter or frontend dependencies.

## Host admission and missing attestations

`piruntime.ValidateHostAdmission` performs the structural binding check for a
production `HostProfile` and a `HostAttestation` across every profile
component. It is not itself an authorization operation: only
`piruntime.AdmitProduction`, supplied with the host's trusted verifier, can
produce the opaque capability accepted by `StartChild`. A structurally valid
receipt copied from a fixture or IPC message cannot authorize a child.
The exact missing production facts are:

- host identity and the reviewed complete runtime-profile digest;
- immutable native child executable identity/digest;
- exact initial argument vector and working-directory identity/digest;
- terminal-input endpoint/profile identity and digest, with the actual
  working-directory handle retained for the host launcher;
- exact minimal child-environment identity/digest;
- peer-authenticated IPC binding identity/digest and the terminal/stdio bridge
  identity/digest;
- containment mechanism/profile identity and effective configuration;
- descendant/process-scope settlement mechanism and evidence identity;
- bounded observation-channel profile identity;
- source/provenance identity and offline-verifier identity; and
- the separately reviewed provider profile identity.

A callback returning `nil`, a client-supplied `ready` string, a provider model
name, or a fixture receipt does not establish any of these facts. The current
approved provider remains the broker-owned
`nvidia/nemotron-3.5-lightning:free` profile; no live exchange is performed by
this increment.

Production callers should use `piruntime.AdmitProduction` with a trusted
`TrustedHostVerifier`; a nil verifier, an unbound capability, a verifier that
does not supply runtime bindings, or a component-mismatch receipt is rejected
before any child or IPC session is admitted. The interface alone is not
external authenticity evidence: the concrete host verifier must be the
reviewed offline/platform dependency, and this increment supplies no such
production implementation.

## Child lifecycle seam

`piruntime.ChildSpec` starts a child with an exact environment allow-list: a
nil environment is empty and is never replaced with the parent environment.
The executable path is absolute and must be bound to the reviewed profile. The
trusted launcher must verify the actual executable bytes and use a
platform-specific immutable/handle-bound start; hashing a path and then
calling an ordinary path-based exec is not a TOCTOU proof.
`IOModeNativeTUI` attaches the native TUI streams; `IOModeObservation` routes
stdout/stderr to a bounded `ObservationSink`. They cannot be mixed. Structured
observations are data only and do not carry authorization or publication
authority.

`Process.Stop` waits for direct exit and then calls the independently supplied
`DescendantSettler`. Direct `exec.Wait` or a successful kill is insufficient.
If descendants are not observed settled, the result is `UNKNOWN`, not
`STOPPED`. `piruntime.Session.Close` first closes admission, cancels admitted
lease contexts, waits for leases to drain, and only then attempts child
settlement. A drain timeout or uncertain cleanup remains `UNKNOWN`.

The bound child authority is captured privately by `BindChild`: executable,
arguments, working directory, exact environment, stdin/terminal input,
terminal/observation output endpoint, launcher, and descendant settler are
copied or retained in the admitted snapshot. Initial arguments, directory, and
stdin must match the verifier-supplied launch plan before binding. Mutating
the exported `ChildSpec` builder after binding cannot replace the trusted
launch plan, settler, or terminal/observation endpoints. Production still
requires the host verifier to retain the actual approved launcher and settler;
interface identity and profile/path/digest fields alone do not prove executable
bytes, environment binding, terminal peer identity, containment, or platform
authenticity.

Structured observation uses a context-aware sink, a bounded queue, a finite
cleanup budget, and a process-wide worker permit pool. Session cleanup,
descendant settlement, and observation callbacks each have bounded worker
admission; a saturated pool refuses explicitly. A stuck trusted sink is
cancelled and reported as an observation cleanup gap; it is never force-killed
or silently classified as settled. Worker registration refuses explicitly when
the bounded pool is saturated, preventing repeated sessions from accumulating
unbounded callback goroutines. Descendant settlement receives an owned finite
context and likewise yields `UNKNOWN` on timeout/error. Session cleanup worker
saturation closes admission and synchronously requests child stop without
starting another cleanup goroutine; it returns `STOPPED` only if drain, direct
exit, and descendant evidence all complete, otherwise `UNKNOWN`. An UNKNOWN
cleanup is retryable: later `Close` calls re-drain leases and re-observe child
settlement rather than caching uncertainty as terminal. These controls bound
runtime-owned workers; they are not proof that an external callback or host
process is trustworthy.

Process wait records bounded observer shutdown errors in `Settlement` as
`observation_gap` plus `observation_detail`. If direct-child exit and descendant
settlement are established, the process may still be `STOPPED`, but `Process`
and `Session.Close` return the observer cleanup error alongside that settlement
so callers cannot mistake missing observations for complete evidence.
Sink-returned errors/panics and byte truncation/backpressure are retained as
`observation_dropped_bytes` plus the gap detail even if the observer worker has
already exited before process cleanup begins.

Production must supply a real host/profile-backed descendant settler. The
`NoDescendants` implementation is only for an explicit synthetic fixture and
is not installed by production admission.

## Broker continuation contract

The fixture pre-registers independent synthetic provider captures before any
IPC proposal. Each continuation is checked by the existing protocol stream for
the exact tool-call ID, response ID, tool name, RFC 8785 argument digest,
strictly increasing sequence, and expected generation. The fixture exercises a
read at `g0`, a write at `g0` producing `g1`, and a later read at `g1` through
the actual durable executor. Duplicate, stale, reordered, digest-mismatched,
or generation-mismatched proposals are rejected and close the stream. These
are protocol/fixture properties, not provider authenticity evidence.

The existing `broker.Broker` remains a registered, broker-owned exchange
component. Multi-turn provider history must be extended by the broker/profile
owner so each tool result is appended with its exact digest and generation; Pi
IPC cannot manufacture continuation facts.

## CLI paths and exit contract

From `supervisor`:

```text
go run ./cmd/tbound
```

returns exit **2** with the existing unconfigured-runtime refusal.

```text
go run ./cmd/tbound serve --pi
```

returns exit **2** with `native Pi host profile is missing; production --pi
launch is refused` unless the future host agent provides the reviewed profile
route. Supplying an arbitrary `--native-host-profile` path still returns exit
**2**; a path is not an attestation.

On Linux only, the explicit fixture route is:

```text
go run ./cmd/tbound serve --pi --native-fixture
```

It returns exit **0** only after the supervised IPC/durable/sessionrepo
publication fixture completes and writes one bounded JSON receipt marked
`"claim_bearing":false`, `"provider_exchange":false`, and
`"containment":"not-established"`. It does not launch Pi or contact a
provider. Fixture setup or publication failure returns exit **2**. On Windows,
`serve --pi` remains refusal-only (exit **2**) because the native Linux
publication fixture is not compiled into that target.

The existing Linux synthetic smoke route remains explicit:

```text
go run ./cmd/tbound --smoke-listen --socket-dir <private-0700-directory>
```

It retains its existing exit **0** success / exit **2** failure contract and is
not a native Pi claim.

## Verification and remaining integration

Safe focused checks are:

```text
cd supervisor
gofmt -l internal/piruntime cmd/tbound
go test ./internal/piruntime ./cmd/tbound
go vet ./internal/piruntime ./cmd/tbound
go build ./cmd/tbound
```

The corrected Linux fixture path has also been exercised as a real built
binary, using an isolated mode-0700 temporary directory. It completed the
actual durable `read(g0) -> write(g1) -> read(g1)` sequence, verified the
result IDs/digests and sealed generation payloads, finalized publication, and
returned:

```text
{"mode":"native-fixture","claim_bearing":false,"provider_exchange":false,"containment":"not-established","publication_status":"SUCCEEDED"}
```

The same binary returned exit **2** for both missing `serve --pi` admission and
an arbitrary `--native-host-profile` path. These checks establish only the
Go-owned fixture/composition boundary; they do not establish a native Pi
launch, executable-byte verifier, minimal-environment process proof,
containment, provider exchange, or production trust.

Coordinator acceptance should run the full source snapshot with a private
ext4 `TMPDIR` mode `0700`, the configured Go toolchain, and the existing Linux
race suite. Windows acceptance should use an exact source snapshot with
`go build ./...` and `go test ./internal/... ./e2e`; no Linux Node assumption is
needed. No provider network, VM, guest, privileged host operation, or secret
lookup is part of these checks.

Still required before production `serve --pi` can be enabled:

1. the native host agent must load and attest the complete profile above;
2. the reviewed Pi executable/entrypoint and minimal child environment must be
   bound to that profile;
3. the host must provide peer-authenticated IPC setup and an independent
   process/descendant/mount settlement observer;
4. the broker owner must connect real multi-turn provider history and exact tool
   result continuations without moving credentials into Pi; and
5. the publication Trust implementation must verify source/offline-verifier
   provenance, containment, quiescence, settlement, transition receipts, and
   origin. No nil callback may be used as production trust.

The future frontend may consume only bounded observation projections (session,
generation, event, proposal/decision/result references, and explicit UNKNOWN).
It must not own admission, correlation, durable authority, settlement, or
publication, and this package intentionally has no frontend dependency.

## Adapter seam compatibility

The reviewed adapter seam in `adapter/src/native-pi-host.ts` is an in-process
library: it accepts an already constructed `IpcClient` and a caller-owned Pi
`Terminal`, and its `onObservation` callback is separate from terminal output.
That maps to this runtime as follows:

| Adapter fact | Host runtime binding |
| --- | --- |
| `IpcClient`/`IpcProposal` | `ipc.Client`/`protocol.Proposal`; the four wire fields and `tbound-ipc/v1` token are compatible |
| `tool_call_id` | Pi's opaque ID is echoed by IPC and is checked against the broker's pre-registered capture; it is not authority |
| `onObservation` | a context-aware `ObservationSink` adapter; bounded structured data remains observation-only and cancellation must be honored |
| caller-owned `Terminal` | `ChildSpec` `IOModeNativeTUI` stdio bridge; terminal output is never parsed as authority |
| `closeIpcOnDispose:false` | required when the supervisor owns IPC lifetime |

The adapter fixture's provider-generated call IDs/arguments are not broker
captures merely because they cross IPC. A real host composition must
pre-register an independent broker capture for each continuation, including
its exact raw arguments, response identity, sequence, and current generation.
The current adapter fixture intentionally uses an in-memory IPC pair and DENY
results, so it is a bounded native-TUI fixture, not the production cross-
language launch. A future launcher/entrypoint still needs to construct the
caller-owned Terminal, dial the authenticated Unix socket, pass only the
profile-approved environment, and expose a bounded observation bridge. No
placeholder direct-Pi launch is added here.

The adapter report currently covers only its two native-host tests (fixture
continuations and production-constructor refusal). Those results are useful
compatibility evidence, not proof of production closure, containment, provider
authenticity, or the host settlement profile.

## Thesis-runtime vertical-slice readiness

The current milestone is a Go-owned composition fixture, not an executable
thesis runtime. The smallest credible vertical slice for interactive dataset
work still needs these concrete components:

| Component | Current state | Required owner action before a claim-bearing run |
| --- | --- | --- |
| Pi terminal/session entrypoint | Adapter seam is an in-process library; stock loop remains refused and the Go CLI launches no Pi/Node process. | Native/adapter owner supplies the reviewed pinned entrypoint and demonstrates actual PTY typing, ordinary chat, tool-result continuation, and denied direct effect/config/session/shortcut routes. |
| Go↔Node launch/bootstrap | `ExecutableLauncher` and full launch-plan binding are interfaces only; no production implementation is registered. | Composition owner provides a platform-reviewed launcher, private authenticated bootstrap/IPC descriptors, terminal stdin/stdout, separate bounded observations, exact argv/cwd/env, and independent shutdown handling. |
| Broker continuation | Go fixture pre-registers synthetic captures for g0→g1; this is not live provider history. | Broker owner wires the real registered provider exchange and appends exact returned tool results/digests/generations before each next provider turn; Pi observations/claims never create captures. |
| Durable effects | `DurableExecutor`, decision audit recorder, and session repository run in the Go fixture. | Host composition creates the actual admitted session/store and durable recorder for every effect-capable run; production `Serve` refuses a missing recorder. A non-nil interface alone is not evidence of durable storage. |
| Publication trust | The fixture's `nativeFixtureTrust` is explicitly synthetic and returns success for fixture-only checks. | Trust owner supplies reviewed source/offline-verifier provenance, quiescence, transition/origin verification, and owner-epoch/recovery evidence. |
| Host/process proof | Profile, attestation, launcher, terminal, observation, and descendant-settlement contracts exist; trusted host verifier and platform implementations are absent. | Host/platform owner binds immutable executable bytes, exact minimal environment, peer identity, containment, and independent process-tree settlement to a reviewed profile. |
| Thesis dataset/hypothesis workflow | No dataset runner, experiment protocol adapter, or claim-bearing evidence manifest is introduced here. | Thesis/evaluation owner maps approved experiments to the runtime only after the above seams are reviewed; preserve reproducibility, policy decisions, effect identities, generations, and explicit evidence gaps. |

Operator prerequisites are separate from code proof: an operator will need the
approved pinned Pi/Node artifacts, a supported interactive terminal, a
reviewed host profile and platform policy, and whichever provider access the
broker owner has separately approved. Installing those artifacts or supplying
credentials does not itself establish executable identity, containment,
provider authenticity, or publication trust. No such installation, secret
lookup, provider call, or privileged host action is part of this increment.
