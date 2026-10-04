# tbound supervisor prototype

This Go module contains the provider-independent correlation core under
`internal/broker/correlation`, the bounded proposal/result contract under
`internal/broker/protocol`, session IPC under `internal/ipc`, a deterministic
policy gate under `internal/gate`, and the dependency-injected supervisor loop
under `cmd/tbound`.

The core accepts trusted broker-captured call envelopes and untrusted adapter proposals. It preserves response and tool-call issuer/value pairs, tool name, canonical-argument digest, sequence, generation, and both the processed input and any uniquely identified counterpart in each decision. It admits only the fixed `read`, `write`, `edit`, and `bash` names. Captures must have strictly increasing sequence numbers; proposals must match the earliest outstanding call exactly. A successful match consumes that call once. Malformed input, mismatches, replay, out-of-order proposals, and unregistered tools deny and close the in-memory stream.

The correlation core treats the digest as opaque evidence. The protocol package uses pinned RFC 8785 canonicalization for the actual Pi proposal shape, enforces exact case-sensitive JSON keys, registered call and response ID issuers, and per-stream count/byte budgets. For each trusted capture it requires broker-owned raw argument bytes, recomputes the canonical digest, and checks the broker's asserted digest before constructing a proposal from capture state. The protocol README records the canonicalization contract and synthetic raw-capture/proposal parity fixture. Real provider raw-argument capture and provider-decoder parity remain integration work; this package does not register or authenticate a real provider profile. Neither package contacts a provider, binds tool results, evaluates policy, creates effect authority, or executes tools.

## Synthetic end-to-end fixture

The transcript follows the normative `read(g0) → edit(g1) → bash(g2) → read(g2)` shape: two generation transitions, `g0→g1` and `g1→g2`, across three labels. It also covers digest, response-ID, call-ID issuer, registered tool-name, sequence, and generation mismatches; duplicate and malformed captures; malformed proposals; unknown calls; replay; unregistered tools; and ambiguous issuer fallback. Every step checks stream closure and capture ordinal.

The transcript uses sentinel digest values, is explicitly marked synthetic and non-claim-bearing, and is not a real provider exchange. It does not satisfy E05/G1. The protocol package has separate synthetic unit tests. With Go installed, run the fixture test from this directory:

```powershell
go test -count=1 ./e2e
```

The test writes `e2e/artifacts/correlation-e2e-report.json` and prints its SHA-256. The report records local fixture dispositions only. It must not be presented as evidence of broker/provider integration.

`internal/ipc/README.md` specifies the length-prefixed frame, token binding,
sequence/replay checks, and fixed message/stream bounds. Portable IPC tests use
`net.Pipe`; Linux additionally provides a Unix-domain socket. The binding token
is not peer authentication. `internal/gate` compiles the explicit
`tbound-policy/v1` profile to a stable SHA-256 digest, requires a successful
broker correlation receipt before rule evaluation, and denies unlisted tools.

The `cmd/tbound` loop accepts abstract broker and executor interfaces and is
tested with both a synthetic broker and the concrete `internal/broker` flow.
That broker accepts one immutable registered profile, enforces the ordered
`read`/`write`/`edit`/`bash` manifest, builds requests through `internal/broker/openrouter`,
records bounded request/response bytes in memory, and registers parsed provider
tool calls for strict one-use proposal correlation. It requires an injected
`HTTPDoer`; there is no default HTTP client, and tests use a fake Doer only. The
executable still refuses to start a production session. Its explicit
`--smoke-listen --socket-dir DIR` mode is a no-effect integration fixture only:
it requires a caller-created, supervisor-owned mode-0700 directory, creates a
mode-0600 Unix socket and transient mode-0600 binding-token file there, accepts
one connection, and processes exactly the four fixed tool proposals. A scripted
fake `HTTPDoer` returns canned SSE bytes to the existing broker, and a stub
executor reports that it did nothing. Standard output is bounded JSONL with
proposal, broker-correlation receipt, gate decision, and result for each call.
The binding token is not peer authentication. This mode does not perform real
provider transport, workspace I/O, command execution, containment, publication,
durable audit, or session repository operations. Passing synthetic and
fake-Doer tests are component/integration checks, not WP1/G1 evidence or proof
of a closed security boundary.

The Linux cross-language smoke script copies the Go module and the dependency-
free IPC adapter client to a private ext4 working directory, builds and starts
the listener, runs Node with `--experimental-strip-types`, and asserts all four
transcript/result pairs. From the Windows checkout under WSL, run:

```bash
bash /mnt/c/<path-to-checkout>/supervisor/ipc-smoke.sh
```

It uses `$HOME/go-sdk/go/bin/go` when present, otherwise `go` from `PATH`, and
uses Node 24.15.0 from its standard extracted path under `$HOME` when present,
otherwise Node on `PATH` or `NODE_BIN` (Node 22.19+).

See `CORRELATION_FAILURE_MODES.md` for the pre-implementation failure analysis and review addendum.
