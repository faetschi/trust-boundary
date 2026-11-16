"""Build the T2 main commitment's `development_case_ids` (adaptation rule section 7.3, README "Development cases").

    py -3 -I corpus/sampling/dev_ids.py build --pilot-output <select.py pilot output> --carriers <dir> \\
        --t5 corpus/pilot/t5-2026-10-29/manifest.draft.json --additions corpus/sampling/t2-development-additions.json \\
        --ledger corpus/sampling/d48-ledger-v3.jsonl --out <new file>

The output is a sorted JSON list, the union of

- every case of the pilot output's `examined` list and its `memberships.pilot` (select.py `draw` refuses a
  commitment that misses a member);
- every case of every `<carriers>/*/manifest.draft.json` (or `manifest.json`), ledger ids only: a carrier case
  that is not a ledger row is not a development candidate and is counted apart;
- every case of the T5 manifest. Ids that are not ledger rows (`PILOT-ADV-A-01` and the like) stay in the list,
  as README "Development cases" allows, and are counted apart;
- the rows of the additions file, which must be ledger rows.

It prints counts per source and per stratum, never an id. It refuses an id that does not match select.py's id
grammar, a pilot or additions id that is not a ledger row, no carrier at all, and an output that exists.
Stdlib only.
"""
import argparse
import glob
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
STRATA = ("TM-A", "TM-B")
NOT_IN_LEDGER = "not in ledger"


def load_select():
    spec = importlib.util.spec_from_file_location("select_for_dev_ids", os.path.join(HERE, "select.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def read_json(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def read_ledger(path):
    """case_id -> stratum, for every row, development and excluded rows too."""
    with open(path, encoding="utf-8") as fh:
        rows = [json.loads(line) for line in fh if line.strip()]
    return {row["case_id"]: row["stratum"] for row in rows}


def pilot_ids(path, select):
    with open(path, "rb") as fh:
        data = fh.read()
    try:
        members = select.parse_pilot_output(data)
    except select.Refused as exc:
        sys.exit(f"{path}: {exc}")
    return members | {record["case_id"] for record in json.loads(data)["examined"]}


def manifest_ids(path):
    return {case["case_id"] for case in read_json(path)["cases"]}


def carrier_ids(directory):
    manifests = sorted(glob.glob(os.path.join(directory, "*", "manifest.draft.json"))
                       + glob.glob(os.path.join(directory, "*", "manifest.json")))
    if not manifests:
        sys.exit(f"{directory}: no carrier manifest (<carrier>/manifest.draft.json) found")
    return set().union(*(manifest_ids(path) for path in manifests)), len(manifests)


def additions_ids(path):
    ids = [row["case_id"] for row in read_json(path)["rows"]]
    if len(set(ids)) != len(ids):
        sys.exit(f"{path}: repeats a case id")
    return set(ids)


def check_grammar(sources, select):
    for name, ids in sources.items():
        for case_id in ids:
            if not isinstance(case_id, str) or select.ID_RE.fullmatch(case_id) is None:
                sys.exit(f"{name}: a case id does not match [A-Za-z0-9._:-]+ (not printed)")


def tally(ids, ledger):
    counts = {"total": len(ids), "TM-A": 0, "TM-B": 0, NOT_IN_LEDGER: 0}
    for case_id in ids:
        counts[ledger.get(case_id, NOT_IN_LEDGER)] += 1
    return counts


def join(sources, ledger):
    """sources: name -> (ids, must_be_ledger_rows). Returns the sorted union and the counts per source."""
    union, report = set(), {}
    for name, (ids, ledger_only) in sources.items():
        unknown = {case_id for case_id in ids if case_id not in ledger}
        if unknown and ledger_only == "refuse":
            sys.exit(f"{name}: {len(unknown)} id(s) are not ledger rows (not printed)")
        if ledger_only == "drop":
            ids = ids - unknown
        report[name] = {**tally(ids, ledger), "dropped, not ledger rows": len(unknown) if ledger_only == "drop" else 0}
        union |= ids
    report["union"] = tally(union, ledger)
    return sorted(union), report


def build(args):
    if os.path.exists(args.out):
        sys.exit(f"{args.out} exists; this tool never overwrites")
    select = load_select()
    ledger = read_ledger(args.ledger)
    carriers, carrier_count = carrier_ids(args.carriers)
    sources = {
        "pilot examined": (pilot_ids(args.pilot_output, select), "refuse"),
        f"carriers ({carrier_count})": (carriers, "drop"),
        "T5": (manifest_ids(args.t5), "keep"),
        "additions": (additions_ids(args.additions), "refuse"),
    }
    check_grammar({name: ids for name, (ids, _) in sources.items()}, select)
    ids, report = join(sources, ledger)
    with open(args.out, "x", encoding="utf-8", newline="\n") as fh:
        fh.write(json.dumps(ids, indent=1) + "\n")
    print(f"wrote {args.out}")
    for name, counts in report.items():
        print(f"{name}: " + ", ".join(f"{key} {value}" for key, value in counts.items()))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = parser.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("build")
    for flag in ("pilot-output", "carriers", "t5", "additions", "ledger", "out"):
        b.add_argument(f"--{flag}", required=True)
    b.set_defaults(run=build)
    args = parser.parse_args(argv)
    args.run(args)


if __name__ == "__main__":
    main()
