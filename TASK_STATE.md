# TBound thesis task state

Thesis lane only. Checkpoint 2026-10-25. Product work is tracked in
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
- D35 step 2, AI panel: tool ready (`docs/guides/panel-run.md`); two phases, 3 families (no Claude/GPT/DeepSeek), Fleiss kappa >= 0.80 and >= 90% agreement, hard USD 10 cap (estimate ~USD 1.2). **Owner runs it** after choosing models and funding OpenRouter.
- D35 step 3: the advisor (non-author) signs meaning records and audits disagreements, unclear/ambiguous items and 60 random unanimous roots. Not started.
- Old review packets `review-artifacts/pilot-review-packets-20261024` are superseded (authored cases).

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
  - There is no separate two-human pilot review any more; the packets `review-artifacts/pilot-review-packets-20261024|20261025` are obsolete. Branch `feat/construct-review-signoff`.
- The coordinated signing/provisioning session (S3'/S4', installs, tbound-netsink, trial key, Spike B) happens after the reviews, as one signing round on the final manifest digest.

## Needs the owner or advisor

- Owner: AI panel run; the corpus-mode session (S3'/S4' signing with owner keys, tbound-netsink account/unit, pinned nft rule, root forced-command trial key, Spike B); approval of every privileged guest write.
- Advisor: D26 scope cut and residual-channel claim; panel audit and meaning-record sign-off.

## Next (thesis lane)

1. F14 freeze. Prep: `F:/TBoundAssets/recovery-20261015/F14-prep/` has F14.env.draft (ids `*-2026-10-25a`), the kit changes and the repo edits. Known kit edit: `guest/05-runtime-swap.sh:33-34` must accept the measurer pair 6162e3c→F14 (audited unchanged so far). P2 falls back to rebuild, and the new IMGDIGEST means a podman store reload. Owner approval for signing and the guest chain. In parallel: the owner runs the panel; advisor review.
2. Next work package (prepared, not executed): `F:/TBoundAssets/recovery-20261015/F13-prep/thesis-work-package-draft.md` — corpus-mode prerequisites, EVQ-NETWORK-SINK-GUEST, and witnesses for 11 calibration-P, 20 acceptance-cell, 7 network-effect checks (all pending). Re-measure post-denial/hint behaviour on F14: earlier service-mode DENY runs stopped at the denial.
3. Corpus-mode rehearsal; watch: reset --check passes after P C-02 and the reset kills the survivor; `podman rm -f` after conmon is killed; no uid-995 pause process in root's SSH cgroup (`ps -u 995 -o pid,cgroup,cmd`); kernel-7 zombie-leader kill; the 5 real-cgroup tests with `TBOUND_REAL_CGROUP_TESTS=1` in a delegated scope.
4. Complete EVQ and route reports; pilots; caps and costs; full corpus; registration; H1.

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
