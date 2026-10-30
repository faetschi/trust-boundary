# Web chat projection service — contract proposal

**Status: implemented for v1 live mode (P9).** `tbound-webchat --live --socket PATH`
serves a live projection of the product service over `tbound-product-protocol/v1`.
[Section 9](#9-implemented-for-v1-live-mode-p9) states what the gateway does and which
fields it could not map. Sections 1 to 6 are the original proposal. Its projection
shape (section 2) is what the live gateway renders, and the product protocol answers
its open questions (section 6). Section 8, the tail `anchor`, is still a proposal. The
fixture, replay and receipt modes remain demonstrations.

Original text: a proposal to the core thread that owns the supervisor. **No
authority-owned file was modified for the frontend increment.** It specifies the small,
additive service surface the untrusted chat gateway needs so that
[read integration](web-chat.md) can be completed without moving any authority
into the browser.

## 1. Goal and non-goals

The gateway needs a **read** projection of governed session/turn activity plus
**request** submission and cancellation. It must not need policy, lease, effect,
journal, or publication authority.

- In scope: a bounded projection of already-durable transitions, and requests
  routed to the existing authenticated supervisor authority.
- Out of scope: any new effect path, any gateway-side policy, any manual per-tool
  approval, any raw journal/provider/credential/path exposure, any remote
  unauthenticated service.

Sequencing (from the frontend review): **persistent multi-turn service and
cancellation → authenticated snapshot/history/event streaming → live web chat.**
Steps 1–2 are supervisor-owned.

## 2. The projection the gateway already consumes

The gateway is built around this shape and validates it strictly
(`extensions/webchat/internal/webchat/contract.go`). A live projection should produce the
same shape, with `source: "live"` and `verification_scope:
"structural-audit-only"` (values already defined).

### 2.1 Snapshot

`GET /api/v1/snapshot` (gateway) ← a service-owned snapshot containing:

- `source`, `verification_scope`, `epoch`, `cursor`, `generated_at`
- `session`: `session_id`, `workflow_id`, `title`, `status`, `profile`,
  `pi_version`, `created_at`, `updated_at`, `turn_count` (optional, §2.1.1)
- `panels`:
  - `governance`: `policy_digest`, `allow_count`, `deny_count`, `unknown_count` (each count optional, §2.1.1)
  - `provenance`: `profile_id`, `profile_hash`, `source_digest`,
    `containment_profile`, `containment_digest`, `provider_profile`,
    `runtime_closure`, `tcb_proof_digest`, `tcb_proof_sequence`
  - `publication`: `state`, `transaction_id`, `observed_at`, `detail`
  - `recovery`: `state`, `generation`, `last_completed_sequence`, `detail`
  - `qualification`: `g1`, `h1`, `containment`, `claim_bearing`
- `streams[]`: `id`, `label`, `health`, `available`, `reason`
- `items[]`: the ordered bounded union in §2.2
- `gaps[]`: durable continuity gaps

### 2.1.1 Absent versus zero

A count the source does not state is **absent**: the gateway omits its key. A stated count is written, and **zero is a stated
count**. A client reads an absent count as `unavailable` and a stated zero as `0`. Nothing else decides between them: a policy
digest, a sibling count, or a turn count inferred from other fields never makes an absent count zero or a zero absent.

| Field | Wire | Absent means | Zero means | Where the gateway sets it |
|---|---|---|---|---|
| `session.turn_count` | optional integer, ≥ 0 | the source states no session turn count | no turns | live projection: stated. Receipt projection: never stated (`provider_turns` counts provider exchanges, not turns, and `turn_completed` is a flag). Unavailable session: not stated |
| `panels.governance.allow_count` | optional integer, ≥ 0 | the source states no ALLOW count | no ALLOW decisions observed | live projection: stated. Receipt projection: never stated (the terminal receipt records no per-tool verdicts) |
| `panels.governance.deny_count` | optional integer, ≥ 0 | the source states no DENY count | no DENY decisions observed | as `allow_count` |
| `panels.governance.unknown_count` | optional integer, ≥ 0 | the source states no UNKNOWN count | no UNKNOWN decisions observed | as `allow_count` |

A negative, fractional or non-numeric count is refused. The gateway never writes `null`; the client refuses `null`, and the
gateway reads it as absent. Sequence positions are not counts: `tcb_proof_sequence`, `last_completed_sequence` and `earliest`
are omitted when 0 (sequences are 1-based, so 0 is no position), and a client reads their absence as not stated. Served
`items[].sequence` values are always written, numbered from 1.

### 2.2 Item union (each item is `sequence`, `id`, `type`, `turn_id`,
`occurred_at`, `authority`, `verification_scope`, and type-specific fields)

- `message`: `role` (`user`/`assistant`/`system`), `text`, `text_truncated`
- `activity`: `kind`, `tool` (`read`/`write`/`edit`/`bash`), `summary`, `state`,
  optional `tool_call_id`, optional `detail`
- `decision`: `tool`, `tool_call_id`, `verdict` (`ALLOW`/`DENY`/`UNKNOWN`),
  `reason_code`, optional `policy_digest`, optional `summary`
- `result`: `tool`, `tool_call_id`, `state`
  (`pending`/`succeeded`/`denied`/`failed`/`unknown`/`lost`), optional `summary`,
  `detail`, `digest`, `lossy`

**`lost` (presentation state, reserved).** `lost` means: the reader holds an effect record whose terminal outcome was not recorded in the retained observation. It is a display label only. The supervisor never emits it (the supervisor records SUCCEEDED, DENIED, FAILED, or UNKNOWN; a missing terminal record is `Unresolved` and is treated like UNKNOWN). The gateway never assigns `lost` today: a call without a retained outcome is projected as `unknown` (internal/webchat/receipt.go). The state is therefore reachable only from injected data (QA scripts). It is never success, never counts as prevention, and never triggers replay. Open owner decision: keep `lost` as a reserved state, or remove it and use `unknown`.

Every string is bounded; every identifier matches a bounded reference pattern;
each type rejects fields from other types. `authority` must remain `diagnostic`
or `unknown` in the presentation contract — the projection is an observation, not
an authority assertion, and `authoritative` is refused at the gateway boundary.

### 2.3 Cursor events

`GET /api/v1/events?cursor=<epoch:sequence>` (gateway) ← a cursor stream of:

- `item` events: one item, monotonically increasing `sequence`
- `session` events: a new `session` + `panels` projection
- `gap` events: `reason`, `requested_cursor`, `epoch`, `earliest`, `latest`

## 3. Required properties

1. **Durable-before-published.** An observation must be published only after its
   corresponding authoritative transition is durable (intent → decision → lease →
   effect → terminal/`UNKNOWN` → checkpoint). Never publish optimistic success.
2. **Monotonic cursor.** `sequence` strictly increases; duplicates are tolerated
   (the client dedupes by ID) but gaps must be reported as a `gap` event, not
   papered over. A gapped client recovers from a fresh snapshot.
3. **No success inference.** `succeeded` requires a terminal record.
   `UNKNOWN` and `LOSSY` are distinct from success. A missing terminal record
   must produce `unknown`, never `succeeded`.
4. **Bounded fields only.** No raw journal lines, credentials, provider payloads,
   arbitrary paths, or unrestricted tool arguments/output. Digests are references
   to authoritative artifacts, not the artifacts.
5. **Explicit absence.** No live source ⇒ `source: "unavailable"`, not empty
   fabricated rows.
6. **Label fidelity.** `verification_scope` must reflect what was actually
   verified. Do not send `structural-audit-only` for unverified input.

## 4. Requests the gateway will route (implemented in v1 live mode, section 9)

The browser never talks to the supervisor. The gateway must submit through the
existing authenticated authority:

- `POST /api/v1/sessions/{id}/turns` → a turn request. The service must enforce
  admission/policy; the gateway only forwards a bounded prompt and operator scope
  supplied by the supervisor-side configuration (the browser must not set scope).
- `POST /api/v1/sessions/{id}/turns/{turnId}/cancel` → a cancellation **request**.
  The gateway displays the settled result. Uncertain effects/cleanup stay
  `UNKNOWN`; reconnect never retries an effect.

The gateway itself must authenticate to the supervisor (the existing
peer-credential/UID model), and — before any non-loopback deployment — implement
browser authentication, per-session ownership, and CSRF/origin protection. This
is a prerequisite, not an optional hardening step.

## 5. Mapping from existing supervisor artifacts

| Projection | Existing source |
|---|---|
| `decision` items | `internal/gate.Decision` (`verdict`, `reason_code`, `policy_digest`, `tool_call_id`, `tool`, `sequence`) |
| `result` items | `internal/broker/protocol.Result` (`verdict`, `reason_code`, `output` → **not** projected; only a bounded summary/digest) |
| `activity` items | adapter proposals/`internal/ipc` frames (tool + canonical summarised intent) |
| session/provenance panels | `productionPiReceipt` (`profile_*`, `containment_*`, `frozen_source_digest`, `provider_profile`, `tcb_proof_*`, `source_generation`, `final_generation`, `settlement`, `publication_status`) |
| `publication` panel | receipt `publication_status` + `publication_transaction_id` |
| `recovery` panel | settled/`UNKNOWN` settlement and Store recovery disposition |
| `qualification` panel | fixed current truth: `g1=false`, `h1="unexecuted"`, `containment="not-established"`, `claim_bearing=false` |

`claim_bearing` is hard-coded `false` in the current receipt and must remain so.
The receipt carries no `G1`/`H1` qualification fields; those require separate
authoritative provenance and must not be synthesized by the projection.

The receipt importer in §4.2 of the [workspace document](web-chat.md) already
consumes the **real** development receipt, so this mapping is exercised against
an actual artifact rather than a fixture. It also demonstrates the boundary the
service must preserve: the terminal receipt carries no per-tool verdicts and no
per-item timestamps, so those must be published by the projection service when
they become available, and must never be inferred.

## 6. Open questions for the core thread

1. Should the projection be served over the existing authenticated Unix socket
   (one new request kind + a subscription kind), or in-process by the same binary
   that already owns the journals? The gateway prefers a **service-owned
   projection** over reading raw journals itself.
2. What is the authoritative source for `turn`/`message` boundaries given that Pi
   RPC handling currently forwards only `agent_settled` and failed prompts? If
   transcript text cannot be bounded safely yet, the projection should expose
   activity/decision/result items first and mark transcript coverage as `gap`.
3. Which ID should be the projection `session_id` — the service `task_id`, the
   supervisor `run_id`, or a new stable session ID? The gateway treats it as an
   opaque bounded identifier.
4. Confirmation that `verification_scope: "structural-audit-only"` is the correct
   label for projection items derived from the durable journals, and that items
   derived from Pi self-report must remain `unverified-input`.

## 7. Status

This is a proposal. The gateway currently ships `fixture`, `replay`, and
`unavailable` sources only; `--live` fails closed. No shared service file was
edited.

## 8. PROPOSAL, not implemented: a tail `anchor` on the snapshot

**Status: proposal for the core thread. Nothing in the gateway or the client
sends, reads or validates this field.**

Today the snapshot's `cursor` already carries the last provided sequence
(`epoch:sequence`). The client keeps its own tail pin, so a reader's place is lost
when the window is replaced, and records a reconnect snapshot delivers are not
counted as new. The proposal adds one additive, optional field that names the tail
at snapshot time:

```json
"anchor": { "epoch": "e1", "sequence": 7, "item_id": "r2" }
```

- `anchor` is the last provided item at snapshot time. It is omitted, or `null`,
  when the snapshot has no items.
- `sequence` and `epoch` must match the snapshot's `cursor` and the last item's
  sequence. `item_id` names that item. The gateway would refuse a mismatch the same
  way it refuses a cursor that disagrees with the item sequence today.
- It is a locator, not an authority. It grants no success, verification or ordering
  that the items do not already carry.
- With it, a client that pinned the tail can count the records that arrive by
  snapshot as new, by sequence after the pinned item, and can find the pinned record
  by `item_id` in a new window. Today those records are not counted.

What it needs before any producer can send it:

- `contract.go` must declare and validate the field on `Snapshot`. The document
  decoder (fixture, replay, receipt) refuses unknown fields. No live snapshot decoder
  exists yet, so how it treats an unknown field is still open.
- The gateway client must read it and keep its pin by identity. Until then the
  field is inert.

Open questions for the core thread: whether `item_id` should be required once a
window has been trimmed (the pinned record may be evicted first), and whether the
anchor should also ride on `session` events.

## 9. Implemented for v1 live mode (P9)

**Status: implemented.** The contract is `docs/product-contracts.md`, "Milestone 6: live
web chat (P9)", over `tbound-product-protocol/v1`. This section records what the Go
gateway (`internal/webchat/live*.go`, `socket.go`, `wire.go`) does with it.

### 9.1 Transport and authority

- `tbound-webchat --live --socket PATH [--listen 127.0.0.1:8791]` binds a literal
  loopback address. `--addr` is the older spelling of `--listen`.
- Each browser request becomes one product protocol call: a new connection to the Unix
  socket, one length-prefixed JSON request frame (4-byte big-endian length, at most
  64 KiB), one response frame (at most 1 MiB), strict decoding on both sides (no
  duplicate keys, no unknown fields, no trailing data). A call times out after 30 s, or
  after the requested wait plus 10 s for an observe.
- The gateway never retries and never invents a request key. A transport failure is a
  `503 SERVICE_UNAVAILABLE`. When the connection was made before it failed, the answer
  also says `"outcome_unknown": true` for a mutation: the browser may retry only with
  the same `request_key`. At most 32 calls are open at once.
- The service sees the user's UID as the principal. The gateway holds no policy, lease,
  effect or publication authority. The module keeps its own copy of the wire types
  (`wire.go`). `testdata/protocol/*.json` are generated by the supervisor test
  `supervisor/internal/productprotocol/fixtures_test.go` (run it with `-update` to
  regenerate). `TestProtocolFixturesRoundTripThroughTheWireCopy` decodes each file
  strictly into the copy and re-encodes it to the same bytes, so drift on either side
  fails a test.

### 9.2 Pairing and request checks

1. At start the gateway prints `http://127.0.0.1:PORT/pair#TOKEN` to stdout. The token
   is 32 random bytes (base64url), kept only as a SHA-256 hash, valid for 10 minutes,
   and written nowhere else. Do not keep a redirect of stdout.
2. `/pair` is a fixed page and `/pair.js` a fixed script. The script reads the token from
   the fragment, clears it from the address bar, posts it to `POST /api/v1/pair`
   (`{"token": "..."}`) and opens `/app/`.
3. A correct token is spent by its first use. The answer sets the `tbound_session` cookie
   (random 32 bytes, HttpOnly, SameSite=Strict, Path=/, no Domain, 12 hours) and returns
   `{"csrf_token": "...", "expires_at": "..."}`. A wrong token changes nothing. One
   gateway process pairs one browser, so a session that expires needs a restart.
4. `GET /api/v1/pair` (cookie required) returns the same `{csrf_token, expires_at}`, so a
   reloaded page can recover its CSRF token. This route is an addition to the contract
   table.
5. Every `/api/` request needs the cookie (`401 UNAUTHENTICATED`, also for an unknown
   route). Every `POST` except pairing needs `X-TBound-CSRF` equal to the session's token
   (`403 FORBIDDEN`).
6. `Host` must be the bound address or `localhost:PORT` (`403 FORBIDDEN` otherwise, on
   every route including `/app/`). An `Origin` header, when present, must be exactly
   `http://` plus the request's `Host`, and appear once. No CORS header is ever sent, and
   `OPTIONS` is not answered.
7. A body is at most 64 KiB (`413`), strict JSON with no duplicate keys, unknown fields or
   trailing data (`400 INVALID_REQUEST`). A browser body carries only the payload fields
   the path does not supply, and a missing `request_key` is refused. A prompt that
   no longer fits the service's 64 KiB request frame once the envelope is added is a
   `400 PROTOCOL_ERROR`. The existing security headers and CSP are on every response.

### 9.3 Routes

Every success is `200` with JSON. `{turn}` is the short `turn-N` or the full turn ID
`psess-.../turn-N` percent-encoded (`%2F`); the full form must belong to the path's
session. Item `turn_id` values are the short form.

| Route | Body | Answer |
| --- | --- | --- |
| `POST /api/v1/pair` | `{token}` | cookie and `{csrf_token, expires_at}` |
| `GET /api/v1/pair` | none | `{csrf_token, expires_at}` |
| `GET /api/v1/contract` | none | the descriptor with `"mode": "live"`, `"read_only": false`, the CSRF header and cookie names |
| `GET /api/v1/sessions` | none | `{sessions: [session view]}` |
| `POST /api/v1/sessions` | `project_id, provider_profile, publication_mode, budget{host_calls, tokens, duration_seconds}, read_only?, request_key` | `{session, replayed?}` |
| `GET /api/v1/sessions/{id}` | none | a snapshot (9.5) |
| `GET /api/v1/sessions/{id}/events?cursor=N&wait=MS` | none | `{schema_version, session_id, cursor, next_cursor, gap, events[], items[]}`. `wait` is 0 to 25000 ms and `cursor` an unsigned integer, each given once, and no other parameter is allowed |
| `POST /api/v1/sessions/{id}/turns` | `expected_revision, request_key, prompt, expected_baseline_digest?` | `{turn, replayed?}` |
| `GET /api/v1/sessions/{id}/turns/{turn}/candidate` | none | `{candidate: {turn_id, candidate_digest, changes[]}}` |
| `POST /api/v1/sessions/{id}/turns/{turn}/cancel` | `expected_revision, request_key` | `{turn}` |
| `POST /api/v1/sessions/{id}/turns/{turn}/resolve` | `expected_revision, request_key, candidate_digest, decision` | `{turn}` |

`resolve` forwards the digest exactly as the browser sent it. The service compares it with
the recorded candidate, so a stale diff cannot authorize a changed candidate. The session,
turn and candidate views are the protocol's own JSON, unchanged. In `events`, `events[i]`
and `items[i]` describe the same observation: read the lifecycle kind from the event and
the transcript line from the item. The read-only routes `/api/v1/snapshot`, `/events` and
`/items/{id}` do not exist in live mode (`404`). A demonstration gateway exposes none of
the live routes.

### 9.4 Errors

The body is `{"error": {"code", "detail", "outcome_unknown"?}}`. The service's stable
codes pass through. A code the gateway does not know keeps its code and answers `502`.
A code that is not shaped like a code becomes `BAD_SERVICE_RESPONSE`.

| Status | Codes |
| --- | --- |
| 400 | `PROTOCOL_ERROR`, `INVALID_REQUEST`, `UNKNOWN_PROJECT`, `UNKNOWN_PROVIDER_PROFILE`, `PUBLICATION_MODE_NOT_PERMITTED`, `BUDGET_EXCEEDS_LIMIT` |
| 401 | `UNAUTHENTICATED` (gateway: no, wrong or expired cookie; bad, spent or expired pairing token) |
| 403 | `FORBIDDEN` (gateway: Host, Origin, CSRF), `UNAUTHORIZED_PRINCIPAL` (service) |
| 404 | `UNKNOWN_SESSION`, `UNKNOWN_TURN`, `NOT_FOUND` (gateway: no such route) |
| 405 | `METHOD_NOT_ALLOWED`, with `Allow` |
| 409 | `REQUEST_CONFLICT`, `STALE_REVISION`, `STALE_BASELINE`, `PROJECT_BUSY`, `PROJECT_BLOCKED`, `TURN_ACTIVE`, `PENDING_REVIEW`, `PREDECESSOR_UNRESOLVED`, `PREDECESSOR_EVIDENCE_INVALID`, `WRONG_TURN_STATE`, `CANDIDATE_MISMATCH`, `REVIEW_NOT_PENDING`, `CANCEL_NOT_ALLOWED`, `BUDGET_EXHAUSTED`, `CANCELLED` |
| 413 | `INVALID_REQUEST` (body over 64 KiB) |
| 500 | `CATALOG_CORRUPT`, `INTERNAL` |
| 502 | `BAD_SERVICE_RESPONSE` (the response is not valid protocol), or an unknown service code |
| 503 | `SERVICE_UNAVAILABLE` (the service cannot be reached, or too many calls are open) |

### 9.5 The snapshot

`GET /api/v1/sessions/{id}` reads `get_session`, then `observe` from cursor 0 until an
empty window, and renders the existing snapshot shape. It validates the result with
`ValidateSnapshot` and answers `502 BAD_SERVICE_RESPONSE` if the service data cannot be
projected. A snapshot is the first 2048 events at most. A longer session sets the
`product` stream to `gap`, adds a warning, and leaves the decision counts unstated.

- `source: "live"`, `verification_scope: "structural-audit-only"`, every item
  `authority: "observed"`. `epoch` is the session ID, `cursor` is `ID:N` with `N` the
  number of events, and item `sequence` is the event `seq` plus 1. The `events` route's
  `cursor` and `next_cursor` are the same count.
- `live` is one added optional field: the service's session view as is (`revision`,
  `blocked`, budgets, and each turn's `id`, `ordinal`, `state`, `prompt`, `outcome`,
  `terminal_reason`, `cancel_requested`, `resolution`, `candidate_digest`). The client
  echoes `live.revision` as `expected_revision`. Only a live projection may carry it,
  and a fixture, replay or receipt document that does is refused.
- `session`: `session_id`, a `title` of project, provider profile and mode, `profile`
  (the provider profile when it is a valid identifier), `turn_count` (stated), and a
  `status` derived from the latest turn. No turn is `pending`. Admitted or linked is
  `running`. `awaiting_review` is `pending`. A terminal turn is `completed` for
  `published`, `no_diff` and `discarded`, `failed` for `failed`, `cancelled` for
  `cancelled`, and `unknown` otherwise. The turn states in `live` are the precise record.
- `panels.qualification` is fixed: `g1: false`, `h1: "unexecuted"`,
  `containment: "not-established"`, `claim_bearing: false`.

### 9.6 Events and items, and what could not be mapped

One event becomes exactly one item, in order:

| Event | Item |
| --- | --- |
| `turn_admitted` | `message`, role `user`, the prompt |
| `assistant_message` | `message`, role `assistant` |
| `tool_call` | `decision`: `tool`, `tool_call_id`, `verdict`, `reason_code`, `summary` = the hint |
| `tool_result` | `result`: `tool`, `tool_call_id`, `state`, `summary` = the text, `detail` = reason code and hint |
| `turn_linked`, `turn_cancel_requested`, `turn_awaiting_review`, `turn_resolution_requested`, `turn_terminal` | `message`, role `system`, a short sentence |

A `result` state is `denied` for a DENY, `failed` when an ALLOW result's summary is
`command failed`, `succeeded` for any other ALLOW, and `unknown` otherwise. That reads
the service's fixed summary text, so it needs a status field on the event when the
service adds one.

Not mapped, and how the gateway handles each:

- **Times.** The protocol records no event time. Every `occurred_at`, and the session's
  `created_at` and `updated_at`, is the time the gateway rendered the response. The
  snapshot carries a warning that says so.
- **Lifecycle kind.** An item has no field for it, so lifecycle events are system
  messages. The client reads the kind from `events[i].kind` or from `live`.
- **`activity` items, `policy_digest`, `digest`, `lossy`** are never produced. The
  protocol has no tool-start event apart from the decision, and no digest or policy
  digest on an event.
- **`session.workflow_id`, `pi_version`, `panels.governance.policy_digest`,
  `panels.publication.transaction_id`** are absent. The decision counts are counted from
  the `tool_call` events and are absent for a truncated snapshot.
- **`panels.provenance`** is unavailable except for the provider profile.
  **`panels.publication`** is derived from the latest turn outcome (`published`,
  `not-attempted` for `no_diff`, `discarded` or `cancelled`, `pending` for a requested
  publish, otherwise `unavailable`). **`panels.recovery`** is `recovery-required` for a
  blocked session and otherwise `unavailable`.
- **`turn_id`** is the short `turn-N`, because an item's turn ID may not contain `/`.
- **`reason_code`** of a `tool_result` has no field on a result item, so it is in `detail`.
  A reason code that is not shaped like one becomes `unspecified`.
- **Candidate digest and paths** of `turn_awaiting_review` appear in the message text.
  The candidate route has the full change list.
- **Text.** Text is cut at 4096 bytes with `text_truncated` (the protocol allows 16 KiB),
  invalid UTF-8 and control characters other than newline and tab become U+FFFD, and a
  carriage return is dropped. A tool outside `read`, `write`, `edit` and `bash`, or an
  event kind the gateway does not know, becomes a system message that names it. A tool
  call id that is not a bounded identifier is rewritten the same way for a call and its
  result.
- **Markup.** The gateway builds no HTML from model or user text. Every text field is a
  JSON string and the content type is `application/json`. Rendering safety is the
  frontend's job.

### 9.7 The browser client

The React app (`frontend/src/live/`) is the client of these routes. What it does with them:

- **Mode.** `GET /api/v1/contract` with `"mode": "live"` selects the live shell. A `401` is a
  live gateway that has not paired this browser, and the app shows the pairing instructions.
  Anything else (a descriptor with no `mode`, an error, no answer) selects the read-only
  demonstration path. That path labels itself `source: fixture` or `replay` and a
  `demonstration · not a live session` badge.
- **Transport.** Same-origin, credentialed, uncached requests with relative URLs. Every
  `POST` carries `X-TBound-CSRF`, read from `GET /api/v1/pair` on the first mutation (so a
  reload recovers it) and read again after a `403`. Every answer is parsed by the strict
  validator before use. The snapshot's `live` field is checked there: closed keys,
  bounded identifiers and counts, only in a served snapshot whose source is `live`, with
  `live.id` equal to the epoch.
- **Request keys.** One key per user action, built when the action is built (a prompt, a
  cancel, a decision, a session). The key is never rebuilt for a retry. A transport
  failure, a `503` or a marked unknown outcome repeats the same body under the same key,
  twice (after 0.4 s and 1.2 s). Then the page shows "Outcome unknown", keeps the draft,
  refuses a new prompt, and offers Retry with the same key and Dismiss. A refusal (any
  `4xx`) is shown with its code and not repeated. The page treats a `5xx` other than a clean
  `503 SERVICE_UNAVAILABLE` as unknown for a mutation, because the service may have run it.
  The service answers a repeated key with `replayed` before it checks the revision, so a
  repeat must carry the original `expected_revision`, and it does.
- **Revision.** `expected_revision` is `live.revision` of the last view the page read. The
  session list carries no turns, so the page reads the view again through
  `GET /api/v1/sessions/{id}` after every window with a lifecycle event
  (`turn_admitted`, `turn_linked`, `turn_cancel_requested`, `turn_awaiting_review`,
  `turn_resolution_requested`, `turn_terminal`). A `STALE_REVISION` also triggers a read.
  The prompt is not sent again.
- **Events.** A long poll of `.../events`: the first poll has `wait=0`, later ones
  `wait=25000`. Each `items[i]` is validated as a streamed item in the projection of the
  snapshot the stream opened after. A `gap` window is not applied: the stream closes and the
  stream controller fetches a snapshot and records the gap. Each window's `next_cursor` is
  stored in `sessionStorage` per session. A reload fetches the snapshot, continues from its
  cursor, and says how many events arrived since the stored one.
- **Review.** The candidate is read when the view shows a turn in `awaiting_review` with a
  digest and no resolution or cancel. The page shows the candidate's own digest and paths,
  the diff as plain text. Publish and Discard send the digest of the candidate object that
  is displayed, not the digest in the latest view.
- **Limit.** Reading the view uses the snapshot route, which replays up to 2048 events per
  read. A route that returns the session view alone would be cheaper for long sessions.

### 9.8 Not covered

Supervisor-issued, scoped browser delegations (the pairing secret scopes the browser
instead, as the milestone's authority note says), a second paired browser, remote
access beyond the SSH tunnel, and an automated test in a real browser. The client was
exercised by hand in a browser against a scripted fake product service, and its logic is
tested with a fake `fetch`.
