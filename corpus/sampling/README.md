# D48 confirmatory selection (`corpus/sampling`)

`select.py` runs the registered draw for the D48 confirmatory adversarial set: the main sample,
the scenario top-up, the cold and variance partitions, and the zero-event certification.
Specification: `thesis/tbound/h1-path/16-sampling-design.md` sections 3 to 6, under decision D48
in `thesis/tbound/AH-technical/10-decision-register.md`. The review it answers is
`thesis/tbound/h1-path/17-review-d48.md`. The script is fixed by digest before the commitment
time `T`; changing it afterwards needs a prospective amendment.

Python standard library only, Python 3.12 compatible. Run from the repository root:

    py -3 -I corpus/sampling/select.py <command> ...     (Windows)
    python3 -I corpus/sampling/select.py <command> ...   (Linux)

## Commands

| Command | Purpose | Exit codes |
|---|---|---|
| `draw --ledger L --commitment C --beacon B --adaptation A --out OUT` | Verify the commitment and beacon, run the draw, write the audit JSON | 0 complete, 3 needs an adaptation decision, 1 blocked or refused |
| `allocate --ledger L --commitment C` | Cap and allocation only | 0 ok, 1 refused or blocked |
| `verify-beacon --commitment C --beacon B` | Check round and `randomness` against the commitment | 0 ok, 1 refused |
| `certify --NA N --NB N --nA N --nB N` | Zero-event certification (section 6); a range is `LOW:HIGH` | 0 certified, 1 not certified |

- `draw` never overwrites `OUT`. After a `needs-adaptation` run, add decisions and rerun with a new `--out`.
- A blocked draw (infeasible cap, exhausted main pool, too little non-cold capacity) still writes its audit file, with the reason.
- For `certify`, take the `population` bounds from the draw output: `low` is the passes observed and `high` is the capped candidates not yet failed.

## Inputs

- **Ledger** (JSONL): one candidate per line with `case_id`, `stratum` (`TM-A` or `TM-B`), `attack_id`
  and `mechanism_id`. Ids match `[A-Za-z0-9._:-]+`. A line with `"excluded": true` is skipped and listed.
  Duplicate case ids, other strata, and an attack id in both strata are refused.
- **Commitment** (JSON): `ledger_sha256`, `script_sha256`, `chain` (`hash`, `public_key`, `scheme`,
  `genesis_time`, `period`) and `T` (UTC, whole seconds, `Z` suffix). The ledger bytes and this
  script's bytes must match the digests.
- **Beacon** (drand JSON): `round`, `randomness`, `signature`, and optionally `previous_signature`.
- **Adaptation decisions** (JSONL): `case_id`, `pass` (true or false) and `reason` (nonempty).

## Output

One JSON object with sorted keys and no timestamps, so identical inputs give identical bytes. It holds
every input digest, `R` with its scheduled time, the seed, the cap (`k`, `M_h`, allocation remainders,
tie resolution), every examined candidate with its purpose, group, key and decision, the population
bounds, and the final memberships: main, top-up, cold, variance and untested attacks.

## Tests

    py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v

The suite includes a fixed test vector whose output digest is pinned. That digest covers `select.py`
itself (`script_sha256`), so any edit to the script needs a deliberate re-pin.

`select.py` has the name the spec requires. It shadows the standard `select` module while this
directory is first on `sys.path`, which unittest discovery does. The tests bind the stdlib module
first and then load the script by path. Do not import this file as `select`.

## BLS verification is not done here

`select.py` checks that the beacon round equals `R` and that `randomness` equals SHA-256 of
`signature`. It does not verify the BLS signature, because the standard library has no BLS.
Verify the signature with an independent drand verifier against the committed chain public key,
and record that result with the draw. `verify-beacon` and `draw` both print this reminder.

## Limits

- `--total` exists for tests only. The registered main sample is 299 and the variance subset is 50;
  the variance size is not a CLI option.
- `certify` uses `n = nA + nB`, which is 299 in the design.
- The script's own digest must equal the commitment's `script_sha256`, or the draw is refused.
