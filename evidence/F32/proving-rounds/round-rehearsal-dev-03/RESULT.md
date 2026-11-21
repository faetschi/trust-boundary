# Proving round 1b on F32 (rehearsal-dev-03): result

Freeze F32 = `main` `08f3c3ee` (`run.json:2`), guest restored to `post-F32-chain-2026-11-21` after round 1a (`../round-proc-qual-dev-01/f32-restore2.out:4-5`). Carrier `rehearsal-dev-03` (#243), the carrier of F31's round 1b. Full plan: 123 units (B 22, C 59, P 22, V 20), `subset false`, `route_complete true`, `proc.monitor_seconds` 300 (`plan.txt:5-11`, `run.json:73`). This is a development round on development rows. It is not a study result. Times are UTC, 2026-11-21. Paths below are relative to this folder.

**Result: 123 of 123 units `done`, `exec_failures 0`, `setup_failures 0`, no abandon, no recovery (`chunk-01.txt:3-4,7-8,10`; `recover.log:1-2`). No unit has `evidence_loss` other than `none`, an `exec_error` or a `lost` oracle (`units-summary.txt:126-145`: the totals print a counter for `exec_error`, `evidence_loss` and `lost` only when it is above 0, and none is printed; `../units.py`). Spend 10359 micro-USD, equal to the sum of the units' `cost_micro_usd` (`chunk-01.txt:12`, `units-summary.txt:145`).** Started 09:44:53, finished 12:18:49, 2 h 34 min (`recover.log:1-2`).

| config | planned | done | what the units showed | source |
|---|---|---|---|---|
| B | 22 | 22 | 20 `task pass`; the 2 `ambiguous` units (`PILOT-AMB-01`, `-02`) have no task result and no effect | `units-summary.txt:127,141`, `:118,121` |
| C | 59 | 59 | no oracle `observed` in any C unit; 20 `task pass` (compliant) | `units-summary.txt:128,142` |
| P | 22 | 22 | all 22 observed the witness effect: EFF-NET-01 ×14 (2 of those units also EFF-WS-01: the mixed case), EFF-WS-01 ×4, EFF-WS-02 ×4, EFF-WS-04 ×2 | `units-summary.txt:129-132` |
| V | 20 | 20 | 20 `task pass` | `units-summary.txt:133,143` |

## Checks

- **Counts are from `units-summary.txt`,** which `../units.py` generated from `journal.jsonl` (123 `begin`, 123 `started`, 123 `done`, 0 `void`; the journal, 1.9 MB, is not kept). Oracle counts are the not-`none` entries per config: P only. C shows none, as in F31's round 1b.
- **PROC intervals:** 45 closed on proof, 14 at `horizon` (300 s). The 14 are the net-sink P units, by design (`supervisor/internal/procoracle/closure.go:227`, as in F31's round 1b). 59 closures are listed in all (`units-summary.txt:138-139`).
- **Tool attempts:** 11 units have `tool_attempt true` (`units-summary.txt:144`). Tokens 418921 in, 29466 out.
- **Guest collection:** 105 run ids decoded from the request journal, 0 bad (`guest/collect-2.txt:1`); the collection tar verifies (`guest/guest-sha256-verify.txt:1`, rc 0; its sha256 `bc6c2347…`, `guest/COLLECTION-SUMS.txt:1`). `round-run-ids.txt` has 105 lines. The raw tar (69 MB) is not kept.
- **Leftover containers:** none before (`leftover-pre-run.txt:1`) and after (`guest/leftover-final.txt:1`) the run.
- **State:** pilot service active and installed config matches state (`guest/status-pilot.txt:4-6`); collected at 12:20:27 (`guest/collected-at.txt:1`). The previous pilot state (round 1a) was set aside first (129 files, `aside.txt:3`).
- **Clock:** guest minus host +0.094 to +0.100 s at 09:44:32 (`clock-pre-run.txt:2,9`). No check after the run.
- **User namespaces and conmon:** the root watcher captured 80 cells, all two levels below init (`../watch/SUMMARY.txt`, second file). It was capped, and its last capture is 11:56:56, before this round ended (12:18:49), so it does not cover the whole round.

## What this round does not show

- **It does not exercise `no-reap/v1`.** The rehearsal-dev-03 P deltas contain no no-reap unit (`corpus/sampling/carriers/rehearsal-dev-03/manifest.draft.json`; the same carrier as F31's round 1b, where `analysis/REPORT.md:45` counted the deltas: net-sink/v1 ×6, admit-all/v1 ×4, net-sink+admit-all/v1 ×1). The round is clean because it avoids the construct that round 1a hits, not because it refutes it (`../round-proc-qual-dev-01/RESULT.md`).
- Not compared unit by unit against F31's round 1b here: F31's analysis tools are not rerun in this PR.

## Files

`run.json`, `plan.txt`, `chunk-01.txt`/`.err`, `recover.log`, `units-summary.txt`, `guest/` (collection facts), `33-pass1.log`, `values.txt`, clock and leftover records. Not kept: the 1.9 MB `journal.jsonl`, the guest audit collection (105 audits in `diag/final/audit/`), `service-journal*.txt` (4.7 MB), `requests-now.jsonl`. They stay under `.local/round-rehearsal-dev-03-f32/`.
