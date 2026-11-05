# Web chat: showing the model's reasoning (proposal)

**Status: proposal. Nothing here is implemented.** Product work only. It makes no thesis
claim, does not touch the H1 experiment, and changes no frozen thesis behavior (section 4).
Written against `codex/tbound-prototype` at `a47661ac` (2026-11-05). File and line references
are to that commit.

Related: [web-chat-contract.md](web-chat-contract.md) (the projection and section 9, live
mode), [web-chat.md](web-chat.md) (claim boundary), `docs/product-contracts.md` (Milestone 6,
the product protocol).

## 1. Status quo: the chat has nothing to show

The projection has four item types and no reasoning record
(`frontend/src/core/contract.ts:31`, `internal/webchat/contract.go:88-91`). In live mode one
product-protocol event becomes one item (`internal/webchat/live_render.go:10-22`). There is
no reasoning event kind (`supervisor/internal/productprotocol/protocol.go:40-50`). Evidence
of what is dropped, from the model call up to the browser:

| Layer | What happens to reasoning | Where |
|---|---|---|
| Request to the provider | Thinking is switched off on the engineering route: `"thinking":{"type":"disabled"}` is a fixed member of the request. The provider profile states `thinking=disabled`. | `supervisor/internal/broker/openrouter/openrouter.go:165,226`; `supervisor/internal/productprovider/profiles.go:50` |
| Response parser | `reasoning`, `reasoning_content` and `reasoning_details` delta fields are accepted and ignored. A test pins this: some real models stream them even then. | `openrouter.go:752-756` (also `:726` for the post-finish trailer); `openrouter_test.go:205-223` |
| Durable record | The raw SSE response of each model call is stored in the provider journal (`provider_exchange`, bounded by `MaxDurableExchangeBytes` = 3 MiB). So any reasoning the provider did stream is in the journal bytes, and is not parsed. | `supervisor/internal/broker/broker.go:32`; `supervisor/cmd/tbound/product_service_events_linux.go:260-275` |
| Product event | `projectProviderEvents` calls `CaptureSSE` and emits `assistant_message` from `AssistantText()` only. | `product_service_events_linux.go:271-273`; `openrouter.go:438` |
| Model history | The next turn's history uses assistant text only, so reasoning never feeds back to the model. | `supervisor/internal/providerbridge/conversation.go:626` |
| Pi RPC (real Pi worker) | The worker reads every Pi RPC record but acts only on `agent_settled` and failed `prompt` responses. Any other record type, including streamed message updates, is read and discarded. | `supervisor/internal/piruntime/worker_channels_linux.go:1660-1682`; `pi_rpc.go` |
| Gateway | An event kind it does not know becomes a system message `Unmapped event "..."`. No kind list exists on either side of the wire. | `live_render.go:187`; `wire.go:46` |
| Browser | A served item whose `type` is not in `ITEM_TYPES` fails validation of the whole snapshot. | `frontend/src/core/validate.ts:289` |

Two facts shape everything below.

1. **The product turn path does not run Pi yet.** The worker is the supervisor's in-process
   loop, which calls the broker directly; `pi` is refused by the service
   (`docs/product-status.md:21,93,280`). So "Pi RPC event to supervisor" is not the path that
   serves the chat today. The one source both worker kinds share is the **provider journal**.
2. **Whether reasoning exists at all is unmeasured.** With `thinking` disabled, the provider
   (`deepseek-v4.1-flash` on OpenCode Go) may stream none. No capture in this repo was
   checked for non-empty reasoning deltas. Unit 0 below measures it before any code lands.

Who is working on this: nobody. No open PR, no remote branch and no `.local` note mentions
reasoning for the chat (details in the PR description). Grep hits for "reasoning" in
`supervisor/` are the OpenRouter research profile request control and the delta-ignore code
above. They are about requesting reasoning, not displaying it.

## 2. Proposal

### 2.1 A new item type, not a message role

Add item type **`reasoning`**. A message role (`user`/`assistant`/`system`) would reuse the
message validator, transcript counts, filters and "assistant said" readings, so reasoning
could be read as something the assistant said to the user. A separate type is refused
loudly by any validator that does not know it, and every view must choose to show it.

| Field | Rule |
|---|---|
| common fields | `schema_version`, `sequence`, `id`, `turn_id`, `occurred_at`, `authority`, `verification_scope`, as every item |
| `text` | model-reported reasoning, bounded to `MaxTextBytes` (4096) after `cleanText` (valid UTF-8, control characters replaced, CR dropped) |
| `text_truncated` | true when cut. Text may be empty only when truncated (same rule as `message`) |
| other fields | forbidden: `role`, `tool`, `tool_call_id`, `verdict`, `reason_code`, `state`, `summary`, `detail`, `digest`, `lossy`, `policy_digest`, `kind` |
| `authority` | **`diagnostic` only**, in both validators with the same rule. Go and TypeScript refuse `observed`, `authoritative` and `unknown` for this type (`validate.go:38-40` lists all four values today) |
| `verification_scope` | must equal the snapshot's scope. The existing gate (`verification.ts` `scopeFault`) refuses any other value, so a live snapshot's reasoning item carries `structural-audit-only`. That label describes the snapshot, not the item: see 3.2 |

Order and grouping: one `reasoning` item per model call (per `provider_exchange`), placed
**before** that call's assistant message and tool calls, with the same short `turn_id`.
Inside one projection (section 2.3) the mapping stays one event to one item, so
`sequence = seq + 1`. Sequence numbers are only comparable inside one projection.

### 2.2 Where it is produced

```
provider journal (provider_exchange, raw SSE)           [unchanged, already durable]
  -> projectProviderEvents (cmd/tbound)                  NEW: second scan -> assistant_reasoning event
  -> productEventProjector.session                       NEW: filter by requested projection, THEN number
  -> product protocol observe response                   NEW event kind and request field, version stays v1
  -> gateway live_render.go eventItem                    NEW: assistant_reasoning -> reasoning item
  -> snapshot / events JSON                              validated by ValidateSnapshot
  -> frontend validate.ts -> store -> chat view          NEW type, collapsed row
```

- **Source is the journal only.** Do not plumb Pi RPC `message_update` records. Pi's stream
  is the worker's own relay, and the broker's capture is the record the supervisor already
  trusts. One source also works unchanged when a real Pi worker replaces the supervisor loop.
- **Extraction without touching the parser.** `CaptureSSE` stays byte-for-byte as is. In
  `projectProviderEvents`, after `CaptureSSE` has accepted the response, a small second pass
  reads the same bytes. It must follow the same event rules as the accepted parser:
  - **Assemble SSE events, not lines.** An event is the `data:` lines up to the blank line,
    joined with `\n` into one JSON value (`openrouter.go:521-563`). A JSON chunk may span
    several `data:` lines, so scanning line by line would miss or split it.
  - **Strings only.** The accepted parser checks the names of delta fields, not their value
    types (`openrouter.go:726,756`). Take `choices[0].delta.reasoning_content` (else
    `reasoning`) only when the value is a JSON string. Numbers, objects, arrays and `null` add
    nothing. `reasoning_details` is ignored (structured, may be opaque or encrypted).
  - Concatenate in stream order, cap at 16 KiB (`productEventMaxTextBytes`), cut on a rune
    boundary. An empty result emits no event.
  - **Failure is diagnostic only.** If the second pass cannot read a chunk, it emits no
    reasoning event for that call and records a service-side diagnostic. It never returns an
    error from `projectProviderEvents` and never changes, drops or reorders the
    `assistant_message`, `tool_call` or `tool_result` events that the existing path produces.

  The alternative, a new accessor on `CapturedResponse`, edits `openrouter.go`, which is
  shared with the research route and the frozen thesis build, so it is not preferred.
- **Not live typing.** The projection is built from durable records after a call ends
  ("durable before published", contract section 3.1). Reasoning therefore appears when the
  model call completes, not token by token.
- **Event.** New kind `assistant_reasoning` using the existing `Event.Text` field. No new
  struct field, so the strict wire decoders need no change beyond the kind constant.

### 2.3 Projection: filter before numbering

The first draft said default-off would drop reasoning events and leave numbering unchanged.
That cannot work, for four reasons found in review:

1. Event positions are the numbering. With events `[message, reasoning, message]` a filter
   after numbering leaves item sequences `[1,3]`, and `ValidateSnapshot` refuses a provided
   sequence that is not contiguous from 1 (`validate.go:551-558`).
2. The poll answer must pair `events[i]` with `items[i]`, have equal lengths, and have
   contiguous `seq` from the cursor (`frontend/src/live/wire.ts:169-176`; `checkEvents`,
   `live.go:594-604`).
3. The observe handler returns the raw `events` array next to the items (`live.go:562`), so
   hiding only the item would still send the reasoning text to the browser.
4. The snapshot cursor is `epoch:<item count>` (`live_render.go:260`), and the poller reads
   only the count (`poller.ts:32`).

So the filter runs **before** numbering, in the service, and the projection is negotiated:

- **Request.** `Observe` (and any other request whose answer depends on event positions) gains
  an optional `projection` string. Absent means `base`, which is today's list exactly: no
  reasoning event, positions assigned over that list. `reasoning-1` adds `assistant_reasoning`
  events, with positions assigned over the larger list. The service strict decoder refuses
  unknown fields (`productprotocol/protocol.go:282`), so a service that does not know the field
  fails loudly instead of ignoring it.
- **Where the filter sits.** `productEventProjector.session` builds the full list, drops events
  the requested projection excludes, and only then sets `Seq` (today `product_service_events_linux.go:59,73`).
  The per-turn cache of settled events (`settled`) must hold either the unfiltered events or be
  keyed by projection, so one projection cannot leak into the other.
- **Gateway.** The process picks one projection: `--show-reasoning` asks for `reasoning-1`,
  default asks for none (`base`). The snapshot and every poll of that process use the same
  one, so their cursors count the same list. Under `base`, a reasoning event from the service
  is a service fault (`BAD_SERVICE_RESPONSE`), not something to drop, because dropping at the
  gateway would break contiguity.
- **Off means absent.** Under `base` there is no reasoning payload in `events[]`, in `items[]`,
  or in any other field. This is tested on the raw response body, not only on item types.
- **Cursor scope.** Under `reasoning-1` the snapshot `epoch` and cursor prefix carry the
  projection id (for example `<session-id>.reasoning-1`); under `base` they stay as today. The
  browser keys its stored cursor by that epoch instead of the bare session id
  (`controller.ts:30,258-280`), and the poll names its projection; the gateway answers a poll
  whose projection is not the process's with a refusal that makes the page reload the
  snapshot. Whether the cursor `epoch` character set allows the dot is **not verified**: pick
  the separator in Unit 2.

### 2.4 Compatibility and deployment

| Surface | Change | Compatibility |
|---|---|---|
| Product protocol (`tbound-product-protocol/v1`) | one new `Kind` string, one optional `projection` request field | Additive for a caller that omits the field (it gets `base`). A caller that sends it to a service that lacks it is refused by the strict decoder. Neither side validates kinds, so the kind alone is never the guard: the request field is |
| Wire copy in the gateway (`wire.go`) | add the constant and the field | Parity test `TestProtocolFixturesRoundTripThroughTheWireCopy` needs observe fixtures for both projections, generated by `supervisor/internal/productprotocol/fixtures_test.go -update` |
| Observation contract (`tbound-webchat-observation/v1`) | one new item type, projection-scoped epoch | Breaking for a client that does not know the type (whole snapshot refused, section 1). The Go gateway embeds its own frontend build, so the pair ships as one binary. Under `base` the output is unchanged. A bump to `v2` is the stricter alternative: open question 3 |
| Go validator (`validate.go`) | `ItemReasoning` const, `validateItemType`, a `validateItem` case, a rule that allows only `diagnostic` | Fixture and replay documents stay valid (no such items) |
| Frontend (`core/contract.ts`, `core/validate.ts`) | `ITEM_TYPES`, `ReasoningItem`, `ITEM_KEYS.reasoning`, an `assertItem` case with the same authority rule as Go | Go and TypeScript must agree on a shared corpus (Unit 4). `core/differential.test.ts` compares the legacy JS modules with the TS ports, and `validate.live.test.ts` is TS only, so neither is evidence of Go agreement |
| Consumers of `Item` | `core/store.ts:345` (blocks), `core/trajectory.ts`, `core/filters.ts`, `app/views/trajectory/rail.ts`, `app/views/chat/Transcript.tsx` | Exhaustive switches fail to compile when the type is added. Each gets an explicit "reasoning is not a tool, not a decision, not a result" branch |
| Verification gate (`verification.ts`) | none | The item-only predicate `isItemEvidenceOnly` needs `authority === "observed"` (`verification.ts:66-68`), so a `diagnostic` item never passes. Views reach the gate only through `isVerifiedIn` (`:74`), and `app/verification.gate.test.ts` forbids `isItemEvidenceOnly`, `isVerified` and `judgedItem` under `src/app` while allowing `isVerifiedIn` |

**Existing sessions.** The service rebuilds a session's events from its retained journals on
every call and sets `Seq` to the index (`product_service_events_linux.go:59,73`). Gateway item
ids are `ev-<seq>` (`live_render.go:116`) and browsers persist a cursor count per session
(`controller.ts:258-280`). The extractor reads old journals too, so under `reasoning-1` it
would insert events before the historical assistant messages of sessions that already exist.
Every later id and count in those sessions shifts. Under `base` nothing shifts. The
projection-scoped epoch and storage key above are the reset mechanism: a browser that moves
from `base` to `reasoning-1` finds no stored cursor for the new epoch, starts from the
snapshot, and shows no "events since you left" count for that first load. Whether to accept
this for old sessions, or to apply reasoning to new sessions only, is owner decision 8.

**Implementation order is not deployment order.** Build in the unit order of section 7. Deploy
in this order, and do not skip a step:

1. Gateway and frontend with projection support, shipped as one binary, flag off. Behavior is
   unchanged: it sends no `projection`.
2. Service that understands `projection`, still serving `base` to every caller that omits it.
3. Activate by starting the gateway with `--show-reasoning`. The service emits no reasoning
   event for any request that did not ask for `reasoning-1`, so service emission can never
   start before a compatible gateway and frontend are in place.

An old gateway against a new service is safe (no field, `base`). A new gateway with the flag
against an old service fails loudly (step 2 missing).

### 2.5 Bounds

- Per item: 4096 bytes after cleaning, `text_truncated` set on a cut (gateway); 16 KiB at the
  service event (same bound as `assistant_message`). Both bounds already exist and are reused.
- Per session: the snapshot already caps at 2048 events, and an observe window at 512. One
  reasoning event per model call adds at most one event per call. If reasoning pushes real
  sessions into the 2048 cap sooner, the existing `gap` handling applies. No new cap.
- No tool output, no tool arguments, no provider request body is added. Only reasoning text.

## 3. Honesty and security rules

These are requirements, each with a test (section 6).

### 3.1 What reasoning is

1. Reasoning is **the model's own report** of why it acted. It is not a record of what ran.
   Execution evidence stays in `decision` and `result` items and the receipt.
2. It is **never verified**. It carries `diagnostic` authority, is never counted as
   evidence, and never feeds a status, a count, a rail lane or a claim-boundary reading.
   The qualification panel (`g1=false`, `h1="unexecuted"`, `containment="not-established"`,
   `claim_bearing=false`) is unchanged.
3. It is **not policy input**. Nothing reads it to decide ALLOW or DENY, to label a result,
   to pick a hint, or to summarize a turn. The gate sees tool calls only.
4. It may be **incomplete or edited**. A provider can return a summary, a redacted block or
   nothing. The UI says "reported by the model", never "what the model thought".
5. A model can **write plausible but false reasoning**. It must not be shown next to a tool
   line in a way that reads as an explanation of that tool's outcome. The row says what the
   model said it would do, and the tool line says what the supervisor observed.

### 3.2 About the scope label

Contract section 6, question 4, says Pi self-report should be `unverified-input`. The gate
requires an item's scope to equal its snapshot's scope, so a `reasoning` item inside a live
`structural-audit-only` snapshot cannot carry `unverified-input` without changing the
gate. Rather than change the one module that decides what the chat may claim, the
protection is the **authority**: `diagnostic` is admitted everywhere and verified nowhere.
Open question 2 asks whether to relax the gate for a lower claim instead.

### 3.3 Hostile and sensitive text

- Reasoning can contain **prompt-injection text** (copied from files the model read) and
  **secrets** (quoted from files or the prompt). Treat it as untrusted data, like the
  assistant message.
- **Render literally.** Plain text in a `pre-wrap` block. No markdown, no HTML, no links,
  no autolinking, no copy-as-command affordance. The markdown renderer used for assistant
  messages is not used here.
- **Bound and clean** as above. There is no general secret scanner in the repo: redaction in
  `supervisor/` covers provider credentials only. So "redacted like tool output" is true in
  one sense: tool output is not projected at all (contract section 3.4), while reasoning
  can paraphrase or quote what the model just read. Hence the flag and the owner decision
  in question 1. If the live provider key is known to the service, an
  exact-match scrub of that key from reasoning text is cheap; whether an existing helper
  can be reused is **not verified**.
- The gateway is loopback-only, paired and CSRF-checked (contract 9.2). The flag keeps
  reasoning off unless the operator asks for it. Off means the service never sends it, so no
  reasoning text reaches the gateway process or the browser (section 2.3).

## 4. Why this does not touch the thesis

- No change to request bytes, the engineering or research route, `CaptureSSE`, journal
  records, digests, receipts, or any thesis harness. Reasoning is read from bytes already
  stored. The thesis freeze (F15 = `main` at `2a6492dd`) has the thesis sources unchanged;
  this work lands on the dev branch and reaches `main` only through the Resume thread's
  merge, like other product work. A supervisor rebuilt later with this change must not reuse
  the F15 binary pins: it is a different binary and needs its own pins.
- The webchat extension is already marked frozen for the thesis and carries no thesis claim
  (`extensions/webchat/README.md`).
- Turning thinking **on** is a different change and is not proposed here: it alters the
  provider request and profile, token reservations and cost, and touches code shared with
  the thesis. See option C.

## 5. UX sketch (frontend lane)

Chat view, per assistant turn, newest model call last:

```
  you   Fix the failing test in parser.go
  ▸ Thinking                                   <- collapsed by default, one line
  ▸ read parser.go            allowed · ok      <- existing folded tool line
  ▸ Thinking
  ▸ edit parser.go            allowed · ok
  assistant   I changed the loop bound ...
```

- A **Thinking** row is a `Disclosure` (existing `ui/Disclosure`) before the model call's
  tool lines. Closed by default. The closed line shows the word "Thinking" and nothing from
  the text. No preview, so a hostile first line is not shown unopened.
- Opened, it shows the text in a muted, indented, left-ruled block with the caption
  "Reported by the model. Not verified." and the truncation note when `text_truncated`.
- **No durations and no timestamps.** The protocol records no event time; `occurred_at` is
  the gateway's render time (contract 9.6). Do not show "Thought for 4s".
- Not part of status dots, not a lane in the trajectory rail (or a muted lane that does not
  count), not counted in the header, and excluded from the "denied/unknown" filters.
- A toggle in the UI preferences (`uiPrefs`) hides all Thinking rows. Hidden is the
  default when the gateway flag is off, because no items exist then.
- Screen readers: the disclosure button's name is "Thinking, reported by the model". Opening
  a row announces nothing. The shell's single polite live region (`ui/LiveRegion.tsx:21`)
  stays the only polite announcer; the row adds no `aria-live` element.

## 6. Options and recommendation

| Option | What | Verdict |
|---|---|---|
| A. Do nothing for now | No reasoning in the chat. Revisit after the real Pi worker is qualified | Safe, but gives up an agent-UX feature the owner wants and costs nothing in the thesis |
| B. **Product-only, journal-sourced, behind a flag** | Sections 2 to 5. Default off. Reads only what the provider already streams | **Recommended.** No change to shared broker code, request bytes or thesis behavior |
| C. Turn provider thinking on | Change the engineering request to enable reasoning, then display it | Not now. Changes the registered provider profile, request digests, token and cost budgets, and shared code. Needs its own decision, route id and review. Revisit only if Unit 0 shows the provider streams no reasoning with thinking off |
| D. Stream live from Pi RPC | Forward Pi message updates to the gateway | Not recommended. A second, less trusted source, a new bounded IPC channel, and it does not exist for the supervisor-loop worker |

**Recommendation.** B, in this order: measure first (Unit 0); if the provider streams no
reasoning with thinking off, stop and bring option C to the owner; otherwise build the
units below, default off.

## 7. Implementation plan

Small units. Each ends with a green focused test and can be reviewed alone. Owner lane in
brackets. Gateway and supervisor are the product lane ("Overview / Product Demo" thread);
`frontend/` is the webchat lane. Per `.local/coordination.md` the webchat lane rebuilds the
committed `internal/webchat/dist`, so frontend units rebase and rebuild dist (never
hand-merge).

| Unit | Change | Tests that must exist | Lane |
|---|---|---|---|
| 0 | **Measure.** Read-only script or one-off test that scans stored `provider_exchange` responses (a demo-VM turn, `.local/product-demo` captures) for non-empty `reasoning_content`/`reasoning`. Report counts and sizes. No product code | A written result in the PR: how many exchanges, how many with reasoning, median size | product |
| 1 | `assistant_reasoning` kind and the second-pass extractor in `projectProviderEvents` (rules in 2.2) | Table test on recorded SSE: `reasoning_content` only, `reasoning` only, both, none, split across chunks, oversize, invalid UTF-8, `reasoning_details` ignored. **Event assembly:** one JSON chunk spread over several `data:` lines, comment lines, CRLF line ends, `[DONE]`. **Types:** number, object, array and `null` in `reasoning_content`/`reasoning` add nothing. **Failure:** an unreadable reasoning chunk yields no reasoning event and a diagnostic, and the `assistant_message`, `tool_call` and `tool_result` events equal those of the same turn without the extractor. **Unchanged parser:** compare the trusted parsed fields (`AssistantText`, `ToolCall`, `FinishReason`, `Usage`) of a capture with reasoning and of the same capture with the reasoning deltas removed. Do not compare raw bytes across the two: `RawResponse()` preserves the response bytes (`openrouter.go:442`), so they differ by design. Compare exact bytes before and after projection of the **same** capture (journal record and `RawResponse()` untouched). Reasoning never appears in model history | product |
| 2 | Protocol and service projection: `projection` request field, wire copy constant and field, filter-before-numbering in `productEventProjector.session`, settled-cache scoping, observe fixtures for both projections | `TestProtocolFixturesRoundTripThroughTheWireCopy` extended; supervisor `fixtures_test.go -update` regenerates. Service tests on **real reasoning-containing exchanges** (journals built from recorded SSE with reasoning deltas, not synthetic events): `[message, reasoning, message]` under `base` yields contiguous `Seq` 0..n with no reasoning event; under `reasoning-1` yields the larger list; the same session read in both projections does not mix through the cache; a request naming an unknown projection is refused | product |
| 3 | Go contract: `ItemReasoning`, validator rules (authority `diagnostic` only, forbidden fields, text rules), `live_render.go` mapping, `--show-reasoning` flag (default off, requests `base`), projection-scoped epoch and cursor, refusal of a poll for another projection, service fault when `base` returns a reasoning event | Go tests: valid item passes; `observed`, `authoritative` and `unknown` refused; each forbidden field refused; control characters cleaned; truncation flag. **Flag off, real reasoning-containing exchange behind the fake service:** snapshot items have sequence `1..n`, `ValidateSnapshot` passes, `events[]` and `items[]` of a poll have equal length and contiguous `seq`, the snapshot cursor equals the cursor a poll continues from, and the raw response bodies contain none of the reasoning marker text. Flag on: the same checks with reasoning present, and snapshot cursor and poll cursor count the same list. Existing live fixtures render byte-identical to today under `base` | product |
| 4 | Frontend contract: `ITEM_TYPES`, `ReasoningItem`, `ITEM_KEYS`, `assertItem` case with the Go authority rule (`diagnostic` only); every exhaustive `Item` switch gets an explicit branch; projection-scoped cursor key and reload on refusal | **Shared Go/TS corpus:** valid and invalid reasoning documents (each other authority, each forbidden field, over-long text, wrong scope, gapped sequence) under `internal/webchat/fixtures`, which `fixtures.test.ts` already imports for TS. A Go test runs `ValidateSnapshot` over the same files; both suites must agree on accept or refuse for every file. Plus `verification.test.ts` and `verification.property.test.ts` gain "a reasoning item is never verified through `isVerifiedIn` in any projection"; `verification.gate.test.ts` keeps forbidding `isItemEvidenceOnly`, `isVerified` and `judgedItem` under `src/app` and still allows `isVerifiedIn`; a controller test that a different epoch reads no stored cursor | webchat |
| 5 | Chat view: collapsed "Thinking" row, literal rendering, caption, truncation note, no durations, prefs toggle | Component tests: closed by default; opened text is literal (`<script>`, markdown, `javascript:` links stay text, no `dangerouslySetInnerHTML`); no preview when closed; not counted in headline, rail, filters, status; a11y name; **exactly one `aria-live="polite"` element in the rendered page with Thinking rows closed and opened** (the shell's `LiveRegion`; the row adds none); CSP smoke unchanged | webchat |
| 6 | Docs: contract section 2.2 and 9.6 get the new type, event and projection, `product-contracts.md` Milestone 6 lists the kind and the field, README flag | Doc-only | product |

Implementation order: 0, then 1, 2 and 3, then 4 and 5, then 6. Deployment order is different
and is fixed in section 2.4: gateway and frontend first (flag off), then the service, then
the flag. Units 3 and 4 must land in the same release because the gateway embeds the
frontend. Do not run the full gate for this proposal; the units above run the focused Go and
Vitest suites, and Linux-only supervisor tests wait for the Linux line per
`.local/coordination.md`.

## 8. Open questions for the owner

1. **Should model-authored reasoning be projected at all?** The projection so far carries
   no tool output and no provider payload text beyond the assistant's reply. Reasoning can
   quote secrets from files the model read. Accept with the flag default off, accept
   always on, or decline?
2. **Scope label.** Keep the gate untouched and rely on `diagnostic` authority (proposed),
   or let the gate admit `unverified-input` items inside any projection (lower claim, but
   it changes the one module that decides what the chat claims)?
3. **Contract version.** Keep `observation/v1` with the negotiated projection (proposed),
   or bump to `v2` when the type appears?
4. **If Unit 0 finds no reasoning with thinking off:** stop, or open option C (enable
   provider thinking, new route/profile id, cost and digest review)?
5. **Per-call or per-turn grouping.** One `Thinking` row per model call (proposed, matches
   the tool lines), or one merged row per turn?
6. **Secret scrub.** Add an exact-match scrub of the live provider key from reasoning text,
   if a reusable helper exists?
7. **Hidden vs shown by default** once the gateway flag is on: collapsed (proposed) or
   hidden behind the prefs toggle?
8. **Existing sessions when the projection changes.** Turning the flag on re-projects every
   existing session, so its item ids and counts shift and each browser loses its stored
   "events since you left" cursor once (section 2.4). Accept that, with the projection-scoped
   epoch as the reset (proposed, no persistent state)? Or apply reasoning only to sessions
   created after activation, which needs a new per-session field recorded at creation and
   keeps old sessions on `base` for good?

## 9. Not verified

- Whether the provider streams any reasoning when `thinking` is disabled (Unit 0).
- The exact name and shape of Pi 0.87.1's streamed reasoning events. Pi was not run. They
  are irrelevant to option B, which reads the journal, and matter only to option D.
- Whether a reusable exact-match secret scrub exists in `supervisor/`.
- Behavior of the in-progress folded tool-line UI on the open webchat branches
  (`feat/webchat-shadcn-recipes`, PR #43): not read. Section 5 assumes `ui/Disclosure`
  is the shared building block.
- Whether the cursor `epoch` character set accepts the projection separator (section 2.3).
- Whether any request besides `Observe` needs the `projection` field (for example a session
  view that carries event counts). Unit 2 enumerates the requests.
- No test, build or gate was run. This is a document-only change.

## 10. Review notes

An adversarial review of the first draft raised four findings. Each was checked against the
code at `a47661ac` before this revision. All four held; none was rejected.

- **Default-off filtering and the cursor contract** (`validate.go:551-558`, `wire.ts:169-176`,
  `live.go:562`): confirmed. Replaced by the negotiated, filter-before-numbering projection
  in 2.3, with tests on real reasoning-containing exchanges.
- **Existing sessions change identity** (`product_service_events_linux.go:59,73`,
  `live_render.go:116`, `controller.ts:258-280`): confirmed. Added owner decision 8, the
  projection-scoped epoch and the deployment order in 2.4.
- **Extractor robustness** (`openrouter.go:521-563,726,756`): confirmed. Rules in 2.2 and the
  cases in Unit 1.
- **Validation plan**: confirmed on all five points. `differential.test.ts` compares legacy JS
  with TS, `validate.live.test.ts` is TS only (Unit 4 now uses a shared corpus); the gate test
  forbids `isItemEvidenceOnly` and allows `isVerifiedIn`; `RawResponse()` keeps response bytes
  (Unit 1 compares parsed fields, and bytes only for the same capture); `unknown` authority
  contradicted "diagnostic only" (removed); the single polite live region is now asserted in
  Unit 5.

Line numbers in the review were off by one in two places (`session` is at `:59` and the `Seq`
loop at `:73`, not `:58` and `:72`). The findings are unaffected.
