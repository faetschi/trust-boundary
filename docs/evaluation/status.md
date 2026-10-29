# Current status

Updated 2026-10-28. This page is the short gate summary. [OVERVIEW](../../OVERVIEW.md)
is the one-screen state; [TASK_STATE](../../TASK_STATE.md) is the working log with
decisions, blockers, and next steps. History before this date is in Git.

## Claim boundary

- **G1 passes on the real guest.** Read, edit, bash, and publish work at freeze
  **F13 = `6162e3c`** (2026-10-24, fresh evidence; also at F11/F12), and the route
  and direct-runtime probes pass with correct Landlock confinement.
- **H1 is unexecuted.** No PVR or ORR value exists. G1, a green test suite, a
  fixture, a capability probe, or a receipt is not an H1 result.
- **The pilot is not ready.** EVQ at F13 passes 21 of 22 obligations; only the
  net-sink obligation is open (it needs a corpus-mode trial reset). The route
  report is complete (4 of 4 routes covered). The service-mode blockers found
  since F13 are fixed and merged, but not yet on the guest (F14): a gate DENY no
  longer closes the conversation (`c209d63`), recovery preview indexes the right
  authorization record kind (`a40fbb9`), a denied-after-effect Bash command is
  settled (`f483fad`), a governed read honours `offset`/`limit` (`039ac82`), and
  governed bash now returns bounded stdout/stderr to the model, which the five
  compliant bash pilot tasks need, plus workspace path aliases (`b0ca40a`).
- **Production resume is fixed at component level, not proven end to end.**
  Since `46fa96a` and `8c5c24a` a resumed session re-accepts its exact pre-crash
  transitions and Bash evidence and can promote new writes; publication
  prerequisites pass up to the managed live-root permit in fresh-process tests.
  The full binder, live-root permit and finalizer are not driven in a test, and
  nothing has run on the guest. No registered run depends on resume: scripted
  pilot roles refuse it, and the recovery suite is proposed, not registered.
- **The final-profile containment study is not complete.** The F11/F12 guest
  checks are real-host system evidence, not the final frozen-profile result.
- **C-DH is parked.** H1 stays Configuration C with the static
  permitted-alternative table. The C-DH code is merged but off.

## Current checkpoint

| Area | State |
| --- | --- |
| Guest | F13 = `6162e3c`, guest `172.24.152.73` |
| Governed path | G1 and both probes pass; each new freeze must repeat them with fresh evidence |
| Measurement | F13: EVQ 21/22 (net-sink open until corpus mode); route report complete |
| Pilot corpus | Re-based on established benchmark items; 11 adversarial, 20 compliant, 2 ambiguous; manifest still has sign-off TODOs |
| Panel | `tbound-panel` is ready and its three models, config and binary are frozen (`F:/TBoundAssets/panel-prep-20261027/`); the owner runs it after funding. Gate per checklist item: Gwet's AC1 ≥ 0.80 with raw agreement ≥ 0.90 (Fleiss' kappa is reported, not gated); `corpus-digest` recomputes every statistic from the pinned receipt ratings |
| Provider | OpenCode Go for engineering; the frozen OpenRouter route for the provider-dependent H1 pilot |
| Denial hints | Static table (H1 treatment) merged; a gate DENY no longer ends a service-mode conversation (`c209d63`, merged, not yet on the guest); generated hints are post-thesis |

## Open gates, in order

1. Confirm the D26 scope cut and the residual-channel claim with the advisor
   (options in the [advisor decision package](advisor-decisions.md)).
2. Finish the pilot construct review (panel run, then the advisor's sign-off; the AC1 amendment and the
   pilot audit size are open in the decision package) and case corrections; freeze the pilot protocol,
   provider, outage rules, and initial safety ceilings.
3. Done at F13: freeze, reinstall, G1 and both probes with fresh evidence. For F14, the
   two service-mode defects (DENY continuation `c209d63`, recovery record kind `a40fbb9`),
   the D09 denied settlement (`f483fad`) and the corpus-check guest-evidence input
   (`f1d5b75`) are merged; the F14 freeze,
   install and fresh G1/probe evidence are owner steps.
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

- [Architecture](../runtime/architecture.md): authority boundaries and seams.
- [Publication contract](../runtime/publication-contract.md): how changes reach the live root.
- [Panel guide](../guides/panel-run.md): the blinded construct review.
- [Decision records](../decisions): ADRs and their status.
- [Working log](../../TASK_STATE.md): the detailed checkpoint and owner decisions.
