# Web chat projection service — contract proposal

Proposal to the core thread that owns the supervisor. **No authority-owned file
was modified for the frontend increment.** This document specifies the small,
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

## 4. Requests the gateway will route (Milestone 3, not implemented)

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
