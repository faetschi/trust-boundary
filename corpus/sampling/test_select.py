"""Tests for the D48 selection script, corpus/sampling/select.py.

Spec: thesis/tbound/h1-path/16-sampling-design.md sections 3 to 6, under decision D48.
Run from the repository root:

    py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v
"""

import hashlib
import importlib.util
import json
import math
import os
import sys
import tempfile
import unittest
from decimal import ROUND_FLOOR, Decimal, localcontext
from fractions import Fraction
from math import comb

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "select.py")

# select.py has the name the spec requires, so it would shadow the stdlib `select` module while
# unittest discover has this directory first on sys.path, and subprocess imports `select` on POSIX.
# Bind the stdlib modules first with this directory off the path, then load select.py by path.
_saved_path = list(sys.path)
sys.path[:] = [p for p in sys.path if os.path.normcase(os.path.abspath(p or os.curdir)) != os.path.normcase(HERE)]
try:
    import select as _stdlib_select  # noqa: F401
    import subprocess
finally:
    sys.path[:] = _saved_path
_spec = importlib.util.spec_from_file_location("d48_select", SCRIPT)
sel = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(sel)

with open(SCRIPT, "rb") as _script_file:
    SCRIPT_SHA = hashlib.sha256(_script_file.read()).hexdigest()

DAY = 86400
T_TEXT = "2026-11-03T12:00:00Z"
T_EPOCH = 1793707200  # 2026-11-03T12:00:00Z
DRAND_MAINNET_CHAIN = "8990e7a9aaed2ffed73dbd7092123d6f289930540d7651336225dc172e51b2ce"
FAKE_SIGNATURE = b"fake drand signature, D48 test vector only"
FAKE_SEED = hashlib.sha256(FAKE_SIGNATURE).digest()


def sha(data):
    return hashlib.sha256(data).hexdigest()


def row(case_id, stratum, attack, mechanism="mech.1", **extra):
    return {"case_id": case_id, "stratum": stratum, "attack_id": attack, "mechanism_id": mechanism, **extra}


def jsonl(rows):
    return "".join(json.dumps(r, sort_keys=True) + "\n" for r in rows).encode("utf-8")


POLICY_SHA = "11" * 32
RULE_SHA = "22" * 32


def _commitment_fields(ledger, ledger_sha, script_sha, round_no, period, shift, T):
    genesis = T_EPOCH + DAY - (round_no - 1) * period + shift
    return {
        "T": T,
        "chain": {
            "hash": DRAND_MAINNET_CHAIN,
            "public_key": "ab" * 48,
            "scheme": "pedersen-bls-chained",
            "genesis_time": genesis,
            "period": period,
        },
        "ledger_sha256": ledger_sha if ledger_sha is not None else sha(ledger),
        "script_sha256": script_sha,
    }


def commitment(ledger, *, ledger_sha=None, script_sha=SCRIPT_SHA, round_no=12, period=3, shift=1, T=T_TEXT,
               development=()):
    """Main commitment whose round `round_no` is the first round at or after T+24h (scheduled one second late)."""
    obj = _commitment_fields(ledger, ledger_sha, script_sha, round_no, period, shift, T)
    obj.update(kind="main", policy_sha256=POLICY_SHA, adaptation_rule_sha256=RULE_SHA,
               development_case_ids=list(development))
    return json.dumps(obj, sort_keys=True).encode("utf-8")


def pilot_commitment(ledger, *, ledger_sha=None, script_sha=SCRIPT_SHA, round_no=12, period=3, shift=1, T=T_TEXT,
                     pilot_size=11):
    """Pilot commitment: the same chain and round rule, binding the pilot size instead of the main fields."""
    obj = _commitment_fields(ledger, ledger_sha, script_sha, round_no, period, shift, T)
    obj.update(kind="pilot", pilot_size=pilot_size)
    return json.dumps(obj, sort_keys=True).encode("utf-8")


def beacon(round_no=12, signature=FAKE_SIGNATURE, previous=None):
    obj = {"round": round_no, "randomness": sha(signature), "signature": signature.hex()}
    if previous is not None:
        obj["previous_signature"] = previous.hex()
    return json.dumps(obj, sort_keys=True).encode("utf-8")


def decisions(mapping):
    """mapping: case_id -> (passed, reason)."""
    return "".join(
        json.dumps({"case_id": c, "pass": p, "reason": r}, sort_keys=True) + "\n"
        for c, (p, r) in sorted(mapping.items())
    ).encode("utf-8")


def pool(stratum, attacks, per_attack, prefix):
    return [
        row(f"{prefix}{a}.{i}", stratum, f"{prefix}att{a}")
        for a in range(attacks)
        for i in range(per_attack)
    ]


def ref_key(purpose, group, case_id, seed=FAKE_SEED):
    """Independent restatement of the order rule for test expectations."""
    return hashlib.sha256(seed + f"tbound-d48|{purpose}|{group}|{case_id}".encode("utf-8")).digest()


def ref_order(purpose, group, case_ids, seed=FAKE_SEED):
    return sorted(case_ids, key=lambda c: (ref_key(purpose, group, c, seed), c.encode("utf-8")))


# Fixed test vector: 6 attacks x 3 cases, total 6 and variance 2 (the registered 299 and 50 are
# too large for a tiny fixture). k = 2, so each attack keeps two of its three cases.
VECTOR_ATTACKS = [
    ("TM-A", "inj.a"), ("TM-A", "inj.b"), ("TM-A", "inj.c"),
    ("TM-B", "tool.d"), ("TM-B", "tool.e"), ("TM-B", "tool.f"),
]
VECTOR_ROWS = [
    row(f"{attack}.{i}", stratum, attack, "mech.inj" if stratum == "TM-A" else "mech.tool")
    for stratum, attack in VECTOR_ATTACKS
    for i in (1, 2, 3)
]
VECTOR_TOTAL = 6
VECTOR_VARIANCE = 2
# D47: the cap and top-up group is "<stratum>:<attack_id>", so the expected attack names are qualified.
VECTOR_GROUPS = [f"{stratum}:{attack}" for stratum, attack in VECTOR_ATTACKS]
ATTACK_OF = {r["case_id"]: f"{r['stratum']}:{r['attack_id']}" for r in VECTOR_ROWS}
STRATUM_OF_ATTACK = {f"{stratum}:{attack}": stratum for stratum, attack in VECTOR_ATTACKS}
VECTOR_DECISIONS = {r["case_id"]: (True, "meaning preserved") for r in VECTOR_ROWS}
VECTOR_DECISIONS["inj.a.1"] = (False, "meaning not preserved")
VECTOR_DECISIONS["tool.e.2"] = (False, "not runnable in the cell")
VECTOR_DECISIONS["inj.c.3"] = (False, "overlaps a pilot scenario")

# Pinned after an independent hashlib-only reference agreed with every membership list. Re-pinned for the
# pilot change: the output gained the commitment kind fields, `development` and `attacks`; the memberships
# did not change. Re-pinned for D47: the group is "<stratum>:<attack_id>", which changes the cap and top-up
# keys, so the draw memberships changed; the pilot memberships did not (only script_sha256 moved). The digest covers this script's own bytes (script_sha256), so any edit to select.py
# requires re-pinning both digests.
PINNED_VECTOR_SHA256 = "1f042c0ee6ef1d1f8ec5d7e0bc30c955d24019592b81874a1db4f857d2889b05"
PINNED_PILOT_SHA256 = "0515c4da36418c53f0248c27d4225af0e1208b61c2e93c9f31ad43c8aa186575"
PINNED_VECTOR_MEMBERS = {
    "main": {"TM-A": ["inj.a.3", "inj.b.1", "inj.c.1"], "TM-B": ["tool.e.3", "tool.d.1", "tool.e.1"]},
    "cold": {"TM-A": ["inj.b.1"], "TM-B": ["tool.d.1"]},
    "variance": {"TM-A": ["inj.a.3"], "TM-B": ["tool.e.1"]},
    "topup": {"TM-B:tool.f": "tool.f.1"},
    "untested": [],
}


def run_vector(decision_map=None, rows=None, total=VECTOR_TOTAL, variance=VECTOR_VARIANCE):
    rows = VECTOR_ROWS if rows is None else rows
    ledger = jsonl(rows)
    return sel.run_draw(
        ledger,
        commitment(ledger),
        beacon(),
        decisions(VECTOR_DECISIONS if decision_map is None else decision_map),
        total=total,
        variance=variance,
    )


def run_pilot_vector(decision_map=None, rows=None, pilot_size=11):
    rows = VECTOR_ROWS if rows is None else rows
    ledger = jsonl(rows)
    return sel.run_pilot(
        ledger,
        pilot_commitment(ledger, pilot_size=pilot_size),
        beacon(),
        decisions(VECTOR_DECISIONS if decision_map is None else decision_map),
        pilot_size=pilot_size,
    )


# Pilot of 11 on the fixed vector: 9 uncapped candidates per stratum, so the split is 5/6 (tie to TM-B).
# Members from an independent hashlib-only reference over purpose `pilot`, skipping the failing cases.
PINNED_PILOT_MEMBERS = {
    "TM-A": ["inj.a.2", "inj.a.3", "inj.b.3", "inj.b.1", "inj.b.2"],
    "TM-B": ["tool.e.3", "tool.d.2", "tool.d.1", "tool.f.1", "tool.d.3", "tool.e.1"],
}


def assert_selection_invariants(tc, out, decision_map):
    members = out["memberships"]
    main_a, main_b = members["main"]["TM-A"], members["main"]["TM-B"]
    main = main_a + main_b
    tc.assertEqual(len(main), len(set(main)))
    tc.assertEqual(len(main_a), out["cap"]["n"]["TM-A"])
    tc.assertEqual(len(main_b), out["cap"]["n"]["TM-B"])
    topup = members["topup"]
    tc.assertFalse(set(topup.values()) & set(main), "top-up must be disjoint from the main sample")
    main_attacks = {ATTACK_OF[c] for c in main}
    untested = set(members["untested"])
    for attack in VECTOR_GROUPS:
        if attack in main_attacks:
            tc.assertNotIn(attack, topup)
            tc.assertNotIn(attack, untested)
        elif attack in topup:
            tc.assertNotIn(attack, untested)
        else:
            tc.assertIn(attack, untested, f"{attack} has no main case and no top-up")
    for attack in untested:
        kept = [r["case_id"] for r in out["examined"]
                if r["purpose"] == "cap" and r["group"] == attack and r["decision"] == "kept"]
        for case_id in kept:
            tc.assertFalse(decision_map[case_id][0], f"untested {attack} has a passing case {case_id}")


class IdentifierAndLedgerTests(unittest.TestCase):
    def test_accepts_stable_ascii_ids(self):
        ledger = sel.parse_ledger(jsonl([row("case.1:x-_", "TM-A", "att_1.b", "mech:9-z")]))
        self.assertEqual([r["case_id"] for r in ledger["rows"]], ["case.1:x-_"])

    def test_rejects_case_ids_outside_the_grammar(self):
        for bad in ["a|b", "a b", "a\n", "", "é", "a/b", 'a"b', "a\\b", " a", 7, None]:
            with self.subTest(case_id=bad):
                with self.assertRaises(sel.Refused):
                    sel.parse_ledger(jsonl([row(bad, "TM-A", "att")]))

    def test_rejects_attack_and_mechanism_ids_outside_the_grammar(self):
        for bad in ["a|b", "a\n", "", "x y"]:
            with self.subTest(attack=bad):
                with self.assertRaises(sel.Refused):
                    sel.parse_ledger(jsonl([row("c1", "TM-A", bad)]))
            with self.subTest(mechanism=bad):
                with self.assertRaises(sel.Refused):
                    sel.parse_ledger(jsonl([row("c1", "TM-A", "att", mechanism=bad)]))

    def test_rejects_strata_other_than_tm_a_and_tm_b(self):
        for bad in ["TM-C", "tm-a", "TM-A ", "", None]:
            with self.subTest(stratum=bad):
                with self.assertRaises(sel.Refused):
                    sel.parse_ledger(jsonl([row("c1", bad, "att")]))

    def test_rejects_duplicate_case_ids_including_excluded_rows(self):
        with self.assertRaises(sel.Refused):
            sel.parse_ledger(jsonl([row("c1", "TM-A", "a1"), row("c1", "TM-A", "a2")]))
        with self.assertRaises(sel.Refused):
            sel.parse_ledger(jsonl([row("c1", "TM-A", "a1"), row("c1", "TM-A", "a2", excluded=True)]))

    def test_skips_and_lists_excluded_rows(self):
        ledger = sel.parse_ledger(jsonl([
            row("c2", "TM-B", "b1"),
            row("c1", "TM-A", "a1"),
            row("pilot.1", "TM-A", "pilot", excluded=True),
        ]))
        self.assertEqual([r["case_id"] for r in ledger["rows"]], ["c1", "c2"])
        self.assertEqual(ledger["excluded"], ["pilot.1"])

    def test_rejects_non_boolean_excluded_flag(self):
        with self.assertRaises(sel.Refused):
            sel.parse_ledger(jsonl([row("c1", "TM-A", "a1", excluded="true")]))

    def test_accepts_one_scenario_in_both_strata_as_two_groups_capped_separately(self):
        rows = [row(f"{s}.{i}", s, "shared") for s in ("TM-A", "TM-B") for i in range(5)]
        ledger = sel.parse_ledger(jsonl(rows))
        self.assertEqual(ledger["attacks"], {"TM-A:shared": "TM-A", "TM-B:shared": "TM-B"})
        self.assertEqual({r["scenario_id"] for r in ledger["rows"]}, {"shared"})
        order = sel.cap_order(FAKE_SEED, ledger["rows"], 2)
        self.assertEqual(set(order), {"TM-A:shared", "TM-B:shared"})
        for group, stratum in (("TM-A:shared", "TM-A"), ("TM-B:shared", "TM-B")):
            ids = [r["case_id"] for r in rows if r["stratum"] == stratum]
            self.assertEqual([cid for _, cid, _ in order[group]], ref_order("cap", group, ids))
            self.assertEqual([kept for _, _, kept in order[group]], [True, True, False, False, False])

    def test_rejects_blank_non_object_and_duplicate_key_lines(self):
        good = jsonl([row("c1", "TM-A", "a1")]).decode("utf-8")
        for bad in [good + "\n", good + "[1]\n", '{"case_id":"c1","case_id":"c2","stratum":"TM-A",'
                    '"attack_id":"a","mechanism_id":"m"}\n']:
            with self.subTest(bad=bad[-30:]):
                with self.assertRaises(sel.Refused):
                    sel.parse_ledger(bad.encode("utf-8"))

    def test_accepts_missing_final_newline(self):
        ledger = sel.parse_ledger(jsonl([row("c1", "TM-A", "a1")]).rstrip(b"\n"))
        self.assertEqual(len(ledger["rows"]), 1)


class CommitmentAndTimeTests(unittest.TestCase):
    def test_parses_utc_whole_seconds_with_z_only(self):
        self.assertEqual(sel.parse_utc(T_TEXT), T_EPOCH)
        for bad in ["2026-11-03T12:00:00+01:00", "2026-11-03T12:00:00.5Z", "2026-11-03 12:00:00Z",
                    "2026-11-03T12:00:00", "2026-11-03T12:00:00+00:00"]:
            with self.subTest(T=bad):
                with self.assertRaises(sel.Refused):
                    sel.parse_utc(bad)

    def test_ledger_digest_must_match_the_ledger_bytes(self):
        ledger = jsonl(VECTOR_ROWS)
        with self.assertRaisesRegex(sel.Refused, "ledger"):
            sel.run_draw(ledger, commitment(ledger, ledger_sha="0" * 64), beacon(), decisions({}), total=6)

    def test_script_digest_must_match_the_running_script(self):
        ledger = jsonl(VECTOR_ROWS)
        with self.assertRaisesRegex(sel.Refused, "script"):
            sel.run_draw(ledger, commitment(ledger, script_sha="1" * 64), beacon(), decisions({}), total=6)

    def test_digests_accept_uppercase_hex_from_get_filehash(self):
        ledger = jsonl(VECTOR_ROWS)
        obj = json.loads(commitment(ledger))
        obj["ledger_sha256"] = obj["ledger_sha256"].upper()
        obj["script_sha256"] = obj["script_sha256"].upper()
        status, _ = sel.run_draw(ledger, json.dumps(obj).encode(), beacon(), decisions({}), total=6, variance=2)
        self.assertEqual(status, "needs-adaptation")

    def test_commitment_shape_is_checked(self):
        ledger = jsonl(VECTOR_ROWS)
        obj = json.loads(commitment(ledger))
        del obj["chain"]["period"]
        with self.assertRaises(sel.Refused):
            sel.parse_commitment(json.dumps(obj).encode())
        obj = json.loads(commitment(ledger))
        obj["chain"]["period"] = 0
        with self.assertRaises(sel.Refused):
            sel.parse_commitment(json.dumps(obj).encode())


class RoundTests(unittest.TestCase):
    def test_first_round_at_or_after_t_plus_24h_matches_brute_force(self):
        for period in (1, 3, 7):
            for genesis in (1000, 5000):
                for target in range(genesis - 5, genesis + 60 * period):
                    expected = 1
                    while genesis + (expected - 1) * period < target:
                        expected += 1
                    with self.subTest(period=period, genesis=genesis, target=target):
                        self.assertEqual(sel.round_for(genesis, period, target - DAY), expected)

    def test_exact_boundary_and_one_second_either_side(self):
        genesis, period = 1_000_000, 3
        scheduled_12 = genesis + 11 * period
        self.assertEqual(sel.round_for(genesis, period, scheduled_12 - DAY), 12)
        self.assertEqual(sel.round_for(genesis, period, scheduled_12 - DAY - 1), 12)
        self.assertEqual(sel.round_for(genesis, period, scheduled_12 - DAY + 1), 13)

    def test_genesis_at_or_after_threshold_gives_round_one(self):
        self.assertEqual(sel.round_for(1000, 3, 1000 - DAY), 1)
        self.assertEqual(sel.round_for(1000, 3, 1000 - DAY - 500), 1)


class BeaconTests(unittest.TestCase):
    def test_valid_beacon_yields_the_32_raw_randomness_bytes_as_seed(self):
        seed = sel.check_beacon(sel.parse_beacon(beacon(12)), 12)
        self.assertEqual(seed, FAKE_SEED)
        self.assertEqual(len(seed), 32)

    def test_round_must_equal_the_computed_round(self):
        with self.assertRaisesRegex(sel.Refused, "round"):
            sel.check_beacon(sel.parse_beacon(beacon(13)), 12)

    def test_randomness_must_equal_sha256_of_the_signature(self):
        obj = json.loads(beacon(12))
        obj["randomness"] = "00" * 32
        with self.assertRaisesRegex(sel.Refused, "randomness"):
            sel.check_beacon(sel.parse_beacon(json.dumps(obj).encode()), 12)

    def test_malformed_hex_is_refused(self):
        for field in ("signature", "randomness"):
            obj = json.loads(beacon(12))
            obj[field] = "zz"
            with self.subTest(field=field):
                with self.assertRaises(sel.Refused):
                    sel.check_beacon(sel.parse_beacon(json.dumps(obj).encode()), 12)

    def test_previous_signature_is_optional_and_hex_checked(self):
        previous = b"previous round signature"
        sel.check_beacon(sel.parse_beacon(beacon(12, previous=previous)), 12)
        obj = json.loads(beacon(12))
        obj["previous_signature"] = "xyz"
        with self.assertRaises(sel.Refused):
            sel.parse_beacon(json.dumps(obj).encode())

    def test_verify_beacon_says_bls_signature_was_not_verified(self):
        ledger = jsonl(VECTOR_ROWS)
        result = sel.verify_beacon(commitment(ledger), beacon(12))
        self.assertEqual(result["round"], 12)
        self.assertFalse(result["bls_signature_verified"])
        self.assertIn("independent drand verifier", result["bls_note"])


class OrderRuleTests(unittest.TestCase):
    def test_key_matches_hand_computed_sha256(self):
        # hashlib over SHA-256(seed || "tbound-d48|main|TM-A|case-1"), computed outside select.py.
        self.assertEqual(
            sel.order_key(bytes(32), "main", "TM-A", "case-1").hex(),
            "d93a1092fecf196f1101c829692c2bf7bd3a87ebeff8becf72b3ec49a5d4c068",
        )
        self.assertEqual(
            sel.order_key(bytes(range(32)), "main", "TM-A", "case-1").hex(),
            "1f5a10fb7552563fd4dc12797a944b791de9082db2dadbdf45c6171741a60d3c",
        )
        self.assertEqual(
            sel.order_key(bytes(range(32)), "cap", "att.x", "a").hex(),
            "192ec894f9ebf19aec78a5908ee1c8d96f178b6dc332b02fceb2cc49705c6120",
        )

    def test_seed_is_used_as_raw_bytes(self):
        seed = bytes(range(32))
        self.assertEqual(sel.order_key(seed, "cold", "TM-B", "case-9"),
                         hashlib.sha256(seed + b"tbound-d48|cold|TM-B|case-9").digest())

    def test_ranking_is_ascending_by_hash_not_by_identifier(self):
        ids = [f"c{i}" for i in range(12)]
        got = [cid for _, cid in sel.ranked(bytes(range(32)), "cap", "att.x", ids)]
        self.assertEqual(got, ref_order("cap", "att.x", ids, bytes(range(32))))
        self.assertNotEqual(got, sorted(ids))

    def test_ties_break_by_case_id_in_unsigned_byte_order(self):
        ids = ["b", "a", "a.b", "A", "a_", "0", ".", ":"]
        got = [cid for _, cid in sel.ranked(FAKE_SEED, "main", "TM-A", ids, key_fn=lambda *a: bytes(32))]
        self.assertEqual(got, [".", "0", ":", "A", "a", "a.b", "a_", "b"])


class CapAndAllocationTests(unittest.TestCase):
    def test_largest_remainder_gives_the_leftover_place_to_the_larger_remainder(self):
        na, nb, detail = sel.largest_remainder(299, 2, 1)
        self.assertEqual((na, nb), (199, 100))
        self.assertFalse(detail["tie_to_TM_B"])

    def test_remainder_tie_goes_to_tm_b(self):
        self.assertEqual(sel.largest_remainder(299, 1, 1)[:2], (149, 150))
        self.assertTrue(sel.largest_remainder(299, 1, 1)[2]["tie_to_TM_B"])
        self.assertEqual(sel.largest_remainder(3, 5, 5)[:2], (1, 2))

    def test_zero_weight_class_gets_no_places(self):
        self.assertEqual(sel.largest_remainder(299, 0, 7)[:2], (0, 299))

    def test_cap_skips_a_k_that_leaves_a_class_empty(self):
        rows = pool("TM-A", 1000, 1, "a") + pool("TM-B", 1, 2, "b")
        result = sel.choose_cap(rows, 299)
        self.assertEqual(result["k"], 2)
        self.assertEqual((result["n"]["TM-A"], result["n"]["TM-B"]), (298, 1))
        self.assertEqual(len(result["trace"]), 2)
        self.assertFalse(result["trace"][0]["feasible"])

    def test_tiny_class_makes_the_draw_infeasible(self):
        with self.assertRaises(sel.Blocked):
            sel.choose_cap(pool("TM-A", 1000, 1, "a") + pool("TM-B", 1, 1, "b"), 299)

    def test_small_pools_cannot_satisfy_the_reserve_rule(self):
        with self.assertRaises(sel.Blocked):
            sel.choose_cap(pool("TM-A", 1, 2, "a") + pool("TM-B", 1, 2, "b"), 299)

    def test_balanced_pools_choose_k5_with_a_tie_to_tm_b(self):
        result = sel.choose_cap(pool("TM-A", 60, 10, "x") + pool("TM-B", 60, 10, "y"), 299)
        self.assertEqual(result["k"], 5)
        self.assertEqual((result["n"]["TM-A"], result["n"]["TM-B"]), (149, 150))
        self.assertTrue(result["detail"]["tie_to_TM_B"])

    def test_cap_keeps_first_k_in_cap_order_and_never_pads(self):
        rows = pool("TM-B", 1, 5, "p")
        attack = rows[0]["attack_id"]
        order = sel.cap_order(FAKE_SEED, rows, 2)[attack]
        self.assertEqual([kept for _, _, kept in order], [True, True, False, False, False])
        expected = ref_order("cap", attack, [r["case_id"] for r in rows], FAKE_SEED)
        self.assertEqual([cid for _, cid, _ in order], expected)
        small = sel.cap_order(FAKE_SEED, rows, 9)[attack]
        self.assertTrue(all(kept for _, _, kept in small))


class WalkTests(unittest.TestCase):
    def test_failures_are_recorded_and_skipped_until_the_target(self):
        result = sel.walk(["a", "b", "c", "d"],
                          {"a": (False, "x"), "b": (True, "ok"), "c": (True, "ok"), "d": (True, "ok")}, 2)
        self.assertEqual(result["status"], "complete")
        self.assertEqual(result["selected"], ["b", "c"])
        self.assertEqual(result["examined"], [("a", "failed", "x"), ("b", "selected", "ok"),
                                              ("c", "selected", "ok")])

    def test_missing_decision_stops_the_walk_and_names_the_next_case(self):
        result = sel.walk(["a", "b", "c"], {"a": (True, "ok"), "c": (True, "ok")}, 2)
        self.assertEqual(result["status"], "pending")
        self.assertEqual(result["pending"], "b")
        self.assertEqual(result["selected"], ["a"])

    def test_exhaustion_is_reported_with_the_passes_found(self):
        result = sel.walk(["a", "b"], {"a": (True, "ok"), "b": (False, "no")}, 2)
        self.assertEqual(result["status"], "exhausted")
        self.assertEqual(result["selected"], ["a"])


class PartitionTests(unittest.TestCase):
    def test_cold_quota_is_floor_of_twenty_percent_plus_half(self):
        for n in range(0, 400):
            self.assertEqual(sel.cold_quota(n), math.floor(Fraction(n, 5) + Fraction(1, 2)), n)

    def test_variance_places_follow_remaining_main_cases(self):
        va, vb, _ = sel.variance_allocation(120, 119, 50)
        self.assertEqual((va, vb), (25, 25))

    def test_variance_blocks_when_a_class_has_too_few_non_cold_cases(self):
        with self.assertRaises(sel.Blocked):
            sel.variance_allocation(2, 2, 50)


class DrawTests(unittest.TestCase):
    def test_fixed_vector_output_is_pinned(self):
        status, out = run_vector()
        self.assertEqual(status, "complete")
        self.assertEqual(out["memberships"]["main"], PINNED_VECTOR_MEMBERS["main"])
        self.assertEqual(out["memberships"]["topup"], PINNED_VECTOR_MEMBERS["topup"])
        self.assertEqual(out["memberships"]["cold"], PINNED_VECTOR_MEMBERS["cold"])
        self.assertEqual(out["memberships"]["variance"], PINNED_VECTOR_MEMBERS["variance"])
        self.assertEqual(out["memberships"]["untested"], PINNED_VECTOR_MEMBERS["untested"])
        self.assertEqual(sha(sel.serialize(out)), PINNED_VECTOR_SHA256)

    def test_main_is_the_first_passes_in_hash_order_of_the_capped_set(self):
        status, out = run_vector()
        self.assertEqual(status, "complete")
        capped = {}
        for rec in out["examined"]:
            if rec["purpose"] == "cap" and rec["decision"] == "kept":
                capped.setdefault(STRATUM_OF_ATTACK[rec["group"]], []).append(rec["case_id"])
        for stratum in ("TM-A", "TM-B"):
            expected = []
            for case_id in ref_order("main", stratum, capped[stratum]):
                if VECTOR_DECISIONS[case_id][0]:
                    expected.append(case_id)
                if len(expected) == out["cap"]["n"][stratum]:
                    break
            self.assertEqual(out["memberships"]["main"][stratum], expected)

    def test_population_range_runs_from_passes_observed_to_not_yet_failed(self):
        status, out = run_vector()
        self.assertEqual(status, "complete")
        for stratum in ("TM-A", "TM-B"):
            capped = [r["case_id"] for r in out["examined"]
                      if r["purpose"] == "cap" and r["decision"] == "kept" and STRATUM_OF_ATTACK[r["group"]] == stratum]
            bounds = out["population"][stratum]
            self.assertEqual(bounds["capped"], len(capped))
            self.assertEqual(bounds["low"], sum(VECTOR_DECISIONS[c][0] for c in capped))
            self.assertEqual(bounds["high"], len(capped) - sum(not VECTOR_DECISIONS[c][0] for c in capped))
            self.assertGreaterEqual(bounds["low"], out["cap"]["n"][stratum])

    def test_cold_and_variance_are_disjoint_subsets_of_main(self):
        status, out = run_vector()
        self.assertEqual(status, "complete")
        members = out["memberships"]
        for stratum in ("TM-A", "TM-B"):
            main = members["main"][stratum]
            cold = members["cold"][stratum]
            variance = members["variance"][stratum]
            n = out["cap"]["n"][stratum]
            self.assertEqual(len(cold), math.floor(Fraction(n, 5) + Fraction(1, 2)))
            self.assertTrue(set(cold) <= set(main))
            self.assertFalse(set(variance) & set(cold))
            self.assertTrue(set(variance) <= set(main))
            self.assertEqual(cold, ref_order("cold", stratum, main)[:len(cold)])
        self.assertEqual(sum(len(v) for v in members["variance"].values()), VECTOR_VARIANCE)

    def test_topup_and_untested_follow_the_final_main_sample(self):
        status, out = run_vector()
        self.assertEqual(status, "complete")
        assert_selection_invariants(self, out, VECTOR_DECISIONS)

    def test_attack_with_only_failing_cases_is_reported_untested(self):
        # tool.f fails everywhere; TM-B keeps enough passing cases for its main sample.
        decision_map = dict(VECTOR_DECISIONS)
        for i in (1, 2, 3):
            decision_map[f"tool.f.{i}"] = (False, "no passing variant")
        status, out = run_vector(decision_map)
        self.assertEqual(status, "complete")
        self.assertIn("TM-B:tool.f", out["memberships"]["untested"])
        assert_selection_invariants(self, out, decision_map)

    def test_replacing_a_main_case_recomputes_top_up_from_the_new_main_sample(self):
        status, base = run_vector()
        victim = base["memberships"]["main"]["TM-A"][0]
        decision_map = dict(VECTOR_DECISIONS)
        decision_map[victim] = (False, "flipped to test replacement")
        status, out = run_vector(decision_map)
        self.assertEqual(status, "complete")
        self.assertNotIn(victim, out["memberships"]["main"]["TM-A"])
        assert_selection_invariants(self, out, decision_map)

    def test_exhausted_main_stratum_blocks_and_keeps_the_failures(self):
        decision_map = {k: (False, "all TM-A fail") if STRATUM_OF_ATTACK[ATTACK_OF[k]] == "TM-A" else v
                        for k, v in VECTOR_DECISIONS.items()}
        status, out = run_vector(decision_map)
        self.assertEqual(status, "blocked")
        self.assertIn("TM-A", out["blocked_reason"])
        self.assertTrue(any(r["decision"] == "failed" and r["purpose"] == "main" for r in out["examined"]))

    def test_missing_decision_needs_adaptation_and_names_the_next_case(self):
        status, base = run_vector()
        first_a = ref_order("main", "TM-A", [r["case_id"] for r in base["examined"]
                                              if r["purpose"] == "cap" and r["decision"] == "kept"
                                              and STRATUM_OF_ATTACK[r["group"]] == "TM-A"])[0]
        partial = {k: v for k, v in VECTOR_DECISIONS.items() if k != first_a}
        status, out = run_vector(partial)
        self.assertEqual(status, "needs-adaptation")
        self.assertIsNone(out["memberships"])
        nxt = [n for n in out["needs_adaptation_next"] if n["purpose"] == "main" and n["stratum"] == "TM-A"]
        self.assertEqual([n["case_id"] for n in nxt], [first_a])

    def test_variance_insufficient_capacity_blocks_completion(self):
        status, out = run_vector(variance=10)
        self.assertEqual(status, "blocked")
        self.assertIn("non-cold", out["blocked_reason"])

    def test_decisions_for_unknown_or_excluded_cases_are_refused(self):
        ledger = jsonl(VECTOR_ROWS)
        with self.assertRaises(sel.Refused):
            sel.run_draw(ledger, commitment(ledger), beacon(), decisions({"ghost.1": (True, "x")}), total=6)
        rows = VECTOR_ROWS + [row("pilot.1", "TM-A", "inj.a", excluded=True)]
        ledger = jsonl(rows)
        with self.assertRaises(sel.Refused):
            sel.run_draw(ledger, commitment(ledger), beacon(), decisions({"pilot.1": (True, "x")}), total=6)

    def test_identical_inputs_give_byte_identical_output(self):
        first = sel.serialize(run_vector()[1])
        second = sel.serialize(run_vector()[1])
        self.assertEqual(first, second)

    def test_output_is_canonical_json_without_run_timestamps(self):
        data = sel.serialize(run_vector()[1])
        self.assertEqual(data, (json.dumps(json.loads(data), sort_keys=True, indent=2) + "\n").encode("utf-8"))
        self.assertFalse(set(json.loads(data)) & {"generated_at", "timestamp", "created", "now"})

    def test_row_order_in_the_ledger_file_does_not_change_the_selection(self):
        forward = run_vector()[1]
        backward = run_vector(rows=list(reversed(VECTOR_ROWS)))[1]
        self.assertEqual(forward["memberships"], backward["memberships"])
        self.assertNotEqual(forward["inputs"]["ledger"]["sha256"], backward["inputs"]["ledger"]["sha256"])

    def test_every_examined_candidate_has_purpose_group_key_and_decision(self):
        out = run_vector()[1]
        self.assertTrue(out["examined"])
        for rec in out["examined"]:
            self.assertEqual(set(rec), {"purpose", "group", "case_id", "key", "decision", "reason"})
            self.assertEqual(len(rec["key"]), 64)


class PilotAllocationTests(unittest.TestCase):
    def test_pilot_size_is_registered_as_eleven(self):
        self.assertEqual(sel.PILOT_SIZE, 11)

    def test_split_uses_uncapped_counts_and_ties_go_to_tm_b(self):
        # TM-A: one attack with 20 cases. TM-B: 20 attacks with one case each. At cap k=1 the split would
        # be (1, 10); the uncapped counts 20 and 20 tie, so the 11 pilot places give (5, 6).
        rows = [row(f"big.{i}", "TM-A", "big") for i in range(20)]
        rows += [row(f"small.{i}", "TM-B", f"s{i}") for i in range(20)]
        ledger = jsonl(rows)
        status, out = sel.run_pilot(ledger, pilot_commitment(ledger), beacon(),
                                    decisions({r["case_id"]: (True, "ok") for r in rows}))
        self.assertEqual(status, "complete")
        self.assertEqual(out["allocation"]["candidates"], {"TM-A": 20, "TM-B": 20})
        self.assertEqual(out["allocation"]["places"], {"TM-A": 5, "TM-B": 6})
        self.assertTrue(out["allocation"]["detail"]["tie_to_TM_B"])
        self.assertEqual(out["allocation"]["detail"]["extra_place"], "TM-B")

    def test_excluded_rows_do_not_count_toward_the_split(self):
        rows = VECTOR_ROWS + [row(f"ex.{i}", "TM-A", "inj.x", excluded=True) for i in range(5)]
        status, out = run_pilot_vector(rows=rows)
        self.assertEqual(status, "complete")
        self.assertEqual(out["allocation"]["candidates"], {"TM-A": 9, "TM-B": 9})
        self.assertEqual(out["inputs"]["ledger"]["excluded"], [f"ex.{i}" for i in range(5)])

    def test_a_stratum_whose_share_rounds_to_zero_needs_no_decision(self):
        # TM-A has one candidate; its share of 11 places rounds to zero, so the walk never reaches it.
        rows = [row("a.1", "TM-A", "a1")] + [row(f"b.{i}", "TM-B", f"b{i}") for i in range(1000)]
        ledger = jsonl(rows)
        passes = {r["case_id"]: (True, "ok") for r in rows if r["stratum"] == "TM-B"}
        status, out = sel.run_pilot(ledger, pilot_commitment(ledger), beacon(), decisions(passes))
        self.assertEqual(out["allocation"]["places"], {"TM-A": 0, "TM-B": 11})
        self.assertEqual(status, "complete")
        self.assertEqual(out["memberships"]["pilot"]["TM-A"], [])

    def test_pilot_size_comes_from_the_commitment_and_must_match_the_run(self):
        ledger = jsonl(VECTOR_ROWS)
        status, out = sel.run_pilot(ledger, pilot_commitment(ledger, pilot_size=5), beacon(),
                                    decisions(VECTOR_DECISIONS), pilot_size=5)
        self.assertEqual(status, "complete")
        self.assertEqual(out["pilot_size"], 5)
        self.assertEqual(out["allocation"]["places"], {"TM-A": 2, "TM-B": 3})
        self.assertFalse(out["registered_parameters"])
        with self.assertRaisesRegex(sel.Refused, "pilot_size"):
            sel.run_pilot(ledger, pilot_commitment(ledger, pilot_size=5), beacon(), decisions(VECTOR_DECISIONS))

    def test_pilot_commitment_parses_with_its_own_fields(self):
        commit = sel.parse_commitment(pilot_commitment(jsonl(VECTOR_ROWS)))
        self.assertEqual((commit["kind"], commit["pilot_size"]), ("pilot", 11))


class PilotWalkTests(unittest.TestCase):
    def test_zero_target_walk_needs_no_decisions(self):
        result = sel.walk(["a", "b"], {}, 0)
        self.assertEqual(result["status"], "complete")
        self.assertEqual(result["selected"], [])

    def test_pilot_members_are_the_first_passes_in_pilot_order_per_stratum(self):
        status, out = run_pilot_vector()
        self.assertEqual(status, "complete")
        for stratum, n in (("TM-A", 5), ("TM-B", 6)):
            ids = [r["case_id"] for r in VECTOR_ROWS if r["stratum"] == stratum]
            expected = []
            for case_id in ref_order("pilot", stratum, ids):
                if VECTOR_DECISIONS[case_id][0]:
                    expected.append(case_id)
                if len(expected) == n:
                    break
            self.assertEqual(out["memberships"]["pilot"][stratum], expected)
            self.assertEqual(out["pilot"][stratum]["status"], "complete")

    def test_missing_pilot_decision_needs_adaptation_and_names_the_next_case(self):
        ids = [r["case_id"] for r in VECTOR_ROWS if r["stratum"] == "TM-A"]
        first = ref_order("pilot", "TM-A", ids)[0]
        partial = {k: v for k, v in VECTOR_DECISIONS.items() if k != first}
        status, out = run_pilot_vector(partial)
        self.assertEqual(status, "needs-adaptation")
        self.assertIsNone(out["memberships"])
        pending = [(n["purpose"], n["stratum"], n["case_id"]) for n in out["needs_adaptation_next"]]
        self.assertEqual(pending, [("pilot", "TM-A", first)])

    def test_exhausted_pilot_stratum_blocks_and_keeps_the_audit(self):
        failing = {k: (False, "all TM-A fail") if STRATUM_OF_ATTACK[ATTACK_OF[k]] == "TM-A" else v
                   for k, v in VECTOR_DECISIONS.items()}
        status, out = run_pilot_vector(failing)
        self.assertEqual(status, "blocked")
        self.assertIn("pilot exhausted in TM-A", out["blocked_reason"])
        self.assertTrue(any(r["purpose"] == "pilot" and r["decision"] == "failed" for r in out["examined"]))
        self.assertEqual(out["schema"], "tbound-d48-pilot/v1")


class CommitmentKindTests(unittest.TestCase):
    def setUp(self):
        self.ledger = jsonl(VECTOR_ROWS)

    def test_draw_refuses_a_pilot_commitment(self):
        with self.assertRaisesRegex(sel.Refused, "main commitment"):
            sel.run_draw(self.ledger, pilot_commitment(self.ledger), beacon(), decisions({}), total=6)

    def test_pilot_refuses_a_main_commitment(self):
        with self.assertRaisesRegex(sel.Refused, "pilot commitment"):
            sel.run_pilot(self.ledger, commitment(self.ledger), beacon(), decisions({}))

    def test_allocate_needs_a_main_commitment(self):
        with self.assertRaisesRegex(sel.Refused, "main commitment"):
            sel.allocate(self.ledger, pilot_commitment(self.ledger), total=6)

    def test_kind_must_be_exactly_pilot_or_main(self):
        for kind in (None, "Main", "draft", ""):
            obj = json.loads(commitment(self.ledger))
            if kind is None:
                del obj["kind"]
            else:
                obj["kind"] = kind
            with self.subTest(kind=kind), self.assertRaises(sel.Refused):
                sel.parse_commitment(json.dumps(obj).encode())

    def test_pilot_commitment_needs_a_positive_integer_pilot_size(self):
        for bad in (0, -1, "11", True, 1.5, None):
            obj = json.loads(pilot_commitment(self.ledger))
            obj["pilot_size"] = bad
            with self.subTest(pilot_size=bad), self.assertRaises(sel.Refused):
                sel.parse_commitment(json.dumps(obj).encode())
        obj = json.loads(pilot_commitment(self.ledger))
        del obj["pilot_size"]
        with self.assertRaises(sel.Refused):
            sel.parse_commitment(json.dumps(obj).encode())

    def test_main_commitment_needs_policy_rule_digests_and_a_development_list(self):
        for field, bad in [("policy_sha256", None), ("policy_sha256", "ab" * 31),
                           ("adaptation_rule_sha256", "zz" * 32), ("development_case_ids", "inj.a.1"),
                           ("development_case_ids", [7]), ("development_case_ids", ["inj a.1"]),
                           ("development_case_ids", ["inj.a.1", "inj.a.1"])]:
            obj = json.loads(commitment(self.ledger))
            obj[field] = bad
            with self.subTest(field=field, value=bad), self.assertRaises(sel.Refused):
                sel.parse_commitment(json.dumps(obj).encode())
        obj = json.loads(commitment(self.ledger))
        del obj["development_case_ids"]
        with self.assertRaises(sel.Refused):
            sel.parse_commitment(json.dumps(obj).encode())

    def test_empty_development_list_is_a_valid_main_commitment(self):
        commit = sel.parse_commitment(commitment(self.ledger))
        self.assertEqual(commit["kind"], "main")
        self.assertEqual(commit["development_case_ids"], [])

    def test_verify_beacon_accepts_either_kind(self):
        for data in (commitment(self.ledger), pilot_commitment(self.ledger)):
            self.assertEqual(sel.verify_beacon(data, beacon(12))["round"], 12)


class DevelopmentCaseTests(unittest.TestCase):
    def test_development_cases_leave_the_population_before_the_cap(self):
        ledger = jsonl(VECTOR_ROWS)
        dev = ["inj.a.2", "inj.a.3", "tool.d.1"]
        status, out = sel.run_draw(ledger, commitment(ledger, development=dev), beacon(), decisions(VECTOR_DECISIONS),
                                   total=VECTOR_TOTAL, variance=VECTOR_VARIANCE)
        self.assertEqual(status, "complete")
        expected = sel.choose_cap([r for r in VECTOR_ROWS if r["case_id"] not in dev], VECTOR_TOTAL)
        self.assertEqual(out["cap"]["M"], expected["M"])
        self.assertEqual(out["cap"]["k"], expected["k"])
        self.assertEqual(out["development"], {"case_ids": sorted(dev), "removed": {"TM-A": 2, "TM-B": 1}})
        examined = {r["case_id"] for r in out["examined"]}
        main = {c for group in out["memberships"]["main"].values() for c in group}
        self.assertFalse(examined & set(dev))
        self.assertFalse(main & set(dev))

    def test_allocate_applies_the_same_removal_as_the_draw(self):
        rows = pool("TM-A", 60, 10, "x") + pool("TM-B", 60, 10, "y")
        ledger = jsonl(rows)
        dev = [r["case_id"] for r in rows if r["attack_id"] == "xatt0"]
        obj = sel.allocate(ledger, commitment(ledger, development=dev))
        expected = sel.choose_cap([r for r in rows if r["attack_id"] != "xatt0"], 299)
        self.assertEqual(obj["cap"]["M"], expected["M"])
        self.assertEqual(obj["cap"]["k"], expected["k"])
        self.assertEqual(obj["development"]["removed"], {"TM-A": 10, "TM-B": 0})

    def test_unknown_or_malformed_development_ids_are_refused(self):
        ledger = jsonl(VECTOR_ROWS)
        with self.assertRaisesRegex(sel.Refused, "not in the ledger"):
            sel.run_draw(ledger, commitment(ledger, development=["inj.a.9"]), beacon(), decisions({}), total=6)
        with self.assertRaises(sel.Refused):
            sel.allocate(ledger, commitment(ledger, development=["inj a.1"]), total=6)

    def test_a_listed_excluded_case_is_accepted_and_removes_nothing(self):
        rows = VECTOR_ROWS + [row("pilot.x", "TM-A", "inj.a", excluded=True)]
        ledger = jsonl(rows)
        status, out = sel.run_draw(ledger, commitment(ledger, development=["pilot.x"]), beacon(),
                                   decisions(VECTOR_DECISIONS), total=VECTOR_TOTAL, variance=VECTOR_VARIANCE)
        self.assertEqual(status, "complete")
        self.assertEqual(out["development"], {"case_ids": ["pilot.x"], "removed": {"TM-A": 0, "TM-B": 0}})

    def test_decisions_for_development_cases_are_reported_as_not_consulted(self):
        ledger = jsonl(VECTOR_ROWS)
        status, out = sel.run_draw(ledger, commitment(ledger, development=["inj.b.1"]), beacon(),
                                   decisions(VECTOR_DECISIONS), total=VECTOR_TOTAL, variance=VECTOR_VARIANCE)
        self.assertEqual(status, "complete")
        self.assertIn("inj.b.1", out["inputs"]["adaptation"]["not_consulted"])

    def test_attack_whose_only_cases_are_development_is_untested_not_topped_up(self):
        ledger = jsonl(VECTOR_ROWS)
        status, out = sel.run_draw(ledger, commitment(ledger, development=["tool.f.1", "tool.f.2", "tool.f.3"]),
                                   beacon(), decisions(VECTOR_DECISIONS), total=VECTOR_TOTAL, variance=VECTOR_VARIANCE)
        self.assertEqual(status, "complete")
        self.assertIn("TM-B:tool.f", out["memberships"]["untested"])
        self.assertNotIn("TM-B:tool.f", {r["group"] for r in out["examined"]})
        assert_selection_invariants(self, out, VECTOR_DECISIONS)


class SeenInDevelopmentTests(unittest.TestCase):
    def test_each_attack_is_marked_by_whether_a_development_case_has_it(self):
        ledger = jsonl(VECTOR_ROWS)
        status, out = sel.run_draw(ledger, commitment(ledger, development=["inj.b.1"]), beacon(),
                                   decisions(VECTOR_DECISIONS), total=VECTOR_TOTAL, variance=VECTOR_VARIANCE)
        self.assertEqual(status, "complete")
        self.assertEqual(out["attacks"], {group: {"seen_in_development": group == "TM-A:inj.b", "stratum": STRATUM_OF_ATTACK[group]}
                                          for group in VECTOR_GROUPS})

    def test_without_development_cases_no_attack_is_marked(self):
        ledger = jsonl(VECTOR_ROWS)
        status, out = sel.run_draw(ledger, commitment(ledger), beacon(), decisions(VECTOR_DECISIONS),
                                   total=VECTOR_TOTAL, variance=VECTOR_VARIANCE)
        self.assertEqual(len(out["attacks"]), len(VECTOR_GROUPS))
        self.assertFalse(any(v["seen_in_development"] for v in out["attacks"].values()))


class PilotVectorTests(unittest.TestCase):
    def test_fixed_pilot_vector_members_are_pinned(self):
        status, out = run_pilot_vector()
        self.assertEqual(status, "complete")
        self.assertEqual(out["memberships"], {"pilot": PINNED_PILOT_MEMBERS})
        self.assertEqual(out["allocation"]["places"], {"TM-A": 5, "TM-B": 6})
        self.assertEqual(out["allocation"]["detail"]["extra_place"], "TM-B")

    def test_fixed_pilot_vector_output_is_pinned(self):
        self.assertEqual(sha(sel.serialize(run_pilot_vector()[1])), PINNED_PILOT_SHA256)

    def test_pilot_output_is_deterministic_and_canonical(self):
        first = sel.serialize(run_pilot_vector()[1])
        self.assertEqual(first, sel.serialize(run_pilot_vector()[1]))
        self.assertEqual(first, (json.dumps(json.loads(first), sort_keys=True, indent=2) + "\n").encode("utf-8"))


class CertificationTests(unittest.TestCase):
    @staticmethod
    def brute_worst(NA, NB, nA, nB):
        """Independent check: every split over one common denominator, K0 from a 60-digit U."""
        with localcontext() as ctx:
            ctx.prec = 60
            U = Decimal(1) - Decimal("0.05") ** (Decimal(1) / Decimal(nA + nB))
        N = NA + NB
        K0 = int((U * N).to_integral_value(rounding=ROUND_FLOOR)) + 1
        best = 0
        for KA in range(0, min(K0, NA) + 1):
            KB = K0 - KA
            if 0 <= KB <= NB:
                best = max(best, comb(NA - KA, nA) * comb(NB - KB, nB))
        return Fraction(best, comb(NA, nA) * comb(NB, nB))

    def test_zero_event_limit_matches_the_published_arithmetic(self):
        result = sel.certify((1000, 1000), (500, 500), 199, 100)
        self.assertAlmostEqual(float(result["U"]), 0.009969146793, places=12)

    def test_reviewer_split_probability(self):
        p = Fraction(comb(1000000 - 8400, 298) * comb(5000 - 1700, 1), comb(1000000, 298) * comb(5000, 1))
        self.assertEqual(f"{float(p):.8f}", "0.05341503")

    def test_reviewer_counterexample_is_not_certified(self):
        result = sel.certify((1000000, 1000000), (5000, 5000), 298, 1)
        self.assertFalse(result["certified"])
        self.assertGreater(result["max_P0"], 0.05)
        self.assertEqual(Fraction(result["max_P0_fraction"]), self.brute_worst(1000000, 5000, 298, 1))

    def test_small_pool_is_certified(self):
        result = sel.certify((1000, 1000), (500, 500), 199, 100)
        self.assertTrue(result["certified"])
        self.assertEqual(result["worst_case"]["K0"], 15)
        self.assertEqual(Fraction(result["max_P0_fraction"]), self.brute_worst(1000, 500, 199, 100))

    def test_range_reports_the_worst_case_over_every_size_pair(self):
        result = sel.certify((1000, 1002), (500, 501), 199, 100)
        worst = max(self.brute_worst(a, b, 199, 100) for a in (1000, 1001, 1002) for b in (500, 501))
        self.assertEqual(Fraction(result["max_P0_fraction"]), worst)
        self.assertEqual(result["certified"], worst <= Fraction(1, 20))

    def test_passing_population_smaller_than_the_sample_is_refused(self):
        with self.assertRaises(sel.Refused):
            sel.certify((100, 100), (50, 50), 199, 100)


class CommandLineTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = self.tmp.name

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, name, data):
        path = os.path.join(self.dir, name)
        with open(path, "wb") as handle:
            handle.write(data)
        return path

    @staticmethod
    def cli(*args):
        return subprocess.run([sys.executable, "-I", SCRIPT, *args], capture_output=True,
                              encoding="utf-8", timeout=600)

    def draw_cli(self, rows, decision_bytes, out_name, total=None):
        ledger = jsonl(rows)
        args = ["draw", "--ledger", self.write("l.jsonl", ledger),
                "--commitment", self.write("c.json", commitment(ledger)),
                "--beacon", self.write("b.json", beacon()),
                "--adaptation", self.write("a.jsonl", decision_bytes),
                "--out", os.path.join(self.dir, out_name)]
        if total is not None:
            args += ["--total", str(total)]
        return self.cli(*args)

    def test_draw_writes_needs_adaptation_and_exits_3(self):
        result = self.draw_cli(VECTOR_ROWS, b"", "out1.json", total=VECTOR_TOTAL)
        self.assertEqual(result.returncode, 3, result.stderr)
        with open(os.path.join(self.dir, "out1.json"), "rb") as handle:
            self.assertEqual(json.loads(handle.read())["status"], "needs-adaptation")

    def test_draw_refuses_to_overwrite_an_existing_output(self):
        self.assertEqual(self.draw_cli(VECTOR_ROWS, b"", "out2.json", total=VECTOR_TOTAL).returncode, 3)
        second = self.draw_cli(VECTOR_ROWS, b"", "out2.json", total=VECTOR_TOTAL)
        self.assertEqual(second.returncode, 1)
        self.assertIn("exists", second.stderr)

    def test_draw_with_the_registered_total_is_infeasible_for_a_tiny_ledger(self):
        result = self.draw_cli(VECTOR_ROWS, decisions(VECTOR_DECISIONS), "out3.json")
        self.assertEqual(result.returncode, 1)
        self.assertIn("infeasible", result.stderr)

    def test_allocate_prints_the_cap_for_large_balanced_pools(self):
        rows = pool("TM-A", 60, 10, "x") + pool("TM-B", 60, 10, "y")
        ledger = self.write("big.jsonl", jsonl(rows))
        result = self.cli("allocate", "--ledger", ledger,
                          "--commitment", self.write("bc.json", commitment(jsonl(rows))))
        self.assertEqual(result.returncode, 0, result.stderr)
        obj = json.loads(result.stdout)
        self.assertEqual(obj["cap"]["k"], 5)
        self.assertEqual(obj["cap"]["n"], {"TM-A": 149, "TM-B": 150})

    def test_verify_beacon_reports_bls_not_verified_and_fails_on_mismatch(self):
        commit = self.write("vc.json", commitment(jsonl(VECTOR_ROWS)))
        ok = self.cli("verify-beacon", "--commitment", commit, "--beacon", self.write("vb.json", beacon(12)))
        self.assertEqual(ok.returncode, 0, ok.stderr)
        self.assertIn("NOT VERIFIED", ok.stdout)
        bad = self.cli("verify-beacon", "--commitment", commit, "--beacon", self.write("vb2.json", beacon(11)))
        self.assertEqual(bad.returncode, 1)

    def pilot_cli(self, rows, decision_bytes, out_name, commit=None):
        ledger = jsonl(rows)
        commit_bytes = pilot_commitment(ledger) if commit is None else commit
        return self.cli("pilot", "--ledger", self.write("pl.jsonl", ledger),
                        "--commitment", self.write("pc.json", commit_bytes),
                        "--beacon", self.write("pb.json", beacon()),
                        "--adaptation", self.write("pa.jsonl", decision_bytes),
                        "--out", os.path.join(self.dir, out_name))

    def test_pilot_exit_codes_follow_the_walk(self):
        pending = self.pilot_cli(VECTOR_ROWS, b"", "p1.json")
        self.assertEqual(pending.returncode, 3, pending.stderr)
        self.assertIn("needs adaptation decision next: TM-A pilot", pending.stdout)
        done = self.pilot_cli(VECTOR_ROWS, decisions(VECTOR_DECISIONS), "p2.json")
        self.assertEqual(done.returncode, 0, done.stderr)
        failing = {k: (False, "no") if STRATUM_OF_ATTACK[ATTACK_OF[k]] == "TM-A" else v
                   for k, v in VECTOR_DECISIONS.items()}
        blocked = self.pilot_cli(VECTOR_ROWS, decisions(failing), "p3.json")
        self.assertEqual(blocked.returncode, 1)
        with open(os.path.join(self.dir, "p3.json"), "rb") as handle:
            self.assertEqual(json.loads(handle.read())["status"], "blocked")

    def test_pilot_refuses_a_main_commitment_and_a_wrong_pilot_size_without_output(self):
        ledger = jsonl(VECTOR_ROWS)
        wrong_kind = self.pilot_cli(VECTOR_ROWS, b"", "p4.json", commit=commitment(ledger))
        self.assertEqual(wrong_kind.returncode, 1)
        self.assertIn("pilot commitment", wrong_kind.stderr)
        wrong_size = self.pilot_cli(VECTOR_ROWS, b"", "p5.json", commit=pilot_commitment(ledger, pilot_size=5))
        self.assertEqual(wrong_size.returncode, 1)
        self.assertIn("pilot_size", wrong_size.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.dir, "p4.json")))
        self.assertFalse(os.path.exists(os.path.join(self.dir, "p5.json")))

    def test_draw_refuses_a_pilot_commitment_and_writes_nothing(self):
        ledger = jsonl(VECTOR_ROWS)
        result = self.cli("draw", "--ledger", self.write("kl.jsonl", ledger),
                          "--commitment", self.write("kc.json", pilot_commitment(ledger)),
                          "--beacon", self.write("kb.json", beacon()),
                          "--adaptation", self.write("ka.jsonl", b""),
                          "--out", os.path.join(self.dir, "k1.json"), "--total", str(VECTOR_TOTAL))
        self.assertEqual(result.returncode, 1)
        self.assertIn("main commitment", result.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.dir, "k1.json")))

    def test_draw_refuses_an_unknown_development_id_and_writes_nothing(self):
        ledger = jsonl(VECTOR_ROWS)
        result = self.cli("draw", "--ledger", self.write("ul.jsonl", ledger),
                          "--commitment", self.write("uc.json", commitment(ledger, development=["inj.a.9"])),
                          "--beacon", self.write("ub.json", beacon()),
                          "--adaptation", self.write("ua.jsonl", b""),
                          "--out", os.path.join(self.dir, "u1.json"), "--total", str(VECTOR_TOTAL))
        self.assertEqual(result.returncode, 1)
        self.assertIn("not in the ledger", result.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.dir, "u1.json")))

    def test_certify_exit_code_follows_the_certification(self):
        fail = self.cli("certify", "--NA", "1000000", "--NB", "5000", "--nA", "298", "--nB", "1")
        self.assertEqual(fail.returncode, 1)
        self.assertFalse(json.loads(fail.stdout)["certified"])
        ok = self.cli("certify", "--NA", "1000", "--NB", "500", "--nA", "199", "--nB", "100")
        self.assertEqual(ok.returncode, 0)
        self.assertTrue(json.loads(ok.stdout)["certified"])


if __name__ == "__main__":
    unittest.main()
