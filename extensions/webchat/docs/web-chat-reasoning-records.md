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
| `authority` | **`diagnostic` only.** The validator refuses `observed` and `authoritative` for this type |
| `verification_scope` | must equal the snapshot's scope. The existing gate (`verification.ts` `scopeFault`) refuses any other value, so a live snapshot's reasoning item carries `structural-audit-only`. That label describes the snapshot, not the item: see 3.2 |

Order and grouping: one `reasoning` item per model call (per `provider_exchange`), placed
**before** that call's assistant message and tool calls, with the same short `turn_id`.
The projection stays one event to one item, so `sequence = seq + 1` is unchanged.

### 2.2 Where it is produced

```
provider journal (provider_exchange, raw SSE)           [unchanged, already durable]
  -> projectProviderEvents (cmd/tbound)                  NEW: second scan -> assistant_reasoning event
  -> product protocol observe response                   NEW event kind, protocol version stays v1
  -> gateway live_render.go eventItem                    NEW: assistant_reasoning -> reasoning item
  -> snapshot / events JSON                              validated by ValidateSnapshot
  -> frontend validate.ts -> store -> chat view          NEW type, collapsed row
```

- **Source is the journal only.** Do not plumb Pi RPC `message_update` records. Pi's stream
  is the worker's own relay, and the broker's capture is the record the supervisor already
  trusts. One source also works unchanged when a real Pi worker replaces the supervisor loop.
- **Extraction without touching the parser.** `CaptureSSE` stays byte-for-byte as is. In
  `projectProviderEvents`, after `CaptureSSE` has accepted the response, a small second pass
  over the same bytes concatenates `choices[0].delta.reasoning_content` (else `reasoning`)
  across the SSE data lines, capped at 16 KiB (`productEventMaxTextBytes`), and ignores
  `reasoning_details` (structured, may be opaque or encrypted). An empty result emits no
  event. The alternative, a new accessor on `CapturedResponse`, edits `openrouter.go`, which
  is shared with the research route and the frozen thesis build, so it is not preferred.
- **Not live typing.** The projection is built from durable records after a call ends
  ("durable before published", contract section 3.1). Reasoning therefore appears when the
  model call completes, not token by token.
- **Event.** New kind `assistant_reasoning` using the existing `Event.Text` field. No new
  struct field, so the strict wire decoders need no change beyond the kind constant.

### 2.3 Compatibility

| Surface | Change | Compatibility |
|---|---|---|
| Product protocol (`tbound-product-protocol/v1`) | one new `Kind` string | Additive. Neither side validates kinds. An old gateway shows `Unmapped event "assistant_reasoning"` as a system message: noisy, safe. Ship the gateway first, then the service |
| Wire copy in the gateway (`wire.go`) | add the constant | Parity test `TestProtocolFixturesRoundTripThroughTheWireCopy` needs a new observe fixture with a reasoning event, generated by `supervisor/internal/productprotocol/fixtures_test.go -update` |
| Observation contract (`tbound-webchat-observation/v1`) | one new item type | Breaking for a client that does not know it (whole snapshot refused, section 1). The Go gateway embeds its own frontend build, so the pair ships as one binary. Keep the version string, and keep **default output unchanged**: the gateway emits `reasoning` items only with `--show-reasoning` (default off). A bump to `v2` is the stricter alternative: open question 3 |
| Go validator (`validate.go`) | `ItemReasoning` const, `validateItemType`, a `validateItem` case, a rule refusing `observed`/`authoritative` for it | Fixture and replay documents stay valid (no such items) |
| Frontend (`core/contract.ts`, `core/validate.ts`) | `ITEM_TYPES`, `ReasoningItem`, `ITEM_KEYS.reasoning`, an `assertItem` case mirroring Go | The Go/TS differential suites (`core/differential.test.ts`, `validate.live.test.ts`) must cover the new type |
| Consumers of `Item` | `core/store.ts:345` (blocks), `core/trajectory.ts`, `core/filters.ts`, `app/views/trajectory/rail.ts`, `app/views/chat/Transcript.tsx` | Exhaustive switches fail to compile when the type is added. Each gets an explicit "reasoning is not a tool, not a decision, not a result" branch |
| Verification gate (`verification.ts`) | none | `isItemEvidenceOnly` needs `authority === "observed"`, so a `diagnostic` item is never verified. A test pins this |

### 2.4 Bounds

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
  reasoning off unless the operator asks for it.

## 4. Why this does not touch the thesis

- No change to request bytes, the engineering or research route, `CaptureSSE`, journal
  records, digests, receipts, or any thesis harness. Reasoning is read from bytes already
  stored. The thesis freeze (F15 = `main` at `2a6492dd`) has the thesis sources unchanged;
  this work lands on the dev branch and reaches `main` only through the Resume thread's
  merge, like other product work.
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
- Screen readers: the disclosure button's name is "Thinking, reported by the model".

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
| 1 | `assistant_reasoning` kind and the second-pass extractor in `projectProviderEvents`, cap 16 KiB, UTF-8-safe cut, empty means no event | Table test on recorded SSE: reasoning_content only, reasoning only, both, none, split across chunks, oversize, invalid UTF-8, `reasoning_details` ignored; assert `CaptureSSE` output is byte-identical with and without reasoning (no parser change); assert it never appears in model history | product |
| 2 | Wire copy constant and a generated observe fixture with a reasoning event | `TestProtocolFixturesRoundTripThroughTheWireCopy` extended; supervisor `fixtures_test.go -update` regenerates | product |
| 3 | Go contract: `ItemReasoning`, validator rules (only `diagnostic`/`unknown` authority, forbidden fields, text rules), `live_render.go` mapping, `--show-reasoning` flag (default off drops the event without a placeholder) | Go tests: valid item passes; `observed` and `authoritative` refused; each forbidden field refused; control characters cleaned; truncation flag; flag off yields byte-identical snapshots to today on the existing live fixtures | product |
| 4 | Frontend contract: `ITEM_TYPES`, `ReasoningItem`, `ITEM_KEYS`, `assertItem` case mirroring Go; every exhaustive `Item` switch gets an explicit branch | Differential test against the Go validator (same corpus incl. the new type); `verification.test.ts` and `verification.property.test.ts` gain "a reasoning item is never verified in any projection"; gate test that `src/app` still does not name `isVerifiedIn` | webchat |
| 5 | Chat view: collapsed "Thinking" row, literal rendering, caption, truncation note, no durations, prefs toggle | Component tests: closed by default; opened text is literal (`<script>`, markdown, `javascript:` links stay text, no `dangerouslySetInnerHTML`); no preview when closed; not counted in headline, rail, filters, status; a11y name; CSP smoke unchanged | webchat |
| 6 | Docs: contract section 2.2 and 9.6 get the new type and event, `product-contracts.md` Milestone 6 lists the kind, README flag | Doc-only | product |

Order: 0, then 1 and 2, then 3, then 4 and 5, then 6. Units 3 and 4 must land in the same
release because the gateway embeds the frontend. Do not run the full gate for this
proposal; the units above run the focused Go and Vitest suites, and Linux-only supervisor
tests wait for the Linux line per `.local/coordination.md`.

## 8. Open questions for the owner

1. **Should model-authored reasoning be projected at all?** The projection so far carries
   no tool output and no provider payload text beyond the assistant's reply. Reasoning can
   quote secrets from files the model read. Accept with the flag default off, accept
   always on, or decline?
2. **Scope label.** Keep the gate untouched and rely on `diagnostic` authority (proposed),
   or let the gate admit `unverified-input` items inside any projection (lower claim, but
   it changes the one module that decides what the chat claims)?
3. **Contract version.** Keep `observation/v1` with the flag (proposed), or bump to `v2`
   when the type appears?
4. **If Unit 0 finds no reasoning with thinking off:** stop, or open option C (enable
   provider thinking, new route/profile id, cost and digest review)?
5. **Per-call or per-turn grouping.** One `Thinking` row per model call (proposed, matches
   the tool lines), or one merged row per turn?
6. **Secret scrub.** Add an exact-match scrub of the live provider key from reasoning text,
   if a reusable helper exists?
7. **Hidden vs shown by default** once the gateway flag is on: collapsed (proposed) or
   hidden behind the prefs toggle?

## 9. Not verified

- Whether the provider streams any reasoning when `thinking` is disabled (Unit 0).
- The exact name and shape of Pi 0.87.1's streamed reasoning events. Pi was not run. They
  are irrelevant to option B, which reads the journal, and matter only to option D.
- Whether a reusable exact-match secret scrub exists in `supervisor/`.
- Behavior of the in-progress folded tool-line UI on the open webchat branches
  (`feat/webchat-shadcn-recipes`, PR #43): not read. Section 5 assumes `ui/Disclosure`
  is the shared building block.
- No test, build or gate was run. This is a document-only change.
