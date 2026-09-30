# Correlation core failure modes (recorded before implementation)

This list is the pre-implementation failure analysis required for this isolated protocol slice. The core is fail-closed: a rejected proposal or malformed broker capture closes the in-memory correlation stream, and it never authorizes a side effect.

1. An adapter invents a tool-call ID that the trusted broker never captured.
2. A proposal substitutes a response ID, call ID issuer, or opaque ID value while preserving a plausible display string.
3. A proposal changes the tool name, including case changes, aliases, or a registered tool that belongs to another captured call.
4. A proposal changes the canonical-argument digest, including a digest copied from a different call.
5. A proposal reuses an already consumed call, or attempts a second consumption concurrently.
6. A proposal presents a later captured call before the head of the broker's recorded order.
7. A proposal changes the broker-recorded sequence or generation, including a stale generation after edit/import.
8. The broker attempts duplicate provider call identities, duplicate/non-increasing capture sequence numbers, malformed identifiers/digests, or a tool name outside the fixed four-tool surface.
9. Empty, whitespace-only, or malformed identifiers are normalized differently by adapter and broker; matching must compare issuer and opaque value exactly and must not trim/case-fold evidence fields.
10. Concurrent callers race to consume one outstanding call; only one can be accepted and the result must not depend on scheduling.
11. An invalid attempt is followed by a valid attempt to see whether a failed check leaves authority available; fail-closed closure must prevent that continuation.
12. A generated fixture is mistaken for a real provider exchange or G1 evidence; fixture metadata and report output must say that it is synthetic and non-claim-bearing.

The core intentionally does not parse provider payloads, canonicalize JSON, produce provider requests/results, perform effects, or establish provider/profile closure. It accepts the broker's canonical argument digest as a trusted captured fact and compares it byte-for-byte. A successful synthetic fixture demonstrates only deterministic local correlation behavior.

## Review addendum

A later review identified one additional edge case: different issuer namespaces may reuse the same opaque call-ID string. If a proposal uses a third or incorrect issuer and the opaque value matches more than one captured call, the core must deny the call-ID mismatch without attaching any arbitrary capture. The E2E fixture now includes this collision case.