# Research route configuration

- `research-profile.json` is the research provider profile (OpenRouter, `deepseek/deepseek-v4.1-flash`, DeepInfra fp8 pin, reasoning effort high (D50), price ceilings, token and request limits).
- `research-ledger.json` is the research ledger configuration (cap US$10, reserve basis `request`, stop value).
- At step RR-1 of `../PROVISIONING-SIGNING.md` they are installed as `/etc/tbound/provider/research-profile.json` and `/etc/tbound/provider/research-ledger.json`.
- The API key is never in this directory. It is installed separately at RR-2.
- `frozen_at` is null on purpose, so the profile is OPEN. `max_input_tokens` (131072, owner 2026-11-08, sized for reasoning passback) is rechecked against the research pilot. Both are set at the F-H1 freeze, together with `min_per_unit_worst_case_micro_usd` in the ledger file.
- `supervisor/internal/researchroute/committed_profile_test.go` pins the ledger stop value to the profile: it must equal the worst case of one full unit (`max_requests_per_unit` requests at `max_input_tokens` and `max_output_tokens`, at the ceiling prices).
