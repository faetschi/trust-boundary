# TBound — current state

Updated 2026-10-23. Freeze **F7 = 608f397** is installed on the guest (172.31.134.61). Engineering route OpenCode Go. Green results verify code only: G1 false, H1 unexecuted.

**Headline: the full G1 run (read, edit, bash, publish) still fails at the bash step, but each freeze gets one stage further.** Read and edit work. Each real-guest attempt found one more check that cannot pass for an unprivileged, rootless service:

| Freeze | Where bash stopped | Fixed in the next freeze |
| --- | --- | --- |
| F4 | executable "not root-owned" inside the cell | ownership via the cell's uid_map |
| F5 | read-only check never matched the root mount | kernel `fstatfs` read-only check on the opened file |
| F6 | post-command namespace scan denied on root's processes | scan skips other users' processes |
| F7 | "lacks registered signed D09 settlement evidence" (command audit empty) | under investigation (Opus) |

Each failed run spends a G1 admission id; the guest state is archived (moved aside, nothing deleted) before the next install.

| Milestone | State |
| --- | --- |
| System | Built. Real-host hardening ongoing (F4–F7 above, plus publication fence fixes, journal lock, audit fixes). |
| Measurement | Observers built; 18 of 22 EVQ met offline. |
| Research enablement | Built. Pilot budgets fixed (2N+1); retry margin pending for the pilot freeze. |
| G1 (first full governed run) | Not passed yet. |
| Route probes, last 4 EVQ, pre-registration, pilot, H1 | After G1. |

Needs you: pilot corpus review with your advisor; pre-registration decisions.

Next: settlement-evidence fix → gate + review → F8 → archive F7 state → G1.
