# TBound — current state

Updated 2026-10-19. Branch `codex/tbound-prototype`, HEAD after `051cfe0` (local only; do not push — origin diverged, backup `backup/tbound-prototype-3aed7bf`). **Paused at the owner's request** pending the merge of `integration/prototype-merge` (oracle calibration fixes wsoracle/v8, procoracle/v4 + origin's commits). Green results verify code only: G1 false, H1 unexecuted, containment not established. Engineering route OpenCode Go only.

| Milestone | Current state |
| --- | --- |
| System | Code complete, reviewed. Not installed on the guest. |
| Measurement | Oracles + runner complete. Gap: EFF-TRANSIENT, WS-05, WS-06 have no production source (observer coverage can't be complete) — first item after the merge. |
| Research enablement | All five configurations wired (C, P scripted replay + deltas, B, V container, adaptive attacker), per-trial reset, task oracle, `tbound-run`. Every increment Codex-reviewed. |
| Evidence (EVQ / routes) | 18 of 22 EVQ obligations established offline (bound to the frozen commit via `evq-capture`); 4 need guest artifacts (real provider exchange, net sink, process oracle, Linux capability). Route report: 2 of 4 cells covered; bash-view and direct-runtime need guest probe runs (producers exist). |
| Experiment | Pilot corpus built and validated except reviews; G1 engineering case drafted (needs your approval). |
| Guest (ABI 8) | 172.30.8.106; nft + Landlock present. Signing runbook consolidated (5 signatures + 2 deferred). |

After the merge: rebuild + re-pin digests, observer-coverage sources, runbook gaps (cosign key generation, root console after Z1, R5-5 alias), freeze → signing session + spikes + smoke + guest evidence → owner + advisor reviews → pre-registration → pilot → thesis corpus → G0/G1.

Thesis amendments needed: D32 feedback allowlist (+ command_failed boolean), retry budget semantics, mismatched-budget pairs invalid; configuration definitions (P scripted replay, B no denials, V container); new stated limits (see TASK_STATE.md).
