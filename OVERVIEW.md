# TBound — current state

Updated 2026-10-20 (evening). Branch `codex/tbound-prototype` at `c7f9e9c` (local; not pushed). Engineering route OpenCode Go only. Green results verify code only: G1 false, H1 unexecuted.

**Headline: the first real-guest dry run found that F (`c7f9e9c`) cannot run on a real host.** Fixes are on `fix/real-host-accounts` (17+ commits, gate 1303/0/8), a new freeze follows. With all fixes the governed chain now runs on the guest end to end up to the first released provider response: service start, cell launch, signed image, Landlock, TCB proof, Pi in the cell, broker, real OpenCode Go exchange (HTTP 200, tool call).

| Milestone | Current state |
| --- | --- |
| System | Code complete offline; 27 real-host defects found on the guest (accounts, mount namespaces, cgroup delegation, rootless podman, entrypoint, proof pipe, provider roles, workflow checkpoint). 1–24 fixed red-first, 25–27 in progress. |
| Measurement | All observers have production sources. Unchanged by the dry run. |
| Research enablement | C, P, B, V, adaptive attacker, per-trial reset, task oracle, `tbound-run`. V podman flags proven under no-new-privs. |
| Evidence (EVQ / routes) | 18 of 22 EVQ offline; 4 need guest artifacts (now reachable once 25–27 land). Route report 2 of 4 cells. |
| Guest (ABI 8, 172.29.201.195) | Real: keys (K0), S5 image signature, P7–P13, R1 (S1, S2), SSH-1..3 forced keys, NS-1 net sink, P12 OpenCode Go key, Spike A. Guest config fixed (nsswitch, subuid, podman configs). Dry-run state uses a throwaway key; rolled back before the real session. |
| Experiment | Pilot corpus validated except reviews; G1 approved. |

Open PRs: #16 and #17 merge after the session (no blocker for it); #10 superseded (close). The cgroup-kill kernel defect does not affect production paths.

Next: fixes 25–27 → second dry run to a completed smoke → Codex review → new freeze F′ (rebuild, re-sign S5, S1–S4) → real smoke → G1 / probe runs → guest EVQ artifacts → owner + advisor pilot review → pre-registration → pilot.
