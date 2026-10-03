# Provider profiles

**Reviewed:** 2026-10-03. This note records verified provider contracts and the planned trust boundary. It does not certify a live provider exchange.

## Primary profile: OpenRouter

OpenRouter documents `POST https://openrouter.ai/api/v1/chat/completions`, an API key in `Authorization: Bearer`, a Chat Completions `messages` body, and both streaming and non-streaming operation. Its streaming guide describes SSE delivery. See the [Chat Completions API](https://openrouter.ai/docs/api/api-reference/chat/create-a-chat-completion) and [streaming quickstart](https://openrouter.ai/docs/cookbook/get-started/quickstart).

The current first code slice defines a fixed OpenRouter profile, builds a request, and parses bounded SSE while rejecting unknown fields. It does not make HTTP requests, load or attach credentials, or wire Pi to the broker. No provider call or end-to-end exchange has been verified.

## Optional test profile: OpenCode Go

OpenCode documents Go as a subscription with an API key and publishes direct API endpoints for its models. Its guide lists Pi as a validated client, while saying validated clients are not guaranteed to keep working indefinitely. Go asks other coding agents to send typical coding-agent traffic, identify themselves with their own `User-Agent`, and include a stable per-conversation `x-opencode-session`; traffic is monitored for abuse. See the [OpenCode Go guide](https://opencode.ai/docs/go/).

Go models use different API protocols. The guide lists `/v1/chat/completions` with `@ai-sdk/openai-compatible` for some models, `/v1/responses` with `@ai-sdk/openai` for others, and `/v1/messages` with `@ai-sdk/anthropic` for others. It also publishes `/zen/go/v1/models` for model metadata. The `opencode-go/<model-id>` form is OpenCode's config name; direct API calls use the model ID from the endpoint table. The model list can change.

A later Go profile must be separate and fixed to the chosen model's documented endpoint and protocol. The existing Chat Completions request builder cannot be treated as a Responses or Messages client. The `@ai-sdk/openai-compatible` label identifies the documented API family; it does not establish that this broker's request options or SSE parser work with every Go model. No exact Go model is selected, and no Go SSE compatibility is claimed. Verify the selected route and model before reporting a successful provider test.

OpenCode's Go guide says to obtain an API key and gives endpoint URLs, but does not explicitly specify the raw HTTP authorization header. The [AI SDK OpenAI-compatible provider docs](https://ai-sdk.dev/providers/openai-compatible-providers) say that the SDK's `apiKey` option sends `Authorization: Bearer <apiKey>`. Applying that convention to a direct Go request is an inference, not an explicit guarantee in the Go guide.

Go usage limits are model-specific: 20% of the model's monthly allowance in a five-hour window, 50% weekly, and 100% monthly. On reaching a limit, Go can block requests unless Zen balance fallback is enabled; the guide says free models remain available. Check the current guide and account Console when planning a test because limits and models may change.

## Broker and evidence constraints

- A finite trusted broker/operator-owned approved profile selects provider, endpoint, protocol, and exact model ID. Pi may submit a logical request, but cannot choose a URL, host, provider, model, or arbitrary upstream headers.
- Provider keys belong only in the trusted broker on the guest. They must never enter Pi, its tool cell, transcripts, prompts, request bodies, audit records, or logs. The broker's protected credential handle and secret-loading path are pending; this document does not define a private path or secret interface.
- Keep the exact validated request body separate from credentials. Treat the profile ID, exact model ID, runtime/build versions, `User-Agent`, and `x-opencode-session` as non-secret request metadata. Freeze the selected model and runtime in the evaluation profile before a trial; keep the Go session ID stable for that conversation.
- OpenRouter and OpenCode Go calls reach hosted services. Report them as hosted-provider tests. An offline verifier run or a self-hosted test does not establish a hosted provider exchange, and a hosted exchange does not establish an offline run.

No credentials were read, configured, or used while preparing this note.
