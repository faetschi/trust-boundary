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

`tbound-product-registration/v2`, a strict JSON document with an Ed25519 signature over `"tbound-product-registration/v2\n"` followed by the JSON encoding of the registration with `digest` empty. `digest` is `sha256:` plus the hex SHA-256 of those signed bytes. Unknown fields, a wrong digest, a bad signature, or invalid values refuse the registration.

| Field | Meaning |
| --- | --- |
| `registration_id` | Bounded identity of this signed registration. |
| `service_identity` | Bounded label of the product service it authorizes. |
| `allowed_principals` | Client UIDs that may use the service. |
| `projects[]` | `project_id`, `root_identity_digest` (exact managed-root identity), `policy_digest` (installed gate profile), `bash_output_paths` (exact research-style output scope when no product scope is registered), `publication_modes` (subset of `review`, `auto`). |
| `provider_profiles` | Provider profile IDs a session may select. |
| `limits.turn` | Per-turn ceiling: `host_calls`, `tokens`, `duration_seconds`. |
| `limits.session` | Upper bound a session may request, same fields. |
| `limits.max_turns_per_session` | Hard cap on turns. |
| `limits.review_timeout_seconds` | Default review expiry. P4 enforces it; P2 records it. |
| `git_digest` | Digest of the approved private Git executable. The service config must match it. |
| `host_identity_digest` | Compiled prompts, named provider table entries in ID order, and supervisor-loop worker kind, under `tbound-product-host-identity/v1`. `registration init` computes it. |
| `runtime_digest`, `image_digest`, `extension_digest` | Administrator declarations. Bash-enabled installs bind `image_digest` to the signed and imported command image. The supervisor-loop worker still lists all three in `unverified_inputs`. |

A turn selects a registered `project_id`, never a path. Registering a project is an administrative signing operation. Changing a project, policy, or limit means a new signed registration.

## Paths

Product defaults never share a path with the thesis service:

| Item | Product default | Thesis path it must not equal |
| --- | --- | --- |
| Registration | `/etc/tbound-product/registration.json` (+ `.sig`) | `/etc/tbound/...` |
| Registration public key | `/etc/tbound-product/registration.pub` | thesis admin key |
| Catalog journal | `/var/lib/tbound-product/catalog.jsonl` | `/var/lib/tbound/...` |
| Turn state root | `/var/lib/tbound-product/turns/` | `/var/lib/tbound/sessions/...` |
| Podman configuration | `/etc/tbound-product/podman/` (`containers.conf`, `storage.conf`) | `/etc/tbound/podman-config/` |
| Podman store | `/var/lib/tbound-product/podman-home/`, `/var/lib/tbound-product/podman-storage/graphroot` | `/var/lib/tbound/podman-home/` |
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
| `product_turn_operator_resolved` | Root reconciles inert unknown work with the service stopped | Terminal `abandoned`, full reservation charged, unchanged baseline, operator/evidence attestation; session and project unblocked. |

### Operator resolution of an unknown turn

`tbound product turns resolve --turn ID --reason TEXT [--dry-run]` runs as root with
`tbound-product.service` stopped. The root front end verifies the unit is inactive
with no remaining cgroup processes, then starts a temporary systemd sandbox with
the same filesystem and no-new-privileges settings used for the signed scanner
calibration. Its root helper rechecks exclusion and delegates catalog access to
the service account over a root-authenticated inherited socket. The catalog writer lock is
held throughout evidence checks and append, excluding another service writer.
Only `unknown` turns are accepted; a linked turn must first undergo restart
reconciliation. Missing evidence refuses resolution with its failing check.

Resolution reuses the registered managed-root baseline scan and existing-only
Store/journal/checkpoint readers. The measured tree must equal the admitted
baseline. Ordinary resolution requires no Store quarantine or unknown effect,
terminal or retired effect reservations, and an empty command journal.

A narrow interrupted-Bash path instead verifies the retained Store
authorizations, command-owner protocol, exact effect reservation, and durable
`product_command_scope_drained` record. It abandons the private candidate
without certifying the command result. Other command activity still refuses.
No publication record may name the workflow. Publication ambiguity still
refuses resolution. A provider reservation or captured call may remain
unresolved when the dead supervisor can no longer execute it. Nothing is
resumed or imported. There is no `--accept-current-baseline` override.

The command attests the tree at append time, not only when it starts checking.
It keeps the project directory and the retained journals open from the first
measurement through the catalog append and measures the baseline again
immediately before the append, refusing if the second measurement differs from
the first or from the admitted baseline. No other process may write the project
while `resolve` runs; there is no lock excluding root, maintenance, or other
non-service writers.

One `product_turn_operator_resolved` record commits the turn/workflow, root UID,
nonempty plain-text reason (at most 256 UTF-8 bytes), UTC timestamp, admitted and
measured baseline digests, and a domain-separated evidence commitment over the
retained Store/provider/command journal heads and protected checkpoint digest.
The reducer verifies record shape, turn/workflow/baseline bindings and evidence
commitment on both append and replay; duplicates and malformed/forged bindings
fail catalog replay. The protected journal supplies integrity, not a signature
against its own privileged owner. Dry-run holds the same lock and checks the
same evidence but appends nothing, including no owner epoch.
An abandonment record containing `command_owner_head` fails closed on older binaries whose strict catalog decoder does not recognize that field; keep this replay incompatibility in mind during the format-3 to format-2 rollback drill.

The outcome is terminal `abandoned`, reason `operator_resolved`, with no new execution
receipt and `context_eligible=false`. A previous review receipt stays available
for historical review events. Observation preserves the unknown terminal and
appends the operator terminal, so issued cursors remain valid. It is never selected as a predecessor.
The full original reservation remains charged, with no refund; if review already
settled usage, its earlier refund is charged back. Session revision advances,
session and project blocks clear, and their baseline stays unchanged. New turns
and sessions are admitted subject to the ordinary remaining budget and turn cap.
This is administrative abandonment of inert work, not successful execution or
workflow repair; retained evidence is preserved for inspection.
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
| `linked`, no `terminal` | Unresolved. The session and project stay blocked. Restart reconciliation through `workflowrecovery` is P4. If it still ends unknown, the operator resolution command can abandon provably inert work. |
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

`tbound-product-turn-receipt/v2` (`productsession.TurnReceipt`), strict JSON, at most 64 KiB, written by `cmd/tbound` and stored in `product_turn_terminal` or `product_turn_awaiting_review`. The catalog decodes it strictly and refuses to settle unless its identity, workflow link, imported prefix, predecessor, baselines, outcome, settlement and usage match the turn and the record. Settlement is `supervisor-loop-closed` for worker kind `supervisor-loop` and `stopped` for worker kind `pi`; no other pairing is accepted. `unverified_inputs` is required, which is why the schema is v2: the catalog refuses a v1 receipt. A published receipt names its publication transaction:

`unverified_inputs`, `session_id`, `turn_id`, `ordinal`, `workflow_id`, `run_id`, `conversation_id`, `worker_kind`, `worker_instance`, `provider_journal_head`, `provider_journal_count`, `store_journal_head`, `store_journal_count`, `imported_prefix_digest`, `predecessor_receipt_digest`, `baseline_digest`, `result_baseline_digest`, `outcome`, `settlement`, `publication_transaction_id`, `usage`, `tool_sequence`, `denials`.

Before it imports a predecessor, the next turn reopens the predecessor's retained journals read-only and checks their verified heads and counts against this receipt. A mismatch refuses the launch with `RefuseUnlinkedTurn(turn, PREDECESSOR_EVIDENCE_INVALID)`: the new turn is `failed` with a full refund, and the session is blocked.

Transient journal lock contention or resource exhaustion abandons the unlinked launch with a full refund and leaves the session open for a new request key. It is not invalid predecessor evidence.

`worker_kind` is `supervisor-loop`, because the worker is the supervisor's own in-process agent loop. Only its provider conversation is scripted in tests. Receipts never claim an OS process settlement that did not happen. A real Pi worker per turn needs the product guest and is pending.

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
| `abandoned` (terminal) | Operator resolved inert unknown work; never context. Full reservation consumed. | Unchanged, unblocked. |

Assistant completion is not settlement. A turn is terminal only after the worker loop ended, the conversation closed, the evidence bundle was read, and the publication step returned a definite status.

### Provider capture refusals in product turns

Product turn construction explicitly enables `providerbridge.Config.ProductCaptureRefusals`; its default is false. A fully received HTTP 200 response that the strict capture parser rejects (for example, multiple or malformed tool calls) ends the turn as `failed` once its host-call reservation is durably completed. The returned error and the terminal event shown by `tbound chat` say `the provider returned a response TBound cannot use (<reason>); the turn was not applied`. No rejected assistant text or tool call enters conversation history or reaches the worker. The managed project stays at its admitted baseline, and the next turn in the session is admitted.

The product-only provider journal record `provider_capture_refused` binds the workflow, turn, exact reserved request and host-call identity, HTTP status, rejected response bytes, response digest and normalized parser refusal reason. Multiple or malformed calls retain that specific reason; other parser failures use `invalid provider response` so map iteration cannot change replay and parser errors cannot expose rejected field names. As with the existing `provider_exchange_unknown` record, raw response bytes remain in the host journal for verification; they are withheld from the worker. Replay requires the explicit product option, verifies the request against the admitted history and reservation, recomputes the digest and parser rejection, and requires the refusal to end the journal at the pending exchange. A changed reason, usable response, unmatched reservation or out-of-order record fails closed. The governed checkpoint completion path performs this replay before marking the host call terminal and committing the refusal record hash.

The refused exchange consumes one normal host-call and its reserved token allowance, with no tool-effect charge for rejected calls. Only after checkpoint completion and turn settlement is the unused turn reservation refunded. A crash before the refusal record or between that record and checkpoint completion leaves the host call unresolved: restart records `unknown`, consumes the reservation and blocks the session. After completion, restart verifies retained refusal evidence and the protected checkpoint prefix and settles `failed` with settlement `reconciled-after-restart`, subject to the same clean Store, unchanged baseline and absent publication checks as other interrupted turns. Restart never resumes the refused response or conversation.

Transport errors, incomplete responses and other unclassified exchange failures keep the existing UNKNOWN behavior. Thesis/research conversations, governed research recovery and corpus runs do not enable this option: the same rejected capture still writes `provider_exchange_unknown`, with its existing evidence format and unresolved reservation behavior.

### Soft refusals in product turns

For an otherwise allowed product call, the Store checks preconditions under its lock against the verified sealed generation before authorization or effect reservation. A missing read path, absent or ambiguous edit old text, or a write or edit targeting a directory returns a short error and a static hint. The codes are `read_missing`, `target_not_regular`, `edit_old_text_absent` and `edit_old_text_ambiguous`. The result text is `Error: <code>. No tool effect occurred.` followed by `Hint: <static hint>`. The next provider request carries that result and the conversation continues. A missing edit target, a missing write parent, a read of a directory, a non-directory ancestor and overlapping edits are outside the soft-refusal scope and keep today's fail-closed behavior. Non-regular targets are also refused if representable; current sealed-tree admission already rejects symlinks and special files. Unknown or malformed tool arguments rejected by provider capture end the product turn through the capture-refusal disposition above; they never continue as soft refusals. Argument errors discovered after capture (such as an invalid read offset), Bash, I/O failures, changed generations, and failures after authorization retain the existing fail-closed behavior. Research calls do not use this path.

The distinct `provider_proven_unexecuted` record binds the workflow, conversation, captured call, tool, captured and typed argument digests, sealed generation and tree digest, refusal code, static hint, and exact result text. It is neither a gate DENY nor an ALLOW result. Before release, the protected owner checkpoint retires the captured call with the domain-separated `tbound-proven-unexecuted-call-retirement/v1` commitment to that record. A retired call cannot reserve an effect. Replay checks the capture and recomputes the precondition and exact result against the retained sealed generation; altered or different evidence fails closed.

The provider exchange consumes its normal host-call and token allowance. A soft refusal reserves no tool effect and adds no tool-effect charge. Repeated refusals are bounded by the turn's host-call limit and worker step bound.

Restart never resumes a refused call or its conversation. For a soft refusal, a crash before `provider_proven_unexecuted` or before its captured-call retirement leaves an unresolved capture and ends UNKNOWN. After durable retirement, clean evidence settles the turn as failed under the existing restart contract. Retained refusal evidence is replayed before restart settlement and when rebuilding an eligible predecessor. Tampered evidence blocks launch.

Restart verifies the protected provider checkpoint prefix for every restarted turn, including turns with no retained refusal record. Product capture refusals (`provider_capture_refused`) are also replayed before restart settlement. A truncated provider journal with a clean Store now ends UNKNOWN; previously it could settle as failed. Live progress recomputes only refusals after the verified checkpoint prefix. Restart and predecessor replay recompute every retained refusal against its sealed generation.

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
- Linked and running: the supervisor loop checks for cancellation before every provider exchange. It never interrupts a call in flight. At the next step boundary it stops, the conversation closes, and the turn settles `cancelled` with usage charged. Nothing is published, and the project must still equal the admitted baseline.
- Awaiting review: the turn is discarded with reason `cancelled`.
- After `product_turn_resolution_requested` with `publish`, or once an auto-mode finalizer has started, cancellation cannot promise rollback. The request is recorded, and the turn reports its real disposition.

### Restart

On `Open`, unresolved turns are rebuilt from the catalog. The driver's `ReconcileAfterRestart` runs before any new admission is served. It never publishes on reconstruction alone.

| Retained state | Action |
| --- | --- |
| `admitted`, no link | `AbandonUnlinkedTurn`, as in milestone 1. |
| `linked`, no terminal | Reopen the retained Store and journals read-only, plus the owner checkpoint. Verify any product capture refusal (`provider_capture_refused`) by strict replay under the explicit product option. If the Store reconstructs with no quarantine or unknown effect, no reservation is open, the project equals the admitted baseline, and the project publication journal has no transaction for this workflow: `CompleteTurn(failed)` with settlement `reconciled-after-restart` and usage from the retained ledger counters. Otherwise `MarkTurnUnknown`. |
| `awaiting_review`, no resolution request | If the project equals the admitted baseline and no publication transaction exists for the workflow: `discarded` with reason `restart`. Otherwise `unknown`. |
| `awaiting_review` with resolution request `publish` | If the publication journal shows the workflow's transaction `SUCCEEDED` and the project equals the sealed candidate: `published`. If no transaction exists and the project equals the admitted baseline: `discarded` with reason `restart`. Otherwise `unknown`. |
| `awaiting_review` with resolution request `discard` | `discarded` with reason `user`, after the same baseline check. |

Settlement `reconciled-after-restart` is valid for worker kind `supervisor-loop`, because the process that hosted the in-process worker is gone. A real Pi worker will need its drain proof instead.

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
- `LinkTurn` refuses a cancelled admission with `CANCELLED`; the driver then records an unlinked `CANCELLED` refusal with a full refund. A new `publish` resolution after cancellation is also refused with `CANCELLED`; `discard` remains allowed. Both checks are reducer rules, including on replay.
- The driver mutex orders durable cancellation, review resolution, and the auto-publication start decision. Before auto-publication starts it rechecks cancellation under that mutex, then releases it before the finalizer runs. Review deadline validation, durable resolution and removal from the pending registry share the same critical section.
- `discarded` with reason `restart` or `cancelled` is accepted even after a `publish` resolution, because the driver reports the real disposition.
- `MarkTurnUnknown` also accepts an `awaiting_review` turn, without settling its usage again.
- A reviewed terminal receipt replaces the awaiting-review receipt, so the next turn's predecessor commitment is the terminal one.
- A reviewed terminal receipt must keep every execution fact of the awaiting-review receipt. Only the outcome, result baseline, publication transaction and settlement may change.
- A turn refused with `CANCELLED` before its link is never a predecessor, the same as an abandoned turn. It did no work and has no evidence to import.
- The awaiting-review record requires `review_timeout_seconds`. Earlier test-only journals without it are not migrated, because no product catalog has been deployed.

## Milestone 3: project policy, import and export (P5)

### Product scope

`tbound-product-scope/v1` is a new admission rule, `product-scope/v1`, next to the research rules. Research exact-path and promote-all behaviour and their digests are unchanged.

| Field | Meaning |
| --- | --- |
| `writable` | Canonical relative paths. Each one is an exact file or a subtree root. A change is admissible only at or below one of them. |
| `read_only` | Canonical relative paths. Changes at or below them are refused, even inside a writable subtree. |
| `protected` | Canonical relative paths. Changes at or below them are refused first. These are also the private paths excluded from import. |

- Each list is strictly sorted and holds at most 128 entries. No entry may appear in two lists. Nesting is allowed and resolved by precedence: protected, then read-only, then writable.
- Matching is by whole path components: `src` covers `src` and `src/a`, never `srcx`. Paths are the Store's canonical relative paths, so there is no string-prefix relaxation and no symlink following.
- Every changed path of a candidate is checked: creates, modifies, deletes, both ends of a rename, directory creation, and metadata changes. Any refusal denies the whole candidate.
  - A protected or read-only path gives `scope_protected_path`.
  - A path outside every writable entry gives `scope_path_not_covered`.
  - These are the existing reason codes and hints, so no adapter change is needed.
- The same `Decide` applies to typed `write`/`edit` and to Bash candidate deltas.
- The admission policy digest commits to the rule and all three lists.
- A registration project carries `scope`. A project without one keeps the milestone 1 behaviour.
- A session may be created `read_only`. That empties `writable` for its turns, so no tool can change the project. A read-only command can still run.

Decisions taken during implementation:

- `read_only` is a `create_session` request field and a session field. Both omit it when false, so existing digests and records are unchanged. The request payload digest covers it, and replay recomputes that digest from the record.
- A turn's effective scope is decided once, at admission: the project's `scope`, with `writable` emptied for a read-only session. A read-only session on a project without `scope` runs under the product rule with all three lists empty, so it denies every change. A project without `scope` and a normal session keeps the exact-path rule.
- `product_turn_admitted` records the effective scope as `scope` (absent for the exact-path rule). The reducer requires it to equal what the loaded registration and the session produce, like `policy_digest`. The turn's Store is created under it, and a retained Store reopens under the turn's own recorded scope, never under the registration in force later. A new registration may therefore change a project's scope between turns without invalidating a predecessor.
- A typed `write`, `edit` or `delete` still declares its own path as its operation's output scope, and that field is still validated. Under `product-scope/v1` `Decide` ignores it, so it can neither widen nor narrow the rule.
- A change list holds one entry per path whose state differs. A rename is two entries (the old path removed, the new path created) and each is checked, so the two ends of a rename may sit on different sides of a scope boundary and any refusal denies the candidate.
- A candidate refused by delta admission is a normal DENY for the provider, with the reason code and hint. The turn records it in the receipt's `denials` and continues. If nothing else changed the turn is `no_diff`.
- Registration validation uses the Store's own list validator: canonical relative paths, strictly sorted, at most 128 per list, no path in two lists. An empty `scope` object is a valid scope that admits nothing, and it is not the same as no `scope`.

### Bash in product turns

Product turns allow Bash when the registration project allows it. Under the product rule, the command's output collection is decided by the effective product scope, not by an operator list of exact paths. A Bash turn must also be reopenable as a predecessor, so its retained command settlements are verified by the historical verifier. Real cell execution needs the product guest. Local tests use the existing development command runner.

Decisions taken during implementation:

- A project allows Bash when its installed gate policy, whose digest the signed registration commits to, has an `allow` rule for `bash`. `bash_output_paths` is not the switch. The registration still requires it, but it only has a role under the exact-path rule.
- A turn under `product-scope/v1` declares an empty output scope for every Bash command. `Decide` ignores it. A project without a scope keeps the exact-path rule, so its registered `bash_output_paths` are the declared scope and the research behaviour is unchanged.
- The Store already imports the whole settled command view and computes every changed path, so the product rule needs no new collection mechanism. A denied candidate promotes nothing, and the provider gets the DENY with its reason code and hint. A read-only session has an empty `writable` list, so a command that writes is denied while a command that only reads runs and leaves the turn `no_diff`.
- A publication trust for a Bash turn must implement `workflow.TerminalOriginResolver`, because the terminal transition of a Bash candidate is a command lease. A turn whose trust lacks it is abandoned before any work.
- The service gives the turn driver a command runner factory. A turn whose policy allows Bash on a service without one is abandoned before any work, with no budget consumed. Production fills the factory with the D09 Podman/crun cell runner of a qualified host. The host profile admits a policy that allows Bash only through [command qualification](#command-qualification-at-start). The qualification runs on every start. The demo VM passed qualification and live Bash turns on 2026-11-04 with build `d75ac188`. The development runner is never wired into production.
- The receipt commits the turn's command journal by head and record count, next to the provider and Store journals, with `command_journal_head` and `command_journal_count`. A turn that ran no Bash omits both. The next turn checks them before importing history.
- The model sees what a command printed. A product turn's Bash summary carries `stdout`, `stderr`, `stdout_truncated` and `stderr_truncated` next to the exit code, containment status and runner profile. Each stream is valid UTF-8, with every invalid byte run replaced by U+FFFD, and at most 16 KiB (`productCommandOutputBytes`). A longer stream keeps its tail, because the end of a build or test run holds the failure, and its truncated flag is true. The cut never splits a character. The four members are always present, so an empty stream is an empty string. The thesis summary keeps its frozen 4 KiB tails, raw byte counts, and truncation flags.
- A failed command (a non-zero or unobserved exit) keeps the `command_failed` status and hint, and its output is shown with it.
- The output is passed through as model context. It is the workspace command's own output and is never filtered, so a command can print any file it can read in its workspace. The supervisor adds no credential or host path to it. The service's `tool_result` observation still carries no tool output.
- The output is durable and replay-verified. The Store keeps each command's full stdout and stderr (up to 1 MiB per stream) in the command's effect result. Replay rebuilds the summary the model saw from that retained result with the turn's pinned bound, and requires it to equal the recorded provider result byte for byte. A forged output, or a replay under another bound, fails the predecessor check. The bound is part of the evidence format, so changing it makes earlier Bash turns unverifiable as predecessors.
- Reopening a turn replays its retained Bash effects against the retained command settlements through a read-only opener. For D09 the opener is the research recovery verifier over the command journal. Without an opener a retained Store that holds a Bash effect is refused. Restart reconciliation of a linked turn reopens the Store the same way, and a turn it cannot verify ends `unknown`.

### Import

- `tbound-project-bundle/v1` is a bounded manifest plus content stream that the user's own client builds from the original project. The service account never reads the original repository.
- The bundle covers regular files and directories only. It records each path's type, mode bits (executable or not), size and SHA-256.
- It always excludes `.git` administration. The registration is created after import, so the bundle cannot read its protected paths. The user passes those paths, and any private paths, with `--exclude` (repeatable). Every excluded path is listed as excluded.
- Symlinks, hard links, devices, sockets, FIFOs, special metadata, files over the size limit, and totals over the limit are refused with the path and reason.
- The client prints every included path and every excluded path, one per line, before sending.
- The service validates every path, type, size and digest against the manifest before it creates the managed project directory. A mismatch refuses the whole import and writes nothing.
- Import creates a new managed project and returns its measured baseline. Registering that project in a signed registration remains the administrative step.

### Export

- `tbound-project-export/v1` is a service-produced, digest-bound bundle. It holds the original baseline digest, the published or candidate tree digest, and for each changed path its operation, type, mode and content digest, plus the content itself.
- The user's client applies it only to a clean Git worktree whose tracked tree matches the bundle's original baseline. It then rechecks every target path before writing.
- It refuses dirty trees, conflicts, untracked files at target paths, symlinks, and mode changes other than the executable bit.
- Deletions and new files are supported. Untracked files elsewhere are left alone.
- If any check fails, nothing is written and the original content is kept.
- The client writes a recovery record before applying, and removes it only after a verified result.
- If writer exclusion cannot be established, the client offers the bundle for manual application and claims no race safety.

### Import and export decisions taken during implementation

- Both streams are the format line, a 4-byte big-endian header length, a canonical JSON header, and then the raw file contents in header order. The reader re-encodes the header and refuses any byte difference, so exactly one encoding is accepted. Digests are `sha256:` over the header bytes.
- Every parent directory must be listed in the manifest. Any component equal to `.git` under Unicode case folding and a root `lost+found` are always excluded. Import and export validation refuse Git administration under the same rule. Excluded subtrees are never read.
- Scans and apply open descendants with Linux `openat2`, using `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS | RESOLVE_NO_XDEV`. This refuses same-filesystem bind mounts as well as other mount crossings, including mounted regular files. Kernels without the required resolution support fail closed.
- Files and directories with POSIX access/default ACLs or `user.*` or `trusted.*` extended attributes are refused during scans and apply target rechecks. `security.*` labels are tolerated as host policy; bundles do not carry or restore them.
- Only the executable bit is carried. New files are created 0644 or 0755. A change of type (file to directory) is refused.
- Apply validates the baseline digest, the declared target digest and every content digest before producing a patch or writing targets. It refuses changes to existing files Git does not track, target paths marked assume-unchanged or skip-worktree, and existing targets whose raw bytes differ from their committed HEAD blob, even if Git status reports clean.
- By default `tbound project apply` leaves the worktree unchanged and creates a new `tbound-export.patch` in `--state-dir`. An isolated Git object store and index generate a binary-safe patch with file creates, deletes and executable-bit changes. The client runs `git apply --check` and prints exact `git -C ... apply --check -- PATCH` and `git -C ... apply -- PATCH` commands. The user excludes concurrent writers before checking and applying it. Git cannot represent empty-directory changes; patch mode refuses those changes, including changes that would require retaining an empty directory.
- Direct writes require both `--direct` and `--writer-exclusion-held`. This is the user's guarantee that no concurrent writer can alter or move the worktree for the whole apply; the client cannot establish that guarantee itself. Immediately before each rename it resolves the parent again from the worktree root fd and compares device/inode with the held parent fd. A mismatch aborts and retains the recovery record even before the first target mutation. This check does not replace writer exclusion.
- The state directory must be caller-owned, exactly 0700, outside the worktree, with no symlink ancestors or group/other-writable non-root ancestors. An exclusive lock serializes clients using that directory. Record creation, updates, removal and patch creation use its held directory fd. The direct-mode recovery record lists pre-apply entries and the actual absolute paths changed so far; restoring relies on the clean tracked tree in Git. The client syncs executable-bit changes and retains the record on sync failure.
- `apply` takes the same `--exclude` list as `bundle`, so both sides compute the same manifest.
- `import` and `apply` do not check which account runs them. The separation between service and user comes from who runs each command, as the help text states. The service never reads or writes the original repository.

### Development environment

The current signed base cell provides Bash and Node with networking disabled. A Go toolchain image and a pre-filled module cache are deferred. The supervisor-loop worker runs the conversation, not Pi.

The tool schemas a product turn advertises match the durable executor's behaviour. A research route keeps its frozen schemas, which promise more than the executor does, because its provider-request digests were measured with them. The bash row below is the product turn's. Its route carries `Route.CommandOutput`, which the authority sets when its executor has a command output bound. A `serve --pi` session's executor retains the thesis projection of 4 KiB tails and raw byte counts. Its route is unmarked and its bash description remains the short one every engineering request carried before product turns showed output ("Execute a bash command. Requests are forwarded to tbound."). The schemas, and so the pinned digests, are the same in both modes.

| Tool | Advertised to the model | What the executor does |
| --- | --- | --- |
| `read` | The whole contents of an existing UTF-8 text file at a workspace-relative path. No `offset` or `limit`. A missing file, an absolute path, a file of about 1 MiB or more, or a file that is not valid UTF-8 ends the turn. | Reads the whole bounded file and releases the requested line window when `offset` or `limit` is present. Both accept positive integers; offset is 1-indexed, and an offset past EOF releases empty content. A missing path is a soft refusal (see "Soft refusals in product turns"): the supervisor proves it before authorization, the turn continues, and the Store is not quarantined. The advertised text above still says a missing file ends the turn and omits line windows, because the schema text is pinned by digest; changing it needs an adapter rebuild and re-pin. The Store fails closed on an over-bound or non-regular target and quarantines. A file that is not valid UTF-8 cannot be released as a read result. Any of these ends the provider conversation as unknown. Images are not supported. |
| `write`, `edit` | A workspace-relative path. An absolute path ends the turn. | An absolute path is invalid, and the invalid call ends the provider conversation as unknown. A target that is a directory, and an edit whose old text is absent or ambiguous, are soft refusals. A missing edit target, a missing write parent and overlapping edits keep the fail-closed behavior. |
| `bash` | No `timeout`. The last 16 KiB of each stream. A command that runs longer than 10 minutes, or prints more than 1 MiB on one stream, is stopped and returns no result. | It ignores a `timeout` argument. The command cell stops a command after 10 minutes, and the development runner after 30 seconds. A stream over 1 MiB withholds the result. |

Go still parses `offset`, `limit` and `timeout` when a call carries them, because research calls and retained provider evidence do. The adapter registers the same text, except that its bash description is the short `serve --pi` one, and `adapter/profile.json` pins the digest of each schema. `TestAdvertisedToolSurfaceEqualsTheAdapterTextAndItsPins` and the adapter's `proxy-schema.test.ts` hold the broker, the adapter and the pins together, and `TestResearchToolSurfaceIsFrozen` holds the research surface. Changing a schema changes the adapter bytes, so it needs the adapter runtime bundle to be rebuilt and re-pinned.

## Milestone 4: product service and terminal client (P6)

### Service

- `tbound product-serve --config /etc/tbound-product/service.json` runs the product service, owned by the product service account. Its config names:
  - the signed registration, its signature and public key;
  - the catalog journal and the state root;
  - one managed directory per registered project;
  - the host profile's installed inputs (see [Host profile](#host-profile)): one gate policy file per project, the private Git executable with its pinned digest, and the signed xattr calibration;
  - the worker mode: `supervisor-loop`, the supervisor's own in-process agent loop. `pi` is refused with its own error until a real Pi worker per turn exists on a qualified host.
- Every path must be a product default or below one. None may be a thesis path. The Git executable must sit below `/usr/local/lib/tbound-product`. The other paths sit below the registration, catalog or socket directories.
- On start the service verifies the registration and opens the catalog. The host profile then reads each project's gate policy structurally. When a policy allows `bash`, or a retained command owner journal exists, it loads the signed command host and runs the scope gate. A policy that allows `bash` also prepares the command runtime directories and runs every [command qualification](#command-qualification-at-start) phase. The scope gate is a hard error: the start stops before the catalog is reconciled, and no later log line can swallow it. The service then runs `ReconcileAfterRestart` before it accepts any request, and only then listens on `/run/tbound-product/product.sock`, socket mode 0660, group-restricted. Startup order, in the code: `installProductGatePolicies` and `qualifyProductCommandHost` (`supervisor/cmd/tbound/product_service_host_linux.go`, `product_command_qualification_linux.go`), then `startProductService` (`product_service_linux.go`).
- Before replacing a stale socket it acquires an exclusive nonblocking flock on `<socket>.lock` and holds it for the service lifetime, independently of the catalog lock. A held socket lock refuses startup.
- The principal is the connection's `SO_PEERCRED` UID. The catalog checks it against `allowed_principals`. Payloads never carry a principal.
- Admitted turns run in the service, not in the client. A client that disconnects leaves its accepted work running. One goroutine drives each turn. A review-expiry ticker calls `ExpireReviews`.
- Shutdown stops new admissions, lets running turns reach their next step boundary, and closes pending reviews. A pending review then becomes `discarded` with reason `restart` on the next start.

### Host profile

`tbound product-serve` fills every trusted piece of a turn from installed inputs, and refuses to start, before it opens the socket, when one is missing or differs from what the signed registration commits to. The service config adds these fields:

| Field | Meaning |
| --- | --- |
| `gate_policy_paths` | project ID to its installed gate policy file, one per registered project |
| `git_path`, `git_digest` | the private Git executable and its pinned `sha256:` digest |
| `xattr_calibration_path` | the signed xattr calibration. Its base64 signature sits at the same path plus `.sig` |

What the host profile does with them:

- **Input files.** Each is opened without following a symlink. It must be a regular file owned by root or by the service, writable by neither group nor others, and within its size bound. The service config is read the same way.
- **Publication fence.** Import creates an empty, service-owned `lost+found` directory and startup refuses a missing, nonempty, symlinked or incorrectly owned fence. Its mode is 0755 to satisfy the canonical workspace scanner. The imported project root stays private at 0700. Imported content uses canonical 0644 files and 0755 executables and directories.
- **Gate policies.** Each file is parsed and its digest compared with the registration's `policy_digest` for that project. A mismatch refuses the start.
- **Bash.** A policy with an allow rule for `bash` refuses the start unless command qualification passes in this process. A failure names the project and the failing prerequisite. An assigned runner, a stale qualification record, or a doctor run never admits Bash. The per-turn refusal described under [Bash in product turns](#bash-in-product-turns) stays as a second line. Nothing wires the development runner into production.
- **Provider.** Every profile the registration lists must be in the compiled table, which holds `opencode-go-engineering` only. A turn's conversation is built for its session's profile and refused when that profile is not in the registration. The endpoint, model and request shape are compiled in. No config field or environment variable changes them. The key is checked before listening and read again per turn from `/etc/tbound-product/credentials/<profile>.key` under the provider credential rules (service-owned 0700 directory, 0600 file, protected ancestors), so a rotated key needs no restart.
- **Prompts.** The system and developer prompts are compiled in and name no tool, because the policy decides the tools. They are part of every turn's retained evidence, so changing either text breaks the replay of turns recorded under the old one.
- **Git.** The config's `git_digest` must equal the signed registration. The compiled host identity must also match the registration before listening. The executable's bytes and mode are checked against `git_digest` at start and again when each turn pins it. Its execution boundary is labelled `product-supervisor-private-git-admin/v1`: Git runs in the supervisor's own process, not in a command cell.
- **Xattr.** The calibration must verify under the registration public key and cover the managed-writable source profile. At start, and again before each publication, the filesystem, mount options and scanner identity (non-root UID and GID, no capabilities, no new privileges, initial user namespace) of the state root and of every managed project root must equal the calibration. Each managed project is opened through its registered root identity check. The attestation carries the calibration's bounded authority only: the administrator's signed statement that this filesystem, mount and scanner hide no `trusted.*` attribute. It is not proof that the unprivileged scanner enumerates every attribute, and no managed live-root lease backs it.
- **Worker steps.** The step bound of a turn is the registration's per-turn host-call limit, because one step is one provider exchange.
- **Publication trust.** A publication needs the turn's authority to be bound and the provider profile to be registered. It needs the supervisor loop's conversation to be closed and no unresolved effect, host call or Store quarantine. It re-verifies the xattr identity and needs the managed project to still equal the turn's baseline. For command effects, it verifies the settled command receipt and resolves the command lease through the turn's authority. The turn's own authority verifies every transition and origin. At startup and before every publication, a descriptor-based walk refuses symlinks, foreign owners and group/other-write permissions on every managed root and on every ancestor from `/`, so a project need not sit beneath the state directory. Every directory must be owned by root or by the service account, and the walked root must be the registered opened root. Root and the service account are trusted writers. A root writer racing publication is not excluded. A racing process under the service account is also not excluded. An edit made before publication fails the baseline check. A Bash candidate must pass the same scope and publication checks as other effects.

Setup requires the same fields and writes them into the service config. It does not install the policy files, the Git executable or the calibration. The administrator places them first, and setup and the service both verify them (see [Host inputs](#host-inputs)). `tbound product calibration measure` and `sign` produce the calibration. The research `tbound-profile xattr-calibration-*` commands write the same artifact, but they read a raw private key and write a raw signature, while product keys and signatures are base64.

### Command qualification at start

A policy that allows `bash` needs a command cell that this process has just qualified on this host. The service repeats qualification on every start. It persists no ready state, and no setup, doctor or environment setting skips it.

On a start that admits Bash, the host runs these steps in order, and the first failure stops the start before the socket opens:

1. It loads the signed command host from its fixed installed paths. A missing, unsigned or mismatched input refuses the start.
2. It prepares `tmp`, `cache` and `containers` under the unit's runtime directory. Each must be owned by the service account with mode 0700.
3. The scope gate reconciles every command owner journal under `<service state_root>/turns` and `<service state_root>/qualification`. An opened record names one exact cell scope and its cgroup identity. Before each Podman launch, a synced `product_command_intent` record names the assigned container and the view's device and inode. After authenticating the started process, the launcher syncs `product_command_started` with the mount-namespace inode before acknowledging execution. Callback errors abort launch or acknowledgement. Recovery uses the revalidated product environment to remove each recorded container, verify its absence, and scan for processes or open descriptors that reference its namespace. It also revokes and removes the exact recorded scope. An absent scope still requires container and mount cleanup proof, plus no process reporting the old scope path. Only then does recovery append `drained`. A version-1 owner with no intent proves that no launch reached the callback. Older ownership records without this version refuse startup. Before a `started` record, there is no authenticated namespace identity: a proved writer-free scope and absent container close cleanup authority. A recorded namespace additionally requires proof that no process or open descriptor references it. Unknown children and mismatched identities also refuse startup. An empty journal from a failed allocation blocks nothing unless an unrecorded child remains. Cleanup never writes a command receipt, so a linked turn that depended on it stays `UNKNOWN`.
4. The qualification runs in a disposable cell under `<service state_root>/qualification/<run-id>`. The host builds a private Store, a source with one seed file and a command journal there, all service-owned with mode 0700 or 0600. It runs fixed local commands through the same per-turn runner and launcher that turns use, with `/bin/bash -lc`, the signed base cell and zero delta. Each command gets a fresh cell, and its sealed receipt is bound to the Store's operation and verified as publication verifies it. No provider API request is made, no catalog turn is created and no project is changed. The network control connects to the registered provider on port 443. The phases run in this order, and the first failure stops the run:
   - `proof-ack` runs a one-line command. The receipt's proof must show that the entrypoint was ready before the command started, and that the command ran as the signed identity with no capability, `no_new_privs`, seccomp, Landlock and cgroup v2.
   - `ls` lists the seed file and changes nothing.
   - `scoped-write-readback` writes one file in a declared output scope, checks that the Store's transition changed exactly that path, then reads the file back in a second cell.
   - `rootfs-denial` runs a probe intended to create a file at `/`, `/etc` and `/usr`. Each write must fail with a reported `Read-only file system` or `Permission denied`; any other failure makes the phase fail as inconclusive. It also checks the denial marker, absence of those files, and no Store changes.
   - `limits-mapping` checks the authenticated proof's ID maps against the signed one-ID mapping, then checks the in-cell maps and user and group IDs. Before acknowledgement, the supervisor opens the authenticated leader's actual cgroup through the held command-scope descriptor. It reads `pids.max` and `memory.max` through that live descriptor and retains both observations in the qualification evidence. Both must equal the signed template. Missing observations fail the phase. The cell does not read cgroup files, and the frozen Landlock rules remain unchanged.
   - `network-probe` runs the network probe below.
   - `settlement` drains the scope and launcher, verifies every command's receipt again, and checks that the Store's chain verifies and that no command owner remains.
5. Only after every phase passes does the host install the command runner factory, and only then the provider, trust and publication hooks. A deny-only start with no command journal skips steps 1 to 4 and installs only the read-only history opener. A deny-only start with a journal runs steps 1 and 3, and never installs a factory.

A failed run closes its handles only after settlement succeeds. Failed settlement retains the runner, Store and journals in the environment's retained-owner collection. Deferred teardown does not retry that settlement or release dependent handles. This also applies to construction failures after runner creation. Explicit retained-owner cleanup may retry later. Unproved cleanup leaves the owner `opened`, and the next start drains it or refuses.

**Evidence.** The installed service sets `state_root=/var/lib/tbound-product/turns`. Qualification evidence is therefore `<state>/turns/qualification/<run-id>/evidence.json`, where `<state>` is `/var/lib/tbound-product`. Each run writes `evidence.json` (mode 0600) in its run directory, whether it passed or failed. It names the run, the profile and image digests, each phase with its status, each command with its lease, view, exit code, stdout digest, receipt digest and changed paths, the supervisor's live cgroup limits, and the network report. After successful qualification, the service keeps the newest five completed, fully drained runs and deletes older ones with their disposable Stores. It preserves unresolved runs, missing evidence and unproved ownership. Retention and owner scans honor cancellation. A run killed before it wrote evidence leaves none. `tbound product doctor` shows the latest retained run as an advisory `command qualification` line. A passed run is a `PASS` that says every start qualifies again and that only the self-test establishes readiness. A failed, missing or unreadable record is a `WARN`. Doctor never reports Bash as available. For a policy that allows Bash, the static input check reports `configured, not qualified`.

**Network probe.** `productNetworkProbe` takes the expected policy as input and currently accepts only `deny-all`. It runs the signed image's Node executable through the base cell. The destination comes from the first provider profile in the signed registration, looked up again in the compiled `productprovider` table. The current profile uses `https://opencode.ai/inference/go/v1/chat/completions`. No client URL or new endpoint is accepted.

Before the cell runs the probe, the service resolves that hostname and completes a TCP handshake to one resolved IPv4 address on port 443. It also connects to the host listener. If either control fails, qualification fails. A resolved AAAA address is included only when TCP to that address on port 443 succeeds from the service. Otherwise the report records `ipv6: not available on host`. The cell cannot supply the positive controls.

The program prints structured results for these checks:

- `name-resolution`: `dns.Resolver.resolve4` queries the same provider hostname. Only `ECONNREFUSED` counts: it is what c-ares reports when no resolver is reachable. Negative answers such as `ENOTFOUND` and `EAI_NONAME` can come from a working resolver. `EAI_AGAIN` and timeouts are also ambiguous, so they fail the check.
- `ipv4-connect`: TCP to the exact IPv4 address that passed the service control must fail with `ENETUNREACH`, `EHOSTUNREACH` or `EACCES`. `EACCES` is what the cell's Landlock rule returns: it handles TCP connect with no allowed port, and the kernel applies it before any route lookup.
- `ipv6-connect`: when a service-side-reachable AAAA exists, TCP to that exact address must fail with the same errors. Otherwise this attempt is omitted and its host availability is recorded.
- `host-listener`: TCP to the service's loopback listener must fail with `ENETUNREACH`, `EHOSTUNREACH`, `ECONNREFUSED`, `EPERM` or `EACCES`.
- Interfaces from `/proc/net/dev` must contain exactly `lo`. Every IPv4 and IPv6 routing row must also use `lo`.

A timeout, a missing or crashing Node, malformed output, a reachable destination, an extra interface, an external route, a mismatched destination, or a missing or repeated check fails qualification. Permission errors alone do not prove that provider TCP has no route. The report retains the Node version, attempted destinations, concrete outcomes, interfaces, routes and service controls. The launcher still enforces the signed network namespace settings and `--network=none`.

The reusable probe is the check for a later policy-mediated egress tool. Such a tool would run supervisor-side through the gate, like a provider call, while the cell keeps `--network=none`. Step 2 adds no egress tool or allow policy.

**Known limits.**

- On 2026-11-04, build `d75ac188` passed all seven phases on the demo VM. Startup took about three minutes (190 to 202 seconds). The live network report showed name resolution `ECONNREFUSED`, numeric IPv4 and host-listener connect `EACCES` from Landlock, and only `lo`. Service-side controls succeeded. IPv6 connect was skipped because the host had none.
- The cell cannot write `/dev/null`, so commands that redirect there fail. Changing cell access to `/dev/null` needs an entrypoint or image change and is deferred. Before 2026-11-05 the rootfs probe redirected its errors to `/dev/null`, so the round 7 PASS did not establish write denial; the probe now captures the error itself.
- Crash recovery during Bash and publication is fixed in code after that build and awaits the next live run. Round 7 drained the killed Bash cell, but operator resolution refused; its publication crash check was unrun.
- Recovery fails closed on unreadable relevant `/proc` entries, surviving namespace references, Podman errors or unverifiable ownership. Neither a missing scope nor a missing container proves the command outcome. Cleanup never creates a result receipt.
- The service must reach its registered provider during every Bash-enabled startup. A provider outage can refuse startup even when containment works.
- Unresolved runs are never pruned. Repeated unresolved failures can consume storage until cleanup is proved.

### Protocol `tbound-product-protocol/v1`

Each connection carries one length-prefixed strict JSON request and one response, the same framing as the research service. Requests are at most 64 KiB and responses at most 1 MiB.

| Operation | Request | Response |
| --- | --- | --- |
| `create_session` | project, provider profile, publication mode, budget, read_only, request key | session snapshot, replayed flag |
| `submit_turn` | session, expected revision, request key, prompt, optional expected baseline | turn snapshot, replayed flag (the turn starts asynchronously) |
| `get_session` | session | session snapshot with the latest 64 turns plus every unresolved turn, total_turns, turns_truncated, and remaining budget; each included turn carries its state, outcome, candidate, cancel request and resolution |
| `list_sessions` | none | the principal's sessions |
| `observe` | session, cursor, wait_ms (at most 30000) | events after the cursor, next cursor, gap flag |
| `get_candidate` | session, turn | the pending candidate's changed paths, each with operation and a bounded text diff |
| `cancel_turn` | session, turn, expected revision, request key | turn snapshot |
| `resolve_turn` | session, turn, expected revision, request key, candidate digest, decision | turn snapshot |

- Errors carry the catalog's stable error codes. A request never retries on its own. A client retries only with the same request key.

### Observations

Events are projected only from durable records: catalog records for the session, and each turn's provider journal. The kinds are `turn_admitted`, `turn_linked`, `assistant_message`, `tool_call` (tool, verdict, reason code, hint), `tool_result` (bounded summary), `turn_cancel_requested`, `turn_awaiting_review` (candidate digest and changed paths), `turn_resolution_requested`, and `turn_terminal` (outcome and reason).

- The cursor indexes the deterministic event list of the session.
- Cancellation and resolution events are withheld until the turn is final, so concurrent review mutations cannot move events behind an issued cursor. Full turn history comes from observations, not session snapshots.
- Credentials, host paths and raw request bodies are never projected.
- Assistant text comes only from completed provider exchanges, never from provisional output.

### Terminal client `tbound chat`

- `tbound chat --project P [--provider PROFILE] [--mode review|auto] [--read-only]` creates a session. `tbound chat --session ID` reconnects, replays the observations from the start, and never resubmits a prompt.
- Each line is a prompt. The client submits it with a fresh request key and retries transport failures with the same key. It prints assistant messages, tool activity, denials with their hints, and the outcome.
- At `awaiting_review` the client shows the candidate diff and asks Publish, Discard or Keep.
- `/cancel`, `/publish`, `/discard`, `/status` and `/quit` are available.
- The first Ctrl-C cancels active work. A second Ctrl-C, or `/quit`, detaches. Detaching never cancels work.
- Exit codes:

  | Code | Meaning |
  | --- | --- |
  | 0 | normal exit |
  | 2 | refused |
  | 3 | conflict or stale state |
  | 4 | unresolved or unknown outcome |
  | 1 | anything else |

- Session and turn IDs are printed for reference, but ordinary use never requires typing them, except `--session` to reconnect.

## Milestone 5: product host setup, updates and diagnostics (P8)

Everything here runs against a test root (`--root DIR`) and supports `--dry-run`, so it can be verified without a real host. The root must be an existing absolute directory outside the thesis paths (`/etc/tbound`, `/var/lib/tbound`, `/run/tbound`, `/run/tbound-pi`). Roots and destination parents containing symlinks are refused. Destination changes use pinned directory handles so a parent symlink cannot redirect a privileged write or removal. In-place writes and metadata changes reject multiply-linked regular files and validate and mutate the same open file. Real qualification needs the product host and is pending.

### Release trust

- Release artifacts get a signed manifest, `tbound-release-manifest/v1`. It lists each artifact's name, size and SHA-256, with an Ed25519 signature over the canonical manifest bytes.
- The publisher's public key is distributed separately from the artifacts. Users compare its fingerprint, the first 16 hex characters of the SHA-256 of the raw key, against the README and the release notes.
- `tbound release verify --manifest F --signature F --public-key F [--artifact F...]` refuses a bad signature, an unknown artifact, or a size or digest mismatch. A same-origin checksum file alone is never accepted as authentication.
- `install/install.sh` verifies the manifest signature before extracting anything, when it is given a public key.
- A first install trusts a key that has not been seen before. The documentation states that limit.
- The first signed install pins the public key it verified in the install prefix (`.tbound-publisher.pub`, 0644, a copy made before verification). Every later install or update into that prefix is verified against the pinned key and is refused without a valid signature: it never falls back to `SHA256SUMS`, and a different `--public-key` is refused until the prefix is uninstalled. A prefix installed without a key keeps the checksum-only behavior for a run that is not root, and the installer says so; its first signed update pins a key.
- A run as root requires publisher authentication: `--public-key`, or the key the prefix pins. Without either, install.sh refuses with a message that says a checksum file is not authentication, before it creates a directory or reads the artifact. Decision: root gains privilege from what it installs, and a same-origin checksum file is replaced together with the artifact, so only a run that is not root, which gains no privilege, may install from a checksum alone, into a prefix its user can already write.
- The archive never supplies the pin: install.sh removes any `.tbound-publisher.pub` the archive carries and writes the pin only from the key it verified. A run as root takes the marker and the pin as trust anchors, so it refuses unless the prefix (or its nearest existing ancestor) and every directory above it are root-owned and not writable by group or others, and it refuses a marker or pin that is a symlink, not root-owned, or writable by group or others. A root uninstall applies the same checks before it accepts the prefix and again immediately before it removes it, refuses a prefix whose canonical path changed meanwhile, and removes it with `rm -rf --one-file-system` where `rm` supports it.
- install.sh always creates the prefix marker itself, as a new regular file without following links, and discards any `.tbound-install` the archive carries (an archive link named like that would otherwise be written through, as root). A run as root also never installs or removes the per-user shell completions or writes the per-user config, because `HOME` and `XDG_*` directories are user-controlled and a root run would follow a symlinked leaf or directory. It skips them and prints what the user runs unprivileged. A root run extracts under `umask 077` with `tar --no-same-owner --no-same-permissions` into a private directory it created, so no owner or mode in the archive (not even on its `.` entry) reaches the stage or the prefix, and it creates the marker and the pin with their final mode (0644) instead of changing the mode by pathname afterwards. A root run also downloads, verifies and extracts only inside private directories it creates below the prefix's parent, which it has checked like the prefix, and never under `TMPDIR`, because whoever owns a directory above `TMPDIR` could rename a stage there and put another archive at its path after the signature was verified; nothing it verified is reopened from outside those directories. A run that is not root keeps `TMPDIR`.

### Registration tooling (offline administrator)

- `tbound product registration keygen --out DIR` writes an Ed25519 key pair. The private key is 0600, and the command refuses an existing file. The private key stays offline and never enters the service host.
- `tbound product registration measure --project-dir DIR --gate-policy FILE` runs on the product host as the service user and prints `root=` and `policy=`: the project root's directory identity digest and the registered policy digest, computed as the service computes them. It refuses a directory the service would refuse and a policy that does not parse. It is the only registration command that runs on the host; [the first-turn guide](guides/product-first-turn.md) shows the order.
- `tbound product registration init --out FILE` writes an unsigned registration template from flags: projects, scope, provider profiles, limits, principals, `--git-digest`, runtime, image and extension digests. It computes `host_identity_digest` from this build's prompts, registered provider table entries and supervisor-loop worker kind.
- `tbound product registration sign --registration FILE --key PRIVATE --out FILE` writes the signed registration and its `.sig` with the existing `SignRegistration`. It refuses duplicate JSON keys and trailing data before signing.
- `tbound product registration verify --registration FILE --signature FILE --public-key FILE` prints the digest and a summary.
- Changing a project, scope, policy or limit means signing a new registration. No per-task signing exists.

### Host setup (administrator, on the product host)

`tbound product setup --config FILE [--root DIR] [--dry-run]` makes the host match the plan and is idempotent:

1. It checks prerequisites with the existing governed-host and doctor checks: kernel and Landlock ABI, cgroup v2 delegation, rootless Podman and crun, an ext4 managed root, and systemd. An unsupported host fails before any change, and the failure names the qualified VM option.
2. It creates the `tbound-product` system user and group, or verifies that they exist with the same IDs.
3. It creates the directories with exact owners and modes:

   | Path | Owner | Mode |
   | --- | --- | --- |
   | `/etc/tbound-product` | root | 0755 |
   | `/etc/tbound-product/credentials` | service | 0700 |
   | `/var/lib/tbound-product` (catalog, turns, projects) | service | 0700 (the audit journal requires an exact 0700 parent) |
   | `/etc/tbound-product/podman` (`containers.conf`, `storage.conf`) | root | 0755 |
   | `/var/lib/tbound-product/podman-home` and `/var/lib/tbound-product/podman-storage/graphroot` | service | 0700 |
   | runtime directory | from the unit | from the unit |
   | `tmp`, `cache` and `containers` under the runtime directory | service | 0700, not created by setup; startup creates them for a policy that allows Bash, before qualification, and refuses any other owner or mode |

4. It installs the registration, signature and public key as root-owned 0644 files, and two Podman configuration files as root-owned 0644 files with fixed contents. `containers.conf` sets the `cgroupfs` cgroup manager, file events and `crun`. `storage.conf` sets the `overlay` driver, explicit graph and rootless storage at `/var/lib/tbound-product/podman-storage/graphroot`, the runroot `/run/tbound-product/containers`, and `ignore_chown_errors`. Setup never removes an existing store. It writes the service config and the `tbound-product.service` unit: `User=tbound-product`, `RuntimeDirectory=tbound-product`, `Delegate=yes` with `DelegateSubgroup=runner`, one `Environment=` line with the product Podman variables (`CONTAINERS_CONF`, `CONTAINERS_STORAGE_CONF`, `HOME`, `LANG`, `LC_ALL`, `PATH`, `TMPDIR`, `XDG_CACHE_HOME` and `XDG_RUNTIME_DIR`), `UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS`, hardening matching the research unit where it applies, and `[Install] WantedBy=multi-user.target` (the service opens its own socket, so there is no socket unit). The unit validator pins that section exactly. Then it runs `daemon-reload` and `systemctl enable` for the unit, so it starts at boot. Setup does not start the service. A rerun on an enabled unit changes nothing, `--dry-run` plans the enable, and the doctor reports a unit that is not enabled.
5. It never creates or replaces the owner's signing key, and it never touches a `/etc/tbound`, `/var/lib/tbound` or `/run/tbound` path.

The setup config must name the host profile inputs: `gate_policy_paths` with a file below `/etc/tbound-product` for every project, `git_path` below `/usr/local/lib/tbound-product` with its `git_digest`, and `xattr_calibration_path` below `/etc/tbound-product`. Setup refuses a config without them or with a thesis path, and records them in the service config. It does not install those files, and it refuses to run until each passes the static checks (see [Host inputs](#host-inputs)).

Running it again changes nothing and reports "unchanged". An existing file with different content is refused unless `--replace` is given.

- A host input path (`git_path`, `xattr_calibration_path` and its `.sig`, each `gate_policy_paths` entry) is refused when it equals a path that setup, update, rollback or uninstall writes (the installed files, the binary, last-known-good, the staged binary, the rollback record, the catalog, the socket, the boot link, a directory setup creates) or lies inside the credentials, state or runtime tree. Two projects may share one policy file.
- A host input path is also refused when a directory in it is a symlink, walked without following one from the root, so an alias such as `/etc/tbound-product/alias -> /etc/tbound-product` cannot put an input behind a written path and past the comparison above. A symlink as the last component is refused by the readers.
- `--replace` with a registration of another digest first reads the catalog under the installed registration, with the service stopped, and refuses while any turn is unresolved or has an unknown outcome, naming each turn. The service refuses to start on a changed digest in that case, and its restart reconciliation runs only after that check, so nothing could settle those turns afterwards. `--dry-run` reports the same refusal, and the read appends nothing. A replacement that keeps the digest is not checked. A catalog that cannot be opened (the service holds it) also refuses, so stop the service first. A turn with an unknown outcome stays blocked, because no start settles it.
- `self_test`, when present, needs `client_uid`: a registered client UID, never 0. Setup refuses a missing UID, or one that is not an allowed principal of the verified registration, before it writes anything. Without `self_test` no self-test runs and update says so.
- `release_public_key` (optional) names the publisher's Ed25519 public key file (base64, the format of `tbound release keygen`). Setup refuses a file that is not such a key, installs it root-owned (0644) at `/etc/tbound-product/release-public-key.pub`, and keeps it out of the installed record. This is the pin that `product update` authenticates against, and `product doctor` shows its fingerprint (compare it with the one the publisher distributes separately). A setup without it leaves no pin, doctor warns, and every update is refused. Rotating the key needs `setup --replace`.
- `--binary` is read once, and its installation format is scanned from those same bytes before anything is planned or written. Setup refuses a binary whose format differs from the one this setup writes (`installation_format`), also with `--dry-run`, because it would pair a binary with a unit and a record it cannot read. A test root skips the check, as for update.
- `--binary` is installed as root and kept as the last-known-good that a rollback runs, so setup admits it only in two cases. The binary is the executable that runs this setup (compared byte for byte with `/proc/self/exe`, which is also what setup installs without `--binary`), or it is listed by a manifest signed under the publisher key: `--manifest` and `--signature` as for update, verified under the `release_public_key` of the config, or under the key already pinned when the config names none, against the bytes setup reads once and then installs. Any other binary is refused before anything is planned, also with `--dry-run`: with a key because the manifest or signature is missing, wrong or does not list it, and without any key with the way out (set `release_public_key` and pass `--manifest` and `--signature`, or run setup from the binary to install). A test root applies the same rule. The bootstrap without `--binary` trusts the running executable (`/proc/self/exe`) and authenticates nothing, so the first `tbound` binary must itself have come from an authenticated install: a root `install.sh` run under the publisher key, or a build from source the administrator trusts.

### Offline command image and public command bundle

`tbound product image template-digest --template FILE` decodes a product D09 template and prints its canonical digest. It grants no host authority. The owner fills the measured HostProfile with that digest and signs it offline with the existing `tbound-profile sign` tool. Its raw-key interface requires owner-only temporary conversion of the product base64 key/signature wrappers; no private key belongs on the product host.

The optional setup field `command_bundle` names a protected directory containing `host-profile.json`, `host-profile.json.sig`, `d09-cell.json`, `seccomp.json`, and `cosign.pub`. Setup checks bounded public bytes against the **proposed** signed registration and the unit/configs it will install, then installs them root-owned at `/etc/tbound-product/podman`. The installed setup record omits the source directory. This check needs neither an image store nor a running service. Without a bundle, setup accepts only policies that deny Bash and have no retained command artifacts or obligations. A replacement that keeps command history still needs a bundle bound to its proposed registration. Doctor checks installed static validity; it never qualifies a command cell.
The unit uses `BindReadOnlyPaths=` for `/usr/bin/podman`, `/usr/bin/crun`, `/usr/local/bin/cosign` and `/etc/tbound-product/podman` (including seccomp, the cosign key and OCI layout) inside its mount namespace; host setup/provisioning stays writable, and `-` prefixes permit absent command inputs on Bash-deny installs while the Bash loader still requires them.

Build the release twice from an explicit frozen product commit with the P3 recipe in `infra/guest/PROVISIONING-SIGNING.md`, using the unchanged `packaging/governed/cell_image.py` and `sign_cell_image.py`. Ship the original deterministic OCI archive and the signed local layout. An OCI re-export can rewrite the manifest: measure the shipped archive before import and sign that exact manifest offline. The archive SHA-256 is transfer integrity, not a replacement image pin.

```sh
tbound product image template-digest --template d09-cell.json
sudo tbound product image provision --archive /etc/tbound-product/incoming/cell.tar \
  --signed-layout /etc/tbound-product/incoming/signed-layout --dry-run
sudo tbound product image provision --archive /etc/tbound-product/incoming/cell.tar \
  --signed-layout /etc/tbound-product/incoming/signed-layout
```

Provision requires a stopped service, no unresolved turn, protected staging inputs, the installed signed registration and matching command artifacts. Before load it verifies every archive blob and the signed manifest, then verifies the offline cosign claim under the pinned public key. Import runs as `tbound-product` under NoNewPrivileges, the execution unit's hardening, fixed environment/configs and single own-ID mapping, with `PrivateNetwork=yes`. Each Podman operation runs in a fresh identical transient context. It loads with `podman load --input`, tags by the measured config ID, and checks the image again. It never pulls, allocates sub-IDs, resets a store, starts the service, or changes a pin.

The required equality is `registration.ImageDigest == template.ExpectedImageDigest == signed OCI manifest digest == imported Digest/RepoDigest`. A rewritten manifest, missing local image, mismatched RepoDigest, failed signature or load aborts provisioning. A protected store record binds the account, mapping and configs. A nonempty store without that record, or with an incompatible record, is refused: stop the service, preserve retained evidence, and have the operator explicitly relocate the incompatible store and recreate the empty setup-owned directories before provisioning again. No automatic deletion is supported. Dry-run checks static inputs and reports the import plan; it does not run cosign or Podman and establishes no imported-image validity.

Bootstrap order is: deny-only setup/account creation; host measurement; owner signs offline; transfer the public artifacts; `setup --replace` with `command_bundle` and the Bash registration/policy; offline provision; then start and qualify. Image replacement uses the same candidate-registration validation and stopped/unresolved-turn fence. Setup never requires an already loaded candidate image. The [Bash guide](guides/product-bash.md) gives the operator sequence.

Every newly signed host profile requires `image provision` again, even when the image bytes are unchanged. Provision retains its public verification inputs under `/etc/tbound-product/podman/hosts/<profile-digest>/`. Setup prints a reminder when replacing the profile; doctor reports a missing retained snapshot as `FAIL`. Without it, predecessor replay refuses. Keep old profiles, templates, public keys, signed layouts, and turn journals while retained turns reference them. Signing-key rotation across retained history is not supported.

### Installed Bash unit and measurements

Installation format 3 is required. Format 2 has no automatic migration. `setup --replace` from a format-3 binary replaces the unit and setup record after the stopped-service and unresolved-turn checks. Setup preserves the existing Podman store. A binary-only update or rollback cannot cross installation formats.

The unit is a closed contract, installed verbatim without drop-ins. Its fixed `[Service]` directives are:

```ini
User=tbound-product
Group=tbound-product
ExecStart=/usr/local/lib/tbound-product/tbound product-serve --config /etc/tbound-product/service.json
KillMode=control-group
Restart=on-failure
RestartSec=5
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
Delegate=yes
DelegateSubgroup=runner
RuntimeDirectory=tbound-product
RuntimeDirectoryMode=0750
ReadWritePaths=/run/tbound-product /var/lib/tbound-product
BindReadOnlyPaths=-/usr/bin/podman -/usr/bin/crun -/usr/local/bin/cosign -/etc/tbound-product/podman
UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS
```

Its single `Environment=` directive contains exactly these space-separated assignments. Every product Podman and cosign child gets a freshly built environment with the same values, including cleanup and probes:

```text
CONTAINERS_CONF=/etc/tbound-product/podman/containers.conf
CONTAINERS_STORAGE_CONF=/etc/tbound-product/podman/storage.conf
HOME=/var/lib/tbound-product/podman-home
LANG=C
LC_ALL=C
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
TMPDIR=/run/tbound-product/tmp
XDG_CACHE_HOME=/run/tbound-product/cache
XDG_RUNTIME_DIR=/run/tbound-product
```

No child inherits `USER`, `LOGNAME`, or the caller's environment. `[Install]` is exactly `WantedBy=multi-user.target`. Setup enables the unit but does not start it. The loader checks the installed and effective unit, including overrides and environment files. Manager-level `DefaultEnvironment=` is not checked; it can affect the daemon, while command children retain their fresh environments.

`BindReadOnlyPaths=` gives the loader a read-only mount at each pinned path inside the service. The host stays writable for setup and provision. The `-` prefix allows a Bash-deny install to start when command inputs are absent. It does not waive the Bash loader's checks. The shared `requireReadOnlyMount` helper ignores a mount at `/` because its descendant prefix becomes `//`; the product uses per-path binds.

Pinned host tools must be root-owned regular files, not symlinks, with protected ancestors and no group or other write permission. A symlink at `/usr/local/bin/cosign` refuses even when its target has the correct digest. Use a regular copy of the measured bytes before signing the pins.

The loader verifies the entrypoint, Bash, and Node bytes in the merged signed image. It follows ancestor symlinks inside that image, so `/bin -> usr/bin` can lead to `/bin/bash`. The final component must be a regular executable file, not a link, and must not be writable by group or others. Layer writes through linked ancestors, and hardlinks to the files being verified, refuse the image. The thesis image reader is unchanged.

The template's Landlock read-only list is `/usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev`; the read-write list is `/work,/tmp`. Their ruleset digest is `sha256:6c7bcda1461c17b7847cd124a0bba03ba4d7779bb3b07c2bc83805e7859a06af`. A shorter read-only list breaks every cell when the entrypoint opens `/proc/net/route` after Landlock. The `/var/lib/tbound/pi-worker` entry is an in-image pin, not access to a thesis host directory.

A command-only HostProfile uses the declared sentinel `sha256("unused by the supervisor-loop worker")`, `sha256:a0b7daad8b52356c44c8cc98e5cca6f1242b2012af1a59a6b3e09f5c55c977cf`, for `PiRuntimeBundleDigest` and other unused Pi-only measurements. The registration uses it for `runtime_digest` and `extension_digest`. It authorizes nothing: the product command launcher refuses Pi, and receipts keep their conservative `unverified_inputs`. Node, Bash, entrypoint, tool, and image pins remain real measurements. `SourceProvenanceDigest` is the frozen source's archive-tree digest printed by `tbound-run source-digest --repo REPO --commit COMMIT`, not the Git commit ID or an archive-file checksum. The loader verifies the signed binding and rejects known placeholders; it does not independently recompute source provenance.

### Host inputs

The administrator places the host profile inputs (see [Host profile](#host-profile)) before `tbound product setup`. Three commands make and check them. The static checks are shared: `product-serve` runs them at start with the service's owner set, and `setup` and `doctor` run the same code with root and the service account as the allowed owners (root only on a first install, before the account exists; a test root also admits the test process).

**Static checks** (setup, doctor and service start). Gate policy of each project: a protected regular file (no symlink, not group or world writable, owner in the allowed set, bounded) that parses, whose digest equals the signed registration's `policy_digest`, and whose Bash allowance has matching static command inputs. Static validity does not qualify the runner. Git: the production pin (digest and mode, trusted ancestors), an allowed owner, and a `git_digest` equal to the signed registration's. Host identity (`input host identity`): this build's compiled identity equals the registration's `host_identity_digest`, so a binary update that changes a prompt or provider constant needs a new signed registration. Calibration and its `.sig`: both protected, the signature verifies under the registration public key over the canonical bytes, and the source profile is managed-rw. **Live checks** stay with the service and are not run by setup: the scanner identity of every scanned root against the calibration (needs the service UID, zero capabilities, NoNewPrivs and the initial user namespace) at start and again before each publication, unchanged.

- `tbound product setup` runs the static checks on every input before it writes anything (accounts, directories, unit, config), with the registration public key it is about to install. A bad input refuses the run with exit 2 and names the input and the reason. `--dry-run` reports the same refusal and writes nothing. Setup therefore needs the policy files, Git and the signed calibration in place first.
- `tbound product doctor` reports one advisory line per input from the installed setup record: each project's policy, `input git`, `input host identity`, `input calibration`, `input calibration signature` and `input registration public key`, with path, owner, mode and a digest prefix, or the reason for the refusal. It prints no secret.
- `tbound product calibration measure --source DIR --probe-corpus-digest sha256:HEX --out FILE [--config FILE] [--force]` runs on the host as the service user (not root; the measurement refuses a privileged scanner, so run it with no new privileges and no capabilities). `DIR` is the held probe corpus the operator prepared, and the digest is the one the operator reviewed. The access profile is fixed to managed writable roots. The command also measures the state root and every project root of the service config (default `/etc/tbound-product/service.json`) and refuses, writing nothing, when any scanner identity differs from the corpus measurement. It writes the unsigned measurement to `FILE` and does not replace an existing file without `--force`.
- `tbound product calibration sign --key PRIVATE --public-key PUBLIC --in UNSIGNED --out SIGNED` runs offline with the registration key pair. Product keys are base64 files, not the raw research keys. The private key must be a regular file that group and others cannot read. The command refuses a private key that does not match the public key, a calibration for another access profile, and an existing output. It publishes `SIGNED` and then `SIGNED.sig` through synced temporary files and an atomic no-replace rename, so a crash leaves a pair the verifier refuses, never one it accepts wrongly. It never prints key material.

The probe install, census and cleanup stay an operator procedure: place the probe attributes on a throwaway corpus below the managed root, census it, run `calibration measure`, then remove the corpus and prove the census returns to its baseline. The research runbook step is R1-5 in `infra/guest/PROVISIONING-SIGNING.md`; the product differs only in the commands and the key format.

**Authority.** The signed calibration is root-attested. The trusted administrator signs that, for this filesystem type, mount options and scanner identity, no hidden `trusted.*` attribute exists on the managed roots. Enumeration by the unprivileged scanner cannot prove that: a non-root scanner cannot list `trusted.*` names at all, so the scan's silence is not evidence of absence. The measurement records the observed probe corpus and scanner identity. The signature, made with the offline registration key, adds the administrator's claim about attributes the scanner cannot see.

### Update, rollback and uninstall

- `tbound product update --binary NEW --manifest FILE --signature FILE [--root DIR] [--dry-run]` first authenticates the candidate, then stages it, stops the service, and opens the catalog. The authentication exists because the self-test runs the installed binary as root, so a candidate that was not authenticated would run arbitrary code as root, and a forged format marker cannot prevent that. `--manifest` and `--signature` are the files `tbound release manifest` and `tbound release sign` produce (the same names `tbound release verify` takes; `install.sh` finds them next to the artifacts), and the manifest must list the candidate's file name with its size and SHA-256, so a publisher releases the product binary as a manifest artifact. In order, and before any `systemctl` call: the pinned publisher key is read through a no-follow descriptor (root-owned, 0644); the manifest and signature are read through bounded no-follow descriptors and the signature is verified under the pinned key; the candidate is copied through a no-follow descriptor to the staged file next to the binary, which only root can write; the staged copy's size and SHA-256 are checked against the manifest entry; only then are its installation format read and the rest of the flow run. Only that verified staged copy is ever installed or executed, so replacing the original file after the check changes nothing. Before the key is read, `/usr/local/lib/tbound-product`, `/etc/tbound-product` and every directory above them are walked without following a symlink and must not be writable by group or others and, on a live host, must be owned by root, because a user who can write there could replace the staged file after it was checked. The copy reads at most the manifest's declared size plus one byte, so a source that keeps growing is cut off and then refused by the size comparison. The swap renames the staged file only when it is still the inode that was authenticated. Rollback applies the same directory rule to the binary directory. A missing pinned key, an unreadable or unsigned manifest, a signature by another key, a binary the manifest does not list, or a size or digest mismatch is refused (exit 2) with nothing staged, stopped or changed. `--dry-run` runs the same checks on the file in place, hashing at most the manifest's declared size plus one byte, and stages, stops and executes nothing. A signed manifest authenticates the publisher's release, not that the binary is correct for this host: the self-test and the restoration still guard that. Update refuses while any turn is unresolved, other than a pending review, which becomes `discarded` with reason `restart`. It keeps the current binary as last-known-good, swaps atomically, starts the service, and runs the self-test. If the self-test fails, it rolls back. Retrying when the requested binary is already installed still starts the service and runs the self-test, preserving the existing last-known-good for recovery.
- `tbound product rollback` restores last-known-good. It takes no candidate and needs no signature: the only binary it ever runs is last-known-good, a root-owned file that was authenticated when it was installed (by an authenticated update, or as the binary the administrator gave to setup). It exchanges the installed binary and last-known-good with one `RENAME_EXCHANGE`, so both exist whichever way an interruption falls. The binary it replaces becomes the new last-known-good, so a second rollback returns to it. Before the exchange it records both digests in `/usr/local/lib/tbound-product/tbound.rollback` (0600, made durable). A retry in a fresh process reads the record. An installed binary that already is the target means the exchange happened, so the retry only starts the service and runs the self-test, and never exchanges back. The old pair still in place means it carries on. Any other pair means the record is stale, and it is dropped. The record is removed once the restored binary started and passed, or was restored. A last-known-good equal to the installed binary is refused, and so is a filesystem without `RENAME_EXCHANGE`, before any change.
- A failed start or self-test restores last-known-good over the installed binary. The restoration is recorded first in the same file (`restoring`, with the digest being put back) and cleared only once the service started again. When the record cannot be written, nothing else happens: the service is not stopped again, the binary is not replaced, and the error says to run `tbound product rollback`. An interruption leaves both binaries equal and the service stopped, so the next update or rollback finishes the restoration first: it puts last-known-good back if the copy had not completed, starts the service, runs the self-test and clears the record, and only then does its own work (a rollback then finds nothing to roll back to and says so). A dry run refuses while such a record exists.
- The installed setup record carries `installation_format` (3: the unit has the command cell's `DelegateSubgroup` and `Environment`, and the Podman layout exists; 2: the unit has `[Install]` and the self-test has `client_uid`; a record without the field is format 1, as at c6fbd5f). A format 2 installation is not updated in place. `tbound product setup --replace` with a format 3 binary rewrites its unit and record, creates the Podman layout, and keeps any existing Podman store. No automatic migration runs. Update reads the new binary's format, and rollback the last-known-good binary's, from a marker string compiled into every tbound binary (`tbound-product-installation-format=N;`), found by scanning the file's bytes through the one no-follow descriptor already opened for it, bounded by the binary size limit. A file without a well-formed marker is format 1. No candidate is ever executed to learn its format, and a dry run executes nothing either. Update and rollback refuse a different format before stopping the service or staging anything, and setup refuses a `--binary` of another format before it plans anything, with the way out: `tbound product setup --replace` with a binary of the matching format. The release build holds the marker exactly once (a test builds it and counts), and a test places the marker at every offset around the scan's 1 MiB chunk boundary. A forged marker can only make this check pass. The post-swap self-test and the restoration of last-known-good still guard a binary that does not work, as they guard any other bad binary. A test root skips the check, because its fixture binaries carry no marker.
- Update and rollback check the installed self-test client UID against the installed registration before they stop the service or stage anything, and refuse as setup does.
- Binary and catalog sources are opened through pinned parents without following leaf symlinks. The opened file must be regular and single-linked; managed sources must have their expected owner on a live host, binaries must be 0755, and catalog backups accept 0600 or 0640 sources. Digests and copies use the same descriptor. A retry validates any retained last-known-good before starting the service, and recovery validates it again before copying.
- Catalog schema changes need a version check and a backup before the swap. A newer schema is never read by an older binary.
- `tbound product uninstall [--root DIR] [--purge]` stops and disables the service and removes the unit, binary and runtime access. On a live host it verifies the service is inactive before removing files. Stop or disable failures abort removal unless systemd confirms the unit is absent and inactive. It reports unresolved turns. It keeps `/etc/tbound-product` and `/var/lib/tbound-product` unless `--purge` is given, so the Podman configuration and store stay with them, and `--purge` asks for confirmation unless `--yes` is also given. A purge refuses an unreadable or locked catalog before removing files. Deletion errors are returned, and only successful deletions are reported as removed.
- Live catalog checks run the currently executing administrative binary as the service account, so purge also works after an ordinary uninstall removed the installed binary. The internal `product turns` command permits non-root callers while retaining the catalog's ownership and permission checks.

### Diagnostics

- `tbound product doctor` is advisory. It reports prerequisites, installation owners and modes (including the Podman directories), the content of the two Podman configuration files against the bytes setup renders, unit state, socket reachability, registration verification, one line per host input (see [Host inputs](#host-inputs)), and credential status (never the key).
- `tbound product doctor --self-test` runs an explicit runtime self-test turn on a synthetic project with the configured worker. The service authorizes a caller by its peer UID and root is not a registered principal, so a root caller runs the turn in a child process (`tbound product self-test`, from the installed binary, request on stdin, minimal environment) that drops to `self_test.client_uid` with that account's primary and supplementary groups. The privileged process never connects to the service, and a UID without an account is refused. A non-root caller runs the turn as itself. With `--dry-run` it reports the planned self-test without submitting a turn or establishing readiness. Only a passed self-test, never the advisory checks, can report governed readiness.

### Open acceptance items and decisions (P8)

Not yet tested, and so not claimed:

- a clean qualified host, and a Windows-to-VM run of the whole sequence (setup, start, self-test, update, rollback, uninstall);
- a reboot with the unit enabled, and client reconnect after it;
- an update or backup recovery across a catalog schema change (the version check and the backup exist, but no test crosses a schema change);
- `RENAME_EXCHANGE` and the boot link on the qualified host's filesystem (the tests ran on ext4 under WSL).

Decision: a registration has no expiry field in v1. Changing principals, projects or limits means signing a new registration and replacing the old one (`setup --replace`, fenced by unsettled turns). An expiry would need a trusted clock and a schema change, and this milestone does not take either on.

## Milestone 6: live web chat (P9)

The web chat stays an optional, separate Go module (`extensions/webchat`). It never imports the supervisor. It speaks `tbound-product-protocol/v1` over the product socket, with its own copy of the wire types. A parity test checks that copy against JSON fixtures the supervisor generates.

### Authority

- The gateway runs as the user and connects to `/run/tbound-product/product.sock`, so the service sees the user's UID as the principal. The browser gains no authority beyond that user's. The gateway holds no policy, lease, effect or publication authority. It forwards requests and renders projections.
- Deviation from the plan: the plan describes supervisor-issued, scoped browser delegations. In this milestone the gateway's pairing secret scopes the browser instead, and the supervisor sees only the user principal. Supervisor-issued delegations with separate read, submit, cancel and resolve rights remain open work.

### Pairing and sessions

- `tbound-webchat --live --socket PATH [--listen 127.0.0.1:8791]` binds only a literal loopback address.
- At start it prints a one-time pairing URL, `http://127.0.0.1:8791/pair#<token>`, to the terminal only. The token is 32 random bytes and lives at most 10 minutes. It is never written to disk or logs.
- The pairing page posts the token to `POST /api/v1/pair`. The gateway answers with a `tbound_session` cookie (random, HttpOnly, SameSite=Strict, Path=/, 12-hour lifetime) and a CSRF token. The pairing token is spent on first use.
- Every API request needs the session cookie.
- Every mutation also needs the `X-TBound-CSRF` header to equal the session's CSRF token.
- `Host` must equal the bound `127.0.0.1:PORT` or `localhost:PORT`.
- When an `Origin` header is present, it must equal `http://` plus that host.
- There is no CORS. Request bodies are at most 64 KiB of strict JSON.

### Routes

| Method and path | Maps to |
| --- | --- |
| `POST /api/v1/pair` | spends the pairing token; sets the session cookie and returns the CSRF token |
| `GET /api/v1/pair` | returns the paired session's CSRF token again, so a reloaded page recovers it |
| `GET /api/v1/sessions` | `list_sessions` |
| `POST /api/v1/sessions` | `create_session` |
| `GET /api/v1/sessions/{id}` | `get_session`, rendered into the existing snapshot shape with `source: "live"` |
| `GET /api/v1/sessions/{id}/events?cursor=N&wait=MS` | `observe` (long poll, wait at most 25 s) |
| `POST /api/v1/sessions/{id}/turns` | `submit_turn` (the request key comes from the client; the gateway never invents a retry) |
| `GET /api/v1/sessions/{id}/turns/{turn}/candidate` | `get_candidate` |
| `POST /api/v1/sessions/{id}/turns/{turn}/cancel` | `cancel_turn` |
| `POST /api/v1/sessions/{id}/turns/{turn}/resolve` | `resolve_turn` with the candidate digest the client displayed |

- Errors keep the service's stable codes.
- The fixture, replay and receipt modes stay available, labelled as demonstrations.

### Browser UI

The existing React app gains a live mode:

- a session picker and creator showing project, provider and mode;
- a composer that submits with a fresh request key per prompt;
- a cancel button;
- the event stream, with assistant text, tool calls, governance decisions and hints, and outcomes;
- a diff review with Publish and Discard bound to the displayed candidate digest;
- remaining budget, recovery and blocked states;
- reconnect from the cursor after reload.

Model-authored text is rendered through the existing sanitizing markdown path. Governance status stays structurally and visually separate from model output. Keyboard access and focus behaviour follow the existing components.

Implemented as follows (detail in `extensions/webchat/docs/web-chat-contract.md` section 9.7):

- The app detects live mode from `GET /api/v1/contract` (`"mode": "live"`). A `401` shows pairing instructions. Any other gateway keeps the read-only demonstration path, labelled as a demonstration.
- The page recovers the CSRF token from `GET /api/v1/pair` and sends it on every `POST`.
- A request key belongs to one user action. A retry sends the same body under the same key. An unknown outcome is shown, blocks a new prompt, and offers a retry under the same key.
- Unresolved intents are saved in per-tab sessionStorage before sending, with their operation, exact body, request key and session. Reload and session changes retain them; only a successful answer settles an unknown outcome. Storage is validated on load and bounded to 16 intents and 1 MiB. No pairing secret, cookie or CSRF token is stored there. Requests are refused locally if their recovery record cannot be saved.
- `expected_revision` is the revision of the last session view the page read, and the view is read again after every lifecycle event.
- The event stream is a long poll that starts from the snapshot's cursor. The cursor is stored per session in the tab, and a gap refetches the snapshot.
- Publish and Discard send the digest of the candidate that is displayed.
- A complete candidate preview must fit the protocol's 1 MiB encoded response frame, including its envelope and JSON escaping. The browser uses the same byte bound instead of a separate path-count limit. The service returns `CANDIDATE_TOO_LARGE` (HTTP 413) above it and never omits changed paths from a candidate preview.

### Windows access

Use an SSH tunnel to the product host: `ssh -L 8791:127.0.0.1:8791 user@host`, with pinned host keys. Never bind a non-loopback address.
