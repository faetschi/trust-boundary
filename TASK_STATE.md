# TBound thesis task state

Thesis lane only. Checkpoint 2026-10-24. Product work is tracked in
[docs/product-plan.md](docs/product-plan.md) (section 10), not here. History:
git log of this file, `../review-artifacts/TASK_STATE-history-20261023.md`,
`../review-artifacts/TASK_STATE-pilot20-receipts-20261024.md`.

## Summary

The engineering core works on a real guest; the thesis has no results yet.
Research is at step 2 of 8 (pilot review). H1 is unexecuted; tests, fixtures,
probes and pilot reports are not thesis results.

## Implementation state

- Branch `codex/tbound-prototype`. Last guest freeze **F12 = eed7722** (kit `F:\TBoundAssets\recovery-20261015\F12`).
- On the real guest (F11/F12, correct Landlock TSYNC): **G1 passed** (read, edit, bash, publish), **route probe passed**, **direct-runtime probe passed** (ends `unknown` by design).
- EVQ dry run on F12: **18/22**. Not passed: 3 stale guest artifacts (kit bug, fixed), capability stale, netsink missing (needs corpus mode). Route report: 3 routes, bash-view stale. "21/22 with fresh artifacts" is a forecast.
- Merged since F12, **not yet on the guest**: static denial hints (C-DH code default-off), retry-margin budgets, audit and reset fixes, the re-based pilot corpus, `tbound-panel`, `tbound-profile corpus-check`, panel `data_collection: deny`, the docs restructure (`docs/guides|runtime|evaluation`).
- Gate on merged HEAD b8d1b60 (2026-10-24): gofmt, vet, full `go test ./...` pass; ~26 guest-only skips.
- Reusable freeze kit `F:/TBoundAssets/recovery-20261015/kit` + `freezes/F13.env`: verified by Sol, never run.
- Not supported: interactive Pi sessions (one fixed signed task per service config). Planned in the product lane only.

## Research state (8-step goal)

| Step | State |
|---|---|
| 1. Scope cut (C-DH parked, TM-C narrowed), align D26 and residual-channel claim | Owner decided 2026-10-23; **advisor confirmation pending** |
| 2. Pilot reviews and corrections; freeze pilot protocol, provider, outage rules, ceilings | **In progress** (below) |
| 3. Freeze and pin code; install; repeat G1 and both probes with fresh evidence | F13 kit ready, not run |
| 4. Corpus mode; reset, net-sink, no-reap, V; complete EVQ and route reports | Not started; needs the owner session |
| 5. Engineering, compliant and adaptive-cap pilots; caps, costs, calendar | Not started |
| 6. Full corpus 299/100/20 + independent review; channel witnesses; cold partition | Designed only |
| 7. Final freeze of artifact, corpus, caps, analysis, funding; H1 registration | Not started |
| 8. Run H1 under the registered rules | Not started |

Step 2 detail:

- Pilot corpus (merged e59e5f6), manifest `sha256:3db1162226a1d38b71ce668a0a97bb9090c51beac442598c6cdd4444853a68d2`: 11 adversarial (InjecAgent 5, ASB 3, AgentDojo 2, RiOSWorld 1), 20 compliant (RepoQA reads, MultiPL-E HumanEval-JS write/edit/bash), 2 ambiguous (ToolEmu). Upstream licenses retained; write/edit graded by dataset tests in an isolated acceptance cell; bash results unguessable.
- D35 step 1, deterministic construct checks: `tbound-profile corpus-check` passes locally (exit 0, byte-identical reruns). All 33 validation/source/license/root/admission/reference checks pass; all 20 compliant cases reject all four mutations; no prompt or fixture leaks (5 RepoQA read retrievals not-checked: answer in fixture by design). Guest-only, required before the corpus freeze: 11 P calibrations, 20 acceptance cells, 7 network effects. Report: `C:/Users/Admin/AppData/Local/Temp/tb-chk-evidence-20261024-r2/`.
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
- The coordinated signing/provisioning session (S3'/S4', installs, tbound-netsink, trial key, Spike B) happens after the reviews, as one signing round on the final manifest digest.

## Needs the owner or advisor

- Owner: AI panel run; the corpus-mode session (S3'/S4' signing with owner keys, tbound-netsink account/unit, pinned nft rule, root forced-command trial key, Spike B); approval of every privileged guest write.
- Advisor: D26 scope cut and residual-channel claim; panel audit and meaning-record sign-off.

## Next (thesis lane)

1. Owner runs the panel; advisor review.
2. F13: freeze the selected code, install, repeat G1 and both probes with fresh evidence.
3. Corpus-mode rehearsal; watch: reset --check passes after P C-02 and the reset kills the survivor; `podman rm -f` after conmon is killed; no uid-995 pause process in root's SSH cgroup (`ps -u 995 -o pid,cgroup,cmd`); kernel-7 zombie-leader kill; the 5 real-cgroup tests with `TBOUND_REAL_CGROUP_TESTS=1` in a delegated scope.
4. Complete EVQ and route reports; pilots; caps and costs; full corpus; registration; H1.

## Working rules

- Freeze loop: gate -> freeze -> archive previous guest state (option A) -> install -> guest chain. No commits in the main checkout during a freeze.
- Kit lessons: no BOM, LF only; decode base64 event data, never grep it; step 40 requires each evidence artifact's commit to equal the freeze commit; stale uid-995 podman pause processes need `RECLAIM_PODMAN_PAUSE=1`; the dr request ends unknown by design and must be archived before the pilot cycle.
- Product changes reach the thesis only through a separate review and freeze; they never change a frozen experiment.
- Thesis limitations to record: kernel 7.0 CLONE_INTO_CGROUP bug; on a single-uid rootless host cell ownership cannot tell image root from the cell user; the audit chain has no external anchor; every guest run before F11 had thread-only Landlock (flag bug) and is not evidence.
- Parked until after the thesis: C-DH / decision-model hints (`docs/design/denial-hints.md` stage 2, `docs/design/denial-hint-providers.md`).

Constraints: explicit `git add`; never push; no secret/key/vm_start.txt reads; Go 1.27.1 at /home/jeli2k/opt/go1.27.1; clean-room gate via the kit's `tools/gate.sh`; logs on F:; C: >= 5 GiB; no `wsl --shutdown`; every privileged guest write, signing, or VM state change needs explicit approval; sign only with the owner's keys.
