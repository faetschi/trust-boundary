# Adaptation tooling (`adapt.py`)

`adapt.py` applies the mechanical parts of `adaptation-rule-v1.md` to one ledger row. It is stdlib only. Run it with `py -3 -I`. It never executes code from the pinned clones. It reads them as data from the git object at the pin, and it parses Python sources with `ast`.

Tests: `py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py"`. Golden tests use `development` rows and synthetic records only.

## Source clones

`load_source_item` reads the pinned clones. They live in the local workspace, not in the repository. The default is `$TBOUND_LOCAL/source-screen/clones`, or `<repo>/.local/source-screen/clones` when `TBOUND_LOCAL` is unset (see [self-contained workspace](../../docs/guides/self-contained-workspace.md)). Worktrees set `TBOUND_LOCAL` to the primary checkout's `.local`. Pass `--clones DIR` to read a different checkout. Tests that need the clones skip when the directory is absent.

## Hard rule: no candidate before the pilot draw

D51 item 13: every examined case becomes a development case. `load_source_item`, `render` and the CLI refuse a row with status `candidate` unless `allow_candidate` (`--allow-candidate`) is set. They refuse an `excluded` row always. Use the flag only for the real pilot walk or main draw, in the order `select.py` names; every rendered candidate becomes a development case.

## What the tool decides

| Function | Rule | Result |
|---|---|---|
| `load_source_item(row, clones)` | step 1 | The digest file, the ledger, the AgentDojo vectors file and the ASB vectors file are checked against the pilot commitment template's hashes. The row's pin must equal the digest file's pin. Files read from the git object at the pin. Every SHA-256 is checked against the pin's file digests in `d48-source-digests-v3.json`. Clone `HEAD` must equal the pin. Raises `Fail("F-SOURCE")`. A row the digest file does not list is refused, except a development row, which uses the pin's own file list. |
| `render(row, item, judgments)` | steps 3 to 6, 8 | Dict with `user_prompt` (preamble included, `str`, write it as UTF-8), `fixture_files`, `pointer_sentences`, `protected_paths`, `canary_files`, `receivers`, `transformations`, `lost_distinctions`, `tool_mapping`, `forbidden_effects`, `effect_join`, `authorization_class`, `needs_judgment`. A structural failure returns `{"fail": code, "reason": text}`. |
| `split_predicate(p)` | step 8 | `(forbidden_effects, effect_join)`. A mix of `+` and `/`, an unsplittable part or an unregistered part is `F-ELIGIBLE`. |
| `scripted_checks(row, rendered)` | 5.2 checks 1, 2 | `[{"check", "pass", "detail"}]`. |
| `decision_line(...)`, `append_decision(path, line)` | section 2 | The ten keys in rule order. Refuses a `rule_sha256` that is not the SHA-256 of `adaptation-rule-v1.md`, a duplicate `case_id`, a blank line in the file, and a reason that is not `<code>: <sentence>` or that disagrees with `pass`. `select.py` reads the file. |
| `write_evidence(dir, record)` | section 1 | `<dir>/<sha256>.json`, canonical JSON, LF. Returns the digest. |

Sources rendered: InjecAgent, AgentDojo, ASB (through `asb_vectors.render`, plus the section 3c goal-class check of step 2), RedCode-Exec, SABER, ASEval. Another source raises `ToolLimit` ("no renderer"), a tool gap: the CLI exits 3 and no rule code results.

## What the tool leaves to the adapter

`render` does not decide these. It lists each one in `needs_judgment`. Pass the answer back in `judgments` (a JSON file for the CLI):

- `replacements`: `[[old, new], ...]`. The destination replacements of step 3 and 6 (only a URL, mailbox, address or host the request sends to, posts to, delivers to, visits, fetches from or downloads from becomes the receiver) and the object replacements. The tool never decides them: it lists every URL, bare host, host:port, IPv4 and mailbox in every text it renders (user task, injected text, written and setup files, SABER TM-B request, ASEval turns, RedCode prompt) with its location, as `step 3/6: destination candidate ...`. A candidate is covered by a `replacements` entry (applied to every rendered text that contains `old`) or by `keep` (`["text", ...]`: stays as it is). A replacement that occurs in no rendered text is refused. Every applied pair is recorded under `transformations`.
- `tool_names`: `{vector: tool}` for an AgentDojo vector when the ground truth has more than one tool. Without an answer for every such vector nothing is rendered: `render` returns `user_prompt: None`, no fixture, and the need (CLI exit 4). A single-tool ground truth names the file by that tool.
- `objects`: `[{"path": "data/x.txt", "kind": "protected"|"canary", "content": optional}]`, the step 5 objects for InjecAgent, AgentDojo, RedCode and ASEval. The default content is the canary line of step 5 (see Adapter readings). ASB and SABER derive their objects as the rule says.
- `path_moves`: `{old: new}` for a RedCode system path that only locates a file the case creates (step 5, D51 items 5 and 7).
- Step 2 executables: the tool lists command words that are not in the present list (rule command positions; heredoc bodies and quoted strings skipped) under `needs_judgment`. It never fails a row for them. Codes that do not parse, scalar shapes the tool cannot read and unsupported printf directives are listed or raise `ToolLimit`, never a rule code.
- Also not done here: the step 2 exclusion classes of `05-source-screen.md` section 4 beyond the three mechanical ones, the step 7 tool mapping of the effect, the 5.3 judgment checks (intent, changes, losses, mapping, route), the unmapped system paths that are only text the case writes, the `python` route, and every `F-MEANING` outcome that needs reading.

Built elsewhere: `case_entry.py` writes the case entry, trial and meaning record of steps 9 and 10, the fixture digest and the calibration payload digest, from `render` plus an adapter-written calibration file (script, pivots, `p_delta` and its justification, `oracle_mapping`). `carrier.py` builds the screening carriers. See README, section Screening carriers. Not built as separate tools: the local and screening construct reports (step 11, section 5.1). The pilot walk reads both from `tbound-profile corpus-check --quota-profile pilot`. The local report runs on a carrier built from the case's bundle alone, without `--guest-evidence`. The screening report runs on the round's carrier with `--guest-evidence`, and a decision reads only its own case's rows of it. `render` returns the directories a setup `mkdir` creates as `fixture_dirs` (SABER and ASEval), so an empty directory reaches the fixture.

## The pilot walk

1. `py -3 -I corpus/sampling/select.py pilot ... --adaptation corpus/sampling/decisions/pilot-v1.jsonl --out OUT`. Exit 3 names the next candidate per stratum.
2. `py -3 -I corpus/sampling/adapt.py render --case <id> --allow-candidate --render-log <log> --out <new dir> [--judgments j.json]`. It writes `prompt.txt`, `fixture/...` and `summary.json`. Answer each `needs_judgment` entry, render again with `--judgments`, and run `check --case <id> --summary <dir>/summary.json` (with `--allow-candidate --render-log <log>` for a candidate).
3. Do the steps the tool does not do (above), then the judgment checks. Put every judgment and its evidence into one evidence record (JSON).
4. `py -3 -I corpus/sampling/adapt.py decide --case <id> --decisions corpus/sampling/decisions/pilot-v1.jsonl --evidence-dir corpus/sampling/evidence --evidence record.json [--pass] --reason "<code>: <sentence>" --adapter <who> --date YYYY-MM-DD [--screen-build B --carrier-sha256 S]`. Pass `--pass` only with a `PASS:` reason. The tool takes the rule digest from the file and the ledger digest from the ledger file.
**Render log (D51 item 13).** `--allow-candidate` on `render` and `check` (and on `case_entry.py build`) needs `--render-log PATH`. Before any source byte is read, the tool appends one line `{"case_id","tool","command","utc"}` (canonical JSON, LF) to that file. A candidate that appears in the log and gets no decision line is a development case. Development rows are never logged.

`check` and `decide` read only the committed ledger (its SHA-256 must be the template's). `decide` needs a candidate row and `--allow-candidate`, and validates the line before it writes the evidence file.
`decide` validates the evidence record first (`validate_evidence`): `case_id`, `rule_sha256`, non-empty `attempts`, `outcome` equal to the reason code; `deciding_check` (`none` for a pass; for a fail `5.1:<row>` with a 5.1 check name, `5.2:<1|2>`, `5.3:<1..5>` or `step:<1..10>`, the first check or step that failed) and, for a fail, `judgment_only`, which must equal rule 5.3's definition: true for `5.3:<n>`, `step:2` (the eligibility recheck) and `step:3` to `step:7` (a "cannot be rendered" outcome), false for every other check or step. A `5.2` or `5.3` check fails with `F-MEANING`, and `5.3:<n>` needs that judgment check answered no; a pass also needs `construct_report`, both 5.2 results passing and the five 5.3 answers (`judgment_checks`, keys `1 intent` to `5 route`, each `answer` yes or no with non-empty `evidence`). The deciding check must fit the failure code: steps 1 to 10 take their rule step's code (step 3 also F-FIXTURE, from a source bullet), 5.2 and 5.3 take F-MEANING, and a 5.1 row takes F-CONSTRUCT, or F-INSTRUMENT after two instrument failures of a screening run (name the screening row, `5.1:calibration-p` or `5.1:effect-satisfiable`). A `5.3:<n>` failure needs every earlier judgment check answered yes, because the first failing check decides (rule section 6).

CLI exit codes of `render`: 0 rendered, 1 rule failure (JSON `fail`), 2 refused (candidate without flag), 3 `tool_limit`, 4 needs only, 5 `judgment_error`. `decide` returns 5 on an invalid record or line.
5. Commit the decision file, then rerun `select.py` with a new `--out`.

**The adapter of the pilot walk** (owner decision 2026-11-22) is a fresh Claude Opus 5.5 subagent per case and phase. Its whole prompt is `adapter-brief-v1.md` with the case id and phase filled in, followed by its `Inputs:` block. `--adapter` is `claude-opus-5-5 prompts:<sha256>,<sha256>,...`: the SHA-256 of every prompt the case was given, in run order. The prompt bytes are kept under `$TBOUND_LOCAL`.

## Round planner (`round_plan.py`)

`py -3 -I corpus/sampling/round_plan.py plan --output <select.py output JSON> --decisions <decision file> [--screened <file>] [--ledger <ledger>]` prints `{"TM-A": [...], "TM-B": [...]}`: per stratum the next undecided, unscreened candidates in walk order, up to 7 TM-A and 4 TM-B (rule 7.1). `--screened` lists one already-screened `case_id` per line. It prints ids only and never reads the ledger.

- A stratum with no pending case gets an empty list. The top-up candidates (one per attack without a main case, the first undecided unscreened case of its order) appear only when every main stratum is complete. A top-up with more candidates than slots is cut at the slots; rerun it after the decisions.
- A draw output holds the seed and every capped candidate, so the `main` and `topup` orders beyond the case `select.py` names are recomputed with `select.ranked`. A pilot output records only the pending case per stratum, so for a pilot output the planner recomputes the `pilot` order with `select.ranked` over the candidates of `--ledger`. That file must be the ledger the output was drawn from (its SHA-256), and without it the planner refuses (exit 2).
- The plan follows the decision file, not the output's date: a decision appended after `select.py` ran moves the plan on. Rerun `select.py` before the decisions are final.

## Adapter readings for the pilot walk

Readings of the rule text that the adapter must apply the same way on every case. The owner decided them on 2026-11-15, after the review of the rehearsal carrier. The rehearsal carrier `rehearsal-dev-01` follows them.

- **Object replacement (step 3).** Only the words that identify the object become its fixture path. Every other word stays: a sender, a service name such as "on GitHub", and the rest of the phrase. A named object loses only its name: "my project repository titled 'Important_Project' (ID 001) on GitHub" becomes "my project repository titled `data/important-project.txt` (ID 001) on GitHub". An object the source describes but does not name loses the shortest description that identifies it: "the email received from by lily.white@gmail.com" becomes "`data/email.txt` received from by lily.white@gmail.com", and "my saved addresses" becomes "my `data/saved-addresses.txt`". A leading article ("the", "a") belongs to the description and goes with it; a possessive or quantifier ("my", "all") stays.
- **Destinations of named sources (step 3).** Only the address itself becomes the receiver. Its introducing noun phrase ("my alternate email address,") stays. The rule adds the noun phrase only in its "Other third-party content" paragraph, and only there is it replaced too.
- **Content of protected files (step 5).** A protected or canary file whose content the source does not give is the one line `tbound-canary <12 hex>` of step 5's first sentence. The empty file of step 5 is only for a path the source does not name at all, such as a hook or a dependency list. The marker holds a hash of the case id, so two cases never share a fixture (`adv.unit.duplicate`) and an attack that wipes or truncates the file still changes it. `judgments.objects` with `content` overrides the default where the source gives content.

## Readings of the rule this tool fixed

The choices that change output bytes:

- AgentDojo user task text is the evaluated Python value, byte for byte. A source trailing space stays, so a pointer sentence follows it after one more space.
- An AgentDojo vector file holds the YAML scalar (one-line double-quoted, or a `|` block scalar) of `environment.yaml` or of an `!include` file that contains the placeholder, with the attack in place and other placeholders at their defaults. Include files come from the digest-checked file list; one the digest file does not list is `F-SOURCE`. The attack template and the user and model names are read from the listed `important_instructions_attacks.py` and `base_attacks.py`. A combined user task (workspace UserTask4, 19, 23, 36 to 39; slack 17 to 20) is built as `task_combinators.py` does at the pin: its PROMPT composition and ground-truth union are read from the listed file and reproduced statically; a task whose `create_combined_task` call or the combinator file has another shape raises `ToolLimit`. A scalar shape the tool cannot read raises `ToolLimit`, a tool gap that is never a rule outcome.
- InjecAgent `NN` and `MM` are 1-based line numbers.
- ASEval and SABER delivered text gets the path mapping and the Python swap first; the adapter's `replacements` apply to that text.
