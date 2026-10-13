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
(`supervisor/internal/webchat/contract.go`). A live projection should produce the
same shape, with `source: "live"` and `verification_scope:
"structural-audit-only"` (values already defined).

### 2.1 Snapshot

`GET /api/v1/snapshot` (gateway) ← a service-owned snapshot containing:

- `source`, `verification_scope`, `epoch`, `cursor`, `generated_at`
- `session`: `session_id`, `workflow_id`, `title`, `status`, `profile`,
  `pi_version`, `created_at`, `updated_at`, `turn_count`
- `panels`:
  - `governance`: `policy_digest`, `allow_count`, `deny_count`, `unknown_count`
  - `provenance`: `profile_id`, `profile_hash`, `source_digest`,
    `containment_profile`, `containment_digest`, `provider_profile`,
    `runtime_closure`, `tcb_proof_digest`, `tcb_proof_sequence`
  - `publication`: `state`, `transaction_id`, `observed_at`, `detail`
  - `recovery`: `state`, `generation`, `last_completed_sequence`, `detail`
  - `qualification`: `g1`, `h1`, `containment`, `claim_bearing`
- `streams[]`: `id`, `label`, `health`, `available`, `reason`
- `items[]`: the ordered bounded union in §2.2
- `gaps[]`: durable continuity gaps

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
