# Frontend product agent handoff

Paste the instruction below into a new agent thread. Select a **new worktree**
based on `codex/tbound-prototype`, with branch `codex/tbound-web-chat`. Keep the
core implementation thread on `codex/tbound-prototype`. Do not share checkouts.

This plan incorporates the final findings from
[Frontend Existing Companion Review](t3-thread://v1/thread:provider:codex:native-thread:01a1369f-2715-79e1-aaac-10b6d7462593).
That review was read-only; it did not implement or test the proposed chat service.

## Instruction for the new thread

Implement the **frontend product side of tbound** in this thread. The other
thread owns the core priorities in [TASK_STATE.md](../TASK_STATE.md) and
[status.md](status.md), containment/profile qualification, governed lifecycle and
recovery, publication evidence and research claims. Coordinate API needs with that
thread; do not duplicate its work.

### Product goal

Build a new web chat interface for the official Pi CLI/RPC harness, wrapped by
tbound governance. Users submit prompts, read assistant replies, see tool
activity and understand tbound's policy decisions, execution results, provenance,
publication and recovery status in the same workspace. This is an interactive
Pi product frontend, not just a read-only governance dashboard.

Design the presentation **from scratch**, with an entirely different appearance
from the existing governance companion. OpenChamber's relationship to OpenCode
is a reference for the product concept, not a visual template or backend to copy.
Use judgment to choose a small maintainable stack consistent with the repo.

### Start with evidence

Read `AGENTS.md`, `TASK_STATE.md` and the relevant thesis documents under
`../MSE_MA_Thesis/agentic-harness/AH-technical`: `00-roadmap.md`,
`03-architecture.md`, `07-1-runtime-flow.md`, and the runtime/language decision.
Inspect the existing authenticated Pi service and its event/result contracts.
Use read-only GPT-6 Luna max agents for focused exploration and critical review;
keep the main agent's context lean and apply changes in the owning thread.

The current service is **one prompt / one terminal receipt** over an authenticated
local Unix socket. It does not yet expose browser authentication, persistent
multi-turn sessions, history, cancellation or transcript streaming. Pi event
processing currently discards most transcript/tool events. Do not present those
capabilities as already implemented.

The review's integration sequence is **persistent multi-turn service and
cancellation -> authenticated snapshot/history/event streaming -> live web chat**.
Contract work and explicitly labelled mock UI can proceed in parallel. A typed
post-run receipt importer can provide an intermediate demo; the existing fixture
loader cannot import the actual development receipt, and that receipt supplies
no G1/H1 qualification fields. Those require separate authoritative provenance.

Useful source locations:

- `supervisor/cmd/tbound/pi_service_handoff_linux.go`
- `supervisor/cmd/tbound/production_session_linux.go`
- `supervisor/internal/piruntime/pi_rpc.go`
- `supervisor/internal/piruntime/worker_channels_linux.go`
- `adapter/src/tbound-cli-extension.ts`

### Milestones

1. **Contract and design:** document the bounded session/turn/message/activity
   projection and missing authority-service APIs. Build the new chat workspace
   with explicit fixture data and unavailable/live/replay labels. Validate desktop
   and mobile layouts, keyboard access, empty/error/UNKNOWN states and reconnect
   gaps. Keep fixtures separate from real run evidence.
2. **Read integration:** consume sanitized typed snapshots and cursor-based
   events from a service-owned projection. Show transcript, tool activity,
   governance decisions and recovery. Distinguish policy ALLOW, successful
   execution, publication and production qualification. Reconnect must recover
   from a snapshot; duplicate/gapped events must not invent success.
3. **Interactive integration:** submit turns and cancellation through the existing
   authenticated supervisor authority. Coordinate persistent sessions and the
   required service APIs with the core thread before editing shared service files.
   Implement browser authentication, session ownership and CSRF/origin protection
   appropriate to the deployment. Do not expose an unauthenticated remote service.
4. **Product delivery:** useful onboarding and configuration errors, setup/run
   documentation, meaningful integration checks, visual QA and a reproducible
   demo. Commit reviewed increments and open a PR into `codex/tbound-prototype`.

### Authority and claim boundaries

- The browser/gateway is an untrusted client. The Go supervisor retains admission,
  policy, leases, effect execution, durable journals and publication authority.
  Do not connect the browser directly to Pi RPC, execute tools in a gateway, or
  add manual per-tool approvals, policy bypasses or alternate execution routes.
- Cancellation is a request to the supervisor; show its settled result. Uncertain
  effects/cleanup remain UNKNOWN. Do not retry effects automatically on reconnect.
- Project only allowlisted bounded fields. Do not serve raw journals, credentials,
  provider payloads, arbitrary paths or unrestricted tool arguments/output.
  The projection should include session/turn IDs, sequence, transcript text,
  sanitized tool summaries, decision reason codes, result states, source/profile
  and generation status, timestamps, and explicit gap/unknown states. Publish
  observations after the corresponding authoritative durable transition.
- Current status: G1=false, H1 unexecuted; no qualified public production launch.
  Development fixtures remain non-claim-bearing. Green UI/tests do not change this.
- Research trial counts/registration are owned by the core/thesis thread.

### Reuse and coordination

Draft installer PR [#10](https://github.com/faetschi/trust-boundary/pull/10) is
blocked by installer security issues; do not merge it as a frontend dependency.
The original installer worktree is untouched.

Existing typed viewer preparation is isolated in sibling
`../trust-boundary-frontend`, branch `codex/tbound-governance-companion`, with
uncommitted files. Reuse its contract/validation/store ideas when useful; replace
the visual design. Its review identified unverified replay success presentation,
SSE deadlines cleared before flush, and unbounded GET-body handling. Fix and test
those before adopting the affected code. Do not overwrite that checkout; use the
checked-in [task state](../TASK_STATE.md) and [current priorities](status.md). Preserve
historical binaries and avoid credential files.

Keep changes in your new worktree. Propose a small API contract to the core thread
before modifying authority-owned files; prefer additive client/projection files.
Report milestones, blockers and exact tested scope concisely. Run required pinned
Linux/WSL Go 1.27.1 checks per AGENTS for Go changes and suitable frontend checks.
Inspect the running UI with the collaborative browser; retain screenshots and
source/command/exit-code receipts. Request a critical Luna max review between
milestones and address its findings. Link any PR you create to your thread.

Reference patterns from the review: [OpenChamber client](https://github.com/openchamber/openchamber/blob/main/packages/ui/src/lib/opencode/client.ts),
[sync model](https://github.com/openchamber/openchamber/blob/main/packages/ui/src/sync/DOCUMENTATION.md),
and [OpenCode API](https://dev.opencode.ai/v2/docs/api). Verify current upstream
documentation before using it. OpenCode APIs are not tbound APIs.
