# F35 exchange probe (D63 amendment item 1, read by thesis #71)

Throwaway copy of the F35 candidate tree `fcf1cee3` in WSL `tbound-dev` at `~/f35-probe/src`. It came from `git -c core.autocrlf=false archive fcf1cee3`, and all 5,076 blobs were verified against `git ls-tree` (`blobs-fcf1cee3.txt`). The only file added is `supervisor/cmd/f35probe/main.go`, and no frozen package is changed. `go.mod` and `go.sum` are unchanged after the build (blob ids `5e971e28` and `53cf483a`).

## Harness (`stub/harness/`, sha256 in `stub-SHA256`)

- `main.go`: `f35probe genesis|snapshot|b|v|stub2`.
  - `b` drives the B path through the frozen research bundle and the provider bridge: `providerbridge.NewFromCredentials`, `AdmitPrompt`, `NextTurn`, `Correlate`, `gate.Evaluate`, `RecordDecision`, `NewExecutor(...).Execute`.
  - It runs with the production prompts (`production_binder_linux.go:54-55`) and the production route mark (`research_route_linux.go:73`, `WithReasoning(true)`).
  - It reserves host calls per request and uses `HostCallsLeft`, so the tools-disabled final call of D56 happens when the budget ends.
  - The executor is an in-memory tree with read, write, edit and bash. bash only reports its exit, as `--bash-no-outputs` does.
  - `v` serves the frozen `Forwarder` (`Bundle.NewForwarder`) on a loopback `httptest` server. The pinned pi-ai 0.87.1 (`v.mjs`) then builds and sends every request with Pi's `openai-completions` serializer. Its model entry is the one `vrun/args.go:49` writes to `models.json`, with the pi-coding-agent 0.87.1 defaults. Request bodies and relayed responses are kept byte for byte.
  - `stub2` is a scripted, OpenRouter-shaped upstream that sends reasoning, `reasoning_details`, tool calls and usage. The frozen `stub-upstream.py` answers only "ok".
- `stub-upstream.py`: byte-identical to frozen `infra/guest/v-stub/stub-upstream.py` (sha256 `c47b5cc4…`).
- Throwaway ledgers:
  - `cfg-stub` (`tbound-f35-probe-stub`) and `cfg` (`tbound-f35-probe`), each with cap 1,007,792 µUSD, minimum 752,029, reserve basis request.
  - The frozen profile is the one with sha256 `3d0eaf64…`.
  - Booked spend is at most 255,763 + 44,237 = 300,000 µUSD.

## Stub run 2026-11-26 (no spend)

`stub/stub-runs.attempt1`: the first attempt reused fixed workflow and admission ids. The ledger refused every rerun with "unit already has a reservation" (fail closed). Ids now come from the run directory name.

`stub/stub-runs` (final harness `main.go` sha256 `7c108b9e…`):

| run | upstream | result |
|---|---|---|
| s1c-b | frozen stub | 1 turn, text, settled 2 µUSD |
| s2c-b-disabled | frozen stub, 2 host calls | 1 turn, tools disabled, settled |
| s3-v | frozen stub | forwarder booked 1, settled 1 (2 µUSD) |
| s4/s5 | scripted stub, bad chunk | the parser refused `native_finish_reason` at the chunk top level (a harness stub bug), and the reservation was held uncertain at 44,237 (fail closed) |
| s4b/s5b | scripted stub, without the route mark | passed, but sent no reasoning back; the harness lacked the production `WithReasoning(true)` |
| s4c-b | scripted stub | read, edit, bash, final, with `reasoning` on every replayed assistant message, all settled |
| s5c-b-disabled | scripted stub, 4 host calls | read, edit, then the third request with `tool_choice` "none" (D56), settled |
| s6-v | scripted stub | turn 1 settled (143 µUSD from `usage.cost`). In turn 2, Pi replays `reasoning_details` on the assistant message, and the forwarder answers HTTP 400 "request not acceptable", unbooked |

No key bytes are in any output (grep).
