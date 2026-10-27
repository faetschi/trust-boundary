# Run the blinded construct-review panel

This guide is for the owner. It runs step 2 of the D35 amendment (2026-10-24):
three AI models from three families rate every corpus case against the thesis
§7.3 construct-validity checklist. The tool then reports agreement before
adjudication and writes the audit list for the advisor. The advisor's signed
`audit.json` and the run's `receipt.json` then feed `tbound-profile corpus-digest`
(step 8).

`tbound-panel` does not run the deterministic checks of D35 step 1, does not
edit `manifest.draft.json`, and records no human review. AI ratings never count
as human reviews.

## What each model sees

Each model rates each case in two separate calls. Every call is a fresh
single-turn chat with no tools, temperature 0 and a `max_tokens` limit. All
phase-1 calls run before any phase-2 call.

**Phase 1, blind classification.** The request contains only:

- the task or stimulus text, verbatim
- the workspace fixture (canary files and case-named canary markers withheld)
- the agent's tool set: Pi read, write, edit and bash in a sandboxed command cell
- the public policy context: the case's registered protected paths, the bash
  output paths and the allowed network destinations

The threat model assumes the agent knows the public policy (thesis §3.2.1), so
this context is case content, not a claimed label. Showing the registered
protected paths in phase 1 is deliberate: a protected path tells the rater where
a workspace effect would count, but not whether the case targets it. Report
this design choice with the phase-1 statistics. The model returns its own
threat class, authorization (`allowed`, `forbidden` or `ambiguous`), effect ids
and, for a task it judges allowed, a completion predicate. It sees no claimed
threat class, authorization, effect ids, oracle, expected route, intent or
source.

**Phase 2, checklist.** The request adds:

- the source benchmark item, preserved intent, transformations and lost
  distinctions
- the claimed label, oracle, route and tool mapping to judge, with the proposed
  stimuli, acceptance checks or rubric excerpt

The model answers `pass`, `fail` or `unclear` with a reason for each checklist
item. The `sensitivity` item is predeclared n/a for ambiguous cases.

No request contains configuration names, system verdicts, outcomes, run
settings, other raters' answers or case ids. Each model and phase gets its own
random item ids and call order, both derived from `seed`. The renderer fails
closed when a blinding pattern survives, and redacts free-text lines that name
one.

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
- routers and moving aliases: `openrouter/*`, any id containing `router`, ids
  starting with `~` or ending in `-latest`, and `:online` variants
- a model without `max_tokens` or prices, because the budget cap needs a worst case

The family of an id is the first match in this table, checked against the
lowercase id; otherwise it is the vendor prefix before `/`:

| Id contains | Family |
|---|---|
| `gemini`, `gemma`, `google/` | gemini |
| `qwen`, `qwq` | qwen |
| `llama` | llama |
| `mistral`, `mixtral`, `stral`, `wizardlm` | mistral |
| `grok`, `x-ai/` | grok |

Excluded whenever the id contains `anthropic`, `claude`, `openai`, `gpt` or
`deepseek`. A fine-tune whose id hides its base model counts as its vendor;
check such ids by hand. After each call, the tool compares the family of the
model that answered with the configured one. On a mismatch it marks the answer
invalid and stops.

Prefer dated model ids over aliases that can move. For reasoning models,
`max_tokens` also limits the reasoning tokens, so keep it at 4000 or more. Each
request sets `provider.require_parameters`, so OpenRouter routes only to hosts
that honour temperature and `max_tokens`, and `provider.data_collection: "deny"`,
so no host may store or train on the cases (MultiPL-E forbids ML-training use). If a model rejects temperature 0, use
its deterministic setting and record the reason.

`public_policy` is required. It lists the bash output paths and the allowed
network destinations of the service config that runs the corpus. The default
holds the pilot values: no bash output paths
(`infra/guest/serve-pi-service.corpus-pilot.json`) and no network destination
(network-none command cell). Check them against the service config before the
run. The receipt records this policy as `public_policy`, and `tbound-op cfg-install`
refuses the corpus service config unless its `workflow_bash_output_paths` equal the recorded
`bash_output_paths` and the recorded `network_destinations` are empty (the service config names
no destination).

`budget_usd` is the hard cap, at most 10. You can set it lower. Keep `seed`
fixed once you have run the panel. `audit_sample` is optional and must be 60, the
registered size of the unanimous audit sample.

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
33 cases × 3 models × 2 phases make 198 calls. With the illustrative prices
above, the expected cost is about USD 1.20 and the worst case about USD 5.69.

## 4. Do a dry run

```powershell
.\tbound-panel.exe --config $HOME\tbound-panel\panel.json --manifest ..\corpus\pilot\manifest.draft.json --out $HOME\tbound-panel\dry-1 --dry-run
```

The dry run uses a fake provider with canned answers. It makes no network call
and needs no key. Check that it prints the agreement table (AC1, raw agreement and kappa)
and writes every output listed in step 6. The fake ratings mean nothing. A dry-run directory cannot be
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
Spend is the cost that OpenRouter reports per response. If a response reports
no cost, the tool books the call's reserved worst case as spent and stops; it
never computes spend from the configured prices. A call with an unknown
outcome, such as a timeout, keeps its whole worst case counted. If a reported
cost exceeds the reserved worst case, the configured prices are too low. The run
then stops until you fix them.

To resume, run the same command again with the same `--out`. Each case, model
and phase with a valid answer is skipped, so a completed phase is not called
again. Invalid answers are recorded and retried on resume. If you
change a frozen setting (models, temperatures, `max_tokens`, `seed`, prompts or
manifest), the tool refuses the old directory. Use a new `--out`.

Once the advisor has filled any `advisor_signoff` field in `audit.json`, the same
command refuses to run: it would rebuild the audit and write a new `receipt.json`,
erasing the sign-offs. Copy the output directory first, or move `audit.json` aside,
before you regenerate. An audit with every sign-off field empty is rebuilt normally.

| Exit code | Meaning |
|---|---|
| 0 | Every pair rated, reports written |
| 1 | Error, for example a provider failure; partial reports written |
| 2 | Usage error |
| 3 | Budget cap reached; partial reports written |

## 6. Read the outputs

| File | Content |
|---|---|
| `receipt.json` | Models, families, observed response models and hosts, settings, seeds, `public_policy`, the manifest digests (`manifest_canonical_digest`, `manifest_file_sha256`) and `content_digest`, spend and cap. Schema `tbound-panel-receipt/v3` |
| `settings.json` | The frozen settings a resume must match |
| `blinding.json` | Seed, random item ids and call order per model and phase |
| `requests/p1`, `requests/p2`, `responses/` | Every raw request body (without the key) and raw response, per phase |
| `calls.jsonl` | Spend ledger: a reservation before each call, then the settled cost |
| `ratings.jsonl` | One parsed answer per call with its phase, marked valid or invalid with its reason |
| `stats.md`, `stats.json` | JSON schema `tbound-panel-stats/v4`, `content_digest`, and `dry_run`. Before adjudication: Gwet's AC1 (the gate statistic), raw agreement, Fleiss' kappa, prevalence, and a 95% root-bootstrap interval of AC1 and of kappa per phase-2 checklist item and per phase-1 field, plus the phase-1 agreement with the claimed label per field |
| `audit.md`, `audit.json` | The advisor's audit list and sign-off fields. `audit.json` (schema `tbound-panel-audit/v4`) records `manifest_digest`, `content_digest`, `dry_run` and `receipt_digest`, the `sha256:` of the exact bytes of the `receipt.json` written just before it |
| `audit-packets/` | The blinded packet of each audited case |

Phase-1 effect ids are compared as a set: two answers agree only when they name
exactly the same ids. The claim agreement rate counts every model's phase-1
answer against the manifest claim. Effect ids are n/a for ambiguous cases,
which register no forbidden effect.

The gate is applied per checklist item and per phase-1 field:

| Result | Rule |
|---|---|
| `pass` | Gwet's AC1 ≥0.80 and raw agreement ≥90% |
| `revise` | AC1 0.60–0.79, or AC1 ≥0.80 with agreement below 90% |
| `stop` | AC1 below 0.60 |
| `no-pass-undefined-ac1` | AC1 undefined: no case has a pair of ratings |
| `incomplete-no-pass` | Any case, model and phase lacks a valid answer (refusal, broken JSON, budget stop); replaces every gate, and `stats.json` lists the missing pairs |

Raw agreement is Fleiss' observed agreement, the mean share of agreeing rater
pairs per case. The gate statistic is Gwet's AC1 (Gwet 2008, Br. J. Math. Stat.
Psychol. 61, 29-48), not Fleiss' kappa: kappa collapses at high prevalence (410 unanimous
`pass` and 9 single dissents give 98.6% agreement and kappa -0.007, and all-`pass` leaves
kappa undefined), while AC1 stays 0.986 and 1. With 3 raters, Pa is the mean share of agreeing
pairs, pi_k the mean share of ratings in category k, Pe = sum pi_k (1 - pi_k) / (q - 1) with q = 3
(`pass`, `fail`, `unclear`), and AC1 = (Pa - Pe) / (1 - Pe). Fleiss' kappa (with its defined flag),
prevalence and the kappa interval are still reported for every item and are not gated. The AC1 and
kappa intervals resample source roots with replacement, 2000 times with `seed`, on the same
resamples, and use the percentile method. Phase-1 fields report AC1 too, over the closed category
lists (`effect_ids` over the observed sets). Agreement alone never accepts a failed construct
check: a `fail` rating makes a case a mandatory audit case even when all three models agree.

## 7. Send the advisor the audit

Send `audit.md`, `audit.json`, `audit-packets/` and `stats.md`. The audit list
contains:

- every case with a checklist disagreement
- every case with an `unclear` rating
- every case with a `fail` rating, unanimous or not
- every case where the models' phase-1 answers differ from each other or from
  the claimed label
- every ambiguous case
- any case with incomplete ratings
- a random sample of up to 60 unanimous roots, drawn with the recorded seed and
  listed with the eligible roots

`audit.json` shows each listed case's claimed labels, phase-1 answers and
phase-2 ratings. The advisor reviews each listed case and fills the four
`advisor_signoff` fields of its entry in `audit.json`:

| Field | Value |
|---|---|
| `decision` | `accept`, `reject` or `revise` |
| `resolution` | The resolution and its reason under the frozen panel rubric |
| `identity` | The advisor's name |
| `signed_at` | RFC 3339 date and time with timezone, for example `2026-10-25T10:00:00+02:00` |

The `resolution` text must equal the `resolution` of the case's sign-off in the manifest.
The advisor then signs `audit.md`. An audit error expands review across that
case's source and tool stratum. Keep this round's ratings and statistics
unchanged. A revised case needs a new run directory. Only `accept` can back an
accepted manifest; a case the advisor rejects or revises needs a corrected
manifest and a new panel run.

The advisor is a non-author human and signs every case of the corpus, not only
the listed ones: the listed cases get the deep review, the others the meaning
record check of thesis §7.3. AI ratings never count as that review, and the case
author's own review is not independent.

## 8. Bind the signed audit to the manifest

The panel rated the draft before any sign-off existed. The manifest then gains
the advisor's sign-offs, which must not detach it from the run. `receipt.json`,
`audit.json` and `stats.json` therefore record `content_digest`, the digest of the manifest
with every `signoff`, `construct_review` and `pins` cleared
(`evalcorpus.ContentDigest`). Writing the sign-offs or freeze pins does not change it;
changing a case does. `manifest_digest` keeps the digest of the whole file the
panel read. The environment, model, profile and policy pins may be filled from
`corpus-pins` after the panel run, but must be filled before `corpus-digest`.
The full corpus `Digest` still includes every pin. The receipt embeds per-call
`rating_outcomes` for every case, model and phase. Its pinned byte digest binds
these ratings without an additional ratings file. Keep `ratings.jsonl` as the
raw-round record.

In the manifest:

1. Set every case's `signoff` to the advisor's name as `reviewer`, `human: true`,
   `author: false`, `decision: "accept"`. For each case on the audit list also
   set `audited: true` and `resolution` to the advisor's resolution. A
   resolution on an unlisted case, or a listed case without one, is refused.
2. Set `construct_review.content_digest` to `content_digest` of the receipt.
3. Set `construct_review.panel_receipt_digest` and
   `construct_review.panel_audit_digest` and `construct_review.panel_stats_digest`
   to the `sha256:` of the bytes of `receipt.json`, the signed `audit.json`, and
   `stats.json`, for example with `sha256sum receipt.json audit.json stats.json`.
   Do not reformat these files afterwards.

Then pass all three files to the admission command:

```sh
tbound-profile corpus-digest --manifest corpus/pilot/manifest.json --quota-profile pilot \
  --panel-receipt <run>/receipt.json --panel-audit <run>/audit.json --panel-stats <run>/stats.json \
  --artifacts corpus/pilot/artifacts --node "$(command -v node)" \
  --max-host-calls 17 --max-tokens 20000 --duration-seconds 1200
```

It prints the digest only if the file hashes match `construct_review`, the audit's
`receipt_digest` is the digest of the receipt file (a signed audit cannot back another
run's receipt), all three
files record the manifest's content digest, all explicitly mark a live OpenRouter run, none is a dry run, the receipt
rates every case completely, the audit list and the cases marked `audited` are
the same set, and every audit entry carries a decision `accept`, a resolution, an
identity matching the manifest advisor and a time. It replays audit selection
with the panel's own seeded selection function and requires 60 unanimous roots,
or all if fewer are eligible. A dry-run directory is never accepted.
`stats.json` uses schema `tbound-panel-stats/v4`, records `content_digest`, and
must give every phase-2 checklist item a `pass` gate. The reader does not trust its numbers: it
rebuilds every item's subjects from the receipt's `rating_outcomes` (applicable cases, receipt
model order) and recomputes subjects, roots, AC1, kappa, raw agreement, unanimous share and
prevalence, refuses unless each matches within 1e-9, and refuses unless the recomputed gate is
`pass` (AC1 at least 0.80 and raw agreement at least 0.90). The audit seed must equal the receipt's
run seed. The reader checks every required statistics field (the receipt also requires `public_policy`), the three distinct permitted model
families, and complete case x model x phase outcomes. It derives mandatory audit
cases from those pinned outcomes and from the manifest's ambiguous cases. Missing ratings
in either phase block admission. Phase-1 agreement and its gates remain reported;
phase-1 gate values do not determine admission. Keep revised rounds separately.
`preregistration-record` requires and verifies the same three panel files.
