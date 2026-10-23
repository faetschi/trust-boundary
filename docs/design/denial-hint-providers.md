# Denial hint providers

Status: offline implementation; no funded or live chooser experiment has run. H1 remains Configuration C with the original static table. C-DH (Configuration C with decision-model hints) is the owner-designated secondary thesis arm and a product experiment. Its manifest must be finalized and pinned before pre-registration and data collection.

## Provider contract and candidates

`providerbridge.Config.HintProvider` is host-owned. Nil selects `StaticHintProvider`, preserving the round-1 model-facing bytes. `HintInput` has exactly `reason_code`, `tool`, canonical `arguments`, and the latest admitted public `task`. No system/developer prompt, policy internals, oracle, receipt, credential or filesystem reads enter this input. Canonicalization failure yields null arguments; it never forwards unvalidated raw arguments. Remote disclosure of public task and call contents still requires the owner's approved route.

`ChoiceHintProvider` asks a classifier to choose one frozen candidate. `HintCandidates` returns two sentences per known reason (including command_failed): **0 is the original static sentence and every fallback; 1 is another public-policy-derived alternative**. The version is `tbound-hint-candidates/v1`. Both sentences are path-free and at most 128 UTF-8 bytes. The full candidate bytes are pinned by a golden test (SHA-256 of deterministic JSON: `e28ad5dac7c75e7cfb23a0fd22ff946218f5dffe8c56754724e31f01611f1d9e`). Never edit v1 in place; retain it when adding a future table version. Unknown reasons emit no hint.

The chooser returns a typed Choice answer with candidate id, confidence and optional per-option probabilities. Only the registered id and confidence affect selection. Wrong answer type, unknown id, non-finite/out-of-range confidence, low confidence, error or deadline expiry selects 0. Failure details are stable public codes, never remote error bodies. There is no generation provider or free-text client. Hints grant no authority; each next proposal is gated afresh.

## Client configuration

`ChooserConfig` holds Kind (decisions or local), Enabled (false by default), BaseURL, Model, ModelDigest, Client, BearerToken, Timeout and MinConfidence. Standalone transport config has no default endpoint, model, network client, environment loader or credential-file access. The caller injects an HTTPDoer which honors request contexts and refuses redirects. For Kind=decisions, BaseURL must be HTTPS, without embedded credentials, query or fragment; the client POSTs to BaseURL plus `/decisions`. A funded OpenRouter route uses the approved API root ending in `/api/alpha` (also the remote endpoint pinned by NewCDHManifest). Response bodies are capped at 64 KiB; errors never expose response text. There are no retries.

The request/response structs follow the [OpenRouter Decisions API reference](https://openrouter.ai/docs/api/api-reference/alphadecisions/submit-a-decisions-request): model, state, questions.hint (type choice, instructions, criteria); answers.hint (type, choice, confidence, probabilities). Model is a configuration string, not a Jev-specific branch. The configured chooser ID is journaled; provider-returned model aliases do not replace that pin. See also the [Jev overview](https://openrouter.ai/docs/guides/community/jev).

For the product defaults, zero confidence threshold means 0.8; zero/oversized timeout means the 2 second maximum. Invalid confidence configuration fails closed. Injected choosers/clients must honor context cancellation. Every attempted call records measured latency, including errors/timeouts; static and disabled selections record zero. Fallback codes: disabled, unknown_reason, invalid_config, timeout, chooser_error, invalid_response, invalid_candidate, low_confidence, confidence_missing; an invalid custom provider selection becomes static with invalid_selection.

## Local candidate-ID transport

Set Kind=local and explicitly enable the injected client. BaseURL is a local OpenAI-compatible API root such as `http://127.0.0.1:8080/v1`; the request POSTs to `/completions`. Endpoint validation allows only localhost, literal loopback IPs and literal private LAN IPs (including a guest). Public addresses, public/DNS hostnames, embedded credentials, queries and fragments are refused before HTTP dispatch. The injected local client must honor contexts, refuse redirects and disable proxies; the owner pins the approved PC/guest endpoint. No discovery or hosted fallback exists.

The prompt includes the same four public input fields plus the frozen candidate list. Fixed decoding profile `candidate-id-greedy/v1` means temperature=0, max_tokens=2, newline stop, a llama.cpp-style grammar restricting output to the registered IDs, and top logprobs for both candidates. OpenAI-compatible servers which do not support grammar/logprobs may reject the request; that falls back to static. The response must contain exactly one unpadded candidate ID, never a sentence. Only a single matching token and a complete finite logprob map for the candidate IDs provides confidence: stable softmax over that constrained candidate distribution. Missing/truncated logprobs yield confidence_missing and static fallback; they are never treated as certainty. A below-threshold softmax also falls back. Greedy output remains sensitive to model quantization, server/runtime and hardware, so it is not claimed deterministic.

`NewCDHLocalManifest(modelID, endpoint, modelFileSHA256, confidence, timeout)` pins the local transport for slot 1 and Jev's Decisions transport for slot 0. It requires an explicit SHA-256 of the owner's loaded model file and the decoding profile; no model file is read by the chooser. Freeze/attest that the local server loaded those bytes before collecting results. The same replay metadata and offline comparison fixture apply to local and hosted choices.

## C-DH preregistration manifest

`CDHManifest` is one credential-free object containing schema, arm, exactly two model IDs, per-model transport kind and endpoint (plus local model-file SHA-256 and decoding profile), candidate-table version and full candidate bytes, input field names, confidence threshold and timeout in integer milliseconds. Model slot 0 is `typesafe/jev-1.14`; slot 1 must be the owner's later selected exact open-source decision model ID. Empty or duplicate slot 1 fails validation. No open-source model ID is guessed or defaulted. The owner must verify the selected model's availability and license.

`NewCDHManifest(selectedModelID, confidence, timeout)` constructs it; `Digest()` validates its frozen shape then returns SHA-256 over domain `tbound/cdh-manifest/v1` + NUL + deterministic compact JSON. Reordering/changing models, candidates, inputs, threshold or timeout changes the digest or fails validation. `NewCDHProvider(manifest, ChooserConfig)` only accepts a registered model and takes transport kind, endpoint, model-file digest, threshold and timeout from the manifest, overriding caller config values. It binds the digest into every hint selection. The manifest is copied into scalar controls; later caller mutation cannot change a constructed provider's treatment.

The thesis threshold and timeout remain owner freeze decisions; .8 and 1000 ms are harness examples, not a completed pre-registration. No usable C-DH manifest/digest is emitted until the second model is selected. H1 uses the nil/static provider. C-DH has **no gate dry-run**, because candidates are sentences, not calls.

Comparison candidates supplied by the owner (names only; exact IDs, availability and licenses must be verified before selection): proprietary/hosted candidates are Jev, OpenAI GPT-6 Luna Decisions, Microsoft-Decision-1, Perplexity Decider V1.1 27B, Inception Mercury Decide, Liquid AI d1/d1-ultra, Upstage Solar Decide/Flash. The local transport is the natural candidate for C-DH slot 1: greedy decoding is reproducible-ish, keeps inference private on the owner PC or guest, and has no provider API cost. No small-model name or ID is guessed here; the owner selects and verifies the open-weight model and its license, then hashes the actual loaded model file.

Other open-source comparison candidates supplied by the owner are Cloudflare Clef/Clef Flash/Clef Omni (Qwen fine-tunes on Workers AI), Kev 4B and Together Tev1, subject to license verification. C-DH selects exactly Jev plus one open-source candidate; broader N-model comparisons belong to the product experiment. No catalog count is pinned here.

## Journal and replay

Wire Result JSON, result digests and gate decisions stay unchanged. The existing `provider_tool_result` or `provider_admission_denied` exchange-linked evidence adds optional `hint` metadata: candidate_set, provider (static or choice), configured model, candidate_id, confidence, latency_ns, fallback_reason, and manifest_digest for C-DH. Empty core metadata values are explicit; transport, endpoint, model_digest and manifest_digest are optional additions. Configured transport and endpoint, and the local model-file digest, are journaled for choice hints. A choice failure retains provider=choice and the attempted model, but candidate_id=0. The hint record is durable before the model-facing result enters history.

Replay never calls a chooser. It validates the metadata and resolves the candidate id against the frozen reason-specific table, then reconstructs `<unchanged Result JSON>\nhint: <sentence>` without a final newline. Recorded subsequent provider requests must byte-match rebuilt history. Missing hint metadata is the round-1 static format and still replays when all hint-bearing records omit it. Replay rejects mixing records with and without hint metadata for hint-bearing reasons, in either order. Provider, model, confidence and fallback fields are covered by the journal hash chain and pinned head; replay byte-matching covers the candidate text. Journals from before the hint treatment (JSON-only denied messages) are not migrated. History commitments use `tbound/provider-history/v2`, separating them from the old v1 digest domain. Result digests are unchanged; provider journal heads, transcript commitments and binaries require new pins before freeze.

The Pi adapter still displays its static local tool result. Its `createBrokerProvider` stream and streamSimple discard `_untrustedTranscript` and call streamFromBroker without that argument; only Go-owned history reaches the provider. Thus the adapter cannot overwrite/diverge from the chosen model-facing hint, and needs no chooser or credential. Go/TS static parity remains pinned. Scripted D32 transport accepts exactly either frozen candidate for the Result reason and strips the suffix before the attacker projection.

## Offline comparison

From supervisor, run:

```sh
go run ./cmd/tbound-hint-eval
# Later, after separately capturing approved provider responses (this command stays offline):
go run ./cmd/tbound-hint-eval -fixtures /path/to/recorded-scenarios.json -manifest /path/to/finalized-cdh-manifest.json
```

`internal/providerbridge/testdata/hint_eval.json` freezes 20 synthetic scenarios drawn from pilot A-01/A-02/A-03/A-04 denial shapes plus public reason producers. Sources are annotated per scenario. All ten public hint reasons are represented twice. These are not pilot observations or model efficacy results. The fixture carries N model recordings per scenario: typed answer or failure, simulated/captured latency and recorded next-proposal outcome. For C-DH the tool requires exactly the two manifest model IDs on every scenario; product mode allows N. Keep scenario inputs frozen when replacing fake responses with separately collected approved-route responses.

Reports include fallback/check-rejection rates, mean/p95 latency, mean/max hint bytes and alternative_allowed/retry_denied/no_further_action counts. Low confidence is fallback, not schema rejection. Timings are supplied via a deterministic clock and no sleeping/network is needed. Recorded continuation fields are descriptive only: this harness does not execute a continuation or infer that a candidate caused its outcome. No live collection CLI is installed; after funding, the owner must inject the approved context-aware/redirect-forbidding client into NewCDHProvider and capture the same fixture schema. This keeps offline replay incapable of contacting a model.

Example fake output (20 scenarios each): static fallback=0, rejection=0, mean_ms=0, mean_bytes=90.9; chooser-a fallback=.4, rejection=.2, mean_ms=3.95, mean_bytes=76.2; chooser-b fallback=.3, rejection=0, mean_ms=9.5, mean_bytes=84.85. These are test plumbing checks, not robustness findings. The thesis comparison must also pre-register continuation denominators, robustness/attack strata, cost, retention/disclosure treatment and failure accounting; unchanged H1/C results must remain separately reported.

## Runner integration deferred: exact seam

Adding a C-DH enum alone would mislabel authority and evidence. No runner configuration was added. Before running this secondary arm:

1. Add C-DH to internal/runner/types.go and an explicit opt-in secondary schedule in plan.go (do not change H1 B/C/V/P ordering, denominators or existing plan digests).
2. Extend GovernedC.Check in governed_c.go, configuration routing/executor dispatch and attest.go's configuration-specific policy binding; retain C's gate, scope admission, observers and receipt requirements.
3. Bind the C-DH manifest digest and selected chooser ID into the schedule, governed request, host session/profile and receipt, with a separate report path in assemble.go/export.go so C-DH cannot enter H1's C denominator.
4. At cmd/tbound/research_route_linux.go applyProviderRoute and governed_conversation_linux.go newProviderConversation, inject NewCDHProvider into Config.HintProvider using only the host-pinned manifest and selected model. Restore the same binding for fresh/recovered conversations (governed_recovery_linux.go, production_recovery_binder_linux.go). Never accept chooser configuration from Pi.
5. Keep production_corpus_binding_linux.go scriptedFactory roles static: scriptHistory intentionally rejects non-static suffixes. The C-DH chooser comparison concerns model trajectory roles; any change to scripted-role treatment needs separate registration.
6. Reserve/settle chooser cost and request budget through the workflow authority before enabling funded calls; the current offline client has no production ledger wiring.

## Product-only concrete-call dry-run seam

TODO, deliberately outside C-DH: gate.Evaluate(proposal, matched, policy) is pure but requires a valid exact correlation receipt (gate/gate.go validCorrelation). An invented suggested call has no such receipt; never fabricate one or mutate the live protocol.Stream to obtain it. Expose a separate read-only advisory gate entry point which shares canonicalization/policy evaluation and binds a snapshot of task/scope; then apply the Store's delta-admission checks for protected/output scope, and reserve a durable verification budget. A candidate that includes a concrete call must pass both checks before display, then be proposed and authorized afresh before execution. Today's sentence candidates contain no concrete calls, so no dry-run hook is executed or claimed as implemented.
