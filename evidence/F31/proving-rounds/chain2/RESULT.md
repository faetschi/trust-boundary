# F31 guest chain 2: result

After round 1a halted on the barrier, the owner chose (2026-11-20) to fix in dev and test F31 further instead of stopping (relayed by the coordinator; no repo file records it yet). Chain 2 is the F31 guest chain run again on a guest restored to its state before chain 1, so that the carriers of rounds 1b and 3 run on a fresh admission catalog. The F31 build is unchanged: `main` `cc1de6df`. The host freeze steps (10 to 40 sign) ran once, for chain 1; chain 2 repeats only the guest steps. Times are UTC. Paths are relative to this folder. Chain 1 is in `evidence/F31/logs/` and `evidence/F31/F31-RESULT.md`.

**Result: chain 2 reproduced chain 1. G1, rp and dr pass, step 33 passes on the rerun with the abandon token, EVQ 22/22, routes 4/4, and every value that the chain measures again is byte-identical to chain 1.** The values that differ are run ids, tokens, timestamps and the digests of the result files that embed them.

## Timeline

| time | event | source |
|---|---|---|
| 22:55:33 | restore script starts on VM `TBound-Ubuntu-2404-ABI8`, MAC `00155D0C4002` checked | `f31-restore2.out:2`, `f31-restore2.ps1:12-14` |
| 22:55:55 | checkpoint `post-F31-qual-barrier-2026-11-20` taken (chain 1 with the QUAL-PROC-01 barrier) | `f31-restore2.out:3` |
| 22:56:29 | restored `pre-F31-2026-11-20` (F23 installed, H1 hold applied); `Start-VM` returned 22:56:43 | `f31-restore2.out:4-5` |
| 22:57:43 | chain attempt 1 starts | `guest-chain-driver.chain2-attempt1.txt:1` |
| 22:58:42 | **attempt 1 stops at step 27** (`STOP rc=1 at 27-archive-state.sh archive`) | `guest-chain-driver.chain2-attempt1.txt:9-10` |
| 22:59:24 | chain attempt 2 starts; 27 finds "already archived" and passes | `guest-chain-driver.txt:1-8` |
| 23:08:04 | chain stops at 33 for the abandon token, as designed and as in chain 1 | `guest-chain-driver.txt:100-108` |
| 23:08:21 | 33 rerun with the token; `rc=0` at 23:09:46 | `run33-f31.out:1,10`, `logs/guest-33-corpus-pilot.sh.txt:66-69` |
| 23:10:17 | checkpoint `post-F31-chain2-2026-11-20` taken | `f31-restore2.out:7` |

## Attempt 1: the zero-byte check

Step 27 ended in `FATAL: 0-byte output file(s) under /home/tbound/tbound-h/f31 (WSL disk stall?)` and listed five files under `chain1-20261120/` (`logs/guest-27-archive-state.sh-archive-attempt1.tail.txt:3-8`, `guest-chain-driver.chain2-attempt1.txt:3-7`). They are legitimate empty files of chain 1 (three `*.stderr`, two empty audit files). `aside-chain1.sh` had moved chain 1's owner-side records into `$HF/chain1-20261120` (`aside-chain1.sh:4-5`), inside the tree that the check scans. The guest-side archive of 27 had already run (`/root/ga/archive-f23-20261120T225804Z`, `guest-chain-driver.txt:5-6`). Fix: chain 1's records were moved out of `$HF`. The move is **inferred** from where they are now (`/home/tbound/tbound-work/f31-chain1-20261120`, `compare-chain1-chain2.txt:1`); the script that did it is not retained. Chain 1's receipt logs were set aside as `receipts/F31/logs.chain1-20261120` (`aside-chain1.sh:10-11`) and are the ones in `evidence/F31/logs/`.

## Chain 2 vs chain 1

| item | chain 2 | source |
|---|---|---|
| steps 27, 00, 05, 10, 20, 50, 00 close, 29, 30, 40, 31, 40, 32, 40 | all `DONE` | `guest-chain-driver.txt:8,15,22,25,36,40,50,57,64,71,78,85,91,99` |
| 05 runtime swap | pinned F23 tbound-profile; "no swap needed" | `logs/guest-05-runtime-swap.sh.txt:11,17` |
| 20 R1 | `R1-MODE` `reused-from-f23`; `CAL_RO_DIGEST` and `CAL_RW_DIGEST` equal the F23 records | `logs/guest-20-r1.sh.txt:61,73,79` |
| 50 i1/i2 | "live root: no xattr names, no errors" | `logs/guest-50-i1-i2.sh.txt:138` |
| G1 | request `g1-req-20261120T230234Z` `completed` | `logs/guest-30-reg-g1.sh.txt:375` |
| rp | `rp-req-20261120T230455Z` `completed`, `CHECKS_OK` | `logs/guest-31-reg-rp.sh.txt:288,333` |
| dr | unknown by design, `CHECKS_OK` | `logs/guest-32-reg-dr.sh.txt:454` |
| 33 | dr request `dr-req-20261120T230654Z` abandoned on its facts; `CYCLE_OK`; trial reset; sudo closed (`sudo -n true` fails) | `logs/guest-33-corpus-pilot.sh.txt:68-79,313,316-318,349` |
| EVQ | `passed 22`, `not_passed 0`, `unmapped 0`; `evq_capture`, `test_json`, `mapping_sha256` equal chain 1's | `evq-report/evq.stdout:2-3,27-30` |
| routes | `cells 4`, `covered 4`, `complete true`; `matrix_digest` equal | `evq-report/route-report.stdout:3,5-6,9` |
| adapter capture | `exit_status 0`; adapter tree, `node_modules` and package-lock digests equal (`output_sha256` and `artifact_sha256` differ, as the test output embeds timings: **inferred**) | `evq-report/adapter-capture.stdout:4,6,9-10` |
| guest root watcher | 59 cells two levels below init, conmon in `tbound-pi.service/runner`, 0 container lines in 67 leftover checks | `../watch/SUMMARY.txt` (second file) |

`compare-chain1-chain2.txt` compares `tbound-work/f31-chain1-20261120` with `tbound-h/f31` by SHA-256. All 19 top-level files are identical: the calibrations and their signatures, `BASE_TD`, `LANDLOCK_DIGEST`, `CGROUP_ROOT_DIGEST`, `XATTR_*`, `CONTAINERS_CONF_DIGEST`, `STORAGE_CONF_DIGEST`, `d09-cell.base.json`, `xattr-measure-runtime.json`, `R1-MODE`. Chain 1 had 706 files; 569 are identical, 84 differ and 53 do not exist in `tbound-h/f31` (its T5 pilot state, kept under `pilot.t5-20261120T214123Z`). The 84 that differ are run ids, request journals, per-run guest evidence, `evq_results.json` and `route_report.json`. The result digests differ for that reason: `results_sha256` `0d67bac6…` (chain 2) against `121fbc0d…` (chain 1) and `route_report_sha256` `a8478b62…` against `f29a2b3e…` (`evq-report/evq.stdout:31`, `route-report.stdout:10`; rounds 1a and 1b/3 carry them in their `run.json`/journal). The comparison was taken on 2026-11-21 after round 3, so `pilot/` is the soak carrier's state.

**What this means:** the measured guest values (calibrations, template, cgroup root, landlock, xattr) are stable across two clean installs of the same F31 build. The defect of round 1a is therefore not a guest-state accident of chain 1: chain 2 uses the same build and the next round that uses no-reap would stall the same way (**inferred**; rounds 1b and 3 do not use no-reap, so chain 2 did not test it).

## Not done

- No guest contact in this PR's work. No key, password or token is in these files (checked by search; the chain script that reads the sudo secret file is not copied).
- `10-upgrade` and `27` raw logs of chain 2 are not all copied: `10-upgrade.log` (23 KB), the 50 KB attempt-1 log of 27 and the `00-session-sudo` logs stay in `tbound-work/receipts/F31/logs/`.
