F35 QUALIFIED: `9165c43657951f7b3db972cfe228b68249312a7e` (`main` after PR #312, tree `3979713e`), tags `f35-freeze` and `f35-corpus-1` ([`tags/`](tags/README.md)), 2026-11-26/27. F35 is the final freeze, F-H1 (thesis D63 and its amendments #70, #71, #72). It replaces F34 ([`../F34/F34-RESULT.md`](../F34/F34-RESULT.md)).

F35 is F34 plus the fixes that F34's research pilot and the F35 real-exchange probe found:
- #304: the re-check of an abandoned earlier request derives its evidence on the route its abandon record binds. F34's research pilot was refused at unit 0 (`prior-workflow-unsettled`).
- #305: the v-stub route admits governed units, for the free research-route check of D63 item 4.
- #310: the V forwarder sends a replayed `reasoning_details` as `reasoning`. The paid real-exchange probe (D63 amendment item 1, read by #71) found that Pi 0.87.1 replays OpenRouter reasoning in that member and that the forwarder refused the request.
- #308: fresh admission ids. Kit #306 and docs and brand #296, #291, #302, #303 are in the tree; evidence #307 too.

#311 (P units in the stub check), #314 (F35.env and the measurer pair) and #313 (the tags record) were merged into dev after the gate and the tags, at 20:49Z. They are not part of F35: tree `3979713e` has no `freezes/F35.env`. The guest chain ran from kit commit `09d4a5fe` (dev after #313, #314 and #311), whose `infra/freeze` differs from `f35-freeze` only in F35.env, the 05 measurer pair, the README line and 34's P units (step 6d). The scripts check that commit (`K/scripts/guest-chain-f35.sh:6`, `K/scripts/guest-34-f35.sh:6`).

The F34→F35 diff by PR is in `K/candidate/f35-diff-by-origin.md` (rev 1) and `K/candidate/f35-diff-by-origin-r2.md` (rev 2 = rev 1 + #310).

Citation roots: `L=evidence/F35/logs`, `K=L/f35-checks`. On `tbound-dev`: `H=/home/tbound/tbh`, `HF=$H/f35`. Driver headers use Windows local time (UTC+1). All other times are Z.

- **Probe (D63 amendment item 1):** on the developer machine, the F35 candidate tree `fcf1cee3` with an uncommitted exchange harness (`K/probe/`). 11 paid requests on a throwaway ledger (cap 1,007,792 µUSD; at most 300,000 bookable), all settled, 0 uncertain, 1,037 µUSD (`K/probe/REPORT.md`). Finding F1 (V `reasoning_details` replay refused) was fixed by #310; F2 (empty text on the tools-disabled call) and F3 (cached prompt pricing) are documented limits. The key was shredded after the probe.
- **Gate:** full race gate g35b on the exact archive of `7cbc9e52` (tree `3979713e`, the F35 build tree): 5077/5077 blobs byte-exact, GATE_RC=0, 7912 pass, 52 skip, 0 fail, 59 packages (`L/gates/gate-g35b.log`, `L/gates/run-g35b.out`). The rev-1 gate g35 (tree `fcf1cee3`) is kept (`L/gates/gate-g35.log`).
- **Reviews:** the review of record for all runtime changes (#304, #305, #310) is Claude Fable 5.2, APPROVE with no blocker under the D63 bar; its nits are documented limits. #310 also had Codex GPT 6 Luna APPROVE. Kit #311, #314 and evidence #313 had Claude Sonnet 5.5 APPROVE (on the PRs).
- **#310 red→green:** red `d8c32380` (4 tests fail), green `7cbc9e52` (tree `3979713e`, the gated tree; all pass under `-race`), `K/red-green/f1-red.out`, `f1-green.out`; the probe's refused body is the test input. No run replayed reasoning against the fixed code, live or on the guest stub: the probe ran on `fcf1cee3` before the fix, and step 7's V unit makes one request. Only the free stub V reruns on the merged dev tree on the developer machine did (`K/probe/stub-r2/`, 3/3 and 4/4 settled).
- **Measurer:** the four measurer files are unchanged in `13ed1193..9165c436` (`git diff --quiet` rc=0). `adapter/`, `packaging/` and `cmd/tbound-cell-entrypoint` (source) are unchanged against F34. BUNDLE_DIGEST `73866d76…` is the same as F23's and F34's. The F23 measurer pair is in kit #314.
- **Local freeze (20:51:09Z to 21:19:51Z, EXIT=0; `L/run-f35-freeze.out`, `L/freeze-driver.txt`):** every step passed. The EVQ capture had `exit_status 0` over 63 packages on commit `9165c436` (`L/console-35-evq-capture.sh.txt:4,11,18,20`).
  - Unchanged against F34: BUNDLE.
  - New from the code change: TBOUND, TBP, IMG, CFG, LOAD_TAR, SRC, and also ENTRYPOINT, TBOUND_RUN, TEST_PROCORACLE and TEST_ENTRYPOINT. `go list -deps` shows that the cell entrypoint, `tbound-run` and the procoracle test import `internal/broker/openrouter`, which #310 changed (`forward.go`). #305's piruntime change is one comment line with the same line count.
- **Guest restore (`K/f35-restore.out`):** checkpoint `pre-F35-restore-2026-11-26` kept (the F34 research-pilot state: research config, `openrouter.key`, genesis ledger `bea5ce06`, service stopped), then `pre-F31-2026-11-20` restored (21:30:36Z). Hold post-check (`K/f35-h1-hold-postcheck.txt`): kernel 7.0.0-38 held, sudo narrowed, only `opencode-go.key` in `/etc/tbound/provider` (D63 item 5), pilot service active.
- **Guest chain part A (21:31:27Z; `K/chainA.out`):** 27 to 32 and the 40 evidence steps passed. G1 completed (tool sequence read, edit, bash, read; `L/guest-30-reg-g1.sh.txt:375`). rp `CHECKS_OK` with 4 bash calls (`L/guest-31-reg-rp.sh.txt:333`), no model refusal. dr `CHECKS_OK` (`L/guest-32-reg-dr.sh.txt:454`). 33 stopped at the abandon token as designed (`L/guest-33-corpus-pilot.sh.attempt1.txt`) and passed on the rerun with `ABANDON_CONFIRM=f2220a3036d0` (`K/run33.out`, `L/guest-33-corpus-pilot.sh.txt`).
- **34 with `RESEARCH_E2E=1` (D63 item 4), PASS (22:42:25Z to 22:46:02Z; `K/partB.out`, `L/guest-34-v-stub-probe.sh.txt`):** checkpoint `pre-F35-34-2026-11-26` taken right before (22:42:11Z).
  - The DR-drill prior `dr-req-20261126T213908Z` is abandoned on the engineering route (6b).
  - B unit `PILOT-CMP-read-02/b-compliant/1`: begin passed with the prior present, 1 call booked and settled in the throwaway ledger, `terminal:completed` (6c).
  - P units `PILOT-ADV-A-01/p-static-replay/1` and `/p-adaptive-replay/1`: admitted with the prior present, `terminal:completed`, nothing booked or sent (6d, #311). P's first run under the research-route config.
  - V unit `PILOT-CMP-read-01` through the forwarder (7, 8): 1 ledger record, settled, tokens 11/3, cost 2 µUSD; `accept passed=False` is expected, because the stub's reply is canned (`L/guest-34-v-stub-probe.sh.txt:197,222`).
  - Forwarder checks on the throwaway ledger (8b): a settled call, a refusal, an upstream failure, a missing usage, a crash during a call and the restart, the stop rule (refused before sending at 734572 < 752029 µUSD); an overage on a second throwaway ledger (8c).
  - The research ledger and files untouched; the throwaway files deleted (`K/vs/`). 40 v-stub passed.
  - `pre-F35-34-2026-11-26` restored afterwards (22:46:41Z), as D63 item 4 requires in every case. That removed the B, P and V admissions and requests.
- **EVQ 22/22** (`L/evq-report/evq.stdout`: passed 22, not_passed 0, unmapped 0). **Routes 4/4** (`L/evq-report/route-report.stdout`: covered 4, complete true). `K/evq35.out`.
- **Static-P re-witness of the 11 drawn pilot cases on F35, PASS.** The walk's carriers unchanged, as on F33 and F34:
  - `pilot-r01` (`94892257`): 11/11 done, 23:32:55Z to 00:15:58Z, exec_failures 0, spent 0. corpus-check exit 0, `calibration-p` and `effect-satisfiable` pass for all 11. Report sha256 `a8b52d01…` (`K/rewitness/pilot-r01/`). The 7 drawn cases in it are injecagent ds:0014:u05, ds:0011:u07, ds:0002:u04 and ds:0018:u03; agentdojo workspace UT21/IT4; aseval:0497; and asb psychological_counselor HiddenDataExtraction:103.
  - `pilot-r02` (`881b6335`): 11/11 done, 00:30:26Z to 01:13:28Z, exec_failures 0, spent 0. corpus-check exit 0, and `calibration-p` and `effect-satisfiable` pass for all 11. Report sha256 `aa4ed845…` (`K/rewitness/pilot-r02/`). The 4 drawn cases in it are injecagent dh:0013:u06, ds:0002:u10 and ds:0012:u12, and agentdojo travel UT17/IT5.
  - The collections were clean: audits verified (rc=0), no leftover container, and the clock offset within +0.27 s before and after each round.
  - Together the two rounds re-witness all 11 drawn pilot cases (`evidence/pilot-walk/cases.json`) on F35. Under D32 item 6, none of them fails its F-H1 witness.

End state (`K/f35-end-guest-state.txt`, read-only, 2026-11-27T01:28:08Z):
- the pilot service and socket are active, and the installed config matches `pilot` (carrier `pilot-r02`, the last carrier round, `K/rewitness/pilot-r02/vals_values.txt`), pinned to F=`9165c436`;
- no service container is left;
- sudo is narrowed;
- **`/etc/tbound/provider/openrouter.key` is absent**, and so are the research files and the v-stub throwaway paths.

The guest stays with this coordinator for the research pilot (the RA steps, a new ledger genesis at US$10 and a new key card; `.local/research-pilot/RP.md`). The guest password file `.local/pilot-walk/secret/tba_pw` (ACL FABIAN\Admin only) was used only inside the chain's and the rounds' sudo windows; it is deleted after the research pilot's last guest step (RA-12).

The binary signatures of the throwaway v-stub registration and profile are not kept under `K/vs/`, because `scripts/check-self-contained.sh` refuses `ed25519` files under `evidence/`. Their verification on the guest is in `K/vs/s3-verify.out` and `K/vs/s4-verify.out`, and the signed digests are in `K/vs/REGDIGEST`, `K/vs/PROFILE_DIGEST` and `K/vs/TD`. The probe harness binary and the key are not kept either; the probe's harness sources and hashes are in `K/probe/`.
