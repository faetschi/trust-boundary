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
| `pilot --ledger L --commitment P --beacon B --adaptation A --out OUT` | Verify the pilot commitment and beacon, run the pilot, write the audit JSON | 0 complete, 3 needs an adaptation decision, 1 blocked or refused |
| `draw --ledger L --commitment C --beacon B --adaptation A --out OUT` | Verify the main commitment and beacon, run the draw, write the audit JSON | 0 complete, 3 needs an adaptation decision, 1 blocked or refused |
| `allocate --ledger L --commitment C` | Cap and allocation only, after development removal | 0 ok, 1 refused or blocked |
| `verify-beacon --commitment C --beacon B` | Check round and `randomness` against a pilot or a main commitment | 0 ok, 1 refused |
| `certify --NA N --NB N --nA N --nB N` | Zero-event certification (section 6); a range is `LOW:HIGH` | 0 certified, 1 not certified |

- `pilot` and `draw` never overwrite `OUT`. After a `needs-adaptation` run, add decisions and rerun with a new `--out`.
- A blocked `pilot` or `draw` (infeasible cap, exhausted stratum, too little non-cold capacity) still writes its audit file, with the reason.
- For `certify`, take the `population` bounds from the draw output: `low` is the passes observed and `high` is the capped candidates not yet failed.

## The two commitments

1. Freeze the ledger and the script, and record the SHA-256 of each (`Get-FileHash` output is accepted).
2. Write the **pilot commitment** (`"kind": "pilot"`) and publish it as one OSF file version. Its `T` is that version's first publication time in UTC.
3. Round `R` is the first drand round scheduled at least 24 hours after `T`. Once it exists, save its beacon JSON, run `verify-beacon`, and check the BLS signature with an independent drand verifier.
4. Run `pilot`. On exit 3, add the adaptation decisions for the named cases and rerun with a new `--out`. Exit 1 means the pilot is blocked. No new seed or larger pilot is allowed.
5. Policy tuning and adaptation-rule fixing use the pilot and development material only. Development cases are every case used in the pilot or in any development or rehearsal run.
6. Write the **main commitment** (`"kind": "main"`) with the same ledger and script digests, `policy_sha256`, `adaptation_rule_sha256`, and `development_case_ids` listing every development case, the pilot's selected cases included. Publish it once, at `T2`.
7. For `T2`, take the round `R` defined in step 3 (first round scheduled at least 24 hours after `T2`), save its beacon, verify it as in step 3, and run `draw`. Run `allocate` first if you want the cap preview.

`pilot` refuses a main commitment and `draw` refuses a pilot commitment. `allocate` needs a main commitment.

## Inputs

- **Ledger** (JSONL): one candidate per line with `case_id`, `stratum` (`TM-A` or `TM-B`), `attack_id` and `mechanism_id`. Ids match `[A-Za-z0-9._:-]+`. A line with `"excluded": true` is skipped and listed. Duplicate case ids, other strata, and an attack id in both strata are refused.
- **Commitment** (JSON). Both kinds have `kind`, `ledger_sha256`, `script_sha256`, `chain` (`hash`, `public_key`, `scheme`, `genesis_time`, `period`) and `T` (UTC, whole seconds, `Z` suffix). The ledger bytes and this script's bytes must match the digests.
  - `pilot` also has `pilot_size`, a positive integer. It must equal the size the run uses, which is 11 from the command line.
  - `main` also has `policy_sha256` and `adaptation_rule_sha256` (64-character SHA-256 hex digests) and `development_case_ids` (a list of ledger case ids without repeats; it may be empty).
- **Beacon** (drand JSON): `round`, `randomness`, `signature`, and optionally `previous_signature`.
- **Adaptation decisions** (JSONL): `case_id`, `pass` (true or false) and `reason` (nonempty). A decision for a development case is accepted and listed under `not_consulted`.

## Development cases

`draw` and `allocate` remove every `development_case_ids` entry from the candidates before the cap and the allocation. An id that is not in the ledger at all (excluded rows included) is refused, so a typo cannot silently leave a case in the population. The removal is counted per stratum in `development`. An attack whose cases are all development cases has no candidate left, so it is reported as untested.

## Output

One JSON object with sorted keys and no timestamps, so identical inputs give identical bytes. Both outputs hold every input digest, the commitment as parsed, `R` with its scheduled time, the seed, and every examined candidate with its purpose, group, key and decision.

- `pilot`: the allocation (candidates and places per stratum, with remainders and the tie), the walk per stratum, and the pilot memberships.
- `draw`: the cap (`k`, `M_h`, allocation remainders, tie resolution), `development` (listed ids and removals per stratum), `attacks` (each candidate attack with its stratum and `seen_in_development`), the population bounds, and the memberships: main, top-up, cold, variance and untested attacks.

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
and record that result with the draw. `verify-beacon`, `pilot` and `draw` all print this reminder.

## Limits

- `--total` exists for tests only. The registered main sample is 299 and the variance subset is 50; the variance size is not a CLI option. The pilot size is 11 with no CLI option; the API accepts an override for tests.
- `certify` uses `n = nA + nB`, which is 299 in the design.
- The script's own digest must equal the commitment's `script_sha256`, or the run is refused.
- The script does not check that `development_case_ids` lists every development case. That is the owner's attestation, so list the pilot's selected cases too.
- `policy_sha256` and `adaptation_rule_sha256` are recorded and echoed, but the script has no policy or rule file to check them against.
- Section 3 item 5 also binds the signed tag object and its commit, the per-source unit choices and the rule that derives `R`. The script does not read those fields. They sit in the published commitment file, whose digest the output records.
- `T` is taken as written. Check it against the OSF version's first publication time.
