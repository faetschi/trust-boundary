# TBound task state

Updated 2026-10-14. Branch `codex/tbound-prototype`; implementation head `2e88ee8`.
See [docs/status.md](docs/status.md) for current implementation evidence, claims,
environment state, blockers, priorities, and historical counts. This file keeps the
action plan and execution rules.

## Action plan

1. **Core engineering:** finish production D20 recovery-binder verification,
   interrupted-activation qualification, and actual owner-cell drain; continue D14
   capacity/fault work and LC-04/LC-05 with independent oracles and publication and
   provenance evidence. See [open gates](docs/status.md#open-gates).
2. **Deployment:** preserve required artifacts before replacing the development VM;
   qualify a fresh ABI-8 guest, signed host profile, managed-service admission,
   restart recovery, installer, update, and rollback. The replacement is authorized
   by the [2026-10-14 decision](docs/decisions/2026-10-14-qualification-and-research-procedure.md).
3. **Operations:** complete cancel/history/doctor lifecycle paths, operator states,
   sanitized receipts, bounded shutdown, and recovery instructions. See
   [priorities](docs/status.md#priorities).
4. **Research claims (last):** follow the decision's advisor-guided construct review;
   freeze the corpus, oracle matrix, calibration, and scoring locally; complete the
   clean-machine G4 reproduction and H1 evidence. No paid evaluation before routing,
   request/token limits, current cost ceilings, and durable worst-case reservations
   fit the US$5 cap. See [claim boundaries and acceptance status](docs/status.md#claim-boundary).

## Execution rules

- Never read, print, copy, or commit `vm_start.txt`, provider or admin credentials,
  or real signing keys.
- Preserve `supervisor/tbound` and `tbound.exe`; build only to explicit private paths.
- Stage reviewed increments explicitly; never use a blanket reset.
- Passing checks do not establish G1, containment, or H1.
- Delegated work uses GPT-6 Luna with reasoning `max`.

## Records

- [Canonical status and evidence index](docs/status.md)
- [Evidence receipts](docs/evidence/README.md)
- [Status audit](../review-artifacts/tbound-status-audit-2026-10-14.md)
- [Consolidation pre-edit patch](../review-artifacts/tbound-completion-2026-10-14/initial-working-tree.patch)
  (SHA-256 prefix `90517f28`, 67,447 bytes; preserves the earlier draft).
