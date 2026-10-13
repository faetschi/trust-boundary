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
| Bounded read-only projection (snapshot + cursor SSE) | Implemented (fixture / replay / receipt / unavailable sources) |
| Typed post-run receipt import of a real development or production receipt | Implemented (`--receipt`, `--receipt-demo`) |
| Typed post-run Pi receipt import (dev, native-fixture, production shapes) | Implemented (`--receipt`, `--receipt-demo`) |
| Chat transcript rendering (user/assistant messages) | Implemented |
| Tool activity with policy verdict, reason code, and result state | Implemented |
| Timeline finding filters and an observed posture summary | Implemented |
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
outcome: a verified `succeeded` result whose *output* was truncated is still a
succeeded terminal outcome, and the UI renders a `lossy` chip beside it so the
reader knows the output is incomplete. A `LOSSY` result is also counted and
matched under the `Unknown` filter, because lossy output cannot substitute for
terminal evidence.

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
  Each button carries its own count and an `aria-pressed` state. A filter keeps
  the correlated tool block (activity + decision + result) together, so a matched
  finding is never shown without its execution context, and transcript messages
  are hidden while a finding filter is active. A filter with no matches renders an
  explicit empty state rather than a blank timeline.
- Filtering is presentation-only: it never changes the projection, an
  observation, a verdict or an effect, and it never relabels an `UNKNOWN`.

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
  encoding) is rejected `400` rather than read. The rejection is
  **timeout-bounded**, not drain-free: `net/http` may still consume a bounded
  amount of an unread body while closing, and the server's `ReadTimeout` is what
  prevents an unbounded stall. `Content-Security-Policy` is strict and does
  **not** allow inline script or style (`script-src 'self'; style-src 'self'`),
  with `default-src 'none'`, `frame-ancestors 'none'`, `base-uri 'none'`.
- Static assets are an exact allowlist; there is no directory serving.
- Each SSE/data write is bounded by a deadline that also covers the socket flush.
- Identifiers are a strict character set (no path separators), display text
  rejects control characters, and receipt provenance/status values are echoed
  only when they are bounded identifiers. **Residual risk, stated plainly:** the
  projection *bounds* operator-supplied prose (transcript text, summaries,
  details, notes) but does **not** content-redact it, so a fixture or receipt the
  operator chooses to load can echo sensitive free text. A production projection
  must emit structured safe summaries upstream; the gateway cannot infer which
  prose is sensitive.

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
| A reconnect snapshot could overwrite newer live events | The client refreshes in place on a re-open (the stream is already re-established, so there is no resubscribe and no loop) and buffers live events during the fetch; gap announcements are keyed by gap identity |
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

### 8.1 Presentation design system

The visual language follows the **coss/ui design-token vocabulary** (the
Cal.com/shadcn-compatible CSS variables, including the extra
`--destructive-foreground` and `--info`/`--success`/`--warning` tokens) plus coss
component conventions: `Card`-style bounded surfaces, outline and semantic
`Badge`/chip variants, `Alert`-style notices with an icon, `Empty`/`Skeleton`
loading states, `Kbd`, and a sidebar with muted hover/active rows. The timeline
filter is a segmented Toggle Group built from `aria-pressed` buttons with roving
arrow-key focus, and the posture row uses the badge/chip shapes.

The React/Base UI/Tailwind implementation is **not** used, deliberately: the
thesis treats a browser client as a deferred, non-claim-bearing demonstrator and
requires the web service to stay minimal, so this workspace remains a zero-build
ESM + CSS client served by the Go gateway. The tokens are defined in `:root` so a
later coss/ui adoption can reuse them. Fonts degrade to local Inter / Geist Mono
when installed and otherwise fall back to system stacks; no webfont is fetched,
which keeps the strict `font-src 'self'` policy and offline operation intact.

## 9. Running it

Requires the pinned Go toolchain (module `tbound/supervisor`).

```sh
cd supervisor
go vet ./internal/webchat/... ./cmd/tbound-webchat/...
go test ./internal/webchat/... ./cmd/tbound-webchat/...
node --test internal/webchat/jstest/format.test.mjs \
            internal/webchat/jstest/store.test.mjs \
            internal/webchat/jstest/filters.test.mjs \
            internal/webchat/jstest/render.test.mjs
go run ./cmd/tbound-webchat --demo                 # embedded fixture, port 8791
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

Screenshots (captured with headless Chrome against the running gateway) are in
[`webchat/qa/`](webchat/qa/): `fixture-desktop`, `fixture-expanded-tools`,
`fixture-denied-filter`, `replay-continuity-gap`, `receipt-real-desktop`,
`receipt-real-expanded-tools`, `unavailable-desktop`, `mobile-conversation`,
`mobile-inspector`. Keyboard audits driven over the DevTools protocol are in
`webchat/qa/keyboard-audit-desktop.json` and `keyboard-audit-mobile.json`. They
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

Focused checks on the pinned Go 1.27.1 in Linux:

- `gofmt -l .` — no output.
- `go build ./...` — exit 0.
- `go vet ./internal/webchat/... ./cmd/tbound-webchat/...` — exit 0.
- `go test -count=1 ./internal/webchat/... ./cmd/tbound-webchat/...` — ok.
- `go test -race -count=1 ./internal/webchat/... ./cmd/tbound-webchat/...` — ok.
- `node --test` (browser modules) — 40 tests pass.
- The **real** development CLI fixture receipt was imported with
  `--receipt` and rendered in the running workspace: source `replay`, session
  `unknown`, four tool cards (two reported `succeeded`, two `unknown`),
  `per-tool-outcome` stream `gap`, publication `not-attempted`, recovery
  `unknown`, and no browser console errors.
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
