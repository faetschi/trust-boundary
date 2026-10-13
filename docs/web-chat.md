# Pi web chat workspace (frontend product)

Status: **development increment, non-claim-bearing.** `G1=false`; containment is
`not-established`; `H1` is `unexecuted`. No qualified public production launch has
occurred. A green workspace, green tests, or a loaded fixture establishes none of
these.

This document describes the first frontend product increment for tbound: a new,
untrusted web chat workspace for the official Pi CLI/RPC harness, wrapped by
tbound governance. It is a presentation layer over the supervisor, not a second
authority. The relationship to OpenChamber-for-OpenCode is the *product concept*
(a better presentation layer for a harness), not a visual or backend template.

Related: [web chat service contract proposal](web-chat-contract.md),
[architecture](architecture.md), [status](status.md).

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
exist. The composer and cancel control are present but **disabled and labelled**
because submission requires the authenticated supervisor session service.

| Capability | State |
|---|---|
| Bounded read-only projection (snapshot + cursor SSE) | Implemented (fixture / replay / unavailable sources) |
| Chat transcript rendering (user/assistant messages) | Implemented |
| Tool activity with policy verdict, reason code, and result state | Implemented |
| Governance / provenance / publication / recovery / qualification panels | Implemented |
| Explicit source labels (`fixture`/`live`/`replay`/`unavailable`) and unverified markers | Implemented |
| Continuity-gap recovery from a snapshot (no invented success) | Implemented |
| Live projection from the supervisor | **Not implemented** (`--live` fails closed) |
| Persistent multi-turn sessions and history | **Not implemented** (supervisor-owned) |
| Turn submission / cancellation | **Not implemented** (composer disabled) |
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

- `supervisor/internal/webchat/` — the bounded projection contract, validation,
  the gateway store, the HTTP/SSE handler, and the embedded workspace assets.
- `supervisor/cmd/tbound-webchat/` — the loopback-only gateway entrypoint.

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

Two independent guards enforce this:

1. **Go boundary.** Any observation loaded from a document (fixture or replay) is
   forced to `authority=diagnostic` and `verification_scope=unverified-input`;
   provenance `available` and qualification (`g1`/`claim_bearing`) are forced
   false; a document that claims containment qualification or an executed research
   protocol is refused. `authority=authoritative` is refused at the presentation
   boundary, and `live` can never be loaded from a file.
2. **Client gate.** A success state (`succeeded`/`completed`/`published`/`ready`)
   is rendered with the confirmed-success chip only when the item is *not*
   diagnostic and *not* unverified input (`isVerified`), and panel success only
   when the projection is `live` with non-unverified scope (`isVerifiedProjection`).
   Otherwise success renders as an amber, dashed **`reported`** chip next to an
   explicit `unverified` marker.

This is the fix for the previously reviewed "unverified replay success
presentation" defect; see §7.

## 5. API endpoints

All routes are `GET`-only and reject any request body.

| Route | Purpose |
|---|---|
| `GET /` | The workspace shell (embedded assets). |
| `GET /static/*.css`, `/static/*.mjs` | Closed allowlist of embedded modules. |
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
  encoding) is rejected `400` rather than read, removing the slow-body surface.
  `Content-Security-Policy` is strict and does **not** allow inline script or
  style (`script-src 'self'; style-src 'self'`), with `default-src 'none'`,
  `frame-ancestors 'none'`, `base-uri 'none'`.
- Static assets are an exact allowlist; there is no directory serving.
- Each SSE/data write is bounded by a deadline that also covers the socket flush.

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
| (c) Unbounded/slow GET request bodies | Bodies are rejected outright; no request body is ever read | `TestHandlerRejectsMethodsOriginsAndBodies` |

Three further properties are locked by tests: per-type field smuggling is refused
(`TestPerTypeFieldSmugglingRefused`), a gap or duplicate cannot invent success
(`jstest/store.test.mjs` "a gap is reported…", "a missing terminal result cannot
become a success display"), and the gateway exposes no control route
(`TestHandlerRejectsMethodsOriginsAndBodies`).

## 8. Reuse of the governance companion

The typed viewer in the sibling `codex/tbound-governance-companion` worktree
informed this contract and validation/store design (bounded records, closed
enums, identifier/text bounds, cursor model, untrusted-document downgrade, and
the three defects above). The visual design is entirely new: a three-pane chat
workspace with a transcript/tool timeline, a governance inspector, a persistent
claim-boundary strip, and explicit source/verified/gap labelling. No file from
that checkout was copied verbatim except the platform-specific
safe-no-follow document opener, which is retained unchanged.

## 9. Running it

Requires the pinned Go toolchain (module `tbound/supervisor`).

```sh
cd supervisor
go vet ./internal/webchat/... ./cmd/tbound-webchat/...
go test ./internal/webchat/... ./cmd/tbound-webchat/...
node --test internal/webchat/jstest/format.test.mjs \
            internal/webchat/jstest/store.test.mjs \
            internal/webchat/jstest/render.test.mjs
go run ./cmd/tbound-webchat --demo                 # embedded fixture, port 8791
go run ./cmd/tbound-webchat                         # unavailable projection
go run ./cmd/tbound-webchat --replay path.json      # retained observations
go run ./cmd/tbound-webchat --live                  # refused: supervisor service absent
```

`--demo`, `--fixture <path>`, `--replay <path>` and `--live` are mutually
exclusive. The startup banner prints the source class and the claim boundary; it
never prints fixture paths, controls, or secrets.

### 9.1 Demo modes and labels

- `--demo` — the embedded `fixtures/demo-workspace.json`: a full turn with
  `ALLOW`/`DENY` decisions and `succeeded`/`denied`/`unknown` results, all marked
  `FIXTURE` and `unverified`.
- `--replay fixtures/replay-gap.json` — a retained observation with a `CONTINUITY
  GAP` notice and an `UNKNOWN` result.
- no flag — the `UNAVAILABLE` projection: no fabricated rows, no stream.

### 9.2 Visual QA and keyboard receipts

Screenshots (captured with headless Chrome against the running gateway) are in
[`webchat/qa/`](webchat/qa/): `fixture-desktop`, `fixture-expanded-tools`,
`replay-continuity-gap`, `unavailable-desktop`, `mobile-conversation`,
`mobile-inspector`. Keyboard audits driven over the DevTools protocol are in
`webchat/qa/keyboard-audit-desktop.json` and `keyboard-audit-mobile.json`; they
verify tab order (skip link → governance toggle → session → each tool disclosure),
`Enter` toggling a tool disclosure, `Enter` opening the governance drawer, and
`Escape` closing it. At narrow widths the closed drawer is `inert` +
`aria-hidden` so keyboard focus cannot reach off-canvas content, and becomes
focusable only when opened. Rendering preserves keyboard focus on the tool
disclosure a user was reading, and a render error cannot block data loading.

## 10. Verification receipts

Focused checks on the pinned Go 1.27.1 in Linux:

- `gofmt -l .` — no output.
- `go build ./...` — exit 0.
- `go vet ./internal/webchat/... ./cmd/tbound-webchat/...` — exit 0.
- `go test -count=1 ./internal/webchat/... ./cmd/tbound-webchat/...` — ok.
- `go test -race -count=1 ./internal/webchat/... ./cmd/tbound-webchat/...` — ok.
- `node --test` (browser modules) — 20 tests pass.
- `go test -race -count=1 ./...` — all packages pass except
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
