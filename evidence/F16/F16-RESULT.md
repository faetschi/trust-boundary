Fresh F16 verified: `97738b60125b77be41c4800c7ce1d0fb327557f6` (`main` after PR #58), 2026-11-06.

F16 is an interim qualification of `main` on the guest (plan M2 repeated after the settlement fix #56). It is not the H1 build (F-H1) and establishes neither H1 readiness nor product release readiness.

Citation roots: `L=evidence/F16/logs`; `H=/home/tbound/tbound-h/f16`, `W=/home/tbound/tbound-w.SENtYC` in `tbound-dev`. Values: `infra/freeze/freezes/F16.env` (PREV_* from the F15 records, re-verified by `10-freeze.sh`). Owner side natively in `tbound-dev` from a Linux checkout at the frozen commit; guest side from Git Bash from kit commit `173526d3` (PR #62) with `TBOUND_WGA` set to a Linux checkout of that commit.

- **Gate:** GitHub Actions is unavailable (billing) until December 2026; the owner approved merging on the local full gate instead. `tools/gate.sh` on `1d29cc60` (tree identical to F16), 2026-11-06: BUILD_OK VET_OK WINDOWS_OK FMT_OK BOUNDARY_OK TEST_RC=0 (7130 pass, 50 skip, 0 fail).
- **Measurer audit:** `launcher_linux.go`, `host.go`, `profile_tooling_linux.go` unchanged in 2a6492d..97738b6 (`git diff --quiet`); pair in `173526d3`.
- **Local freeze:** P2 builds byte-identical; `BUNDLE_DIGEST` unchanged from F15 (`sha256:5104ec0e…`), so 05 does not swap the runtime. New `TBOUND_DIGEST=sha256:44f63cc2…`, `TBP_DIGEST=sha256:fe11ddd1…`, `SRC_DIGEST=sha256:670fbee9…`, `IMGDIGEST=sha256:5b7ce434…`, `CFG=sha256:271e0c02…`; `BASH_DIGEST`, `NODE_DIGEST` unchanged (`L/console-*.txt`).
- **EVQ capture:** passed, idle WSL. **Signing:** `signed_manifest_digest == IMGDIGEST`; `LOAD_TAR_DIGEST=sha256:add650b3…`.
- **Hyper-V checkpoint:** `pre-F16-2026-11-06` (standard checkpoint; production checkpoints fail on this running Linux VM, the type was set back to ProductionOnly).
- **Guest address:** `172.26.25.5`, by MAC and the pinned host key.
- **Guest chain:** 27 archived the F15 state (including F15's unsettled `dr` workflow); upgrade, R1 re-verify, I1/I2 passed; sudo window closed.
- **G1 PASS:** completed, success, `read/edit/bash/read`, PUBLISHED/SUCCEEDED, run `production-run-102123b7879ed267d8fbfd27` (`L/g1-response-extract.json`). The kit's new check found the G1 workflow `terminal:completed` immediately after the run (the #56 settlement fix, live).
- **rp PASS, dr PASS** (`L/guest-31-reg-rp.sh.txt`, `L/guest-32-reg-dr.sh.txt`).

**EVQ 21/22**, only `EVQ-NETWORK-SINK-GUEST` open (`L/evq-report/evq.stdout`). **Routes Complete**, 4/4 (`L/evq-report/route-report.stdout`). Adapter capture passed first time (npm cache copied from P2's `cache-master` up front).

**After the chain (B9 corpus mode, kit `ff106156`):** the F16 `dr` request was abandoned on request-specific evidence (`abandoned-unknown`); F15's leftover pilot install was archived to `/root/ga/archive-pilot-f15-20261106T093414Z` by the new `pilot-reset` phase; the F16 pilot was signed and installed and the corpus service started (strict L2 passed). `tbound trial reset PILOT-CMP-read-01 --check` now returns a check document (it was refused on F15), and one real reset succeeded (`TRIAL_RESET_OK`).

**Finding, not an F16 failure:** `guest-evidence netsink` was refused: the UDP probe got `write: operation not permitted` (EPERM from the nftables drop of a locally generated packet), while `guestevidence/netsink.go:98` expects `sent`. Validator assumption error; fixed on dev for the next freeze. `EVQ-NETWORK-SINK-GUEST` stays open on F16; the claim-bearing 22/22 belongs to F-H1.

Not done: rehearsal pilot (M3), corpus witnesses, preregistration, H1.
