# TBound — current state

Updated 2026-10-23. Last freeze on the guest: **F12 = eed7722** (guest 172.24.152.73). Engineering route OpenCode Go.

**Headline: the governed path works on the real guest; the pilot is not ready yet.** G1 (read, edit, bash, publish), the route probe and the direct-runtime probe pass on F11/F12 with correct Landlock confinement. The EVQ dry run passes 18 of 22 items; the rest need fresh artifacts from the next freeze and a netsink artifact from a trial reset.

What the real guest exposed, freeze by freeze (each fixed with a regression test):

| Freeze | Real-host bug |
| --- | --- |
| F4-F6 | ownership via uid_map, read-only check on the root mount, namespace scan of other users' processes |
| F7 | Bash evidence compared host and cell mount IDs |
| F8 | publication never attested quiescence |
| F9 | (G1 passed) route probe: nil vs empty change list broke the evidence binding |
| F10 | Landlock restricted only one thread (wrong flag constant); prior settlement used the wrong epoch |
| F11 | direct-runtime probe read its control file at the wrong path |
| F12 | freeze kit reused stale guest evidence |

| Milestone | State |
| --- | --- |
| System | Built; G1 and both probes pass on the real guest. |
| Measurement | 18/22 EVQ in the dry run; full EVQ + route report needed at the pilot freeze. |
| Research enablement | Static denial hints (H1 treatment) merged. C-DH parked until after H1. |
| Pilot | Needs advisor review, protocol decisions, then corpus-mode checks (reset, net-sink, no-reap, V). |
| Pre-registration, H1 | After the pilots (caps and costs come from the pilot). |

Needs you: advisor review of the pilot manifest, the D26 scope cut and the residual-channel claim; pre-registration decisions.

Next: [TASK_STATE.md, "Next (thesis lane)"](TASK_STATE.md#next-thesis-lane).
