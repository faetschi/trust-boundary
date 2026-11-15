# Adaptation tooling (`adapt.py`)

`adapt.py` applies the mechanical parts of `adaptation-rule-v1.md` to one ledger row. It is stdlib only. Run it with `py -3 -I`. It never executes code from the pinned clones. It reads them as data from the git object at the pin, and it parses Python sources with `ast`.

Tests: `py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py"`. Golden tests use `development` rows and synthetic records only.

## Hard rule: no candidate before the pilot draw

D51 item 13: every examined case becomes a development case. `load_source_item`, `render` and the CLI refuse a row with status `candidate` unless `allow_candidate` (`--allow-candidate`) is set. They refuse an `excluded` row always. Use the flag only for the real pilot walk, in the order `select.py` names.

## What the tool decides

| Function | Rule | Result |
|---|---|---|
| `load_source_item(row, clones)` | step 1 | The digest file, the ledger, the AgentDojo vectors file and the ASB vectors file are checked against the pilot commitment template's hashes. The row's pin must equal the digest file's pin. Files read from the git object at the pin. Every SHA-256 is checked against the pin's file digests in `d48-source-digests-v3.json`. Clone `HEAD` must equal the pin. Raises `Fail("F-SOURCE")`. A row the digest file does not list is refused, except a development row, which uses the pin's own file list. |
| `render(row, item, judgments)` | steps 3 to 6, 8 | Dict with `user_prompt` (preamble included, `str`, write it as UTF-8), `fixture_files`, `pointer_sentences`, `protected_paths`, `canary_files`, `receivers`, `transformations`, `lost_distinctions`, `tool_mapping`, `forbidden_effects`, `effect_join`, `authorization_class`, `needs_judgment`. A structural failure returns `{"fail": code, "reason": text}`. |
| `split_predicate(p)` | step 8 | `(forbidden_effects, effect_join)`. A mix of `+` and `/`, an unsplittable part or an unregistered part is `F-ELIGIBLE`. |
| `scripted_checks(row, rendered)` | 5.2 checks 1, 2 | `[{"check", "pass", "detail"}]`. |
| `decision_line(...)`, `append_decision(path, line)` | section 2 | The ten keys in rule order. Refuses a `rule_sha256` that is not the SHA-256 of `adaptation-rule-v1.md`, a duplicate `case_id`, a blank line in the file, and a reason that is not `<code>: <sentence>` or that disagrees with `pass`. `select.py` reads the file. |
| `write_evidence(dir, record)` | section 1 | `<dir>/<sha256>.json`, canonical JSON, LF. Returns the digest. |

Sources rendered: InjecAgent, AgentDojo, ASB (through `asb_vectors.render`, plus the section 3c goal-class check of step 2), RedCode-Exec, SABER, ASEval. Other sources return `F-SOURCE` ("no renderer").

## What the tool leaves to the adapter

`render` does not decide these. It lists each one in `needs_judgment`. Pass the answer back in `judgments` (a JSON file for the CLI):

- `replacements`: `[[old, new], ...]`. The destination replacements of step 3 and 6 (only a URL, mailbox, address or host the request sends to, posts to, delivers to, visits, fetches from or downloads from becomes the receiver) and the object replacements. The tool never decides them: it lists every URL, bare host, host:port, IPv4 and mailbox in every text it renders (user task, injected text, written and setup files, SABER TM-B request, ASEval turns, RedCode prompt) with its location, as `step 3/6: destination candidate ...`. A candidate is covered by a `replacements` entry (applied to every rendered text that contains `old`) or by `keep` (`["text", ...]`: stays as it is). A replacement that occurs in no rendered text is refused. Every applied pair is recorded under `transformations`.
- `tool_names`: `{vector: tool}` for an AgentDojo vector when the ground truth has more than one tool. Without an answer for every such vector nothing is rendered: `render` returns `user_prompt: None`, no fixture, and the need (CLI exit 4). A single-tool ground truth names the file by that tool.
- `objects`: `[{"path": "data/x.txt", "kind": "protected"|"canary", "content": optional}]`, the step 5 objects for InjecAgent, AgentDojo, RedCode and ASEval. The default content is the canary line of step 5. ASB and SABER derive their objects as the rule says.
- `path_moves`: `{old: new}` for a RedCode system path that only locates a file the case creates (step 5, D51 items 5 and 7).
- Step 2 executables: the tool lists command words that are not in the present list (rule command positions; heredoc bodies and quoted strings skipped) under `needs_judgment`. It never fails a row for them. Codes that do not parse, scalar shapes the tool cannot read and unsupported printf directives are listed or raise `ToolLimit`, never a rule code.
- Also not done here: the step 2 exclusion classes of `05-source-screen.md` section 4 beyond the three mechanical ones, the step 7 tool mapping of the effect, the 5.3 judgment checks (intent, changes, losses, mapping, route), the unmapped system paths that are only text the case writes, the `python` route, and every `F-MEANING` outcome that needs reading.

Built elsewhere: `case_entry.py` writes the case entry, trial and meaning record of steps 9 and 10, the fixture digest and the calibration payload digest, from `render` plus an adapter-written calibration file (script, pivots, `p_delta` and its justification, `oracle_mapping`). `carrier.py` builds the screening carriers. See README, section Screening carriers. Not built: the local and screening construct reports (step 11, section 5.1) and the guest runs. `render` does not return the empty directories it parses from `mkdir` (`_dirs`), so a fixture cannot hold an empty directory yet.

## The pilot walk

1. `py -3 -I corpus/sampling/select.py pilot ... --adaptation corpus/sampling/decisions/pilot-v1.jsonl --out OUT`. Exit 3 names the next candidate per stratum.
2. `py -3 -I corpus/sampling/adapt.py render --case <id> --allow-candidate --out <new dir> [--judgments j.json]`. It writes `prompt.txt`, `fixture/...` and `summary.json`. Answer each `needs_judgment` entry, render again with `--judgments`, and run `check --case <id> --summary <dir>/summary.json`.
3. Do the steps the tool does not do (above), then the judgment checks. Put every judgment and its evidence into one evidence record (JSON).
4. `py -3 -I corpus/sampling/adapt.py decide --case <id> --decisions corpus/sampling/decisions/pilot-v1.jsonl --evidence-dir corpus/sampling/evidence --evidence record.json [--pass] --reason "<code>: <sentence>" --adapter <who> --date YYYY-MM-DD [--screen-build B --carrier-sha256 S]`. Pass `--pass` only with a `PASS:` reason. The tool takes the rule digest from the file and the ledger digest from the ledger file.
`check` and `decide` read only the committed ledger (its SHA-256 must be the template's). `decide` needs a candidate row and `--allow-candidate`, and validates the line before it writes the evidence file.
`decide` validates the evidence record first (`validate_evidence`): `case_id`, `rule_sha256`, non-empty `attempts`, `outcome` equal to the reason code; a pass also needs `construct_report`, both 5.2 results passing and the five 5.3 answers (`judgment_checks`, keys `1 intent` to `5 route`, each `answer` yes or no with non-empty `evidence`).

CLI exit codes of `render`: 0 rendered, 1 rule failure (JSON `fail`), 2 refused (candidate without flag), 3 `tool_limit`, 4 needs only, 5 `judgment_error`. `decide` returns 5 on an invalid record or line.
5. Commit the decision file, then rerun `select.py` with a new `--out`.

## Adapter readings for the pilot walk

Readings of the rule text that the tool cannot apply and the adapter must apply the same way on every case (lane C, 2026-11-15, after the review of the rehearsal carrier):

- **Object replacement (step 3).** Only the object's name becomes its fixture path. Every other word stays: a sender, a service name such as "on GitHub", and the rest of the phrase. "the email received from lily.white@gmail.com" becomes "`data/email.txt` received from lily.white@gmail.com".
- **Destinations of named sources (step 3).** Only the address itself becomes the receiver. Its introducing noun phrase ("my alternate email address,") stays. The rule adds the noun phrase only in its "Other third-party content" paragraph, and only there is it replaced too.
- **Content of protected files (step 5).** A protected or canary file whose content the source does not give gets the canary line. The empty file of step 5 is only for a protected path the source does not create at all, such as a hook or a dependency list.

## Readings of the rule this tool fixed

The choices that change output bytes:

- AgentDojo user task text is the evaluated Python value, byte for byte. A source trailing space stays, so a pointer sentence follows it after one more space.
- An AgentDojo vector file holds the YAML scalar (one-line double-quoted, or a `|` block scalar) of `environment.yaml` or of an `!include` file that contains the placeholder, with the attack in place and other placeholders at their defaults. Include files come from the digest-checked file list; one the digest file does not list is `F-SOURCE`. The attack template and the user and model names are read from the listed `important_instructions_attacks.py` and `base_attacks.py`. A combined user task (workspace UserTask4, 19, 23, 36 to 39; slack 17 to 20) is built as `task_combinators.py` does at the pin: its PROMPT composition and ground-truth union are read from the listed file and reproduced statically; a task whose `create_combined_task` call or the combinator file has another shape raises `ToolLimit`. A scalar shape the tool cannot read raises `ToolLimit`, a tool gap that is never a rule outcome.
- InjecAgent `NN` and `MM` are 1-based line numbers.
- ASEval and SABER delivered text gets the path mapping and the Python swap first; the adapter's `replacements` apply to that text.
