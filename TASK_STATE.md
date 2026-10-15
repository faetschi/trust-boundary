# TBound task state

Updated 2026-10-15. Branch `codex/tbound-prototype`; implementation HEAD `ddbe255` at this update.
User priority: finish the thesis first and defer broader product features. See
[docs/status.md](docs/status.md) for evidence, qualification state, blockers, and history;
this file keeps the short action plan.

## Thesis completion plan

1. **Qualify the claim-bearing environment and launch path.** Preserve required artifacts
   before using the authorized VM replacement; boot and qualify the exact ABI-8 guest,
   signed profile, Pi closure, and local service admission. A single authenticated UDS
   service may be an internal boundary behind the CLI. Remote and multi-user service
   claims are out of scope. Reconcile D15's no-service-mode wording in the thesis before
   freezing the protocol; the sibling thesis files are not amended by this scope update.
2. **Complete the retained runtime obligations.** Exercise a real production Pi run with
   independent workspace, process, network, and publication oracles. Verify the D20
   production recovery binder, interrupted-activation behavior, and actual owner/cell
   drain in fresh processes. Component tests and synthetic receipts do not qualify them.
3. **Close D14 under a bounded storage model.** Keep one claim-bearing service and one
   unresolved workflow at a time; prevent new request IDs while earlier work is not
   independently settled. Use physically reserved capacity on a dedicated protected
   filesystem or strict quota, separate task outputs, derive the maximum terminal-write
   bound, and test real ENOSPC/fault recovery. Add a global ticket/schema only if the
   simpler admission and storage model fails. See
   [the bounded D14 design](docs/design/d14-reserved-capacity.md).
4. **Research Claims (last).** Follow the user/advisor construct-review procedure;
   freeze the corpus, oracle matrix, calibration, scoring, provider route, request/token
   caps, current prices, and worst-case cost estimate. Execute only within the total
   US$5 OpenRouter cap using DeepSeek V4.1 Flash. Keep independent runtime/effect oracles
   and the adopted security/calibration/friction criteria. If the full protocol cannot
   fit, amend the thesis claim before paid evaluation; do not silently reduce its
   denominator. G1 and H1 remain unestablished until their evidence is reviewed.

## Deferred product work

- Defer broad operator history/cancel/doctor UX, frontend/web chat, generalized
  installer/update/rollback, remote/multi-user service, and the optional 51-trial
  recovery expansion. A reproducible setup/runbook and safe process shutdown/drain
  remain engineering requirements; user-facing product polish does not gate the thesis.

## Execution rules

- Never read, print, copy, or commit `vm_start.txt`, provider/admin credentials, or real signing keys.
- Preserve `supervisor/tbound` and `tbound.exe`; build only to explicit private paths.
- Stage reviewed increments explicitly; never use a blanket reset.
- Passing checks do not establish G1, containment, or H1.
- Delegated work uses GPT-6 Luna with reasoning `max`.

## Records

- [Canonical status and evidence index](docs/status.md)
- [Evidence receipts](docs/evidence/README.md)
- [Status audit](../review-artifacts/tbound-status-audit-2026-10-14.md)
- [Consolidation pre-edit patch](../review-artifacts/tbound-completion-2026-10-14/initial-working-tree.patch) (SHA-256 prefix `90517f28`, 67,447 bytes).