# TBound — current state

Updated 2026-10-20. Branch `codex/tbound-prototype` (local; origin merged at `d019e94`; do not push without the owner). Code tip `9a75f3f`: clean-room gate 4118 pass / 0 fail / 24 skip (pinned Go 1.27.1, -race; build, vet, gofmt, Windows vet clean); webchat extension 174/0/0, boundary script + self-test OK; web frontend npm gate not run (no offline Node deps). All Codex adversarial reviews closed (last: window fences OK). Green results verify code only: G1 false, H1 unexecuted. Engineering route OpenCode Go only.

| Milestone | Current state |
| --- | --- |
| System | Code complete, reviewed. Not installed on the guest. |
| Measurement | All observers have production sources: E04, NET, PROC, WS-05/06 (publication evidence), EFF-TRANSIENT (inotify transient-watch, window placed by publication fences, effectoracle/v3). |
| Research enablement | C, P (scripted replay + deltas), B, V, adaptive attacker, per-trial reset, task oracle, `tbound-run`. |
| Evidence (EVQ / routes) | 18 of 22 EVQ obligations established offline (commit-bound capture); 4 need guest artifacts. Route report 2 of 4 cells; bash-view and direct-runtime need guest probe runs. Guest evidence bound to frozen source, base template, image, runtime bundle and runner digest. |
| Experiment | Pilot corpus validated except reviews; G1 case drafted — approve before F0. |
| Guest (ABI 8) | 172.30.8.106. Runbook ready for one session: K0 keys, F0, 13 owner signatures, RC-1 root console before Z1, re-registration cycle RG-1..8. |

Next (needs you): approve G1 (before F0) → F0 freeze → signing session → spikes, smoke, G1, probe runs, guest EVQ artifacts → owner + advisor pilot reviews → pre-registration → pilot → thesis corpus → G0/G1.

Thesis amendments needed: D32 feedback allowlist (+ command_failed), retry budget semantics, mismatched-budget pairs invalid; configuration definitions (P scripted replay, B no denials, V container); stated limits incl. transient-watch canaries written by the evidence channel, fence dependency on the live root's `lost+found`, symlink-free probe target, AppArmor/seccomp as co-causes of route denials.
