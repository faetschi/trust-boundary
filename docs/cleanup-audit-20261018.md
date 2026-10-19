# Historical core cleanup evidence

This records work tested on the old `94378e8` prototype. These core changes are **not in the rebased PR13**. Their unapplied, provenance-preserving patches are on [codex/cleanup-core-deferred](https://github.com/faetschi/trust-boundary/tree/codex/cleanup-core-deferred), based on `2c31881`, for post-freeze/post-pilot review. See the [current scope and verification](quality-review-20261019/README.md). Historical test results below do not qualify that queue on the new architecture.

## Historical removals

`deadcode -test ./...` plus repository-wide caller searches established these 15 declarations as unreachable. Only their implementations and unused imports were removed; no test case was removed.

- `cmd/tbound`: `serveConn`, `governedSessionrepoArgumentDigest`, `(*piServiceRequestJournal).validate`, `piServiceRequestEventID`, `piServiceParentPathIdentity`, `piServiceSocketRegistrationDigest`.
- Uncalled command test helpers: `appendGovernedTestEvent`, `lenMustTrace`, `appendGovernedToolResult`.
- Internal Linux code: `privategit.hashBytes`, `publication.artifactHasStage`, `(*piruntime.D09PodmanCrunLauncher).cleanupCell`, `piruntime.d09ContainerArgumentsWithEnv`, `piruntime.readContainerID`, `piruntime.mustHashProtectedFile`.

The adapter's unused `ProbeProposalReply` variant was removed because its only live sender returns `IpcResult`; `ProposalReply` aliases that type. Active cleanup, CID validation, hashing, launch, cancellation, and refusal paths remain.

## Historical refactors and fixture fix

- Duplicate digest validation, scope copying, and schema factories now use identical existing helpers. Delta copying relies on its unchanged preflight. Tests pin error precedence, nil/empty distinctions, fresh storage/maps, callback mutation isolation, and schema hashes. Original digest validators agreed on 16,398 deterministic inputs.
- Worker-ready admission and diagnostics reuse comparisons. The final 29-case table passes unchanged before/after, including collection edge cases and exact messages.
- Direct managed-root admission tests cover valid control, missing lease, and changed held-directory permissions. Both prior quiescence helper tests remain; direct `AttestQuiescent` needs a larger authenticated fixture.
- The old offline Pi fixture failed because its Bash command writes `task.txt` without a declared output scope. Historical `ec7f816` declares that path; the unchanged regression failed before and passed afterward. This unapplied patch is separate from the pilot corpus, whose deliberately empty `workflow_bash_output_paths` and pinned config remain unchanged.

Independent GPT 6.1-Sol reviews found no blockers. The [archived audit](https://github.com/faetschi/trust-boundary/blob/60456aa80a94245105b429328edcf9dfd1f8fcf2/docs/cleanup-audit-20261018.md) retains per-stage source hashes, baseline reproductions, named test comparisons, and full receipts.

## Historical verification

Pinned WSL environment: Go 1.27.1, Node 24.21.0, official Pi CLI 0.87.1, `umask 077`, private short TMPDIR with checked ancestor modes. Frozen source manifests were rechecked after execution. Full normal/race tests enabled the official offline fixture; adapter checks required official CLI integration.

All commands exited 0; formatting output was empty:

```sh
# supervisor/
go build -mod=readonly ./...
go vet -mod=readonly ./...
gofmt -l .
go test -mod=readonly -count=1 -json ./...
go test -race -mod=readonly -count=1 -json ./...
deadcode -test ./...
deadcode ./...
GOOS=windows deadcode -test ./...
GOOS=windows go vet -mod=readonly ./...
# adapter/: npm ci --ignore-scripts --no-audit --no-fund && npm run check
# root: PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p test_prepare_official_pi_runtime.py -v
```

| Implementation | Normal/race passing records per suite | Skips | Adapter | Python |
| --- | --- | --- | --- | --- |
| `ec7f816`, initial cleanup/fixture fix | 991 | 6 | 17 | 9 |
| `bcd91fe`, equivalent helper consolidation | 1,006 | 6 | 17 | 9 |
| `97402dc`, ready comparisons | 1,036 | 6 | 17 | 9 |
| `0dc8525`, bounded performance cleanup | 1,042 | 6 | 34 | 9 |

Each full gate had 25 passing packages and zero failures. No existing test identity disappeared or changed outcome between these checkpoints. Windows checks were static. Latest frozen tree: `239accb1b57bd33e187349eca2c91d9717432302`; source-manifest SHA256: `c6e2e38f30f74833900180c81e4995aa41e9f940cd0e813ef4f8e960b2fa84a6`. Receipts: `F:/TBoundAssets/cleanup-20261018/quality-final-v2/`. Earlier stages remain under `F:/TBoundAssets/cleanup-20261018/` and `F:/TBoundAssets/cleanup-refactor-20261018/`.

Six unchanged skips remain unverified: `TestLandlockTSYNCReachesEnforcement`, `TestRealProviderDurableLinux`, `TestOpenRejectsUntrustedOwnership`, `TestRealOpenRouterExchange`, `TestD09CellTemplateDraftIsNotAdmissible`, and `TestCgroupLimitsOrExplicitSkip`. A separate reduced-privilege attempt still skipped the scanner case. No eligibility checks were relaxed. GitHub's Go workflow failed before jobs on both [baseline](https://github.com/faetschi/trust-boundary/actions/runs/39045673353) and [earlier cleanup](https://github.com/faetschi/trust-boundary/actions/runs/39142737768); local checks do not establish CI success or production qualification.
