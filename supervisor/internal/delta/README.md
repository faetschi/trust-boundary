# Ordered approved-delta chain validation

`ValidateChain` verifies a bounded sequence of complete logical tree
transitions. The caller supplies full sorted baseline and sealed manifests,
their expected tree digests, the selected policy and metadata-policy digests,
and the ordered transitions. Generation labels are caller-supplied names; they
are not proof of tree origin or immutability.

Each transition binds its contiguous sequence, unique transition and operation
identity, input/output generation labels and tree digests, sorted changed-path
manifest, and policy-decision receipt. A proposal transition names both the
proposal and captured call; a command transition names its lease. For a change
set, every preimage is compared with the same input snapshot before any change
is applied. The output tree is rebuilt and its digest recomputed. The final
complete object map and digest must exactly match the sealed endpoint.
Repeated edits compose by retaining the first preimage and last postimage;
paths that revert to their baseline state are omitted from the net delta but
remain in `TouchedPaths`. An empty change set can bind a new generation with an
unchanged tree; an individual no-op path row is rejected.

Tree digests use RFC 8785 JCS over a fixed schema, metadata-policy digest, and
the full path/type/content-digest/metadata-identity manifest, followed by
SHA-256 using `tbound-tree-jcs-rfc8785/v1`. Generation labels are excluded from
the tree digest and linked separately by transitions. Content digests cover
regular-file bytes or symlink targets. Metadata identity is an opaque SHA-256
commitment supplied under the selected metadata policy; this package does not
select that policy or inspect metadata fields. The JCS input is generated from
typed Go values, not decoded from arbitrary JSON, so this path does not accept
duplicate JSON keys or other raw-JSON ambiguity.

Bounds limit transitions to 2,048, objects per manifest to 20,000, changes per
transition to 10,000, total changes to 25,000, path labels to 1,024 bytes,
identifiers to 512 bytes, aggregate caller-controlled path, identity, digest,
and record-overhead bytes to 96 MiB, and estimated aggregate tree-validation
work to 2 GiB. The work estimate is
checked before each full-tree clone, hierarchy walk, sort, and canonical digest
pass. These are prototype ceilings that may need tuning against the registered
repository corpus; exceeding a limit rejects the chain without truncation.

Every non-empty chain requires a caller-supplied `DecisionVerifier`. The
verifier must authenticate the decision receipt against a trusted supervisor
policy or audit source and bind it to the exact transition. This package checks
that the receipt says `allow` and uses the explicitly pinned policy digests,
but does not create approvals, authenticate the verifier, sign receipts, or
persist audit evidence. A caller that feeds untrusted decisions or manifests
to a permissive verifier receives only structural composition, not authorization
or provenance.

This package does not discover filesystem changes, prove that supplied
manifests are complete, seal or freeze generations, authenticate tree-digest
commitments, enforce path allow/deny policy, inspect descriptor-relative object
metadata, check live-workspace baselines, persist audit records, or apply a
delta. It does not prove full-tree provenance, OS containment, or publication
safety by itself. Those remain responsibilities of the surrounding session
repository, sealer, policy, audit, and descriptor-relative apply components.
