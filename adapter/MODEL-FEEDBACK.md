# Model-facing tool feedback

The wire `tbound-result/v1` Result, its journaled Result bytes and digest, receipts,
and gate decision are unchanged. Provider evidence adds the optional hint metadata
described below. For a DENY with a known public reason,
or an ALLOW whose existing `status` is `command_failed`, the proxy renders:

```text
<unchanged compact Result JSON>
hint: <fixed permitted-alternative sentence>
```

The separator is exactly one LF byte, followed by `hint: ` and the sentence.
There is no trailing newline. Other ALLOW results, unknown reasons, and probe-only
replies retain their JSON-only text. The Go bridge constructs this same message
for provider history; recovery reconstructs it from journaled hint selections and
requires byte-for-byte equality with recorded provider requests. Records without hint metadata use the
round-1 static table. Pi does not supply provider history to the bridge.

By default hints are static advisory text derived from public policy. The bridge
can instead use a host-configured, default-off decision chooser to select candidate
0 (the same static sentence) or candidate 1 from a frozen reason-specific set.
They grant no authority,
interpolate no arguments or paths, and never rewrite or execute a call. The model
must issue a new proposal, which is authorized afresh.

`supervisor/internal/providerbridge/result_hints.go` owns the table.
`TestAdapterHintTableEqualsGo` golden-checks the entire TypeScript copy in
`src/result-hints.ts`; change the Go table first, then replace the TypeScript file
with the expected source printed by that test. The exhaustive Go test checks the
public reason producers, the D32 allowlist, the 128-byte limit, and path exclusion.
The v1 chooser candidate table is immutable replay data pinned by a separate
golden digest; retain v1 and add a version when changing its sentences.

The scripted transport verifies and removes the suffix before handing Result JSON
to a scripted source. D32 still consumes only its existing public projection; its
attacker table and pinned artifacts do not change.

The table is included in both development and packaged runtime file lists. These
source changes require rebuilding and re-pinning the adapter runtime bundle and
any manifest that pins it before the next freeze; they do not regenerate corpus
or attacker artifacts.

## Journaled decision hints

The exchange-linked provider_tool_result and provider_admission_denied records
add optional hint metadata: candidate_set, provider (static or choice), configured
chooser model, candidate_id, confidence, latency_ns, fallback_reason, transport,
endpoint, optional local model_digest and optional C-DH manifest_digest. Replay resolves the id through the immutable candidate table;
it never contacts a chooser. A failed choice records its attempted model and
fallback reason with candidate 0. Result bytes and their digest do not change.
Provider history commitments now use tbound/provider-history/v2.

The local Pi tool display keeps its static sentence. createBrokerProvider's stream
and streamSimple deliberately drop _untrustedTranscript; streamFromBroker never
sends Pi's tool text or history to Go. The bridge constructs the complete actual
provider history, including its chosen sentence. No adapter chooser is installed,
and the adapter cannot replace the bridge's selection. The static Go/TS golden
parity test remains in force. Scripted D32 roles remain static and strip only a
verified static suffix before the Result-only attacker projection.

H1 remains Configuration C/static. The secondary C-DH arm pins two classifier
models and the whole treatment in CDHManifest; runner integration and funded
collection remain deferred. See ../docs/design/denial-hint-providers.md for
configuration, fallback rules, the offline harness, and exact integration seams.
