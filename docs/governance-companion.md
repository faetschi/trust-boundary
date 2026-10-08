# Native-Pi governance companion

## Product boundary

The product is the **actual native Pi TUI inside TBound** plus this separate,
read-only browser companion. The browser is not a replacement Pi chat, a
developer `tbound` test dashboard, a policy editor, or an authority boundary.
Pi's native terminal remains the interaction surface. The companion presents
structured governance observations alongside it and works in unavailable,
fixture, or replay mode without requiring a native launch/profile proof. Live
file-source mode is intentionally refused until a descriptor-bound, no-follow
tailer is available; the existing webview tailer is not reused blindly.

The implementation is deliberately small and dependency-free:

```text
supervisor/internal/governanceview/
  contract.go       versioned bounded wire contract
  validation.go     fail-closed shape/size validation and fixture/replay loading
  store.go          viewer-owned bounded ring, cursors, dedup, slow-client close
  adapter.go        structural adapter from public webview Buffer APIs
  handler.go        GET-only loopback HTTP/SSE handler
  index.html        embedded responsive HTML/CSS/small JavaScript client
supervisor/cmd/tbound-governance/
  main.go           loopback command and explicit fixture/replay wiring
```

The command exposes no launch, stop, approval, execution, provider, policy,
publication, arbitrary-file, credential, or environment-dump endpoint. Source
file flags are currently refused fail-closed; the registered source adapter
uses non-path IDs/labels only and never sends a source path to the browser.

## Frozen observation contract: `tbound-governance-observation/v1`

The contract is frozen in `supervisor/internal/governanceview/contract.go`.
Document inputs use `tbound-governance-document/v1`; replay gaps use
`tbound-governance-replay-gap/v1`.

Every snapshot has:

- explicit `mode`: `fixture`, `live`, `replay`, or `unavailable`;
- a session/profile/preflight header and registered source-health entries;
- publication and recovery state;
- generation metadata, tree/hash status, retained-content status, and truthful
  diff status (metadata/hash presence is not presented as retained contents);
- bounded observed Pi actions and **project-test** output, separate from
  developer/conformance test output;
- linked records in the namespaces `provider`, `pi`, `proposal`, `decision`,
  `effect`, `generation`, `session`, and `source`;
- per-record `authority`: `diagnostic` or `unknown` in this companion; the
  compatibility enum also names `authoritative`, but validation rejects it;
- per-record `verification_scope`: `unavailable`, `unverified-input`, or
  `structural-audit-only`;
- distinct states `pending`, `denied`, `failed`, `withheld`, `unknown`, and
  `completed`.

Correlation is only through the namespaced `Links` fields
(`provider_id`, `pi_id`, `proposal_id`, `decision_id`, `effect_id`, and
`generation_id`). Evidence is a registered reference (`id`, `kind`, `scope`),
not a client-selected path. Hostile summaries/details are display text, not
commands or policy input.

### Bounds

The current version bounds the store to 128 records by default and 256 maximum,
32 sources, 32 preflight checks, 32 action entries, 32 project-test entries, 32 warnings, six
links per record, four evidence references per record, 2 KiB display text per
field, and 8 KiB per project-test output. SSE has eight subscribers with an
eight-record queue per subscriber. A slow subscriber is disconnected rather
than allowed to stall Pi/runtime work. Cursor epochs, replay gaps, and
deduplication are explicit.

## Authority and source semantics

The default command state is `unavailable`: no synthetic session, profile,
generation, effect, or provider claim is generated. Fixture documents must be
passed through `--fixture`; document input is always downgraded to diagnostic
and unverified scope, even if the JSON claims authority. Fixture scope also
rejects `publication: published`, `recovery: ready`, non-fixture source modes,
and authoritative action/record claims. Replay documents must be passed
through `--replay`; they are equally untrusted and do not contact a provider or
execute tools. The command refuses registered live file paths until a safe
descriptor-bound, no-follow source opener/tailer is available.

That refusal is an explicit integration gap, not a claim of a fully live
frontend. The existing `FromWebviewBufferWithRegistrations` /
`FromWebviewRecordsWithRegistrations` adapter is a narrow projection seam, not
a connected native-session observer. The intended real-session wiring is a
registered structured observer attached to the actual TBound/Pi runtime, with
stable non-path source IDs and explicit health/epoch updates; it should consume
the runtime's typed event API rather than revive path-tail following. This
companion remains unavailable until that trusted registration is supplied.

`governanceview.FromVerifiedAuditTrace` is intentionally **not** an authority
capability. `audit.Trace` is constructible by callers and carries no
non-forgeable verifier provenance, so the adapter maps every record as
diagnostic with `verification_scope: structural-audit-only`. An outcome without
a unique compatible intent earlier in the same ordered source/effect-ID tuple
is downgraded to `unknown`; duplicate intents/outcomes, conflicting IDs,
out-of-order sequences, and outcomes before intents cannot produce a completed
projection. The lineage scan covers the full input trace before the displayed
256-record window is applied. No caller boolean can promote these records.
Only a future verifier-owned non-forgeable capability may support a stronger
projection, and that is outside this slice.

The adapter does not copy or replace the existing authoritative stores, policy
checks, audit verifier, or evidence reconstructor. Generation contents are
shown as unavailable unless a future trusted adapter supplies registered
retained-content evidence. `unknown`, `withheld`, quarantine, and source gaps
remain visible; none is silently promoted to success.

## Fixture/replay document shape

The command accepts one exact JSON document (unknown fields are rejected):

```json
{
  "schema_version": "tbound-governance-document/v1",
  "snapshot": {
    "schema_version": "tbound-governance-observation/v1",
    "epoch": "fixture-demo",
    "cursor": "fixture-demo:2",
    "mode": "fixture",
    "verification_scope": "unverified-input",
    "generated_at": "2026-10-07T12:00:00Z",
    "session": {"status": "unknown", "profile": "fixture-profile"},
    "preflight": [{"name": "production admission", "state": "unavailable"}],
    "sources": [{"id": "fixture-source", "label": "sanitized fixture", "mode": "fixture", "health": "complete", "available": true}],
    "publication": {"state": "unavailable"},
    "recovery": {"state": "unavailable"},
    "generation": {"metadata_state": "unavailable", "contents_state": "unavailable", "diff_state": "not-comparable"},
    "project_tests": [{"name": "Pi project tests", "state": "unknown"}],
    "records": [],
    "warnings": ["fixture; not production evidence"]
  }
}
```

Records may be added to `records` with the same schema, a registered
`source_id`, `mode: "fixture"`, `authority: "diagnostic"`, and
`verification_scope: "unverified-input"`. Actions use the same explicit mode,
authority, and scope. They must remain diagnostic. A replay document is
identical except for `mode: "replay"` and replay source labels. Loaded replay
and fixture authority fields are overwritten to diagnostic rather than trusted.

## Construction and test commands

From `supervisor/`:

```text
gofmt -w internal/governanceview/*.go cmd/tbound-governance/*.go
go test ./internal/governanceview ./cmd/tbound-governance
go test -race ./internal/governanceview
```

The full repository test/race run remains a coordinator responsibility. Linux
evidence and native-Pi integration are not claimed by this companion slice.
For a safe unavailable smoke run:

```text
go run ./cmd/tbound-governance --addr 127.0.0.1:8790
```

For a fixture smoke run, use a private `0700` scratch directory and a
sanitized document, then pass it explicitly:

```text
go run ./cmd/tbound-governance --addr 127.0.0.1:8790 --fixture <registered-fixture.json>
```

The server binds only a literal loopback IP and sets exact Host/Origin checks,
same-origin resource policy, no-store headers on HTML/JSON/SSE/error routes,
and a restrictive CSP. Network writes use `ResponseController.SetWriteDeadline`
when supported; non-network test recorders use the same payload path. The
browser client uses `textContent` and DOM construction for hostile observation
text; it has no remote scripts, modules, forms, or HTML interpolation.

The client performs the initial snapshot first, captures its `(epoch,cursor)`,
then opens SSE with that cursor. Events are deduplicated by `(epoch,sequence)`;
future/noncontiguous cursors or an epoch change close the stream and replace
stale state with a fresh snapshot. SSE events carry current registered source
health projections. Public webview records are accepted only for explicit
non-path `SourceRegistration` tokens/IDs/labels; missing registered sources are
shown unavailable, and unregistered source records remain unknown/unavailable.

The document loader opens regular files with no-follow flags and checks the
opened descriptor identity against the pre-open identity on Linux and Windows.
Other platforms refuse document loading. This is separate from the refused
live tailer path and does not expose arbitrary HTTP file retrieval.
