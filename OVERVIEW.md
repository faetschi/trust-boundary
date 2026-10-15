# TBound — current state

Updated 2026-10-15 (evening). Branch: `codex/tbound-prototype`, HEAD `7520458`. Priority: thesis completion. Green results verify code only: G1 false, H1 unexecuted, containment not established.

| Milestone | Current state |
| --- | --- |
| Recovery / D14 | Logical owner headroom gate committed (`b089a9d`) and pinned by a tested invariant: every non-terminal commit is refused while an effect is Reserved (`852f276`). Race gates: workflowrecovery 68 pass / 1 skip, cmd/tbound 138 pass / 2 skip; the opt-in ext4 capacity experiment is unverified. Open: physical reserve, fsync/ENOSPC/power-loss, root terminal anchor and shared owner file ungated, real owner/cell drain, retained restart. |
| Model pin | Pi and adapter now derive from the broker-approved `deepseek-v4.1-flash` (`bd6de94`). The runtime-bundle digest in the ABI 8 provisioning receipt is stale; re-measure and re-sign during provisioning. |
| ABI 8 guest | Exact ABI 8 measured. Receipts recorded: xattr source-scope census (`84cfaad`), scanner-context retest in WSL root-to-nonroot (`56aadeb`). Not qualification: B2/B3 calibration, cells creation, systemd leaf, signed host profile, registration, E2E and drain remain open; xattr completeness authority not granted. |
| Research Claims — last | Code-only components committed: scoring/disposition module `evalscore` (`4a6a849`, 20 thesis points left open, so `rule_satisfied` stays false until decided) and fail-closed micro-USD ledger `usdledger` (`95f6ab6`). In progress: corpus manifest validator, research provider profile. Missing: corpus content, calibration harness, B/V runners, independent E04/E06/effect oracles, frozen prices and cost plan, pre-registration. |
| Hygiene | Stale `adapter/DESIGN.md` removed. Ten other-session docs remain modified and unstaged. |

Needs your decision: label for a compliant task where C succeeds (and other evalscore OPEN points); ledger unit vs request, stop-rule enforcement, and where the head hash is anchored; OSF/Zenodo registration versus the 2026-10-14 decision; engineering-ledger unit for OpenCode Go.

Engineering: OpenCode Go. Final research: OpenRouter DeepSeek V4.1 Flash, US$5 total cap. Independent runtime/effect oracles retained. Broader product features deferred. Original/seed VMs preserved.
