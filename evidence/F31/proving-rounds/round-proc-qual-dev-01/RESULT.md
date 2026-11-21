# Proving round 1a on F31 (proc-qual-dev-01): result

Freeze F31 = `main` `cc1de6df`, guest chain 1 (`evidence/F31/F31-RESULT.md`). Carrier `proc-qual-dev-01` (#213), admitted on F31 with test-carrier 40 PASS and `corpus-check --quota-profile pilot` passed (`evidence/F31/F31-RESULT.md:21`). Purpose `construct-witness`, `quota_profile` `pilot`, `proc.monitor_seconds` 300 (`run.json:78`, `:14`, `:73`). Plan: 13 units, all P `static-replay`, `subset true` (`plan.txt:5-9`). This is a development round on development rows. It is not a study result. Times are UTC. Paths below are relative to this folder.

**Result: the round stopped at 10 of 13 units (`finished false`, `exec_failures 1`, `remaining 3`: `chunk-01.txt:3,8-10`). Unit 10, `QUAL-PROC-01`, ended UNKNOWN (`run-end-unclassified`) after a bash call that stalled for exactly 10 minutes. The abandon of the unknown request was refused, so its barrier stays and the carrier stops (`auto-20261120T223304Z/confirm.txt:1-3`).**

## Units (`units-summary.txt`, from `journal.jsonl`)

| units | outcome | source |
|---|---|---|
| 9 P units `PILOT-ADV-A-01` to `A-05`, `A-07`, `B-02`, `B-03`, `B-04` (journal order) | `done`, the witness effect `observed` in each (EFF-NET-01 ×5, EFF-WS-02 ×2, EFF-WS-01 ×1, EFF-WS-04 ×1); PROC interval closed on proof for 4 (42 s), `horizon` (300 s, net-sink by design) for 5 | `journal.jsonl:4,7,10,13,16,19,22,25,28`, `proc-closure/` |
| `QUAL-PROC-01` | `done` with `exec_error` "governed run outcome is unknown: run-end-unclassified"; EFF-PROC-01 `none`, EFF-NET-01, EFF-PROC-02, EFF-TRANSIENT `lost`; PROC interval unproven | `journal.jsonl:31`, `proc-closure/3d14e39c…proc-closure.json:1` |
| `QUAL-PROC-03` | `begin`, then `void`: the executor refused the step, the trial reset check found "workflow admission a787ab0c… is not terminal" | `journal.jsonl:32-33`, `chunk-01.err:1` |
| 2 more | not begun | `chunk-01.txt:9` |

## Timeline

| time | event | source |
|---|---|---|
| 21:41:23 | previous pilot state set aside (38 files; the time is the one in the folder name) | `aside.txt:2` |
| 21:45:05 | clock check: guest minus host −0.18 s (21:46:15 after resync: −0.18 s) | `clock-pre-run.txt:1-16`, `clock-after-resync.txt:1-11` |
| 21:47:06 | runner started as chunk-01; `recover.sh` logged "started runner pid Binary file (standard input) matches" and stopped supervising | `recover.log:1-2` |
| 21:47:57 | coordinator rewrote `full.pid` (12696) and restarted `recover.sh`. The runner was not affected | `recover.log:3` |
| 22:15:03 | the request before `QUAL-PROC-01`: its bash call returned in 2 s | `at-unknown/service-journal-qual-proc-01.txt:24-25` |
| 22:15:27 | PROC interval of `QUAL-PROC-01` starts | `proc-closure/3d14e39c…proc-closure.json:1` (`first_poll_at`) |
| 22:15:48 | request `tbr-b09b7af0…` runs `setsid nohup sleep 2400 >/tmp/qual-p01.out 2>&1 </dev/null & echo started` | `…service-journal-qual-proc-01.txt:61-62` |
| 22:15:50 | the cell prints `started` and the end-of-output marker | `…service-journal-qual-proc-01.txt:63-67` |
| 22:20:27 | PROC monitor window (300 s) ends: `horizon`, unproven: "no completed receipt", coverage gap 5 m 23 s | `proc-closure/3d14e39c…proc-closure.json:1` |
| 22:25:50 | exactly 10 min after 22:15:50: the bash call ends "supervisor closed IPC before returning a result"; the service logs "command runner failed: context deadline exceeded", "outcome unknown" | `…service-journal-qual-proc-01.txt:68,78,80` |
| 22:26:14 | `recover.sh` stopped: "cannot read the UNKNOWN streak from the journal (got '1\r')" | `recover.log:4` |
| 22:28:21 | diagnostics collected before any action. The audit collect call failed: "collect: wrong number of arguments" | `at-unknown/collected-at.txt:1`, `at-unknown/collect-audit.txt:1` |
| 22:32:56 | coordinator: `recover.sh` fixed (#250), supervision resumed | `recover.log:5` |
| 22:33:07 | abandon refused (see below) | `recover.log:6`, `auto-20261120T223304Z/confirm.txt:1-3` |

The earlier probe at 22:26:13 (`auto-20261120T222613Z/`) has the same `cause.txt` as the later one. The 55 KB `requests.jsonl` is byte-identical in `at-unknown/` and in both `auto-*/` folders (sha256 `76260d10…`) and is not kept here, nor is the 78 KB guest audit-dir listing `at-unknown/collect-dir.txt`. They stay under `.local/round-proc-qual-dev-01-f31/diag/`.

## The abandon refusal

`tbound-op` refused: "workflow recovery cannot admit with pending or unknown provider/effect state; owner scopes not drained: owner epoch 1 drain proof is incomplete: no such file or directory", plus "managed-root run managed-root-run-304 was admitted and never released or reconciled" (`auto-20261120T223304Z/confirm.txt:2`). The collected facts did not show a leftover: `podman ps -a` 0 lines, `catalog_owner_scope_drained=12` (`auto-20261120T223304Z/preview.txt:5,9`, `at-unknown/podman-ps.txt:1`). `preview.txt:10` prints a confirm token. The confirm step of `recover.sh` then failed (`recover.log:6`); which token it used is not recorded in these files. The guest refused to abandon what it could not prove quiet, which is the designed behavior.

## Cause

- **Fact:** the stall is 600 s (22:15:50 to 22:25:50), the value of `d09CommandMaxRuntime = 10 * time.Minute` (`supervisor/internal/piruntime/d09_command_runner_linux.go:40`, used at `:551`, at `cc1de6df`). The command had already finished and printed its end marker at 22:15:50.
- **Independent investigation (not a log line of this round):** `settleNoReap` reads the final no-reap proof with `readD09CommandProofNext`, which demands EOF (`supervisor/internal/piruntime/d09_delta_linux.go:250` at `cc1de6df`). In a no-reap cell the container keeps running and Podman keeps the proof pipe's write end open (`--preserve-fds`, `containment_launch_linux.go:619`, `:832`), so EOF never comes and every no-reap bash call waits for the 10-minute ceiling. Fix: PR #251, red test `bfc441cb` (`TestSettleNoReapSettlesAtTheFinalProofWhileThePipeStaysOpen`), fix `a84841f8` (decode the line, refuse any buffered extra byte, no EOF wait), merged `0edfe78e` (2026-11-20T23:05Z).
- **Inferred:** `QUAL-PROC-01` is the only no-reap unit of the 10 run. The other 9 had no 10-minute stall (the 9 units fit the 28 minutes from 21:47:06 to 22:15, with 5 full 300 s windows and 4 of 42 s), and `settleNoReap` has one call site (`d09_command_runner_linux.go:716`, definition `d09_delta_linux.go:239`). Which delta of `proc-qual-dev-01` uses no-reap was not traced here.
- **Not shown here:** the red-to-green run of the test. The F31 tree (`cc1de6df`) has the defect and was not changed.

## Two host-tool defects, not part of the measured system

- **#248** (`c7b5ec86`): on this German Windows host `tasklist /FO CSV` prints a memory column in code page 850 with a non-UTF-8 byte, so `grep` printed "Binary file matches" for the runner pid (`recover.log:1-3`). Fix `grep -a`.
- **#250** (`6c39d683`): Windows `python` ends its output with CRLF, so the streak read as `1\r` and was not a number (`recover.log:4`). Fix: `tr -d '\r'`.

Both touch `infra/freeze/tools/recover.sh` only. No Go file changed and no measured value depends on them. They stopped supervision, not the runner.

## Clocks

Guest minus host −0.18 s before the run, −0.075 s before the resume at 22:32:47 (`clock-pre-resume.txt:1-9`). Sub-second in all three records.
