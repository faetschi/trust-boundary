# TBound — current state

Updated 2026-10-20 (night). Engineering route OpenCode Go only. Green results verify code only: G1 false, H1 unexecuted.

**Headline: a governed smoke completed on the real guest** (dry run, throwaway keys): service start, signed cell, Landlock + TCB proof, Pi in the cell, broker, real OpenCode Go exchange, `read` allowed, receipt `status: completed`, disposition `NO_DIFF`. Getting there took 30 real-host fixes that the offline gate could not see.

| Milestone | Current state |
| --- | --- |
| System | 30 real-host defects fixed red-first (accounts, mount namespaces, cgroup delegation, rootless podman, entrypoint, proof pipe, provider roles + bounded retry, workflow checkpoint, canonical live root). Merged for review on `integ/real-host-20261020` (`1f69281`); Codex review and full gate running. |
| Measurement | All observers have production sources (unchanged). |
| Research enablement | C, P, B, V, adaptive attacker, per-trial reset, task oracle, `tbound-run`. V podman flags proven under no-new-privs. |
| Evidence (EVQ / routes) | 18 of 22 EVQ offline; the 4 guest artifacts are now reachable (G1, probes). Route report 2 of 4 cells. |
| Guest (ABI 8, 172.29.201.195) | Real: K0 keys, P8–P13, R1 (S1, S2), SSH-1..3, NS-1, P12 key, Spike A, guest config fixes. Dry-run artifacts (throwaway key, dev image) are rolled back before the real session. |
| Experiment | Pilot corpus validated except reviews; G1 approved. |

Provider note: OpenCode Go's upstream is flaky (503 / 429 bursts); the broker now retries pre-effect 5xx (each attempt a reserved host call).

Next: Codex review + gate → merge → new freeze F′ (rebuild, S5, I1, R2/R5 = S3/S4) → RC-1/Z1 (root console, needs you at the VM console) → real smoke → G1 / probe runs → guest EVQ artifacts → owner + advisor pilot review → pre-registration → pilot.
