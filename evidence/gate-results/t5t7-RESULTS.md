# T5 / T7 closeout receipts (2026-10-29)

Repository `trust-boundary`, branch `codex/tbound-prototype`, pushed to `origin` at `02f9fb71a26aadf4eebeb0ff217fe97c374ffff8`.

## Commits on top of the previous pin `1058b09`

| SHA | Scope |
| --- | --- |
| `1955d3c` | feat: T7 step 1, proven no-effect argument refusals released as a bounded `no_effect_refused` result (squash of `t57/t7s1-no-effect`) |
| `74f7188` | feat: T5, seven TM-A injections delivered through tool-read carriers (squash of `t57/t5-carrier`) |
| `f892740` | docs: TASK_STATE for T5 and T7 step 1 |
| `04b5d45` | feat: T7 step-2 executor path class released as no-effect refusals |
| `02f9fb7` | docs: TASK_STATE for the path class and the residual UNKNOWN classes |

The unsquashed working branches `t57/t5-carrier` and `t57/t7s1-no-effect` are kept locally and in `t57-branches.bundle` (this directory). The T7 agent notes are in `agent-notes-t7/`.

## Full gates (`/mnt/f/TBoundAssets/gates-20261027/gate-full.sh`, WSL, Go 1.27.1, `-race -count=1`)

| SHA | Label | Result | Receipts |
| --- | --- | --- | --- |
| `f892740` | `t57g` | TEST_RC=0, 5361 pass, 40 skip, 0 fail; BOUNDARY/FMT/BUILD/VET/WINDOWS OK | `gate-f892740/` |
| `02f9fb7` | `t57h` | TEST_RC=1: 2 loopback-timeout failures (`TestConstructRecomputesFromProducerFiles`, `TestConcurrentArrivalsAreAllJournaled`); both pass in isolation from the same archive | `gate-02f9fb7-r1/` |
| `02f9fb7` | `t57i` | **TEST_RC=0, 5367 pass, 40 skip, 0 fail**; BOUNDARY/FMT/BUILD/VET/WINDOWS OK; one boot id | `final.tar`, `gate-final.txt`, `gate-final.json` |

The 40 skips are the same named set as the `1058b09` gate `pc29h`: 14 pilot static-replay subtests (Node not executable in the gate environment) and 26 guest/root/cgroup/live-provider tests. Skipped tests are unverified by this gate.

## F14 repin

`python -I repin-f14.py 02f9fb71a26aadf4eebeb0ff217fe97c374ffff8 t57i` (log `repin.log`). Candidate `F:/TBoundAssets/recovery-20261015/F14-candidate-20261027/`: `FREEZE_COMMIT` and patch 01's second measurer pair name `02f9fb7`; pre-repin copies in `repin-before/`; hashes in `repin-files.sha256`.

Checks made by the script before writing: HEAD and `origin/codex/tbound-prototype` equal the pin; the archive pax comment is the pin; gate receipts newer than the archive, all marks present, 0 fail, 40 skips; measurer sources unchanged since F13 `6162e3c`; `supervisor/cmd/tbound-profile/main_linux.go` unchanged since `1058b09`.

Bundle inputs changed since `1058b09`: `adapter/src/ipc-transport.ts`, `adapter/src/ipc-transport.test.ts`. F14 therefore expects a new `BUNDLE_DIGEST`, a P2 rebuild, and a real guest runtime swap (`05-runtime-swap.sh`, SWAP=1). Not measured yet.

## Not done here

No freeze, signing, kit build, install, guest operation, panel run or paid provider call. The pin is not a freeze.

## F14 frozen and validated (2026-10-30)

Full result: `F:/TBoundAssets/recovery-20261015/F14/F14-RESULT.md`. Tag `f14-freeze` on origin dereferences to `02f9fb7`. Archive: `f14-archive/f14-freeze.bundle`, `f14-archive/f14-freeze-02f9fb7.tar`, `f14-archive/SHA256SUMS`.
