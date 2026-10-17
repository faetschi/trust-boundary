# TBound — current state

Updated 2026-10-17. Branch: `codex/tbound-prototype`, HEAD `1423e0d`. Priority: thesis completion. Green results verify code only: G1 false, H1 unexecuted, containment not established. Goal cleared by the user; no background work running.

| Milestone | Current state |
| --- | --- |
| Recovery / D14 / D20 | Logical headroom gate, invariant test, clock-persist fix (`484d806`), and production drain proof wiring (`928a79e`: drainproof, evidence binds observed facts, live owner refused first). Open: physical reserve, fsync/ENOSPC/power-loss, root terminal anchor and shared owner file ungated, crash window between scope remove and bind (refused, not drained), pending effects not observed, live cgroup paths untested without delegation. |
| Model pin | Pi and adapter derive from the broker-approved `deepseek-v4.1-flash`. Runtime-bundle digest in the ABI 8 provisioning receipt is stale; re-measure/re-sign at provisioning. |
| ABI 8 guest | Measured; xattr census and scanner-context receipts recorded. VM is Off. Open (need guest, your approvals, signing): B2/B3 calibration (you chose B2 option A: chmod root:root 0555, changes bundle digest), cells, systemd leaf, NodeRO mount, signed host profile, registration, E2E `serve --pi`, drain on real cells, checkpoint restore. You approved read-only re-measurement; start the guest from an elevated shell first (no start action in the pinned script). |
| Research Claims — last | Host-side, code-only and reviewed: `evalscore` (compliant C-success = VALID_PASS, user decision; `rule_satisfied` can now be true on a synthetic full corpus), `evalcorpus`, `usdledger`, `researchprofile`, `effectoracle` (E04), `wsoracle` (EFF-WS-05/06 unit-level), `drainproof`, `routematrix` (coverage checker), `calibration`, `attacker` (library content open). Full-module gate on `9598a36`: 1955 pass / 8 skip / 0 fail. Not wired into a runner. Missing: corpus content, B/V runners, P/C replays (guest), NET-01/02 and PROC-01..03 oracles (guest), frozen prices and cost plan, pre-registration, G0/G1, advisor review. |
| Hygiene | Dead code and stale docs removed. Ten other-session docs modified; `docs/tbound-product.md` untracked (other session). |

Needs your decision:
- Ledger: unit vs request, stop-rule, head-hash anchor owner (`AllowGenesis` explicit); engineering-ledger unit for OpenCode Go.
- evalscore: one-rerun cap for headline trials; unevidenced outage as conservative failure vs unsupported.
- evalcorpus: seed rule (SHA-256 placeholder), cold stratum, root uniqueness scope, source-pool selection, EFF-* vocabulary.
- wsoracle: ledger serialization and tree-digest definitions, metadata scope, `openat2` requirement.
- researchprofile: pin a provider or set price ceilings; verify `require_parameters`/`zdr` names at freeze.
- calibration: static-only pair semantics; monetary cap needs prices.
- OSF/Zenodo registration vs the 2026-10-14 decision; full VM-uplink capture (EFF-NET-02).

Engineering: OpenCode Go. Final research: OpenRouter DeepSeek V4.1 Flash, US$5 total cap. Original/seed VMs preserved.
