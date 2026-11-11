# Post-operation delta admission

Store authorizes an operation before execution, then makes a separate admission
decision on the verified resulting candidate before promoting its generation.
This internal gate is distinct from final publication.

Write, edit and delete use their exact target path. Bash requires a trusted,
canonical list of exact relative output paths: sorted, unique and bounded to
128 entries. Nil disables Bash; an explicit empty list permits no changed paths.
The managed service client supplies these through `--bash-output` (repeatable)
or `--bash-no-outputs`; see [launch instructions](../guides/setup.md#managed-linux-client).
The scope is included in the Store argument digest. D09 receipts continue to
hash the execution-only command specification.

The protected `delta-admission.jsonl` journal retains the input and candidate
manifests, candidate transition, request, policy commitment and ALLOW/DENY.
The primary effect outcome references its exact sequence and hash. ALLOW
promotes the verified candidate. DENY retains its evidence, removes the candidate
and reports `denied_after_effect` without advancing the generation or ledger.
Uncertain execution or cleanup remains UNKNOWN and blocks publication.

One admission record holds both manifests and must fit one audit record
(`audit.MaxRecordBytes`, 8 MiB, with the record base64-encoded in the frame).
Write, edit and Bash therefore check the input manifest before authorization:
if even a record with an unchanged output and no changes could not fit, they
refuse with `ErrAdmissionRecordLimit` (a `workspace.ErrLimit`). Nothing runs and
the Store stays usable. Without the check, the append would fail only after the
effect and leave it UNKNOWN. A command that grows the tree enough to overflow
the record after a passing check still ends UNKNOWN; storing manifests by digest
would remove that case.

A denied Bash command has already run in its D09 cell. Its command journal
records exactly one `production_d09_command_settled` event with schema
`tbound-production-d09-command-settled-denied/v1`, which binds the denied
candidate and names no output generation (a promoted command uses
`tbound-production-d09-command-settled/v2`). The settled lease lets the session
close without quarantine. Recovery re-verifies the denied record against the
Store operation; under evidenced Bash containment it refuses a denied effect
whose runner cannot verify the settlement. With the pilot's `workflow_bash_output_paths: []`, every Bash
write takes this path.

Opening a Store and reconstructing its evidence bundle verify both journals,
exact intent and reference bindings, manifest changes and generation ordering.
Admission records cannot be reused or silently ignored. Recovery does not
repeat effects. Intent, evidence and bundle schemas are version 2; legacy roots
without the required admission journal are refused rather than assigned
invented decisions.

### No-reap settlement is live-only and unreconstructable

The research P-half `no-reap/v1` delta leaves the command cell init and a
descendant running after the command exits. Its settlement carries
`SurvivorsTolerated` with all three postcondition booleans false
(`CommandSettlement.Admitted`). The contract is deliberate and pinned by
`no_reap_settlement_contract_linux_test.go` in `internal/sessionrepo`:

- Live `RunBash` admits the settlement only when the caller explicitly
  authorizes it through `VerifySettlement` (the production runner does, bound to
  the delta). Every other runner still needs the three booleans.
- `Reconstruct`, replay and `Open`/`OpenRecoveryPreview` refuse a bundle or
  repository that holds a committed no-reap Bash effect: `Reconstruct` returns
  `ErrInvalidEvidenceBundle` and reopening quarantines the repository
  (`ErrQuarantined`). Such runs are unreconstructable by design. A serialized
  `SurvivorsTolerated` is never trusted as authority.
- The production command evidence validator calls `Reconstruct`, so it also
  refuses these commands (`bundle-reconstruct`).

An evaluation unit that uses no-reap should therefore expect: the receipt records
`command_survivors` (the number of cells left running) and the delta template
digest; its evidence cannot be reconstructed or replayed after the fact; an
interrupted run cannot reopen its own repository; and the operator reset, not the
run, ends the surviving descendants. Do not plan any bundle-level disposition
check, replay or recovery for a no-reap unit.

A native subprocess regression cuts a typed write after the admitted candidate
has been promoted and synced, before the primary outcome is durable. It checks
the actual candidate files and admission manifest, then verifies quarantine in
a fresh recovery process and bundle replay, with no accepted transition or
journal changes. This is process-crash coverage; it does not simulate power loss.

These component guarantees do not establish a qualified production host, G1,
registered recovery trials or H1. See [OVERVIEW](../../OVERVIEW.md).
