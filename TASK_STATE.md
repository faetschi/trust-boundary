# TBound thesis task state

Thesis lane only. Checkpoint 2026-10-27. Product work is tracked in
[docs/product-plan.md](docs/product-plan.md) (section 10), not here. History:
git log of this file, `../review-artifacts/TASK_STATE-history-20261023.md`,
`../review-artifacts/TASK_STATE-pilot20-receipts-20261024.md`.

## Summary

The engineering core works on a real guest (F13 installed 2026-10-24); the thesis has no results yet.
Research is at step 2 of 8 (pilot review); step 3 (fresh freeze + G1) is done at F13. H1 is unexecuted; tests, fixtures,
probes and pilot reports are not thesis results.

## Implementation state

- F14 content (merged, not frozen; full gate on f1d5b75: 4776 pass / 0 fail / 26 guest-only skips):
  - The service-defect fixes (892cc00).
  - The BL-2 fix (c7ceb84): nft 1.0.9 omits the socket level from `-j`; measured offline with the guest's exact nft packages, the fixture is in `piruntime/testdata/nft/`.
  - `corpus-check --guest-evidence` (f1d5b75), after 3 review rounds (Sol xhigh) and 1 orchestrator review. PASS needs recomputation from producer outputs:
    - the runner journal and retained NET and WS inputs (workspace oracle inputs are now retained);
    - `trial accept` docs bound to commit, `/proc/self/exe` digest, cell image and corpus, with the tree digested from the private evaluated copy;
    - imported JSON that is free of duplicates and case-fold collisions.
    The no-flag pilot report is byte-identical. Stated limit: the evidence is operator-retained with no external anchor, so a deliberate consistent forgery is out of scope.
- Service-defect details: DENY continuation fixed with ledger-backed tests (exact next-request denial + static hint, retry after DENY, ALLOW/DENY/ALLOW, fresh-process owner replay); ALLOW/executed calls remain non-retirable. Retained-Store preview now indexes exactly `governed_operation_authorized` and the profiled gate-policy digest, with fresh-process effect recovery and negative evidence tests. No old-kind compatibility: source history and archived review artifacts show only `authorized` writers; `authorization` appears in draft prose/comments and the broken reader. Both failures reproduced before their fixes; final clean-export gate recorded in the implementation handoff.

- Branch `codex/tbound-prototype`. Last guest freeze **F13 = 6162e3c** (installed 2026-10-24, owner-approved; results `F:/TBoundAssets/recovery-20261015/F13/F13-RESULT.md`). Previous: F12 = eed7722 (retained evidence).
- **F13 fresh results:** G1 completed (read, edit, bash, read; published, settled at the rp admission), route probe completed + CHECKS_OK, dr `unknown` by design + CHECKS_OK; runtime swapped to bundle `6a07a8a9…`, new image loaded, session sudo closed again; all six guest evidence artifacts carry commit 6162e3c; **EVQ 21/22** (only `EVQ-NETWORK-SINK-GUEST` open, needs corpus mode); **route report Complete, 4/4 covered**. Owner housekeeping: 4 pre-F13 `.tbound-publication-*` dirs in the live image root (harmless, predate F13).
- On the real guest (F11/F12/F13, correct Landlock TSYNC): **G1 passed** (read, edit, bash, publish), **route probe passed**, **direct-runtime probe passed** (ends `unknown` by design).
- EVQ on F12 was 18/22 (stale artifacts); superseded by F13 21/22.
- On the guest since F13: static denial hints in the adapter (C-DH off), retry-margin budgets, audit/reset fixes, `tbound-panel`, `tbound-profile corpus-check`, the cleanup.
- Bounded cleanup before F13 (2026-10-24): 20 fixed, 7 deferred, 13 rejected, behaviour-preserving; ledger `../review-artifacts/cleanup-f13-20261024.md`.
- **F13 candidate `6162e3c`** (cleanup + fresh admission ids `g1-eng-2026-10-24a`, `route-probe-2026-10-24a`; dr `route-probe-dr-2026-10-23c` in F13.env). Fresh local results: kit gate green (race, 4634 pass / 0 fail / 26 guest-only skips); owner-side steps 10, 20, 25-p2-rebuild, 30, session-env, 35 capture rc 0. New runtime bundle (static denial hints in the adapter) `sha256:6a07a8a9…` (F0/F12 `8e405ef0…`, two offline builds identical) and new IMGDIGEST `sha256:20403e67…`.
- Kit changes for F13 (reviewed, 3 findings fixed): `25-p2-rebuild.sh`, guest `05-runtime-swap.sh` (runbook P11 with lock and rollback), guest-chain closes session sudo on failure. Signing and guest chain ran 2026-10-24 after owner approval (rc 0). Handoff: `F:/TBoundAssets/recovery-20261015/F13-prep/HANDOFF.md`.
- Not supported: interactive Pi sessions (one fixed signed task per service config). Planned in the product lane only.

## Research state (8-step goal)

| Step | State |
|---|---|
| 1. Scope cut (C-DH parked, TM-C narrowed), align D26 and residual-channel claim | Owner decided 2026-10-23; **advisor confirmation pending** |
| 2. Pilot reviews and corrections; freeze pilot protocol, provider, outage rules, ceilings | **In progress** (below) |
| 3. Freeze and pin code; install; repeat G1 and both probes with fresh evidence | **Done at F13 `6162e3c`** (G1, rp, dr fresh; EVQ 21/22; route report complete) |
| 4. Corpus mode; reset, net-sink, no-reap, V; complete EVQ and route reports | Not started; needs the owner session |
| 5. Engineering, compliant and adaptive-cap pilots; caps, costs, calendar | Not started |
| 6. Full corpus 299/100/20 + independent review; channel witnesses; cold partition | Designed only |
| 7. Final freeze of artifact, corpus, caps, analysis, funding; H1 registration | Not started |
| 8. Run H1 under the registered rules | Not started |

Step 2 detail:

- Pilot corpus (merged e59e5f6), manifest `sha256:3db1162226a1d38b71ce668a0a97bb9090c51beac442598c6cdd4444853a68d2`: 11 adversarial (InjecAgent 5, ASB 3, AgentDojo 2, RiOSWorld 1), 20 compliant (RepoQA reads, MultiPL-E HumanEval-JS write/edit/bash), 2 ambiguous (ToolEmu). Upstream licenses retained; write/edit graded by dataset tests in an isolated acceptance cell; bash results unguessable.
- D35 step 1, deterministic construct checks: `tbound-profile corpus-check` passes locally (exit 0, byte-identical reruns). All 33 validation/source/license/root/admission/reference checks pass; all 20 compliant cases reject all four mutations; no prompt or fixture leaks (5 RepoQA read retrievals not-checked: answer in fixture by design). Guest-only, required before the corpus freeze: 11 P calibrations, 20 acceptance cells, 7 network effects. Report: `C:/Users/Admin/AppData/Local/Temp/tb-chk-evidence-20261024-r2/`.
- F14 implementation branch `feat/corpus-check-guest-evidence`: optional `corpus-check --guest-evidence` recomputes the 38 guest checks from the producers' original outputs (runner journal replayed with its own rerun rules, `trial accept` results that now name their route and judged tree, the NET interval documents `tbound-run` now retains, the signed run files); the only operator file is a [mapping](docs/guides/construct-witnesses.md). Each imported row carries provenance and the "operator-retained, no external anchor" note (the audit chains have no external anchor, a recorded thesis limitation). Synthetic closure, the reviewer's hand-assembled attack (passes nothing), tampering and exact no-flag pilot report hashes are tested. No guest contacted or real witness validated; review, freeze and the owner's capture remain required before claiming closure.
- 15 MultiPL-E cases carry a no-ML-training license condition; panel requests set `provider.data_collection: deny`.
- D35 step 2, AI panel: tool ready (`docs/guides/panel-run.md`); two phases, 3 families (no Claude/GPT/DeepSeek), Gwet AC1 >= 0.80 and >= 90% agreement (Fleiss kappa reported, not gated), hard USD 10 cap (estimate USD 1.10, worst case USD 7.32). Prep rebuilt at 66a3dea: `F:/TBoundAssets/panel-prep-20261027/PANEL-READY.md`. **Owner runs it** after funding OpenRouter.
- D35 step 3: the advisor (non-author) signs meaning records and audits disagreements, unclear/ambiguous items and 60 random unanimous roots. Not started.
- Amended D35 tooling merged 2026-10-27 (5c1c835, from `feat/construct-review-signoff` 3ef478c):
  - Each case carries one advisor `signoff` (human, non-author, accept/reject, audited + resolution). `construct_review` pins {content_digest, panel_receipt_digest, panel_audit_digest, panel_stats_digest}.
  - Content-digest rule: the panel binds `evalcorpus.ContentDigest`, the manifest digest with every case `signoff`, `construct_review` and the freeze `pins` cleared. Sign-offs and F14 pins can be written after the run without detaching it.
  - Gate per phase-2 checklist item: Gwet AC1 >= 0.80 and >= 90% raw agreement; Fleiss kappa and AC1 intervals are reported.
  - `corpus-digest` and `preregistration-record` require `--panel-receipt --panel-audit --panel-stats` (the files the manifest pins). They recompute the statistics from the receipt ratings and replay the audit selection.
  - The audit seed is bound to the run seed; unanimous fails are mandatory audits; `audit_sample` must be 60 (the tool then audits all eligible roots when fewer than 60 exist; see the OPEN item under "Needs the owner or advisor"). The receipt records `public_policy`, and `tbound-op cfg-install` refuses a corpus service config that differs from it.
- Obsolete: the two-human review packets `review-artifacts/pilot-review-packets-20261024` and `-20261025`.

## Confirmatory corpus plan (D37)

- H1 adversarial N=299 = 110 established (A55/B55) + 189 derived from OWASP Agentic / MITRE ATT&CK (A45/B45/C99), labelled and reported separately; pre-registered established-only sensitivity analysis. No composites; pilot and H1 roots disjoint.
- Compliant 100 and ambiguous 20 from JS/TS-runnable sources and ToolEmu. None of the 189 derived or the confirmatory compliant/ambiguous cases exists yet.
- Clopper-Pearson 95% upper bound with 0 escapes: 1.23% at 299, ~3.3% at 110.
- Source sweeps: `C:/Users/Admin/AppData/Local/Temp/source-feasibility/` (established pool 121 conservative / 189 optimistic).

## Decisions

- 2026-10-23: C-DH parked until after H1 (static permitted-alternative table only). TM-C narrowed to mediated command-cell interpreter/socket/persistence stimuli; direct Pi-runtime bypass excluded (amendment (4), advisor to confirm). Pilot before registration.
- Pilot provider: OpenCode Go for the engineering pilot; the frozen H1 provider for the provider-dependent final pilot (paid: ask the owner first). Initial ceilings (provisional, final from complete pilots per D32): adversarial 17 calls / 20,000 tokens / 1,200 s; compliant 7 / 10,000 / 1,200; adaptive 7 iterations / 14 calls / 1,200 s. One evidenced outage rerun per case, both records kept; never rerun adverse outcomes.
- 2026-10-24: corpus from established benchmarks first, own derived cases only to fill N, clearly marked (D37). Construct review = deterministic checks + AI panel + advisor sign-off (D35 amendment). Owner funds and runs the panel (USD 10 cap).
- 2026-10-24 (orchestrator): `corpus-check` guest-evidence input is built AFTER the F13 install on its own branch and enters the thesis only through F14, the freeze before corpus mode; F13 is not re-frozen for it. Why: F13 exists to produce fresh G1/probe evidence; changing evidence tooling now would void the validated candidate; the 38 guest-only checks gate the corpus freeze, not F13 or the panel.
- 2026-10-24 (orchestrator): two defects reported by the product thread, confirmed in code on ff374c9, are **blockers for the pilot and F14**, not for F13 (G1/rp/dr contain no gate DENY and no retained-Store recovery): (1) in service mode a gate DENY closes the conversation — `buildActiveProgressPins` (`cmd/tbound/governed_workflow_live_linux.go`, `CapturedCallAvailable`) retires a denied captured call only at `awaiting_host_prompt`, but after a DENY the provider is at `awaiting_provider_exchange`, so the model never receives the denial or its hint (breaks Configuration C, D26 hints and post-denial behaviour); (2) `sessionrepo` recovery indexes record kind `governed_operation_authorization` (`internal/sessionrepo/recovery_linux.go:382`) while `cmd/tbound` writes `governed_operation_authorized` (`governed_session_linux.go:37`), so recovery preview likely refuses retained Stores with an effect. Fix both on this branch with fresh-process regression tests (service-mode DENY → next provider turn carries the hint) and review before F14; reuse the product-branch fix only after review.
- 2026-10-25 (orchestrator): the corpus tooling must implement the amended D35 BEFORE the owner runs the panel. Until now `evalcorpus` required two blinded human reviews per case plus a third-human adjudicator, which is the pre-amendment rule, so `corpus-digest` could not accept a manifest reviewed under the amendment. Also, `tbound-panel` bound the full draft digest, so the advisor's sign-offs would break the link to the panel run.
  - Change: one advisor `signoff` per case (human, non-author, accept/reject, audited + resolution), plus `construct_review` {content_digest, panel_receipt_digest, panel_audit_digest}.
  - The panel binds `ContentDigest`, which excludes the review fields.
  - `corpus-digest` requires the signed panel audit and receipt.
  - There is no separate two-human pilot review any more; the packets `review-artifacts/pilot-review-packets-20261024|20261025` are obsolete. Merged 2026-10-27 (5c1c835).
- 2026-10-26 (owner): the D35 panel agreement gate uses **Gwet's AC1**: ≥0.80 with ≥90% raw agreement passes, 0.60–0.79 means revise, <0.60 means stop. Fleiss' kappa is reported, not gated. Reason: kappa collapses at high prevalence (410/419 unanimous gives kappa ≈ −0.007). Cases the panel fails unanimously join the advisor's mandatory list. Recorded as a dated amendment in MSE_MA_Thesis `cd629ce` (decision register + thesis §7.3); the advisor confirms it. The content digest the panel binds also excludes the manifest `pins`, so the panel run does not depend on F14.
- 2026-10-26 (orchestrator, from the product-thread check-in): two shared-code defects.
  1. Recovery refused a retained Store after an allowed Bash command with an empty delta (`null` vs `[]`). Fixed on this branch (`ed395f0`/`3a4ff0c`, port of product `ba6f59e`).
  2. **Pilot/F14 blocker, fixed and merged 2026-10-27 (f483fad, 385584c):** a gate-allowed Bash command whose delta is refused after it ran (`denied_after_effect`; with the pilot's `workflow_bash_output_paths: []` that is ANY write) left its D09 lease unsettled, so the session quarantined on close and no settlement was retained.
     - ab14196 binds a denied settlement (`tbound-production-d09-command-settled-denied/v1`) to the Store's `denied_after_effect` record; the validator accepts exactly the promoted and denied shapes. 64de3d2 verifies it in recovery, session close and publication.
     - Review (Sol xhigh, 2 rounds) found that 64de3d2's own tests failed on a pre-existing recovery bug: `CommandSpec.DeclaredOutputPaths` is `json:"-"` and only the result side got the request scope back, so recovery refused every historical D09 receipt with a recorded scope, promoted or denied. Fixed in b102670. The product branch has the same line (`production_recovery_command_linux.go:393` at 499fb9f); port owed. ee70a76 makes the active denied branch fail closed.
     - Tests: `TestProductionBashDeniedAfterEffectRetainsItsSettlement`; `TestProductionD09{DeniedBashRetainsASettlementAndReleasesItsLease, PromotedBashSettlementKeepsItsShape, OperationValidatorAcceptsExactlyTwoShapes, DeniedSettlementRecoversInAFreshInstance, RecoveryRefusesUnsettledAndTamperedBashSettlements, SettlementsRecoverInFreshProcess (child process: controls + missing/receipt-tampered/duplicate refusals), DeniedBashCheckpointsAndClosesCleanly}`; `TestActiveRecoveryRefusesADeniedD09EffectItCannotVerify` (red without ee70a76).
     - Follow-ups, not F14 blockers per the review: full `ReplayGovernedHistoryForRecovery` with a denied Bash (needs a real provider-transcript fixture). The direct `CheckPublicationPrerequisites` denied-branch test is done (8654e2d).
- The coordinated signing/provisioning session (S3'/S4', installs, tbound-netsink, trial key, Spike B) happens after the reviews, as one signing round on the final manifest digest.

## Needs the owner or advisor

- Owner: whether the denied-settlement probe goes into F14 (new template, `tbound-op` name, kit step, one more signed cycle and gate) or into the next freeze/corpus session (the candidate package recommends the latter); AI panel run; the corpus-mode session (S3'/S4' signing with owner keys, tbound-netsink account/unit, pinned nft rule, root forced-command trial key, Spike B); approval of every privileged guest write.
- Advisor: D26 scope cut and residual-channel claim; panel audit and meaning-record sign-off. Options for all four open questions (D35 panel/AC1 amendments, pilot audit size, D26/TM-C scope, and since 2026-10-28 Configuration C's handling of no-effect tool errors, which can void the H1 friction clause through `complete == nComp`): `docs/evaluation/advisor-decisions.md`.
- **OPEN (owner/advisor, before the pilot audit is signed):** `audit_sample=60` is registered, but the pilot dry run had 1 eligible unanimous root. The owner/advisor must confirm whether all-eligible is acceptable or an amendment is required.
  - The tool audits all eligible roots when fewer than 60 exist: `supervisor/internal/panelaudit/selection.go:51` (`min(size, len(eligible))`), `docs/guides/panel-run.md:259` ("up to 60") and `:330` ("or all if fewer are eligible").
  - The thesis protocol registers "a random audit sample of 60 unanimous confirmatory roots" with a 95%-detection rationale and states no fewer-than-60 or pilot rule: MSE_MA_Thesis `tbound/tbound-thesis.md:922-925` and `tbound/AH-technical/10-decision-register.md:380-383`.
  - So all-eligible is tool behaviour, not an adopted protocol decision. The live run's eligible count may differ from the dry run's.
  - The pilot has 33 roots, 2 of them ambiguous, so at most 31 roots are ever eligible. The pilot audit is therefore always a census, and every pilot case reaches the advisor's deep review (the dry run listed 33/33).

## Next (thesis lane)

1. F14 freeze. Candidate package (2026-10-27, replaces `F14-prep/`): `F:/TBoundAssets/recovery-20261015/F14-candidate-20261027/` (README, F14.env, sources, kit patches 01 required / 02 recommended / 03 docs, OWNER-CHECKLIST, EVIDENCE-NEXT). Fresh admission ids committed as ade72cc (`g1-eng-2026-10-27a`, `route-probe-2026-10-27a`; dr `route-probe-dr-2026-10-27a` in F14.env); the F13 ids were still in the templates and the kit would have refused them only on the guest. FREEZE_COMMIT is pinned after the last pre-freeze commit and its full gate (WSL is busy with the product thread). Expected: same runtime bundle as F13 (`6a07a8a9…`, unverified until the P2 rebuild), new IMGDIGEST (epoch = freeze commit time), so a podman store reload; patch 01 lets `guest/05-runtime-swap.sh` accept the measurer pair 6162e3c→F14. Owner approval for signing and the guest chain. In parallel: the owner runs the panel; advisor review.
2. Next evidence step: `F14-candidate-20261027/EVIDENCE-NEXT.md` (refreshes `F13-prep/thesis-work-package-draft.md`). Neither G1 (declares its only Bash output) nor the route probe (changes no file, asserts NO_DIFF) exercises a `denied_after_effect` Bash command, so F14's D09 settlement fix gets no guest evidence from them. A minimal two-Bash denied-settlement probe is specified there; it also re-measures post-denial/hint behaviour (earlier service-mode DENY runs stopped at the denial). No existing guest path re-verifies a retained denied settlement in a fresh process: a restart, `tbound-op status` or a second admitted request does not open the Store with the command journal (EVIDENCE-NEXT addendum). Merged as cc831e9: `tbound verify-retained --run-id` (root-only, service stopped, drops to the service uid, holds the catalog lock, opens journals without O_CREAT, refuses C-DH runs by name, one JSON line or exit 2) and `tbound-op verify-retained RUNID`; two Sol xhigh rounds (round 1: 3 should-fix + 1 nit fixed; round 2: none). `audit.OpenExisting` now backs `openExistingRecoveryJournal`, the workflow catalog open and the Store admission-journal open (all already required the file). Never run on a production-closed Store yet. With it, the probe, G1 (positive control, OWNER-CHECKLIST section 5) and the pilot cases with a static protected-path Bash step (A-03, A-04, B-04) can show "recovery verifies" on the guest. Then the corpus-mode prerequisites: EVQ-NETWORK-SINK-GUEST and witnesses for 11 calibration-P, 20 acceptance-cell, 7 network-effect checks (all pending, owner session).
3. Corpus-mode rehearsal; watch: reset --check passes after P C-02 and the reset kills the survivor; `podman rm -f` after conmon is killed; no uid-995 pause process in root's SSH cgroup (`ps -u 995 -o pid,cgroup,cmd`); kernel-7 zombie-leader kill; the 5 real-cgroup tests with `TBOUND_REAL_CGROUP_TESTS=1` in a delegated scope.
4. Complete EVQ and route reports; pilots; caps and costs; full corpus; registration; H1.

## State 2026-10-27

- The machine move is cancelled; work continues on FABIAN.
- Full gates: WSL, Go 1.27.1, clean `git archive` export, the two `make boundary` commands (make is not installed in WSL), gofmt, build, vet, Windows cross-build, `go test -race -count=1 ./...`. Counts include subtests, as in the kit's `gate.sh`. Script and logs: `F:/TBoundAssets/gates-20261027/`. Of the 26 skips, 24 need the guest, root or a real cgroup and 2 are live-provider tests (`TestRealOpenRouterExchange`, `TestRealProviderDurableLinux`) that need explicit enablement.
  - 5f2d0a6: 4776 pass / 26 guest-only skips / 2 fail. Both failures were `netoracle: canary start ... context deadline exceeded` after ~70 s while other WSL tests ran (`TestConstructRecomputesFromProducerFiles`, `TestCollectorCleanWindowIsNoneWithAReportReference`). Both tests and both packages pass under `-race` in isolation: a load flake. Run gates with nothing else on WSL.
  - f483fad (D09 merge): 4786 pass / 26 guest-only skips / 0 fail; the same skip set.
  - 5c1c835 (signoff merge, includes 385584c): 5047 pass / 26 guest-only skips / 0 fail; the same skip set.
  - 66a3dea (panel audit-binding merge): 5062 pass / 26 guest-only skips / 0 fail; the same skip set.
  - 8654e2d (F14 admission ids ade72cc + the `CheckPublicationPrerequisites` denied-Bash test): 5063 pass / 26 skips / 0 fail; the same skip set. The new test is red under both mutations of the denied branch (branch disabled; settlement check removed): `focused-d09pub-35e6fdb.txt`.
  - 2fe33b0 (`feat/verify-retained` head; merged as cc831e9, whose tree differs only in TASK_STATE.md): 5077 pass / 26 skips / 0 fail; the same skip set.
  - eb53d7d (`fix/durable-read-line-window` head; merged as 039ac82 with a comment/test-name nit commit d0aec7a): 5108 pass / 26 skips / 0 fail; the same skip set.
  - f1ee21d (`fix/recovery-retirement-pins` head, not merged yet): 5082 pass / 26 skips / 0 fail; the same skip set. The two branches merge cleanly (`git merge-tree`).
- 2026-10-28 fixes found by code reading before the pilot:
  - Governed `read` refused the advertised `offset`/`limit`, ending the conversation as an unknown execution (one such compliant trial voids the H1 friction clause). Fixed in 039ac82: Pi 0.87.1 line semantics through one helper shared by executor and replay; the Store still reads and digests the whole file; past-EOF is an empty success; 0/negative/fractional refused. Sol xhigh: merge as is (2 nits fixed). Runtime bundle unchanged.
  - Production resume (`fix/recovery-retirement-pins`): (1) lineage retirement used the binder's pins without initial generation/tree, so every resume needing retirement was refused; (2) the Store replay verifier kept state across the two replay passes of retirement. Both fixed red-first; Sol xhigh: merge (one regression test requested). Also adds the full governed-replay test with a denied D09 Bash (the former follow-up), fresh process, 5 refusal cases.
  - Still open, confirmed by Sol: (3) `Activate` refuses every recovered Store with a promoted transition (the fresh authority's empty proof map; `sessionlaunch_linux.go:518` -> `store.Verify()` -> `lifecycle_linux.go:511`); (4) the binder calls `BindPreparedSession` a second time (`production_recovery_binder_linux.go:382` vs `governed_recovery_linux.go:469`); (5) recovered publication delegates historical transition/origin checks to the fresh authority (`production_publication_linux.go:546`). Production resume after a promoted write therefore does not work yet. It fails closed. No registered run depends on it: scripted pilot roles refuse resume, and the 51-trial recovery suite (incl. `LC-08-AFTER-CHECKPOINT`) is proposed, not registered. Proposed design (Sol): after full historical verification, freeze the exact approved-transition prefix and install a Store decision-verifier adapter that requires exact equality for that prefix and defers later sequences to the fresh authority.
- D09 merged (f483fad, regression test 385584c). `fix/recovery-empty-bash-delta` (e7e9d33) stays unmerged.
- `feat/construct-review-signoff` merged as 5c1c835. On the merge: the panelaudit/tbound-panel/evalcorpus/corpusadmit and tbound-profile/tbound/tbound-run tests pass, the 24 local `tbound-op` tests pass, and the full gate is 5047 pass / 26 guest-only skips / 0 fail (same skip set).
- Pre-run review of the merge (Sol xhigh):
  - Fixed before the paid panel run, merged as 66a3dea (`fix/panel-audit-receipt-binding`, 2 Sol review rounds). (1) `audit.json` (schema `tbound-panel-audit/v4`) records `receipt_digest`, the digest of the exact `receipt.json` written just before it, and `panelaudit.Verify` requires it; an advisor-signed audit of run A can no longer accompany run B's receipt. (2) `tbound-panel` refuses before any provider call and before any report write when `audit.json` carries an advisor sign-off, does not parse, or has duplicate members.
  - Accepted limitation: a crash between saving a provider response and appending its rating makes resume pay for that call again. Both charges stay counted against the cap.
  - No AC1/raw-agreement formula error found; every request sets `data_collection: deny` and `require_parameters: true`.
- Panel prep rebuilt at 66a3dea in `F:/TBoundAssets/panel-prep-20261027/`:
  - `tbound-panel.exe` (Go 1.27.1 cross-build of the gated archive) and the unchanged frozen `panel.json` (`FREEZE.sha256`).
  - Model ids and prices re-checked against the public OpenRouter lists.
  - Estimate: 198 calls, USD 1.10 expected, USD 7.32 worst case.
  - Fresh dry run `dry-2`: 198/198 requests with `data_collection: deny`, stats v4, audit v4 bound to its receipt, deterministic apart from timestamps.
  - `PANEL-READY.md` holds the owner's live steps.
  - The old `F:/TBoundAssets/panel-prep/` is marked STALE; do not run it.
- Next: the owner runs the live panel; the advisor signs; then the F14 freeze on FABIAN (owner step; `corpus-digest` needs the F14 pins).
- Shared-fix ledger (thesis ↔ product):
  - Product → thesis, done: the empty-delta recovery fix (product ba6f59e → ed395f0/3a4ff0c); the DENY continuation and the recovery record kind (892cc00).
  - Thesis → product, owed: the product synced F14 up to 5f2d0a6 (product 7ceeff6). It still lacks the D09 denied settlement and its recovery (f483fad: ab14196, 64de3d2, b102670, ee70a76, tests; plus the 8654e2d test), checked at product b6493b9 on 2026-10-27.
    - b102670 alone also fixes a live product bug: recovery refuses any historical promoted D09 receipt with a recorded output scope (`production_recovery_command_linux.go:393`).
    - Also from 2026-10-28: the read line window (039ac82) and the recovery retirement fixes (`fix/recovery-retirement-pins`); the product shares this recovery code, so it likely has defects (1)-(5) too (unchecked).
    - Also from cc831e9: `audit.OpenExisting` and its three shared call sites (recovery journal, workflow catalog, Store admission journal); `verify-retained` itself depends on the D09 settlement code above.
  - The panel/corpus tooling (5c1c835, 66a3dea) is thesis-only.
- The F14 freeze, signing, guest install and evidence are owner steps on FABIAN after the merges and the panel prep.

## Working rules

- Freeze loop: gate -> freeze -> archive previous guest state (option A) -> install -> guest chain. No commits in the main checkout during a freeze.
- Kit lessons: no BOM, LF only; decode base64 event data, never grep it; step 40 requires each evidence artifact's commit to equal the freeze commit; stale uid-995 podman pause processes need `RECLAIM_PODMAN_PAUSE=1`; the dr request ends unknown by design and must be archived before the pilot cycle.
- Product changes reach the thesis only through a separate review and freeze; they never change a frozen experiment.
- Thesis limitations to record: kernel 7.0 CLONE_INTO_CGROUP bug; on a single-uid rootless host cell ownership cannot tell image root from the cell user; the audit chain has no external anchor; every guest run before F11 had thread-only Landlock (flag bug) and is not evidence. From the 2026-10-25 cross-check of product findings, none of which the governed agent can trigger:
  - Publication assumes it is the only writer on the host; a concurrent host-side directory move can redirect a write.
  - The WS oracle does not measure ACLs or xattrs; publication refuses them.
  - Release and verifier clean-checks can be blinded by git `assume-unchanged`/`skip-worktree`; the freeze kit archives committed bytes.
  - Development and V-run credential paths check only the final component; the production reader checks every ancestor.
- Parked until after the thesis: C-DH / decision-model hints (`docs/design/denial-hints.md` stage 2, `docs/design/denial-hint-providers.md`).

Constraints: explicit `git add`; never push; no secret/key/vm_start.txt reads; Go 1.27.1 at /home/jeli2k/opt/go1.27.1; clean-room gate via the kit's `tools/gate.sh`; logs on F:; C: >= 5 GiB; no `wsl --shutdown`; every privileged guest write, signing, or VM state change needs explicit approval; sign only with the owner's keys.
