"""Tests for corpus/sampling/round_plan.py. The inputs are real select.py outputs of the test vectors in test_select.py
(synthetic ids only).

Run from the repository root: py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py"
"""
import contextlib
import io
import json
import os
import tempfile
import unittest

import round_plan as rp
import test_select as ts

BIG_ROWS = ts.pool("TM-A", 4, 5, "a") + ts.pool("TM-B", 4, 5, "b")


def capped(out, stratum):
    return [r["case_id"] for r in out["examined"]
            if r["purpose"] == "cap" and r["decision"] == "kept" and out["attacks"][r["group"]]["stratum"] == stratum]


class DrawPlan(unittest.TestCase):
    def test_lists_the_main_order_beyond_the_named_case_up_to_the_slots(self):
        _, out = ts.run_vector({}, rows=BIG_ROWS)
        plan = rp.plan(out, {})
        self.assertEqual(plan["TM-A"], ts.ref_order("main", "TM-A", capped(out, "TM-A"))[:7])
        self.assertEqual(plan["TM-B"], ts.ref_order("main", "TM-B", capped(out, "TM-B"))[:4])
        named = {n["stratum"]: n["case_id"] for n in out["needs_adaptation_next"]}
        self.assertEqual({h: plan[h][0] for h in rp.STRATA}, named)

    def test_a_pool_smaller_than_the_slots_lists_every_candidate(self):
        _, out = ts.run_vector({})
        self.assertEqual(len(rp.plan(out, {})["TM-A"]), len(capped(out, "TM-A")))

    def test_decided_and_screened_cases_are_skipped(self):
        _, out = ts.run_vector({}, rows=BIG_ROWS)
        order = ts.ref_order("main", "TM-A", capped(out, "TM-A"))
        decisions = {order[0]: (False, "meaning not preserved"), order[1]: (False, "x")}
        plan = rp.plan(out, decisions, {order[2], order[4]})
        self.assertEqual(plan["TM-A"], [order[3], order[5], order[6], order[7]])

    def test_only_a_stratum_with_a_pending_case_is_filled(self):
        _, out = ts.run_vector({}, rows=BIG_ROWS)
        order = ts.ref_order("main", "TM-A", capped(out, "TM-A"))
        full = {c: (True, "ok") for c in order[:3]}  # the TM-A main sample has 3 places
        plan = rp.plan(out, full)
        self.assertEqual(plan["TM-A"], [])
        self.assertEqual(len(plan["TM-B"]), 4)

    def test_a_decision_for_the_named_case_moves_the_plan_without_a_new_output(self):
        _, out = ts.run_vector({}, rows=BIG_ROWS)
        order = ts.ref_order("main", "TM-B", capped(out, "TM-B"))
        plan = rp.plan(out, {order[0]: (True, "ok")})
        self.assertEqual(plan["TM-B"], order[1:5])

    def test_plan_matches_the_pending_case_of_a_partly_decided_walk(self):
        _, base = ts.run_vector()
        order = ts.ref_order("main", "TM-A", capped(base, "TM-A"))
        partial = {k: v for k, v in ts.VECTOR_DECISIONS.items() if k not in order[1:]}
        _, out = ts.run_vector(partial)
        named = [n["case_id"] for n in out["needs_adaptation_next"] if n["stratum"] == "TM-A" and n["purpose"] == "main"]
        self.assertEqual(rp.plan(out, partial)["TM-A"][0], named[0])


class TopUpPlan(unittest.TestCase):
    def setUp(self):
        self.decisions = {k: v for k, v in ts.VECTOR_DECISIONS.items() if not k.startswith("tool.f.")}
        _, self.out = ts.run_vector(self.decisions)
        self.kept_f = [r["case_id"] for r in self.out["examined"]
                       if r["purpose"] == "cap" and r["decision"] == "kept" and r["group"] == "TM-B:tool.f"]

    def test_top_up_lists_the_attack_without_a_main_case_once_main_is_final(self):
        self.assertEqual([n["purpose"] for n in self.out["needs_adaptation_next"]], ["topup"])
        plan = rp.plan(self.out, self.decisions)
        self.assertEqual(plan, {"TM-A": [], "TM-B": [ts.ref_order("topup", "TM-B:tool.f", self.kept_f)[0]]})

    def test_a_screened_top_up_case_gives_way_to_the_next_in_order(self):
        order = ts.ref_order("topup", "TM-B:tool.f", self.kept_f)
        self.assertEqual(rp.plan(self.out, self.decisions, {order[0]})["TM-B"], [order[1]])

    def test_a_passing_top_up_case_completes_the_attack(self):
        order = ts.ref_order("topup", "TM-B:tool.f", self.kept_f)
        self.assertEqual(rp.plan(self.out, {**self.decisions, order[0]: (True, "ok")}), {"TM-A": [], "TM-B": []})

    def test_no_top_up_while_a_main_stratum_is_pending(self):
        _, out = ts.run_vector({}, rows=BIG_ROWS)
        every = {r["case_id"] for r in out["examined"] if r["purpose"] == "cap"}
        order = ts.ref_order("main", "TM-A", capped(out, "TM-A"))
        plan = rp.plan(out, {c: (True, "ok") for c in order[:3]})
        self.assertTrue(set(plan["TM-B"]) <= every and len(plan["TM-B"]) == 4)


class PilotPlan(unittest.TestCase):
    def test_a_pilot_output_is_refused_not_planned_partially(self):
        _, out = ts.run_pilot_vector({})
        with self.assertRaises(rp.Refused):
            rp.plan(out, {})


class Cli(unittest.TestCase):
    def run_cli(self, out, decisions, screened=None):
        with tempfile.TemporaryDirectory() as tmp:
            paths = {}
            for name, text in (("out.json", json.dumps(out)), ("d.jsonl", "".join(json.dumps(d) + "\n" for d in decisions)),
                               ("s.txt", "".join(c + "\n" for c in screened or ()) + "\n")):
                paths[name] = os.path.join(tmp, name)
                with open(paths[name], "w", encoding="utf-8", newline="") as fh:
                    fh.write(text)
            argv = ["plan", "--output", paths["out.json"], "--decisions", paths["d.jsonl"]]
            if screened is not None:
                argv += ["--screened", paths["s.txt"]]
            stdout, stderr = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                code = rp.main(argv)
            return code, stdout.getvalue(), stderr.getvalue()

    def test_prints_one_json_object_of_ids(self):
        _, out = ts.run_vector({}, rows=BIG_ROWS)
        order = ts.ref_order("main", "TM-A", capped(out, "TM-A"))
        code, text, _ = self.run_cli(out, [{"case_id": order[0], "pass": False, "reason": "F-MEANING: x"}], [order[1]])
        self.assertEqual(code, 0)
        self.assertTrue(text.endswith("\n") and text.count("\n") == 1)
        self.assertEqual(json.loads(text)["TM-A"], order[2:9])

    def test_refuses_a_file_that_is_not_a_select_output(self):
        code, text, err = self.run_cli({"schema": "other"}, [])
        self.assertEqual((code, text), (2, ""))
        self.assertIn("not a select.py", err)

    def test_refuses_a_bad_decision_line(self):
        _, out = ts.run_vector({})
        with tempfile.TemporaryDirectory() as tmp:
            o, d = os.path.join(tmp, "o.json"), os.path.join(tmp, "d.jsonl")
            json.dump(out, open(o, "w"))
            open(d, "w").write("not json\n")
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(rp.main(["plan", "--output", o, "--decisions", d]), 2)


if __name__ == "__main__":
    unittest.main()
