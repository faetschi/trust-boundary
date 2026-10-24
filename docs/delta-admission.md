# Post-operation delta admission

Store authorizes an operation before execution, then makes a separate admission
decision on the verified resulting candidate before promoting its generation.
This internal gate is distinct from final publication.

Write, edit and delete use their exact target path. Bash requires a trusted,
canonical list of exact relative output paths: sorted, unique and bounded to
128 entries. Nil disables Bash; an explicit empty list permits no changed paths.
The managed service client supplies these through `--bash-output` (repeatable)
or `--bash-no-outputs`; see [launch instructions](setup.md#managed-linux-client).
The scope is included in the Store argument digest. D09 receipts continue to
hash the execution-only command specification.

The protected `delta-admission.jsonl` journal retains the input and candidate
manifests, candidate transition, request, policy commitment and ALLOW/DENY.
The primary effect outcome references its exact sequence and hash. ALLOW
promotes the verified candidate. DENY retains its evidence, removes the candidate
and reports `denied_after_effect` without advancing the generation or ledger.
Uncertain execution or cleanup remains UNKNOWN and blocks publication.

Opening a Store and reconstructing its evidence bundle verify both journals,
exact intent and reference bindings, manifest changes and generation ordering.
Admission records cannot be reused or silently ignored. Recovery does not
repeat effects. Intent, evidence and bundle schemas are version 2; legacy roots
without the required admission journal are refused rather than assigned
invented decisions.

A native subprocess regression cuts a typed write after the admitted candidate
has been promoted and synced, before the primary outcome is durable. It checks
the actual candidate files and admission manifest, then verifies quarantine in
a fresh recovery process and bundle replay, with no accepted transition or
journal changes. This is process-crash coverage; it does not simulate power loss.

These component guarantees do not establish a qualified production host, G1,
registered recovery trials or H1. See [current status](status.md).
