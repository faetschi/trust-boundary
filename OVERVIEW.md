# TBound — current state

Updated 2026-10-21 evening. Freeze **F3 = ea59706**, installed on the guest. Engineering route OpenCode Go only. Green results verify code only: G1 false, H1 unexecuted.

**Headline: defect #31 is fixed on the real guest. G1 was admitted and ran, but its first model turn failed.** The F3 service settled the earlier smoke run (`pi_workflow_terminal` written) and started G1. OpenCode Go answered with a streaming response that the bridge could not capture (`missing field "finish_reason"`), so the turn was withheld and G1 ended `unknown`. Root cause and recovery under investigation; the fix goes into F4.

| Milestone | Current state |
| --- | --- |
| System | F3 frozen, signed, installed (capture 4224/0/24). F4 in progress on `next/f4` (gate 4234/0/24): refusal hints, cgroup-test guard, fresh net-sink cgroup per reset, flaky-test fix; next: streaming-response capture fix, clean settlement of a failed launch (#33), direct-runtime launch proof. |
| Measurement | All observers have production sources. Net-sink evidence: fresh cgroup per reset in F4 (kernel 7.0 cgroup bug). |
| Research enablement | C, P, B, V, adaptive attacker, per-trial reset, task oracle, `tbound-run`. |
| Evidence (EVQ / routes) | 18 of 22 EVQ offline. G1 needs a successful run on F4; route probe, direct-runtime probe and guest EVQ follow. |
| Guest (ABI 8, 172.29.194.24) | F3 installed, sudo narrowed (tbound-op + password sudo). One stuck `unknown` G1 request to reconcile. |
| Host | WSL moved to F: (C: was full: the cause of today's WSL hangs). |
| Product | Web chat frozen for the thesis (CLI only). Post-thesis backlog: per-run admission without re-freezing, `provision plan` tool, guided doctor (`review-artifacts/product-backlog-20261021.md`). |
| Experiment | Pilot review packet + pre-registration draft wait for you and your advisor (`review-artifacts/`). |

Needs you: pilot corpus review with your advisor; pre-registration decisions.

Next: stream-capture fix + #33 + launch proof on F4 → Codex review → freeze F4 → reconcile the stuck G1 → G1 → route probe → direct-runtime probe → guest EVQ → pre-registration → pilot.
