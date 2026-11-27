# F35 real-exchange probe: report (2026-11-26)

Register: D63 amendment item 1 (thesis #70), read as an exchange-only probe (thesis #71, merge commit `2f324127`). Build: F35 candidate tree `fcf1cee3` (5,076 blobs verified), plus the uncommitted harness `supervisor/cmd/f35probe/main.go` (sha256 `c77a1801…`). Developer machine: WSL `tbound-dev`. Frozen research profile sha256 `3d0eaf64…`. Throwaway ledger `tbound-f35-probe`: cap 1,007,792 µUSD, so booked spend can never pass 300,000 µUSD. The guest, the research ledger and the admissions were not used.

## Key

- 19:27:46Z: the key arrived by secret card (delivery file, 73 bytes). It was piped on stdin into `~/f35-probe/cfg/openrouter.key` (mode 0400, owner tbound, size checked), and the Windows file was deleted at once.
- 19:30:19Z: the key was deleted with `shred -u`. The first attempt failed because the file was mode 0400, so it was made `chmod 600` first. `ABSENT` was confirmed, no `sk-or-` string remains in `~/f35-probe` outside the frozen source, and the Windows secrets folder is empty.

## Exchanges

All requests went to OpenRouter `deepseek/deepseek-v4.1-flash`, provider DeepInfra.

| run | request | finish / tool | in | out | reasoning | µUSD settled |
|---|---|---|---|---|---|---|
| R1 B, 11 host calls | 1 | tool_calls read | 861 | 47 | 8 | 140.3 |
| | 2 | tool_calls edit | 1,274 | 156 | 83 | 122.2 |
| | 3 | tool_calls bash | 1,756 | 45 | 0 | 73.5 |
| | 4 | stop, final text | 2,155 | 111 | 67 | 105.0 |
| R2 B, 4 host calls | 1 | tool_calls read | 861 | 55 | 9 | 39.3 |
| | 2 | tool_calls edit | 1,288 | 192 | 104 | 156.7 |
| | 3 | tools disabled (`tool_choice` "none"), stop, **empty text** | 952 | 45 | 0 | 152.2 |
| R3 V | 1 | toolUse read | 385 | 47 | 0 | 73.6 |
| | 2 | stop, final text | 456 | 65 | 31 | 39.0 |
| R3b V | 1 | toolUse read | 405 | 83 | 0 | 56.8 |
| | 2 | toolUse bash | 523 | 124 | 56 | 73.2 |
| | 3 | **HTTP 400 "request not acceptable", not booked** | | | | |

- Ledger: 11 reservations, 11 settled, 0 uncertain, 0 open, no overage. Settled total is 1,037 µUSD (US$0.001037). There are 23 records, head `f5c43a2b…`, and the file sha256 is `df6a8e73…`.
- B (R1, R2): every reply passed the strict parser. The replies' usage carries `cost`, `cost_details`, `prompt_tokens_details.cached_tokens` and `completion_tokens_details.reasoning_tokens`, and each request settled at exactly its reported `cost`, rounded up. The deltas carry both `reasoning` and `reasoning_details`. The bridge sent `reasoning` back on every later assistant message (R1: 28 and 322 characters).
- V (R3, R3b): the forwarder settled each reply from the usage it tapped from the stream. The `ForwardTotals` match the ledger. The forwarded bodies are the frozen `BuildForwardedRequest` output, with `reasoning: {effort: "high"}`, the `deepinfra/fp8` pin and `max_completion_tokens` 4096.
- R3 did not exercise the replay case, because its first reply had 0 reasoning tokens. R3b is the same V setup with a task that needs two tool calls.

## Findings

**F1. V: Pi replays `reasoning_details`, and the forwarder refuses the request.** Verdict: **blocks research-route V units**, so this is a fix candidate under D63 item 2.

- In R3b, the real reply to request 2 carried reasoning: 56 tokens and 8 `reasoning_details` deltas.
- pi-ai 0.87.1 stores the deltas as the thinking signature. It replays them as `reasoning_details` on the assistant message (`openai-completions.js:1042`), and it does not send a `reasoning` string.
- The forwarder allows only `role`, `content`, `tool_calls`, `tool_call_id`, `name` and `reasoning` on a message (`forward.go`, `parseForwardedMessage`). It answers 400 and books nothing.
- The refused member is `messages[5].reasoning_details`. `f35probe why` rebuilds the body offline with the frozen builder: the original is refused, and it is accepted with only that member removed.
- The refused body is `real/real-runs/r3b-v/v-request-3.json` (sha256 `d07d948c…`). The replayed message, byte range 997 to 1577 of that body, is `real/r3b-replayed-assistant-message.json` (sha256 `86d2e902…`).
- R3b stopped at the 400, with no retry; pi-ai sets `maxRetries: 0`.
- Reach: every V unit that makes another request after a reply with reasoning. In R1, 3 of 4 replies had reasoning tokens.
- Register: D62 amendment item 2 says "If the provider returned reasoning content despite the profile's setting and Pi replayed it, the forwarder would refuse that request and the V unit would fail. The research pilot's first V unit, run alone (D61 item 6), checks this." The probe shows that the case occurs. "Despite the profile's setting" does not hold: the profile requests `reasoning: {effort: "high"}` and does not exclude it, so OpenRouter returns reasoning by design.

**F2. B, tools-disabled final call: empty assistant text.** Verdict: **documented limit**.

- R2 request 3 (D56, `tool_choice` "none") ended with `stop`, empty content and no reasoning, while 45 output tokens were billed (152.2 µUSD).
- The conversation ends cleanly. The unit's outcome comes from its acceptance check on the tree, not from the final text, so no verdict depends on this.
- Also seen: that request reported fewer input tokens (952) than request 2 (1,288), with no cache hit. The cause is not established.

**F3. Cost is below the token-price estimate.** Verdict: **documented limit (projection input)**. OpenRouter reports cached prompt tokens and charges less for them. The ledger books the reported `cost`, so later requests in a conversation cost less than tokens × the declared price. The research pilot's projection should use ledger costs, not token × price.

**No finding for C.** On a compliant task, C sends the same prompts and tool manifest as B (`production_binder_linux.go:54-55,520`, `broker.DeclaredToolManifest`).

Limits of the probe:

- The executor is an in-memory tree, and bash returns only its exit.
- V's tools are two simple read and bash definitions, sent through pi-ai's request builder rather than the Pi CLI agent loop. That this matters is inferred, not measured: the CLI uses the same `openai-completions` message conversion.
- R3b is an addition to the planned steps, run because R3 did not reach a replay. It cost 130 µUSD and stayed inside the approved scope and cap.
