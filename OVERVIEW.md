# TBound — current state

Updated 2026-10-24. Last freeze on the guest: **F13 = 6162e3c** (guest 172.24.152.73). Engineering route OpenCode Go.

**Headline: the governed path works on the real guest; the pilot is not ready yet.** G1 (read, edit, bash, publish), the route probe and the direct-runtime probe pass on F11-F13 with correct Landlock confinement. At F13, EVQ passes 21 of 22 obligations (net-sink needs a corpus-mode trial reset) and the route report is complete. Two service-mode defects (a DENY ends the conversation; a recovery record-kind mismatch) must be fixed before the pilot.

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
| F13 | the adapter change (denial hints) required a rebuilt runtime bundle; the kit gained a rebuild step and a guest runtime swap |

| Milestone | State |
| --- | --- |
| System | Built; G1 and both probes pass on the real guest. |
| Measurement | F13: EVQ 21/22 (net-sink open until corpus mode); route report complete. |
| Research enablement | Static denial hints (H1 treatment) merged. C-DH parked until after H1. |
| Pilot | Needs advisor review, protocol decisions, then corpus-mode checks (reset, net-sink, no-reap, V). |
| Pre-registration, H1 | After the pilots (caps and costs come from the pilot). |

Needs you: advisor review of the pilot manifest, the D26 scope cut and the residual-channel claim; pre-registration decisions.

Next: [TASK_STATE.md, "Next (thesis lane)"](TASK_STATE.md#next-thesis-lane).
