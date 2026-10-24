# Current status

Updated 2026-10-24. This page is the short gate summary. [OVERVIEW](../OVERVIEW.md)
is the one-screen state; [TASK_STATE](../TASK_STATE.md) is the working log with
decisions, blockers, and next steps. History before this date is in Git.

## Claim boundary

- **G1 passes on the real guest.** Read, edit, bash, and publish work at freeze
  **F12 = `eed7722`**, and the route and direct-runtime probes pass with correct
  Landlock confinement.
- **H1 is unexecuted.** No PVR or ORR value exists. G1, a green test suite, a
  fixture, a capability probe, or a receipt is not an H1 result.
- **The pilot is not ready.** The EVQ dry run passed 18 of 22 items. The rest
  need fresh artifacts from the next freeze and a net-sink artifact from a trial
  reset.
- **The final-profile containment study is not complete.** The F11/F12 guest
  checks are real-host system evidence, not the final frozen-profile result.
- **C-DH is parked.** H1 stays Configuration C with the static
  permitted-alternative table. The C-DH code is merged but off.

## Current checkpoint

| Area | State |
| --- | --- |
| Guest | F12 = `eed7722`, guest `172.24.152.73` |
| Governed path | G1 and both probes pass; each new freeze must repeat them with fresh evidence |
| Measurement | EVQ dry run 18/22; full EVQ and route report needed at the pilot freeze |
| Pilot corpus | Re-based on established benchmark items; 11 adversarial, 20 compliant, 2 ambiguous; manifest still has review TODOs |
| Panel | `tbound-panel` is ready; the owner runs it after funding and model choice |
| Provider | OpenCode Go for engineering; the frozen OpenRouter route for the provider-dependent H1 pilot |
| Denial hints | Static table (H1 treatment) merged; generated hints are post-thesis |

## Open gates, in order

1. Confirm the D26 scope cut and the residual-channel claim with the advisor.
2. Finish the pilot reviews and case corrections; freeze the pilot protocol,
   provider, outage rules, and initial safety ceilings.
3. Freeze and pin the selected code; reinstall it and repeat G1 and both probes
   with fresh evidence.
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
paid provider call. The next human actions are the advisor review, the pilot
protocol decisions, and the coordinated signing session.

## Related

- [Architecture](architecture.md): authority boundaries and seams.
- [Publication contract](publication-contract.md): how changes reach the live root.
- [Panel guide](panel-run.md): the blinded construct review.
- [Decision records](decisions/): ADRs and their status.
- [Working log](../TASK_STATE.md): the detailed checkpoint and owner decisions.
