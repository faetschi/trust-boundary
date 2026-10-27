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
| `projects[]` | `project_id`, `root_identity_digest` (exact managed-root identity), `policy_digest` (installed gate profile), `bash_output_paths` (exact research-style output scope; empty disables Bash), `publication_modes` (subset of `review`, `auto`). |
| `provider_profiles` | Provider profile IDs a session may select. |
| `limits.turn` | Per-turn ceiling: `host_calls`, `tokens`, `duration_seconds`. |
| `limits.session` | Upper bound a session may request, same fields. |
| `limits.max_turns_per_session` | Hard cap on turns. |
| `limits.review_timeout_seconds` | Default review expiry. P4 enforces it; P2 records it. |
| `git_digest` | Digest of the approved private Git executable. The service config must match it. |
| `host_identity_digest` | Compiled prompts, named provider table entries in ID order, and supervisor-loop worker kind, under `tbound-product-host-identity/v1`. `registration init` computes it. |
| `runtime_digest`, `image_digest`, `extension_digest` | Administrator declarations, unmeasured by the supervisor-loop worker. Receipts list these in `unverified_inputs`. |

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

`tbound-product-turn-receipt/v1` (`productsession.TurnReceipt`), strict JSON, at most 64 KiB, written by `cmd/tbound` and stored in `product_turn_terminal` or `product_turn_awaiting_review`. The catalog decodes it strictly and refuses to settle unless its identity, workflow link, imported prefix, predecessor, baselines, outcome, settlement and usage match the turn and the record. Settlement is `supervisor-loop-closed` for worker kind `supervisor-loop` and `stopped` for worker kind `pi`; no other pairing is accepted. A published receipt names its publication transaction:

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
- Linked and running: the supervisor loop checks for cancellation before every provider exchange. It never interrupts a call in flight. At the next step boundary it stops, the conversation closes, and the turn settles `cancelled` with usage charged. Nothing is published, and the project must still equal the admitted baseline.
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
- The service gives the turn driver a command runner factory. A turn whose policy allows Bash on a service without one is abandoned before any work, with no budget consumed. Production will fill the factory with the D09 cell runner of a qualified host. No such runner exists yet, so the production host profile refuses at startup any installed gate policy that allows Bash (see [Host profile](#host-profile)). The development runner is never wired into production.
- The receipt commits the turn's command journal by head and record count, next to the provider and Store journals, with `command_journal_head` and `command_journal_count`. A turn that ran no Bash omits both. The next turn checks them before importing history.
- The model sees what a command printed. A product turn's Bash summary carries `stdout`, `stderr`, `stdout_truncated` and `stderr_truncated` next to the exit code, containment status and runner profile. Each stream is valid UTF-8, with every invalid byte run replaced by U+FFFD, and at most 16 KiB (`productCommandOutputBytes`). A longer stream keeps its tail, because the end of a build or test run holds the failure, and its truncated flag is true. The cut never splits a character. The four members are always present, so an empty stream is an empty string. The research summary is unchanged and carries no output.
- A failed command (a non-zero or unobserved exit) keeps the `command_failed` status and hint, and its output is shown with it.
- The output is passed through as model context. It is the workspace command's own output and is never filtered, so a command can print any file it can read in its workspace. The supervisor adds no credential or host path to it. The service's `tool_result` observation still carries no tool output.
- The output is durable and replay-verified. The Store keeps each command's full stdout and stderr (up to 1 MiB per stream) in the command's effect result. Replay rebuilds the summary the model saw from that retained result with the turn's pinned bound, and requires it to equal the recorded provider result byte for byte. A forged output, or a replay under another bound, fails the predecessor check. The bound is part of the evidence format, so changing it makes earlier Bash turns unverifiable as predecessors.
- Reopening a turn replays its retained Bash effects against the retained command settlements through a read-only opener. For D09 the opener is the research recovery verifier over the command journal. Without an opener a retained Store that holds a Bash effect is refused. Restart reconciliation of a linked turn reopens the Store the same way, and a turn it cannot verify ends `unknown`.

### Import

- `tbound-project-bundle/v1` is a bounded manifest plus content stream that the user's own client builds from the original project. The service account never reads the original repository.
- The bundle covers regular files and directories only. It records each path's type, mode bits (executable or not), size and SHA-256.
- It excludes `.git` administration, the registration's protected paths, and operator-configured private paths. Those are listed as excluded.
- Symlinks, hard links, devices, sockets, FIFOs, special metadata, files over the size limit, and totals over the limit are refused with the path and reason.
- The client prints the included and excluded sets before sending.
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

The first supported language is Go, with a pinned toolchain image and a pre-filled module cache. Tests run with the command cell's network disabled. Building and pinning the image needs the product guest and is pending.

The tool schemas a product turn advertises match the durable executor's behaviour. A research route keeps its frozen schemas, which promise more than the executor does, because its provider-request digests were measured with them.

| Tool | Advertised to the model | What the executor does |
| --- | --- | --- |
| `read` | The whole contents of an existing UTF-8 text file at a workspace-relative path. No `offset` or `limit`. A missing file, an absolute path, a file of about 1 MiB or more, or a file that is not valid UTF-8 ends the turn. | Reads the whole file. It refuses an `offset` or `limit`. The Store fails closed on a missing, over-bound or non-regular target and quarantines. A file that is not valid UTF-8 cannot be released as a read result. Any of these ends the provider conversation as unknown. Images are not supported. |
| `write`, `edit` | A workspace-relative path. An absolute path ends the turn. | An absolute path is invalid, and the invalid call ends the provider conversation as unknown. |
| `bash` | No `timeout`. The last 16 KiB of each stream. A command that runs longer than 10 minutes, or prints more than 1 MiB on one stream, is stopped and returns no result. | It ignores a `timeout` argument. The command cell stops a command after 10 minutes, and the development runner after 30 seconds. A stream over 1 MiB withholds the result. |

Go still parses `offset`, `limit` and `timeout` when a call carries them, because research calls and retained provider evidence do. The adapter registers the same text and `adapter/profile.json` pins the digest of each schema. `TestAdvertisedToolSurfaceEqualsTheAdapterTextAndItsPins` and the adapter's `proxy-schema.test.ts` hold the broker, the adapter and the pins together, and `TestResearchToolSurfaceIsFrozen` holds the research surface. Changing a schema changes the adapter bytes, so it needs the adapter runtime bundle to be rebuilt and re-pinned.

## Milestone 4: product service and terminal client (P6)

### Service

- `tbound product-serve --config /etc/tbound-product/service.json` runs the product service, owned by the product service account. Its config names:
  - the signed registration, its signature and public key;
  - the catalog journal and the state root;
  - one managed directory per registered project;
  - the host profile's installed inputs (see [Host profile](#host-profile)): one gate policy file per project, the private Git executable with its pinned digest, and the signed xattr calibration;
  - the worker mode: `supervisor-loop`, the supervisor's own in-process agent loop. `pi` is refused with its own error until a real Pi worker per turn exists on a qualified host.
- Every path must be a product default or below one. None may be a thesis path. The Git executable must sit below `/usr/local/lib/tbound-product`. The other paths sit below the registration, catalog or socket directories.
- On start the service verifies the registration and opens the catalog. It runs `ReconcileAfterRestart` before it accepts any request. Then it listens on `/run/tbound-product/product.sock`, socket mode 0660, group-restricted.
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
- **Bash.** No contained command runner exists, so a policy with an allow rule for `bash` refuses the start with an error that names the project. The per-turn refusal described under [Bash in product turns](#bash-in-product-turns) stays as a second line. Nothing wires the development runner into production.
- **Provider.** Every profile the registration lists must be in the compiled table, which holds `opencode-go-engineering` only. A turn's conversation is built for its session's profile and refused when that profile is not in the registration. The endpoint, model and request shape are compiled in. No config field or environment variable changes them. The key is checked before listening and read again per turn from `/etc/tbound-product/credentials/<profile>.key` under the provider credential rules (service-owned 0700 directory, 0600 file, protected ancestors), so a rotated key needs no restart.
- **Prompts.** The system and developer prompts are compiled in and name no tool, because the policy decides the tools. They are part of every turn's retained evidence, so changing either text breaks the replay of turns recorded under the old one.
- **Git.** The config's `git_digest` must equal the signed registration. The compiled host identity must also match the registration before listening. The executable's bytes and mode are checked against `git_digest` at start and again when each turn pins it. Its execution boundary is labelled `product-supervisor-private-git-admin/v1`: Git runs in the supervisor's own process, not in a command cell.
- **Xattr.** The calibration must verify under the registration public key and cover the managed-writable source profile. At start, and again before each publication, the filesystem, mount options and scanner identity (non-root UID and GID, no capabilities, no new privileges, initial user namespace) of the state root and of every managed project root must equal the calibration. Each managed project is opened through its registered root identity check. The attestation carries the calibration's bounded authority only: the administrator's signed statement that this filesystem, mount and scanner hide no `trusted.*` attribute. It is not proof that the unprivileged scanner enumerates every attribute, and no managed live-root lease backs it.
- **Worker steps.** The step bound of a turn is the registration's per-turn host-call limit, because one step is one provider exchange.
- **Publication trust.** A publication needs the turn's authority to be bound and the provider profile to be registered. It needs the supervisor loop's conversation to be closed and no unresolved effect, host call or Store quarantine. It re-verifies the xattr identity and needs the managed project to still equal the turn's baseline. It refuses any command effect. The turn's own authority verifies every transition and origin. The trust does not exclude a writer outside the catalog, the publication ledger and the Store. An outside edit made before publication fails the baseline check, and one made later is caught by the next baseline measurement. The trust has no Bash lease resolver, so it cannot publish a Bash turn.

Setup requires the same fields and writes them into the service config. It does not install the policy files, the Git executable or the calibration. The administrator places them first, and setup and the service both verify them (see [Host inputs](#host-inputs)). `tbound product calibration measure` and `sign` produce the calibration. The research `tbound-profile xattr-calibration-*` commands write the same artifact, but they read a raw private key and write a raw signature, while product keys and signatures are base64.

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

### Registration tooling (offline administrator)

- `tbound product registration keygen --out DIR` writes an Ed25519 key pair. The private key is 0600, and the command refuses an existing file. The private key stays offline and never enters the service host.
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
   | runtime directory | from the unit | from the unit |

4. It installs the registration, signature and public key as root-owned 0644 files. It writes the service config and the `tbound-product.service` unit: `User=tbound-product`, `RuntimeDirectory=tbound-product`, and hardening matching the research unit where it applies. Then it runs `daemon-reload`.
5. It never creates or replaces the owner's signing key, and it never touches a `/etc/tbound`, `/var/lib/tbound` or `/run/tbound` path.

The setup config must name the host profile inputs: `gate_policy_paths` with a file below `/etc/tbound-product` for every project, `git_path` below `/usr/local/lib/tbound-product` with its `git_digest`, and `xattr_calibration_path` below `/etc/tbound-product`. Setup refuses a config without them or with a thesis path, and records them in the service config. It does not install those files, and it refuses to run until each passes the static checks (see [Host inputs](#host-inputs)).

Running it again changes nothing and reports "unchanged". An existing file with different content is refused unless `--replace` is given.

### Host inputs

The administrator places the host profile inputs (see [Host profile](#host-profile)) before `tbound product setup`. Three commands make and check them. The static checks are shared: `product-serve` runs them at start with the service's owner set, and `setup` and `doctor` run the same code with root and the service account as the allowed owners (root only on a first install, before the account exists; a test root also admits the test process).

**Static checks** (setup, doctor and service start). Gate policy of each project: a protected regular file (no symlink, not group or world writable, owner in the allowed set, bounded) that parses, whose digest equals the signed registration's `policy_digest`, and that does not allow `bash` while no contained runner exists. Git: the production pin (digest and mode, trusted ancestors) and an allowed owner. Calibration and its `.sig`: both protected, the signature verifies under the registration public key over the canonical bytes, and the source profile is managed-rw. **Live checks** stay with the service and are not run by setup: the scanner identity of every scanned root against the calibration (needs the service UID, zero capabilities, NoNewPrivs and the initial user namespace) at start and again before each publication, unchanged.

- `tbound product setup` runs the static checks on every input before it writes anything (accounts, directories, unit, config), with the registration public key it is about to install. A bad input refuses the run with exit 2 and names the input and the reason. `--dry-run` reports the same refusal and writes nothing. Setup therefore needs the policy files, Git and the signed calibration in place first.
- `tbound product doctor` reports one advisory line per input from the installed setup record: each project's policy, `input git`, `input calibration`, `input calibration signature` and `input registration public key`, with path, owner, mode and a digest prefix, or the reason for the refusal. It prints no secret.
- `tbound product calibration measure --source DIR --probe-corpus-digest sha256:HEX --out FILE [--config FILE] [--force]` runs on the host as the service user (not root; the measurement refuses a privileged scanner, so run it with no new privileges and no capabilities). `DIR` is the held probe corpus the operator prepared, and the digest is the one the operator reviewed. The access profile is fixed to managed writable roots. The command also measures the state root and every project root of the service config (default `/etc/tbound-product/service.json`) and refuses, writing nothing, when any scanner identity differs from the corpus measurement. It writes the unsigned measurement to `FILE` and does not replace an existing file without `--force`.
- `tbound product calibration sign --key PRIVATE --public-key PUBLIC --in UNSIGNED --out SIGNED` runs offline with the registration key pair. Product keys are base64 files, not the raw research keys. The private key must be a regular file that group and others cannot read. The command refuses a private key that does not match the public key, a calibration for another access profile, and an existing output. It publishes `SIGNED` and then `SIGNED.sig` through synced temporary files and an atomic no-replace rename, so a crash leaves a pair the verifier refuses, never one it accepts wrongly. It never prints key material.

The probe install, census and cleanup stay an operator procedure: place the probe attributes on a throwaway corpus below the managed root, census it, run `calibration measure`, then remove the corpus and prove the census returns to its baseline. The research runbook step is R1-5 in `infra/guest/PROVISIONING-SIGNING.md`; the product differs only in the commands and the key format.

**Authority.** The signed calibration is root-attested. The trusted administrator signs that, for this filesystem type, mount options and scanner identity, no hidden `trusted.*` attribute exists on the managed roots. Enumeration by the unprivileged scanner cannot prove that: a non-root scanner cannot list `trusted.*` names at all, so the scan's silence is not evidence of absence. The measurement records the observed probe corpus and scanner identity. The signature, made with the offline registration key, adds the administrator's claim about attributes the scanner cannot see.

### Update, rollback and uninstall

- `tbound product update --binary NEW [--root DIR]` stages the new binary, stops the service, and opens the catalog. It refuses while any turn is unresolved, other than a pending review, which becomes `discarded` with reason `restart`. It keeps the current binary as last-known-good, swaps atomically, starts the service, and runs the self-test. If the self-test fails, it rolls back. Retrying when the requested binary is already installed still starts the service and runs the self-test, preserving the existing last-known-good for recovery.
- `tbound product rollback` restores last-known-good the same way.
- Binary and catalog sources are opened through pinned parents without following leaf symlinks. The opened file must be regular and single-linked; managed sources must have their expected owner on a live host, binaries must be 0755, and catalog backups accept 0600 or 0640 sources. Digests and copies use the same descriptor. A retry validates any retained last-known-good before starting the service, and recovery validates it again before copying.
- Catalog schema changes need a version check and a backup before the swap. A newer schema is never read by an older binary.
- `tbound product uninstall [--root DIR] [--purge]` stops and disables the service and removes the unit, binary and runtime access. On a live host it verifies the service is inactive before removing files. Stop or disable failures abort removal unless systemd confirms the unit is absent and inactive. It reports unresolved turns. It keeps `/etc/tbound-product` and `/var/lib/tbound-product` unless `--purge` is given, and `--purge` asks for confirmation unless `--yes` is also given. A purge refuses an unreadable or locked catalog before removing files. Deletion errors are returned, and only successful deletions are reported as removed.
- Live catalog checks run the currently executing administrative binary as the service account, so purge also works after an ordinary uninstall removed the installed binary. The internal `product turns` command permits non-root callers while retaining the catalog's ownership and permission checks.

### Diagnostics

- `tbound product doctor` is advisory. It reports prerequisites, installation owners and modes, unit state, socket reachability, registration verification, one line per host input (see [Host inputs](#host-inputs)), and credential status (never the key).
- `tbound product doctor --self-test` runs an explicit runtime self-test turn on a synthetic project with the configured worker. With `--dry-run` it reports the planned self-test without submitting a turn or establishing readiness. Only a passed self-test, never the advisory checks, can report governed readiness.

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
- `expected_revision` is the revision of the last session view the page read, and the view is read again after every lifecycle event.
- The event stream is a long poll that starts from the snapshot's cursor. The cursor is stored per session in the tab, and a gap refetches the snapshot.
- Publish and Discard send the digest of the candidate that is displayed.

### Windows access

Use an SSH tunnel to the product host: `ssh -L 8791:127.0.0.1:8791 user@host`, with pinned host keys. Never bind a non-loopback address.
