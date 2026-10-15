# TBound - current state

Updated 2026-10-15. Branch: `codex/tbound-prototype`. Priority: thesis completion.

| Milestone | Current state |
| --- | --- |
| Recovery/provider foundations | Committed cumulative restart/retry accounting, historical checkpoint verification and bounded engineering exchanges. |
| SQLite safety increment | Verified isolated source: read-only geometry refusal, per-connection ceilings and retained-WAL recovery controls. Linux Go1.27.1 build/vet/format pass; unit and race each 1,229 pass events, zero failures, eight skipped tests. Parent matched four source hashes and 30 logs. This does not qualify D14. |
| D09 scanner path | Previously skipped test now passes under the required unprivileged NNP/empty-group context in WSL. Piruntime has 121 top-level passes, no skips in that context. |
| Durable service settlement | NO_DIFF patch passes focused/cmd tests but lacks full fresh-process admission proof. Follow-up implementation active. Unknown outcomes remain blocked. |
| ABI 8 deployment | Exact ABI8 measured repeatedly over pinned SSH on 7.0.0-38-generic. Guest running; candidate profile unmeasured/unregistered. Runtime/cell provisioning active. No real governed E2E yet. |
| D14/D20 qualification | Reachable production capacity, physical reserve, real storage faults, owner/cell drain and retained restart remain open. Production-shaped capacity and isolated ext4 task active. Static size estimates remain unadopted. |
| Research Claims - last | Open. Complete runtime/research gates and thesis reconciliation before freezing deployment, corpus, independent oracles, calibration, scoring and model/cost plan. G1/H1 are not established. |

Owner-incarnation caveat: intermittent refusals appeared in earlier cmd and a separate no-diff module-unit run; the successful isolated SQLite race run does not resolve their cause. Other cmd/packaging work is excluded from that check.

Engineering: OpenCode Go. Final research: OpenRouter DeepSeek V4.1 Flash, US$5 total cap. User/advisor construct review; independent runtime/effect oracles retained. Broader product features deferred; original VM, seed and other-session docs preserved.
