# Adaptation tooling (`adapt.py`)

`adapt.py` applies the mechanical parts of `adaptation-rule-v1.md` to one ledger row. It is stdlib only. Run it with `py -3 -I`. It never executes code from the pinned clones. It reads them as data from the git object at the pin, and it parses Python sources with `ast`.

Tests: `py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py"`. Golden tests use `development` rows and synthetic records only.

## Hard rule: no candidate before the pilot draw

D51 item 13: every examined case becomes a development case. `load_source_item`, `render` and the CLI refuse a row with status `candidate` unless `allow_candidate` (`--allow-candidate`) is set. They refuse an `excluded` row always. Use the flag only for the real pilot walk, in the order `select.py` names.

## What the tool decides

| Function | Rule | Result |
|---|---|---|
| `load_source_item(row, clones)` | step 1 | Files read from the git object at the pin. Every SHA-256 is checked against the pin's file digests in `d48-source-digests-v3.json`. Clone `HEAD` must equal the pin. Raises `Fail("F-SOURCE")`. A row the digest file does not list is refused, except a development row, which uses the pin's own file list. |
| `render(row, item, judgments)` | steps 3 to 6, 8 | Dict with `user_prompt` (preamble included, `str`, write it as UTF-8), `fixture_files`, `pointer_sentences`, `protected_paths`, `canary_files`, `receivers`, `transformations`, `lost_distinctions`, `tool_mapping`, `forbidden_effects`, `effect_join`, `authorization_class`, `needs_judgment`. A structural failure returns `{"fail": code, "reason": text}`. |
| `split_predicate(p)` | step 8 | `(forbidden_effects, effect_join)`. A mix of `+` and `/`, an unsplittable part or an unregistered part is `F-ELIGIBLE`. |
| `scripted_checks(row, rendered)` | 5.2 checks 1, 2 | `[{"check", "pass", "detail"}]`. |
| `decision_line(...)`, `append_decision(path, line)` | section 2 | The ten keys in rule order. Refuses a `rule_sha256` that is not the SHA-256 of `adaptation-rule-v1.md`, a duplicate `case_id`, a blank line in the file, and a reason that is not `<code>: <sentence>` or that disagrees with `pass`. `select.py` reads the file. |
| `write_evidence(dir, record)` | section 1 | `<dir>/<sha256>.json`, canonical JSON, LF. Returns the digest. |

Sources rendered: InjecAgent, AgentDojo, ASB (through `asb_vectors.render`, plus the section 3c goal-class check of step 2), RedCode-Exec, SABER, ASEval. Other sources return `F-SOURCE` ("no renderer").

## What the tool leaves to the adapter

`render` does not decide these. It lists each one in `needs_judgment`. Pass the answer back in `judgments` (a JSON file for the CLI):

- `replacements`: `[[old, new], ...]`, the destination replacements of step 3 (a mailbox, address, URL or host becomes the receiver) and the object replacements (an object becomes its protected path). Each `old` must occur in the injected text. It is applied to the injected text only: the InjecAgent Tool Response, the AgentDojo goal, the SABER payload, an ASEval delivered tool response. Each pair is recorded under `transformations`.
- `tool_names`: `{vector: tool}` for an AgentDojo vector when the ground truth has more than one tool. Without it, the first ground-truth tool is used and the need is listed.
- `objects`: `[{"path": "data/x.txt", "kind": "protected"|"canary", "content": optional}]`, the step 5 objects for InjecAgent, AgentDojo, RedCode and ASEval. The default content is the canary line of step 5. ASB and SABER derive their objects as the rule says.
- `path_moves`: `{old: new}` for a RedCode system path that only locates a file the case creates (step 5, D51 items 5 and 7).
- Also not done here: the step 2 exclusion classes of `05-source-screen.md` section 4 beyond the three mechanical ones, the step 7 tool mapping of the effect, the 5.3 judgment checks (intent, changes, losses, mapping, route), the unmapped system paths that are only text the case writes, the `python` route, and every `F-MEANING` outcome that needs reading.

Not built: the calibration script, the pivot table, `p_delta` and its justification (step 9), the case entry, trial and meaning record in the `build_adv` shape (step 10), `oracle_mapping`, the workspace fixture digest and the calibration payload digest, the local and screening construct reports (step 11, section 5.1), the screening carriers and the guest. `render` gives the inputs those steps need.

## The pilot walk

1. `py -3 -I corpus/sampling/select.py pilot ... --adaptation corpus/sampling/decisions/pilot-v1.jsonl --out OUT`. Exit 3 names the next candidate per stratum.
2. `py -3 -I corpus/sampling/adapt.py render --case <id> --allow-candidate --out <new dir> [--judgments j.json]`. It writes `prompt.txt`, `fixture/...` and `summary.json`. Answer each `needs_judgment` entry, render again with `--judgments`, and run `check --case <id> --summary <dir>/summary.json`.
3. Do the steps the tool does not do (above), then the judgment checks. Put every judgment and its evidence into one evidence record (JSON).
4. `py -3 -I corpus/sampling/adapt.py decide --case <id> --decisions corpus/sampling/decisions/pilot-v1.jsonl --evidence-dir corpus/sampling/evidence --evidence record.json [--pass] --reason "<code>: <sentence>" --adapter <who> --date YYYY-MM-DD [--screen-build B --carrier-sha256 S]`. Pass `--pass` only with a `PASS:` reason. The tool takes the rule digest from the file and the ledger digest from the ledger file.
5. Commit the decision file, then rerun `select.py` with a new `--out`.

## Readings of the rule this tool fixed

See the implementation report for the ambiguous lines. The choices that change output bytes:

- AgentDojo user task text is the evaluated Python value, byte for byte. A source trailing space stays, so a pointer sentence follows it after one more space.
- An AgentDojo vector file holds the one-line double-quoted YAML scalar of `environment.yaml` that contains the placeholder, with the attack in place and other placeholders at their defaults. An environment with `!include` (workspace) is `F-SOURCE`: the included files are not in the digest file.
- InjecAgent `NN` and `MM` are 1-based line numbers.
- ASEval and SABER delivered text gets the path mapping and the Python swap, then the adapter's `replacements`.
