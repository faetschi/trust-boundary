# TBound — current state

Updated 2026-10-17. Branch `codex/tbound-prototype`, HEAD `1a4cf40`. Priority: thesis completion. Green results verify code only: G1 false, H1 unexecuted, containment not established. Prototyping uses OpenCode Go only; no OpenRouter calls yet.

| Milestone | Current state |
| --- | --- |
| Recovery / D14 / D20 | Drain proof in production (never-exposed owners via `NeverBound`). Operator recovery `tbound abandon-unknown` only after durable reconciliation (owner drained with proof, publications resolved, provider disposition recorded; never claims remote quiescence). Open: physical reserve, power-loss, root anchor; one D14 test flake seen once, not reproduced. |
| ABI 8 guest | Reachable (172.22.198.27, pinned key). Node bound read-only; service and socket installed; runner creates `cells`. Repo-ready, not yet on the guest: read-only mounts for runner, podman, crun and cell assets; reproducible cell image with owner-side cosign signing; stable cgroup digest; bounded xattr authority; symlink-safe key files. Runbook `infra/guest/PROVISIONING-SIGNING.md` ends in the first E2E smoke over OpenCode Go. Needs your signing session. |
| Research route | Implemented, unused until you switch: frozen profile with price ceilings, reasoning, allowed response models, framing reserve; ledger reserves before send, refuses oversize input, stops on over-ceiling cost. Rollback-anchor owner still open. |
| Research Claims | Runner: 1,935-unit plan, Prepare/Execute contract, crash-safe journal, rerun rule (97 headroom), partial export never overstates. Oracles: wsoracle v7 (WS-01..04 attribution, E04 seven-point comparison, `indeterminate`, root identity, provenance). Service admits trials from a signed corpus manifest (`case_id/role/attempt`, pilot/thesis quota profile). Tools: `tbound-profile corpus-digest`, `preregistration-record`. Missing: guest-backed executors, NET/PROC oracles (guest), corpus content, pilot run, freeze + pre-registration record, G0/G1. |
| Verification | Every increment reviewed adversarially by Codex GPT-6.1-Sol; all confirmed findings fixed or recorded as explicit trust-boundary decisions (executors are trusted evidence producers; publication exemptions need a service-receipt anchor, being added now). Last full gate 2702 pass / 10 skip / 0 fail. |

Needs your decision: ledger rollback-anchor owner; P/C unit definition; corpus content and its review; when to freeze and run the signing session on the guest.

Engineering: OpenCode Go. Final research (later): OpenRouter DeepSeek V4.1 Flash, US$5 cap, peak $0.30/$1.20 per M tokens.
