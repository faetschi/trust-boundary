Fresh F17 verified: `8931c69576c2cb58b0c87d8a4ec0e88dd2c162cc` (`main` after #67, #64, #66, #68, #69), 2026-11-06.

F17 is an interim qualification of `main` on the guest (M2, repeated after the M3 pilot blockers were fixed). It is not F-H1 and establishes neither H1 readiness nor product release readiness.

Citation roots: `L=evidence/F17/logs`; `H=/home/tbound/tbound-h/f17`, `W=/home/tbound/tbound-w.Qoutr0` in `tbound-dev`. Values: `infra/freeze/freezes/F17.env` (PREV_* from the F16 records). Owner side natively in `tbound-dev`; guest side from Git Bash from kit commit `4237d307` (PR #70) with `TBOUND_WGA` set to a Linux checkout of that commit.

- **Gate:** GitHub Actions unavailable (billing) until December 2026; owner-approved local full gate on `c37a85d2` (tree identical to F17): BUILD_OK VET_OK WINDOWS_OK FMT_OK BOUNDARY_OK TEST_RC=0 (7153 pass, 50 skip, 0 fail).
- **Includes:** net-sink `cgroup.kill` delegation and two lifecycle fixes (#67), streamed `tool_calls: null` parsing (#64), netsink UDP attribution under the maintenance lock (#66), static replays on the T5 scripts (#68), fresh admission ids (#69).
- **Measurer audit:** `launcher_linux.go`, `host.go`, `profile_tooling_linux.go` unchanged in 97738b6..8931c69; pair in the kit commit.
- **Local freeze:** P2 builds identical, bundle unchanged from F16; new `TBOUND`, `TBP`, `SRC`, `IMG`, `CFG`, `LOAD_TAR` digests (`L/console-*.txt`). EVQ capture passed on idle WSL; signing passed.
- **Hyper-V checkpoint:** `pre-F17-2026-11-06`.
- **Guest chain:** the first attempt stopped at 27 before moving anything (`L/guest-chain-driver-attempt1.txt`): the F16 corpus pilot had reset the live root to a case fixture, which 27 did not expect. 27 now treats a live root left by the previous freeze's pilot (`$PREV_HF/pilot/TRIAL_RESET_OK`) as evidence to archive instead of judging it as the g1 fixture (kit `1c67fe0e`, `4237d307`, Codex-reviewed). The rerun passed every step (`L/guest-chain-driver.txt`).
- **G1 PASS:** completed, PUBLISHED/SUCCEEDED, run `production-run-7a3e3c0e564490b696f5c7e2` (`L/g1-response-extract.json`). **rp PASS, dr PASS.**

**EVQ 22/22** (`L/evq-report/evq.stdout`, `L/evq-report/commands.log`): after B9 (corpus mode, a successful real trial reset), `guest-evidence netsink` was accepted for the first time (`L/guest-netsink-envelope.json`, sha256 `5cb01b85…`), and the EVQ report with that artifact passes all 22 obligations. This is a rehearsal on an interim freeze; the claim-bearing 22/22 belongs to F-H1. **Routes Complete**, 4/4.

**B9 on F17:** `dr` abandoned on request-specific evidence (`abandoned-unknown`); pilot registration and profile signed and installed; corpus service running with the strict L2 check passing; `trial reset --check` returns a check document; one real reset succeeded.

Not done at the time of this record: the M3 rehearsal pilot on F17, corpus witnesses, preregistration, H1.
