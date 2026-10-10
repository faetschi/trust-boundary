# Recommended plan: native Pi harness inside tbound

## Architecture decision — 2026-10-06

**Actual Pi inside tbound—not a replacement Pi client.** Inspection of pinned
Pi 0.87.1 confirms that its native `InteractiveMode` is publicly exposed.
Constructing Pi's existing harness and TUI through its APIs is integration,
not rebuilding its chat or harness from scratch. The unrestricted stock CLI
alone does not expose enough configuration to establish tbound governance.

Target shape (proposed command, not an implemented governed-launch claim):

```text
tbound serve --pi
├─ actual Pi harness + native Pi TUI
├─ tbound-governed provider/tool/lifecycle integration
└─ separate tbound governance interface
```

Keep Pi's native terminal experience. Initially show tbound's decisions,
evidence and publication status in a companion web view. A terminal governance
pane can follow; literally nesting two TUIs adds terminal-handling complexity
without strengthening governance. Structured observation events, not terminal
output, link Pi activity to authoritative supervisor records.

Extensions/additions remain part of the target, subject to admitted profiles.
Every effect-producing route—including direct user shell actions—must be
governed or explicitly refused. Simply launching `pi` as a child is insufficient.
Provider credentials and execution/publication authority remain outside Pi.
Public API feasibility does not establish surface closure, containment or G1;
production admission remains refused until the required checks are proven.

This decision supersedes the replacement web-chat recommendation below. The
existing SDK/browser chat is fixture and integration-test evidence, not the
final Pi client. Retain its control/security/event work for the shared backend
and companion governance viewer; do not duplicate supervisor authority.

## Earlier web-chat plan (historical; presentation choice superseded)

The following records the earlier slice and its reusable contracts. References
to browser chat as the primary interface are not the current product direction.

## Goal

Build a live web presentation layer for **a real Pi SDK session governed by
tbound**. Users request session launch, submit a task, interact with Pi's chat,
and observe the surrounding governance in the same interface.

Pi remains the agent harness; only its presentation changes. The supervisor
retains authorization, execution, staging, recovery, and evidence authority.
The required CLI remains available through the same supervisor API.

## Technology

- Extend the existing Go `cmd/tbound-web` server.
- Embed locally built HTML/CSS and a small TypeScript browser client; no separate
  production Node web server or frontend framework is needed for this slice.
- Use authenticated HTTP requests for launch, bounded task input, and stop.
- Use SSE for live chat, tool activity, governance observations, and test results.
- Use the pinned Pi 0.87.1 SDK in the isolated worker, with the existing locked
  resource loader and four proxy tools: `read`, `write`, `edit`, `bash`.
- Route model access exclusively through the trusted provider broker. The
  browser and Pi worker must not receive provider credentials.

SDK message subscriptions support streamed text and tool/session activity.
The restricted session/broker/control composition still needs implementation
and verification; SDK support alone does not establish a governed session.

## Why not embed the stock TUI immediately?

Pi's stock TUI includes interactive actions beyond its model-visible tools:
`!`/`!!` user shell execution, model/login changes, reload, and import/share
operations. In the inspected Pi 0.87.1 implementation, interactive Bash reaches
`session.executeBash` separately from the registered proxy `bash` tool.
Replacing the four tools therefore does not automatically close these paths.

Embedding that TUI unchanged would require additional surface-closure work,
plus PTY lifecycle, terminal escape-sequence handling, resize, bidirectional
transport, and backpressure management. Terminal output also lacks the structured
identifiers needed to link chat actions to governance records reliably.

The recommended SDK-backed chat exposes a narrow set of admitted interactions
and structured events while leaving the actual agent behavior in Pi. This does
not make the SDK worker trusted: broker correlation, restricted configuration,
containment, and independent evidence remain necessary.

## Why NOT the alternative

These exclusions explain the recommendation; they are not competing delivery plans.

- **Stock Pi RPC passed directly to the browser:** its command surface includes
  direct Bash and model/configuration operations. Stock RPC launch also does not
  automatically install tbound's locked SDK adapter. A restricted SDK bridge
  reuses the integration already present without exposing the entire RPC API.
- **A terminal emulator around an unrestricted Pi process:** reproduces Pi's
  presentation but does not establish governed launch, provider lineage, tool
  closure, or safe execution. A visual wrapper is not an authority boundary.
- **Static HTML or a standalone test dashboard:** cannot provide the requested
  interactive Pi session and live governance context. Retained replay is a mode
  of the same live web application, not a substitute for it.
- **A separate browser-owned agent/provider connection:** creates an execution
  or conversation path outside the supervisor. All admitted input and model
  exchange must remain in the governed session.
- **A new frontend framework, Node web service, or telemetry stack initially:**
  adds deployment/dependency work without resolving the missing session bridge
  and event contracts. The existing Go server and small typed client suffice.

## What still needs implementation

The inspected prototype has a raw read-only Go/SSE viewer, Pi closure probe,
proxy tools/IPC, tested supervisor components, evidence reconstruction, and a
separate one-shot provider exchange. These are building blocks, not the complete
integrated launch/chat experience. Before implementation, recheck current source
and recorded artifacts rather than treating this planning snapshot as live status.

1. **Shared supervisor session-control API:** authenticated launch, bounded task
   admission, status and stop, with the same authority checks for CLI and browser.
2. **Production-shaped Pi SDK worker:** locked services/resources, broker-backed
   model access, four proxy tools, subscribed chat/tool events, and no exposed
   direct user-Bash or configuration paths.
3. **Broker/session composition:** admitted task and multi-turn conversation
   lineage, exact tool-result binding, durable session admission, and runner-owned
   process/cell lifecycle. The one-shot exchange does not establish this chain.
4. **Live observation projection:** typed, correlated Pi/supervisor/observer
   events, in-flight state, bounded output, and supported test reporters. Current
   post-result transcripts and captured output are not full progress streams.
5. **Browser workspace and reliable history:** chat rendering, linked governance
   details, generation/test views, snapshots, SSE resume/deduplication/gaps,
   source health and clearly labeled retained replay.
6. **Access and validation:** local authentication, Host/Origin/CSRF controls,
   limits, sanitization, cancellation/disconnection tests and real browser E2E
   checks before exposing mutation controls.
7. **Governed-launch prerequisites:** resolve and verify the declared closure,
   containment, frozen-profile and cleanup gaps before enabling a launch labeled
   governed. Until then, development fixtures remain explicit and non-claim-bearing.

Live publication is separate core work, not a browser feature. If unavailable,
the interface must show staged changes and unsupported publication honestly.

## Architecture

```text
Browser: launch / task input / stop
                 │ authenticated supervisor API
                 ▼
       tbound supervisor + host-owned runner
                 │ validates profile and preflight; owns lifecycle
                 ▼
       Isolated Pi SDK worker
        ├── locked resources and four proxy tools → tbound IPC
        └── model access → trusted provider broker

Pi chat/activity ───────────────┐
tbound decisions/execution ─────┼── bounded presentation projection
Independent effect evidence ───┘              │
                                       tbound-web / SSE
                                             ▼
                                   live session workspace
```

The browser requests operations; it does not directly spawn Pi, execute shell
commands, operate containers/VMs, or call the provider. Session-control requests
use a separate channel from tool-proposal IPC. A user prompt is not an execution
lease and must enter the registered broker-owned conversation lineage.

## Interface

The primary screen contains:

1. **Session header:** selected registered profile, preflight results, source and
   environment identity, session status, launch/stop, and LIVE/REPLAY mode.
2. **Pi chat:** task input, streamed assistant messages, tool requests and returned
   results. Pi-reported activity is attributed separately from trusted evidence.
3. **Governance timeline:** broker match, ALLOW/DENY and reason, durable authority,
   execution/settlement, generation transition, terminal outcome and result release.
4. **Governed state:** current sealed generation, approved path/hash/mode changes,
   command-cell status, observer coverage, unresolved evidence and quarantine.
5. **Tests/output:** bounded action output and supported structured test results.

Selecting a tool request highlights its correlated decision, execution, generation
change and evidence. Pending, denied, failed, withheld, UNKNOWN and completed
states remain distinct. Normal compliant operations proceed automatically;
there is no browser approval button that overrides a strict denial.

Project tests requested by Pi and developer/conformance tests of tbound are
separate contexts. An exit code is not individual test-case evidence. Textual
diffs require retained file contents; manifests alone support metadata/hash diffs.

## Authority and access rules

- Clients cannot modify policy/profiles, alter audit evidence, write the live
  workspace, bypass containment, or turn DENY into ALLOW.
- Expose only explicitly admitted session commands, not raw Pi SDK methods.
  Do not expose direct user Bash, configuration reload, login/model changes,
  arbitrary filesystem attachments, or client-selected provider endpoints.
- Start loopback-only. Before enabling mutation requests, implement local session
  authentication, Host/Origin validation, CSRF protection, request limits, and
  session ownership checks. Remote access is outside the first slice.
- Bound events, connections, replay, output and rendering. Slow/disconnected
  browsers must not stall supervised execution or authoritative audit.
- Mark diagnostic observations separately from durable confirmations. A visible
  JSONL line or Pi completion event does not prove durable supervisor completion.
- Keep original evidence protected. Present allowlisted data and registered
  artifact references; never expose arbitrary paths, credentials or environment dumps.
- Reuse `audit.Verify` and Linux `sessionrepo.Reconstruct`; do not reimplement
  verdicts or evidence classification in the browser. Structural verification
  does not establish external authenticity, containment or G1.
- Respect disconnected-VM trial boundaries: do not enable networking for UI access.
  Windows viewing of such trials requires retained replay after reconnect unless
  a separate permitted observation channel has been evaluated.

## Delivery sequence

### 1. Define contracts and E2E failure cases

Specify session-control commands, launch refusal, task admission, event schemas,
correlation references, redaction, source identity and live/replay semantics.
Keep provider IDs, decision/effect IDs and generation IDs in their namespaces.

**Acceptance:** fixtures cover unauthorized control, invalid launch, malformed
input, correlation failure, denial, cancellation, UNKNOWN and missing evidence.

### 2. Prove the restricted Pi SDK bridge

Construct the explicitly configured Pi worker and supervisor-owned control path.
Initially accept one bounded plain-text task after preflight. Subscribe before
prompting; disable unapproved command/template expansion. Use a fake provider
to verify streamed chat and the governed four-tool proposal/result path first.

Stop closes admission and invokes runner-owned cancellation/cleanup. A Pi abort
or browser disconnect must not be mistaken for command-cell settlement.

**Acceptance:** real SDK events and correlated proxy calls are observed; forbidden
direct operations are rejected; client disconnect does not orphan ownership or
replay effects. Synthetic/provider-fixture status is explicit.

### 3. Build the live web session workspace

Implement the chat, linked governance timeline, generation summary and test/output
panel. Introduce bounded in-flight diagnostic observations without changing
pre-effect audit ordering or making browser delivery a prerequisite for execution.

Use snapshots plus SSE cursors/source epochs, deduplication, historical loading,
explicit replay gaps and source-health indicators. Retain history outside the
short in-memory event buffer.

**Acceptance:** updates appear without reload; selecting a request links its
governance chain; reconnect preserves known history or explicitly reports gaps;
viewer failure does not change effects or audit. Browser layout, accessibility
and hostile-content rendering are tested in an actual browser.

### 4. Connect and validate a real broker-mediated Pi session

Complete provider conversation/result lineage, durable session admission and
runner composition. Launch through the same authorized API from CLI and browser.
Validate one registered task with real Pi messages and governed tool operations.

Full governed admission requires the declared closure/containment/profile checks;
failed checks refuse launch rather than silently falling back. Current prototype
gaps must be resolved before claiming this milestone. A one-shot provider exchange
or a fixture-driven UI does not substitute for it.

**Acceptance:** launch → task → streamed Pi activity → correlated decisions →
settled effects/generations → durable outcome is demonstrated with retained source
identities. Unsupported publication or claim states remain explicit.

### 5. Reproducible interactive demo mode

Replay retained, sanitized event streams through the **same web interface**, with
pause/step/play controls, source commitments and visible verification scope.
Replay never executes tools, launches sessions or contacts providers.

**Acceptance:** the same retained inputs yield the same projected outcomes;
unknown, missing and tampered evidence cannot become success. Live and replay
modes are unmistakable.

## First-slice boundary

The initial implementation covers steps 1–3: restricted SDK bridge, local control
boundary, live web chat and linked governance, validated with explicit synthetic
sources. Step 4 is required for the complete real governed-Pi experience and
must not be represented as already delivered by that first slice.

Keep raw terminal embedding, arbitrary RPC commands, remote/multi-tenant service,
policy editing, publication implementation and advanced recovery controls outside
this presentation slice. Task steering and follow-ups require separately validated
conversation lineage and budget semantics before exposure.

## Thesis alignment

Normative authority remains in `MSE_MA_Thesis/agentic-harness/`:

- `tbound-thesis.md` §5: untrusted presentation clients use the supervisor API;
  the CLI remains required, and authority stays external to Pi and clients.
- `AH-technical/03-architecture.md` §§2–4: runtime topology, four proxy tools,
  broker correlation, and interchangeable presentation clients.
- `AH-technical/02-process-model.md` and `02-1-recovery-and-continuation.md`:
  authorization, durable outcomes, lifecycle and recovery semantics.
- `AH-technical/06-1-language-decision.md`: presentation is separate from the
  supervisor; browser authentication/session management needs evaluation.
- `AH-technical/11-evidence-contract.md`: evidence identity, coverage and
  verification boundaries.

This is an optional live demonstrator/client, not a change to H1 or its gates.
