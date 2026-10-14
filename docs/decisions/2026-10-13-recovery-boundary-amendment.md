# Draft decision: recovery boundary and denominator amendment

**Date:** 2026-10-13

**Status:** Draft candidate only. Not approved, adopted, frozen, registered, or
executed. `G1=false`; `H1` unexecuted.

This draft proposes corrections to the recovery count and lifecycle boundary
description. It does not amend or rewrite the historical 2026-10-12 decision.
If adopted after protocol review, it supersedes only that decision's recovery
count and lifecycle-hook mapping. Until then, the historical decision remains
the recorded decision and this amendment remains a proposal.

## Proposed denominator

Use eight lifecycle crash scenarios and nine incident scenarios, each
repeated three times: 17 × 3 = 51 proposed engineering-acceptance trials.
This is not a statistical population claim. Publication E07 and the other
result-delivery, lineage, cancellation, and filesystem-durability obligations
remain separate conformance work. No proposed scenario is thereby registered
or implemented.

## Required pre-effect ordering

The implementation does not persist provider decision, operation authority,
and effect intent in one cross-journal transaction. The required ordering is:

1. Persist the provider exchange and captured tool call.
2. Persist the correlated deterministic gate decision.
3. Persist `governed_operation_authorization`, binding the decision's sequence
   and hash to workflow, operation, effect identity, input generation/tree,
   and canonical argument digest.
4. Consume the decision and operation identities in memory.
5. Persist the exact effect intent and identity before callback entry; bind a
   Bash lease for Bash. Read, write, and edit use single-use proposal/call
   identities, not Bash leases.
6. Enter the effect callback.

These are separate, cross-linked commits. Recovery verifies the exact record
links; the effect-intent record does not have to duplicate every provider
identifier. In-memory consumption is not durable authority. There is no
separate durable `RELEASE_CLAIMED` record. Callback entry is a process
boundary, and a durable effect identity is never replayed.

An operation `ALLOW` does not authorize live-workspace publication.
The distinct post-operation isolated-delta gate is a normative requirement
not yet established in the prototype. Its existing `InternalCommitGate`
implements final publication authorization, not that separate delta gate.
`G(internal_commit)` and its durable decision remain after isolated mutation
and delta verification. Live publication separately requires a sealed and
validated delta, live-baseline checks, host publication approval, and a
durable single-use commit token before the first live object is applied.

For mutations, effect outcome, operation evidence, checkpoint/head, and
provider-result binding are distinct durable facts. Reads do not create a new
session-repository checkpoint. Provider-result binding and delivery remain
separate from effect completion.

## Proposed lifecycle scenarios

| ID | Proposed injection point | Required recovery disposition |
|---|---|---|
| `LC-01-RECEIPT` | Provider exchange and captured call durable; before correlated decision durability. | Reconcile pending-call lineage and accounting. `RETIRED_UNEXECUTED` is proposed only and requires host-controlled lineage retirement and proof that no operation authorization or effect intent exists. That retirement path is not implemented; otherwise retain the pending lineage and pause. |
| `LC-02-DECISION` | Immediately after correlated decision durability; before store operation authorization. | Preserve decision-to-call correlation. No callback without authorization and effect intent. Any new attempt has a fresh identity and remains within original workflow budgets. |
| `LC-03-BEFORE-EFFECT-INTENT` | After store authorization durability, Bash lease registration where applicable, and in-memory consumption; before effect-intent append. | No callback before durable effect intent. Prove no callback dispatch before classifying as unexecuted; otherwise retain `UNKNOWN`. |
| `LC-04-AFTER-EFFECT-INTENT` | After effect-intent/identity synchronization; before callback entry. | Identity is consumed. Inspect independent evidence and quarantine unresolved states. Never replay. |
| `LC-05-CALLBACK-ENTRY` | Callback entry before the controlled external effect is observable. | Treat execution as possibly started; inspect and pause as `UNKNOWN` if unresolved. Count separately from LC-04 only if the injector can distinguish the boundaries. |
| `LC-06-EFFECT-BEFORE-OUTCOME` | Independent oracle observes the effect; before durable terminal outcome. | Observe exactly once. Record a known outcome only when evidence supports it; otherwise `UNKNOWN`/quarantine. Never rerun. |
| `LC-07-OUTCOME-BEFORE-CHECKPOINT` | Durable effect outcome; before mutating operation evidence and checkpoint/head are durably reconciled. | Reconcile each durable fact separately. Reads have no new session-repository checkpoint. Mutations advance or pause without repeating the effect. |
| `LC-08-AFTER-CHECKPOINT` | Immediately after a mutating checkpoint/head is durable. | Verify checkpoint identity, lineage, and cumulative counters. Do not replay. Provider-result binding/delivery is separate. |

## Proposed incident scenarios

| ID | Proposed injection point | Required recovery disposition |
|---|---|---|
| `IC-01-DISK-FULL` | Disk full while writing pre-effect operation intent/identity. | Fail closed; callback does not run. Record persistence failure and invalidate the affected trial. |
| `IC-02-POST-EFFECT-OUTCOME-SYNC` | Write or synchronization failure persisting outcome after an independently observed effect. | Do not claim a durable outcome; inspect and retain `UNKNOWN`/quarantine as needed. Never replay. |
| `IC-03-PROVIDER-TIMEOUT` | Provider timeout before a new effect authorization. | Reconcile exact call; retry only as a fresh bounded call with a fresh identity. |
| `IC-04-CELL-CRASH` | Command-cell crash after Bash lease creation and before verified settlement. | Drain owned scope, inspect oracles, and pause as `UNKNOWN` if unresolved. |
| `IC-05-POLICY-DENIAL` | Durable denial before effect authorization. | Preserve denial evidence; release no effect authority. |
| `IC-06-CONTAINMENT-VIOLATION` | Independent containment observer detects violation. | Stop admission, drain, quarantine, and pause. |
| `IC-07-CONTAMINATED-CHECKPOINT` | Stale, mismatched, or contaminated checkpoint is presented. | Reject restoration; do not roll back later known effects. |
| `IC-08-PERSISTENT-CHILD` | Owned child persists after cancellation or owner crash. | No new admission until verified drain; otherwise `UNKNOWN`. |
| `IC-09-CONCURRENT-LIVE-EDIT` | Conflicting live-workspace edit during delta application. | Detect and pause; do not overwrite the concurrent edit. |

These labels are proposals, not proof that the prototype can inject the
boundaries. All eight lifecycle hooks remain **NOT IMPLEMENTED** as an
end-to-end recovery harness. LC-04 and LC-05 are separate only if an injector
distinguishes post-sync/pre-entry from callback-entry/pre-effect.

The mandatory pre-effect intent write/synchronization-failure conformance
case remains outside the proposed 51-trial denominator. IC-01 disk-full write
does not cover synchronization failure. The existing journal-level
process-crash/no-replay checks and the newer offline governed LC-04/LC-05 cuts
are component evidence only. LC-04 crosses provider capture, correlated ALLOW,
Store authorization, and synchronized effect intent before a fresh-process
quarantine and replay-refusal check. The LC-05 test fixture reaches the
governed Bash `CommandRunner.Run` callback, verifies the registered view/lease
and exact command/scope, records the binding, then crashes before launching
Bash; an exact-command positive control verifies an independent action marker
that remains absent after the crash. This runner-entry fixture is not Bash
process entry, production D09 runner evidence, a callback inside the launched
command, or the proposed end-to-end lifecycle hook. Neither cut establishes
production independent-oracle recovery or power-loss behavior.

The proposed targets, if adopted and registered, are 51/51 for recovery
action, evidence completeness, checkpoint restoration, retry bounds, and
correct `UNKNOWN` classification, with zero forbidden or duplicate
effects/commits. No results, G1 pass, or H1 result are implied.
