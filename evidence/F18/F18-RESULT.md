Fresh F18 verified: `b26e5efdf8ac04cce185de06d771a558d57b42af` (`main` after #78: dev `ef68dc8a` with #71, #72-#77), 2026-11-07.

F18 is an interim qualification of `main` on the guest, cut so the M3 rehearsal pilot can run after its two F17 blockers. It is not F-H1 and establishes neither H1 readiness nor product release readiness.

Citation roots: `L=evidence/F18/logs`; `H=/home/tbound/tbound-h/f18`, `W=/home/tbound/tbound-w.pWt43B` in `tbound-dev`. Values: `infra/freeze/freezes/F18.env` (PREV_* from the F17 records, all 28 re-verified by 10-freeze and 33). Owner side natively in `tbound-dev` from a Linux checkout of F18; guest side from Git Bash from this kit commit with `TBOUND_WGA` set to a Linux checkout of it.

- **Gate:** GitHub Actions unavailable (billing) until December 2026; owner-approved local full race gate on `00fefae3`, whose `supervisor/`, `adapter/`, `corpus/` and `infra/freeze/` trees equal F18's: BUILD_OK VET_OK WINDOWS_OK FMT_OK BOUNDARY_OK TEST_RC=0 (7234 pass, 50 skip, 0 fail; `L/gate-00fefae3.log`).
- **Includes:** audit cleanup and R1/R2 provider-evidence fixes (#71); `tbound-op abandon` for a started pilot's unknown runner request (#76, M3 blocker 1); fresh admission ids (#77); product docs and web chat UI (#72-#75; not linked into the supervisor binaries). M3 blocker 2 (parallel tool calls) needs no runtime change by owner decision (thesis `h1-path/README.md` decision 13).
- **Measurer audit:** `launcher_linux.go`, `profile_tooling_linux.go` and `construct_evidence_linux.go` have deletion-only non-test changes in 8931c69..b26e5ef, unreachable from `runtime-bundle-digest`; `runRuntimeBundleDigest`, `VerifyLinuxPiRuntimeBundle`, `statDevice`, `requireReadOnlyMount`, `unescapeMountField` and the `host.go` limits are byte-identical (Codex GPT 6.1 Sol import-closure and caller audit); pair in this kit commit.
- **Local freeze:** P2 builds identical; every digest new except `BASH_DIGEST`, `NODE_DIGEST` and the calibration pair, as predicted in the values file (`L/console-*.txt`). EVQ capture passed on idle WSL; signing passed (`S5 signed_manifest_digest == IMGDIGEST`).
- **Hyper-V checkpoint:** `pre-F18-2026-11-07` (standard, like F15-F17; the VM's checkpoint type was set back to production-only).
- **Guest chain:** passed every step on the first attempt (`L/guest-chain-driver.txt`).
- **G1 PASS:** completed, PUBLISHED/SUCCEEDED, run `production-run-1f60cf6072f0376bec7e8c41`, tools read, edit, bash, read (`L/g1-response-extract.json`). **rp PASS, dr PASS.**

**EVQ 22/22** (`L/evq-report/evq.stdout`, `L/evq-report/commands.log`), with the guest netsink artifact from B9 (`L/guest-netsink-envelope.json`, sha256 `934bea10…`): `not_passed 0`, `unmapped 0`. **Routes complete**, covered 4, pending_guest 0, missing 0.

**B9 on F18:** the first run stopped by design at the dr abandon, because the stopped service makes the cgroup and podman probes read `unavailable` (`L/33-corpus-pilot-driver-attempt1.txt`); the facts were admissions 1, scope bound and drained, none undrained, so the rerun used `ABANDON_CONFIRM` (`L/33-corpus-pilot-driver.txt`). Pilot registration and profile signed and installed; corpus service running; one real trial reset succeeded; netsink evidence produced.

Not done at the time of this record: the M3 rehearsal pilot on F18 (fresh run, owner decision 2026-11-07), corpus witnesses, preregistration, H1.
