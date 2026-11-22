"""Round planner for screening rounds (adaptation rule v1 section 7.1).

    py -3 -I corpus/sampling/round_plan.py plan --output <select.py output JSON> --decisions <decision file> [--screened <file>] [--ledger <ledger>]

Prints `{"TM-A": [...], "TM-B": [...]}`: per stratum the next undecided, unscreened candidates in walk order, up to the
carrier's slots (7 TM-A, 4 TM-B). It prints case ids only and never opens a source.

A draw output lists every capped candidate (the `cap` records) and the seed, so the `main` and `topup` orders beyond the
case select.py names are recomputed with select.ranked. A pilot output records only the pending case per stratum, so the
pilot order is recomputed with select.ranked over the candidates of `--ledger`, which must be the ledger the output was
drawn from (its SHA-256).
"""
import argparse
import importlib.util
import json
import os
import sys

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
STRATA = ("TM-A", "TM-B")
SLOTS = {"TM-A": 7, "TM-B": 4}


class Refused(Exception):
    pass


def _select():
    # select.py shadows the stdlib module of that name, so it is loaded by path under another name
    spec = importlib.util.spec_from_file_location("d48_select", os.path.join(HERE, "select.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _next(order, start, decisions, screened, limit):
    """Undecided, unscreened cases from `start` on. A screened case is never screened again; its decision reads its run."""
    return [c for c in order[order.index(start):] if c not in decisions and c not in screened][:limit]


def _plan_pilot(sel, out, decisions, screened, ledger):
    """The pilot walk of select.py (purpose `pilot`, one group per stratum), recomputed over the ledger's candidates."""
    if ledger is None:
        raise Refused("a pilot output needs --ledger: the pilot order is recomputed over the ledger's candidates")
    try:
        seed = bytes.fromhex(out["seed_hex"])
        want = out["inputs"]["ledger"]["sha256"]
        places = {h: out["pilot"][h]["n"] for h in STRATA}
    except (KeyError, TypeError, ValueError):
        raise Refused("the pilot output holds no seed, ledger digest or pilot allocation") from None
    if sel.sha256_hex(ledger) != want:
        raise Refused("--ledger is not the ledger the pilot output was drawn from (SHA-256 differs)")
    try:
        rows = sel.parse_ledger(ledger)["rows"]
    except sel.Refused as exc:
        raise Refused(str(exc)) from None
    plan = {h: [] for h in STRATA}
    for h in STRATA:
        ordered = [c for _, c in sel.ranked(seed, "pilot", h, [r["case_id"] for r in rows if r["stratum"] == h])]
        result = sel.walk(ordered, decisions, places[h])
        if result["status"] == "pending":
            plan[h] = _next(ordered, result["pending"], decisions, screened, SLOTS[h])
    return plan


def _plan_draw(sel, out, decisions, screened):
    try:
        seed = bytes.fromhex(out["seed_hex"])
        places = {h: out["main"][h]["n"] for h in STRATA}
        stratum_of = {a: v["stratum"] for a, v in out["attacks"].items()}
        records = [r for r in out["examined"] if r["purpose"] == "cap" and r["decision"] == "kept"]
    except (KeyError, TypeError):
        raise Refused("the draw output holds no seed, main allocation or cap records") from None
    kept = {}
    for r in records:
        kept.setdefault(r["group"], []).append(r["case_id"])
    attack_of = {c: a for a, ids in kept.items() for c in ids}
    plan = {h: [] for h in STRATA}
    main = {}
    for h in STRATA:
        ordered = [c for _, c in sel.ranked(seed, "main", h, [c for a, ids in kept.items() if stratum_of[a] == h for c in ids])]
        main[h] = sel.walk(ordered, decisions, places[h])
        if main[h]["status"] == "pending":
            plan[h] = _next(ordered, main[h]["pending"], decisions, screened, SLOTS[h])
    if any(main[h]["status"] != "complete" for h in STRATA):
        return plan  # the top-up follows the final main sample
    main_attacks = {attack_of[c] for h in STRATA for c in main[h]["selected"]}
    for attack in sorted(set(kept) - main_attacks):
        ordered = [c for _, c in sel.ranked(seed, "topup", attack, kept[attack])]
        result = sel.walk(ordered, decisions, 1)
        if result["status"] == "pending":
            plan[stratum_of[attack]] += _next(ordered, result["pending"], decisions, screened, 1)
    return {h: plan[h][:SLOTS[h]] for h in STRATA}


def plan(out, decisions, screened=frozenset(), ledger=None):
    """decisions: case_id -> (passed, reason), as select.walk reads them; ledger: the ledger bytes (pilot outputs only).
    Returns {stratum: [case_id, ...]}."""
    sel = _select()
    if not isinstance(out, dict):
        raise Refused("the output is not a JSON object")
    if out.get("schema") == sel.PILOT_SCHEMA:
        return _plan_pilot(sel, out, decisions, screened, ledger)
    if out.get("schema") == sel.SCHEMA:
        return _plan_draw(sel, out, decisions, screened)
    raise Refused(f"schema {out.get('schema')!r} is not a select.py pilot or draw output")


def read_decisions(path):
    decisions = {}
    with open(path, encoding="utf-8") as fh:
        for n, line in enumerate(fh, 1):
            if not line.strip():
                raise Refused(f"{path} line {n} is blank (select.py refuses it)")
            try:
                d = json.loads(line)
                case_id, passed = d["case_id"], d["pass"]
            except (ValueError, KeyError, TypeError):
                raise Refused(f"{path} line {n} is not a decision line") from None
            if not isinstance(passed, bool):
                raise Refused(f"{path} line {n}: pass is not true or false")
            if case_id in decisions:
                raise Refused(f"{path} line {n} repeats a case_id")
            decisions[case_id] = (passed, d.get("reason"))
    return decisions


def read_screened(path):
    with open(path, encoding="utf-8") as fh:
        return {line.strip() for line in fh if line.strip()}


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("plan", help="the next candidates per stratum for one screening round")
    p.add_argument("--output", required=True, help="select.py pilot or draw output JSON")
    p.add_argument("--decisions", required=True, help="the adaptation decision file")
    p.add_argument("--screened", help="file with one already-screened case_id per line")
    p.add_argument("--ledger", help="the ledger the pilot output was drawn from (pilot outputs only)")
    a = ap.parse_args(argv)
    try:
        with open(a.output, encoding="utf-8") as fh:
            out = json.load(fh)
        ledger = None
        if a.ledger:
            with open(a.ledger, "rb") as fh:
                ledger = fh.read()
        result = plan(out, read_decisions(a.decisions), read_screened(a.screened) if a.screened else frozenset(), ledger)
    except (Refused, OSError, ValueError) as exc:
        print(f"round_plan: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
