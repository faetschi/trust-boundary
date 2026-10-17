# TBound — current state

Updated 2026-10-17. Branch `codex/tbound-prototype`, HEAD `82e6348`. Priority: thesis completion. Green results verify code only: G1 false, H1 unexecuted, containment not established. Prototyping uses OpenCode Go only; no OpenRouter calls yet.

| Milestone | Current state |
| --- | --- |
| Recovery / D14 / D20 | Drain proof in production, never-exposed owners drain via `NeverBound` (workflow and scope bound). Open: physical reserve, power-loss, root anchor, the crash window between scope remove and bind (refused), and a rare D14 headroom-gate test flake (being investigated). |
| ABI 8 guest | Reachable (172.22.198.27, pinned key). Node bound read-only; service and socket templates installed and verified; runner creates `cells`. Repo-ready, not yet run on the guest: read-only mounts for runner, podman, crun and cell assets; reproducible cell image (digest-pinned base, offline build) with owner-side cosign signing; cgroup digest binding stable facts; bounded xattr authority; key files read without symlinks. Runbook `infra/guest/PROVISIONING-SIGNING.md`: 5 owner signatures, ~18 privileged writes, then E2E smoke over OpenCode Go. Needs your approved signing session. |
| Research route | OpenRouter route implemented but unused until you switch: frozen profile (price ceilings, reasoning, allowed response models, framing reserve), US$ ledger reserves before send, refuses oversize input, stops on over-ceiling cost. Rollback anchor owner still open. |
| Research Claims | Runner (`internal/runner`): deterministic 1,935-unit plan, crash-safe journal, rerun rule, budget stop, evalscore export. Service admits trials from a signed corpus manifest (`case_id/role/attempt`). Scoring, report (fail-closed), corpus, calibration, attacker, oracles (wsoracle v3, effectoracle) all committed. Missing: guest executors, NET/PROC oracles (guest), corpus content, pilot, freeze and pre-registration, G0/G1. |
| Verification | Full gate on `82e6348`: 40 packages, 2462 pass / 10 skip / 0 fail. Codex reviews done for drain, wsoracle, evalreport, guest code and research route (all findings fixed); runner and corpus admission pending. |

Needs your decision: ledger rollback-anchor owner; P/C unit definition; corpus content and review; when to freeze and sign (signing session on the guest).

Engineering: OpenCode Go. Final research (later): OpenRouter DeepSeek V4.1 Flash, US$5 cap, peak prices $0.30/$1.20 per M tokens.
