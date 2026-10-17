# TBound — current state

Updated 2026-10-17. Branch `codex/tbound-prototype`, HEAD `e8a2004`. Priority: thesis completion. Green results verify code only: G1 false, H1 unexecuted, containment not established.

| Milestone | Current state |
| --- | --- |
| Recovery / D14 / D20 | Drain proof wired into production; a never-exposed owner now drains through its own `NeverBound` proof basis (`ff6185d`, after a Codex review finding). Open: physical reserve, fsync/ENOSPC/power-loss, root anchor ungated, the crash window between scope remove and bind (refused, not drained), pending effects not observed, live cgroup paths untested. |
| ABI 8 guest | Reachable at 172.22.198.27, checked against the pinned host key. Re-measured read-only with no drift; runtime and runtime-src already root:root 0555. Node is bound read-only (`usr-local-bin-node.mount`). The service and socket templates (`DelegateSubgroup=runner`) are installed and verified, and the runner creates `cells` at start (`e8a2004`). The service has not been started. Next: calibration, registration and host-profile signing with your admin key (dry run with throwaway keys in progress), passwordless sudo revoked before the first cell launch, E2E `serve --pi`, drain on real cells, checkpoint restore (runbook ready). |
| Research Claims — last | Host-side, code-only: `evalscore`, `evalcorpus`, `usdledger`, `researchprofile`, `effectoracle`, `wsoracle` v2, `drainproof`, `routematrix`, `calibration`, `attacker`, and the new `evalreport` (fails closed on duplicate or unregistered records, binds the full ledger snapshot). None is wired into a runner yet. Missing: corpus content, B/V runners, P/C replays (guest), NET-01/02 and PROC-01..03 oracles (guest), frozen prices and cost plan, pre-registration, G0/G1, advisor review. |
| Verification | Full-module clean-room gate on `ff6185d`: 38 packages, 2102 pass / 8 skip / 0 fail. The skips need a real provider, a guest, or root. Each increment gets an adversarial Codex GPT-6.1-Sol review: drain and wsoracle in round 2; evalreport and runbook round 2 pending (two Codex crashes). |
| Hygiene | Previously unlinked docs are now linked. Ten other-session docs are modified, and `docs/tbound-product.md` is untracked (other session). |

Decided 2026-10-17: the cell leg (D2) is out of scope and the in-place root-writer calibration is used instead; the xattr claim is bounded ("no hidden trusted.*", root-attested); passwordless sudo stays through provisioning and is revoked before the first cell launch; compliant C-success = VALID_PASS; a dead owner with an absent scope and no receipt = not drained.

Needs your decision:
- Ledger: unit vs request, stop rule, head-hash anchor owner; engineering-ledger unit for OpenCode Go.
- evalscore: one-rerun cap; unevidenced outage as a conservative failure vs unsupported.
- evalcorpus: seed rule, cold stratum, root uniqueness scope, source pool, EFF-* vocabulary.
- wsoracle: ledger serialization and tree-digest definitions; `openat2` requirement.
- researchprofile: pin a provider or set price ceilings. Calibration: static-only pair semantics.
- OSF/Zenodo registration vs the 2026-10-14 decision; full VM-uplink capture (EFF-NET-02).

Engineering: OpenCode Go. Final research: OpenRouter DeepSeek V4.1 Flash, US$5 total cap.
