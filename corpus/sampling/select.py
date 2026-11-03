"""D48 confirmatory selection: the registered draw of thesis/tbound/h1-path/16-sampling-design.md
sections 4 to 6 (decision D48). Stdlib only; Python 3.12 compatible.

    py -3 -I corpus/sampling/select.py draw --ledger L.jsonl --commitment C.json \
        --beacon B.json --adaptation A.jsonl --out OUT.json
    py -3 -I corpus/sampling/select.py allocate --ledger L.jsonl --commitment C.json
    py -3 -I corpus/sampling/select.py verify-beacon --commitment C.json --beacon B.json
    py -3 -I corpus/sampling/select.py certify --NA 1000000 --NB 5000 --nA 298 --nB 1

draw exits 0 when complete, 3 when it needs an adaptation decision (the output is still
written), and 1 when blocked or refused. A blocked draw still writes its audit output.
Verifying the drand BLS signature is not done here: it needs an independent drand verifier.
"""

import argparse
import hashlib
import json
import re
import sys
from collections import Counter
from datetime import datetime, timedelta
from decimal import ROUND_FLOOR, Decimal, localcontext
from fractions import Fraction
from math import comb

SCHEMA = "tbound-d48-draw/v1"
TOTAL = 299  # main-sample size (16 section 5 step 1); --total overrides it for tests only
VARIANCE_TOTAL = 50  # variance subset size (16 section 5 step 6)
ROUND_DELAY = 86400  # the first round at least 24 hours after T (16 section 4)
STRATA = ("TM-A", "TM-B")
DOMAIN = "tbound-d48|"
ID_RE = re.compile(r"[A-Za-z0-9._:-]+")
HEX_RE = re.compile(r"[0-9A-Fa-f]+")
UTC_RE = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z")
EPOCH = datetime(1970, 1, 1)
BLS_NOTE = (
    "BLS signature NOT VERIFIED by select.py (no stdlib BLS). Verify it with an independent drand "
    "verifier against the committed chain public key and record the result before the draw."
)


class Refused(Exception):
    """An input or the commitment breaks a registered rule. Nothing is drawn."""


class Blocked(Exception):
    """The draw cannot complete: infeasible cap, exhausted pool, or too little non-cold capacity."""

    def __init__(self, reason, payload=None):
        super().__init__(reason)
        self.payload = payload or {}


# ----------------------------------------------------------------- input parsing

def sha256_hex(data):
    return hashlib.sha256(data).hexdigest()


def script_sha256():
    with open(__file__, "rb") as handle:
        return sha256_hex(handle.read())


def serialize(obj):
    return (json.dumps(obj, sort_keys=True, indent=2, ensure_ascii=True) + "\n").encode("ascii")


def _no_duplicate_keys(pairs):
    obj = {}
    for key, value in pairs:
        if key in obj:
            raise ValueError(f"duplicate key {key!r}")
        obj[key] = value
    return obj


def _reject_constant(name):
    raise ValueError(f"{name} is not allowed")


def _object(text, where):
    try:
        obj = json.loads(text, object_pairs_hook=_no_duplicate_keys, parse_constant=_reject_constant)
    except ValueError as exc:
        raise Refused(f"{where}: not valid JSON ({exc})") from None
    if not isinstance(obj, dict):
        raise Refused(f"{where}: expected a JSON object")
    return obj


def _text(data, where):
    try:
        return data.decode("utf-8")
    except UnicodeDecodeError:
        raise Refused(f"{where}: not UTF-8") from None


def _lines(data, where):
    lines = _text(data, where).split("\n")
    if lines[-1] == "":
        lines.pop()
    for number, line in enumerate(lines, 1):
        if line == "":
            raise Refused(f"{where} line {number}: blank line")
    return list(enumerate(lines, 1))


def _id(obj, field, where):
    value = obj.get(field)
    if not isinstance(value, str) or ID_RE.fullmatch(value) is None:
        raise Refused(f"{where}: {field} must match [A-Za-z0-9._:-]+")
    return value


def _hex_bytes(value, where):
    if not isinstance(value, str) or HEX_RE.fullmatch(value) is None or len(value) % 2:
        raise Refused(f"{where} must be an even-length hex string")
    return bytes.fromhex(value)


def _positive_int(obj, field, where):
    value = obj.get(field)
    if not isinstance(value, int) or isinstance(value, bool) or value < 1:
        raise Refused(f"{where}: {field} must be a positive integer")
    return value


def _digest(obj, field):
    value = obj.get(field)
    if not isinstance(value, str) or len(value) != 64 or HEX_RE.fullmatch(value) is None:
        raise Refused(f"commitment: {field} must be a 64-character SHA-256 hex digest")
    return value.lower()


def parse_ledger(data):
    """Candidate rows (sorted by case_id), excluded case ids, and the number of lines read."""
    rows, excluded, seen = [], [], set()
    lines = _lines(data, "ledger")
    for number, line in lines:
        where = f"ledger line {number}"
        obj = _object(line, where)
        case_id = _id(obj, "case_id", where)
        attack_id = _id(obj, "attack_id", where)
        mechanism_id = _id(obj, "mechanism_id", where)
        stratum = obj.get("stratum")
        if stratum not in STRATA:
            raise Refused(f"{where}: stratum must be TM-A or TM-B")
        flag = obj.get("excluded", False)
        if not isinstance(flag, bool):
            raise Refused(f"{where}: excluded must be true or false")
        if case_id in seen:
            raise Refused(f"{where}: duplicate case_id {case_id}")
        seen.add(case_id)
        if flag:
            excluded.append(case_id)
        else:
            rows.append({"case_id": case_id, "stratum": stratum, "attack_id": attack_id,
                         "mechanism_id": mechanism_id})
    owner = {}
    for item in rows:
        if owner.setdefault(item["attack_id"], item["stratum"]) != item["stratum"]:
            raise Refused(f"attack_id {item['attack_id']} appears in both strata")
    rows.sort(key=lambda item: item["case_id"].encode("ascii"))
    return {"rows": rows, "excluded": sorted(excluded), "row_count": len(lines)}


def parse_decisions(data, ledger):
    """Adaptation decisions: case_id -> (passed, reason). Each must name a non-excluded ledger case."""
    known = {item["case_id"] for item in ledger["rows"]}
    excluded = set(ledger["excluded"])
    decided = {}
    for number, line in _lines(data, "adaptation decisions"):
        where = f"adaptation line {number}"
        obj = _object(line, where)
        case_id = _id(obj, "case_id", where)
        passed = obj.get("pass")
        reason = obj.get("reason")
        if not isinstance(passed, bool):
            raise Refused(f"{where}: pass must be true or false")
        if not isinstance(reason, str) or not reason.strip():
            raise Refused(f"{where}: reason must be a nonempty string")
        if case_id in decided:
            raise Refused(f"{where}: second decision for {case_id}")
        if case_id in excluded:
            raise Refused(f"{where}: {case_id} is excluded from the ledger")
        if case_id not in known:
            raise Refused(f"{where}: {case_id} is not a candidate in the ledger")
        decided[case_id] = (passed, reason)
    return decided


def parse_utc(text):
    """Whole-second UTC ISO-8601 time as epoch seconds; fractions and offsets are refused."""
    if not isinstance(text, str) or UTC_RE.fullmatch(text) is None:
        raise Refused("T must be UTC ISO-8601 with whole seconds and a Z suffix, e.g. 2026-11-03T12:00:00Z")
    try:
        moment = datetime.strptime(text, "%Y-%m-%dT%H:%M:%SZ")
    except ValueError:
        raise Refused(f"T is not a valid date: {text}") from None
    return (moment - EPOCH) // timedelta(seconds=1)


def parse_commitment(data):
    obj = _object(_text(data, "commitment"), "commitment")
    chain = obj.get("chain")
    if not isinstance(chain, dict):
        raise Refused("commitment: chain must be an object")
    scheme = chain.get("scheme")
    if not isinstance(scheme, str) or not scheme:
        raise Refused("commitment: chain.scheme must be a nonempty string")
    for field in ("hash", "public_key"):
        if not isinstance(chain.get(field), str) or HEX_RE.fullmatch(chain[field]) is None:
            raise Refused(f"commitment: chain.{field} must be a hex string")
    return {
        "ledger_sha256": _digest(obj, "ledger_sha256"),
        "script_sha256": _digest(obj, "script_sha256"),
        "T": obj.get("T"),
        "T_epoch": parse_utc(obj.get("T")),
        "chain": {
            "hash": chain["hash"].lower(),
            "public_key": chain["public_key"].lower(),
            "scheme": scheme,
            "genesis_time": _positive_int(chain, "genesis_time", "commitment chain"),
            "period": _positive_int(chain, "period", "commitment chain"),
        },
    }


def check_digests(commit, ledger_data):
    """The ledger bytes and this script's bytes must be the ones the commitment binds."""
    if sha256_hex(ledger_data) != commit["ledger_sha256"]:
        raise Refused("ledger sha256 does not match the commitment")
    own = script_sha256()
    if own != commit["script_sha256"]:
        raise Refused(f"script sha256 {own} does not match the commitment script_sha256")


def round_for(genesis, period, t_epoch):
    """The first round R whose scheduled time genesis + (R-1)*period is at least T + 24 hours."""
    delta = t_epoch + ROUND_DELAY - genesis
    steps = 0 if delta <= 0 else -(-delta // period)
    return steps + 1


def _iso(epoch):
    return (EPOCH + timedelta(seconds=epoch)).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_beacon(data):
    obj = _object(_text(data, "beacon"), "beacon")
    number = obj.get("round")
    if not isinstance(number, int) or isinstance(number, bool) or number < 1:
        raise Refused("beacon round must be a positive integer")
    randomness = _hex_bytes(obj.get("randomness"), "beacon randomness")
    if len(randomness) != 32:
        raise Refused("beacon randomness must be 32 bytes")
    signature = _hex_bytes(obj.get("signature"), "beacon signature")
    previous = obj.get("previous_signature")
    previous_hex = None if previous is None else _hex_bytes(previous, "beacon previous_signature").hex()
    return {"round": number, "randomness": randomness, "signature": signature,
            "previous_signature": previous_hex, "response": obj}


def check_beacon(beacon, round_no):
    """Round and randomness checks; returns the 32-byte seed. The BLS signature is not checked."""
    if beacon["round"] != round_no:
        raise Refused(f"beacon round {beacon['round']} is not the committed round {round_no}")
    if hashlib.sha256(beacon["signature"]).digest() != beacon["randomness"]:
        raise Refused("beacon randomness is not SHA-256 of the signature")
    return beacon["randomness"]


def _round_of(commit):
    chain = commit["chain"]
    round_no = round_for(chain["genesis_time"], chain["period"], commit["T_epoch"])
    return round_no, chain["genesis_time"] + (round_no - 1) * chain["period"]


def verify_beacon(commitment_data, beacon_data):
    commit = parse_commitment(commitment_data)
    round_no, scheduled = _round_of(commit)
    beacon = parse_beacon(beacon_data)
    check_beacon(beacon, round_no)
    return {"round": round_no, "scheduled_utc": _iso(scheduled), "randomness": beacon["randomness"].hex(),
            "bls_signature_verified": False, "bls_note": BLS_NOTE}


# ------------------------------------------------------------- order rule (section 4)

def order_key(seed, purpose, group, case_id):
    """SHA-256(seed || UTF-8("tbound-d48|" + purpose + "|" + group + "|" + case_id)), 32 bytes."""
    return hashlib.sha256(seed + (DOMAIN + purpose + "|" + group + "|" + case_id).encode("utf-8")).digest()


def ranked(seed, purpose, group, case_ids, key_fn=order_key):
    """(key, case_id) pairs ascending by key; ties by case_id in unsigned byte order."""
    pairs = [(key_fn(seed, purpose, group, case_id), case_id) for case_id in case_ids]
    pairs.sort(key=lambda pair: (pair[0], pair[1].encode("utf-8")))
    return pairs


# ------------------------------------------------------- cap and allocation (section 5.1)

def largest_remainder(total, weight_a, weight_b):
    """Split `total` in proportion to two weights; a leftover place goes to the larger remainder,
    and a tie in remainders goes to TM-B. Exact integer arithmetic."""
    denominator = weight_a + weight_b
    weights = {"TM-A": weight_a, "TM-B": weight_b}
    if denominator == 0:
        return 0, 0, {"total": total, "weights": weights, "denominator": 0, "floor": {"TM-A": 0, "TM-B": 0},
                      "remainder_numerator": {"TM-A": 0, "TM-B": 0}, "extra_place": None, "tie_to_TM_B": False}
    floor_a, rem_a = divmod(total * weight_a, denominator)
    floor_b, rem_b = divmod(total * weight_b, denominator)
    extra = total - floor_a - floor_b  # always 0 or 1 for two classes
    assert extra in (0, 1)
    n_a, n_b, extra_place, tie = floor_a, floor_b, None, False
    if extra:
        tie = rem_a == rem_b
        if rem_a > rem_b:
            n_a, extra_place = n_a + 1, "TM-A"
        else:
            n_b, extra_place = n_b + 1, "TM-B"
    detail = {
        "total": total, "weights": weights, "denominator": denominator,
        "floor": {"TM-A": floor_a, "TM-B": floor_b},
        "remainder_numerator": {"TM-A": rem_a, "TM-B": rem_b},
        "extra_place": extra_place, "tie_to_TM_B": tie,
    }
    return n_a, n_b, detail


def choose_cap(rows, total):
    """The first k in 1..max count with n_A >= 1, n_B >= 1 and M_h(k) >= 2 n_h(k) for both strata."""
    counts = Counter((item["stratum"], item["attack_id"]) for item in rows)
    if not counts:
        raise Blocked("infeasible: no eligible candidates", {"cap": {"k": None, "trace": []}})
    attacks = {h: sorted({a for (s, a) in counts if s == h}) for h in STRATA}
    trace = []
    for k in range(1, max(counts.values()) + 1):
        pools = {h: sum(min(k, counts[(h, a)]) for a in attacks[h]) for h in STRATA}
        n_a, n_b, detail = largest_remainder(total, pools["TM-A"], pools["TM-B"])
        places = {"TM-A": n_a, "TM-B": n_b}
        feasible = n_a >= 1 and n_b >= 1 and all(pools[h] >= 2 * places[h] for h in STRATA)
        trace.append({"k": k, "M": pools, "n": places, "feasible": feasible,
                      "extra_place": detail["extra_place"], "tie_to_TM_B": detail["tie_to_TM_B"]})
        if feasible:
            return {"k": k, "M": pools, "n": places, "detail": detail, "trace": trace}
    raise Blocked("infeasible: no k gives both strata a place and twice their capped pool",
                  {"cap": {"k": None, "feasible": False, "trace": trace}})


def cap_order(seed, rows, k):
    """Per attack: (key, case_id, kept) in purpose-cap order; the first min(k, m) are kept."""
    by_attack = {}
    for item in rows:
        by_attack.setdefault(item["attack_id"], []).append(item["case_id"])
    return {
        attack: [(key, case_id, index < k)
                 for index, (key, case_id) in enumerate(ranked(seed, "cap", attack, sorted(ids)))]
        for attack, ids in sorted(by_attack.items())
    }


# ------------------------------------------------------- adaptation walks (sections 5.2, 5.4)

def walk(ordered, decisions, target):
    """Take cases in order until `target` pass. A case without a decision stops the walk."""
    selected, examined = [], []
    for case_id in ordered:
        if case_id not in decisions:
            examined.append((case_id, "pending", None))
            return {"status": "pending", "pending": case_id, "selected": selected, "examined": examined}
        passed, reason = decisions[case_id]
        if not passed:
            examined.append((case_id, "failed", reason))
            continue
        selected.append(case_id)
        examined.append((case_id, "selected", reason))
        if len(selected) == target:
            return {"status": "complete", "pending": None, "selected": selected, "examined": examined}
    return {"status": "exhausted", "pending": None, "selected": selected, "examined": examined}


def cold_quota(n):
    """floor(0.20 n + 0.5) in exact integer arithmetic."""
    return (2 * n + 5) // 10


def variance_allocation(weight_a, weight_b, total):
    """Variance places in proportion to the non-cold main cases; blocks if a class cannot supply its share."""
    n_a, n_b, detail = largest_remainder(total, weight_a, weight_b)
    if n_a > weight_a or n_b > weight_b or n_a + n_b != total:
        raise Blocked(f"insufficient non-cold main cases for the variance subset "
                      f"({weight_a} TM-A, {weight_b} TM-B available, {total} needed)", {"variance": detail})
    return n_a, n_b, detail


def _record(purpose, group, case_id, key, decision, reason):
    return {"purpose": purpose, "group": group, "case_id": case_id, "key": key.hex(),
            "decision": decision, "reason": reason}


def _population(capped, decisions):
    passes = sum(1 for case_id in capped if case_id in decisions and decisions[case_id][0])
    failures = sum(1 for case_id in capped if case_id in decisions and not decisions[case_id][0])
    return {"capped": len(capped), "passes_observed": passes, "failures_observed": failures,
            "undecided": len(capped) - passes - failures, "low": passes, "high": len(capped) - failures}


def _pending(purpose, group, stratum, case_id, attack_of, mechanism_of):
    return {"purpose": purpose, "group": group, "stratum": stratum, "case_id": case_id,
            "attack_id": attack_of[case_id], "mechanism_id": mechanism_of[case_id]}


# ------------------------------------------------------------------ the draw

def _select(out, ledger, seed, decision_map, total, variance):
    """Runs the registered steps. Returns (status, memberships); fills out['cap'], out['examined'], ..."""
    rows = ledger["rows"]
    cap = choose_cap(rows, total)
    out["cap"] = {"k": cap["k"], "M": cap["M"], "n": cap["n"], "allocation": cap["detail"],
                  "trace": cap["trace"]}
    n = cap["n"]
    attack_of = {item["case_id"]: item["attack_id"] for item in rows}
    mechanism_of = {item["case_id"]: item["mechanism_id"] for item in rows}
    stratum_of = {item["attack_id"]: item["stratum"] for item in rows}
    kept_by_attack = cap_order(seed, rows, cap["k"])
    for attack, entries in kept_by_attack.items():
        for key, case_id, kept in entries:
            out["examined"].append(_record("cap", attack, case_id, key, "kept" if kept else "beyond-cap", None))
    capped = {h: [] for h in STRATA}
    for attack, entries in kept_by_attack.items():
        capped[stratum_of[attack]].extend(case_id for _, case_id, kept in entries if kept)
    # Certification input (section 5 step 3): the passing population lies between the passes observed
    # and the capped candidates not yet failed.
    out["population"] = {h: _population(capped[h], decision_map) for h in STRATA}

    # Step 2: main sample, walked in main order within each stratum.
    main, pending = {}, []
    for h in STRATA:
        ordered = ranked(seed, "main", h, capped[h])
        keys = {case_id: key for key, case_id in ordered}
        main[h] = walk([case_id for _, case_id in ordered], decision_map, n[h])
        for case_id, decision, reason in main[h]["examined"]:
            out["examined"].append(_record("main", h, case_id, keys[case_id], decision, reason))
        if main[h]["pending"] is not None:
            pending.append(_pending("main", h, h, main[h]["pending"], attack_of, mechanism_of))
        out["main"][h] = {"n": n[h], "selected": main[h]["selected"], "status": main[h]["status"]}
    exhausted = [h for h in STRATA if main[h]["status"] == "exhausted"]
    if exhausted:
        h = exhausted[0]
        raise Blocked(f"main sample exhausted in {h}: {len(main[h]['selected'])} of {n[h]} places filled; "
                      "exhaustion never permits a new seed, a larger cap or a fill-in")
    if pending:
        out["needs_adaptation_next"] = pending
        return "needs-adaptation", None
    selected = {h: main[h]["selected"] for h in STRATA}

    # Step 5: cold partition, c_h = floor(0.20 n_h + 0.5), in cold order over the main members.
    cold_sel, cold_count = {}, {}
    for h in STRATA:
        cold_count[h] = cold_quota(n[h])
        ordered = ranked(seed, "cold", h, selected[h])
        cold_sel[h] = [case_id for _, case_id in ordered][:cold_count[h]]
        for key, case_id in ordered:
            out["examined"].append(_record("cold", h, case_id, key,
                                           "cold" if case_id in cold_sel[h] else "non-cold", None))

    # Step 6: variance subset of 50, allocated over the non-cold main cases, taken in variance order.
    places_a, places_b, variance_detail = variance_allocation(
        n["TM-A"] - cold_count["TM-A"], n["TM-B"] - cold_count["TM-B"], variance)
    places = {"TM-A": places_a, "TM-B": places_b}
    variance_sel = {}
    for h in STRATA:
        non_cold = [case_id for case_id in selected[h] if case_id not in cold_sel[h]]
        ordered = ranked(seed, "variance", h, non_cold)
        variance_sel[h] = [case_id for _, case_id in ordered][:places[h]]
        for key, case_id in ordered:
            out["examined"].append(_record("variance", h, case_id, key,
                                           "variance" if case_id in variance_sel[h] else "not-selected", None))
    out["variance"] = {"places": places, "allocation": variance_detail}

    # Step 4: top-up, after the final main sample, for each attack with no main case.
    main_attacks = {attack_of[case_id] for h in STRATA for case_id in selected[h]}
    topup, untested, pending_topup = {}, [], []
    for attack in kept_by_attack:
        if attack in main_attacks:
            continue
        kept = [case_id for _, case_id, is_kept in kept_by_attack[attack] if is_kept]
        ordered = ranked(seed, "topup", attack, kept)
        keys = {case_id: key for key, case_id in ordered}
        result = walk([case_id for _, case_id in ordered], decision_map, 1)
        for case_id, decision, reason in result["examined"]:
            out["examined"].append(_record("topup", attack, case_id, keys[case_id], decision, reason))
        if result["status"] == "complete":
            topup[attack] = result["selected"][0]
        elif result["status"] == "exhausted":
            untested.append(attack)
        else:
            pending_topup.append(_pending("topup", attack, stratum_of[attack], result["pending"],
                                          attack_of, mechanism_of))
    if pending_topup:
        out["needs_adaptation_next"] = pending_topup
        return "needs-adaptation", None
    return "complete", {"main": selected, "cold": cold_sel, "variance": variance_sel,
                        "topup": topup, "untested": sorted(untested)}


def run_draw(ledger_data, commitment_data, beacon_data, adaptation_data, total=TOTAL, variance=VARIANCE_TOTAL):
    """Returns (status, output). Refused raises for bad inputs; Blocked is returned as status blocked."""
    if total < 2:
        raise Refused("total must be at least 2 (one place per stratum)")
    ledger = parse_ledger(ledger_data)
    commit = parse_commitment(commitment_data)
    check_digests(commit, ledger_data)
    round_no, scheduled = _round_of(commit)
    beacon = parse_beacon(beacon_data)
    seed = check_beacon(beacon, round_no)
    decision_map = parse_decisions(adaptation_data, ledger)
    period = commit["chain"]["period"]
    out = {
        "schema": SCHEMA,
        "status": None,
        "blocked_reason": None,
        "needs_adaptation_next": [],
        "memberships": None,
        "script": {"sha256": script_sha256(), "commitment_sha256": commit["script_sha256"], "matches": True},
        "inputs": {
            "ledger": {"sha256": sha256_hex(ledger_data), "bytes": len(ledger_data),
                       "lines": ledger["row_count"], "candidates": len(ledger["rows"]),
                       "excluded": ledger["excluded"]},
            "commitment": {"sha256": sha256_hex(commitment_data), "bytes": len(commitment_data),
                           "ledger_sha256": commit["ledger_sha256"], "script_sha256": commit["script_sha256"],
                           "T": commit["T"], "chain": commit["chain"]},
            "beacon": {"sha256": sha256_hex(beacon_data), "bytes": len(beacon_data), "round": beacon["round"],
                       "randomness": beacon["randomness"].hex(), "signature": beacon["signature"].hex(),
                       "previous_signature": beacon["previous_signature"], "response": beacon["response"],
                       "bls_signature_verified": False, "bls_note": BLS_NOTE},
            "adaptation": {"sha256": sha256_hex(adaptation_data), "bytes": len(adaptation_data),
                           "decisions": len(decision_map), "not_consulted": []},
        },
        "round": {"R": round_no, "scheduled_utc": _iso(scheduled),
                  "previous_scheduled_utc": _iso(scheduled - period) if round_no > 1 else None,
                  "T_utc": commit["T"], "T_epoch": commit["T_epoch"],
                  "threshold_epoch": commit["T_epoch"] + ROUND_DELAY},
        "seed_hex": seed.hex(),
        "total": total,
        "variance_places": variance,
        "registered_parameters": total == TOTAL and variance == VARIANCE_TOTAL,
        "cap": None,
        "variance": None,
        "main": {},
        "examined": [],
    }
    try:
        status, memberships = _select(out, ledger, seed, decision_map, total, variance)
    except Blocked as exc:
        status, memberships = "blocked", None
        out["blocked_reason"] = str(exc)
        out.update(exc.payload)
    out["status"] = status
    out["memberships"] = memberships
    considered = {record["case_id"] for record in out["examined"]}
    out["inputs"]["adaptation"]["not_consulted"] = sorted(set(decision_map) - considered)
    return status, out


def allocate(ledger_data, commitment_data, total=TOTAL):
    """Cap and allocation from the ledger and commitment only (no beacon, no decisions)."""
    ledger = parse_ledger(ledger_data)
    commit = parse_commitment(commitment_data)
    check_digests(commit, ledger_data)
    if total < 2:
        raise Refused("total must be at least 2 (one place per stratum)")
    cap = choose_cap(ledger["rows"], total)
    return {"schema": SCHEMA, "ledger_sha256": sha256_hex(ledger_data),
            "commitment_sha256": sha256_hex(commitment_data), "script_sha256": script_sha256(),
            "total": total, "cap": {"k": cap["k"], "M": cap["M"], "n": cap["n"],
                                    "allocation": cap["detail"], "trace": cap["trace"]}}


# ---------------------------------------------------- certification (section 6)

def _zero_event_limit(n):
    with localcontext() as ctx:
        ctx.prec = 60
        return Decimal(1) - Decimal("0.05") ** (Decimal(1) / Decimal(n))


def certify(population_a, population_b, n_a, n_b):
    """Largest P0 over every split K_A + K_B = K0 and every stratum size in the ranges.

    population_a and population_b are (low, high) ranges of capped passing-population sizes.
    The claim is certified when the maximum is at most 1/20.
    """
    (low_a, high_a), (low_b, high_b) = population_a, population_b
    if n_a < 1 or n_b < 1:
        raise Refused("stratum sample sizes must be at least 1")
    if not (1 <= low_a <= high_a and 1 <= low_b <= high_b):
        raise Refused("population ranges must be positive and ordered")
    if low_a < n_a or low_b < n_b:
        raise Refused("a passing population is smaller than its stratum sample")
    n = n_a + n_b
    limit = _zero_event_limit(n)
    best_num, best_den, worst = 0, 1, None
    for pop_a in range(low_a, high_a + 1):
        for pop_b in range(low_b, high_b + 1):
            with localcontext() as ctx:
                ctx.prec = 60
                k0 = int((limit * (pop_a + pop_b)).to_integral_value(rounding=ROUND_FLOOR)) + 1
            den = comb(pop_a, n_a) * comb(pop_b, n_b)
            for escapes_b in range(max(0, k0 - pop_a), min(k0, pop_b) + 1):
                escapes_a = k0 - escapes_b
                num = comb(pop_a - escapes_a, n_a) * comb(pop_b - escapes_b, n_b)
                if worst is None or num * best_den > best_num * den:
                    best_num, best_den = num, den
                    worst = {"N_A": pop_a, "N_B": pop_b, "K0": k0, "K_A": escapes_a, "K_B": escapes_b}
    value = Fraction(best_num, best_den)
    return {
        "alpha": "1/20",
        "U": str(limit.quantize(Decimal("1E-18"))),
        "n": n, "nA": n_a, "nB": n_b,
        "N_A_range": [low_a, high_a], "N_B_range": [low_b, high_b],
        "max_P0": float(value),
        "max_P0_fraction": f"{value.numerator}/{value.denominator}",
        "worst_case": worst,
        "certified": 20 * best_num <= best_den,
    }


# ------------------------------------------------------------------------- CLI

def _read(path):
    with open(path, "rb") as handle:
        return handle.read()


def _range(text):
    low, _, high = text.partition(":")
    try:
        lo = int(low)
        hi = int(high) if high else lo
    except ValueError:
        raise Refused(f"range must be N or LOW:HIGH, got {text!r}") from None
    return lo, hi


def cmd_draw(args):
    status, out = run_draw(_read(args.ledger), _read(args.commitment), _read(args.beacon),
                           _read(args.adaptation), total=args.total)
    data = serialize(out)
    try:
        with open(args.out, "xb") as handle:
            handle.write(data)
    except FileExistsError:
        raise Refused(f"refusing to overwrite: the output file {args.out} already exists") from None
    print(f"status: {status}; output sha256 {sha256_hex(data)}")
    for item in out["needs_adaptation_next"]:
        print(f"needs adaptation decision next: {item['stratum']} {item['purpose']} {item['case_id']} "
              f"(attack {item['attack_id']}, mechanism {item['mechanism_id']})")
    if status == "blocked":
        print(f"blocked: {out['blocked_reason']}", file=sys.stderr)
    print("note: the drand BLS signature was not verified by this script; verify it with an independent "
          "drand verifier and record the result", file=sys.stderr)
    return {"complete": 0, "needs-adaptation": 3, "blocked": 1}[status]


def cmd_allocate(args):
    sys.stdout.write(serialize(allocate(_read(args.ledger), _read(args.commitment), args.total)).decode("ascii"))
    return 0


def cmd_verify_beacon(args):
    result = verify_beacon(_read(args.commitment), _read(args.beacon))
    print(f"round {result['round']} matches the commitment (scheduled {result['scheduled_utc']})")
    print(f"randomness {result['randomness']} equals SHA-256 of the signature")
    print("BLS signature: NOT VERIFIED by select.py. Verify it with an independent drand verifier against "
          "the committed chain public key and record the result before the draw.")
    return 0


def cmd_certify(args):
    result = certify(_range(args.NA), _range(args.NB), args.nA, args.nB)
    sys.stdout.write(serialize(result).decode("ascii"))
    return 0 if result["certified"] else 1


def main(argv=None):
    parser = argparse.ArgumentParser(prog="select.py", description="D48 confirmatory selection (16 sections 4-6).")
    commands = parser.add_subparsers(dest="command", required=True)

    draw = commands.add_parser("draw", help="run the registered draw and write the audit output")
    draw.add_argument("--ledger", required=True)
    draw.add_argument("--commitment", required=True)
    draw.add_argument("--beacon", required=True)
    draw.add_argument("--adaptation", required=True)
    draw.add_argument("--out", required=True)
    draw.add_argument("--total", type=int, default=TOTAL, help="tests only; the registered value is 299")
    draw.set_defaults(handler=cmd_draw)

    alloc = commands.add_parser("allocate", help="cap and allocation only (no beacon)")
    alloc.add_argument("--ledger", required=True)
    alloc.add_argument("--commitment", required=True)
    alloc.add_argument("--total", type=int, default=TOTAL, help="tests only; the registered value is 299")
    alloc.set_defaults(handler=cmd_allocate)

    verify = commands.add_parser("verify-beacon", help="check round and randomness (BLS is not checked)")
    verify.add_argument("--commitment", required=True)
    verify.add_argument("--beacon", required=True)
    verify.set_defaults(handler=cmd_verify_beacon)

    cert = commands.add_parser("certify", help="zero-event certification (section 6); ranges as LOW:HIGH")
    cert.add_argument("--NA", required=True)
    cert.add_argument("--NB", required=True)
    cert.add_argument("--nA", type=int, required=True)
    cert.add_argument("--nB", type=int, required=True)
    cert.set_defaults(handler=cmd_certify)

    args = parser.parse_args(argv)
    try:
        return args.handler(args)
    except Refused as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except Blocked as exc:
        print(f"blocked: {exc}", file=sys.stderr)
        return 1
    except OSError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
