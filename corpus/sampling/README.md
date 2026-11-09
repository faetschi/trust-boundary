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

- `--published-utc` is the commitment file version's first publication time from OSF: whole-second UTC ending in `Z`, for example `2026-11-03T12:00:00Z`. If OSF shows fractions of a second, round up to the next whole second. The commitment file cannot hold it (a file with a `T` field is refused). It is recorded as `round.published_utc`.
- `--bls-verified-by "TOOL VERSION"` names the independent drand verifier that checked the BLS signature (a nonempty string). The script does not verify BLS; it records the name under `inputs.beacon.bls_verified_by`. The draw counts only after that verification is archived.
- `draw --pilot-output` is the JSON that `pilot` wrote. The draw is refused unless it is a complete pilot (`schema` `tbound-d48-pilot/v1`, `memberships.pilot` present) and every pilot member is in `development_case_ids`. Its digest and members are recorded under `inputs.pilot_output`.
- `pilot` and `draw` never overwrite `OUT`. After a `needs-adaptation` run, add decisions and rerun with a new `--out`.
- `allocate` and `certify` write JSON to stdout with `
` line endings on every platform, so a redirected file has the same bytes everywhere.
- A blocked `pilot` or `draw` (infeasible cap, exhausted stratum, too little non-cold capacity) still writes its audit file, with the reason.
- For `certify`, take the `population` bounds from the draw output: `low` is the passes observed and `high` is the capped candidates not yet failed.

## The two commitments

1. Freeze the ledger and the script, and record the SHA-256 of each (`Get-FileHash` output is accepted).
2. Write the **pilot commitment** (`"kind": "pilot"`) and publish it as one OSF file version. Its time `T1` is that version's first publication time on OSF, which you pass as `--published-utc`.
3. Round `R` is the first drand round scheduled at least 24 hours after `T1`. Once it exists, save its beacon JSON, run `verify-beacon --published-utc T1`, and check the BLS signature with an independent drand verifier. Note its name and version for `--bls-verified-by`.
4. Run `pilot` with `--published-utc T1`. On exit 3, add the adaptation decisions for the named cases and rerun with a new `--out`. Exit 1 means the pilot is blocked. No new seed or larger pilot is allowed.
5. Policy tuning and adaptation-rule fixing use the pilot and development material only. Development cases are every case used in the pilot or in any development or rehearsal run.
6. Write the **main commitment** (`"kind": "main"`) with the same ledger and script digests, `policy_sha256`, `adaptation_rule_sha256`, and `development_case_ids` listing every development case, the pilot's selected cases included. Publish it once; its OSF first-publication time is `T2`.
7. For `T2`, take the round `R` defined in step 3 (first round scheduled at least 24 hours after `T2`), save its beacon, verify it as in step 3, and run `draw` with `--published-utc T2` and `--pilot-output` set to the pilot's output file. Run `allocate` first if you want the cap preview.

`pilot` refuses a main commitment and `draw` refuses a pilot commitment. `allocate` needs a main commitment.

## Inputs

- **Ledger** (JSONL): one candidate per line with `case_id`, `stratum` (`TM-A` or `TM-B`), `attack_id` and `mechanism_id`. Ids match `[A-Za-z0-9._:-]+`. A line with `"excluded": true` is skipped and listed. Duplicate case ids and other strata are refused. The group for the cap and the top-up is `"<stratum>:<attack_id>"`, because one scenario may appear in both strata (D47).
- **Commitment** (JSON). Both kinds have `kind`, `ledger_sha256`, `script_sha256`, and `chain` (`hash`, `public_key`, `scheme`, `genesis_time`, `period`). There is no `T` field. `chain.hash` must be `8990e7a9aaed2ffed73dbd7092123d6f289930540d7651336225dc172e51b2ce`, the League of Entropy mainnet default chain that D48 names; any other hash is refused. Genesis time and period are not pinned here. The ledger bytes and this script's bytes must match the digests.
  - `pilot` also has `pilot_size`, a positive integer. It must equal the size the run uses, which is 11 from the command line.
  - `main` also has `policy_sha256` and `adaptation_rule_sha256` (64-character SHA-256 hex digests) and `development_case_ids` (a list of case ids without repeats; it may be empty; ids need not be ledger rows).
- **Beacon** (drand JSON): `round`, `randomness`, `signature`, and optionally `previous_signature`.
- **Adaptation decisions** (JSONL): `case_id`, `pass` (true or false) and `reason` (nonempty). A decision for a development case that is a candidate row is accepted and listed under `not_consulted`. A decision for an excluded row is refused, and this includes the development rows the frozen ledger marks excluded.

## Frozen ledger v1

`d48-ledger-v1.jsonl` is the candidate ledger both draws use (16 section 3 item 2). It has 1,797 rows:
1,358 candidates (TM-A 1,020, TM-B 338), 149 development rows and 290 exclusions, each exclusion with a reason.
Every row keeps its provenance: source, pin, base item, the rule that resolved it (`resolution_20`) and
its values before resolution. Development rows (the T5 rehearsal items, D48) are `excluded: true`, so
`pilot` cannot draw them. List them in the main commitment's `development_case_ids`, so their scenarios
are still marked as seen. Extra fields beyond `case_id`, `stratum`, `attack_id`, `mechanism_id` and
`excluded` are not read by the script; the ledger digest covers them.

`pilot-commitment-v1.template.json` is the pilot commitment for `T1`, with the ledger and script digests,
the chain, the round rule and the per-source unit choices. Before publication on OSF, fill
`signed_tag.object` and `signed_tag.commit` from the owner-signed tag `corpus-d48-ledger-v1`. The script
ignores those fields.

## Ledger v2 (draft, under review)

`d48-ledger-v2.jsonl` replaces v1 for the pilot commitment once it is reviewed and the owner signs its tag. v1 stays as history. v2 has 3,089 rows: 1,536 candidates (TM-A 1,020, TM-B 516), 149 development rows and 1,404 exclusions. Against v1 it adds:

- the ASB re-screen: the 152 aggressive attacker tools v1 did not hold, 29 of them candidates;
- the RedCode-Exec re-screen: the 38 files v1 did not hold (18 Python, 20 Bash, 1,140 rows), 149 of them candidates. Bash rows run as written;
- the ASEval pin `f24d57ce…` on the 165 ASEval rows, where v1 had "unresolved".

Every candidate predicate splits under adaptation rule v1 step 8, and every part is registered for its stratum.

`d48-source-digests-v2.json` is the source digest file of adaptation rule v1, step 1. For every candidate it gives the pin key, the revision, the source files and `benchmark_digest`. For every pin key it gives the repository, the licence and the licence files with their digests. It also records the SHA-256 of the ledger it was built from. File digests are taken from the git blobs at each pin, not from working trees.

Both files are built outside the repository by the lane C screening scripts (`20-build.py`, `21-build.py`, `21-source-digests.py`, `22-asb-screen.py`, `22-redcode-screen.py`), against the pinned clones.

## Development cases

`draw` and `allocate` remove every `development_case_ids` entry from the candidates before the cap and the allocation. An id that is not a ledger row (a T5 rehearsal item, say) is not refused. Its grammar is checked, it stays in `development.case_ids`, and it is also listed in `development_not_in_ledger`, so a typo shows up there. Only ledger rows are removed; the removal is counted per stratum in `development`. An attack whose cases are all development cases has no candidate left, so it is reported as untested.

## Output

One JSON object with sorted keys and no timestamps, so identical inputs give identical bytes. Both outputs hold every input digest, the commitment as parsed, `R` with its scheduled time, the seed, and every examined candidate with its purpose, group, key and decision.

- `pilot`: the allocation (candidates and places per stratum, with remainders and the tie), the walk per stratum, and the pilot memberships.
- `draw`: the cap (`k`, `M_h`, allocation remainders, tie resolution), `development` (listed ids and removals per stratum), `attacks` (each candidate attack with its stratum and `seen_in_development`, set by scenario: a scenario with a development case in either stratum counts as seen in both of its groups), `mechanisms` (each candidate mechanism with `seen_in_development`), `development_not_in_ledger`, the population bounds, and the memberships: main, top-up, cold, variance and untested attacks.

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
- `--published-utc` is taken as given. The script cannot see OSF, so check it against the OSF version's first publication time (rounded up to the next whole second).
- `--bls-verified-by` is an attestation, not a check.
