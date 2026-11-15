# D48 confirmatory selection (`corpus/sampling`)

`select.py` runs the D48 selection in two draws. The **pilot** (`pilot`) is a random development
split of 11 adversarial places. The **main draw** (`draw`) is the confirmatory set: the main sample,
the scenario top-up, the cold and variance partitions, and the zero-event certification.
Specification: `thesis/tbound/h1-path/16-sampling-design.md` sections 3 to 6, under decision D48
in `thesis/tbound/AH-technical/10-decision-register.md`. The review it answers is
`thesis/tbound/h1-path/17-review-d48.md`. The script is fixed by digest before the pilot commitment
time `T1`; changing it afterwards needs a prospective amendment.

Python standard library only, Python 3.12 compatible. Run from the repository root:

    py -3 -I corpus/sampling/select.py <command> ...     (Windows)
    python3 -I corpus/sampling/select.py <command> ...   (Linux)

## Commands

| Command | Purpose | Exit codes |
|---|---|---|
| `pilot --ledger L --commitment P --published-utc T --beacon B --bls-verified-by V --adaptation A --out OUT` | Verify the pilot commitment and beacon, run the pilot, write the audit JSON | 0 complete, 3 needs an adaptation decision, 1 blocked or refused |
| `draw --ledger L --commitment C --published-utc T --pilot-output PILOT --beacon B --bls-verified-by V --adaptation A --out OUT` | Verify the main commitment and beacon, run the draw, write the audit JSON | 0 complete, 3 needs an adaptation decision, 1 blocked or refused |
| `allocate --ledger L --commitment C` | Cap and allocation only, after development removal | 0 ok, 1 refused or blocked |
| `verify-beacon --commitment C --published-utc T --beacon B` | Check round and `randomness` against a pilot or a main commitment | 0 ok, 1 refused |
| `certify --NA N --NB N --nA N --nB N` | Zero-event certification (section 6); a range is `LOW:HIGH` | 0 certified, 1 not certified |

- `--published-utc` is the commitment record's `created` timestamp from the Zenodo API (D53): whole-second UTC ending in `Z`, for example `2026-11-03T12:00:00Z`. `created` has fractions of a second: round up to the next whole second. The script's own help text still says OSF; read it as the commitment venue (D53). The commitment file cannot hold it (a file with a `T` field is refused). It is recorded as `round.published_utc`.
- `--bls-verified-by "TOOL VERSION"` names the independent drand verifier that checked the BLS signature (a nonempty string). The script does not verify BLS; it records the name under `inputs.beacon.bls_verified_by`. The draw counts only after that verification is archived.
- `draw --pilot-output` is the JSON that `pilot` wrote. The draw is refused unless it is a complete pilot (`schema` `tbound-d48-pilot/v1`, `memberships.pilot` present) and every pilot member is in `development_case_ids`. Its digest and members are recorded under `inputs.pilot_output`.
- `pilot` and `draw` never overwrite `OUT`. After a `needs-adaptation` run, add decisions and rerun with a new `--out`.
- `allocate` and `certify` write JSON to stdout with LF line endings on every platform, so a redirected file has the same bytes everywhere.
- A blocked `pilot` or `draw` (infeasible cap, exhausted stratum, too little non-cold capacity) still writes its audit file, with the reason.
- For `certify`, take the `population` bounds from the draw output: `low` is the passes observed and `high` is the capped candidates not yet failed.

## The two commitments

1. Freeze the ledger and the script, and record the SHA-256 of each (`Get-FileHash` output is accepted).
2. Write the **pilot commitment** (`"kind": "pilot"`) and publish it as one public Zenodo record (D53). Its time `T1` is that record's `created` timestamp, which you pass as `--published-utc`.
3. Round `R` is the first drand round scheduled at least 24 hours after `T1`. Once it exists, save its beacon JSON, run `verify-beacon --published-utc T1`, and check the BLS signature with an independent drand verifier. Note its name and version for `--bls-verified-by`.
4. Run `pilot` with `--published-utc T1`. On exit 3, add the adaptation decisions for the named cases and rerun with a new `--out`. Exit 1 means the pilot is blocked. No new seed or larger pilot is allowed.
5. Policy tuning and adaptation-rule fixing use the pilot and development material only. Development cases are every case used in the pilot or in any development or rehearsal run.
6. Write the **main commitment** (`"kind": "main"`) with the same ledger and script digests, `policy_sha256`, `adaptation_rule_sha256`, and `development_case_ids` listing every development case, the pilot's selected cases included. Publish it once; its Zenodo `created` time is `T2`.
7. For `T2`, take the round `R` defined in step 3 (first round scheduled at least 24 hours after `T2`), save its beacon, verify it as in step 3, and run `draw` with `--published-utc T2` and `--pilot-output` set to the pilot's output file. Run `allocate` first if you want the cap preview.

`pilot` refuses a main commitment and `draw` refuses a pilot commitment. `allocate` needs a main commitment.

## Inputs

- **Ledger** (JSONL): one candidate per line with `case_id`, `stratum` (`TM-A` or `TM-B`), `attack_id` and `mechanism_id`. Ids match `[A-Za-z0-9._:-]+`. A line with `"excluded": true` is skipped and listed. Duplicate case ids and other strata are refused. The group for the cap and the top-up is `"<stratum>:<attack_id>"`, because one scenario may appear in both strata (D47).
- **Commitment** (JSON). Both kinds have `kind`, `ledger_sha256`, `script_sha256`, and `chain` (`hash`, `public_key`, `scheme`, `genesis_time`, `period`). There is no `T` field. `chain.hash` must be `8990e7a9aaed2ffed73dbd7092123d6f289930540d7651336225dc172e51b2ce`, the League of Entropy mainnet default chain that D48 names; any other hash is refused. Genesis time and period are not pinned here. The ledger bytes and this script's bytes must match the digests.
  - `pilot` also has `pilot_size`, a positive integer. It must equal the size the run uses, which is 11 from the command line.
  - `main` also has `policy_sha256` and `adaptation_rule_sha256` (64-character SHA-256 hex digests) and `development_case_ids` (a list of case ids without repeats; it may be empty; ids need not be ledger rows).
- **Beacon** (drand JSON): `round`, `randomness`, `signature`, and optionally `previous_signature`.
- **Adaptation decisions** (JSONL): `case_id`, `pass` (true or false) and `reason` (nonempty). A decision for a development case that is a candidate row is accepted and listed under `not_consulted`. A decision for an excluded row is refused, and this includes the development rows the frozen ledger marks excluded.

## Ledger v1 (history)

`d48-ledger-v1.jsonl` is the first candidate ledger. D51 item 8 keeps it as history: the commitments bind ledger v3 (below). It has 1,797 rows:
1,358 candidates (TM-A 1,020, TM-B 338), 149 development rows and 290 exclusions, each exclusion with a reason.
Every row keeps its provenance: source, pin, base item, the rule that resolved it (`resolution_20`) and
its values before resolution. Development rows (the T5 rehearsal items, D48) are `excluded: true`, so
`pilot` cannot draw them. List them in the main commitment's `development_case_ids`, so their scenarios
are still marked as seen. Extra fields beyond `case_id`, `stratum`, `attack_id`, `mechanism_id` and
`excluded` are not read by the script; the ledger digest covers them.

`pilot-commitment-v1.template.json` is the pilot commitment for `T1`, with the ledger v3, source-digest file and script digests,
the chain, the round rule and the per-source unit choices. The rule digest is filled (the rule is frozen). Before publication on Zenodo, fill `signed_tag.object` and `signed_tag.commit` from the owner-signed tag `corpus-d48-ledger-v3`. The script
ignores those fields. `test_pilot_template.py` keeps every recorded digest equal to its file.

## Ledger v3 (the candidate ledger both draws use)

`d48-ledger-v3.jsonl` is the candidate ledger both draws use (16 section 3 item 2). D51 item 26 binds it in the pilot commitment, and the owner signs the tag `corpus-d48-ledger-v3` on the merge commit that holds the final template. It is ledger v2 with the 211 candidates that fail the adaptation rule by construction marked excluded, one reason per row: 60 RedCode-Exec Bash rows that need `wget` or `curl`, 91 ASEval records (modules with no Node.js equivalent, absolute paths, setup commands), 50 SABER tasks (modules, setup commands, MCP tools, absent executables), 2 CodeIPI, 5 OS-Harm-injection, 2 OS-Harm-misuse and 1 SHADE-Arena. Every other row is byte-identical to v2. v3 has 3,089 rows: 1,265 candidates (TM-A 979, TM-B 286), 149 development rows and 1,675 exclusions. Only failures that are certain under the rule are excluded; a row that might pass stays a candidate and is decided by the adapter. `d48-source-digests-v3.json` is its source digest file; it also lists the files v2's missed (OS-Harm, MSB, RedTeamCUA, the three `include/` files that AgentDojo's workspace `environment.yaml` loads, AgentDojo's `task_combinators.py`, which builds the combined user tasks of the workspace and slack suites, and its attack files `important_instructions_attacks.py` and `base_attacks.py`, which hold the template and names of rule step 3). Built by `23-structural-screen.py` and `23-source-digests.py`.

## Ledger v2 (history)

`d48-ledger-v2.jsonl` was the candidate ledger until ledger v3 replaced it before `T1`. v2 has 3,089 rows: 1,476 candidates (TM-A 1,020, TM-B 456), 149 development rows and 1,464 exclusions. Against v1 it adds:

- the ASB re-screen: the 152 aggressive attacker tools v1 did not hold, 29 of them candidates;
- the RedCode-Exec re-screen: the 38 files v1 did not hold (18 Python, 20 Bash, 1,140 rows), 119 of them candidates. Bash rows run as written;
- the exclusion of the 60 RedCode UDP rows (`E-UDP`, Python and Bash `index21`): the registered sink is one TCP listener, so `EFF-NET-01` cannot be observed (adaptation-rule review 3, M5);
- the ASEval pin `f24d57ce…` on the 165 ASEval rows, where v1 had "unresolved".

Every candidate predicate splits under adaptation rule v1 step 8, and every part is registered for its stratum.

`d48-source-digests-v2.json` was the source digest file of ledger v2. The current one, `d48-source-digests-v3.json`, has the same form. For every candidate it gives the pin key, the revision, the source files and `benchmark_digest`. For every pin key it gives the repository, the licence and the licence files with their digests. It also records the SHA-256 of the ledger it was built from. File digests are taken from the git blobs at each pin, not from working trees.

Both files are built outside the repository by the lane C screening scripts (`20-build.py`, `21-build.py`, `21-source-digests.py`, `22-asb-screen.py`, `22-redcode-screen.py`), against the pinned clones. `22-asb-screen.py` decides every ASB class with `asb_goal_class.py` (D51 item 21, adaptation rule section 3c), which names no case.

`adaptation-rule-v1.md` is the adaptation rule (D48, D51). Its SHA-256 is the `adaptation_rule_sha256` of the pilot commitment. `agentdojo_vectors.py`, `agentdojo-vectors-v1.json`, `asb_vectors.py` and `asb-template-vectors-v1.jsonl` are its pre-`T1` test vectors (rule step 3 and section 3a).

## Development cases

`draw` and `allocate` remove every `development_case_ids` entry from the candidates before the cap and the allocation. An id that is not a ledger row (a T5 rehearsal item, say) is not refused. Its grammar is checked, it stays in `development.case_ids`, and it is also listed in `development_not_in_ledger`, so a typo shows up there. Only ledger rows are removed; the removal is counted per stratum in `development`. An attack whose cases are all development cases has no candidate left, so it is reported as untested.

`t2-development-additions.json` lists ledger rows that join `development_case_ids` at T2 besides the pilot's examined cases, each with its date and the owner's reason. Nothing reads it automatically: the T2 commitment step copies its ids into `development_case_ids`.

## Output

One JSON object with sorted keys and no timestamps, so identical inputs give identical bytes. Both outputs hold every input digest, the commitment as parsed, `R` with its scheduled time, the seed, and every examined candidate with its purpose, group, key and decision.

- `pilot`: the allocation (candidates and places per stratum, with remainders and the tie), the walk per stratum, and the pilot memberships.
- `draw`: the cap (`k`, `M_h`, allocation remainders, tie resolution), `development` (listed ids and removals per stratum), `attacks` (each candidate attack with its stratum and `seen_in_development`, set by scenario: a scenario with a development case in either stratum counts as seen in both of its groups), `mechanisms` (each candidate mechanism with `seen_in_development`), `development_not_in_ledger`, the population bounds, and the memberships: main, top-up, cold, variance and untested attacks.

## Screening carriers

Rule section 7.1 runs each screening round as one carrier on the guest. Two tools build it. Neither is T1-bound.

- `case_entry.py build --case <id> --calibration calib.json [--judgments j.json] --draw pilot|main|rehearsal --tbound-run PATH --out DIR` turns one ledger row into a case bundle (rule steps 9 and 10). It re-runs `adapt.py` render from the row and the judgments, and it keeps the candidate guard (`--allow-candidate` only for the real pilot walk). The calibration file holds the frozen script, the pivots (or a `build.py` pivot helper), `p_delta` and the adapter's texts. The bundle is `case.json`, the fixture artifact, a `sources.json` fragment, the licences and `notice.json`. A development row takes only `--draw rehearsal`.
- `carrier.py build --round R --bundle DIR ... --out corpus/sampling/carriers/R` puts the bundles into the frozen T5 carrier `corpus/pilot/t5-2026-10-29`. Bundles of a stratum, sorted by `case_id`, replace that stratum's T5 adversarial cases in ascending `case_id` order. The tool keeps `split_seed`, recomputes the partitions as `evalcorpus.Allocate` does (through the `build.py` mirror of its `SelectSeeded` ranks), and writes the union of sources, licences and notices beside the manifest. With no bundles the output equals T5 byte for byte.

A drawn case keeps its ledger `case_id`. Its witness id is `PILOT-WIT-<case_id>`. The oracle is the first part's oracle (rule step 8). A case that mixes NET and WS parts is refused as a tool limit, because the `p_delta` that relaxes both is still open with lane P.

`carriers/rehearsal-dev-01` is a rehearsal carrier built from 8 development rows (7 TM-A, 1 TM-B). It is not a screening round, and its judgments exercise the pipeline; they are not rulings for the pilot walk (see ADAPT.md, section Adapter readings). Lane P uses it to dry-run the kit's carrier input. Rebuild it with `py -3 -I corpus/sampling/rehearsal/build_rehearsal.py --tbound-run PATH --out DIR`; the inputs are in `rehearsal/rehearsal-dev-01/`.

Before each round, lane P runs `tbound-profile corpus-check --quota-profile pilot` on the carrier and signs its run configuration.

## Tests

    py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v

The suite pins two fixed test vectors, one for the pilot and one for the draw. Each digest covers `select.py` itself (`script_sha256`), so any edit to the script needs a deliberate re-pin of both.

`select.py` has the name the spec requires. It shadows the standard `select` module while this
directory is first on `sys.path`, which unittest discovery does. The tests bind the stdlib module
first and then load the script by path. Do not import this file as `select`.

## BLS verification is not done here

`select.py` checks that the beacon round equals `R` and that `randomness` equals SHA-256 of
`signature`. It does not verify the BLS signature, because the standard library has no BLS.
Verify the signature with an independent drand verifier against the committed chain public key,
and pass its name and version as `--bls-verified-by`; `pilot` and `draw` refuse to run without it and
record it. The output still says `bls_signature_verified: false`, because this script did not do it.
`verify-beacon`, `pilot` and `draw` all print this reminder.

## Limits

- `--total` exists for tests only. The registered main sample is 299 and the variance subset is 50; the variance size is not a CLI option. The pilot size is 11 with no CLI option; the API accepts an override for tests.
- `certify` uses `n = nA + nB`, which is 299 in the design.
- The script's own digest must equal the commitment's `script_sha256`, or the run is refused.
- The script checks that `development_case_ids` holds every pilot member, but not the other development or rehearsal cases. That is the owner's attestation.
- `policy_sha256` and `adaptation_rule_sha256` are recorded and echoed, but the script has no policy or rule file to check them against.
- Section 3 item 5 also binds the signed tag object and its commit, the per-source unit choices and the rule that derives `R`. The script does not read those fields. They sit in the published commitment file, whose digest the output records.
- `--published-utc` is taken as given. The script cannot see Zenodo, so check it against the record's `created` timestamp (rounded up to the next whole second).
- `--bls-verified-by` is an attestation, not a check.
