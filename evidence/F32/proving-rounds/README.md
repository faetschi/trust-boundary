# F32 proving rounds: what the development rounds on the final freeze found

F32 (`main` `08f3c3ee`) is the final freeze and F-H1 candidate (`evidence/F32/F32-RESULT.md:3`). Before any H1 run, four development rounds ran on it, as F31's did (`evidence/F31/proving-rounds/README.md`). They are not study results. Times are UTC, 2026-11-21. Each folder has a `RESULT.md` that cites a file and line in this PR, or in this repository at the named commit, for every claim; causes that come from an independent investigation or are inferred are marked as such.

| folder | what it ran | what it showed |
|---|---|---|
| [`round-proc-qual-dev-01/`](round-proc-qual-dev-01/RESULT.md) (round 1a) | construct-witness plan, 13 P units, monitor 300 s | **Halted at 10 of 13.** 9 P units manifested their effects. `QUAL-PROC-01` (a no-reap bash call) ended without a result after 26 s from interval start; no 10-minute stall (consistent with #251; its red-to-green run is not in this PR), and EFF-PROC-01 was `observed`. But the request ended UNKNOWN (`run-end-unclassified`): the production session cannot reconstruct a left-running no-reap settlement. The command container stays up for hours, the abandon preview is not clean, the round stops and `QUAL-PROC-03` is void. |
| [`round-rehearsal-dev-03/`](round-rehearsal-dev-03/RESULT.md) (round 1b) | full 123-unit rehearsal | 123 of 123 `done`; 0 setup and 0 exec failures; no UNKNOWN, no lost oracle, no evidence loss; the guest collection tar verifies and 105 run ids decode; the audits are not kept here. It has no no-reap unit, so it does not touch the defect of round 1a. |
| [`round-research/`](round-research/RESULT.md) (round 2) | install of the research route on OpenRouter, runbook RA-0 to RA-11 | The install went through to the start (frozen profile, ledger genesis, key placement, registration, S3, S4; the RA-8 and RA-10 installs are inferred from verify output). The service then refused to start, by design: a pilot corpus on the research route must pin its D48 draw. No request, no spend; the ledger stays at genesis (inferred: the service never started; no ledger read after RA-11). The first live OpenRouter request is therefore the research pilot (owner step). |
| [`round-soak-dev-01/`](round-soak-dev-01/RESULT.md) (round 3) | 15 units, 3 cases, monitor window 1500 s | 15 of 15 `done`; all P units manifested (WS-02, WS-04, NET-01); all C units `none`; no evidence loss. |

`watch/` has the two guest root-watcher files and their counts. `units.py` and `watch.py` make `units-summary.txt` and `watch/SUMMARY.txt`.

## The finding of round 1a

The replay of a session refuses a no-reap command settlement on purpose (`supervisor/internal/sessionrepo/replay_linux.go:602-607` at `08f3c3ee`, audit #12), and the live path releases a Bash result only after that replay accepts it (`supervisor/cmd/tbound/production_command_receipts_linux.go:342,459-461`). So a no-reap bash call cannot return a result in a production session. This is the reading of an independent investigation; the logs show the refusal text (`round-proc-qual-dev-01/at-unknown/service-journal-qual-proc-01.txt:80-83`) and the container still up (`round-proc-qual-dev-01/podman-poll.txt`, `round-proc-qual-dev-01/at-unknown/podman-ps.txt:2`).

## The owner decision

Keep F32 and amend D45: PROC-01 and PROC-03 are not measurable on F-H1; TM-C becomes DC01, DC02, DC04; no no-reap P unit runs on the H1 guest (decision of 2026-11-21, recorded in the thesis register PR faetschi/MSE_MA_Thesis #43, merged 2026-11-21T11:29:55Z as `0533a18f`). F32 is unchanged and stays the F-H1 candidate. The guest was checkpointed as `post-F32-qual-barrier-2026-11-21` and restored to `post-F32-chain-2026-11-21` (`round-proc-qual-dev-01/f32-restore2.out:3-5`).

Not kept here: raw per-run guest audits (the 69 MB collection of round 1b), the 1.9 MB journal of round 1b (sizes and digests from the local `.local/` copy, not checkable here), service journals, interval snapshots, `requests.jsonl`, all keys (they stay under `.local/` and on the guest). No secret or key value is in these files (checked by search); key and secret-file paths appear in `run.json` and scripts as in F31.
