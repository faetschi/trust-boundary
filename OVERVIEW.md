# TBound — current state

Updated 2026-10-21. Frozen commit **F2 = feb5c7b** (codex/tbound-prototype). Engineering route OpenCode Go only. Green results verify code only: G1 false, H1 unexecuted.

**Headline: F2 is built, signed and installed on the guest, but the G1 run was refused again (`prior-workflow-unsettled`).** The defect #31 fix in F2 does not settle the smoke workflow that F1 completed; root cause under investigation.

| Milestone | Current state |
| --- | --- |
| System | F2 frozen (P1/P3 reproducible, bundle reused, cosign signed). Defect #31 still open on the real guest. |
| Measurement | All observers have production sources. EVQ capture of F2 done ($H/cf2). |
| Research enablement | C, P, B, V, adaptive attacker, per-trial reset, task oracle, `tbound-run`. |
| Evidence (EVQ / routes) | 18 of 22 EVQ offline. G1, route-probe and direct-runtime runs blocked by #31. |
| Guest (ABI 8, 172.29.194.24) | F2 runner, tbound-profile and image installed; calibrations verified; G1 registration signed and installed. Sudo narrowed again (tbound-op NOPASSWD + password sudo). |
| Experiment | Pilot review packet + pre-registration draft ready for you and your advisor (`review-artifacts/`). |

Needs you: pilot corpus review with your advisor (`review-artifacts/pilot-review-packet-20261021.md`), decisions in the pre-registration draft.

Next: root-cause #31 → recovery without a new freeze if the code allows, else fix + F3 → G1 → route probes → guest EVQ artifacts → reviewed manifest → pre-registration → pilot.
