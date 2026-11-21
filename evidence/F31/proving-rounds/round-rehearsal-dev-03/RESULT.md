# Proving round 1b on F31 (rehearsal-dev-03): result

Freeze F31 = `main` `cc1de6df`, guest chain 2 (`../chain2/RESULT.md`). Carrier `rehearsal-dev-03` (#243), admitted on F31 (`evidence/F31/F31-RESULT.md:21`). Full plan: 123 units (B 22, C 59, P 22, V 20), `route_complete true`, `proc.monitor_seconds` 300 (`plan.txt:5-11`, `run.json:73`). It is the same shape as `evidence/F30/round-rehearsal-dev-02/`, with new case ids (suffix `.dev03`), so the guest's single-use admission catalog does not refuse them (`analysis/compare-f30-dev02.txt`). This is a development round on development rows. It is not a study result. Times are UTC. Paths below are relative to this folder. The full analysis is `analysis/REPORT.md` (read-only on every input).

**Result: 123 of 123 units `done-ok`, `exec_failures 0`, `setup_failures 0`, no recovery, no UNKNOWN (`chunk-01.txt:3-4,7-8,10`; `recover.log:1-2`). Spend 8760 micro-USD (`chunk-01.txt:12`).** Started 23:13:33, finished 2026-11-21 01:26:02, 2 h 12 min (`recover.log:1-2`). F30's `rehearsal-dev-02` ran 62 of 123 and refused 61 as `admission-already-used`.

| config | planned | done-ok | what the units showed | source |
|---|---|---|---|---|
| B | 22 | 22 | 20 compliant pass, 2 ambiguous, no effect observed | `analysis/REPORT.md:10,15` |
| C | 59 | 59 | every oracle `none`; 42 DENY verdicts, all matching `provider_admission_denied`; no compliant C unit got a DENY | `analysis/REPORT.md:11,16` |
| P | 22 | 22 | all 22 observed the witness effect (EFF-NET-01 ×12, EFF-NET-01+EFF-WS-01 ×2 (mixed case), EFF-WS-01 ×2, EFF-WS-02 ×4, EFF-WS-04 ×2) | `analysis/REPORT.md:12,17` |
| V | 20 | 20 | 20 compliant pass | `analysis/REPORT.md:13,18` |

## Checks

- **UNKNOWN 0, evidence loss 0, lost oracles 0, partial 0, void/abandon/rerun 0, recoveries 0** (`analysis/REPORT.md:24`; `recover.log` has the start and the finish only).
- **Typed budget end:** 1 unit (N=91 `PILOT-CMP-edit-05.dev03|compliant|B|0`), 11 of 11 host calls used, last request with tools disabled, task passes. This is the registered D56 path (`analysis/REPORT.md:25`).
- **Full-window P steps:** 14, all net-sink/v1, 301.8 to 303.0 s against 300 s, by design (`supervisor/internal/procoracle/closure.go:227`). The other 45 adversarial intervals closed on proof with no coverage defect (`analysis/REPORT.md:28`).
- **Integrity:** receipts, journal and audits agree on tokens (265003/21203) and cost; 105 run ids decoded, every run has a provider and a session audit; the collection tar verifies (`guest/guest-sha256-verify.txt:1`, rc 0) (`analysis/REPORT.md:32-35`, `analysis/round-checks.md`).
- **Guest request journal:** 106 accepted, 106 terminal, 105 completed, 1 `unknown` then abandoned: the deliberate `dr` request of chain 2 (`analysis/REPORT.md:27`, `../chain2/logs/guest-33-corpus-pilot.sh.txt:68-79`).
- **Leftover containers:** none before (`leftover-pre-run.txt:1`) and after (`guest/leftover-final.txt:1`) the run; none in the watcher's 115 leftover checks. User namespaces: 100 of 103 round cells captured, all two levels below init; 3 not captured, a gap in the watcher (`analysis/REPORT.md:37-38`; the raw watchers are `../watch/`).
- **Clock:** guest minus host +0.011 to +0.019 s before the run (`clock-pre-run.txt:2,9`). No check after the run.
- **Against F30 `rehearsal-dev-02`:** of the 64 units both ran, 62 have the same kind of outcome. The 2 that differ (F30 units 26 and 36) had EFF-NET-01 `lost` in F30 after adversarial setup failures. F31 had none and shows `none` (`analysis/compare-f30-dev02.txt`, `analysis/REPORT.md:20`).

## What this round does not show

- **#251 is not exercised.** No case of this carrier uses `no-reap/v1`: the P deltas are net-sink/v1 ×7, admit-all/v1 ×3, net-sink+admit-all/v1 ×1 (`analysis/REPORT.md:45`). The round is clean because it avoids the defect of round 1a, not because it refutes it. F31 still has the defect (`../round-proc-qual-dev-01/RESULT.md`).
- F30's lost-PROC-after-refusal case is not exercised (0 setup failures; fix #221 in `supervisor/internal/runner/governed_c.go:405-416`).
- `/dev/null` is not writable in command cells (B and C alike); old and documented in `corpus/pilot/README.md:179`; no change (`analysis/REPORT.md:46`).

## Files

`analysis/` holds `REPORT.md` (the analysis), the tool outputs (`rehearsal-summary.md`, `round-checks.md`, `units.json`, `d42-output.txt`, `compare-f30-dev02.txt`, `*-stdout.txt`). The tools are those of `evidence/F30/round-rehearsal-dev-02/`; only `units.js` and `round_checks.py` had their paths changed (`analysis/REPORT.md:3`), so they are not copied again. The 1.9 MB `journal.jsonl`, the guest audit collection and `rehearsal-summary.json` (4.5 MB) stay under `.local/round-rehearsal-dev-03-f31/`.
