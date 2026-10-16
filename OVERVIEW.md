# TBound — current state

Updated 2026-10-17. Branch: `codex/tbound-prototype`, HEAD `484d806`. Priority: thesis completion. Green results verify code only: G1 false, H1 unexecuted, containment not established.

| Milestone | Current state |
| --- | --- |
| Recovery / D14 | Logical owner headroom gate and invariant test committed and re-run green (workflowrecovery 70 pass / 1 skip, cmd/tbound 138 pass / 2 skip). Review fix `484d806`: a rolled-back pre-commit clock-persist failure no longer poisons the root, so a Reserved terminal can be retried. Open: physical reserve, fsync/ENOSPC/power-loss, root terminal anchor and shared owner file ungated, real owner/cell drain wiring, retained restart. |
| Model pin | Pi and adapter derive from the broker-approved `deepseek-v4.1-flash`. Runtime-bundle digest in the ABI 8 provisioning receipt is stale; re-measure and re-sign during provisioning. |
| ABI 8 guest | Exact ABI 8 measured; xattr census and scanner-context receipts recorded. Not qualification: B2/B3 calibration, cells creation, systemd leaf, signed host profile, registration, E2E and drain remain open and need the guest and your signing authority. |
| Research Claims — last | Host-side, code-only components committed and independently reviewed once (all confirmed findings fixed, tests first): `evalscore`, `evalcorpus`, `usdledger`, `researchprofile`, `effectoracle` (E04), `drainproof` (unwired), `routematrix` (E06 coverage checker, not an E06 pass), `calibration` (P/C pair logic), `attacker` (pivot state machine, library content open). None is wired into a runner yet. Missing: corpus content, B/V runners and P/C replays (need guest), oracles EFF-WS-05/06, NET-01/02, PROC-01..03, frozen prices and cost plan, pre-registration. |
| Hygiene | Stale docs/dead code removed. Review-fix commits `a081cbf`, `b2d94ce`, `721b968`, `ab783f1`, `97c6741`. Full-module gate on HEAD running. Ten other-session docs remain modified; `docs/tbound-product.md` is another session's untracked file. |

Needs your decision (code defaults are fail-closed and unfrozen):
- evalscore: label for a compliant task where C succeeds (blocks `rule_satisfied`); one-rerun cap for headline trials; unevidenced outage as conservative failure vs unsupported.
- Ledger: unit vs request, stop-rule enforcement, head-hash anchor location (caller must pass it; `AllowGenesis` is explicit); engineering-ledger unit for OpenCode Go.
- evalcorpus: seed rule (SHA-256 placeholder), cold stratum per class vs source, root uniqueness scope, source-pool selection, EFF-* vocabulary.
- effectoracle: records after Stop dropped by design; ancestor-swap residual (needs fd-relative watches).
- drainproof: dead owner + absent scope + no receipt counts as not drained (the D20 test assumes the opposite); pending effects in the proof vs ledger gate.
- researchprofile: pin a provider or set price ceilings; verify `require_parameters`/`zdr` names against live OpenRouter docs at freeze.
- calibration: static-only pair semantics; monetary cap needs prices (convert per-MTok to per-token).
- OSF/Zenodo registration versus the 2026-10-14 decision; whether to full-capture VM uplink (EFF-NET-02).

Engineering: OpenCode Go. Final research: OpenRouter DeepSeek V4.1 Flash, US$5 total cap. Original/seed VMs preserved.
