# TBound

TBound is a Go supervisor prototype with a TypeScript adapter around the pinned Pi SDK (0.87.1). The adapter's tool surface is `read`, `write`, `edit`, and `bash`.

**Current implementation:** the repository contains supervisor components for broker protocol/correlation, IPC, policy, durable audit and workflow, session generations, command/runtime interfaces, delta handling, publication, private Git, and the governance companion. The adapter contains Pi host/policy fixtures, proxy tools, and IPC transport. OpenRouter request/transport and provider-bridge components are present; that does not make them a production Pi/provider integration.

## Production CLI status

`tbound serve --pi` currently refuses to launch: no installed, attested production Pi host profile is accepted. The separate `--native-fixture` route is explicit and non-claim-bearing; it exercises a synthetic Go workflow and does not launch Pi or contact a provider. Component tests are not evidence of a complete admitted Pi/provider workflow or of host security properties.

Claim boundaries: there is no production Pi route. The opt-in provider exchange tests are one-shot exchanges and do not show a real production multi-turn workflow. Containment is `not-established` and G1 has not passed. Components, fixtures, and synthetic routes are not thesis evidence.

See [fresh-host setup](docs/setup.md) for setup instructions. The [central review](docs/central-review-corrections-2026-10-07.md) and [dated readiness report](docs/tbound-readiness-status-2026-10-08.md) retain their dated scope; they are historical records, not a fresh verification run.

## Design contracts (intended, not guaranteed)

- The adapter registers only four untrusted Pi SDK proxy tools (`read`, `write`, `edit`, `bash`), each sending a proposal over session-bound IPC. The Go supervisor owns session lifecycle, admission, policy, durable state, the session repository, execution, delta validation, and evidence. A provider broker binds each exchange to a registered profile and tool manifest. Pi, the model, IPC payloads, the repository, and commands are untrusted. The Linux runtime, immutable policy, evidence store, and registered external-effect observers are the intended trusted computing base; this is not yet an established deployed TCB.
- A provider tool-call ID is correlation data only, not authorization or an idempotency key. A result must bind the response ID, tool-call ID, exact tool name, and canonical argument digest.
- Effect sequencing: a proposal proceeds only after its registered provider exchange is verified and policy decides. Before an effect is released, claim-critical intent, decision, and single-use execution authority are durably recorded. Before the proposal completes, a durable terminal result or explicit `UNKNOWN` is recorded. Audit failure blocks effect and result release; recovery never blindly replays an unresolved effect.
- Generations: reads come from the governed repository. Supervisor `write` and `edit` operations advance the generation. Pi sees a sealed read-only view. Each `bash` call gets a disposable writable view. After it, the supervisor stops writers, detaches the view, validates the full delta, and imports only attributable output into a new sealed generation. Publication to the live workspace happens only through a separately validated path. A matching final tree does not prove that no transient effect occurred.
- Denying a proxy proposal does not by itself prove that every undeclared Pi or runtime path was contained; the configured surface and the Linux boundary need separate evidence.

These are intended contracts. They are not guarantees, and no WP1/G1 acceptance is claimed. The normative thesis documents are kept in a separate repository and are not vendored here.

## Status and gate

- WP1/G1 has not been accepted. Containment is `not-established`. Production Pi routes refuse to launch, and the `--native-fixture` route is synthetic. Fixtures, component tests, and synthetic routes are not thesis evidence.
- Opt-in provider tests are one-shot, non-production exchanges. They do not establish a production multi-turn workflow or provider authorization.
- Host, runtime, provider/model, and profile choices remain open; see the [guest profile prerequisites](docs/guest-profile-prerequisites-2026-10-06.md) and the [readiness report](docs/tbound-readiness-status-2026-10-08.md).
- Further detail: [implementation status](docs/implementation-status.md), [publication contract](docs/publication-contract.md), [correlation failure modes](supervisor/CORRELATION_FAILURE_MODES.md), and [private Git staging](docs/private-git-staging.md).

## Source map

- `adapter/src/`: Pi SDK host and fixture code, proxy tools, IPC transport, closure probe, and tests.
- `adapter/native-pi-policy/`: pinned-source policy fixture artifact and its patching metadata.
- `supervisor/internal/`: Go governance, broker, runtime, storage, publication, and companion components.
- `supervisor/cmd/`: CLI and doctor/provider/governance/web command packages; `cmd/tbound` includes the refusal path and explicit fixture route.
- `scripts/`, `install/`, `packaging/`: build, install, verification, and release assets.

## Checks

`make test` is retained as the existing narrow Pi-bootstrap and doctor test target. `make check-go` runs Go tests across `supervisor/`. Real-provider tests are gated and skip unless opted in. The Linux sandbox tests, including `TestNoNetwork` in `supervisor/internal/sandbox/sandbox_linux_test.go`, are local sandbox network-isolation tests: they attempt blocked outbound connections inside the sandbox and are not provider exchanges. They are not gated by `RUN_PROVIDER_TESTS`.

To opt in to real-provider tests, run `make RUN_PROVIDER_TESTS=1 check-go` and supply the OpenRouter credentials through `TBOUND_OPENROUTER_API_KEY` or `TBOUND_OPENROUTER_API_KEY_FILE`. The durable integration also requires `TBOUND_OPENROUTER_MODEL`. Opted-in tests can make network requests. Platform, privilege, and fixture precondition tests can also skip. Use `go test -v` to see test-level skip reasons; a passing summary does not mean every test ran.

`make check-adapter` runs the adapter's `npm run typecheck`, its `npm test` suite, and the native host, native policy, and SDK-worker tests. It uses installed dependencies, does not install packages, and does not write artifacts. The closure probe is separate: `make check-closure` runs `npm run closure`, which overwrites `adapter/artifacts/pi-closure-report.json`. `make test-all` runs `check-go` and `check-adapter`. Adapter checks and synthetic fixtures do not establish a production runtime integration.
