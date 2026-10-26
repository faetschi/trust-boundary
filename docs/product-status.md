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
| P3 isolated multi-turn turns | `cmd/tbound/product_turn_*`: one fresh governed workflow per turn, verified predecessor history, supervisor-loop worker, settlement and auto-publication. |

### What runs today

`productTurnEnv.RunTurn` takes one admission from the catalog and runs it to a recorded disposition:

1. It reopens the predecessor turn's retained Store and journals read-only. It checks them against the predecessor's receipt, then replays them with real Store cross-linking into the imported history.
2. It measures the managed project and compares it with the admitted baseline.
3. It composes a fresh workflow: new workflow, run, session and conversation IDs, a Store seeded from the project, a protected owner, and a ledger limited by the turn's reservation.
4. It links the turn in the catalog before any provider work.
5. The supervisor loop runs the governed loop. It correlates each call, evaluates the gate, records the decision, and executes the ALLOWed calls.
6. It settles the turn:
   - no change gives `no_diff`;
   - auto mode publishes the sealed tree through the existing publication finalizer, giving `published`;
   - review mode marks the turn `awaiting_review`;
   - a known failure with no applied effect gives `failed`;
   - anything uncertain gives `unknown`, which keeps the reservation and blocks the project.

The worker is the supervisor's own in-process agent loop (`worker_kind` `supervisor-loop`). It makes the same broker calls the Pi adapter would make, and only its provider conversation is scripted in tests. No real Pi process runs per turn yet, and receipts never claim an OS process settlement.

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
- **Bash.** Milestone 1 refused Bash. P5 composes it when the project policy allows it, and reopens a Bash turn as a predecessor by verifying its retained command settlements. See [contracts](product-contracts.md#bash-in-product-turns). The model sees each command's stdout and stderr, bounded to the last 16 KiB per stream and rebuilt from the Store on replay, and the advertised tool schemas match the executor (a read has no offset or limit, and bash has no timeout parameter). Only the development command runner is exercised locally. The D09 cell runner and its history opener need the product guest.
- **Recovery (P4).** Linked turns that crash can only be marked unknown. A crash between a journaled DENY and its retirement checkpoint is not reconciled by recovery; it fails closed. The recovery path for a workflow's later conversations has no dedicated test.
- **External writers.** A project edit made after the turn's final measurement but before its catalog record is not detected by that turn. It is never adopted as the committed baseline, and the next admission refuses the mismatch. Fencing protects cooperating writers only.
- **Review and cancellation (P4).** `awaiting_review` has no resolution operation yet.

### Next milestone

P4: review, cancellation and fresh-process recovery. That means resolving `awaiting_review` turns by exact candidate, cancellation at every point, evidence-based reconciliation of linked turns, the DENY crash window, and the crash-boundary test matrix from the plan. The product guest should be provisioned in parallel, so a real Pi worker per turn can run beside the supervisor loop.

## Milestone 2: review, cancellation and restart (P4, headless)

Status: implemented and passing the local gate. The independent review is still pending, because both Codex providers were unavailable. Not qualified on a guest. The contract is in [product-contracts.md](product-contracts.md), sections "Milestone 2" and "P4 catalog decisions".

### What runs today

- **Review.**
  - A review-mode turn settles as `awaiting_review` and keeps its closed workflow and publication finalizer in this process.
  - `ResolveTurn` first records the decision for the exact candidate digest.
  - A publish then runs the retained finalizer. It counts as `published` only if the project then measures exactly the candidate.
  - Discard keeps the candidate and the spend.
  - `ExpireReviews` discards reviews past their registered timeout.
- **Cancellation.**
  - `CancelTurn` records the request first.
  - Before launch, the turn ends `cancelled` with a full refund.
  - During execution, the worker stops before its next provider exchange, never mid-call. The turn ends `cancelled` with nothing published.
  - During review, the turn is discarded with reason `cancelled`.
  - Once publication has started, the turn reports its real outcome.
- **Restart.**
  - `ReconcileAfterRestart` runs before new admissions. It settles every unresolved turn from retained evidence: the Store, the journals, the owner checkpoint, a fresh project measurement, and the project publication journal.
  - A pending review never survives a restart and is never published on reconstruction alone.
  - Anything it cannot prove ends `unknown`, which blocks the project.

### Demonstrated behaviour

| Requirement | Test |
| --- | --- |
| Publish only the exact candidate | `TestProductTurnReviewPublishesOnlyTheExactCandidate`, `TestResolveTurnRules` |
| Discard keeps the project unchanged and the spend consumed | `TestProductTurnReviewDiscardKeepsTheProjectAndTheSpend`, `TestReviewedDiscardReasons` |
| Review expiry | `TestProductTurnReviewExpires` |
| Cancel before launch, mid-turn, during review, after publication started | `TestProductTurnCancelBeforeLaunchRefundsAndBlocksNothing`, `TestProductTurnCancelMidTurnStopsBeforeTheNextExchange`, `TestProductTurnCancelDuringReviewDiscards`, `TestProductTurnCancelAfterAutoPublicationStartedReportsTheRealDisposition` |
| Fresh-process crash at six boundaries (after admission, before the first exchange, after an effect, at pending review, after the publish decision, after publication) with no effect repeated | `TestProductTurnRestartReconcilesEveryCrashBoundary` |
| A project changed while the service was down becomes unknown | `TestProductTurnRestartRecordsAChangedProjectAsUnknown` |
| Catalog replay of every new record in a fresh process | `TestFreshProcessRestoresCancelAndResolutionRequests`, `TestOpenRefusesInvalidReviewRecords` |
| Reviewed terminals settle no budget twice and keep the review evidence | `TestReviewedPublishMovesBaselineWithoutSettlingAgain`, `TestReviewedTerminalKeepsTheReviewExecutionFacts` |

### Verification

| Commit | Gate result |
| --- | --- |
| `cf045c9` (thesis branch merged in) | build, vet, gofmt clean; 4728 passed, 27 skipped, 0 failed |
| `93c359a` (P4) | build, vet, gofmt clean; 4759 passed, 28 skipped, 0 failed |

The 28th skip is `TestProductTurnCrashChild`, which runs only as the subprocess of the crash matrix.

### Pending and known limits

- **Independent review.** GPT 6.1 Sol has not reviewed the P4 driver (`41485e4`, `93c359a`) yet. It did review the P4 catalog, and those findings were fixed or rejected with a reason.
- **Guest-only cancellation.** Cancelling a real Pi process, a running Bash command, or an abort acknowledged while descendants survive all need the product guest.
- **Expiry trigger.** Nothing calls `ExpireReviews` periodically yet; the service loop will. A resolve attempt checks expiry first.
- **Reconciled turns.** A turn reconciled after a restart is billed its full reserved duration. Its receipt has no worker instance or tool list, because the in-process worker's record died with its process.
- **Crash between link and prompt admission.** In that window the turn ends `unknown` and blocks the project.
- **Crash in the middle of publication.** Not tested.
- **Restart after a failed publication.** A publish decision whose publication failed is `unknown` after restart, per the table, even though `failed` could be proven.

What this milestone does not include: project import and export (P5), the terminal client (P6), web chat, installation, and release qualification.

## Milestone 7: production host profile (supervisor-loop worker)

Status: implemented and verified locally against a loopback fake provider. Not qualified on a host. No real provider key was read and no paid provider was called. The contract is in [product-contracts.md](product-contracts.md#host-profile).

### What runs today

- `tbound product-serve` starts. Its host profile (`cmd/tbound/product_service_host_linux.go`) fills every trusted piece of a turn from installed, verified inputs: the gate policies, the pinned Git executable, the xattr attestation, the provider conversation factory, the prompts, the publication trust and the worker step bound. It refuses to start when an input is missing or differs from the signed registration.
- The worker kind is `supervisor-loop`, and its settlement is `supervisor-loop-closed`. These replace `headless-fixture` and `headless-fixture-closed` everywhere. No product catalog is deployed, so there is no compatibility alias.
- A project policy that allows Bash refuses the start, because no contained command runner exists.
- The service setup requires the host inputs and writes them into the service config. It does not install the policy files, the Git executable or the calibration.

### Demonstrated behaviour

| Requirement | Test |
| --- | --- |
| Service started through the real host profile and the real conversation factory. Turn 1 writes a file and is published after review. Turn 2 reads and edits it. The fake provider receives turn 1's verified messages in turn 2's first request | `TestProductServiceHostRunsTwoGovernedTurnsThroughTheRealFactory` |
| A write to a protected path is denied, and the provider and the client get the reason code and its hint | same test |
| An edit from outside the service while a review is pending is not published over | `TestProductServiceHostTrustRefusesPublicationOverAnExternalEdit` |
| A policy that allows Bash refuses the start, and nothing listens | `TestProductServiceHostRefusesAPolicyThatAllowsBashAtStartup` |
| Unverified inputs refuse the start with the named reason: policy digest, group-writable or symlinked policy, Git digest or mode, calibration signature or bytes, scanner identity, missing config fields | `TestProductServiceHostRefusesUnverifiedInputsBeforeListening` |
| Credential mode and presence rules apply through the host profile | `TestProductServiceHostEnforcesTheCredentialRules` |
| The offline test seams refuse to run outside a test binary and no other non-test source names them | `TestWithTestTransportIsRefusedOutsideATestBinaryAndKeepsTheEndpointChecks`, `TestConversationFactoryForTestIsRefusedOutsideATestBinaryAndKeepsTheCredentialRules`, `TestOfflineProviderSeamsAreNamedOnlyByTheirOwnPackages` |
| Config fields stay inside the product directories, and setup and serve agree on them | `TestProductServiceConfigHostInputsStayInsideTheProductDirectories`, `TestSetupRequiresAndRecordsTheHostProfileInputs`, `TestProductAdminSetupWritesTheTreeAndARerunIsUnchanged` |

### Verification

Linux, Go 1.27.1, `-race -count=1`, gofmt and vet clean.

| Command | Result |
| --- | --- |
| `./cmd/tbound/ ./internal/productsession/... ./internal/productprovider/... ./internal/productsetup/... ./internal/broker/... ./internal/providerbridge/...` | every package ok (`cmd/tbound` in 509 s) except `productsetup`, where one existing test failed because its fixture lacked the new required policy paths |
| `./internal/productsetup/... ./internal/productprovider/... ./internal/broker/openrouter/...` after that fix | ok |

Source hash of `supervisor/` for the second run: SHA-256 of the sorted per-file SHA-256 list, `ba358feeba5fe4fe9ba83bd2e3ea6d3f2cc89f5e2063908d75133b6125485976`.

### Pending and known limits

- **No qualified host.** The tests replace only the scanner measurement with a described one. The calibration signature, the mount comparison logic, the file protection checks and the Git pin run as written. The scanner identity, the calibration and the service account have never run on a real host.
- **No real provider.** The real transport, the credential rules and the endpoint check ran against a loopback server that serves the fixed endpoint. No real key was used. Provider behaviour that a fake does not show is untested.
- **Bash.** Refused until D09 command cells exist on a qualified host.
- **Pi worker.** Still refused with its own error.
- **Calibration.** No product command measures or signs the xattr calibration. The research `tbound-profile` commands write the same artifact in raw key and signature form, and their use for the product is untested.
- **Setup and doctor.** Setup does not install or check the policy files, the Git executable or the calibration. `product doctor` does not report them. The service verifies them at start.
- **External writers.** The publication trust does not exclude a writer outside the catalog, the publication ledger and the Store. It detects an outside edit that happens before publication, and the next baseline measurement detects a later one.
- **Prompts.** The compiled-in prompts are part of each turn's retained evidence. Changing them breaks the replay of earlier turns.
