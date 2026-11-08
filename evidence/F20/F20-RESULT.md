Fresh F20 verified: `2641c6bc73a501ee1578d6dcdbe652c6086fac53` (`main` after #91: dev `4820df01` with #82 D49, #83, #84, #85, #86, #88, #89, #90), 2026-11-08.

F20 is an interim qualification of `main` on the guest for the M3 rehearsal. It is not F-H1 and establishes neither H1 readiness nor product release readiness. F19 (`b5ed5b0d`) was cut but not qualified (`evidence/F19/F19-RESULT.md`); the guest was restored to `pre-F19-2026-11-07`, so F20's previous freeze is F18.

Citation roots: `L=evidence/F20/logs`; `H=/home/tbound/tbound-h/f20`, `W=/home/tbound/tbound-w.AakMtM` in `tbound-dev`. Values: `infra/freeze/freezes/F20.env` (PREV_* = the F18 records). Owner side natively in `tbound-dev` from a Linux checkout of F20; guest side from Git Bash from this kit commit with `TBOUND_WGA` set to a Linux checkout of it.

- **Gate:** owner-approved local full race gate on exactly `4820df01` (tree identical to F20): BUILD_OK VET_OK WINDOWS_OK FMT_OK BOUNDARY_OK TEST_RC=0, 7359 pass, 50 skip, 0 fail, 60 packages (`L/gate-4820df01.log`).
- **Includes since F18:** D49 proof-based early close of the observation window (#82), transport failure class (#83), B3 witness mode (#80), B7 stage 1 corpus schema (#81), launch-exit diagnosis (#89), D48 ledger data (#88), cleanup (#84, #85), fresh admission ids (#90).
- **Measurer:** `launcher_linux.go`, `host.go` and `profile_tooling_linux.go` unchanged in b26e5ef..2641c6b (`git diff --quiet`); the runtime-bundle-digest call graph is byte-identical; pair in this kit commit.
- **Local freeze:** P2 builds identical, bundle unchanged from F18; new TBOUND, TBP, ENTRYPOINT, SRC, IMG, CFG, LOAD_TAR, TEST_* digests (`L/console-*.txt`). EVQ capture passed on idle WSL; signing passed.
- **Guest chain:** passed every step on the first attempt (`L/guest-chain-driver.txt`). **G1 PASS** (completed, PUBLISHED/SUCCEEDED, `L/g1-response-extract.json`), **rp PASS, dr PASS.**

**EVQ 22/22** (`L/evq-report/`), with the guest netsink artifact from B9 (`L/guest-netsink-envelope.json`): `not_passed 0`, `unmapped 0`. **Routes complete**, covered 4, pending_guest 0, missing 0.

**B9 on F20:** first run stopped by design at the dr abandon (`L/33-corpus-pilot-driver-attempt1.txt`); facts admissions 1, scope bound and drained, none undrained; rerun with `ABANDON_CONFIRM` (`L/33-corpus-pilot-driver.txt`): dr abandoned, pilot registered, trial reset ok, netsink evidence produced.

Not done at the time of this record: the fresh M3 rehearsal on F20, corpus witnesses, preregistration, H1.
