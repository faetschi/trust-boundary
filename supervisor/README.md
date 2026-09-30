# tbound supervisor broker/proposal boundary

This Go module contains a provider-independent correlation core under `internal/broker/correlation` and a bounded Pi request boundary under `internal/broker/protocol`.

The core accepts trusted broker-captured call envelopes and untrusted adapter proposals. It preserves response and tool-call issuer/value pairs, tool name, canonical-argument digest, sequence, generation, and both the processed input and any uniquely identified counterpart in each decision. It admits only the fixed `read`, `write`, `edit`, and `bash` names. Captures must have strictly increasing sequence numbers; proposals must match the earliest outstanding call exactly. A successful match consumes that call once. Malformed input, mismatches, replay, out-of-order proposals, and unregistered tools deny and close the in-memory stream.

The correlation core treats the digest as opaque evidence. The protocol package uses pinned RFC 8785 canonicalization for the actual Pi proposal shape, enforces exact case-sensitive JSON keys, registered call and response ID issuers, and per-stream count/byte budgets. For each trusted capture it requires broker-owned raw argument bytes, recomputes the canonical digest, and checks the broker's asserted digest before constructing a proposal from capture state. The protocol README records the canonicalization contract and synthetic raw-capture/proposal parity fixture. Real provider raw-argument capture and provider-decoder parity remain integration work; this package does not register or authenticate a real provider profile. Neither package contacts a provider, binds tool results, evaluates policy, creates effect authority, or executes tools.

## Synthetic end-to-end fixture

The transcript follows the normative `read(g0) → edit(g1) → bash(g2) → read(g2)` shape: two generation transitions, `g0→g1` and `g1→g2`, across three labels. It also covers digest, response-ID, call-ID issuer, registered tool-name, sequence, and generation mismatches; duplicate and malformed captures; malformed proposals; unknown calls; replay; unregistered tools; and ambiguous issuer fallback. Every step checks stream closure and capture ordinal.

The transcript uses sentinel digest values, is explicitly marked synthetic and non-claim-bearing, and is not a real provider exchange. It does not satisfy E05/G1. The protocol package has separate synthetic unit tests. With Go installed, run the fixture test from this directory:

```powershell
go test -count=1 ./e2e
```

The test writes `e2e/artifacts/correlation-e2e-report.json` and prints its SHA-256. The report records local fixture dispositions only. It must not be presented as evidence of broker/provider integration.

See `CORRELATION_FAILURE_MODES.md` for the pre-implementation failure analysis and review addendum.
