# TBound product contracts (P1, milestone 1)

Status: contract for the first product milestone, the headless governed multi-turn conversation, and its P4 extension at the end. It fixes the decisions that [the product plan](product-plan.md) sections 3 to 5 leave open, for P2 and P3 only. Review, cancellation, the wire protocol, and client UX are later packages and are not specified here.

Research and corpus modes are unchanged. Nothing below is reachable from `serve --pi`, and the research service refuses any admission mode other than `fresh`, `resume`, and `corpus`.

## Owners

| Concern | Owner |
| --- | --- |
| Product registration, sessions, turns, request keys, budgets, project ownership | `supervisor/internal/productsession` |
| Conversation prefix import and replay | `supervisor/internal/providerbridge` |
| Turn pins in workflow checkpoints | `supervisor/internal/workflowrecovery` |
| Composition of one product turn (Store, journals, ledger, conversation, worker, settlement, publication) | `supervisor/cmd/tbound` |

`productsession` never imports `cmd/tbound`, never runs effects, and never builds provider history. `providerbridge` never reads product records.

## Identities

- `ProductSessionID`: `psess-` plus 32 lowercase hex characters from `crypto/rand`. One user conversation.
- `UserTurnID`: `<ProductSessionID>/turn-<ordinal>`, ordinal from 1, strictly increasing, no gaps.
- Each turn gets fresh `WorkflowID`, `RunID`, runtime `SessionID`, and `ConversationID`. They keep their existing meanings. A completed workflow is never reused. The catalog records the mapping from turn to workflow.
- `Principal` is the authenticated peer UID. The transport derives it. Request payloads never carry it. A session belongs to the principal that created it. Every operation by another principal fails with `UNAUTHORIZED_PRINCIPAL`, reads included.

## Product registration

`tbound-product-registration/v1`, a strict JSON document with an Ed25519 signature over `"tbound-product-registration/v1\n"` followed by the JSON encoding of the registration with `digest` empty. `digest` is `sha256:` plus the hex SHA-256 of those signed bytes. Unknown fields, a wrong digest, a bad signature, or invalid values refuse the registration.

| Field | Meaning |
| --- | --- |
| `registration_id` | Bounded identity of this signed registration. |
| `service_identity` | Bounded label of the product service it authorizes. |
| `allowed_principals` | Client UIDs that may use the service. |
| `projects[]` | `project_id`, `root_identity_digest` (exact managed-root identity), `policy_digest` (installed gate profile), `bash_output_paths` (exact research-style output scope; empty disables Bash), `publication_modes` (subset of `review`, `auto`). |
| `provider_profiles` | Provider profile IDs a session may select. |
| `limits.turn` | Per-turn ceiling: `host_calls`, `tokens`, `duration_seconds`. |
| `limits.session` | Upper bound a session may request, same fields. |
| `limits.max_turns_per_session` | Hard cap on turns. |
| `limits.review_timeout_seconds` | Default review expiry. P4 enforces it; P2 records it. |
| `runtime_digest`, `image_digest`, `extension_digest` | Pins of the runtime the registration authorizes. |

A turn selects a registered `project_id`, never a path. Registering a project is an administrative signing operation. Changing a project, policy, or limit means a new signed registration.

## Paths

Product defaults never share a path with the thesis service:

| Item | Product default | Thesis path it must not equal |
| --- | --- | --- |
| Registration | `/etc/tbound-product/registration.json` (+ `.sig`) | `/etc/tbound/...` |
| Registration public key | `/etc/tbound-product/registration.pub` | thesis admin key |
| Catalog journal | `/var/lib/tbound-product/catalog.jsonl` | `/var/lib/tbound/...` |
| Turn state root | `/var/lib/tbound-product/turns/` | `/var/lib/tbound/sessions/...` |
| Service socket (later) | `/run/tbound-product/product.sock` | `/run/tbound/serve-pi.sock` |

A unit test enforces that no product default is equal to or inside a thesis path.

## Catalog

The catalog is one protected `audit.Journal` (hash chain, fsync per record, exclusive writer lock). Opening it replays and verifies every record before any operation. Records are strict JSON in `Event.Data`.

| Kind | Written when | Makes durable |
| --- | --- | --- |
| `product_catalog_opened` | Every open | Owner epoch (previous + 1) and registration digest. A registration digest change on reopen is refused while any turn is unresolved. |
| `product_session_created` | `CreateSession` accepted | Session, principal, project, provider profile, publication mode, session budget, project baseline at creation, request key and payload digest. |
| `product_turn_admitted` | `SubmitTurn` accepted | **The durable acceptance point.** Turn ID and ordinal, principal, request key, payload digest, exact prompt and its digest, project, policy digest, Bash scope, publication mode, admitted baseline digest, predecessor turn ID and receipt digest, budget reservation, session revision after admission. |
| `product_turn_linked` | Before any provider or tool work of the turn | Workflow, run, runtime session and conversation IDs, provider/Store/command journal paths, imported prefix digest. |
| `product_turn_terminal` | After the child workflow has a verified disposition | Outcome, settlement, receipt bytes and digest, actual usage, result baseline digest, `context_eligible`, refund. |
| `product_turn_awaiting_review` | A changed turn in review mode settled | Receipt bytes and digest, actual usage, refund, candidate tree digest. The turn stays non-terminal and blocks its session and project until P4 resolves it. |
| `product_turn_abandoned` | Reconciling an admitted turn with no link | Admission withdrawn with a full refund. No provider or tool work can precede the link record. |
| `product_turn_refused` | The composition refused to launch an admitted, unlinked turn | Outcome `failed` with a full refund. `PREDECESSOR_EVIDENCE_INVALID` blocks the session; `STALE_BASELINE` blocks the project. |
| `product_turn_unknown` | Reconciling a linked turn that has no verifiable disposition | Reservation stays consumed; project is blocked. |

### Operations

```go
func Open(path string, registration Registration) (*Catalog, error)
func (c *Catalog) CreateSession(p Principal, r CreateSessionRequest, measuredBaseline string) (Session, bool, error)
func (c *Catalog) SubmitTurn(p Principal, r SubmitTurnRequest, measuredBaseline string) (Admission, error)
func (c *Catalog) LinkTurn(turnID string, link TurnLink) error
func (c *Catalog) CompleteTurn(turnID string, t TurnTerminal) error
func (c *Catalog) MarkAwaitingReview(turnID string, r TurnReview) error
func (c *Catalog) AbandonUnlinkedTurn(turnID string) error
func (c *Catalog) RefuseUnlinkedTurn(turnID string, code Code) error
func (c *Catalog) MarkTurnUnknown(turnID string, reason string) error
func (c *Catalog) Session(p Principal, id string) (Session, error)
func (c *Catalog) Turn(id string) (Turn, bool)
```

`measuredBaseline` is the tree digest of the managed project, measured by the trusted composition at call time. Clients never supply it; they may supply `expected_baseline_digest`.

`SubmitTurnRequest` is `session_id`, `expected_revision`, `request_key`, `prompt`, optional `expected_baseline_digest`. Its payload digest covers all five fields.

`SubmitTurn` checks, in this order, and returns the first failure:

1. Principal is registered and owns the session.
2. Request key: an existing `(principal, operation, session, request_key)` with the same payload digest returns the existing admission with `Replayed=true` and writes nothing. A different payload digest returns `REQUEST_CONFLICT`.
3. Session is open and not blocked.
4. No unresolved turn in the session: active turn returns `TURN_ACTIVE`, awaiting review returns `PENDING_REVIEW`, unknown returns `PREDECESSOR_UNRESOLVED`.
5. `expected_revision` equals the session revision, else `STALE_REVISION`.
6. No unresolved turn of another session on the same project, else `PROJECT_BUSY`. A project with an unknown turn returns `PROJECT_BLOCKED`.
7. Baseline: `measuredBaseline` equals the project baseline the catalog committed after the predecessor (or at session creation), and `expected_baseline_digest`, when given, equals it too. Otherwise `STALE_BASELINE`.
8. Turn count is below `max_turns_per_session`, and the remaining session budget can reserve at least one host call and one token. Otherwise `BUDGET_EXHAUSTED`.
9. Prompt is bounded plain text (`MaxPromptBytes` = 16 KiB, same as `providerbridge`), else `INVALID_REQUEST`.

Then one `product_turn_admitted` record is appended and synced. The reservation is `min(limits.turn, remaining session budget)` per field.

`CompleteTurn` requires a linked, non-terminal turn. Usage may not exceed the reservation in any field. The session receives back `reservation - usage`. A `published` outcome moves the project baseline to `result_baseline_digest`. `no_diff`, `discarded`, `failed`, and `cancelled` require `result_baseline_digest` to equal the admitted baseline. `unknown` is only written by `MarkTurnUnknown`. `awaiting_review` is a non-terminal state that blocks the session and project until P4 resolves it.

### Decisions taken during implementation

- Registration-dependent checks run only for records written under the registration loaded now. Older records are checked for state consistency only, so a new registration can be loaded when no turn is unresolved.
- A blocked session returns `PREDECESSOR_UNRESOLVED`. A blocked project returns `PROJECT_BLOCKED`, also from `CreateSession`.
- `BUDGET_EXHAUSTED` requires at least one unit of every budget field, duration included.
- Abandoned turns keep their ordinal but do not count toward `max_turns_per_session` and are never a predecessor.
- `MarkAwaitingReview` settles usage and refund at once. A P4 resolution must not settle the turn again.
- `published` requires an auto-mode turn. A replayed request key returns the turn's current state.

### Error codes

`UNAUTHORIZED_PRINCIPAL`, `UNKNOWN_PROJECT`, `UNKNOWN_PROVIDER_PROFILE`, `PUBLICATION_MODE_NOT_PERMITTED`, `INVALID_REQUEST`, `BUDGET_EXCEEDS_LIMIT`, `BUDGET_EXHAUSTED`, `REQUEST_CONFLICT`, `UNKNOWN_SESSION`, `UNKNOWN_TURN`, `STALE_REVISION`, `STALE_BASELINE`, `PROJECT_BUSY`, `PROJECT_BLOCKED`, `TURN_ACTIVE`, `PENDING_REVIEW`, `PREDECESSOR_UNRESOLVED`, `PREDECESSOR_EVIDENCE_INVALID`, `WRONG_TURN_STATE`, `CATALOG_CORRUPT`.

Each is a `*productsession.Error` with a stable `Code`. `errors.Is(err, productsession.ErrCode(code))` matches it.

### Restart and partial records

| Retained state after a crash | What a new process does |
| --- | --- |
| Request never journaled | Nothing happened. The client resubmits with the same key. |
| `admitted`, no `linked` | No provider or tool work happened, because the link is written first. `AbandonUnlinkedTurn` refunds; the client resubmits with a new key. |
| `linked`, no `terminal` | Unresolved. The session and project stay blocked. Milestone 1 can only record `MarkTurnUnknown`. Evidence-based reconciliation through `workflowrecovery` is P4. |
| `terminal` | Final. Its receipt commits the child journals that the next turn verifies. |

## Conversation prefix

Go owns the history. Each turn's provider journal is self-contained.

- `providerbridge.ReplayPredecessorContext(journal, pins, verifier, boundary)` replays a completed predecessor provider journal with the existing strict replay. It refuses unless the transcript ends at `awaiting_host_prompt`, which means the final provider response carried no tool call. It returns an opaque `PredecessorContext`. Only `providerbridge` can construct one.
- The context holds every message after the system and developer prompts: user prompts, assistant messages, tool calls, released results with their hints, and the predecessor's own imported prefix. Then it appends one host boundary message, built only from enum values and digests: the predecessor outcome, whether its changes were applied, and the current project baseline. A discarded or failed predecessor therefore cannot look like current workspace state.
- A predecessor that is not context-eligible (failed or cancelled before completing its transcript) contributes no messages. `ExtendPredecessorContext(previous, boundary)` adds its boundary message to the last eligible context.
- `ImportedPrefixDigest` is `sha256:` over the canonical JSON of the prefix schema, the predecessor receipt digest, and the messages.
- `providerbridge.Config.Predecessor` seeds the history at construction. `AdmitPrompt` journals one `provider_history_imported` record, holding the messages and digest, immediately before `provider_user_admitted`. It happens once.
- Imported response IDs and tool-call IDs are reserved. A new provider response that reuses one is refused. Imported tool calls are never registered with the correlation stream, so no Pi proposal can match them and gain authority.
- `RecoveryPins.ImportedPrefixDigest` must equal the record's digest. A journal with the record and an empty pin, or the reverse, fails replay.
- Context bounds are the existing `MaxTranscriptBytes` and `MaxTranscriptTurns`, plus the request-size check. Exceeding them fails with `ErrContextExhausted` before any provider call. Nothing is truncated or summarized.

## Workflow pins

`workflowrecovery.Pins` gains `imported_prefix_digest` and `predecessor_turn_receipt_digest`, both bare hex, both `omitempty`. A legacy research checkpoint has both empty and its digest is unchanged. Both are immutable across checkpoints and recovery generations, like `task_digest`. `task_digest` and `prompt_digest` stay specific to the turn's own admitted prompt.

## Product turn receipt

`tbound-product-turn-receipt/v1` (`productsession.TurnReceipt`), strict JSON, at most 64 KiB, written by `cmd/tbound` and stored in `product_turn_terminal` or `product_turn_awaiting_review`. The catalog decodes it strictly and refuses to settle unless its identity, workflow link, imported prefix, predecessor, baselines, outcome, settlement and usage match the turn and the record. Settlement is `headless-fixture-closed` for worker kind `headless-fixture` and `stopped` for worker kind `pi`; no other pairing is accepted. A published receipt names its publication transaction:

`session_id`, `turn_id`, `ordinal`, `workflow_id`, `run_id`, `conversation_id`, `worker_kind`, `worker_instance`, `provider_journal_head`, `provider_journal_count`, `store_journal_head`, `store_journal_count`, `imported_prefix_digest`, `predecessor_receipt_digest`, `baseline_digest`, `result_baseline_digest`, `outcome`, `settlement`, `publication_transaction_id`, `usage`, `tool_sequence`, `denials`.

Before it imports a predecessor, the next turn reopens the predecessor's retained journals read-only and checks their verified heads and counts against this receipt. A mismatch refuses the launch with `RefuseUnlinkedTurn(turn, PREDECESSOR_EVIDENCE_INVALID)`: the new turn is `failed` with a full refund, and the session is blocked.

`worker_kind` is `headless-fixture` in milestone 1, because the worker is the in-process deterministic driver. Receipts never claim an OS process settlement that did not happen. A real Pi worker per turn needs the product guest and is pending.

## Outcomes

| Outcome | Meaning | Project baseline |
| --- | --- | --- |
| `published` | Changed turn, auto-publish mode, publication `SUCCEEDED`. | Moves to the published tree. |
| `no_diff` | Read-only or net-zero turn, verified by the evidence bundle. | Unchanged. |
| `awaiting_review` (non-terminal) | Changed turn in review mode. Blocks the session and project. | Unchanged until P4. |
| `discarded` | P4 only. | Unchanged. |
| `failed` | Known failure with no applied effect, including a refused launch. | Unchanged. |
| `cancelled` | P4 only. | Unchanged. |
| `unknown` | Disposition cannot be established. Reservation consumed, project blocked. | Unchanged, blocked. |

Assistant completion is not settlement. A turn is terminal only after the worker loop ended, the conversation closed, the evidence bundle was read, and the publication step returned a definite status.

## Milestone 2: review, cancellation and restart (P4, headless)

This section extends the contract for P4. It still uses headless clients and the in-process worker. Interrupting a real Pi process or a running Bash command needs the product guest and stays pending.

### Review

- A review-mode turn composes the publication finalizer when it starts, the same as an auto-mode turn. After `MarkAwaitingReview` the driver keeps the closed turn's workflow and finalizer open in a pending-review registry. No worker runs. The project stays owned by the turn.
- `MarkAwaitingReview` records `review_timeout_seconds` from the registration in force. The driver enforces expiry against the process's monotonic clock. A restart never extends a review: a pending review never survives a restart (see Restart).
- `ResolveTurn(p, ResolveTurnRequest{session_id, turn_id, expected_revision, request_key, candidate_digest, decision})`, where `decision` is `publish` or `discard`:
  - It requires the owning principal, the turn in `awaiting_review`, the session revision, and `candidate_digest` equal to the recorded candidate. A stale diff cannot authorize a changed candidate.
  - It is idempotent per request key, like `SubmitTurn`, and the request digest covers all six fields.
  - It appends `product_turn_resolution_requested` before any publication work. That record is the durable intent the restart rules read.
- Publish runs the retained finalizer. Success requires the project to measure exactly the sealed candidate tree afterwards.
- Terminal records for a reviewed turn reuse `CompleteTurn` with a new receipt. Usage was settled at `awaiting_review`, so the terminal record must repeat that usage and refund nothing.
- Reviewed-turn outcomes:
  - `published`;
  - `discarded`, carrying a `reason` of `user`, `expired`, `cancelled` or `restart`;
  - `failed`, when publication failed and the project still equals the admitted baseline;
  - `unknown`.
- Discard keeps the turn's retained Store, journals and candidate. Spend stays consumed.

### Cancellation

- `CancelTurn(p, CancelTurnRequest{session_id, turn_id, expected_revision, request_key})` appends `product_turn_cancel_requested` for a turn that is admitted, linked or awaiting review. It is idempotent per request key and bumps the revision. It never claims settlement; the turn reports `cancel_requested`.
- Admitted, not yet linked: the driver ends the turn before launch with `RefuseUnlinkedTurn(turn, CANCELLED)`. The outcome is `cancelled` with a full refund, and nothing is blocked.
- Linked and running: the headless worker checks for cancellation before every provider exchange. It never interrupts a call in flight. At the next step boundary it stops, the conversation closes, and the turn settles `cancelled` with usage charged. Nothing is published, and the project must still equal the admitted baseline.
- Awaiting review: the turn is discarded with reason `cancelled`.
- After `product_turn_resolution_requested` with `publish`, or once an auto-mode finalizer has started, cancellation cannot promise rollback. The request is recorded, and the turn reports its real disposition.

### Restart

On `Open`, unresolved turns are rebuilt from the catalog. The driver's `ReconcileAfterRestart` runs before any new admission is served. It never publishes on reconstruction alone.

| Retained state | Action |
| --- | --- |
| `admitted`, no link | `AbandonUnlinkedTurn`, as in milestone 1. |
| `linked`, no terminal | Reopen the retained Store and journals read-only, plus the owner checkpoint. If the Store reconstructs with no quarantine or unknown effect, no reservation is open, the project equals the admitted baseline, and the project publication journal has no transaction for this workflow: `CompleteTurn(failed)` with settlement `reconciled-after-restart` and usage from the retained ledger counters. Otherwise `MarkTurnUnknown`. |
| `awaiting_review`, no resolution request | If the project equals the admitted baseline and no publication transaction exists for the workflow: `discarded` with reason `restart`. Otherwise `unknown`. |
| `awaiting_review` with resolution request `publish` | If the publication journal shows the workflow's transaction `SUCCEEDED` and the project equals the sealed candidate: `published`. If no transaction exists and the project equals the admitted baseline: `discarded` with reason `restart`. Otherwise `unknown`. |
| `awaiting_review` with resolution request `discard` | `discarded` with reason `user`, after the same baseline check. |

Settlement `reconciled-after-restart` is valid for worker kind `headless-fixture`, because the process that hosted the in-process worker is gone. A real Pi worker will need its drain proof instead.

### Catalog additions

| Kind | Meaning |
| --- | --- |
| `product_turn_cancel_requested` | Durable cancellation request. |
| `product_turn_resolution_requested` | Durable review decision for the exact candidate, written before publication. |
| `product_turn_terminal` (extended) | Also ends an `awaiting_review` turn with `published`, `discarded` (plus `reason`) or `failed`, refunding nothing. Also accepts `cancelled` for a linked turn with a cancel request. |
| `product_turn_refused` (extended) | Also accepts code `CANCELLED`, giving outcome `cancelled` with no block. |

New error codes: `CANDIDATE_MISMATCH`, `REVIEW_NOT_PENDING`, `CANCEL_NOT_ALLOWED`.

### P4 catalog decisions taken during implementation

- `CancelTurn` and `ResolveTurn` check the principal, the request key, that the turn belongs to the session, the turn state, and then the revision.
- A turn records at most one cancel request. A repeat with a new key returns the turn without writing, and does not consume that key.
- A turn records at most one resolution. A second one, with either decision, is a `REQUEST_CONFLICT`.
- `discarded` with reason `restart` or `cancelled` is accepted even after a `publish` resolution, because the driver reports the real disposition.
- `MarkTurnUnknown` also accepts an `awaiting_review` turn, without settling its usage again.
- A reviewed terminal receipt replaces the awaiting-review receipt, so the next turn's predecessor commitment is the terminal one.
- A reviewed terminal receipt must keep every execution fact of the awaiting-review receipt. Only the outcome, result baseline, publication transaction and settlement may change.
- A turn refused with `CANCELLED` before its link is never a predecessor, the same as an abandoned turn. It did no work and has no evidence to import.
- The awaiting-review record requires `review_timeout_seconds`. Earlier test-only journals without it are not migrated, because no product catalog has been deployed.
