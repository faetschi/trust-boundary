# TBound product status

> **Takeover, 2026-10-29.** For this takeover it supersedes the stale next-action lists in [Handoff 2026-10-26](#handoff-2026-10-26) and [Resumed 2026-10-27](#resumed-2026-10-27-on-fabian). Selective P4 and P5 ports are implemented as `b7eefe3` and `b824d0d`, independently reviewed and covered by red-before-green regressions. The audit merge baseline `43dc492` passed 5745 tests / 31 skips / 0 fail. Final delivery evidence is retained in `F:/TBoundAssets/takeover-20261029/RESULTS.md`; P6 and real-host qualification remain open. No product-to-thesis merge happens before the F14 freeze. A reviewed integration branch is proposed only after that freeze.

This is the product-branch checkpoint for [the product plan](product-plan.md). It records what is implemented and verified on `feature/tbound-product`. The thesis state is owner-local and is not tracked here.

## Status by package (P0-P11)

Current combined gate, 2026-10-27, on `610577b` (code `5311409`). Linux, Go 1.27.1, `-mod=readonly`, `GOPROXY=off`. Archive SHA-256 `4f25ad02cd1174c92856604ae1d4cf0b908a30f75ddfc502b9d1c7204a0305e7`. Receipts are in `%TEMP%\tbproduct\receipts\610577b\`.

| Check | Result |
| --- | --- |
| Supervisor build / vet / gofmt | exit 0 / exit 0 / no unformatted files |
| `go test -race -count=1 -timeout 40m ./...` (supervisor) | exit 0: 5238 passed, 30 skipped, 0 failed, counting subtests (2909 top-level tests; 57 packages ok, 3 without tests) |
| `extension-webchat` (Go 1.27.1 in WSL, node on Windows) | gofmt clean, vet exit 0, `go test -race` both packages ok; `node --test` 262 passed, 0 failed |
| `extension-webchat-frontend-verify` | `npm ci`, then `repro`: two clean rebuilds identical and equal to the committed `dist/`; then `verify`: typecheck, 83 test files and 1397 tests passed, build, sinks and dependency checks OK. This needed `499fb9f`: before it, `differential.test.ts` could not load the legacy assets from a checkout path that contains a space. |
| `make boundary` | the guard passes; self-test 8 of 8 |

`make` is not installed on this host, so the recipes of those Makefile targets were run as written. The 30 skips are the 26 guest-only checks listed under [Baseline](#baseline-p0), the restart and crash subprocess helpers (`TestGovernedLedgerDenyReopenChild`, `TestProductTurnCrashChild`, `TestProductTurnBashCrashChild`), and `TestSecurityBindMounts`, which needs root.

"Done locally" means the code and its tests pass on the development Linux gate. Nothing is qualified on a real host. There is no real provider key, no real Pi worker (the supervisor loop runs the turns), and no product guest.

| Package | State | Evidence | Remaining blockers |
| --- | --- | --- | --- |
| P0 baseline | done locally | `ff374c9`, [Baseline](#baseline-p0) | Product runtime, guest, service and signing registration. |
| P1 contracts | done locally | [product-contracts.md](product-contracts.md), `6b4d194` | None for the headless scope. Revise on any accepted change. |
| P2 admission and accounting | done locally | `c5eb78a`, `52b597c`, [Milestone 1](#milestone-1-headless-governed-multi-turn-conversation-p1-to-p3) | Guest qualification. |
| P3 multi-turn turns | done locally | `26895c7`, `4ac8d2a`, [Milestone 1](#milestone-1-headless-governed-multi-turn-conversation-p1-to-p3) | Real Pi worker per turn (supervisor loop only today). |
| P4 review, cancel, restart | done locally | `41485e4`, `93c359a`, `3f3ae9d`, [Milestone 2](#milestone-2-review-cancellation-and-restart-p4-headless) | Independent review of the driver. Crash in publication untested. Guest-only cancellation. |
| P5 project policy, import/export, Bash | partial | `5a23797`, `cd07351`, `d735ce8`, `29759ae`, `a02f72d`, `ba6f59e`, [Milestone 3](#milestone-3-project-policy-import-export-and-bash-p5) | Pinned Go dev image, no-network real tests, D09 cell runner, service-account isolation check, Bash output review. |
| P6 service and terminal client | partial | `a6ea38b`, `5d81346`, `ae90828`, `7cac628`, [Milestone 4](#milestone-4-product-service-and-terminal-client-p6) | Real two-turn run, SSH from Windows with pinned host keys. |
| P7 provider profiles | partial | `09736c2`, `7656a69`, [Provider profiles](#provider-profiles-and-credentials-p7) | Authorised live exchange, real key, OpenRouter profile (optional). |
| P8 setup, update, diagnostics | partial | `6d2c0e3`, `103f013`, `a46d1d1`, [Milestone 5](#milestone-5-host-setup-updates-and-diagnostics-p8) | Round 3 review, clean-host run, host inputs task, qualified support matrix. |
| P9 web gateway and React live mode | partial | `989881d`, `a5133b5`, `524bdf4`, [Milestone 6](#milestone-6-live-web-chat-p9) | Independent review, real browser against the real service, supervisor-issued delegations. |
| Production host profile (milestone 7) | partial | `7656a69`, [Milestone 7](#milestone-7-production-host-profile-supervisor-loop-worker) | Independent review, qualified host, contained command runner. |
| P10 README and USAGE | not started | none | Whole package. |
| P11 qualification and release | not started | none | Whole package. Needs the guest, host and provider key. |
| Host inputs and soft refusals | done locally | `c6fbd5f`, `b43cce7`, [Resumed 2026-10-27](#resumed-2026-10-27-on-fabian) | Real-host calibration measurement. Soft-refusal review follow-ups (replay recompute test, per-step rehash). |

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

- Product work happens only in the product worktree and branch. Nothing is merged into `main`.
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

These also exist on the thesis baseline `ff374c9`. The thesis thread was told about them. Whether to fix them on `main` is its decision.

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
- **Expiry trigger.** The driver has no timer of its own. `tbound product-serve` added one in P6 (`expiryLoop`, see [Milestone 4](#milestone-4-product-service-and-terminal-client-p6)). A resolve attempt also checks expiry first.
- **Reconciled turns.** A turn reconciled after a restart is billed its full reserved duration. Its receipt has no worker instance or tool list, because the in-process worker's record died with its process.
- **Crash between link and prompt admission.** In that window the turn ends `unknown` and blocks the project.
- **Crash in the middle of publication.** Not tested.
- **Restart after a failed publication.** A publish decision whose publication failed is `unknown` after restart, per the table, even though `failed` could be proven.

What this milestone does not include: project import and export (P5), the terminal client (P6), web chat, installation, and release qualification.

## Milestone 3: project policy, import, export and Bash (P5)

Status: implemented and passing the supervisor gate on `4fe9632`. Not qualified on a host. Bash output and the P5 hardening have not had an independent review. The contract is in [product-contracts.md](product-contracts.md#milestone-3-project-policy-import-and-export-p5).

### What runs today

- **Product scope** (`5a23797`). Rule `product-scope/v1` has `writable`, `read_only` and `protected` lists. Matching is by whole path components, and protected beats read-only beats writable. Typed writes and edits and Bash candidate deltas go through the same `Decide`. A session can be created read-only.
- **Import and export** (`cd07351`, `d735ce8`). `tbound project` builds a bounded bundle from the original project, lists what it includes and excludes, and imports it. Export produces a digest-bound bundle. `tbound project apply` writes a verified patch by default. Direct writes need `--direct` and `--writer-exclusion-held`. Scans refuse symlinks, mounts, ACLs, extended attributes, hidden Git index flags and case variants of `.git`.
- **Bash in turns** (`29759ae`, `a02f72d`, `ba6f59e`). A product turn composes Bash when its installed policy allows it, under the product scope. A Bash turn reopens as a predecessor after its command settlements are verified. The model sees each command's stdout and stderr, bounded to the last 16 KiB per stream and rebuilt from the Store on replay.
- **Tool schemas.** The advertised `read`, `write`, `edit` and `bash` schemas match the executor, and the adapter pins their digests.

### Demonstrated behaviour

| Requirement | Test |
| --- | --- |
| Scope precedence, whole-component matching, both ends of a rename | `TestProductScopeDecide`, `TestStoreProductScopeAdmitsWritableAndDeniesTheRest`, `TestStoreProductScopeChecksBothEndsOfARename` |
| Research digests unchanged | `TestAdmissionPolicyDigestGoldenForTheResearchRules`, `TestScopeFieldsLeaveExistingDigestsUnchanged` |
| A turn enforces the project scope, a read-only session changes nothing, a predecessor reopens under its own scope | `TestProductTurnDriverEnforcesTheProjectScope`, `TestProductTurnDriverReadOnlySessionChangesNothing`, `TestProductTurnDriverReopensAPredecessorUnderItsOwnScope` |
| Import round trip and refusals with path and reason | `TestBundleRoundTrip`, `TestBuildRefusals`, `TestImportRefusals`, `TestProjectBundleRefusalNamesPathAndReason` |
| Export applies to a clean worktree, bad bundles and conflicts are refused, interruption keeps a recovery record | `TestExportApplyRoundTrip`, `TestApplyRefusals`, `TestApplyRefusesBadBundles`, `TestApplyFailureAfterFirstWriteKeepsRecovery`, `TestApplyRecheckBeforeFirstWriteLeavesNothing` |
| Apply defaults to a patch, direct mode needs writer exclusion | `TestSecurityDefaultApplyDoesNotWriteWorktree`, `TestSecurityDirectRequiresWriterExclusion`, `TestProjectApplyDefaultPatchInstructions` |
| Extended attributes, hidden Git flags and `.git` case variants are refused | `TestSecurityRefusesXattrs`, `TestSecurityRefusesHiddenGitFlags`, `TestSecurityGitAdministrationCaseFold` |
| Bash admitted by the product scope, reopened as a predecessor, tampered command evidence refused | `TestProductTurnBashIsAdmittedByTheProductScopeAndReopensAsPredecessor`, `TestProductTurnBashRefusesAPredecessorWithTamperedCommandEvidence` |
| Read-only Bash session writes nothing; a turn with no command runner does not launch; a crash is reconciled | `TestProductTurnBashInAReadOnlySessionChangesNothing`, `TestProductTurnBashRefusesToLaunchWithoutACommandRunner`, `TestProductTurnBashRestartReconcilesALinkedTurn` |
| The model sees bounded command output, replay rebuilds it, a forged output fails, research summaries are unchanged | `TestProductTurnBashShowsTheModelItsCommandOutput`, `TestBashOutputBoundedCommandText`, `TestBashOutputReplayRebuildsFromTheStore`, `TestBashOutputReplayRejectsAForgedOutput`, `TestBashOutputResearchSummaryBytesAreUnchanged` |
| Advertised schemas equal the executor and the adapter pins | `TestReadToolDescriptionMatchesWhatTheExecutorDoes`, `TestAdvertisedToolSurfaceEqualsTheAdapterTextAndItsPins`, `TestResearchToolSurfaceIsFrozen` |

### Verification

`4fe9632` contains every P5 commit above. Its combined gate is recorded under [Handoff 2026-10-26](#handoff-2026-10-26): build, vet and gofmt clean, test exit 0, 5238 pass / 30 skip / 0 fail as reported (raw JSON events 5295 / 33 / 0, which include subtests). Gate results for the individual P5 commits are not recorded.

The mount test `TestSecurityBindMounts` needs root and skips otherwise. It is not verified here.

### Pending and known limits

- **Bash in production.** `product-serve` refuses a policy that allows Bash. Only the development command runner has run Bash. The D09 cell runner and its history opener need the product guest.
- **Output is not filtered.** A command can print any file it can read in its workspace, and the model sees it.
- **Development environment.** The pinned Go toolchain image and module cache do not exist. Real tests with command-cell network disabled have not run.
- **Service-account isolation.** That the service account cannot read the original repository follows from the client doing the reading. It was not checked on a host.
- **Scope.** Only the registration's project scope applies. A narrower per-request scope is not implemented.
- **Direct export.** The client cannot establish writer exclusion. The user guarantees it with a flag.
- **Review.** An independent review of the Bash output work (`a02f72d`) is owed.

## Milestone 4: product service and terminal client (P6)

Status: implemented and passing the supervisor gate on `4fe9632`. The service and client talk to each other only in tests, through fixtures and fakes. Not qualified on a host. The contract is in [product-contracts.md](product-contracts.md#milestone-4-product-service-and-terminal-client-p6).

### What runs today

- **Service** (`a6ea38b`, `ae90828`). `tbound product-serve` listens on a Unix socket, takes the principal from `SO_PEERCRED`, and runs `ReconcileAfterRestart` before serving. One goroutine drives each turn. A review-expiry ticker (`expiryLoop`) calls `ExpireReviews`. Observations come from durable records and are read without writer locks. Snapshots are bounded, and the socket is locked.
- **Protocol** (`dd4da12`). `tbound-product-protocol/v1`: length-prefixed strict JSON, eight operations, stable error codes.
- **Client** (`5d81346`, `7cac628`). `tbound chat` creates or reopens a session, submits prompts under request keys, shows tool activity and denials with hints, and offers Publish, Discard and Keep at review. Ctrl-C cancels once and detaches the second time. Exit codes are 0, 1, 2, 3 and 4.

### Demonstrated behaviour

| Requirement | Test |
| --- | --- |
| Auto turn projects its observations; a review turn publishes its exact candidate | `TestProductServiceAutoTurnProjectsItsObservations`, `TestProductServiceReviewTurnPublishesItsExactCandidate` |
| A second principal cannot see or act on a session | `TestProductServiceSecondPrincipalCannotSeeOrActOnASession`, `TestProductServiceReadsThePrincipalFromTheSocketPeer` |
| Restart reconciles before serving; shutdown lets a turn finish | `TestProductServiceRestartReconcilesBeforeServing`, `TestProductServiceShutdownLetsARunningTurnFinish` |
| The expiry ticker discards an unresolved review | `TestProductServiceExpiryTickerDiscardsAnUnresolvedReview` |
| Cancel while running waits for the settled turn | `TestProductServiceCancelWhileRunningIsWithheldUntilTheTurnSettles` |
| Malformed requests are refused and serving continues | `TestProductServiceRefusesMalformedRequestsAndKeepsServing`, `TestFramesRoundTripAndRefuseMalformedInput` |
| Observation does not compete with the writer, is bounded, and only grows at its end | `TestProductServiceRegressionObservationAndPredecessorDoNotCompete`, `TestProductServiceObserveWindowIsBounded`, `TestProductServiceEventListOnlyGrowsAtItsEnd` |
| Chat shows governance apart from model text and follows the review flow | `TestChatCreatesSessionAndPrintsTurnWithGovernanceDistinct`, `TestChatReviewShowsDiffAndPublishesTheExactCandidate` |
| Transport retries reuse the request key; exhausted retries are an unknown outcome, never a resubmission | `TestChatRetriesTransportFailureWithTheSameRequestKey`, `TestChatExhaustedRetriesAreAnUnknownOutcomeNotAResubmission`, `TestChatRetryTransportCodeKeepsUnknownOutcome` |
| Reconnect replays and submits nothing | `TestChatReconnectReplaysFromTheStartAndSubmitsNothing` |
| First Ctrl-C cancels, second detaches | `TestChatFirstCtrlCCancelsAndKeepsObservingUntilSettled`, `TestChatSecondCtrlCDetachesWithoutCancellingMore` |
| Stable exit codes | `TestChatExitCodeTable`, `TestChatUnknownTurnOutcomeLeavesExit4` |

### Verification

`4fe9632` contains every P6 commit. Its gate is recorded under [Handoff 2026-10-26](#handoff-2026-10-26). Gate results for the individual P6 commits are not recorded.

### Pending and known limits

- **No real run.** No user has completed two real turns through `tbound chat`. The client tests use a scripted fake service (`chatFake`). The service tests use scripted provider fixtures.
- **SSH.** The client has not been run over SSH from Windows with pinned host keys.
- **Worker.** The service runs the supervisor-loop worker. `pi` is refused.
- **Bash.** The service refuses Bash policies (see [Milestone 7](#milestone-7-production-host-profile-supervisor-loop-worker)).

## Provider profiles and credentials (P7)

Status: implemented and passing the supervisor gate on `4fe9632`. No real provider key was read and no paid provider was called.

### What runs today

- **Profile table** (`09736c2`). One profile, `opencode-go-engineering`, with compiled-in endpoint, model and accepted response models. No URL override and no fallback.
- **Credential commands.** `tbound provider list`, `setup`, `status`, `check`, `rotate` and `remove`. Keys come from a no-echo prompt or a caller-owned protected file, never from arguments. The service-owned credential directory is 0700 and the file is 0600.
- **Checks.** `check --offline` uses no network. `check --live` needs `--max-tokens N` and `--yes` and sends one request. The check transport does not follow redirects.
- The service reads the key per turn through the host profile (`7656a69`), so a rotated key needs no restart.

### Demonstrated behaviour

| Requirement | Test |
| --- | --- |
| Setup, status, rotate and remove without disclosing the key | `TestProviderCredentialLifecycle`, `TestProviderCLILifecycleAndOffline`, `TestProviderCredentialRedaction`, `TestProviderCLIExitCodesAndRedaction` |
| Rotation and environment isolation | `TestProviderCredentialRotationAndEnvironmentIsolation` |
| Sources and storage are protected; symlinks and writable ancestors are refused | `TestProviderSourceProtection`, `TestProviderStatusRejectsInsecureStorage`, `TestReadSourceRefusesSymlinkAndWritableAncestors`, `TestProviderCLIProtectedSource` |
| Concurrent setup does not overwrite | `TestProviderConcurrentSetupDoesNotOverwrite` |
| The factory pins the profile identity | `TestProviderFactoryPinsIdentity`, `TestProfileTable` |
| Live check against offline fixtures, network errors, refusal before dispatch, no redirect | `TestProviderLiveCheckOfflineFixtures`, `TestProviderLiveCheckNetworkAndWrappedErrors`, `TestProviderCheckRefusesBeforeDispatch`, `TestProductCheckTransportDoesNotFollowRedirect` |
| The offline test seams are refused outside a test binary | `TestConversationFactoryForTestIsRefusedOutsideATestBinaryAndKeepsTheCredentialRules` |

### Verification

`4fe9632` contains `09736c2` and `7656a69`. Its gate is recorded under [Handoff 2026-10-26](#handoff-2026-10-26). Gate results for `09736c2` alone are not recorded.

### Pending and known limits

- **No live exchange.** The P7 acceptance needs an authorised, bounded call with a real key. It has not happened. Wrong response model, missing usage, expired key and quota exhaustion are untested against a real provider.
- **No price table.** Monetary ceilings are unsupported. Only a token cap bounds the live check.
- **OpenRouter.** No product profile exists. It is optional follow-up work.
- **Real host.** Ownership rules ran against test directories, not the real service account.

## Milestone 5: host setup, updates and diagnostics (P8)

Status: implemented and passing the supervisor gate on `4fe9632`. All of it ran against test roots (`--root`) and fakes. Round 3 of the independent review is owed. The contract is in [product-contracts.md](product-contracts.md#milestone-5-product-host-setup-updates-and-diagnostics-p8).

### What runs today

- **Release trust** (`6d2c0e3`). `tbound release verify` checks an Ed25519 signed manifest and its artifact digests. `install/install.sh` verifies the signature before extracting when it has a public key.
- **Registration tooling.** `tbound product registration keygen`, `init`, `sign` and `verify`. Signing refuses duplicate JSON keys and trailing data (`103f013`).
- **Setup.** `tbound product setup` checks the host, creates the account, directories and unit with exact owners and modes, and is idempotent. `--dry-run` writes nothing.
- **Update, rollback, uninstall.** Update refuses unresolved turns, keeps a last-known-good binary, runs a self-test and rolls back on failure. A retry restarts and re-tests. Uninstall keeps data unless `--purge` is confirmed. Purge refuses a locked catalog.
- **Doctor.** Advisory checks. `--self-test` runs a synthetic turn; `--dry-run` only plans it.
- **Hardening** (`103f013`, `a46d1d1`). Parent directories are pinned, hardlinked targets are refused, managed sources are read through descriptors, purge checks turns without root.

### Demonstrated behaviour

| Requirement | Test |
| --- | --- |
| Tampered artifact refused even when the checksum file is replaced | `TestTamperedArtifactWithReplacedChecksumFileIsRefused`, `TestProductAdminReleaseRefusesTamperingEvenWithAReplacedChecksumFile` |
| The install script verifies before extracting | `TestProductAdminReleaseInstallScriptVerifiesTheSignatureBeforeExtracting` |
| Setup builds the tree, a rerun changes nothing | `TestSetupFreshRootBuildsTheExpectedTree`, `TestSetupRerunIsUnchanged`, `TestProductAdminSetupWritesTheTreeAndARerunIsUnchanged` |
| An unsupported host fails before any change; thesis paths are refused | `TestSetupFailsOnAnUnsupportedHostBeforeAnyChange`, `TestSetupRefusesThesisPathsAndChangesNothing`, `TestProductAdminThesisRootsMatchTheService` |
| Update refuses unresolved work, rolls back on a failed self-test, rolls back on request | `TestUpdateRefusesWhileATurnIsUnresolved`, `TestUpdateRollsBackWhenTheSelfTestFails`, `TestRollbackSwapsBackAndNeedsALastKnownGood` |
| A retried update keeps last-known-good | `TestReviewUpdateRetryStartsAndTestsWithoutReplacingLastKnownGood` |
| Uninstall keeps data, purge asks, a locked catalog blocks it | `TestUninstallKeepsDataAndPurgeNeedsConfirmation`, `TestReviewUninstallFailsClosed`, `TestProductAdminUninstallRefusesLockedCatalog` |
| Only a self-test establishes readiness | `TestDoctorReportsAdvisoryChecksNeverKeysAndOnlyASelfTestEstablishesReadiness`, `TestProductAdminDoctorSelfTestThroughTheServiceSocket` |
| Symlinked parents, hardlinks and unsafe sources are refused | `TestReviewParentSymlinksAndThesisRoots`, `TestReviewSetupRejectsHardlinkedMutations`, `TestReviewUpdateRejectsUnsafeManagedSources` |

### Verification

`4fe9632` contains `6d2c0e3`, `103f013` and `a46d1d1`. Its gate is recorded under [Handoff 2026-10-26](#handoff-2026-10-26). Gate results for the individual P8 commits are not recorded.

### Pending and known limits

- **No clean host.** No clean qualified Linux machine or fresh Windows-to-VM setup has run. Systemd, accounts, cgroups and Podman were never touched for real.
- **Host inputs.** Setup does not install or check the policy files, the Git executable or the calibration. No product command measures or signs the calibration. `product doctor` does not report them. This task is not implemented.
- **Scope of setup.** The contract's setup steps do not install a Pi bundle, an image or a network policy. The supported kernel and systemd matrix is not recorded.
- **Interruption.** Only the update retry path is tested. Interrupted setup and interrupted rollback are not injected. No reboot test.
- **Trust.** A first install trusts a key it has not seen before. No publisher key is distributed yet.
- **Review.** P8 round 3 is owed.

## Milestone 6: live web chat (P9)

Status: implemented. The gateway and the React live mode are in the separate module `extensions/webchat`. They were tested separately from the supervisor gate. Counts are not recorded. Only fakes stand behind the gateway and behind the browser code. The contract is in [product-contracts.md](product-contracts.md#milestone-6-live-web-chat-p9).

### What runs today

- **Gateway** (`989881d`, `524bdf4`). `tbound-webchat --live --socket PATH` binds a loopback address and prints a one-time pairing URL. Pairing sets an HttpOnly, SameSite=Strict cookie and a CSRF token. Every mutation needs the CSRF header. Host and Origin are checked. There is no CORS. Bodies are bounded strict JSON. The gateway holds no authority of its own and forwards the user's UID to the service. On shutdown it force-closes long polls.
- **React live mode** (`a5133b5`). A session picker and creator, a composer with a request key per prompt, cancel, an event stream with resume from a stored cursor, and a diff review whose Publish and Discard are bound to the displayed candidate digest. Fixture, replay and receipt modes stay as labelled demonstrations.
- **Wire parity.** The gateway keeps its own copy of the wire types. The supervisor generates the JSON fixtures that check it (`5068560`).

### Demonstrated behaviour

| Requirement | Test |
| --- | --- |
| Pairing token spent once, expires after 10 minutes, session after 12 hours | `TestPairingSpendsTheTokenOnFirstUseAndSetsAHardenedCookie`, `TestPairingTokenExpiresAfterTenMinutes`, `TestBrowserSessionExpiresAfterTwelveHours` |
| Every request needs the cookie, every mutation the CSRF header | `TestEveryAPIRequestNeedsTheSessionCookie`, `TestEveryMutationNeedsTheCSRFHeader` |
| Host rebinding, foreign origin and CORS are refused | `TestHostRebindingIsRefusedOnEveryRoute`, `TestOriginMustEqualHttpPlusTheHost`, `TestNoCORSAndOptionsIsNotAPreflightAnswer` |
| The gateway never invents a request key or retries; resolve forwards the displayed digest | `TestTheGatewayNeverInventsARequestKeyOrRetries`, `TestResolveForwardsTheExactCandidateDigestTheClientDisplayed` |
| Transport failure marks mutations unknown | `TestTransportFailuresAreServiceUnavailableAndMarkUnknownMutations` |
| An XSS-looking prompt passes as data | `TestAnXSSLookingPromptIsPassedThroughAsData` |
| Non-loopback bind is refused; a real listener and socket pair and forward | `TestNewLiveGatewayRefusesNonLoopbackAndMissingSocket`, `TestLiveRunPairsOverTheRealListenerAndForwardsOverTheRealSocket` |
| Wire copy matches the supervisor fixtures | `TestProtocolFixturesRoundTripThroughTheWireCopy`, `TestFixturesMatchTheCommittedFiles` |
| Browser code (Vitest, in `frontend/src/live`): unknown outcome keeps the draft and retries under the same key | `LiveUi.test.tsx`: "shows an unknown outcome, keeps the draft, and retries under the same request key" |
| A gap recovers from a fresh snapshot; a stale candidate is refused | `controller.test.ts`: "recovers from a gap with a fresh snapshot, and says so"; `LiveUi.test.tsx`: "refuses a stale candidate with the service" |
| A hostile diff is drawn as text | `LiveUi.test.tsx`: "draws a hostile diff as text and builds no element from it" |

### Verification

`989881d` is in the `4fe9632` supervisor gate, but only through the supervisor's `productprotocol` fixtures test. The webchat Go tests and the Vitest suite are a separate module. `5311409`, `a5133b5` and `524bdf4` are not in the `4fe9632` gate. The combined gate on `610577b` covers all of them (see [Status by package](#status-by-package-p0-p11)).

### Pending and known limits

- **Fakes only.** The browser tests run against `liveGateway.ts`, a scripted fake. The Go gateway tests run against a fake product service. No real browser has driven a real governed turn. The P9 acceptance requires that.
- **Authority.** The pairing secret scopes the browser. Supervisor-issued delegations with separate read, submit, cancel and resolve rights are open work.
- **Untested cases.** Service restart in a live page, cursor expiry in a real browser, and the Windows SSH tunnel have not run.
- **Review.** An independent review of the gateway and the React live mode is owed.

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

## Handoff 2026-10-26

- HEAD is `5311409`. The last full supervisor gate was on `4fe9632`: build, vet and gofmt clean, test exit 0, reported 5238 pass / 30 skip / 0 fail. `5311409` adds only the webchat module and the React live mode, which were tested separately. The merged tree has not had one combined gate run.
- P5, P6, P8, P9 and the production host profile are merged. This status document still lacks their sections and the P0-P11 status table required by docs/product-plan.md section 10.
- The host-inputs task (calibration measure and sign, setup checks, doctor reporting) and the soft-refusals task are NOT implemented. Both branches have zero commits beyond `5311409`; the agents were cut off before committing. Restart both from scratch.
- Four independent reviews are owed: P9 gateway and React, Bash output, P8 round 3, and the production host profile. At the last check the second Codex account failed to open a session and needed re-login; the main account had a usage reset at 23:10. The fallback is Opus or Sonnet at xhigh. OpenRouter is paid and limited to the panel and hint experiments.
- Next actions, in order:
  1. Gate `5311409`.
  2. Update this document with the missing sections and the P0-P11 table.
  3. Sync F14 from the current thesis branch head (post-closeout). Expect conflicts in `governed_workflow_live_linux.go`, `recovery_linux.go` and `workflowrecovery`.
  4. Redo host inputs.
  5. Redo soft refusals.
  6. Get the four reviews.
  7. P10.
  8. P11.
- The qualified host, the provider key and the guest stay on FABIAN. The new machine is for code, reviews and docs.
- A machine handoff is in progress. No further edits on this checkout after this commit.

## Resumed 2026-10-27 on FABIAN

- The machine move was cancelled. Work continues on FABIAN, which keeps the qualified host, the guest and the keys.
- Step 1 is done: `610577b` passed the combined gate (see [Status by package](#status-by-package-p0-p11)). Step 2 is this document.
- **F14 sync**, `7ceeff6`: thesis `5f2d0a6` was merged, leaving the product 81 ahead and 0 behind. Only test files conflicted:
  - `governed_ledger_deny_linux_test.go`, `governed_recovery_preview_linux_test.go` and `workflowrecovery/denied_call_exchange_linux_test.go`: the thesis version was taken.
  - `workflow_vertical_linux_test.go`: the product version was kept, because it holds `prepareWorkflowVertical`.
  - `governed_workflow_live_linux.go`, `sessionrepo/recovery_linux.go` and `workflowrecovery/recovery.go` merged cleanly. Their thesis changes are byte-identical ports of the product fixes, and the product's restart-reconciliation pins are kept.
  - Gate: focused tests ok; full gate 5370 passed, 29 skipped, 0 failed. The skip count fell by one because the thesis turned `TestGovernedLedgerDenyReopenChild` into a helper.
- **Host inputs**, `7d903a0` to `c6fbd5f`. These commands were added: `tbound product calibration measure` and `sign`. Static host-input validators with an explicit owner set are shared by serve, setup (before any write) and doctor. The Git pin now applies the same owner list to the file and to every parent directory. An Opus review found 4 issues, and all are fixed. Gate on `c6fbd5f`: 5459 passed, 29 skipped, 0 failed.
- **Soft refusals**, merged in `b43cce7`. In product turns, a tool call is refused softly only when a check before authorization proves it cannot have an effect: a missing read path, a non-regular target, or edit text that is absent or ambiguous. The refusal returns an error and a hint to the model, and the conversation continues. Each refusal is recorded as `provider_proven_unexecuted`, a binding distinct from a DENY retirement, and replay recomputes it. Research mode is unchanged. An Opus review found no hole in authority, effects or counting; its follow-ups are open. Gate on `4ca3fa4`: 5420 passed, 29 skipped, 0 failed.
- **Combined gate** on `b43cce7`: build, vet and gofmt clean; 5509 passed, 29 skipped, 0 failed.
- **The four owed reviews** were done on 2026-10-27, by Sol xhigh with ponytail-review. Every fix was reviewed again before it was merged. Opus stood in when both Codex accounts were limited.
  - **P9 gateway and React:**
    - An unresolved request now keeps its key and body in per-tab `sessionStorage`, and an unknown outcome stays sticky across reload, session switch and dismiss.
    - The candidate bound is the 1 MiB protocol frame (`CANDIDATE_TOO_LARGE`).
    - Round 2 fixed a create race and the resolve Retry notice.
    - Merged in `44f0438`. The frontend now has 85 files and 1429 tests, and the rebuild equals the committed `dist`.
  - **Bash output:** the bash description now follows the executor's output mode, so `serve --pi` gets the exit-only text. Merged in `f2d03ee`.
    - That merge broke the soft-refusal restart replay, which was missing the route mark. The fix is `c898e1a`.
  - **Host profile:**
    - Import provisions the publication fence `lost+found`, and serve validates it.
    - Credentials and root identity are checked before the socket listens.
    - Registration v2 binds `git_digest` and `host_identity_digest`.
    - Receipts move to v2 and list the unverified runtime, image and extension digests.
    - Managed-root exclusivity is checked by a descriptor walk from `/`. Setup and doctor check the registered bindings.
    - Merged in `384a6b0`.
  - **P8 round 3** took three fix rounds:
    - publisher-key pin in `install.sh`, with root ancestry and marker checks for install and uninstall;
    - crash-safe rollback (`RENAME_EXCHANGE` plus a recovery record), and restoration that resumes after a crash;
    - a `setup --replace` fence against unresolved turns;
    - host-input path collision and symlink checks;
    - a self-test that runs as a registered client UID with dropped privileges;
    - boot activation;
    - the installation format, read from a marker in the binary without executing it.
    - Merged in `a964bf1` and `b6493b9`.
  - **Soft-refusal follow-ups:** the replay guards are covered by mutation-checked tests. Live progress recomputes only refusals after the checkpointed prefix, which cut recomputes from 32 to 8 for four refusals; restart and predecessor replay still recompute everything. Merged in `a503308`.
- **Combined gate on `b6493b9`:** build, vet and gofmt clean; 5646 passed, 30 skipped, 0 failed. The extension boundary guard passes.
  - The 30th skip is the new crash helper `TestProductTurnSoftRefusalUnsettledEffectChild`.
  - The webchat module was last gated on `44f0438`: Go race tests ok, frontend 1429 passed. Nothing after it changes `extensions/`.
- **P8 rounds 4 to 6**, merged in `4f9fc76`. The work was reviewed again after every round. The round-6 verdict was "shippable for a local qualified host" under the stated threat model.
  - `tbound product update --binary --manifest --signature` and `setup --binary` verify the binary against a signed release manifest, under the publisher key that setup pins (`release_public_key`).
  - Only the verified staged inode is installed or run.
  - The binary and key directories must be root-owned and not writable by group or others.
  - Staging and the dry-run measurement read at most the declared size plus one byte.
  - A root `install.sh` run requires publisher authentication. It stages below the checked prefix parent, extracts without the archive's owners or modes, and never writes per-user paths.
  - Without a key, setup installs only its own executable, so the first binary must come from an authenticated install.
  - Gate on `48499af`: 5730 passed, 30 skipped, 0 failed. `4f9fc76` differs from it only in this document.
  - Known LOW: a dry-run update hashes the candidate a second time without a bound. A local writer who grows the file can waste CPU and I/O, but nothing is installed or run.
  - Debian's `/usr/local` (2775, group `staff`) is refused until `chmod g-w`. Ubuntu's default passes.
- **Still blocked:**
  - P9 acceptance needs a real Pi worker per turn, and `product-serve` refuses `worker_mode: pi`.
  - Publication does not swap the old object back when it detects a mismatch after `RENAME_EXCHANGE`. This is shared code in `supervisor/internal/publication`. The product now limits writers to root and the service account; the thesis thread owns the shared fix.
  - Not run on a real host: calibration measurement, the privilege drop, `systemctl enable`, `RENAME_EXCHANGE` on the qualified filesystem, a real browser against the real service, and a real provider.
- **Dev dependency:** `npm audit` reports a high finding in the dev-only test DOM `happy-dom`. There are 0 production findings, and the shipped `dist/` is not affected.
- Two risks to track from the 2026-10-26 priorities review:
  - **Shared code diverges.** The product and thesis branches carry ports of the same security fixes. Keep a short ledger of each shared fix: the commit on each branch and the test that covers it. Reconcile before every freeze.
  - **Prompt versioning.** Changing the compiled-in prompts breaks the replay of earlier turns. Define prompt and evidence versioning before the first catalog is deployed.
- **Next:**
  1. Sync the thesis branch again. `codex/tbound-prototype` is now at `3040061`, 106 commits ahead of the F14 sync point. It brings the D09 denied-after-effect settlement and its recovery verification, the amended D35 construct review, and `verify-retained`. Gate the product on the merge.
  2. P10: rewrite README and USAGE.
  3. P11.

  Open product blockers: the real Pi worker per turn, which P9 acceptance needs; and qualification on a real host.
