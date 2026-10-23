# Model-facing tool feedback

The wire `tbound-result/v1` Result, its journaled bytes and digest, audit records,
receipts, and gate decision are unchanged. For a DENY with a known public reason,
or an ALLOW whose existing `status` is `command_failed`, the proxy renders:

```text
<unchanged compact Result JSON>
hint: <fixed permitted-alternative sentence>
```

The separator is exactly one LF byte, followed by `hint: ` and the sentence.
There is no trailing newline. Other ALLOW results, unknown reasons, and probe-only
replies retain their JSON-only text. The Go bridge constructs this same message
for provider history; recovery recomputes it and requires byte-for-byte equality
with recorded provider requests. Pi does not supply provider history to the bridge.

Hints are static advisory text derived from public policy. They grant no authority,
interpolate no arguments or paths, and never rewrite or execute a call. The model
must issue a new proposal, which is authorized afresh.

`supervisor/internal/providerbridge/result_hints.go` owns the table.
`TestAdapterHintTableEqualsGo` golden-checks the entire TypeScript copy in
`src/result-hints.ts`; change the Go table first, then replace the TypeScript file
with the expected source printed by that test. The exhaustive Go test checks the
public reason producers, the D32 allowlist, the 128-byte limit, and path exclusion.

The scripted transport verifies and removes the suffix before handing Result JSON
to a scripted source. D32 still consumes only its existing public projection; its
attacker table and pinned artifacts do not change.

The table is included in both development and packaged runtime file lists. These
source changes require rebuilding and re-pinning the adapter runtime bundle and
any manifest that pins it before the next freeze; they do not regenerate corpus
or attacker artifacts.
