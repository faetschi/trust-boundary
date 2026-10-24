# Providers

**Status:** component contracts and profile notes. This page merges the former
`provider-profiles.md` and `provider-conversation-bridge.md`. It does not
certify a production provider exchange, Pi composition, G1, containment, or H1.
The engineering route is OpenCode Go; the provider-dependent H1 pilot uses the
frozen OpenRouter research route (see `../TASK_STATE.md`). Durable evidence for
the 2026-10-05 OpenRouter exchange is in
[provider durable evidence](provider-durable-evidence.md).

## Provider profiles

**Reviewed:** 2026-10-03. This note records verified provider contracts and the planned trust boundary. It does not certify a live provider exchange.

### OpenRouter profile

OpenRouter documents `POST https://openrouter.ai/api/v1/chat/completions`, an API key in `Authorization: Bearer`, a Chat Completions `messages` body, and both streaming and non-streaming operation. Its streaming guide describes SSE delivery. See the [Chat Completions API](https://openrouter.ai/docs/api/api-reference/chat/create-a-chat-completion) and [streaming quickstart](https://openrouter.ai/docs/cookbook/get-started/quickstart).

The broker builds the fixed-profile request and executes it through the OpenRouter transport. The development-only 2026-10-05 exchange is recorded in [provider-durable-evidence](provider-durable-evidence.md); it does not establish Pi composition or production qualification.

### OpenCode Go profile

OpenCode documents Go as a subscription with an API key and publishes direct API endpoints for its models. Its guide lists Pi as a validated client, while saying validated clients are not guaranteed to keep working indefinitely. Go asks other coding agents to send typical coding-agent traffic, identify themselves with their own `User-Agent`, and include a stable per-conversation `x-opencode-session`; traffic is monitored for abuse. See the [OpenCode Go guide](https://opencode.ai/docs/go/).

Go models use different API protocols. The guide lists `/v1/chat/completions` with `@ai-sdk/openai-compatible` for some models, `/v1/responses` with `@ai-sdk/openai` for others, and `/v1/messages` with `@ai-sdk/anthropic` for others. It also publishes `/zen/go/v1/models` for model metadata. The `opencode-go/<model-id>` form is OpenCode's config name; direct API calls use the model ID from the endpoint table. The model list can change.

A later Go profile must be separate and fixed to the chosen model's documented endpoint and protocol. The existing Chat Completions request builder cannot be treated as a Responses or Messages client. The `@ai-sdk/openai-compatible` label identifies the documented API family; it does not establish that this broker's request options or SSE parser work with every Go model. No exact Go model is selected, and no Go SSE compatibility is claimed. Verify the selected route and model before reporting a successful provider test.

OpenCode's Go guide says to obtain an API key and gives endpoint URLs, but does not explicitly specify the raw HTTP authorization header. The [AI SDK OpenAI-compatible provider docs](https://ai-sdk.dev/providers/openai-compatible-providers) say that the SDK's `apiKey` option sends `Authorization: Bearer <apiKey>`. Applying that convention to a direct Go request is an inference, not an explicit guarantee in the Go guide.

Go usage limits are model-specific and change; check the current guide before planning a test.

### Broker and evidence constraints

- A finite trusted broker/operator-owned approved profile selects provider, endpoint, protocol, and exact model ID. Pi may submit a logical request, but cannot choose a URL, host, provider, model, or arbitrary upstream headers.
- Committed OpenRouter `LoadCredentials` requires `TBOUND_OPENROUTER_MODEL` and accepts the API key from `TBOUND_OPENROUTER_API_KEY` or `TBOUND_OPENROUTER_API_KEY_FILE`; `NewHTTPDoer` passes the key to the transport through a closure, and the captured request record does not include its Authorization header. These code and development-evidence facts do not establish production secret provisioning or Pi/runtime wiring. Provider keys must never enter Pi, its tool cell, transcripts, prompts, request bodies, audit records, or logs.
- Keep the exact validated request body separate from credentials. Treat the profile ID, exact model ID, runtime/build versions, `User-Agent`, and `x-opencode-session` as non-secret request metadata. Freeze the selected model and runtime in the evaluation profile before a trial; keep the Go session ID stable for that conversation.
- OpenRouter and OpenCode Go calls reach hosted services. Report them as hosted-provider tests. An offline verifier run or a self-hosted test does not establish a hosted provider exchange, and a hosted exchange does not establish an offline run.
## Conversation bridge (2026-10-08)


**Status:** concrete Go/TypeScript component contract and offline component
implementation. This document does not claim production launch integration,
provider execution, G1, containment, or thesis acceptance.

Source basis for this increment: implementation predecessor
`8d2e47833030b8c723049006252a24d2d6712be0`; current docs baseline
`640cf30f98efd6a2dd0e82172caed7d4b3a34d7d`. Existing broker/OpenRouter,
native host, launcher, session/CLI, private-Git, policy, and other worker-owned
files are read-only for this increment.

### Ownership and flow

`supervisor/internal/providerbridge` is the trusted conversation owner. It loads
the already-reviewed OpenRouter secure credential object in Go, fixes model
`nvidia/nemotron-3.5-lightning:free`, and uses the existing Go broker,
`openrouter.BuildRequest`, bounded SSE parser, fixed four-tool manifest, and
redirect-forbidding HTTP transport. No API key, endpoint, model selector,
headers, message history, or tool result is sent from Node to the provider. This is the engineering/test channel pinned by `ApprovedModel`. The research evaluation model (DeepSeek V4.1 Flash, `docs/decisions/2026-10-14-qualification-and-research-procedure.md`) is not yet in code and is a candidate until frozen in the experiment manifest.

The Go owner maintains the canonical system/developer/user/assistant/tool
transcript and the current sealed workspace generation. A trusted host admits a
bounded user task by calling `Conversation.AdmitPrompt` **before** submitting
that same prompt to Pi. Pi's `Provider.stream` receives the SDK transcript but
does not serialize or forward it; it issues only a sequence/request-ID `next`
frame on the inherited provider channel. The Go owner constructs the request
from its own transcript, performs one HTTP exchange, registers the captured
response/tool-call with its one-use correlation stream, durably appends the
exact exchange record, and only then returns a bounded turn projection.

Pi converts that projection to the public `AssistantMessageEventStream` events
and executes the existing four proxy tools over the existing proposal IPC. The
Go owner implements the `piruntime.Broker` and
`piruntime.DurableDecisionRecorder` contracts. An owner-supplied executor
wrapper delegates to the existing trusted executor, requires its bounded result
to carry the durable generation/transition summary, appends the exact
`protocol.Result` and correlation/generation bindings to the audit journal, and
only then returns the executor result to `piruntime.Supervisor` for IPC release.
DENY results are journaled before the Supervisor sends them. Journal failure,
missing executor provenance, stale generation, malformed/replayed tool result,
or cancellation closes the provider conversation and withholds further
provider turns/results.

The separate inherited provider channel is length-prefixed strict JSON. It
accepts only `next` and cancellation frames; no prompt, transcript, provider
configuration, credential, result, or model-supplied metadata is accepted on
that channel. One turn is in flight per conversation. Channel EOF/error cancels
the active HTTP request. This is a host-created local capability, not an
Internet listener or an authentication claim by itself.

The Pi AI event conversion is exact and public: each Go turn emits `start`,
then optional `text_start/text_delta/text_end`, then optional
`toolcall_start/toolcall_delta/toolcall_end`, and finally `done`. The final
reason is `toolUse` only for a registered captured call, otherwise `stop`.
The assistant message preserves the captured response ID/model, usage summary,
and bounded text/call. Node does not reconstruct provider context from SDK
history. The complete raw response/request, ID issuers, argument digest, and
generation binding remain in the Go owner/audit event, not in caller-controlled
SDK fields.

### Runtime integration API

The concrete exports are `providerbridge.NewFromCredentials`,
`Conversation.AdmitPrompt`, `Conversation.NextTurn`, `Conversation.Correlate`,
`Conversation.RecordDecision`, `providerbridge.NewExecutor`, and
`providerbridge.Serve`. The host composition root should:

```go
profile := providerbridge.Config{
    ConversationID: conversationID, WorkflowID: workflowID, ProfileID: profileID,
    InitialGenerationID: generationID, InitialTreeDigest: treeDigest,
    SystemPrompt: registeredSystemPrompt, DeveloperPrompt: registeredDeveloperPrompt,
}
conversation, err := providerbridge.NewFromEnvironment(profile, auditJournal)
// Or use NewFromCredentials(profile, secureCredentials, auditJournal).
if err != nil { return err }
if err := conversation.AdmitPrompt(taskID, boundedPrompt); err != nil { return err }
wrappedExecutor, err := providerbridge.NewExecutor(conversation, durableExecutor)
if err != nil { return err }
supervisor := &piruntime.Supervisor{
    IPC: proposalServer, Broker: conversation,
    Policy: frozenPolicy, Decisions: conversation, Executor: wrappedExecutor,
}
```

The actual method contracts are `AdmitPrompt(taskID, prompt) error`,
`NextTurn(ctx) (providerbridge.Turn, error)`, `Correlate(ctx, proposal)` and
`RecordDecision(ctx, proposal, decision) error`. `NewExecutor` returns a
`ResultingExecutor` with the existing `(ctx, proposal, gateDecision) -> JSON`
executor contract. `Serve(ctx, io.ReadWriteCloser, conversation)` services the
separate inherited provider FD. There is no public fake-transport constructor;
offline transport/journal seams are package-private tests.

1. create one protected `audit.Journal` and one conversation per fresh workflow;
2. obtain credentials through `openrouter.LoadCredentials` and call
   `NewFromCredentials` with the approved immutable profile and journal;
3. call `AdmitPrompt(taskID, prompt)` from trusted session-control before
   accepting/forwarding the matching prompt into Pi;
4. pass the Conversation as `piruntime.Supervisor.Broker` and
   `piruntime.Supervisor.Decisions`; wrap the already configured
   `piruntime.Executor` using `NewExecutor`;
5. serve `Serve(ctx, inheritedProviderConn, conversation)` over one dedicated
   inherited duplex FD; Node's `broker-provider.ts` uses that FD to obtain turns;
6. keep mutation/session publication and VM/profile admission in their
   existing owners. This bridge does not provide those authorities.

The proposed launch wire seam is a Unix socketpair created by the trusted Go
host. The child receives only the provider side as inherited **fd 3** (not an
environment variable or a network address). Go passes the peer as the
`io.ReadWriteCloser` to `Serve`; TypeScript calls
`createBrokerProviderFromFD(3)`, which wraps that inherited descriptor and
opens no socket. This FD passing still needs adoption by the separate CLI/native
launcher owner; no launcher file is changed here.

The request frame is exactly
`{version, sequence, kind, request_id}` where `kind` is `next` or `cancel`.
A response is exactly `{version, sequence, kind, request_id, turn}` for a
successful turn, or `{version, sequence, kind, request_id, error}` on failure.
`turn` contains only schema version, response ID, fixed model ID, provider
timestamp, current generation, ordered sequence, assistant text/finish reason,
bounded usage, optional single fixed-tool call, and the durable journal
sequence/hash. Length prefix is four-byte big endian and each JSON frame is
bounded to 1 MiB. Both sides reject out-of-order sequences; the Go owner also
rejects reused request IDs and multiple in-flight turns.

For the generation fixture, the durable executor's strict result envelope
supplies the post-operation `generation.id`, `generation.tree_digest`, and
`transition.id/sequence`; a mutation cannot be continued if these fields are
missing or the returned generation is stale. The recorded E05 sequence remains
`read(g0) -> edit(g1) -> Bash(g2) -> read(g2)`.
Here `edit(g1)` and `Bash(g2)` name their resulting sealed generations: edit is
correlated against g0 and yields g1; Bash is correlated against g1 and yields
g2; the final read is correlated against g2.

### Limits and evidence boundary

The audit append waits for `Journal.Append`'s write+sync result before the
provider response or tool result is released. This is component ordering, not
an environment-independent power-loss guarantee, a signed journal, exactly-once
effect guarantee, or replacement for the existing trial ledger. The owner does
not reconstruct/replay an interrupted conversation; recovery creates a fresh
conversation only under the host's separately authorized workflow/recovery
rules. The existing `piruntime.Supervisor` and native host composition are not
modified here. Until those owners adopt this API, this package is an executable
integration component, not a `tbound serve --pi` product path.
It exposes the public Pi `Provider`/`AssistantMessageEventStream` contract, but
the existing native `InteractiveMode` host still does not construct this
provider or implement the required pre-prompt admission call.

Offline tests inject a deterministic Go `HTTPDoer`, exercise the existing
request builder/SSE parser and a real Pi SDK `AgentSession` provider/tool
continuation. The opt-in Linux test
`TestInheritedFDRealPiSDKGoOwnedE05CrossProcess` passes actual fd 3 across a
Node 24.21.0 child process, sends the four SDK proxy calls over existing local
proposal IPC, and checks the Go owner's `read(g0) -> edit(g1) -> Bash(g2) ->
read(g2)` result-history chain against its synced audit journal. Its HTTP Doer
and executor are explicit synthetic fixtures; it does not perform an OpenRouter
request, load credentials, access a guest, or qualify containment. The Go test journal/transport
adapter is an in-package seam only; exported `NewFromCredentials` and
`NewFromEnvironment` require the concrete protected `*audit.Journal` and use
the existing secure Go loader/transport. No public fake transport or journal
constructor is exposed.

The coordinator's earlier `provider-race.log` records a 30.06-second timeout
before IPC acceptance, while the Pi SDK test body itself took about 72.8 ms.
That log contains **no child exit status or stdout/stderr**, so the exact cause
of that original failure remains unconfirmed; it must not be retrospectively
reclassified as either a cold import or an early child exit. The test now
distinguishes early child exit (actual exit status plus captured stdout/stderr)
from startup timeout and retains bounded 64-KiB-per-stream logs in a private
`TMPDIR` on failure. It uses a test-only 90-second SDK-startup allowance and a
separate 30-second post-IPC E05 budget; production provider/session deadlines
are unchanged. A subsequent successful stage-instrumented run measured the Go
broker-provider import at 9.867 s, local IPC connection at 22.428 s, Pi
AgentSession ready at 22.445 s, and prompt completion at 22.561 s (whole test
22.65 s). That makes cold DrvFS module loading a demonstrated startup cost and
a plausible explanation for the earlier timeout, but does not prove its cause.
The earlier failure remains in the coordinator's retained log and is not
counted as a pass.
Later genuine testing requires separate operator approval, the secure loader,
the approved model, and bounded synthetic content.

### Source anchors

- Thesis provider ownership/result binding: `../../MSE_MA_Thesis/agentic-harness/AH-technical/11-evidence-contract.md`, E02 and E05 (last reviewed 2026-09-29, lines 36-53 and 164-174); `../../MSE_MA_Thesis/agentic-harness/AH-technical/02-process-model.md` §3 (lines 121-145); and `../../MSE_MA_Thesis/agentic-harness/AH-technical/03-architecture.md` §4 (lines 91-117).
- Existing Go implementation reused: `supervisor/internal/broker/broker.go`, `internal/broker/openrouter/openrouter.go` and `transport.go`, `internal/broker/protocol/protocol.go`, `internal/audit/audit.go`, and `internal/piruntime/supervisor.go`. No existing file in those packages is changed by this increment.
- Pinned Pi SDK event/provider surface checked read-only at Pi 0.87.1: `adapter/node_modules/@earendil-works/pi-ai/dist/models.d.ts` (`Provider`, lines 58-95), `types.d.ts` (`AssistantMessageEvent`, lines 353-523), and `utils/event-stream.d.ts` (lines 16-20).
- Frontend direction is in the [web chat workspace](../extensions/webchat/docs/web-chat.md). The retired `frontend-observability-plan.md` is retained in the [historical source map](evidence/historical-sources.json). This bridge exports the public Pi provider stream; launch and TUI composition remain owned by the native-host/runtime workstream.
