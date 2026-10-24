# Run the blinded construct-review panel

This guide is for the owner. It runs step 2 of the D35 amendment (2026-10-24):
three AI models from three families rate every corpus case against the thesis
§7.3 construct-validity checklist. The tool then reports agreement before
adjudication and writes the audit list for the advisor.

`tbound-panel` does not run the deterministic checks of D35 step 1, does not
edit `manifest.draft.json`, and records no human review. AI ratings never count
as human reviews.

## What each model sees

Each request is a fresh single-turn chat with no tools, temperature 0 and a
`max_tokens` limit. A request contains the system prompt and one case packet:

- the task or stimulus text, verbatim
- the source benchmark item, preserved intent, transformations and lost
  distinctions
- the workspace fixture (canary files and case-named canary markers withheld)
- the claimed label, oracle, route and tool mapping to judge, with the proposed
  stimuli, acceptance checks or rubric excerpt

A request never contains configuration names, system verdicts, outcomes, run
settings, other raters' answers or case ids. Each model gets its own random item
ids and its own call order, both derived from `seed`. The renderer fails closed
when a blinding pattern survives, and redacts free-text lines that name one.

Each model answers `pass`, `fail` or `unclear` with a reason for each checklist
item. It also gives its own threat class, authorization and effect ids. The
`sensitivity` item is predeclared n/a for ambiguous cases.

## 1. Build the command

From `supervisor/`, in PowerShell or WSL:

```sh
go build -o tbound-panel.exe ./cmd/tbound-panel
```

In WSL, name the output `tbound-panel`. The steps below use the Windows name.

## 2. Choose three models

Copy `supervisor/cmd/tbound-panel/panel.default.json` to a private folder
outside the repository, for example `%USERPROFILE%\tbound-panel\panel.json`.
The default lists no models. Its `example_models` are candidates only.

Fill `models` with exactly three entries. Take each id and both prices from the
model's page on openrouter.ai/models. Prices are USD per million tokens.

```json
"models": [
  {"id": "google/gemini-2.5-pro", "temperature": 0, "max_tokens": 4000,
   "prompt_usd_per_mtok": 1.25, "completion_usd_per_mtok": 10},
  {"id": "qwen/qwen3-235b-a22b-2507", "temperature": 0, "max_tokens": 4000,
   "prompt_usd_per_mtok": 0.2, "completion_usd_per_mtok": 0.8},
  {"id": "mistralai/mistral-large-2411", "temperature": 0, "max_tokens": 4000,
   "prompt_usd_per_mtok": 2, "completion_usd_per_mtok": 6}
]
```

The values above are illustrations, not a selection or current prices.

The tool refuses a panel that breaks a D35 rule:

- fewer or more than three models
- two models of one family; a reseller fine-tune counts as its base family
- any Claude/Anthropic, GPT/OpenAI or DeepSeek model, including distills
- `openrouter/auto` and `:online` variants
- a model without `max_tokens` or prices, because the budget cap needs a worst case

Prefer dated model ids over aliases that can move. For reasoning models,
`max_tokens` also limits the reasoning tokens, so keep it at 4000 or more. Each
request sets `provider.require_parameters`, so OpenRouter routes only to hosts
that honour temperature and `max_tokens`. If a model rejects temperature 0, use
its deterministic setting and record the reason.

`budget_usd` is the hard cap, at most 10. You can set it lower. Keep `seed`
fixed once you have run the panel.

Freeze the config before the live run. D35 requires pinned model ids,
settings, prompts and rubric before review. Keep a copy of `panel.json` with the
run, and disclose any prior exposure of the models to the corpus.

## 3. Estimate the cost

```powershell
.\tbound-panel.exe --config $HOME\tbound-panel\panel.json --manifest ..\corpus\pilot\manifest.draft.json --estimate
```

The estimate calls nothing and writes nothing. It prints calls, prompt bytes,
estimated tokens, an expected cost and a worst case. The worst case counts
every prompt byte as a token and every answer at `max_tokens`. For the pilot,
33 cases × 3 models make 99 calls. With the illustrative prices above, the
expected cost is about USD 0.91 and the worst case about USD 3.18.

## 4. Do a dry run

```powershell
.\tbound-panel.exe --config $HOME\tbound-panel\panel.json --manifest ..\corpus\pilot\manifest.draft.json --out $HOME\tbound-panel\dry-1 --dry-run
```

The dry run uses a fake provider with canned answers. It makes no network call
and needs no key. Check that it prints the kappa table and writes every output
listed in step 6. The fake ratings mean nothing. A dry-run directory cannot be
resumed as a live run.

## 5. Run the panel

Create an OpenRouter key with a credit limit, for example USD 10. The key limit
is a second spending stop. Then set the key for this shell only:

```powershell
$s = Read-Host -AsSecureString "OpenRouter key"
$env:OPENROUTER_API_KEY = [Net.NetworkCredential]::new('', $s).Password
.\tbound-panel.exe --config $HOME\tbound-panel\panel.json --manifest ..\corpus\pilot\manifest.draft.json --out $HOME\tbound-panel\live-1 --live
Remove-Item Env:OPENROUTER_API_KEY
```

In bash, use `read -rs OPENROUTER_API_KEY; export OPENROUTER_API_KEY` and then
`unset OPENROUTER_API_KEY`.

The key is read from the environment, sent only in the HTTP `Authorization`
header, and never written to disk or logs.

Before each call, the tool adds the recorded spend to the worst case of the
next call. If the sum would exceed `budget_usd`, it stops with exit code 3.
Spend is the cost that OpenRouter reports per response. A call with an unknown
outcome, such as a timeout, keeps its whole worst case counted. If a reported
cost exceeds the reserved worst case, the configured prices are too low. The run
then stops until you fix them.

To resume, run the same command again with the same `--out`. Pairs with a
valid rating are skipped. Invalid answers are recorded and retried on resume. If you
change a frozen setting (models, temperatures, `max_tokens`, `seed`, prompts or
manifest), the tool refuses the old directory. Use a new `--out`.

| Exit code | Meaning |
|---|---|
| 0 | Every pair rated, reports written |
| 1 | Error, for example a provider failure; partial reports written |
| 2 | Usage error |
| 3 | Budget cap reached; partial reports written |

## 6. Read the outputs

| File | Content |
|---|---|
| `receipt.json` | Models, families, observed response models and hosts, settings, seeds, manifest digests, spend and cap |
| `settings.json` | The frozen settings a resume must match |
| `blinding.json` | Seed, per-model random item ids and call order |
| `requests/`, `responses/` | Every raw request body (without the key) and raw response |
| `calls.jsonl` | Spend ledger: a reservation before each call, then the settled cost |
| `ratings.jsonl` | One parsed rating per call, marked valid or invalid with its reason |
| `stats.md`, `stats.json` | Per-item Fleiss' kappa, raw agreement, prevalence and a 95% root-bootstrap interval, before adjudication |
| `audit.md`, `audit.json` | The advisor's audit list and sign-off fields |
| `audit-packets/` | The blinded packet of each audited case |

The gate is applied per checklist item:

| Result | Rule |
|---|---|
| `pass` | Kappa ≥0.80 and raw agreement ≥90% |
| `revise` | Kappa 0.60–0.79, or kappa ≥0.80 with agreement below 90% |
| `stop` | Kappa below 0.60 |
| `no-pass-undefined-kappa` | Kappa undefined, for example when every rating is `pass` |

Raw agreement is Fleiss' observed agreement, the mean share of agreeing rater
pairs per case. The interval resamples source roots with replacement, 2000
times with `seed`, and uses the percentile method. Agreement alone never
accepts a failed construct check.

## 7. Send the advisor the audit

Send `audit.md`, `audit.json`, `audit-packets/` and `stats.md`. The audit list
contains:

- every case with a disagreement
- every case with an `unclear` rating
- every ambiguous case
- any case with incomplete ratings
- a random sample of up to 60 unanimous roots, drawn with the recorded seed and
  listed with the eligible roots

The advisor reviews each listed case and fills the sign-off fields in
`audit.json`: decision, resolution, identity and time with timezone. The
advisor then signs `audit.md`. An audit error expands review across that case's
source and tool stratum. Keep this round's ratings and statistics unchanged. A
revised case needs a new run directory.
