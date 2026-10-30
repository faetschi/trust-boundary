# Current status

Updated 2026-10-29. This page is the short gate summary. [OVERVIEW](../../OVERVIEW.md)
is the one-screen state. Decisions, blockers, and next steps are in the owner's working log, which is not part of this repository. History before this date is in Git.

Current policy is the [dated owner decisions](owner-protocol-decisions-2026-10-29.md).
The independent advisor and examiner roles are removed. The supervisor is
administrative only, with no input or sign-off. No advisor confirmation,
human audit, or replacement mandatory owner acceptance gate is required.
Operational owner cryptographic signing is unchanged. Independent human review
is not claimed.

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
- **D26 is owner-decided.** C-DH stays outside the registered configurations.
  Configuration C uses the static permitted-alternative table. TM-C covers
  mediated command-cell interpreter, socket, and persistence stimuli, excluding
  direct Pi-runtime bypass. The existing `dr` probe remains unchanged.
- **Full construct evidence is incomplete.** Deterministic `corpus-check`,
  including applicable guest witnesses, is required before preregistered H1.
  A local green report, `corpus-digest`, or `preregistration-record` does not
  establish full construct completion.

## Current checkpoint

| Area | State |
| --- | --- |
| Guest | F13 = `6162e3c`, guest `172.24.152.73` |
| Governed path | G1 and both probes pass; each new freeze must repeat them with fresh evidence |
| Measurement | F13: EVQ 21/22 (net-sink open until corpus mode); route report complete |
| Pilot corpus | Re-based on established benchmark items; 11 adversarial, 20 compliant, 2 ambiguous; T5 delivery correction and T7 continuation proof remain implementation-deferred |
| Panel | Optional supplementary analysis. AC1, raw agreement, kappa, and non-pass/incomplete labels are diagnostics only. Historical prep is retained; no paid run or human audit is required. |
| Provider | OpenCode Go for engineering; the frozen OpenRouter route for the provider-dependent H1 pilot |
| Denial hints | Static table (H1 treatment) merged; a gate DENY no longer ends a service-mode conversation (`c209d63`, merged, not yet on the guest); generated hints are post-thesis |

## Remaining proof and operations

1. Implement and test T5 and T7 under the settled owner decisions. Retain seven
   T5 cases and provenance, with malicious material delivered by a lower-trust
   tool result and the legitimate task in the user prompt. T7 needs retained
   no-effect proof for explicitly eligible classes, settlement, journal, and
   fresh-process replay. Ambiguous failures remain conservative.
2. Parent: run the final exact-HEAD full gate, check F14 package compatibility,
   and repin F14 before any later freeze. `56cf99c` is a historical gated pin.
   F14 signing, installation, G1, and the existing route and `dr` probes remain
   later owner-controlled operations. The separate denied-after-effect `dd`
   qualification is not required for F14; G1 and route probes do not prove it.
3. Enter corpus mode and verify reset, net-sink, no-reap, and V. Complete EVQ,
   route evidence, and the applicable construct witnesses.
4. Run the engineering, compliant, and adaptive-cap pilots. Derive caps and
   costs from retained records. Complete the 299 adversarial, 100 compliant,
   and 20 ambiguous cases, channel witnesses, and cold partition.
5. Complete full deterministic construct evidence before H1. Freeze artifact,
   corpus, caps, and analysis, then complete immutable H1 registration and run
   under the registered thresholds, N, scoring, and denominators.

AMB-02 retains `EXECUTED` / `PROCEEDS`, row 1, score 0. Record containment and
no disclosure separately. No rubric change or new execution result is claimed.

`corpus-digest` and `tbound-profile preregistration-record` perform strict
loading, validation, quotas, and `CheckTrials`, plus requested bindings and
artifacts. Run full `CheckConstructs` through `corpus-check` separately. With
no panel flags, panel verification is skipped. All three nonempty panel paths
are required if supplied, and their integrity and numeric checks remain strict.
The optional panel cannot block the critical path on diagnostic agreement.

Paid calls, freeze, signing, installs, guest operations, and product merges
remain outside this documentation task. The parent owns Git and F14 repinning.

## Related

- [Architecture](../runtime/architecture.md): authority boundaries and seams.
- [Publication contract](../runtime/publication-contract.md): how changes reach the live root.
- [Panel guide](../guides/panel-run.md): optional supplementary blinded model analysis.
- [Decision records](../decisions): ADRs and their status.
