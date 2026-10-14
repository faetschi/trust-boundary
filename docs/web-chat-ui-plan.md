# tbound web workspace — UI redesign and Trajectory view: analysis and plan

Status: **Phase 0 and Phase 1 implemented; Phase 2 partly implemented; Phase 3 present
in its ordinal form only.** See §5 for exactly what shipped. Nothing in this document is
claim-bearing. It describes changes to a presentation-only, untrusted client. G1=false,
H1 unexecuted, containment not-established, claim-bearing=false.

> **Implementation status (this worktree).**
> Shipped: the three-region shell (rail / workspace / inspector), the `Chat | Trajectory`
> view ring, the source and density tokens, the record ledger with grouping by
> `Turn`/`Tool`/`Verdict`, the record inspector (Summary / Payload / Decision / Result /
> Evidence / Timing), trajectory search over allowlisted bounded fields, the ordinal
> sequence rail, the honest footer, and the static-asset graph test.
> Not shipped: export of the observed view, the real timing rail (blocked on a recorded
> duration), and every item in §5 Phase 4 (supervisor-owned).
> Deviation from the mockup: the record inspector renders its sections **stacked** rather
> than tabbed, so an audit never hides a field behind a click. The mockup's `CONTEXT` row
> was removed from both the mockup and the implementation because the projection has no
> context item type.

Reference analysed: DeepSeek Harness (`deepseek-ai/deepseek-harness`), `packages/client/ui-trajectory`
and the surrounding web client, from the package READMEs, the architecture document, and
screenshots of the running Web UI. The harness is a different product with different
authority; we borrow **patterns and presentation**, never its authority model.

---

## 0. Verdict summary

| Question | Answer |
|---|---|
| Can we make the workspace look and feel like the harness? | **Yes, cheaply.** It is dark-only, dense, one foreground surface with a left rail, a tab ring, and a right inspector. Our shell already has the same three regions; this is a restyle plus a density pass, not a rewrite. |
| Can we build a real Trajectory view? | **Yes, as a record ledger — today**, from the durable audit journal plus the existing four-type projection. Our items already carry `turn_id`, and `store.mjs` already groups by turn. |
| Can we build the harness's *timing* overview exactly? | **No, not truthfully.** Nothing durable in tbound carries a wall-clock timestamp. Building lanes with durations would require inventing time, which is precisely what the harness itself refuses to do. We ship an **ordinal sequence rail** instead, and mark duration as unavailable. |
| Does the Trajectory view strengthen the tbound thesis? | **Yes, materially.** The harness ledger has no governance lens at all: no verdicts, no reason codes, no policy digest, no generations, no containment, no UNKNOWN state. A governance-aware ledger is a *stronger* artifact than the one we are imitating. |
| Do we need the core thread first? | **Only for live data, timing, tokens, transcript, and submission.** The ledger, inspector, search, filters and grouping are all buildable now from the existing projection. |

---

## 1. What the harness Trajectory view actually is

### 1.1 Architecture

- The trajectory UI is a **standalone client plugin** (`@deepseek-ai/dsh-client-ui-trajectory`)
  registered into the conversation's **view ring** alongside Chat. Tabs are a ring, not a route.
- It is a **pure projection**. Trajectory-owned *Definitions* assemble "business records"
  from the shared Session window of raw events. Its README states it "neither reads nor
  changes the Chat conversation snapshot."
- The durable substrate is a single append-only event log (`SessionEvent`, `session.jsonl` /
  `session.vN.jsonl`). Fork, resume, transcripts, telemetry and persistence all derive from it.
- The projection seam is `ctx.sessionProjections`: registered units fold committed events
  **incrementally**; consumers read one typed state via `stateOf()`; carriers batch **cropped**
  client views via `snapshot()`.
- Live incrementality comes from a separate transient stream (`agent/assistant-stream`:
  start → chunk* → end), which is *not* the durable log.
- Export is the session log itself (a zip containing `session.jsonl`).

This is, structurally, **the same shape as our contract**: durable record → incremental fold →
bounded cropped view → client. Our `epoch:cursor` SSE and `snapshot` are a smaller instance of
the same seam. That is why the borrow is cheap.

### 1.2 Anatomy observed in the running UI

- **Tab ring:** `Chat | Trajectory`, with the session title, a mode chip ("Standard mode"),
  and view-level actions at the right.
- **Trajectory toolbar:** a group-by toggle group (`Duration | Turns | Calls`) and a search box.
- **Timing overview:** a fixed lane chart above the ledger, lanes `Input / Model / Tools`,
  assistant spans split into TTFT vs decoding, hover (500 ms) reveals exact clock and duration,
  drag focuses the ledger on the selected range, wheel zooms, right-click clears.
- **Turn/step gutter:** a numbered turn box and per-record step numbering.
- **Ledger row:** status dot · source chip · optional inline label · truncated monospace payload ·
  `→` result preview.
- **Source chips (colour-coded):** `SYSTEM`, `USER`, `CONTEXT`, `ASSISTANT`, `TOOL` (+ nested
  `SUBTOOL`, compaction, and a standalone `Between turns` section).
- **Inspector:** header `TOOL · Turn n · Step m` with a close control; tabs
  `Summary | Payload | Result | Schema | Timing`; fields `Hierarchy`, `Status`, `Payload`
  (pretty JSON), `Result`, `Schema` (the tool's own description), `Timing` (`Started`, `Duration`).
- **Density:** ~13 px rows, monospace payloads, muted secondary text, 10–12 px radii,
  ~1 px low-contrast borders, hover-revealed affordances.
- **Behaviour:** opens at the tail, pages older history on demand, renders only visible rows,
  follows the tail while streaming, and **suspends following when the user scrolls up**.

### 1.3 Principles it encodes (and why they matter to us)

Quoting the package README, because these are the sentences worth stealing:

- *"in-flight records show a start marker without inventing elapsed time."*
- *"A completed model response does not imply that its tools or turn have finished."*
- *"an unloaded Step start or a stream without tokens leaves the corresponding metric unavailable."*
- *"Model-visible means logged."* (architecture doc)

The harness therefore has **the same defect-aversion we do**: it refuses to fabricate a
measurement it did not record. Our success gate (`authority=observed` **and**
`verification_scope=structural-audit-only`) is the same instinct applied to *outcomes* rather
than to *durations*. The two designs are compatible; ours is stricter.

Where we diverge, deliberately: the harness's log **is** the authority, so it may fork, resume
and branch from it. Ours is not. A browser-side fork control in tbound would be an authority
claim. We do not build one, and the UI must say why it is absent.

---

## 2. Feature-by-feature mapping

| Harness feature | tbound equivalent | Available today | Verdict |
|---|---|---|---|
| Chat \| Trajectory tab ring | Same ring; Trajectory is the governance lens | Shell exists (single view) | **Phase 0** |
| `SYSTEM / USER / CONTEXT / ASSISTANT / TOOL` chips | Same chips, plus `DECISION` and `RESULT` as first-class sources | 4 item types already exist | **Phase 1** |
| Turn grouping + turn/step gutter | Group by `turn_id` (already in the contract and in `store.mjs:groupTurn/turnsOf`) | `turn_id` present; no step concept | **Phase 1** (turn); step needs supervisor |
| Tool row: name + args preview + `→` result | Tool row correlated by `tool_call_id` (already grouped client-side) | Yes | **Phase 1** |
| Row status dots | `ResultState` / `Verdict` mapped to dot states, `unknown`/`lost` rendered as UNKNOWN | Yes | **Phase 1** |
| Inspector `Summary / Payload / Result / Schema / Timing` | `Summary / Payload / Decision / Result / Evidence / Timing` | Bounded fields exist; `Decision` and `Evidence` come from our panels | **Phase 2** |
| Group-by toggle (`Duration/Turns/Calls`) | `Turn / Tool / Verdict` grouping | Filter toggle group exists | **Phase 1** |
| Search across records | Search bounded fields (text/summary/detail/tool/reason_code) | No search yet | **Phase 2** |
| Timing lanes (Input/Model/Tools, TTFT) | **Ordinal sequence rail** (turn → records), durations marked unavailable | No timestamps anywhere durable | **Phase 3 (ordinal only)**; real timing blocked |
| Token/usage footer, cache hit, tok/s | Footer shows `turns · records · policy verdicts`; token metrics rendered `unavailable` | Usage exists only inside raw provider bytes | **Phase 0** (honest) / blocked for real values |
| Export session log (zip/jsonl) | Export the **bounded observed view** (JSON) | Cheap | **Phase 2**, bounded form only |
| Fork / resume / branch from the log | **Not built.** Authority is supervisor-owned | — | **Non-goal**, stated in the UI |
| Workspaces rail + session list | Source binding + session list (read-only) | Rail exists | **Phase 0** |
| Model + permission selectors in the composer | Shown as **observed** posture, never as controls | Composer disabled today | **Phase 0** |
| Approval prompts before risky operations | Not applicable: the supervisor decides, and the client may not approve | — | **Non-goal** |
| Deep links to a record | Out of scope (the harness does not have them either) | — | Non-goal |

---

## 3. The honesty problem: what cannot be built today

This is the part that decides the design. Evidence, not opinion:

| Gap | Evidence | Consequence |
|---|---|---|
| **No wall-clock time in the durable log** | `audit.Event`/`audit.Record` have no time field (`internal/audit/audit.go:59-89`); no provider/operation evidence struct, `CapturedResponse`, `ExchangeRecord` or the receipt carries one. The only clock value in the whole webchat path is the receipt file's mtime (`internal/webchat/receipt.go`). | No duration, no TTFT, no tok/s, no true left-to-right timing. **A timing lane would be fabricated.** |
| **Pi RPC forwards lifecycle only** | `pumpEvents` maps `agent_settled → turn_end` and failed `prompt → worker_error`; assistant text, tool calls, results and usage are read and dropped (`internal/piruntime/worker_channels_linux.go:1612-1701`). | No live transcript, no live tool rows. |
| **Turn boundaries are partial** | `providerExchangeEvidence.turn` is a monotonic counter with no start/end record and no turn id; the live `turn_end` is never journalled. | Turn grouping is approximate; "Step" does not exist at all. |
| **Usage is untyped** | Token counts live only inside raw `provider_exchange.response` bytes. | Token/cache metrics cannot be shown as values. |
| **Raw payloads are unbounded and sensitive** | The journal durably holds full request bodies, response bodies and tool result bytes; the webchat contract bounds display text to 4096/8192 B and our docs already record that prose is bounded but **not** content-redacted. | The browser must show **bounded previews + digests**, not raw payloads. |
| **No live source** | `NewLiveStore()` returns `ErrLiveUnavailable`; `--live` fails closed. | Everything is fixture / replay / receipt. |

What *is* durable and therefore projectable: per-tool **identity** (tool name, `tool_call_id`,
sequence), **argument digest** and raw arguments, **result** bytes and digest, the **gate
decision** (`verdict`, `reason_code`, `policy_digest`), **generation transitions**
(`generation_from`/`to`, tree digests), **effect/transition ids**, **operation evidence**
(exit code, command containment status, admission), and the **publication/recovery** state.

That is enough for a genuinely useful trajectory ledger — **minus time**. So Phase 3 ships an
ordinal rail, not a timing chart, and says so in the UI.

---

## 4. Target design

Keep: zero-build ESM + CSS, `role="log"`/`aria-live="off"`, keyboard audits, the
no-success-without-verification gate, the `.mjs` module split, and the coss/ui token vocabulary.
Borrow the harness's **layout, density and row anatomy**; do not import React/Tailwind/virtualizer.

### 4.1 Shell

```
topbar      brand · [fixture|live|replay|unavailable] · verification · connection · Governance
claim strip G1 false · H1 unexecuted · containment not-established · claim-bearing false
layout
├─ rail (Projects/Workspaces → Source binding, Sessions, Streams, rail foot)
├─ main
│   ├─ conversation head: title · observed source/verification chips · status
│   ├─ tab ring: Chat | Trajectory
│   ├─ Chat: transcript + tool/decision/result cards
│   ├─ Trajectory: group-by toggle · search · ordinal sequence rail · record ledger
│   ├─ composer (disabled, honest note)
│   └─ footer metrics row: turns · records · verdicts · tokens(unavailable) · cache(unavailable)
└─ inspector: Summary / Payload / Decision / Result / Evidence / Timing
```

### 4.2 New tokens (additive, in `:root`)

Source colours, so a chip is never identified by text alone:

```
--source-system --source-user --source-context --source-assistant
--source-tool   --source-decision --source-result
--ledger-dot-ok --ledger-dot-deny --ledger-dot-unknown --ledger-dot-lost
```

Plus density tokens (`--row-h`, `--row-pad-x`) and a `--rail-width` / `--inspector-width` pair so
the three-region geometry is declared once.

### 4.3 Row anatomy (Trajectory)

```
●  [TOOL]   write            {"path":"…","content":"…"}          →  DENY · argument_digest_mismatch
│    │       │                 │                                      │
│    │       │                 │                                      └ verdict chip (tbound only)
│    │       │                 └ bounded payload preview (monospace, truncated, never raw)
│    │       └ inline label (tool name / record kind)
│    └ source chip (colour + text, never colour alone)
└ status dot (aria-labelled → ResultState/Verdict; unknown and lost are distinct)
```

### 4.4 Honest degradation rules (UI-enforceable)

1. A metric without a recorded source renders as `unavailable` with the reason, never as `0`,
   `—`, or a plausible guess. (This mirrors our `StreamHealth` rule.)
2. An in-flight record shows a `started` marker and **no** elapsed time.
3. The ordinal rail's axis is labelled `sequence`, never `time`.
4. `unknown`/`lost` results keep their own dot and chip; they are never aggregated into a
   success count.
5. Nothing in the Trajectory view can change a projection, verdict, or filter outcome; filters
   remain presentation-only.

---

## 5. Phased plan

Each phase is independently shippable and testable. No phase may be described as
"verified" unless the receipt exists (command, exit code, source hash).

### Phase 0 — Shell, density, tokens
- Restyle the shell to the observed geometry (rail / main / inspector), add the tab ring and
  the footer metrics row, add the source/density tokens, keep every existing accessible name
  and the claim strip.
- Fix the duplicate `#status-announcer` id found during this analysis.
- **Files:** `assets/index.html`, `assets/styles.css`, `assets/render.mjs`, `assets/app.mjs`.
- **Acceptance:** existing Node render/store tests still pass; keyboard audit re-run shows the
  same tab order plus the new tab ring; screenshots recorded for desktop and mobile.

### Phase 1 — Trajectory ledger
- New view: group-by toggle (`Turn | Tool | Verdict`), record rows with source chips, status
  dots, bounded payload previews and `→` result previews; turn gutter from `turn_id`.
- **Files:** new `assets/trajectory.mjs`; `assets/store.mjs` (grouping helpers are already
  there), `assets/render.mjs`, `assets/index.html`, `assets/styles.css`, plus `jstest/`.
- **Acceptance:** pure-function tests for grouping, dot/chip mapping, and the degradation rules
  (unknown ≠ success, no fabricated duration); DOM tests that an `unknown` record renders an
  UNKNOWN chip and that no duration string is produced without a timestamp.

### Phase 2 — Inspector, search, filters, export
- Inspector tabs `Summary / Payload / Decision / Result / Evidence / Timing` fed only by
  contract fields; search over bounded text; export the observed view as JSON.
- **Acceptance:** the inspector shows `unavailable` for every field the source omits; export
  contains only contract fields (a test asserts no raw `detail` beyond the bounded limit and no
  unknown keys).

### Phase 3 — Ordinal sequence rail
- A lane overview whose axis is the record sequence, with per-turn segments and verdict colouring;
  explicitly labelled as ordinal; hover shows sequence numbers, **not** durations.
- **Acceptance:** a test asserts the rail's label and that no time value appears unless the
  source declares one.

### Phase 4 — Supervisor-owned data (core-thread dependency)
Proposed in `docs/web-chat-contract.md`; not implementable in this repo alone:
1. a timestamp on durable records (a hash-chained record-format change, or an agreed sidecar);
2. explicit turn/step records with ids;
3. typed token usage;
4. Pi RPC forwarding of the events the pump currently drops;
5. a bounded, structured transcript projection (safe summaries, not raw bytes);
6. the live source, submission and cancellation routes.

Only after (1)–(3) may the ordinal rail become a real timing rail, and only after (5) may the
browser show transcript text.

---

## 6. Trust constraints and non-goals

- **No authority in the browser.** No fork, resume, branch, approve, deny, cancel, or execute
  control. The harness can offer these because its log is authoritative; ours is supervisor-owned.
- **No raw payload display.** Digests and bounded previews only, until the supervisor emits a
  structured safe summary. Our own docs already record that prose is bounded but not redacted.
- **No fabricated metrics.** See §4.4. This is the same rule as our success gate, applied to
  measurement rather than outcome.
- **No claim inflation.** The view is presentation; it never asserts G1, H1 or containment.
- **No new client dependencies.** Patterns borrowed; stack not.

## 7. Open questions for the core thread

1. Would the audit record gain a timestamp, or should the projection own a monotonic clock
   sidecar? (Affects the hash-chained format; `recordVersion` is 1.)
2. Is a turn/step record authoritative, or derived from `providerExchangeEvidence.turn`?
3. Which bounded fields may reach a browser for transcript text — and under what redaction rule?
4. Should the trajectory feed be a new supervisor-owned read model, or an extension of the
   existing observation contract (`tbound-webchat-observation/v1`)?

---

## Appendix A — Could we simply adopt the harness client instead of porting the design?

Measured on a shallow sparse checkout of `deepseek-harness` at `d743267`
(`release-0.2.1-alpha.2`), and on the npm registry.

### A.1 What was measured

| Fact | Value |
|---|---|
| Client packages in `packages/client` | **63** |
| Source files in `packages/client` (`.ts/.tsx/.css`) | **1,918** (~14.9 MB) |
| React components (`.tsx`) | **581** |
| `ui-primitives` alone | 250 files, ~1.4 MB |
| `ui-theme` alone | 23 KB `design-platform.css` + CSS-module components + 3 Montserrat `.woff2` + a JS theme applier |
| `ui-trajectory` alone | 58 files, ~650 KB, 45 source files |
| Everything in the client requires | React 18, `@deepseek-ai/cordis` 4, CSS modules, and a `tsdown`/bundler build |
| Published to npm? | **Yes**, but client packages are `0.0.1-rc.1` while the CLI is `0.2.0-rc.2` |
| `ui-trajectory` declares it must co-mount with | `dsh-api-session-controller`, `dsh-client-locale`, `ui-conversation`, `ui-renderer`, `ui-session` |
| `ui-conversation` peer-requires | ~17 more packages, including `dsh-agent`, `dsh-tools`, `dsh-llm-retry`, `dsh-client-connection`, `ui-slots`, `ui-primitives` |
| Host side, if running dsh's own server | `@deepseek-ai/dsh` has ~90 direct dependencies, including `dsh-tool-bash`, `dsh-tool-subagent`, `dsh-plugin-manager`, `dsh-mcp-client`, `dsh-hooks-codex`, `dsh-experimental-auto-review`, `dsh-schedule` |
| License | MIT (root `LICENSE`); a separate `BRAND_GUIDELINES.md` governs its brand |

Checked and worth stating plainly: the published tarballs **do** carry rewritten semver ranges, so
they are installable outside the monorepo. The blocker is not packaging.

### A.2 The finding that matters

**The trajectory UI is a leaf of a composed client runtime, not a widget.**

- `ui-theme`, `ui-primitives`, `ui-slots`, `ui-renderer`, `ui-session`, `ui-conversation`,
  `client-runtime`, `client-connection` and a session controller are all prerequisites. A
  "minimal" roster is still roughly **25–35 packages plus a Node host with its own auth
  (launch-token exchange + signed cookie) and WebSocket transport**, served by dsh's own loader.
- The client is a **Cordis plugin tree**. Its README describes the trajectory view as a plugin
  "registered into the conversation view ring", served and composed by the dsh host.
- Consequence: "use the existing UI" means **adopting a second runtime as the web server**, with
  the Go supervisor demoted to a data source behind it.

### A.3 Three further points, all of which cut against the premise

1. **The UI code is not the bottleneck.** Adopting the harness UI does not close a single gap
   from §3: no timestamps, no turn/step records, no typed usage, no Pi event forwarding, no
   bounded transcript. The ledger would render an empty or wrong page until we build the read
   model and change the durable record format. The expensive work is the same either way.
2. **Their ledger is not governance-aware.** There is no verdict, reason code, policy digest,
   generation transition, containment status, effect id, or UNKNOWN state anywhere in its record
   vocabulary. Every tbound-specific row, chip and inspector tab is **new code we would write as a
   client plugin against `0.0.1-rc.1` internals**, in TypeScript, against APIs the project
   explicitly warns are breaking-change-prone.
3. **The trust direction inverts.** Today the browser is an untrusted client of a loopback Go
   gateway. Under adoption, the browser is served by a Node host whose product ships tool
   execution, approvals, subagent delegation, scheduling, hooks and an MCP client. Our profile
   patch would have to *remove* all of that, and that removal becomes part of what must be
   audited — a larger and less verifiable surface than the one we have.

### A.4 Options

| Option | What it means | Cost | Thesis fit |
|---|---|---|---|
| **A. Port the design** (recommended) | Keep the zero-build client and the Go gateway. Take the harness's *design decisions* — row anatomy, source chips, inspector tabs, density, tab ring — and the token values, with attribution. | Bounded: our `store.mjs` already groups by turn; the ledger + inspector is a few hundred lines of ESM/CSS. | Strong: authority and the trust story are unchanged. |
| **B. Run dsh as the UI, ship a tbound profile + governance plugin** | `npx @deepseek-ai/dsh web` with a patch that disables authority-bearing rows and adds a tbound plugin feeding our records into the session projection. | Real UI immediately; then plugin work against RC internals; Node host + auth + WS in the trust-adjacent path. | Weak unless labelled precisely: the client is third-party, and the demo is of dsh rendering tbound data. |
| **C. Vendor a stripped fork** | Copy `packages/client`, delete ~55 of 63 packages, rewire to the Go gateway. | Largest: React + Cordis + a bundler + a divergent fork to maintain. | Medium: keeps Go authority, but directly opposes "keep the web service minimal". |
| **D. Bounded spike first** | 1–2 days proving Option B end-to-end with fixture data, then decide. Informative and bounded. | Small, but not free. | Neutral; produces evidence, not the artifact. |

**Recommendation: A, optionally with D run in parallel as a reference.** Port what actually
carries the UX value (the ledger, the inspector, the chips, the density, the tab ring) and copy
MIT-licensed *token values and markup patterns* with attribution — while keeping the Go gateway,
the verification gate, and the minimal service. Use the harness locally to compare behaviour.
Adopt B or C only with the explicit understanding that the shipped client is then a third-party
harness, the browser no longer talks to the Go authority directly, and the governance lens is
still our own new plugin rather than something we inherit.

Whatever is chosen, **BRAND_GUIDELINES.md is not ours**: copy patterns and tokens, never the
harness name, logo or trade dress.

## Appendix B — Patterns worth borrowing piecemeal from the harness client

A read-only survey of `packages/client` (upstream `d743267`, MIT; read in source unless
marked otherwise) found small patterns that fit the vanilla, Go-authoritative design.
None of them changes the Appendix A decision. Effort is an estimate for a vanilla ES-module port.

| Pattern | Upstream file | Use here | Effort |
|---|---|---|---|
| Stable-prefix streaming markdown (re-parse only the tail blocks, fence-aware) | `ui-primitives/src/markdown/incremental.ts` | Streaming transcript (milestone 2) | M |
| No HTML parser in markdown; URL scheme allowlist (http, https, mailto) | `ui-primitives/src/markdown/render.tsx:333-335,53` | Keep zero `innerHTML`; build DOM with `createElement`/`createTextNode` only | S–M |
| Capped exponential backoff with jitter; slow-connect warning that does not cancel | `connection/src/client/connection.ts:139-150`, `recovery-config.ts` | Reconnect; a resync replaces the whole loaded window | S |
| Explicit "history not loaded" window gap marker | `ui-conversation/.../assembler.ts:27,769` | Gap rendering | S |
| Two-press Escape cancel bound to session/turn/generation; skips IME and key repeat | `ui-conversation/src/client/stop-sequence.ts` | Cancel is a request; the UI still waits for the settled result | S |
| Visually hidden text beside colour-only status | `ui-tool/.../ToolRow.tsx:104-114` | Accessibility | S |
| Anchored tail window (pin the tail at open, extend forward) | `ui-trajectory/src/client/TrajectoryView.tsx:163-177` | Needs a snapshot `anchor` cursor field | M |

Avoid: authority-bearing UI (`ui-approval`, `ui-permission-presets`, terminal/browser
sidebars; judged from package descriptions only); the `preparing|running|ok|error|stopped`
tool vocabulary (`ok` has no verification gate and there is no `unknown`/`denied`/`lost`);
zero-width spans for null durations (we render `unavailable: <reason>`); `aria-live="polite"`
on the ledger; any `innerHTML` sink; and search over full message bodies (index bounded fields only).

Not verified: transitive licences (`diff`, `micromark`, `shiki`, `dompurify`, virtual-list);
no upstream build or test was run; the scroll-follow and attempt-history behaviours rest on
the README only.

**Gateway fence before any POST route (milestone 3).** Upstream refuses
`Sec-Fetch-Site: cross-site` and the literal `null` Origin but accepts an absent Origin;
our `handler.go` is GET-only and also accepts an absent Origin. Before POST routes land,
add the `Sec-Fetch-Site` refusal and a JSON-only content-type requirement, and decide
whether to require `Origin` on POST (stricter than upstream).
