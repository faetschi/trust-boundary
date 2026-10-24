# TBound product status

This is the product-branch checkpoint for [the product plan](product-plan.md). It records what is implemented and verified on `feature/tbound-product`. The thesis state lives in `TASK_STATE.md` on `codex/tbound-prototype` and is not tracked here.

## Baseline (P0)

| Item | Value |
| --- | --- |
| Product branch | `feature/tbound-product` |
| Product worktree | `MA Thesis/trust-boundary-product` (git worktree of `trust-boundary`) |
| Baseline commit | `ff374c9634a296fad77cba72cb837ebdb733aac8` on `codex/tbound-prototype` |
| Baseline gate | Linux, Go 1.27.1, `-mod=readonly`, `GOPROXY=off` |
| Build / vet / gofmt | exit 0 / exit 0 / exit 0, no unformatted files |
| `go test -race -count=1 ./...` | exit 0: 4634 passed, 26 skipped, 0 failed |
| Gate archive | `git archive ff374c9`, SHA-256 `37d95e7fcf248721f7fe9a241847b7fd3a4ce564c3ef2f710dc09b840cde6bab` |
| Gate script and receipts | `%TEMP%\tbproduct\gate.sh`, `%TEMP%\tbproduct\receipts\<commit>\` |

The 26 skips need root, a real cgroup, Landlock, a real provider key, or the pinned Pi CLI. They are guest-only checks and were skipped on the thesis branch too:

- `cmd/tbound`: real provider, dev-fixture Pi launch, foreign-owner credentials, five real-cgroup tests.
- `cmd/tbound-cell-entrypoint`: two Landlock enforcement tests.
- `internal/procoracle`: eight real-process tests.
- One test each in `audit`, `broker/openrouter` (two), `piruntime`, `sandbox`, `trialfix`, `workflowrecovery`, `cmd/tbound-profile`.

### Isolation

- Product work happens only in the product worktree and branch. Nothing is merged into `codex/tbound-prototype`.
- The T3 thread could not be bound to the worktree with `t3_worktree_handoff`, because the T3 project root (`MA Thesis`) is not a git repository. Work uses the worktree's absolute paths instead.
- No product runtime exists yet. There is no product guest, product service, product managed root, product signing key, or product provider credential. **P0 runtime isolation and all guest acceptance are pending.** Product code uses its own default paths (see [contracts](product-contracts.md#paths)) so it cannot address the thesis service or roots by default.

## Milestone 1: headless governed multi-turn conversation (P1 to P3)

Status: implemented and verified locally with headless clients and deterministic provider fixtures. Not qualified on a guest. This is not an interactive product yet.

| Package | State |
| --- | --- |
| P0 baseline | Done locally. Product runtime, guest and signing registration pending. |
| P1 contracts | [product-contracts.md](product-contracts.md), including decisions taken during implementation. |
| P2 product admission and accounting | `internal/productsession`: signed registration, durable catalog, request keys, budgets, project ownership, restart reconciliation of unlinked turns. Not wired to a service transport. |
| P3 isolated multi-turn turns | `cmd/tbound/product_turn_*`: one fresh governed workflow per turn, verified predecessor history, headless worker, settlement and auto-publication. |

### What runs today

`productTurnEnv.RunTurn` takes one admission from the catalog and runs it to a recorded disposition:

1. It reopens the predecessor turn's retained Store and journals read-only. It checks them against the predecessor's receipt, then replays them with real Store cross-linking into the imported history.
2. It measures the managed project and compares it with the admitted baseline.
3. It composes a fresh workflow: new workflow, run, session and conversation IDs, a Store seeded from the project, a protected owner, and a ledger limited by the turn's reservation.
4. It links the turn in the catalog before any provider work.
5. The headless worker runs the governed loop. It correlates each call, evaluates the gate, records the decision, and executes the ALLOWed calls.
6. It settles the turn:
   - no change gives `no_diff`;
   - auto mode publishes the sealed tree through the existing publication finalizer, giving `published`;
   - review mode marks the turn `awaiting_review`;
   - a known failure with no applied effect gives `failed`;
   - anything uncertain gives `unknown`, which keeps the reservation and blocks the project.

The worker is the in-process deterministic driver (`worker_kind` `headless-fixture`). It stands in for the Pi process and makes the same broker calls the Pi adapter makes. No real Pi process runs per turn yet, and receipts never claim an OS process settlement.

### Demonstrated behaviour

| Milestone requirement | Test |
| --- | --- |
| Two different prompts admitted under one registration | `TestProductTurnDriverRunsTwoTurnsFromRetainedEvidence`, `TestTwoPromptsFormTurnsOneAndTwo` |
| Verified context carried into turn 2, and turn 2 reads turn 1's published file | `TestProductTurnDriverRunsTwoTurnsFromRetainedEvidence` |
| Fresh worker, workflow, run, conversation and runtime session per turn | same test (identity assertions) |
| Denied call returns its hint, the hint reaches turn 2's provider request, and old call IDs gain no authority | `TestProductTurnDriverCarriesADenialAndHintIntoTheNextTurn`, `TestProductTurnDriverImportedCallDoesNotCorrelate`, providerbridge `predecessor_linux_test.go` |
| Duplicate requests and conflicting payloads | `TestSubmitTurnIdempotencyAndConflict`, `TestOpenRecomputesTheRequestPayloadDigest` |
| Stale revision and stale baseline | `TestSubmitTurnCheckOrder`, `TestProductTurnDriverRefusesAStaleBaseline` |
| Altered predecessor evidence blocks launch and refunds | `TestProductTurnDriverRefusesTamperedPredecessorEvidence`, `TestProductTurnPredecessorRefusesAModifiedRetainedJournal` |
| Truthful settlement; unresolved work and external edits block continuation | `TestProductTurnDriverRecordsUnresolvedChildWorkAsUnknown`, `TestProductTurnDriverRecordsAnExternalEditAsUnknown`, `TestProductTurnDriverRecordsAnEditAfterPublicationAsUnknown`, `TestProductTurnDriverHoldsAChangedReviewTurn`, `TestSettlementRequiresAReceiptForThisTurn` |
| Restart: an admitted, unlinked turn survives a fresh process and is reconciled | `TestFreshProcessReconcilesAnUnlinkedAdmission` |
| Research mode intact | full gate below; `TestServiceConfigValidationCoversBothAdmissionShapes` refuses a `product` admission mode |

### Pre-existing defects fixed on this branch

These also exist on the thesis baseline `ff374c9`. The thesis thread was told about them. Whether to fix them on `codex/tbound-prototype` is its decision.

- A gate DENY in a ledger-backed workflow closed the conversation, so the model never saw the denial or its hint (reproduced on `ff374c9`). Fixed in `c89e763` with a new workflowrecovery state for a retired denied call whose answer is pending.
- Recovery preview indexed the wrong authorization record kind and policy-digest form, so it could not reopen a Store with effects. Fixed in `e35a94e`.
- The governed capture check did not accept the imported-history record. Fixed in `c9effb4`.

### Verification

| Commit | Gate result |
| --- | --- |
| `3394fb3` | build, vet, gofmt clean; 4692 passed, 26 skipped, 0 failed |
| `0bcce2f` | build, vet, gofmt clean; 4723 passed, 27 skipped, 0 failed |
| `b2cbd4f` | build, vet, gofmt clean; 4727 passed, 27 skipped, 0 failed |
| `d2d2798` (final code commit) | build, vet, gofmt clean; 4728 passed, 27 skipped, 0 failed; archive SHA-256 `1ce67304e9500ee31f61facdd3da1786404790833cb8ece0abc43c8aebc34659` |

The 27th skip is `TestGovernedLedgerDenyReopenChild`, a subprocess helper that runs only when its parent test starts it.

Independent reviews: GPT 6.1 Sol (xhigh) ran ponytail-review over the catalog and history import, the governed replay and DENY fixes, and the driver. All confirmed findings were fixed with regression tests except the deferred items below.

### Pending and known limits

- **Guest qualification.** No product guest, runtime, service, signing key or provider key exists. A real Pi worker per turn, product publication trust (tests use a fixture trust that accepts admission and quiescence), and every guest check remain unrun.
- **Service transport.** The catalog has no wire protocol, peer-UID authentication or client. Principals are passed by the caller.
- **Scope.** Turns use the registration's project scope. The narrower per-request scope from the plan is not implemented, and the gate policy works per tool, not per path.
- **Bash.** Product turns refuse Bash, because a turn that used Bash cannot yet be reopened as a predecessor.
- **Recovery (P4).** Linked turns that crash can only be marked unknown. A crash between a journaled DENY and its retirement checkpoint is not reconciled by recovery; it fails closed. The recovery path for a workflow's later conversations has no dedicated test.
- **External writers.** A project edit made after the turn's final measurement but before its catalog record is not detected by that turn. It is never adopted as the committed baseline, and the next admission refuses the mismatch. Fencing protects cooperating writers only.
- **Review and cancellation (P4).** `awaiting_review` has no resolution operation yet.

### Next milestone

P4: review, cancellation and fresh-process recovery. That means resolving `awaiting_review` turns by exact candidate, cancellation at every point, evidence-based reconciliation of linked turns, the DENY crash window, and the crash-boundary test matrix from the plan. The product guest should be provisioned in parallel, so a real Pi worker per turn can replace the headless fixture.

What this milestone does not include: review and cancellation (P4), project import and export (P5), the terminal client (P6), web chat, installation, and release qualification.
