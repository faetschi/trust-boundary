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
- Every parent directory must be listed in the manifest. `.git` and a root `lost+found` are always excluded. Excluded subtrees are never read.
- Only the executable bit is carried. New files are created 0644 or 0755. A change of type (file to directory) is refused.
- Apply validates the baseline digest, the declared target digest and every content digest before the first write. It refuses changes to files Git does not track, because their original content could not be restored. The recovery record lists the pre-apply entries, and restoring relies on the clean tracked tree in Git.
- `apply` takes the same `--exclude` list as `bundle`, so both sides compute the same manifest.
- `import` and `apply` do not check which account runs them. The separation between service and user comes from who runs each command, as the help text states.

### Development environment

The first supported language is Go, with a pinned toolchain image and a pre-filled module cache. Tests run with the command cell's network disabled. Building and pinning the image needs the product guest and is pending. The advertised tool schemas must match the durable executor's real read, binary and Bash-timeout behaviour.

## Milestone 4: product service and terminal client (P6)

### Service

- `tbound product-serve --config /etc/tbound-product/service.json` runs the product service, owned by the product service account. Its config names:
  - the signed registration, its signature and public key;
  - the catalog journal and the state root;
  - one managed directory per registered project;
  - the provider profile;
  - the worker mode: `pi` on a qualified host, or `headless-fixture` for tests only.
- Every path must be a product default or below one. None may be a thesis path.
- On start the service verifies the registration and opens the catalog. It runs `ReconcileAfterRestart` before it accepts any request. Then it listens on `/run/tbound-product/product.sock`, socket mode 0660, group-restricted.
- The principal is the connection's `SO_PEERCRED` UID. The catalog checks it against `allowed_principals`. Payloads never carry a principal.
- Admitted turns run in the service, not in the client. A client that disconnects leaves its accepted work running. One goroutine drives each turn. A review-expiry ticker calls `ExpireReviews`.
- Shutdown stops new admissions, lets running turns reach their next step boundary, and closes pending reviews. A pending review then becomes `discarded` with reason `restart` on the next start.

### Protocol `tbound-product-protocol/v1`

Each connection carries one length-prefixed strict JSON request and one response, the same framing as the research service. Requests are at most 64 KiB and responses at most 1 MiB.

| Operation | Request | Response |
| --- | --- | --- |
| `create_session` | project, provider profile, publication mode, budget, read_only, request key | session snapshot, replayed flag |
| `submit_turn` | session, expected revision, request key, prompt, optional expected baseline | turn snapshot, replayed flag (the turn starts asynchronously) |
| `get_session` | session | session snapshot with every turn's state, outcome, candidate, cancel request, resolution, and remaining budget |
| `list_sessions` | none | the principal's sessions |
| `observe` | session, cursor, wait_ms (at most 30000) | events after the cursor, next cursor, gap flag |
| `get_candidate` | session, turn | the pending candidate's changed paths, each with operation and a bounded text diff |
| `cancel_turn` | session, turn, expected revision, request key | turn snapshot |
| `resolve_turn` | session, turn, expected revision, request key, candidate digest, decision | turn snapshot |

- Errors carry the catalog's stable error codes. A request never retries on its own. A client retries only with the same request key.

### Observations

Events are projected only from durable records: catalog records for the session, and each turn's provider journal. The kinds are `turn_admitted`, `turn_linked`, `assistant_message`, `tool_call` (tool, verdict, reason code, hint), `tool_result` (bounded summary), `turn_cancel_requested`, `turn_awaiting_review` (candidate digest and changed paths), `turn_resolution_requested`, and `turn_terminal` (outcome and reason).

- The cursor indexes the deterministic event list of the session.
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
