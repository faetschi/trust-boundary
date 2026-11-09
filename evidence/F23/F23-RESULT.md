Fresh F23 verified: `13ed11939c06e552a2d8662fb752bf2b3640ac68` (`main` after #109: dev `d4d75d04` = F22 code plus fresh admission ids #107), 2026-11-08.

F23 is an interim qualification of `main` on the guest for the M3 rehearsal; not F-H1. Since F21 it carries the pre-admission launch fixes (#100): a launch that fails before admission with proven cleanup is settleable by abandon, and a vanished worker cell is diagnosed with podman's exit status, the final container state and per-phase timestamps. F22 (`aa5aed10`, same code) was not qualified: its route probe failed on a provider stream deviation (`evidence/F22/F22-RESULT.md`). F21's rehearsal block (unit 2) is retained in `evidence/F21/rehearsal-u2-diag/`. The guest was restored to `pre-F21-2026-11-08`, so F23's previous freeze is F20.

Citation roots: `L=evidence/F23/logs`; `H=/home/tbound/tbound-h/f23`, `W=/home/tbound/tbound-w.DWUiOo` in `tbound-dev`. Values: `infra/freeze/freezes/F23.env` (PREV_* = the F20 records).

- **Gate:** local full race gate on exactly `d4d75d04` (tree identical to F23): TEST_RC=0, 7383 pass, 50 skip, 0 fail, 60 packages (`L/gate-d4d75d04.log`).
- **Measurer:** `launcher_linux.go`, `host.go`, `profile_tooling_linux.go` unchanged in 2641c6b..13ed119; pair in this kit commit.
- **Local freeze:** bundle unchanged; new TBOUND, TBP, ENTRYPOINT, SRC, TEST_*, LOAD_TAR digests (`L/console-*.txt`); EVQ capture and signing passed.
- **Guest chain:** passed every step (`L/guest-chain-driver.txt`). **G1, rp, dr PASS.**

**EVQ 22/22** (`L/evq-report/`, with the guest netsink `L/guest-netsink-envelope.json`): `not_passed 0`, `unmapped 0`. **Routes complete** 4/4. **B9:** dr abandoned on its facts, pilot registered, trial reset ok, netsink evidence (`L/33-corpus-pilot-driver*.txt`).
