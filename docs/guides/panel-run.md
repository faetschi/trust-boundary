# Run optional supplementary AI analysis

The [owner protocol decisions of 2026-10-29](../evaluation/owner-protocol-decisions-2026-10-29.md)
supersede the former D35 panel and advisor-signature gates. `tbound-panel` is
optional supplementary analysis. The critical path is full deterministic
construct evidence, including applicable guest witnesses, followed by
preregistered H1. The supervisor is administrative only, with no input or
sign-off. No advisor, examiner, human audit, or replacement mandatory owner
acceptance gate is required. Independent human review is not claimed.

The tool rates corpus cases in two blinded phases with three model families.
AC1, raw agreement, kappa, and the generated audit selection are diagnostics
only. A diagnostic pass does not validate a construct. Non-pass and incomplete
results do not block admission or preregistration. `tbound-panel` does not run
`CheckConstructs`, edit the manifest, or create human review evidence.
No panel or paid call is authorized by this documentation update.

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

For this optional analysis, the tool retains these panel configuration constraints:

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

`public_policy` is required in the panel configuration. It lists the bash output paths and the allowed
network destinations of the service config that runs the corpus. The default
holds the pilot values: no bash output paths
(`infra/guest/serve-pi-service.corpus-pilot.json`) and no network destination
(network-none command cell). Check them against the service config before the
run. The receipt records this policy as `public_policy`. If a panel bundle is supplied,
the operator checks the installed service policy against that receipt. Without
panel evidence, the normal signed service policy still applies.

`budget_usd` is the hard cap, at most 10. You can set it lower. Keep `seed`
fixed once you have run the panel. `audit_sample` retains the tool
value 60 for diagnostic selection. When fewer unanimous roots are eligible,
the tool selects all eligible roots. This selection creates no human audit
obligation, admission gate, or detection-probability claim.

Retain the config with pinned model IDs, settings, prompts, and rubric for
any optional live run. Keep a copy of `panel.json` with the
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

The retained tool protects a directory with populated legacy `advisor_signoff`
fields from regeneration. Preserve that historical directory and use a new
output directory. No such fields need to be populated for current policy.

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
| `stats.md`, `stats.json` | JSON schema `tbound-panel-stats/v4`, `content_digest`, and `dry_run`. Before adjudication: Gwet's AC1, raw agreement, Fleiss' kappa, prevalence, and a 95% root-bootstrap interval of AC1 and of kappa per phase-2 checklist item and per phase-1 field, plus the phase-1 agreement with the claimed label per field |
| `audit.md`, `audit.json` | Diagnostic selection list and legacy optional metadata fields. `audit.json` (schema `tbound-panel-audit/v4`) records `manifest_digest`, `content_digest`, `dry_run` and `receipt_digest`, the `sha256:` of the exact bytes of the `receipt.json` written just before it |
| `audit-packets/` | The blinded packet of each audited case |

Phase-1 effect ids are compared as a set: two answers agree only when they name
exactly the same ids. The claim agreement rate counts every model's phase-1
answer against the manifest claim. Effect ids are n/a for ambiguous cases,
which register no forbidden effect.

The tool retains these diagnostic labels per checklist item and per phase-1 field.
They are not admission, preregistration, or H1 gates:

| Result | Rule |
|---|---|
| `pass` | Gwet's AC1 ≥0.80 and raw agreement ≥90% |
| `revise` | AC1 0.60–0.79, or AC1 ≥0.80 with agreement below 90% |
| `stop` | AC1 below 0.60 |
| `no-pass-undefined-ac1` | AC1 undefined: no case has a pair of ratings |
| `incomplete-no-pass` | Any case, model and phase lacks a valid answer (refusal, broken JSON, budget stop); replaces every diagnostic label, and `stats.json` lists the missing pairs |

Raw agreement is Fleiss' observed agreement, the mean share of agreeing rater
pairs per case. The diagnostic label uses Gwet's AC1 (Gwet 2008, Br. J. Math. Stat.
Psychol. 61, 29-48), not Fleiss' kappa: kappa collapses at high prevalence (410 unanimous
`pass` and 9 single dissents give 98.6% agreement and kappa -0.007, and all-`pass` leaves
kappa undefined), while AC1 stays 0.986 and 1. With 3 raters, Pa is the mean share of agreeing
pairs, pi_k the mean share of ratings in category k, Pe = sum pi_k (1 - pi_k) / (q - 1) with q = 3
(`pass`, `fail`, `unclear`), and AC1 = (Pa - Pe) / (1 - Pe). Fleiss' kappa (with its defined flag),
prevalence and the kappa interval are still reported for every item. None gates the proof path. The AC1 and
kappa intervals resample source roots with replacement, 2000 times with `seed`, on the same
resamples, and use the percentile method. Phase-1 fields report AC1 too, over the closed category
lists (`effect_ids` over the observed sets). Agreement alone never accepts a failed construct
check. A `fail` rating puts a case on the diagnostic selection list even when
all three models agree. No human audit is required.

## 7. Retain the diagnostic selection

Keep `audit.md`, `audit.json`, `audit-packets/`, and `stats.md` with the run.
The selection includes disagreements, unclear and fail ratings, phase-1
mismatches, ambiguous cases, incomplete ratings, and up to 60 eligible unanimous
roots drawn with the recorded seed. An all-eligible selection is a census of
those roots, not independent human review. No signature or detection-probability
claim follows from it.

The owner controls protocol decisions and provenance. Legacy `advisor_signoff`
and manifest `signoff` fields are optional metadata. Do not invent names,
quotations, resolutions, or signatures to fill them. Historical signed records
remain evidence of their own content snapshot and policy, not new approval.

## 8. Bind optional evidence to the manifest

No panel flags are required by `corpus-digest` or
`tbound-profile preregistration-record`. With none supplied, panel verification
is skipped. To supply a bundle, provide all three nonempty paths. Partial or
explicitly empty paths are refused.

`ContentDigest` retains its existing canonical serialization. It excludes
case sign-offs, `construct_review`, and freeze `pins`. Filling freeze pins does
not change that digest. Changing case content does. The full corpus `Digest`
still includes the pins. Preserve the original panel-input file and hashes.

For a supplied bundle, bind `construct_review.content_digest` to the run's
`content_digest` and the three panel digest members to the exact bytes of
`receipt.json`, `audit.json`, and `stats.json`. Do not reformat them after
hashing. Retain raw ratings, requests, and responses.

The normal admission command needs no panel files. It reads the prepared,
untracked `corpus/pilot/manifest.json` (a copy of `manifest.draft.json` with the
measured pins; see [the pilot README](../../corpus/pilot/README.md)):

```sh
tbound-profile corpus-digest --manifest corpus/pilot/manifest.json --quota-profile pilot \
  --artifacts corpus/pilot/artifacts --node "$(command -v node)" \
  --max-host-calls 17 --max-tokens 69632 --duration-seconds 1200
```

To verify optional evidence, append all three paths:

```sh
  --panel-receipt <run>/receipt.json --panel-audit <run>/audit.json --panel-stats <run>/stats.json
```

Supplied evidence remains subject to bounded reads and strict integrity checks
for file hashes, content and receipt bindings, model identities, outcomes, and
numeric recomputation. Honest non-pass or incomplete diagnostics are allowed.
These checks protect the optional record from inconsistent or fabricated data.
They do not make panel agreement a protocol gate or establish full construct
completion. Run the separate `corpus-check` path, including applicable guest
witness imports, before H1. No human acceptance is required.

The former mandatory panel, AC1/raw-agreement pass requirement, sample-60 audit,
and advisor sign-off workflow are retained in Git history. They were
superseded by the owner's dated decision, not confirmed by an advisor.
