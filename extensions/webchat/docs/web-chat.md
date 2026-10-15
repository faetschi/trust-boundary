# Pi web chat workspace (frontend product)

Status: **development increment, non-claim-bearing.** `G1=false`; containment is
`not-established`; `H1` is `unexecuted`. No qualified public production launch has
occurred. A green workspace, green tests, or a loaded fixture establishes none of
these. The React client at `/app/` is the default UI, and `/` redirects there. The
legacy workspace at `/legacy/` is deprecated until sign-off. See
[frontend.md](frontend.md) for the client's status.

This document describes the first frontend product increment for tbound: a new,
untrusted web chat workspace for the official Pi CLI/RPC harness, wrapped by
tbound governance. It is a presentation layer over the supervisor, not a second
authority. The relationship to OpenChamber-for-OpenCode is the *product concept*
(a better presentation layer for a harness), not a visual or backend template.

Related: [web chat service contract proposal](web-chat-contract.md),
[architecture](../../../docs/architecture.md), [status](../../../docs/status.md).

## 1. Authority boundary

The Go supervisor retains admission, policy, leases, effect execution, durable
journals, and publication authority. The browser and this gateway are **untrusted
clients**:

- The gateway never launches Pi, executes a tool, evaluates policy, admits or
  releases an effect, writes the live workspace, or connects to Pi RPC.
- There is no per-tool approval, no policy override, and no alternate execution
  route.
- The gateway serves only bounded, allowlisted projection fields. It never serves
  raw journals, credentials, provider payloads, arbitrary paths, or unrestricted
  tool arguments/output.
- A successful execution, a publication, and a policy `ALLOW` are **distinct**
  facts with distinct authority. The workspace renders them separately and never
  infers one from another.
- `UNKNOWN` and `LOST`/`LOSSY` are not success. Uncertain effects and cleanup stay
  `UNKNOWN`; the client never retries an effect on reconnect.

## 2. What is implemented, and what is not

Honesty is a hard requirement: the UI must not imply capabilities that do not
exist. The composer is present and labelled but **read-only**: its field is `readonly` and
`aria-disabled`, and its send control is `aria-disabled`. Neither is natively `disabled`,
so both stay focusable. There is no cancel control, because submission and cancellation
require the authenticated supervisor session service.

| Capability | State |
|---|---|
| Bounded read-only projection (snapshot + cursor SSE) | Implemented (fixture / replay / receipt / unavailable sources) |
| Default UI: the React client at `/app/`, with `/` redirecting there | Implemented. The legacy UI at `/legacy/` is deprecated, and `/` serves it only when the React app is unavailable |
| Typed post-run Pi receipt import (development, native-fixture and production shapes) | Implemented (`--receipt`, `--receipt-demo`) |
| Chat transcript rendering (user/assistant messages) | Implemented |
| Tool activity with policy verdict, reason code, and result state | Implemented |
| Timeline finding filters and an observed posture summary | Implemented |
| Trajectory view: grouped record ledger, ordinal sequence rail, and a per-record inspector | Implemented (read-only; see §4.4) |
| Governance / provenance / publication / recovery / qualification panels | Implemented |
| Explicit source labels (`fixture`/`live`/`replay`/`unavailable`) and unverified markers | Implemented |
| Continuity-gap recovery from a snapshot (no invented success) | Implemented |
| Snapshot-first reconnect: capped, jittered backoff, offline suspend, slow-handshake warning (§7.3) | Implemented |
| Assistant messages as a bounded markdown subset, no HTML parsing (§4.6) | Implemented |
| Tail window: records that arrive while scrolled away are counted on a `Jump to latest` control (§4.5) | Implemented, client-only |
| Diff viewer | **Deferred** (§12) |
| Two-press cancel state machine (legacy `stop-sequence.mjs`; typed port in `frontend/src/core/stop-sequence.ts`) | Written and ported, **not wired** into either UI (§4.7) |
| Live projection from the supervisor | **Not implemented** (`--live` fails closed) |
| Persistent multi-turn sessions and history | **Not implemented** (supervisor-owned) |
| Turn submission / cancellation | **Not implemented** (composer read-only) |
| Browser authentication, session ownership, CSRF/origin protection for remote use | **Not implemented** (loopback-only gateway; see §6) |
| Per-tool approval, policy override, gateway-side tool execution | **Deliberately absent** |

The current attested supervisor service is one prompt / one terminal receipt over
an authenticated local Unix socket. Pi event processing currently forwards only
`agent_settled`/failed-prompt records, so most transcript and tool events are not
available for projection yet. Nothing in this document claims otherwise.

## 3. Architecture

```
browser (untrusted)  ──HTTP/SSE (loopback)──▶  tbound-webchat gateway (untrusted)
                                                   │  bounded projection only
                                                   ▼
                                        projected observation store
                                                   ▲
                            (future) supervisor-owned session/stream service
                                                   ▲
                        Go supervisor: admission · policy · leases · effects · journals
                                                   ▲
                                        official Pi CLI/RPC + mediation extension
```

New, additive code (no authority-owned file is modified):

- `extensions/webchat/internal/webchat/` — the bounded projection contract,
  validation, the gateway store, the HTTP/SSE handler, the receipt importer, the
  legacy workspace assets (`assets/`, deprecated), and the committed React build
  (`dist/`, served under `/app/`). This is an optional extension: the core never
  imports it (see `extensions/webchat/README.md`).
- `extensions/webchat/frontend/` — the React and TypeScript client: `src/` (with the
  DOM-free `core/` and the `markdown/` AST), its scripts, and the pinned lockfile
  (see `frontend/DEPENDENCIES.md`).
- `extensions/webchat/cmd/tbound-webchat/` — the loopback-only gateway
  entrypoint.

## 4. Projection contract

Schema `tbound-webchat-observation/v1`, document schema
`tbound-webchat-document/v1`. A snapshot (`GET /api/v1/snapshot`) has:

- `source`: `fixture` | `live` | `replay` | `unavailable`. Absence of an
  observation is `unavailable`, never a synthetic production record.
- `verification_scope`: `unavailable` | `unverified-input` |
  `structural-audit-only`.
- `session`: bounded identity and lifecycle (`session_id`, `workflow_id`, `title`,
  `status`, `profile`, `pi_version`, timestamps, `turn_count`).
- `panels`: `governance`, `provenance`, `publication`, `recovery`,
  `qualification`.
- `streams`: per-channel health with continuity (`observing`/`complete`/`gap`/
  `unavailable`).
- `items`: the ordered transcript/activity stream. Each item is one of four
  **closed** types, each with a minimal field set:

| item `type` | required fields | forbidden fields |
|---|---|---|
| `message` | `role`, `text` (or `text_truncated`) | tool/verdict/state/digest/… |
| `activity` | `kind`, `tool`, `summary`, `state` | transcript text/verdict/reason… |
| `decision` | `tool`, `tool_call_id`, `verdict`, `reason_code` | transcript text/state/digest… |
| `result` | `tool`, `tool_call_id`, `state` | transcript text/verdict/reason… |

Unknown JSON fields are rejected (`DisallowUnknownFields`), every string is
length-bounded, every identifier matches a bounded reference pattern, and every
enum is closed. A caller cannot smuggle a verdict into a transcript line or a raw
result into a decision.

### 4.1 Success never renders as confirmed unless it is verified

Two independent guards enforce this, and both fail closed:

1. **Go boundary.** Every item must declare its verification scope; the store
   never defaults it, because a defaulted scope could later read as verified. A
   document (fixture/replay/receipt) is forced to `authority=diagnostic` +
   `verification_scope=unverified-input`; the `observed` authority — "projected
   from the supervisor's durable transition" — is refused outside a live
   projection; `authoritative` is refused outright; a document that claims
   containment qualification or an executed research protocol is refused; and
   `live` can never be loaded from a file. An item sequence that is provided must
   be complete and contiguous from 1, so an input discontinuity is refused rather
   than silently renumbered, and the stream epoch is restricted to a safe
   character set because it is interpolated into the SSE `id` line.
2. **Client gate.** `isVerified` requires the exact pair `authority=observed`
   **and** `verification_scope=structural-audit-only`; an empty, `unavailable` or
   otherwise unexpected value is not verified. Panel success additionally
   requires `isVerifiedProjection` (`live` + `structural-audit-only`). An
   unverified success renders as an amber, dashed **`reported`** chip next to an
   explicit textual `unverified`/`reported, not verified` marker, so the claim is
   legible to assistive technology and to copy-paste, not only by colour.

This is the fix for the previously reviewed "unverified replay success
presentation" defect; see §7.

`lossy` is deliberately **not** part of the success gate. It describes the
completeness of bulky output (stdout/stderr, timings, indexes), not the terminal
outcome: a verified `succeeded` result whose *output* was truncated keeps its
reported `succeeded` state chip, and the UI renders a `lossy` chip beside it so
the reader knows the output is incomplete. Because lossy output cannot substitute
for terminal evidence, such a result is also counted and matched under the
`Unknown` filter, and the Trajectory ledger draws its dot and rail segment in the
conservative `unknown` tone (never `ok`), with the accessible text "output lossy,
not counted as confirmed". The chip states what was reported; the dot never claims
more than the evidence supports.

### 4.2 Typed post-run receipt projection

`--receipt <path>` imports a **retained Pi run receipt** — the real terminal
artifact a governed run produces — and projects it into the same bounded
observation contract with `source: replay`. This is the intermediate demo the
handoff anticipated: the original fixture loader could not import the actual
development receipt.

Supported receipt shapes (only these declared fields are read; every other field
in the artifact is ignored and never projected):

- the production receipt (`supervisor/cmd/tbound/production_session_linux.go`,
  `schema_version: tbound-production-pi-receipt/v1`, `mode: production`);
- the development CLI fixture receipt (`mode: dev-fixture`);
- the native-runtime fixture line (`mode: native-fixture`).

The importer **refuses** a receipt that claims `claim_bearing: true`, claims
established containment, uses an unrecognized mode or production schema, records
an unregistered tool, or contains trailing JSON. It reports rather than
fabricates:

- one `system` message summarising the lifecycle counts the receipt records
  (worker ready, prompt admitted, turn completed, proposals, provider turns,
  tool calls/results, generation range);
- one `activity` and one `result` per tool-lineage entry. A result is
  `succeeded` only where the receipt retains a terminal outcome (an observed Bash
  exit code, or an approved delta that advanced the sealed generation); otherwise
  it is `unknown`, with the reason stated in the item. A missing terminal outcome
  is never inferred as success and is surfaced as a `per-tool-outcome` stream
  `gap` plus a warning: missing evidence is not a negative finding;
- panels from the receipt's settlement, publication, generation and provenance
  fields. Provenance stays `available: false` (the gateway has not verified it),
  and the qualification panel keeps the fixed truth
  (`g1=false`, `H1 unexecuted`, `containment not-established`,
  `claim_bearing=false`) that no receipt can change.

The receipt carries no per-tool timestamps, so the file's modification time is
used as the observation time and this is stated in a warning. The terminal
receipt records no per-tool policy verdicts, so no decision counts are projected.
The raw `command_runner_profile` blob is deliberately not projected.

### 4.3 Reviewing a run: posture and finding filters

The conversation toolbar answers the primary governance question — *what was not
a clean allow?* — without re-reading the whole transcript.

- **Posture** badges summarise what the projection observed. Policy verdicts
  (`ALLOW` / `DENY` / `unknown decision`) and execution results (`verified` /
  `reported` / `denied` / `failed` / `unknown result` / `lossy`) are counted
  separately, because a policy `ALLOW` is not a successful execution. An
  unverified success is counted and labelled `reported`, never `verified`.
- **Filters** narrow the timeline to `All`, `Denied`, `Unknown` or `Failed`.
  Each button carries its own count and an `aria-pressed` state. The group is a
  labelled `toolbar` with one tab stop, on the pressed filter; Left and Right move
  between filters and Enter or Space selects one. A filter keeps
  the correlated tool block (activity + decision + result) together, so a matched
  finding is never shown without its execution context, and transcript messages
  are hidden while a finding filter is active. A filter with no matches renders an
  explicit empty state rather than a blank timeline.
- Filtering is presentation-only: it never changes the projection, an
  observation, a verdict or an effect, and it never relabels an `UNKNOWN`.

### 4.4 The Trajectory view: a record ledger with a governance lens

The workspace has two views of one bounded window — `Chat` and `Trajectory`. The
view ring and the trajectory's own controls are presentation-only: neither can
change a projection, an observation, a verdict, an effect, or a filter outcome.
The trajectory view consumes the **existing** contract unchanged; no new field or
endpoint was needed to build it.

- **Ledger.** One row per projected element, in sequence order, grouped by
  `Turn`, `Tool` or `Verdict`. Each row carries a **source chip**
  (`System` / `User` / `Assistant` / `Tool` / `Decision` / `Result`), a **status
  dot**, a bounded payload preview, and the record's trust chips. Grouping and
  search are presentation choices and never drop or reorder the projection.
  There is deliberately **no `Context` chip**: the projection has no context item
  type, and rendering one would present a capability the service does not have.
- **Status dot.** The dot and the chip share one tone name, so they cannot disagree
  about a record's state. Their colours are not identical for every tone: a pending
  chip is amber, while a pending dot is a grey mark. An unverified execution outcome takes the `reported` tone, never
  the confirmed `ok` tone; `lost`, `denied`, `pending` and `unknown` are distinct.
  A policy ALLOW is a permission, not an outcome: it has its own neutral `allow`
  tone and is never drawn in the success colour, so an ALLOW beside a `lost`
  result cannot look like a success. Lossy output never takes the `ok` tone. A
  verdict carries an explicit `unverified` marker when the projection did not
  verify it.
- **Status in words.** The dot is decorative for assistive technology. The
  record's accessible name carries its status instead, for example `Policy verdict:
  ALLOW (permission, not an outcome), not independently verified` or `Result:
  succeeded, reported, not independently verified`, and an UNKNOWN or LOST result
  reads `not success`. Each rail lane's name states its tones the same way. Each
  status is announced once: the dot and the segments carry no separate label.
- **Sequence rail.** A three-lane overview (`Model` / `Policy` / `Tools`) whose
  axis is the **record sequence, not time**. The projection records a timestamp
  and never a duration, so no span is drawn as one and the axis says so. Segment
  widths are bucketed CSS classes, not inline styles (the CSP allows no inline
  style). The rail is always built from every retained record: a search narrows
  the ledger and never hides a DENY from the rail. A message is drawn `idle`,
  distinct from the `muted` span of a lane that did not act at that position. An
  UNKNOWN outcome has its own `unknown` tone; it is never drawn as a lane that did
  not act.
- **Record inspector.** Selecting a record shows its bounded fields:
  `Summary`, `Payload`, `Decision` (verdict, reason code, policy digest),
  `Result` (state, lossy, digest), `Evidence` (authority, verification scope and
  an explicit "independently verified: no") and `Timing`. The Timing section is
  always present and always states that a duration is unavailable, because none
  was recorded. The selected record shows in a bar between the governance tab
  strip and the body. **Close record** clears the selection, keeps an open drawer
  open on its governance panel, and moves focus to that panel's tab. `Escape`
  clears the selection first; a second `Escape` closes an open drawer. Below 1180
  px the inspector is an off-canvas drawer with a scrim: selecting a record opens
  it.
- **Search** reads only allowlisted bounded fields (`id`, `text`, `summary`,
  `detail`, `tool`, `kind`, `state`, `verdict`, `reason_code`, `tool_call_id`,
  `turn_id`). Raw paths, arguments and result bytes are not in the projection and
  can never be matched.
- **Footer** states what was observed (`turns`, `records`, policy counts) and
  marks every metric the projection does not carry as `unavailable` rather than
  rendering a zero. A stated count reads its number, zero included; an absent one
  reads `unavailable`. The footer is the last box of the workspace, directly under
  the composer, at every width; the DOM order is the drawn order. On a phone the
  composer dock covers the footer only while the chat scrolls; at the page's end
  the footer sits below the dock.

### 4.5 Following the tail (client-only)

At 761 px and wider the transcript scrolls inside the Chat view, above the composer,
and the ledger scrolls inside its own view. At 760 px and below the page scrolls both. A reader within 40 px of the bottom keeps following new records. A reader who has scrolled away keeps their
place: the first record visible at the top of the viewport is held across each
re-render. Records applied while they are away are counted on a `N new records ·
Jump to latest` control in that view, which moves them to the tail and moves focus
to the list. The count comes only from records the store applied in order from the
stream; a gap, a duplicate or a snapshot load never adds to it. A burst of arrivals
is announced once, through the status region, after it settles. The first load
opens at the top of the transcript.

Limits, stated plainly: the count is exact only for live arrivals. Records that a
reconnect snapshot delivers are not counted, so after a reconnect the count is a
lower bound. Trimming the oldest records while the reader is away moves the content
shown, and the held record keeps its place only if it is still in the window. The
arithmetic is covered by `jstest/anchor.test.mjs`; the scroll and focus behaviour is
browser-only and is not covered by the Node tests.

### 4.6 Assistant messages as markdown

Only assistant text is rendered as markdown; user and system text stays literal.
The parser is a bounded subset built without an HTML parser: source text is never
parsed as markup, and the DOM is made only with `createElement` and
`createTextNode`. Supported: paragraphs, ATX headings, fenced code, inline code,
bold, italic, bulleted and ordered lists, blockquotes and `[text](url)` links. A
link is created only for `http`, `https` or `mailto`; other schemes stay as text,
and an image renders as its source text.

Bounds: 64 KiB of source, 512 top-level blocks, and a nesting depth of 4 for
blockquotes and lists. When the text is cut, the message carries a notice such as
`Message truncated for display: …`.

Known simplifications:

- fenced code inside a list or a blockquote is split out of its container;
- a loose list (blank lines between items) is split into separate lists;
- there is no autolink: a bare URL is text, not a link.
- headings are demoted so they sit below the page headings: `#` renders as `h3` and `##` as `h4`.

While a message streams, only the blocks whose source changed are re-rendered.

### 4.7 Stop sequence (written, not wired)

`stop-sequence.mjs` is a pure two-press Escape state machine: a first press arms a
request for the turn on screen, and a second press within 2 s confirms it. It is
**not wired**. No module imports it, the shell does not reference it, and
`handler.go` does not serve it. It waits for the milestone-3 cancel API, which the
core thread owns.

Its wording is fixed. It never says a turn was cancelled, stopped or settled.
Cancellation is a request; the settled result comes from the supervisor, and an
uncertain effect stays `UNKNOWN`. A confirmed press yields a cancel-request intent
for the caller to send. Nothing is re-sent on a reconnect, and nothing is retried.

## 5. API endpoints

`GET` and `HEAD` are answered on `/` and `/favicon.ico`; every other route is `GET`-only.
The routes reject any request body. Any other method gets `405` with an `Allow` header
(`Allow: GET, HEAD` on `/` and `/favicon.ico`, `Allow: GET` elsewhere). A foreign `Host`
or `Origin` gets `403` on every route.

| Route | Purpose |
|---|---|
| `GET` or `HEAD /` | `302` to `/app/`. The target is a constant: the query string and the rest of the request never reach `Location`, so it is not an open redirect. If the React app cannot be derived, `/` serves the legacy workspace (`200`) with a fallback notice instead. |
| `GET` or `HEAD /favicon.ico` | `204`, with no body and no `Content-Type`. |
| `GET /legacy/` | The deprecated legacy workspace, with a deprecation notice above its content. It is removed after sign-off. |
| `GET /static/*.css`, `/static/*.mjs` | Closed allowlist of the legacy workspace's modules. |
| `GET /app/`, `/app/*` | The React client build ([frontend.md](frontend.md)). Exact files come from the committed Vite manifest; `503` if the table cannot be derived. |
| `GET /api/v1/snapshot` | Bounded observation snapshot (recovery point for a gap). |
| `GET /api/v1/contract` | Bounded contract descriptor (schemas, enums, limits, `read_only: true`, `controls: []`). |
| `GET /api/v1/items/{id}` | One retained projected item by bounded ID. |
| `GET /api/v1/events?cursor=<epoch:seq>` | Cursor SSE: `item` and `session` events, `event: gap` on a cursor that cannot be served, `: keepalive` every 15s. |

There is no session-control, launch, cancel, approval, or execute route.

### 5.1 Cursor and gap semantics

Cursors are `epoch:sequence`. A duplicate delivery is idempotent (ignored by ID);
an out-of-order or stale item is ignored. Any discontinuity — a sequence gap, an
evicted window (`history_evicted`), an epoch change, an invalid cursor, or a
cursor ahead of the store — produces an explicit gap: the client **discards
continuity and re-fetches the snapshot**. A missing element is never rendered and
never inferred as success. Snapshot-recorded gaps are shown as a `continuity gap`
notice.

## 6. Security posture

The gateway is loopback-only and read-only by construction:

- `--addr` must be a literal loopback IP; binding `0.0.0.0`, a host name, or a
  remote address is refused before listening.
- `Host` must match the bound address (or be loopback); any non-empty `Origin`
  must equal the configured origin.
- `GET` only; any request with a body (`Content-Length != 0` or a transfer
  encoding) is rejected `400` rather than read. The rejection is
  **timeout-bounded**, not drain-free: `net/http` may still consume a bounded
  amount of an unread body while closing, and the server's `ReadTimeout` is what
  prevents an unbounded stall. `Content-Security-Policy` is strict and does
  **not** allow inline script or style (`script-src 'self'; style-src 'self'`),
  with `default-src 'none'`, `frame-ancestors 'none'`, `base-uri 'none'`.
- Static assets are an exact allowlist; there is no directory serving.
- Each SSE/data write is bounded by a deadline that also covers the socket flush.
- Identifiers are a strict character set (no path separators), display text
  rejects control characters other than LF/TAB (including the C1 range), and
  receipt provenance/status values are echoed only when they are bounded
  identifiers. **Residual risk, stated plainly:** the projection *bounds*
  operator-supplied prose (transcript text, summaries, details, notes) but does
  **not** content-redact it, so a fixture or receipt the operator chooses to load
  can echo sensitive free text. A production projection must emit structured safe
  summaries upstream; the gateway cannot infer which prose is sensitive.

Remote/multi-user deployment is explicitly out of scope here: it would require
browser authentication, session ownership, and CSRF/origin protection, none of
which exist yet. Do not expose this gateway beyond loopback. The thesis road map
allows remote/browser access only through an existing secure channel, never a new
custom control plane.

## 7. Previously reviewed defects: fixes and the tests that lock them

| Defect | Fix | Test |
|---|---|---|
| (a) Unverified replay success presented as confirmed success | Go downgrade + client `isVerified`/`isVerifiedProjection` gate rendering `reported` chips | `TestFixtureStoreDowngradesToUnverifiedDiagnostic`, `TestDocumentRefusals`, `jstest/render.test.mjs` ("unverified success never renders with the confirmed-success chip", "unverified publication is presented as reported") |
| (b) SSE deadline cleared before flush, so a write could outlive it | `boundedWrite` sets the deadline, writes, **flushes**, then clears | `TestBoundedWriteFlushesBeforeClearingDeadline` (asserts order `set → write → flush → clear`) |
| (c) Unbounded/slow GET request bodies | A body-bearing request is rejected without being parsed, and the server's read timeout bounds the stall | `TestHandlerRejectsMethodsOriginsAndBodies` |

Three further properties are locked by tests: per-type field smuggling is refused
(`TestPerTypeFieldSmugglingRefused`), a gap or duplicate cannot invent success
(`jstest/store.test.mjs` "a gap is reported…", "a missing terminal result cannot
become a success display"), and the gateway exposes no control route
(`TestHandlerRejectsMethodsOriginsAndBodies`).

### 7.1 Additional fixes from the second review

| Finding | Fix | Locking test |
|---|---|---|
| Success verification could fail open on a missing scope | Store requires an explicit scope and never defaults it; the client gate requires the exact `observed` + `structural-audit-only` pair | `TestAppendItemRequiresExplicitVerificationScope`, `TestValidateSnapshotRefusesObservedAuthorityOutsideLive`, `jstest/format.test.mjs` ("the verification gate fails closed") |
| Unverified panel/session success was labelled only by colour | Publication and recovery render an explicit `trust` row; the session badge and success chips carry textual `reported`/`not verified` markers | `jstest/render.test.mjs` ("unverified panel and session claims carry an explicit textual trust label") |
| Receipt projection invented per-tool success/approval | Only a settled Bash exit maps to success; other calls stay `UNKNOWN` with a factual summary; `STOPPED` is recognised and `tool_sequence` is supported | `receipt_test.go` (`TestProjectDevelopmentReceiptIsHonest`, `TestReceiptToolSequenceFallback`) |
| Slow/incomplete GET bodies could still stall a connection | A bounded `ReadTimeout` is set and a body-bearing request is closed rather than drained | `TestHandlerRejectsMethodsOriginsAndBodies` |
| A session/panel update could be lost across a reconnect | The client re-fetches a fresh snapshot on stream re-open and `UpdateStream` notifies subscribers | `handler.go`/`store.go`; verified in the running workspace |
| Store state could be mutated through a returned snapshot/event | Panels (including the observation-time pointer) are deep-copied on ingress and egress; events are cloned per subscriber | `TestStoreDeduplicatesAndBroadcasts` |
| Unbounded client history | The client history is capped at `MAX_CLIENT_ITEMS` with oldest-first eviction | `jstest/store.test.mjs` |
| Over-limit subscribers still received a full replay | The over-limit subscription returns no replay window | `TestSubscriberLimitWithholdsReplay` |
| Dead session button, always-set `aria-expanded`, focus loss on resize | The current session is a non-interactive row; `aria-expanded` is set only while collapsible; focus leaves the panel before it is made inert; a dedicated status region announces gaps once | `jstest/render.test.mjs`, keyboard audits |

### 7.2 Fixes from the third review

| Finding | Fix |
|---|---|
| A reconnect snapshot could overwrite newer live events | Superseded by the snapshot-first policy in §7.3: each connection closes the old stream, applies a fresh snapshot, then opens from that snapshot's cursor. Events from a replaced stream are ignored. Gap announcements are keyed by gap identity |
| A cursor could disagree with the item sequence | `ValidateSnapshot` refuses a cursor that does not match the last provided sequence |
| Client eviction was silent | The notices region and the filter summary both disclose the retained window and the evicted count |
| Identifiers admitted path separators; text admitted control characters | Identifiers (item/turn/tool-call/session/workflow/profile/stream/gap and provenance labels) use a stricter pattern with no `/`; digests keep the wider reference pattern; display text rejects control characters other than LF/TAB, including the C1 range; receipt disposition, settlement/publication status, generation labels and command containment status are echoed only when they are bounded identifiers |
| LOSSY results were invisible to the Unknown filter; `pending` was counted as unknown | `lossy` is matched and counted independently; `pending` has its own posture count |
| The filter summary called context elements "matches" | It now reports `N matches · M of T shown (context included)` |
| An uncorrelated activity could be dropped by a filter | Activities must carry a `tool_call_id`, so every tool block is correlatable |
| Stream health was not carried on session events | Session events include the stream projection and the client applies it |
| A production receipt could borrow the development Bash settlement field | `bash_settlement` is read only for the dev/native shapes |
| A subscription shared the store's panel pointer | Subscription panels are deep-copied on egress |
| `subscriber_limit` could trigger a reconnect loop | The client treats it as terminal and reports it instead of retrying |
| "Closed without draining" overstated the guarantee | The rejection is documented as timeout-bounded, not drain-free |
| Noisy live regions | The filter summary is no longer a live region; connection changes are announced once |
| The desktop Governance control had no effect | It is hidden above the breakpoint, where the panel is always visible |

The typed viewer in the sibling `codex/tbound-governance-companion` worktree
informed this contract and validation/store design (bounded records, closed
enums, identifier/text bounds, cursor model, untrusted-document downgrade, and
the three defects above). The visual design is entirely new: a three-pane chat
workspace with a transcript/tool timeline, a governance inspector, a persistent
claim-boundary strip, and explicit source/verified/gap labelling. No file from
that checkout was copied verbatim except the platform-specific
safe-no-follow document opener, which is retained unchanged.

### 7.3 Reconnect policy (snapshot-first, implemented)

`stream.mjs` holds the connection lifecycle as a DOM-free state machine, and
`backoff.mjs` holds the timing. The browser's own EventSource retry is not used,
so the delay can be capped, jittered, suspended offline, and reset only after a
stable connection.

- **Snapshot first.** Every connection closes any open stream, fetches a snapshot,
  applies it, and only then opens the stream from the snapshot's cursor. A
  reconnect never resumes from a cursor the client has not just verified, and
  nothing retries an effect. Events from a stream that has since been replaced are
  ignored.
- **Backoff.** The delay is `500 ms × 2^attempt`, capped at 10 s, and jittered
  uniformly into 50–100% of that value. Clients that lost the gateway together do
  not retry in lockstep, and no delay exceeds the cap.
- **Stable open resets the counter.** The attempt counter resets only after the
  stream has stayed open for 5 s (`STABLE_OPEN_MS`). A server that accepts and then
  drops at once does not reset the backoff.
- **Slow handshake.** After 3 s (`CONNECT_WARN_MS`) the status reads
  `slow handshake: still connecting`. The warning never cancels the attempt.
- **Offline.** When the browser reports offline, retries are suspended and no timer
  runs; the status reads `offline`. On `online` the client reconnects at once with
  the backoff reset. An open stream is left alone.
- **Subscriber limit is terminal.** The gateway's subscriber limit is reported and
  never retried automatically.
- **Window gaps.** A gap recovery alone does **not** set `windowGap`. A complete
  snapshot restores the window, and the notice says the gap was recovered.
  `windowGap` (the `Earlier history not loaded` marker) is set only when:
  1. a replacement snapshot drops retained elements (source window, from the first
     dropped sequence);
  2. the client cap trims the oldest retained elements (client window, from the
     first trimmed sequence); or
  3. the snapshot itself declares `history_evicted` (source window, from its first
     missing element).

  It is cleared only by a snapshot that restores an element at or before the
  earliest lost one and declares no eviction. A live item never clears it.

## 8. Presentation

The current visual spec is [design/design-language.md](design/design-language.md). The
current static design mockup is `webchat/mockups/tbound-redesign-v3.html` (demo data,
offline). The running UI is `/app/`. The subsection below records the earlier token and
component vocabulary.

### 8.1 Presentation design system

The token names and component shapes follow the **coss/ui naming and pattern
vocabulary** (the Cal.com/shadcn-compatible CSS variable names, including the
extra `--destructive-foreground` and `--info`/`--success`/`--warning` tokens, and
the component conventions). This is a naming and pattern reference only: no
coss/ui code, CSS or package is a dependency or is copied. The conventions are
`Card`-style bounded surfaces, outline and semantic
`Badge`/chip variants, `Alert`-style notices with an icon, `Empty`/`Skeleton`
loading states, `Kbd`, and a sidebar with muted hover/active rows. The timeline
filter is a segmented Toggle Group built from `aria-pressed` buttons with roving
arrow-key focus, and the posture row uses the badge/chip shapes.

No React, Base UI or Tailwind is used in the legacy workspace served at `/legacy/`.
The thesis treats a browser client as a deferred, non-claim-bearing demonstrator and
requires the web service to stay minimal, so the legacy workspace is a zero-build
ESM + CSS client served by the Go gateway. It was the default until the React client
became the default (2026-10-14); it is now deprecated and will be removed after
sign-off. [ADR-0001](adr-0001-optional-extension.md) supersedes
that zero-build rule for Phase 1 onward: React and TypeScript with the project's
own components, CSS Modules compiled to same-origin CSS, and a committed Vite
build. The tokens are defined in `:root`. Fonts degrade to local Inter / Geist Mono
when installed and otherwise fall back to system stacks; no webfont is fetched,
which keeps the strict `font-src 'self'` policy and offline operation intact.

## 9. Running it

Requires the pinned Go toolchain (module `tbound/webchat`, in
`extensions/webchat/`) and Node for the browser-side tests.

```sh
cd extensions/webchat
go vet ./internal/webchat/... ./cmd/tbound-webchat/...
go test ./internal/webchat/... ./cmd/tbound-webchat/...
node --test internal/webchat/jstest/*.test.mjs
go run ./cmd/tbound-webchat --demo                 # embedded fixture, port 8791; open / (React), /legacy/ is deprecated
go run ./cmd/tbound-webchat                         # unavailable projection
go run ./cmd/tbound-webchat --replay path.json      # retained observations
go run ./cmd/tbound-webchat --receipt path.json     # import a real run receipt
go run ./cmd/tbound-webchat --receipt-demo          # embedded example receipt
go run ./cmd/tbound-webchat --live                  # refused: supervisor service absent
```

`--demo`, `--fixture <path>`, `--replay <path>`, `--receipt <path>`,
`--receipt-demo` and `--live` are mutually exclusive. The startup banner prints
the source class and the claim boundary; it never prints fixture paths, controls,
or secrets.

### 9.1 Demo modes and labels

- `--demo` — the embedded `fixtures/demo-workspace.json`: a full turn with
  `ALLOW`/`DENY` decisions and `succeeded`/`denied`/`unknown` results, all marked
  `FIXTURE` and `unverified`.
- `--replay fixtures/replay-gap.json` — a retained observation with a `CONTINUITY
  GAP` notice and an `UNKNOWN` result.
- `--receipt <path>` — a **real** retained run receipt. Pointing it at the
  development CLI fixture receipt produced by the pinned supervised run renders
  the lifecycle summary, four tool cards (`edit` and `bash` reported as
  `succeeded`, both `read`s as `unknown` because the receipt retains no terminal
  outcome), a `per-tool-outcome` stream `gap`, `unknown` settlement, a
  `not-attempted` publication, and the fixed claim boundary.
- `--receipt-demo` — the embedded synthetic example receipt (`fixtures/example-receipt.json`).
- no flag — the `UNAVAILABLE` projection: no fabricated rows, no stream.

### 9.2 Visual QA and keyboard receipts

These captures were made while `/` still served the legacy workspace (before `6d03657`).
The React client's captures and receipts (`p3-polish-*`, `p3b-*`, `p3c-*`, `v3-real-app-*`)
are listed in [frontend.md](frontend.md). Screenshots (captured with headless Chrome against
the running gateway) are in [`webchat/qa/`](webchat/qa/): `fixture-desktop`, `fixture-expanded-tools`,
`fixture-denied-filter`, `replay-continuity-gap`, `receipt-real-desktop`,
`receipt-real-expanded-tools`, `unavailable-desktop`, `mobile-conversation`,
`mobile-inspector`. Keyboard audits driven over the DevTools protocol are in
`webchat/qa/keyboard-audit-desktop.json` and `keyboard-audit-mobile.json`. They
were recorded before the redesign follow-up, so they describe that build. They
verify tab order (skip link → governance toggle → session → timeline filters →
each tool disclosure), `Enter` toggling a tool disclosure, `Enter` opening the
governance drawer and `Escape` closing it, `Enter` selecting a timeline filter
with focus surviving the re-render, and `ArrowRight` moving within the toggle
group. The audits also record the live `innerWidth` and the media-query result so
each receipt is self-describing. At narrow widths the closed drawer is `inert` +
`aria-hidden` so keyboard focus cannot reach off-canvas content, and becomes
focusable only when opened. Rendering preserves keyboard focus on the tool
disclosure or filter button a user was operating, and a render error cannot block
data loading.

## 10. Verification receipts

Current numbers at HEAD `57e6406` (Linux, WSL, Go 1.27.1, Node 24.15.0): vitest 896 passed in
60 files, 0 skipped; extension Go `-race` 54 top-level tests PASS, 0 SKIP (132 PASS including
subtests, from the `-v` run recorded in [phase3-parity.md](phase3-parity.md)); legacy node
tests 258 pass, 0 skipped; boundary guard OK. The receipts below are the historical record of
earlier increments. The React client's receipts are in [frontend.md](frontend.md).

Focused checks on the pinned Go 1.27.1 in Linux, run from `extensions/webchat/`
(entries from before the extraction ran from `supervisor/`):

- `gofmt -l .` — no output.
- `go build ./...` — exit 0.
- `go vet ./internal/webchat/... ./cmd/tbound-webchat/...` — exit 0.
- `go test -count=1 ./internal/webchat/... ./cmd/tbound-webchat/...` — ok.
- `go test -race -count=1 ./internal/webchat/... ./cmd/tbound-webchat/...` — ok.
- Tail-window and status-in-words increment: `gofmt -l` (no output), `go vet`, and
  `go test -race -count=1 ./internal/webchat/... ./cmd/tbound-webchat/...` on Go 1.27.1
  (WSL) all exit 0, with no skipped tests. `TestStaticAssetsAreClosedAndComplete`
  passes with the new `anchor.mjs` route.
- `node --test internal/webchat/jstest/*.test.mjs` — 258 pass, 0 skipped at HEAD
  `4700ca5` (229 at the tail-window increment, `3146d1b`). These cover
  the pure modules and the DOM-stub renderer; browser scroll, focus and keyboard
  behaviour is not covered by them.
- The **real** development CLI fixture receipt was imported with
  `--receipt` and rendered in the running workspace: source `replay`, session
  `unknown`, four tool cards (two reported `succeeded`, two `unknown`),
  `per-tool-outcome` stream `gap`, publication `not-attempted`, recovery
  `unknown`, and no browser console errors.
- Before the extraction, from `supervisor/`: `go test -race -count=1 ./...` — all packages pass except
  `internal/evaluationindex`, which fails only because the private Linux copy
  excluded the repository `docs/` tree (it passes from within the full checkout).

These are component and UI checks. They establish no policy, effect, containment,
publication, `G1`, or `H1` result.

## 11. Next integration steps (proposed to the core thread)

The integration sequence is: **persistent multi-turn service and cancellation →
authenticated snapshot/history/event streaming → live web chat.** The gateway is
already shaped for step 2: a service-owned projection only needs to publish
snapshots and cursor events matching §4 and the
[contract proposal](web-chat-contract.md). Milestone 2 (read integration) and
Milestone 3 (interactive integration) require supervisor service changes that are
owned by the core thread; this gateway will consume them, not implement them.

## 12. Deferred from the harness survey

The harness survey ([UI plan, Appendix B](web-chat-ui-plan.md)) named two items this
increment does not build.

- **Diff viewer: deferred.** The projection has no bounded diff field, so there is
  nothing safe to render. A contract change would need: bounded patch hunks, each
  with a per-hunk size cap; an explicit truncation flag per file and per hunk; and a
  path allowlist with redaction, decided by the supervisor rather than the browser.
  Until those exist, a file change is shown only as its bounded summary.
- **Slash-command listbox: no target.** A `/` listbox needs a composer and a command
  menu. Neither exists: the composer is read-only and there is no command set. No
  listbox is built, and none is a claim this client makes.

The survey's other "avoid" items are also not adopted: `aria-live="polite"` on the
ledger, any `innerHTML` sink, and search over full message bodies.
