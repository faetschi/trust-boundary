"""Tests for dev_ids.py. Run from the repository root: py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v"""
import contextlib
import importlib.util
import io
import json
import os
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


dev, sel = load("dev_ids_under_test", "dev_ids.py"), load("select_under_test_dev", "select.py")
LEDGER = os.path.join(HERE, "d48-ledger-v3.jsonl")
ADDITIONS = os.path.join(HERE, "t2-development-additions.json")
CARRIERS = os.path.join(HERE, "carriers")
T5 = os.path.join(ROOT, "corpus", "pilot", "t5-2026-10-29", "manifest.draft.json")


class Fixture(unittest.TestCase):
    """Synthetic inputs: ledger rows a1..a3 (TM-A), b1..b3 (TM-B)."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = tmp.name
        self.ledger = self.write("ledger.jsonl", "".join(
            json.dumps({"case_id": c, "stratum": "TM-A" if c[0] == "a" else "TM-B"}) + "\n"
            for c in ("a1", "a2", "a3", "a4", "b1", "b2", "b3")))
        self.pilot = self.write("pilot.json", json.dumps({
            "schema": sel.PILOT_SCHEMA, "memberships": {"pilot": {"TM-A": ["a1"], "TM-B": ["b1"]}},
            "examined": [{"case_id": "a1"}, {"case_id": "a2"}, {"case_id": "b1"}]}))
        os.makedirs(os.path.join(self.dir, "carriers", "c1"))
        os.makedirs(os.path.join(self.dir, "carriers", "c2"))
        self.write("carriers/c1/manifest.draft.json", json.dumps({"cases": [{"case_id": "b2"}, {"case_id": "T5-ONLY-1"}]}))
        self.write("carriers/c2/manifest.draft.json", json.dumps({"cases": [{"case_id": "b2"}, {"case_id": "a3"}]}))
        self.t5 = self.write("t5.json", json.dumps({"cases": [{"case_id": "PILOT-ADV-A-01"}, {"case_id": "b3"}]}))
        self.additions = self.write("additions.json", json.dumps({"rows": [{"case_id": "b3"}]}))
        self.render_log = self.write("render-log.jsonl", '{"case_id":"a2"}\n{"case_id":"a2"}\n')
        self.out = os.path.join(self.dir, "out.json")

    def write(self, name, text):
        path = os.path.join(self.dir, name)
        with open(path, "w", encoding="utf-8", newline="\n") as fh:
            fh.write(text)
        return path

    def run_build(self, **override):
        args = {"pilot-output": self.pilot, "carriers": os.path.join(self.dir, "carriers"), "t5": self.t5,
                "additions": self.additions, "render-log": self.render_log, "ledger": self.ledger, "out": self.out, **override}
        argv = ["build"] + [x for k, v in args.items() for x in (f"--{k}", v)]
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            dev.main(argv)
        return buf.getvalue()

    def ids(self):
        with open(self.out, encoding="utf-8") as fh:
            return json.load(fh)


class Join(Fixture):
    def test_the_output_is_the_sorted_union_of_every_source(self):
        self.run_build()
        self.assertEqual(self.ids(), ["PILOT-ADV-A-01", "a1", "a2", "a3", "b1", "b2", "b3"])

    def test_a_rendered_but_undecided_candidate_joins(self):
        self.run_build(**{"render-log": self.write("log2.jsonl", '{"case_id":"a4"}\n')})
        self.assertIn("a4", self.ids())

    def test_a_render_log_id_that_is_not_a_ledger_row_is_refused(self):
        with self.assertRaises(SystemExit):
            self.run_build(**{"render-log": self.write("log3.jsonl", '{"case_id":"zz9"}\n')})

    def test_a_carrier_case_that_is_not_a_ledger_row_is_left_out(self):
        self.run_build()
        self.assertNotIn("T5-ONLY-1", self.ids())

    def test_a_t5_id_that_is_not_a_ledger_row_is_kept(self):
        self.run_build()
        self.assertIn("PILOT-ADV-A-01", self.ids())

    def test_output_is_lf_and_accepted_by_select(self):
        self.run_build()
        with open(self.out, "rb") as fh:
            raw = fh.read()
        self.assertNotIn(b"\r", raw)
        self.assertEqual(sel._id_list({"x": json.loads(raw)}, "x"), self.ids())

    def test_counts_only_per_source_and_stratum(self):
        printed = self.run_build()
        self.assertIn("union: total 7, TM-A 3, TM-B 3, not in ledger 1", printed)
        self.assertIn("pilot examined: total 3, TM-A 2, TM-B 1", printed)
        self.assertIn("carriers (2): total 2, TM-A 1, TM-B 1, not in ledger 0, dropped, not ledger rows 1", printed)
        self.assertIn("T5: total 2, TM-A 0, TM-B 1, not in ledger 1", printed)
        for case_id in self.ids():
            self.assertNotIn(case_id, printed)


class Refusals(Fixture):
    def test_an_existing_output_is_refused_and_kept(self):
        self.write("out.json", "keep")
        with self.assertRaises(SystemExit):
            self.run_build()
        with open(self.out, encoding="utf-8") as fh:
            self.assertEqual(fh.read(), "keep")

    def test_bad_grammar_is_refused_in_every_source(self):
        bad = json.dumps({"cases": [{"case_id": "has space"}]})
        for name, path in (("t5", self.write("bad.json", bad)),):
            with self.assertRaises(SystemExit, msg=name):
                self.run_build(**{name: path})
        self.write("carriers/c2/manifest.draft.json", bad)
        with self.assertRaises(SystemExit):
            self.run_build()
        self.assertFalse(os.path.exists(self.out))

    def test_an_additions_id_that_is_not_a_ledger_row_is_refused(self):
        with self.assertRaises(SystemExit):
            self.run_build(additions=self.write("add2.json", json.dumps({"rows": [{"case_id": "zz9"}]})))

    def test_a_pilot_id_that_is_not_a_ledger_row_is_refused(self):
        with open(self.pilot, encoding="utf-8") as fh:
            pilot = json.load(fh)
        pilot["examined"].append({"case_id": "zz9"})
        with self.assertRaises(SystemExit):
            self.run_build(**{"pilot-output": self.write("pilot2.json", json.dumps(pilot))})

    def test_a_pilot_output_that_is_not_complete_is_refused(self):
        with self.assertRaises(SystemExit):
            self.run_build(**{"pilot-output": self.write("pilot3.json", json.dumps({"schema": sel.PILOT_SCHEMA, "examined": []}))})

    def test_a_carriers_directory_without_carriers_is_refused(self):
        os.makedirs(os.path.join(self.dir, "empty"))
        with self.assertRaises(SystemExit):
            self.run_build(carriers=os.path.join(self.dir, "empty"))


class RealInputs(Fixture):
    def test_rehearsal_carrier_and_additions_join_the_real_ledger(self):
        with open(os.path.join(CARRIERS, "rehearsal-dev-01", "manifest.draft.json"), encoding="utf-8") as fh:
            carrier = {c["case_id"] for c in json.load(fh)["cases"]}
        with open(LEDGER, encoding="utf-8") as fh:
            rows = {r["case_id"]: r["status"] for r in map(json.loads, fh)}
        in_ledger = {c for c in carrier if c in rows}
        self.assertTrue(in_ledger)
        self.assertEqual({rows[c] for c in in_ledger}, {"development"})  # no candidate row is involved
        pilot = self.write("p.json", json.dumps({"schema": sel.PILOT_SCHEMA, "memberships": {"pilot": {}}, "examined": []}))
        printed = self.run_build(carriers=CARRIERS, ledger=LEDGER, additions=ADDITIONS, t5=T5,
                                 **{"pilot-output": pilot, "render-log": self.write("empty-log.jsonl", "")})
        ids = set(self.ids())
        with open(ADDITIONS, encoding="utf-8") as fh:
            added = {r["case_id"] for r in json.load(fh)["rows"]}
        self.assertEqual(ids - {c for c in ids if c not in rows}, in_ledger | added)
        self.assertIn("PILOT-ADV-A-01", ids)  # T5 ids outside the ledger are kept
        self.assertNotIn("agentdojo:", printed)


if __name__ == "__main__":
    unittest.main()
