# Configuration V on the research route, planned in F33 (design)

Status: design, 2026-11-24. Nothing here is built yet. Register: thesis D61 (V returns to RQ4 on the research route, planned in F33). D61 replaces the D58 amendment ("V leaves the thesis") and D59's V-free planning. The build is F33, which is not yet built or qualified. These changes join its scope before its gate and guest qualification.

## What the register asks

V (plain Pi, no tbound) runs the 100 compliant tasks, paired with B and C, on the research provider with the same pinned model and D50 settings. RQ4 reports V to B (plumbing), B to C (governance) and V to C (total) as medians of per-task differences. V never runs an adversarial case, never affects H1 and has no rerun. The owner decides V's spend after the research pilot. A no-go switches V off without a re-freeze.

## F33 changes

1. **Plan.** Bring back the six-order V/B/C compliant schedule that `042c7972` (#272) removed from `supervisor/internal/runner/plan.go`, behind one `runner.Config` field that is part of the plan digest. With the field off, the plan is today's V-free plan, step for step. With it on, the plan is F32's compliant shape. B precedes C in exactly 50 of 100 tasks (orders 0, 1 and 4 of the six, with 17, 17 and 16 tasks). `evalreport.PlannedUnits` follows the field (3 or 2 compliant units per task, 1,935 or 1,835 in the legacy shape, headroom 97 or 92). `tbound-run` and `tbound-profile preregistration-record` agree on the same total, and the registration record carries the field. A compliant-screening plan stays B only.
2. **V's provider route.** `supervisor/cmd/tbound/trial_vrun_linux.go` mounts the OpenCode Go key into the container today (lines 48 to 59), and Pi computes the cost from its own price table (`internal/vrun/usage.go`). For a thesis V unit:
   - No key in the container, and no key in its environment or mounts.
   - Pi uses a provider whose base URL is a forwarder on the guest, outside the container, reachable only from V's container.
   - The forwarder holds the research key, reserves each request in `usdledger` before it sends it and settles it after, with the same request accounting as governed calls. It reuses `internal/broker/openrouter` and `internal/usdledger` and adds no second client.
   - The model id and D50 settings are the research profile's.
   - The unit's cost is the ledger's. Pi's own token and cost figures may stay in the V document as a cross-check and are not reported.
3. **Timing.** Wall time per unit is the runner journal's `elapsed_ms` (`internal/runner/journal.go:70`) for V, B and C alike. RQ4 does not use `governed_v.go`'s guest latency. Per-request provider time is recorded nowhere today. Add it once, at the point every research-route request passes for all three configurations, so that provider time and wall time less provider time compare like with like.
4. **Tests.**
   - Plan: both field values, step counts, the 50/50 B-before-C split, and the off value equal to the current plan digest.
   - V route: the container has no key and the forwarder books before sending.
   - Counts: `PlannedUnits` agrees with the plan for both values.
5. **Guest qualification.** One V route probe on a development compliant task, next to G1, rp and dr.

## Not changed

Screening stays B only. No V on adversarial cases. The product's own V provider choice is a product question. A thesis plan runs V only on the research route.

## Risk

Item 2 is the only new security-relevant code in F33's scope. V has no egress filter, so the key must never be readable from the container. It needs its own independent review and the full race gate before F33 is qualified.
