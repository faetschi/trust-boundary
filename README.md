# `tbound` prototype

**Status:** architecture scaffold only. No provider exchange, Linux containment run, conformance result, or WP1/G1 evidence exists yet.

`tbound` is a host-side supervisor prototype for one Pi coding workflow. The first slice is deliberately narrow: one Pi configuration, one agent, the proxy-backed `read`, `write`, `edit`, and `bash` tools, one compliant task, and one declared Linux VM/host profile. The prototype is intended to make the existing thesis design concrete; this README does not claim that its security properties have been implemented or demonstrated.

## Design boundary

- A TypeScript Pi SDK adapter will install a **complete custom resource loader** that exposes only `read`, `write`, `edit`, and `bash`. Each tool will send a proposal over session-bound IPC and return the supervisor's correlated result. The adapter will not independently access files, spawn commands, or call a provider. Pi and adapter inputs remain untrusted.
- A Go supervisor will own session lifecycle, IPC admission, deterministic policy decisions, durable workflow state, the isolated session repository, command-cell execution, delta validation, and evidence collection.
- A provider broker is a required later component. It will construct provider requests from a registered profile, enforce the declared tool manifest and conversation lineage, and record the exact request/result before forwarding. The model, provider, endpoint, protocol profile, and Pi SDK version remain undecided until the broker and closure work. No model call is part of this scaffold.
- The Linux runtime, immutable policy, evidence store, and registered external effect observers belong to the declared trusted computing base. Pi, model output, IPC payloads, repository contents, and commands are untrusted. This boundary is a design target, not a tested result.

```text
provider broker (profile/model selected later)
              │ registered exchange
              ▼
TypeScript Pi adapter ── session IPC ── Go supervisor
   four proxy tools                         ├─ gate + workflow + durable audit
                                            ├─ sealed session generations
                                            └─ isolated per-bash command cell
```

Provider, workflow, proposal, tool-call, and execution identifiers retain their own issuer/namespace. A provider tool-call ID is correlation data; it is not authorization or an idempotency key. Correlation must bind the response ID, tool-call ID, exact tool name, and canonical argument digest through the returned result.

## Planned source layout

```text
adapter/pi/             TypeScript Pi SDK loader and four proxy-tool definitions
cmd/tbound/             Go supervisor entry point
internal/broker/        provider profile, request construction, and lineage (broker stage)
internal/ipc/           bounded, session-bound proposal/result transport
internal/gate/          canonicalization and deterministic policy decision
internal/workflow/      leases, lifecycle, recovery, and checkpoints
internal/audit/         durable claim-critical records and bounded enrichment
internal/sessionrepo/   independent, supervisor-owned session generations
internal/executor/      one isolated command cell per allowed bash call
internal/delta/         approved-delta attribution, sealing, validation, publication
internal/oracle/        protected-tree, network, and process observations
internal/eval/          profile identity, evidence manifest, and trial disposition
```

This is a module plan, not a set of implemented packages. The README is the only initial project artifact; dependency and lock files wait until the Go toolchain, Pi SDK release, and provider profile are selected. Go is the prototype supervisor direction, but entrypoint viability remains a WP1 spike and is not G1 evidence.

## Effect and generation invariants

1. Correlate a proposal with the registered provider exchange before policy evaluation; reject unknown, duplicate, stale, reordered, or digest-mismatched calls.
2. Durably record claim-critical intent, decision, and single-use execution authority before releasing an allowed effect. Durably record a terminal result, or explicit `UNKNOWN`, before completing the proposal. Audit failure blocks effect/result release. Recovery never blindly replays an unresolved effect.
3. Read only from the governed session repository. Supervisor `write` and `edit` operations produce new supervisor-owned generations. Pi sees a read-only sealed generation; each `bash` call receives a disposable writable view of a sealed generation.
4. After `bash`, terminate writers, detach the command view, validate its complete delta, and import only attributable output into a new sealed generation. No Pi or command cell receives the live workspace or live `.git` administration.
5. Apply to the live workspace only through the separately validated publication path after the workflow is complete. A matching final tree alone does not establish that no transient mutation occurred.

These are intended contracts. They are not implemented guarantees. Denial of a proxy proposal alone does not demonstrate that an undeclared Pi/runtime path was contained; the configured surface and the Linux boundary need separate evidence.

## First implementation slice and gate

The tracked starting point is [`todo-implementation-tbound.md`](../agentic-harness/todo-implementation-tbound.md). Begin with its candidate-profile preparation and falsification spikes; do not represent them as completed. The intended order is Pi closure, broker correlation, generation/repository staging, containment, durable authority, and a reconstructable evidence package. Regardless of workstream order, each governed effect must follow the pre-effect durable-recording rule above.

WP1/G1 requires more than a tool smoke test. On the declared candidate VM, the plan requires: (1) prove the Pi surface is closed to exactly the four proxy tools; (2) capture one real provider-specific exchange and the E05 two-generation `read → edit → Bash → read` fixture; (3) prove independent repository isolation and delta provenance, beginning with ordinary byte-copy views; (4) exercise the selected rootless Podman/crun containment profile and Go entrypoint; (5) fault-inject audit write, sync, and disk-full failures; and (6) prepare the E04 transient-mutation and E06 denied-read fixtures. The tested host and runtime identities must be frozen. A failed capability probe requires redesign or a narrower claim, not an unrecorded runtime fallback.

No WP1 spike, G1 fixture, provider integration, model request, package installation, or test has been run for this scaffold.

## Open decisions

- Pi SDK version and the exact native/extension/config paths that must be disabled for closure.
- Provider/model, endpoint, request/result schemas, streaming/cancellation behavior, content-block limits, and broker profile.
- Candidate VM image, kernel/filesystem profile, Podman/crun builds, and measured rootless capabilities.
- Whether the Go supervisor entrypoint meets the declared host boundary; this remains a falsification spike.
- Journal implementation and pinned engine/filesystem identity after the required durability fault testing.
- Exact IPC schema/bounds, policy profile, filtered materialization rules, and feasibility-selected resource limits.

## Thesis design references

The normative thesis documents remain in the original thesis repository under agentic-harness/ and are not part of this standalone repository. The design references are DOCUMENT-GOVERNANCE.md, AH-technical/02-process-model.md, AH-technical/02-1-recovery-and-continuation.md, AH-technical/03-architecture.md, and AH-technical/11-evidence-contract.md.