# Advisor decision package: pilot construct review and scope

Prepared 2026-10-27 at `codex/tbound-prototype` `dbc5858`. **All three items are OPEN.**
This page decides nothing. Where the owner has already decided, it says so; the advisor confirms or amends.
Register paths are in the thesis repository `MSE_MA_Thesis`; code paths are in this repository.

| # | Question | Owner position | Blocks |
|---|---|---|---|
| 1 | Confirm the D35 construct-review amendments (AI panel 2026-10-24, AC1 gate 2026-10-26) | Decided, advisor confirmation pending | The meaning of the panel's pass/revise/stop verdicts |
| 2 | Audit sample when fewer than 60 roots are eligible | None recorded | The advisor's signature on the pilot audit |
| 3 | D26 scope cut (C-DH withdrawn) and narrowed TM-C residual-channel claim | Decided, advisor confirmation pending | Claim text and H1 registration, not the pilot run |

None of the three blocks the paid panel run itself: the ratings do not depend on any of them.

## 1. D35 amendments: AI panel and AC1 gate

- **Text.** `tbound/AH-technical/10-decision-register.md:343-399` replaces the two-human review with deterministic checks, a blinded three-family AI panel (no Claude, GPT or DeepSeek) and an advisor sign-off. `:401-429` gates each checklist item on Gwet's AC1 instead of Fleiss' kappa: pass at AC1 ≥ 0.80 with raw agreement ≥ 90%, revise at 0.60–0.79, stop below 0.60. Kappa is still reported. `:429`: "The advisor confirms this amendment together with the 2026-10-24 amendment."
- **Why AC1** (`:419-422`): kappa collapses at high prevalence. 410 of 419 unanimous `pass` plus 9 single dissents is 98.6% agreement but kappa ≈ −0.007; AC1 is 0.986.
- **Implemented.** `stats.json` (schema `tbound-panel-stats/v4`) gates on AC1 and raw agreement and reports kappa; merged in `5c1c835`, gated green at `66a3dea`.

Options:

- **A. Confirm both as written.** The pilot round runs under the registered gate. Confirming before the paid run keeps both amendments prospective, as `:403` states.
- **B. Confirm the panel, keep kappa as the gate.** The tool and `stats.json` gate would need a code change, a gate and a rebuilt panel prep. A sound, high-prevalence pilot is then likely to get `revise` or `stop` from kappa alone.
- **C. Confirm with different thresholds.** Same code and prep cost as B. Thresholds chosen after ratings exist are no longer prospective.

## 2. Audit sample size for the pilot

- **Registered rule** (`10-decision-register.md:378-384`, thesis `tbound-thesis.md:922-925`): the advisor reviews every disagreement, `unclear`, and ambiguous case, plus "a random audit sample of 60 unanimous confirmatory roots" (about 95% chance of detecting a 5% error rate). No rule covers fewer than 60 eligible roots, and the pilot's cases are not confirmatory.
- **Tool behaviour.** `supervisor/internal/panelaudit/selection.go:51` audits `min(60, eligible)` roots. A root is eligible only when no member is ambiguous or has a review reason (`:36-43`). The verifier requires the recorded `sample_size` to be 60 (`:60`). Guide: `docs/guides/panel-run.md:259`, `:330`.
- **Pilot facts.** `corpus/pilot/manifest.draft.json` has 33 cases in 33 distinct roots, 2 of them ambiguous. At most 31 roots can ever be eligible, so the pilot audit is always all eligible roots: a census, not a sample. Every pilot case therefore reaches the advisor's deep review, either with a review reason or as an audited root. The dry run (`F:/TBoundAssets/panel-prep-20261027/dry-2/audit.json`, canned ratings) listed all 33 cases: 32 with reasons and 1 random unanimous root.
- **Detection.** A census reviews every unanimous root, so it detects more than the registered 60-root sample, provided the review itself is sound.

Options:

- **A. Accept all-eligible for the pilot without an amendment.** Read the 60-root rule as applying to confirmatory roots only, and state in the signed `audit.md` that the pilot audit is a census. No code or prep change.
- **B. Record a dated clarifying amendment before signing:** "When fewer than 60 roots are eligible, all eligible roots are audited." It matches the tool and also covers the full corpus if its eligible count falls below 60. Costs one register entry and one thesis sentence; no code change.
- **C. Adopt a different rule.** Needs a change to `selection.go` and its verifier, tests, a full gate, and a rebuilt `tbound-panel.exe`, which changes `FREEZE.sha256`. An unsigned audit can be regenerated from the same run directory without new provider calls; once any `advisor_signoff` field is filled, `tbound-panel` refuses to touch that directory.

## 3. D26 scope cut and residual-channel claim

- **Amendment 2026-10-23 (3)**, owner decision (`10-decision-register.md:214-231`): C-DH is withdrawn from thesis scope. The code stays in the artifact, default-off and outside every registered configuration. Reasons given (`:220-223`): it competes with the main experiment (at least 200 more compliant executions), its planner cannot use the pilot's pivot cases, and paid chooser calls are outside the spend accounting.
- **Amendment 2026-10-23 (4) A**, advisor confirmation pending (`:233-245`): TM-C covers mediated command-cell interpreter, socket and persistence stimuli. A direct Pi-runtime bypass of the mediated route is excluded from the claim, so its witnesses are out of scope rather than missing. N=299 and all H1 rules are unchanged.

Options:

- **A. Confirm both.** The residual-channel claim names only mediated witnesses; the thesis states the exclusion.
- **B. Confirm (3), reject (4) A.** Direct-runtime bypass witnesses become mandatory: they need a witness design and guest work before registration.
- **C. Reject (3).** C-DH returns as a secondary arm, with the costs listed above. Workload goes back above the core 1,935 execution units, and the spend accounting must cover chooser calls first.

## After the advisor answers

1. Record each answer as a dated entry in the decision register, quoting the advisor's wording.
2. Item 1 = B or C, or item 2 = C: the code change goes through a red test, a full gate and a rebuilt panel prep before the advisor signs anything.
3. Item 3 = B or C: update the claim boundary in `docs/evaluation/status.md` and the thesis scope sections before registration.
