# TBound - current state

Updated 2026-11-20. This is the only current-state file. Everything else is a
contract, a guide, a decision record, or historical evidence.

| Layer | State | Record |
| --- | --- | --- |
| Frozen research baseline | G1 passes on the real guest at F13 = `6162e3c` (2026-10-24). F14 = `02f9fb7` is the guest-verified thesis source freeze (2026-10-30). Both stay as recorded. | [F14 result](evidence/F14/F14-RESULT.md), [freeze kit](infra/freeze/README.md) |
| Interim engineering qualification | F15 = `2a6492d`, F16 = `97738b6`, F17 = `8931c69`, F18 = `b26e5ef`, F20 = `2641c6bc`, F21 = `bbc6ec85`, F23 = `13ed1193`, F26 = `2780072a` and F28 = `40e651b1` are guest-verified engineering freezes of `main`. F29 = `28f407b8` (2026-11-17) and F30 = `5626e34f` (2026-11-18) are guest-verified screening builds. Each passed G1, rp and dr, and routes 4/4. EVQ was 21/22 at F15 and F16, and 22/22 with the guest net-sink artifact from F17 on. | [F15](evidence/F15/F15-RESULT.md), [F16](evidence/F16/F16-RESULT.md), [F17](evidence/F17/F17-RESULT.md), [F18](evidence/F18/F18-RESULT.md), [F20](evidence/F20/F20-RESULT.md), [F21](evidence/F21/F21-RESULT.md), [F23](evidence/F23/F23-RESULT.md), [F26](evidence/F26/F26-RESULT.md), [F28](evidence/F28/F28-RESULT.md), [F29](evidence/F29/F29-RESULT.md), [F30](evidence/F30/F30-RESULT.md) |
| H1 evaluation | Not run. The build for it is F-H1, which does not exist yet. There are no H1 results. | [F30 result](evidence/F30/F30-RESULT.md) |
| Product lane | In progress with no qualified product host. See the product paragraph below. | [Product contract](docs/product-contracts.md), [debt ledger](docs/product-audit-2026-10-29.md) |

F19 = `b5ed5b0d`, F22 = `aa5aed10`, F24 = `de2a3cfd`, F25 = `2fa3e008` and
F27 = `db078536` were cut but not qualified. Each record names the reason and the
freeze that superseded it: [F19](evidence/F19/F19-RESULT.md),
[F22](evidence/F22/F22-RESULT.md), [F26](evidence/F26/F26-RESULT.md) for F24 and
F25, and [F27](evidence/F27/F27-RESULT.md). F30 is the latest verified freeze.

F15 to F30 are not the H1 build. They establish neither H1 readiness nor product
release readiness. `EVQ-NETWORK-SINK-GUEST` is open at F14, F15 and F16. F17 and
every later verified freeze passed it as an interim rehearsal; the claim-bearing
22/22 belongs to F-H1.

The product lane runs governed chat turns with the supervisor loop, not Pi.
Bash through the signed D09 cell is implemented. On 2026-11-04, build
`d75ac188` passed live startup qualification, Bash turns, review publication,
scope refusal, and predecessor replay on the demo VM. Publication crash recovery
was not run. The recovery fixes through `23800b63` await the next live run.
These product runs are not thesis evidence.

## Claim boundary

- **G1 passes on the real guest.** Read, edit, Bash, and publish work at freeze
  F13 = `6162e3c` (2026-10-24, fresh evidence; also at F11/F12). The route and
  direct-runtime probes pass with correct Landlock confinement.
- **H1 is unexecuted.** No PVR or ORR value exists. G1, a green test suite, a
  fixture, a capability probe, or a receipt is not an H1 result.
- **The pilot is not ready.** EVQ at F13 passed 21 of 22 obligations; only the
  net-sink obligation was open. F17 and every later verified freeze passed
  22/22 as interim rehearsals. The service-mode blockers found since F13 are fixed and merged,
  but not all have run on the guest.
- **Production resume is fixed at component level, not proven end to end.**
  The full binder, live-root permit and finalizer are not driven in a test, and
  nothing has run on the guest. No registered run depends on resume.
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

## Remaining proof and operations

This list dates from 2026-11-08. The current execution plan is
`tbound/h1-path/README.md` in the thesis repository.

1. Implement and test T5 and T7 under the settled owner decisions. Retain seven
   T5 cases and provenance, with malicious material delivered by a lower-trust
   tool result and the legitimate task in the user prompt. T7 needs retained
   no-effect proof for explicitly eligible classes, settlement, journal, and
   fresh-process replay. Ambiguous failures remain conservative.
2. Enter corpus mode and verify reset, net-sink, no-reap, and V. Complete EVQ,
   route evidence, and the applicable construct witnesses.
3. Run the engineering, compliant, and adaptive-cap pilots. Derive caps and
   costs from retained records. Complete the 299 adversarial, 100 compliant,
   and 20 ambiguous cases, channel witnesses, and cold partition.
4. Complete full deterministic construct evidence before H1. Freeze artifact,
   corpus, caps, and analysis, then complete immutable H1 registration and run
   under the registered thresholds, N, scoring, and denominators.

AMB-02 retains `EXECUTED` / `PROCEEDS`, row 1, score 0. Record containment and
no disclosure separately. No rubric change or new execution result is claimed.

`corpus-digest` and `tbound-profile preregistration-record` perform strict
loading, validation, quotas, and `CheckTrials`, plus requested bindings and
artifacts. Run full `CheckConstructs` through `corpus-check` separately. With no
panel flags, panel verification is skipped. The optional panel cannot block the
critical path on diagnostic agreement.

## Current policy

The [dated owner decisions](docs/evaluation/owner-protocol-decisions-2026-10-29.md)
are current. The independent advisor and examiner roles are removed. The
supervisor is administrative only, with no input or sign-off. No advisor
confirmation, human audit, or replacement mandatory owner acceptance gate is
required. Operational owner cryptographic signing is unchanged. Independent
human review is not claimed.

## Product debt

The [product audit of 2026-10-29](docs/product-audit-2026-10-29.md) is the debt
ledger. P6 to P16 are open or accepted. The [product contract](docs/product-contracts.md)
fixes the first milestone's contracts and is a specification, not a status
report.

## Historical records

- [Evidence](evidence/): F14 to F30 results and guest logs (F24 and F25 have no
  directory; the F26 result records them).
- [Freeze kit](infra/freeze/README.md): freeze values and procedure.
- [Decision records](docs/decisions/): dated architecture decisions.
- [Architecture](docs/runtime/architecture.md): components and trust boundaries.
