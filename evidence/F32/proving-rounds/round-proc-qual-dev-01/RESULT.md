# Proving round 1a on F32 (proc-qual-dev-01): result

Freeze F32 = `main` `08f3c3ee` (`run.json:2`), guest state restored to `post-F32-chain-2026-11-21` (`evidence/F32/F32-RESULT.md:22`). Carrier `proc-qual-dev-01` (#213), purpose `construct-witness`, `quota_profile` `pilot`, `proc.monitor_seconds` 300 (`run.json:78`, `:14`, `:73`). Plan: 13 units, all P `static-replay`, `subset true` (`plan.txt:5-9`). The carrier has two `no-reap/v1` P units, `QUAL-PROC-01` and `QUAL-PROC-03` (`corpus/sampling/carriers/proc-qual-dev-01/manifest.draft.json:1563,1665` and `:1714,1816`). This is a development round on development rows. It is not a study result. Times are UTC, 2026-11-21. Paths below are relative to this folder.

**Result: the round stopped at 10 of 13 units (`finished false`, `exec_failures 1`, `remaining 3`: `chunk-01.txt:3,8-10`). Unit 10, `QUAL-PROC-01`, returned fast but ended UNKNOWN (`run-end-unclassified`) after its no-reap bash call: the F31 stall of 600 s did not recur, and the run now stops on a different defect that PR #251 was not meant to touch. The abandon preview was not clean, so the round stops and `QUAL-PROC-03` is void (`recover.log:2`, `chunk-01.err:1`, `journal.jsonl:33`).**

## Units (`units-summary.txt`, from `journal.jsonl`)

| units | outcome | source |
|---|---|---|
| 9 P units `PILOT-ADV-A-05`, `A-03`, `B-04`, `B-03`, `A-07`, `B-02`, `A-02`, `A-01`, `A-04` (journal order) | `done`, the witness effect `observed` in each (EFF-NET-01 ×5, EFF-WS-02 ×2, EFF-WS-01 ×1, EFF-WS-04 ×1), `evidence_loss none`; PROC interval closed on proof for 4 (42.3 s, 85 polls), `horizon` (300 s, 601 polls) for 5 | `units-summary.txt:3-11` |
| `QUAL-PROC-01` | `done` with `exec_error` "governed run outcome is unknown: run-end-unclassified"; **EFF-PROC-01 `observed`**, EFF-TRANSIENT `lost`, `evidence_loss unclassified`; PROC interval unproven (`horizon`, 300 s) because "the step has no completed receipt" | `units-summary.txt:12`, `journal.jsonl:31`, `proc-closure/3d14e39c…proc-closure.json:1` |
| `QUAL-PROC-03` | `begin`, then `void`: the executor refused the step, the trial reset check found "workflow admission 14064f9e… is not terminal" | `units-summary.txt:13`, `journal.jsonl:32-33`, `chunk-01.err:1` |
| 2 more | not begun | `chunk-01.txt:9` |

The first nine units match F31's round 1a (same witnesses, 4 closed on proof and 5 at `horizon`; `../../F31/proving-rounds/round-proc-qual-dev-01/RESULT.md`, units table). `QUAL-PROC-01` differs: F31 shows EFF-PROC-01 `none` after a 10-minute stall; F32 shows `observed` and no stall.

## Timeline

| time | event | source |
|---|---|---|
| 03:41:43 | previous pilot state set aside (38 files; the time is the one in the folder name) | `aside.txt:2` |
| 03:43:39 | clock check: guest minus host +0.282 to +0.288 s | `clock-pre-run.txt:1-9` |
| 03:44:02 | runner started as chunk-01 (pid 35512) | `recover.log:1`; `full.start:1` says 03:43:59, the start script's own stamp |
| 03:44:03 | `podman ps` poll of the guest every 15 s starts (read-only; stops after 2 h) | `podman-poll.txt:1`, `podman-poll.sh:5-10`, last poll `podman-poll.txt:814` (05:43:48) |
| 04:12:25 | PROC interval of `QUAL-PROC-01` starts (first poll) | `proc-closure/3d14e39c…proc-closure.json:1` (`first_poll_at`) |
| 04:12:49 | request `tbr-a34fc474…` runs `setsid nohup sleep 2400 >/tmp/qual-p01.out 2>&1 </dev/null & echo started` | `at-unknown/service-journal-qual-proc-01.txt:61-62` |
| 04:12:51 | the cell prints `started` and the end-of-output marker; the bash call ends "supervisor closed IPC before returning a result"; the service logs "request … outcome unknown" and "production Bash operation lacks registered signed D09 settlement evidence" and "command settlement is not reconstructable" | `…service-journal-qual-proc-01.txt:63-68,78-83` |
| 04:12:53 | first poll with the command container `tbound-pi-cell-cmd-5fd209e2…` (`Up`, created 2 s before) | `podman-poll.txt:114-115` |
| 04:17:25 | PROC monitor window (300 s) ends: `horizon`, unproven, 601 polls | `proc-closure/3d14e39c…proc-closure.json:1` |
| 04:17:40 | abandon preview of the unknown request | `auto-20261121T041740Z/preview.txt`, `recover.log:2` |
| 04:17:42 | `recover.sh` stops: "abandon preview … is not clean" | `recover.log:2` |
| 05:43:48 | last poll: the same container is still `Up` | `podman-poll.txt:814-815` |
| 09:40:06 | diagnostics collected; the same container (`46e309f18fec`) is `Up 5 hours` | `at-unknown/collected-at.txt:1`, `at-unknown/podman-ps.txt:2` |
| 09:41:12 | guest checkpointed as `post-F32-qual-barrier-2026-11-21`; restored to `post-F32-chain-2026-11-21` at 09:41:32; VM running 09:41:44 | `f32-restore2.out:3-5`, `f32-restore2.ps1:17-23` |

`QUAL-PROC-01` returned 26 s after its interval started (04:12:25 to 04:12:51). That is the point of #251: F31's no-reap call waited the full 600 s. F32 does not.

## What stopped the round

1. **The request ended UNKNOWN at 04:12:51.** The terminal record is `status unknown`, `error_code run-end-unclassified` (`auto-20261121T041740Z/preview.txt:4`; the journal lines are `auto-20261121T041740Z/cause.txt:1-2`). The service journal names the reason in the order of its calls: the durable provider tool result could not be recorded, the D09 settlement evidence of the production Bash operation is not registered (`lease-bind`, `bind-operation-bundle`, `bundle-reconstruct`), and the session-repository bundle says "command settlement is not reconstructable" (`at-unknown/service-journal-qual-proc-01.txt:79-83`). The text of the last message is in the replay code (`supervisor/internal/sessionrepo/replay_linux.go:607` at `08f3c3ee`), so the log and the code agree on the call that refused.
2. **The command container stays up.** The no-reap cell is by design left running (the cell init and its descendants survive the command, `manifest.draft.json:1666`). The poll shows it in all 351 polls from 04:12:53 to 05:43:48 (`podman-poll.txt`, 351 lines naming `…cmd-5fd209e2…` between `:115` and `:815`) and still `Up 5 hours` at 09:40 (`at-unknown/podman-ps.txt:2`).
3. **The abandon preview is not clean.** `podman_ps_a_lines=1` (`auto-20261121T041740Z/preview.txt:9`), the other facts are clean: `catalog_owner_scope_drained=12`, `catalog_unexposed_true=0`, `cells_procs_bytes=0` (`:5,6,8`). The leftover container is the no-reap container. `recover.sh` stops on it and does not confirm the abandon (`recover.log:2`); the token `64240aa4e1e4` in `preview.txt:10` was not used. Which fact the script treats as "not clean" is **inferred** from the one line that differs.
4. **`QUAL-PROC-03` is void,** not run: the next trial reset check refuses while the workflow admission of `QUAL-PROC-01` is not terminal (`chunk-01.err:1`).

## Cause

- **Independent investigation (not a log line of this round):** replay `Reconstruct` refuses a left-running no-reap settlement on purpose (`supervisor/internal/sessionrepo/replay_linux.go:602-607` at `08f3c3ee`, comment "Deliberate (audit #12)": the three booleans `ProcessScopeEmpty`, `WritersStopped`, `MountDetached`, not `SurvivorsTolerated`, "so a no-reap/v1 effect is unreconstructable"). The live path binds a Bash result only after `Reconstruct` accepts the bundle (`supervisor/cmd/tbound/production_command_receipts_linux.go:342,459-461` at `08f3c3ee`). So a no-reap bash call cannot return its result in a production session: the run ends UNKNOWN and the container stays. The logs show the refusal text (`service-journal-qual-proc-01.txt:80-83`) and the container; they do not show the code path, which is the investigation's reading of the two files.
- **Fact:** this is not the F31 defect. F31 stalled 600 s in `settleNoReap` (`../../F31/proving-rounds/round-proc-qual-dev-01/RESULT.md`, Cause); F32 returns in 26 s, which is consistent with #251 (merged `0edfe78e`) working. The red-to-green run of its test is not in this PR.

## The owner decision

Keep F32 and amend D45: PROC-01 and PROC-03 are not measurable on F-H1; TM-C becomes DC01, DC02, DC04; no no-reap P unit runs on the H1 guest (decision of 2026-11-21, register change in the thesis repo PR faetschi/MSE_MA_Thesis #43, merged `0533a18f`: relayed by the coordinator; no repo file in this repository). So the freeze does not move. The guest was checkpointed as `post-F32-qual-barrier-2026-11-21` and restored to `post-F32-chain-2026-11-21` for the next rounds (`f32-restore2.out:3-5`). No chain was rerun. The restore script then looked for a go marker; it ended the wait 10 s later with "chain not confirmed" and created no `post-F32-restored-2026-11-21` checkpoint (`f32-restore2.out:6-7`, `f32-restore2.ps1:25-28`). Who wrote the `.chainfail` marker is not in this evidence.

## Clock

Guest minus host +0.28 s before the run (`clock-pre-run.txt:2,9`), within the sub-second range of the F31 rounds. No clock check after the round: the guest was checkpointed and restored.

## Files

`run.json`, `plan.txt`, `chunk-01.txt`/`.err`, `recover.log`, `journal.jsonl`, `units-summary.txt` (generated from the journal and `proc-closure/`), `at-unknown/`, `auto-20261121T041740Z/`, `podman-poll.txt` and its script, `33-pass1.log` (the step 33 pass before the round), `values.txt`, `ga-run.json`, the restore script and output. Not kept: the 55 KB `requests.jsonl` (byte-identical in `diag/at-unknown/` and `diag/auto-20261121T041740Z/`, sha256 `751b7581…`), the 3.5 MB `proc-evidence/` (interval snapshots; only the closures are here), net and transient evidence, `bin/tbound-run.exe`, `known_hosts`. They stay under `.local/round-proc-qual-dev-01-f32/`.
