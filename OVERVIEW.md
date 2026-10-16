# TBound — current state

Updated 2026-10-16. Branch: `codex/tbound-prototype`, HEAD `1384e9a`. Priority: thesis completion. Green results verify code only: G1 false, H1 unexecuted, containment not established.

| Milestone | Current state |
| --- | --- |
| Recovery / D14 | Logical owner headroom gate committed (`b089a9d`) and pinned by a tested invariant (`852f276`). Open: physical reserve, fsync/ENOSPC/power-loss, root terminal anchor and shared owner file ungated, real owner/cell drain (stub still no-op), retained restart. |
| Model pin | Pi and adapter derive from the broker-approved `deepseek-v4.1-flash` (`bd6de94`). Runtime-bundle digest in the ABI 8 provisioning receipt is stale; re-measure and re-sign during provisioning. |
| ABI 8 guest | Exact ABI 8 measured; xattr source-scope census (`84cfaad`) and scanner-context retest (`56aadeb`) recorded. Not qualification: B2/B3 calibration, cells creation, systemd leaf, signed host profile, registration, E2E and drain remain open. |
| Research Claims — last | Code-only components committed: `evalscore` scoring/dispositions (`4a6a849`), `usdledger` micro-USD ledger (`95f6ab6`), `researchprofile` frozen OpenRouter profile (`94484cf`, prices/limits still unset), `evalcorpus` manifest validator/allocator (`81abc77`), `effectoracle` independent E04 inotify oracle (`1384e9a`). Missing: corpus content, calibration harness, B/V runners, attacker library, E06 route matrix, frozen prices and cost plan, pre-registration. |
| Hygiene | Stale docs/dead code removed (`dc985e2`, `5ed2295`, `5bd16fd`). Ten other-session docs remain modified and unstaged. In progress: full-module race gate on HEAD, drain-verifier plan. |

Needs your decision (all code-side defaults are fail-closed and unfrozen):
- evalscore: label for a compliant task where C succeeds, plus 19 other OPEN points.
- Ledger: unit vs request, stop-rule enforcement, where the head hash is anchored; engineering-ledger unit for OpenCode Go.
- evalcorpus: seed value/rule (SHA-256 length-prefixed is a placeholder), cold stratum per class vs source, root uniqueness global vs per class, source-pool selection rule, EFF-* predicate vocabulary.
- effectoracle: directory creation/rename makes the window evidence_lost; canaries are real files in the root; path vs descriptor-relative snapshots; timing excluded from digest.
- researchprofile: verify OpenRouter routing field names at freeze; pin one provider or price-cap all.
- OSF/Zenodo registration versus the 2026-10-14 decision.

Engineering: OpenCode Go. Final research: OpenRouter DeepSeek V4.1 Flash, US$5 total cap. Independent runtime/effect oracles retained. Original/seed VMs preserved.
