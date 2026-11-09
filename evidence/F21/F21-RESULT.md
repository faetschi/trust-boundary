Fresh F21 verified: `bbc6ec85a0e2f4895c6c9e392b1736a1968d62b8` (`main` after #96: dev `61022b14` with #92, #94, #95), 2026-11-08.

F21 is an interim qualification of `main` on the guest for the M3 rehearsal. It is not F-H1 and establishes neither H1 readiness nor product release readiness. It carries the D49 fix from the F20 rehearsal (unit 1): the empty registered `net-sink` cgroup no longer refuses an early close, and a refusal that only leaves the early close unproven keeps the full horizon without extra evidence loss (#94; register D49 amended 2026-11-08).

Citation roots: `L=evidence/F21/logs`; `H=/home/tbound/tbound-h/f21`, `W=/home/tbound/tbound-w.RlN5MJ` in `tbound-dev`. Values: `infra/freeze/freezes/F21.env` (PREV_* = the F20 records).

- **Gate:** local full race gate on exactly `61022b14` (tree identical to F21): BUILD_OK VET_OK WINDOWS_OK FMT_OK BOUNDARY_OK TEST_RC=0, 7368 pass, 50 skip, 0 fail, 60 packages (`L/gate-61022b14.log`).
- **Measurer:** `launcher_linux.go`, `host.go` and `profile_tooling_linux.go` unchanged in 2641c6b..bbc6ec8 (`git diff --quiet`); pair in this kit commit.
- **Local freeze:** bundle and entrypoint unchanged from F20; new TBOUND, TBP, SRC, LOAD_TAR digests (`L/console-*.txt`). EVQ capture passed on idle WSL; signing passed. Hyper-V checkpoint `pre-F21-2026-11-08`.
- **Guest chain:** passed every step on the first attempt (`L/guest-chain-driver.txt`). **G1 PASS** (completed, PUBLISHED, `L/g1-response-extract.json`), **rp PASS, dr PASS.**

**EVQ 22/22** (`L/evq-report/`), with the guest netsink artifact (`L/guest-netsink-envelope.json`): `not_passed 0`, `unmapped 0`. **Routes complete**, covered 4, pending_guest 0, missing 0.

**B9 on F21:** dr abandoned on its facts (admissions 1, scope drained, none undrained; `L/33-corpus-pilot-driver-attempt1.txt`, `L/33-corpus-pilot-driver.txt`); pilot registered, trial reset ok, netsink evidence produced.
