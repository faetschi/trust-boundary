# F31 proving rounds: why the final freeze moved from F31 to F32

F31 (`main` `cc1de6df`) was sealed and verified as the final freeze and F-H1 candidate (`evidence/F31/F31-RESULT.md:1-3`). Before any H1 run, three development rounds and a second guest chain tested it. They are not study results. Round 1a found a 600 s stall in the F31 runtime, attributed by an independent investigation (`round-proc-qual-dev-01/RESULT.md:44`; PR #251, red test not in this PR) to `settleNoReap`. The fix cannot enter F31, so F31 is not the F-H1 candidate. Times are UTC, 2026-11-20 to 2026-11-21.

| folder | what it ran | what it showed |
|---|---|---|
| [`round-proc-qual-dev-01/`](round-proc-qual-dev-01/RESULT.md) (round 1a, chain 1) | construct-witness plan, 13 P units | **Halted at 10 of 13.** 9 P units manifested their effects. `QUAL-PROC-01` (a bash call that starts a background `sleep`) stalled exactly 10 min and ended UNKNOWN (`run-end-unclassified`). The abandon was refused (owner epoch 1 drain proof missing), so the barrier stays. |
| [`chain2/`](chain2/RESULT.md) | F31 guest chain again, on the guest restored to before chain 1 | Reproduced chain 1: G1, rp, dr pass; EVQ 22/22; routes 4/4; every re-measured value identical. Attempt 1 stopped on a kit zero-byte check (chain 1's aside dir sat inside `$HF`). |
| [`round-rehearsal-dev-03/`](round-rehearsal-dev-03/RESULT.md) (round 1b, chain 2) | full 123-unit rehearsal | 123 of 123 `done-ok`, no UNKNOWN, no loss. It does not use `no-reap/v1`, so it cannot show the defect. |
| [`round-soak-dev-01/`](round-soak-dev-01/RESULT.md) (round 3, chain 2) | 15 units, 3 cases, monitor window 1500 s | 15 of 15 `done-ok`; all P units manifested (WS-02, WS-04, NET-01); all C units `none`; no lost evidence. |

`watch/` has the three raw guest root-watcher logs and their counts (`watch/SUMMARY.txt`).

## The defect and the fix

An independent investigation attributes the stall to `settleNoReap`, which reads the final no-reap proof with an EOF-demanding reader (`supervisor/internal/piruntime/d09_delta_linux.go:250` at `cc1de6df`; `round-proc-qual-dev-01/RESULT.md:44`). On that account Podman keeps the proof pipe's write end open (`--preserve-fds`), so EOF never comes and a no-reap bash call waits for the 10-minute `d09CommandMaxRuntime` (`d09_command_runner_linux.go:40`). The stall in round 1a is 600 s (`round-proc-qual-dev-01/RESULT.md`). The cause comes from an independent investigation; the logs show the 600 s and the missing proof, not the blocked read. Fix: PR #251, red test `bfc441cb`, fix `a84841f8`, merged `0edfe78e`. It changes a Go file under `supervisor/internal/piruntime`, so it cannot enter F31 without a new freeze.

Round 1a also found two host-tool defects in `infra/freeze/tools/recover.sh`, fixed in #248 (`c7b5ec86`, pid from a localized `tasklist` line) and #250 (`6c39d683`, CR of Windows `python` output). They stopped supervision only. No measured value depends on them.

## The owner decision

At the barrier the owner chose (2026-11-20) to fix in dev and test F31 further. The guest was checkpointed (`post-F31-qual-barrier-2026-11-20`), restored to `pre-F31-2026-11-20` and the chain rerun (`chain2/RESULT.md`). Rounds 1b and 3 then ran clean because their carriers do not reach the defect (1b: `round-rehearsal-dev-03/analysis/REPORT.md:45`; the soak: inferred). (The owner decision and checkpoint names are relayed by the coordinator; no repo file.)

## F32 supersedes F31

F31 has the defect, and F31's tree must not change. The fix is in dev (`0edfe78e`), so the final freeze moves to F32, built from dev. **F32 supersedes F31 as the F-H1 candidate.** F31 stays as the record of this evidence. F32 is now frozen and verified (F32 = `main` `08f3c3ee`, evidence in `evidence/F32/`, PR #257); round 1a is planned to rerun on F32.

Not kept here: raw per-run guest audits, the 1.9 MB journal of round 1b, `pack.tar`, all keys (they stay under `.local/` and in the guest receipts). No secret or key is in these files (checked by search).
