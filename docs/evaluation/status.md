# Current status

Updated 2026-10-27. This page is the short gate summary. [TASK_STATE](../../TASK_STATE.md)
is the working log with decisions, blockers, and next steps. History before this
date is in Git.

## Claim boundary

- **G1 passes on the real guest.** Read, edit, bash, and publish work at freeze
  **F13 = `6162e3c`** (fresh evidence), and the route and direct-runtime probes
  pass with correct Landlock confinement.
- **H1 is unexecuted.** No PVR or ORR value exists. G1, a green test suite, a
  fixture, a capability probe, or a receipt is not an H1 result.
- **F14 content is merged but not frozen.** It carries the fixes for the two
  service-mode defects (a DENY ending the conversation; the recovery record-kind
  mismatch) and the `corpus-check --guest-evidence` input.
- **The D09 denied-after-effect settlement is merged** on the thesis branch
  (`f483fad`).
- **The pilot is not ready.** Construct-review sign-off
  (`feat/construct-review-signoff`) is not merged, so the panel prep must be
  rebuilt and the panel has not run live. EVQ at F13 passes 21 of 22 obligations
  (net-sink open until corpus mode); the route report is complete (4 of 4 routes).
- **The final-profile containment study is not complete.** The F11-F13 guest
  checks are real-host system evidence, not the final frozen-profile result.
- **C-DH is parked.** H1 stays Configuration C with the static
  permitted-alternative table. The C-DH code is merged but off.

## Current checkpoint

| Area | State |
| --- | --- |
| Guest | F13 = `6162e3c`, installed and frozen |
| Code | F14 content merged, not frozen; product lane synced at `7ceeff6` |
| Governed path | G1 and both probes pass; each new freeze repeats them with fresh evidence |
| Measurement | F13: EVQ 21/22 (net-sink open until corpus mode); route report complete |
| Construct review | Sign-off tooling not merged; panel prep stale, so the panel has not run live |

## Open gates, in order

1. Confirm the D26 scope cut and the residual-channel claim with the advisor.
2. Finish the pilot reviews and case corrections; freeze the pilot protocol,
   provider, outage rules, and initial safety ceilings.
3. Done at F13: freeze, reinstall, G1 and both probes with fresh evidence. The
   service-defect fixes and the corpus-check guest-evidence input sit in F14
   content; the F14 freeze is pending.
4. Enter corpus mode and verify reset, net-sink, no-reap, and V; require complete
   EVQ and route reports.
5. Run the engineering, compliant, and adaptive-cap pilots; derive caps and costs
   from the retained records.
6. Build and independently review the full corpus; complete every required
   channel witness and preserve the cold partition.
7. Freeze the final artifact, corpus, caps, and analysis; complete immutable H1
   registration.
8. Run H1 under the registered rules and report the result without changing
   denominators.

The owner must approve privileged guest writes, signing, provisioning, and any
paid provider call. The next human actions are the advisor review, the panel run
after its prep is rebuilt, the pilot protocol decisions, and the coordinated
signing session.

## Related

- [Architecture](../runtime/architecture.md): authority boundaries and seams.
- [Publication contract](../runtime/publication-contract.md): how changes reach the live root.
- [Panel guide](../guides/panel-run.md): the blinded construct review.
- [Decision records](../decisions): ADRs and their status.
- [Working log](../../TASK_STATE.md): the detailed checkpoint and owner decisions.
