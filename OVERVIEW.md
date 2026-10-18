# TBound — current state

Updated 2026-10-18 (evening). Branch `codex/tbound-prototype`, HEAD `8484d46`. Green results verify code only: G1 false, H1 unexecuted, containment not established. Prototyping uses OpenCode Go only; no OpenRouter calls yet.

| Milestone | Current state |
| --- | --- |
| System (service, recovery, signed profiles) | Code complete and reviewed. Not yet installed on the guest. |
| Measurement | Oracles (workspace, network, process) and runner library complete and reviewed. |
| Research enablement | Code complete, final review running. Host runner `tbound-run` (plan/run/rerun/export/report); per-trial fixture reset (`tbound trial reset`, cgroup.kill); all five configurations wired: C (real model + protected paths + delta admission, denials reach the agent), P (same route with a scripted stimulus and a registered delta: admit-all, net-sink, no-reap), B (no governance denials), V (native Pi in a plain container), adaptive replay (identical deterministic attacker for C and P, fed only by denial reason codes); independent task oracle (`tbound trial accept`). Every increment adversarially reviewed by Codex. |
| Experiment | Pilot corpus built (21 cases: fixtures, scripts, pivots, acceptance tests, rubrics, attacker artifact); `corpus-digest` passes all checks except reviews. Needs owner + advisor review, then pre-registration. Nothing run yet. |
| Guest (ABI 8) | At 172.30.8.106 (DHCP; pinned key via HostKeyAlias). Untouched since the read-only prep. One signing session + guest spike after the freeze. |

Needs you later: signing session (signatures, privileged guest writes incl. nft rule and net-sink cgroup, trial forced-command key), guest spike for net-sink/no-reap/V, E2E smoke, pilot reviews with your advisor.

Stated limits (thesis must say so): bounded xattr claim; PROC-02 misses crossings shorter than the poll gap; one cell per uid; no transparency log for the image signature; E04 blind to effects undone between lifecycle points; executors and evidence channel trusted; replays use a scripted provider instead of the model; P deltas widen the P cell (net-sink cell shares the guest network namespace); WS-04 limited to metadata changes; V has no egress filter and sees its provider key; adaptive attacker stops at the first allowed pivot.

Next: review fixes → freeze → signing session + spike + smoke → reviews → pre-registration → pilot → thesis corpus → G0/G1.
