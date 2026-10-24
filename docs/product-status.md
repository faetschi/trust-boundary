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

Status: in progress.

| Package | State |
| --- | --- |
| P0 baseline | Done locally; product runtime pending |
| P1 contracts | [product-contracts.md](product-contracts.md) |
| P2 product admission and accounting | Not started |
| P3 isolated multi-turn turns | Not started |

What this milestone does not include: review and cancellation (P4), project import and export (P5), the terminal client (P6), web chat, installation, and release qualification.
