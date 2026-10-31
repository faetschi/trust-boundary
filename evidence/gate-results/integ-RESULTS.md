# Product integration and main consolidation receipts (2026-10-30)

`main` is the canonical branch at `41f69a62821a9fb029c08ac117883e64e916f0bb`, pushed; the GitHub default branch is `main`. Its tree `4264dbdd235db0ac1190183fd471574f801da318` is identical to gated `9b217146f37b581df9b101cdcd6d1fb056746e2e`.

This is a local verification of the integrated tree. It is **not** a guest qualification of that tree. F14 validation applies only to `02f9fb7` (tag `f14-freeze`, `F:/TBoundAssets/recovery-20261015/F14/F14-RESULT.md`).

## Commits from F14 to main

| SHA | Scope |
| --- | --- |
| `3fd0e90` | merge `feature/tbound-product` (`c793ef3`); 9 conflicted files |
| `8790eba`, `33a3574`, `8c70066`, `da5c5dd`, `56324ec`, `5430f40`, `da5e5d0` | integration fixes (`ee58996` was a saved WIP step) |
| `c686cf2` | public-tree cleanup: 285 paths removed, archive `F:/TBoundAssets/repo-internal-docs-20261030/` |
| `9b21714` | EVQ capture gets the gate's 40-minute test timeout |
| `41f69a6` | merge into `main`; main's only unique commit `7be261b` kept as an ancestor |

## Verification

| Check | Result | Receipt |
| --- | --- | --- |
| Full gate `ig30` at `9b21714` | TEST_RC=0; 6355 pass, 45 skip, 0 fail; BOUNDARY/FMT/BUILD/VET/WINDOWS OK | `gate-final.txt`, `gate-final.json`, `final.tar` |
| Webchat Go, `-race` | pass | run at `9b21714` |
| Adapter typecheck and tests | rc 0; 51 pass, 1 skip | run at `9b21714` |
| Frontend typecheck and Vitest | rc 0; 85 files, 1429 tests pass | run at `41f69a6` |
| Per-package race results at `da5e5d0` | 61 pass, 3 no tests | `agent-evidence/round3-report.md` |
| Independent review of `3fd0e90..9b21714` | no P0-P2; one P3 (host metadata in `infra/hyperv`) | reported to the owner |

Skips: the 40 F14 names plus 5 product ones (`TestProductTurnBashCrashChild`, `TestProductTurnCrashChild`, `TestProductTurnSoftRefusalUnsettledEffectChild`, `TestSetupLiveRunsAccountsOwnersAndReloadInOrder`, `TestSecurityBindMounts`). Skipped tests are unverified. The pinned Pi CLI adapter integration test and the browser `ui-qa.mjs` run were not run.

## Preserved

- Unmerged branches: `unmerged-local-branches.bundle` (this directory) and the branches themselves. PRs #16 and #17 stay open.
- T5/T7 working branches: `F:/TBoundAssets/t5t7-20261029/t57-branches.bundle`, plus local labels.
- Agent evidence: `agent-evidence/`, `F:/TBoundAssets/t5t7-20261029/agent-notes-t7/`, `working-notes/`.
- The owner's `AGENTS.md` and `TASK_STATE.md`: untracked and ignored in the checkout; copies in `F:/TBoundAssets/t5t7-20261029/owner-local/`.
