# TBound — current state

Updated 2026-10-20. Branch `codex/tbound-prototype` (local; merged with origin at `d019e94`; do not push without the owner). Tip: see `git log -1`. Last full gate `3ecde15`: 4085 pass / 0 fail / 24 skip (pinned Go 1.27.1, -race, build/vet/gofmt/Windows vet clean); webchat extension 174/0/0, boundary script OK; web frontend npm gate not run (no offline Node deps). Green results verify code only: G1 false, H1 unexecuted. Engineering route OpenCode Go only.

| Milestone | Current state |
| --- | --- |
| System | Code complete, reviewed. Not installed on the guest. |
| Measurement | Oracles (wsoracle/v8, netoracle/v3, procoracle/v4) and all observer sources wired in production: E04, NET, PROC, WS-05/06 from publication evidence, EFF-TRANSIENT via inotify transient-watch. |
| Research enablement | C, P (scripted replay + deltas), B, V, adaptive attacker, per-trial reset, task oracle, `tbound-run`. |
| Evidence (EVQ / routes) | 18 of 22 EVQ obligations established offline (commit-bound capture); 4 need guest artifacts. Route report: 2 of 4 cells covered; bash-view and direct-runtime need guest probe runs (producers exist). Guest evidence bound to the frozen source via ProfileBinding. |
| Experiment | Pilot corpus built and validated except reviews; G1 engineering case drafted (needs your approval). |
| Guest (ABI 8) | 172.30.8.106; nft + Landlock present. Runbook consolidated: 13 owner signatures, K0 key generation, RC-1 root console before Z1, re-registration cycle RG-1..8. |

Freeze blockers: Codex review of `9ed7458..3ecde15` (Codex limit until 02:57; Sonnet reviews done and fixed); then F0 freeze → signing session (owner) → guest spikes, smoke, G1, probe runs, guest EVQ artifacts → owner + advisor pilot reviews → pre-registration → pilot → thesis corpus → G0/G1.

Thesis amendments needed: D32 feedback allowlist (+ command_failed), retry budget semantics, mismatched-budget pairs invalid; configuration definitions (P scripted replay, B no denials, V container); stated limits incl. transient-watch canaries written by the evidence channel, symlink-free probe target, AppArmor/seccomp as co-causes for route denials.
