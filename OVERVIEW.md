# TBound — current state

Updated 2026-10-21. Freeze **F3 = ea59706** (codex/tbound-prototype). Engineering route OpenCode Go only. Green results verify code only: G1 false, H1 unexecuted.

**Headline: defect #31 is fixed in F3, but the F3 signing is blocked by the host disk.** G1 at F2 was refused because a prior run was checked against the new run's bindings; the fix (plus #32, a store record looked up in the wrong journal) passed an offline replay against the real guest state and three Codex reviews. The WSL "hangs" were C: running full (the WSL disk lives there); WSL is being moved to F:.

| Milestone | Current state |
| --- | --- |
| System | F3 built reproducibly (runner, profile, image; bundle reused). Test capture and image signing pending (WSL move). Next freeze F4 in progress: refusal hints (cause + next action), cgroup-test guard, net-sink cgroup per reset, flaky-test fix. |
| Measurement | All observers have production sources. Net-sink evidence must be collected right after the first trial reset (kernel 7.0 cgroup bug). |
| Research enablement | C, P, B, V, adaptive attacker, per-trial reset, task oracle, `tbound-run`. |
| Evidence (EVQ / routes) | 18 of 22 EVQ offline. Guest runs prepared: G1, route probe, direct-runtime probe, EVQ collection. Open: the direct-runtime probe may not journal its launch proof (under investigation). |
| Guest (ABI 8, 172.29.194.24) | F2 installed, sudo narrowed (tbound-op + password sudo). F3 install, old-G1 recovery and G1 rerun follow the signing. |
| Product | Compared with specpi-jev-guard: keep the boundary and evidence; post-thesis: per-run admission without re-freezing, a `provision plan` tool, a guided doctor. Web chat frozen for the thesis (CLI only), to be extracted after it. |
| Experiment | Pilot review packet + pre-registration draft wait for you and your advisor (`review-artifacts/`). |

Needs you: pilot corpus review with your advisor; pre-registration decisions.

Next: WSL on F: → F3 capture + signing → install F3 on the guest → G1 → route probe → direct-runtime probe → guest EVQ → reviewed manifest → pre-registration → pilot.
