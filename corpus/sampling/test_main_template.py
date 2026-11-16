"""The T2 template binds each file by SHA-256 and holds exactly the fields select.py reads from a main commitment.

Run from the repository root: py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v
"""
import hashlib
import importlib.util
import json
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
TEMPLATE = os.path.join(HERE, "main-commitment-v1.template.json")
LEDGER = os.path.join(HERE, "d48-ledger-v3.jsonl")
# Decided later (see the runbook gaps G17, G19, G18): each FILL, with the JSON path it sits at.
FILLS = ["adaptation_rule_sha256", "development_case_ids", "policy_sha256",
         "signed_tag.commit", "signed_tag.name", "signed_tag.object"]
SELECT_READS = {"kind", "ledger_sha256", "script_sha256", "chain", "policy_sha256",
                "adaptation_rule_sha256", "development_case_ids"}

spec = importlib.util.spec_from_file_location("select_under_test", os.path.join(HERE, "select.py"))
sel = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sel)


def load():
    with open(TEMPLATE, encoding="utf-8") as fh:
        return json.load(fh)


def fills_of(template):
    found = []
    for key, value in template.items():
        if isinstance(value, dict):
            found += [f"{key}.{k}" for k, v in value.items() if isinstance(v, str) and v.startswith("FILL")]
        elif isinstance(value, str) and value.startswith("FILL"):
            found.append(key)
    return sorted(found)


def valid(template, dev_ids):
    """The template with every FILL replaced by a valid synthetic value."""
    out = json.loads(json.dumps(template))
    out["adaptation_rule_sha256"] = "1" * 64
    out["policy_sha256"] = "2" * 64
    out["development_case_ids"] = dev_ids
    out["signed_tag"] = {"commit": "b" * 40, "name": "t", "object": "a" * 40}
    return json.dumps(out).encode()


class MainTemplate(unittest.TestCase):
    def test_recorded_digests_equal_file_digests(self):
        template = load()
        pairs = [k[: -len("_sha256")] for k in template if k.endswith("_sha256") and not template[k].startswith("FILL")]
        self.assertEqual(sorted(pairs), ["agentdojo_vectors", "asb_goal_class", "asb_vectors", "ledger", "script", "source_digests"])
        for name in pairs:
            with open(os.path.join(ROOT, template[name + "_path"]), "rb") as fh:
                self.assertEqual(hashlib.sha256(fh.read()).hexdigest(), template[name + "_sha256"], name)

    def test_only_the_listed_fields_are_unfilled(self):
        with open(TEMPLATE, encoding="utf-8") as fh:
            text = fh.read()
        self.assertEqual(fills_of(json.loads(text)), FILLS)
        self.assertEqual(text.count("FILL"), len(FILLS))

    def test_fixed_fields_equal_the_t1_template(self):
        with open(os.path.join(HERE, "pilot-commitment-v1.template.json"), encoding="utf-8") as fh:
            pilot = json.load(fh)
        main = load()
        for key in ("chain", "unit_choices", "ledger_sha256", "script_sha256", "source_digests_sha256"):
            self.assertEqual(main[key], pilot[key], key)
        self.assertEqual(main["kind"], "main")
        self.assertNotIn("pilot_size", main)
        self.assertIn("T2", main["round_rule"])
        self.assertNotIn("T1", main["round_rule"])

    def test_select_reads_exactly_these_fields_and_all_are_in_the_template(self):
        parsed = sel.parse_commitment(valid(load(), []))
        self.assertEqual(set(parsed), SELECT_READS)
        self.assertLessEqual(SELECT_READS, set(load()))
        self.assertNotIn("T", load())


class SelectAccepts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with open(LEDGER, "rb") as fh:
            cls.ledger = fh.read()
        with open(LEDGER, encoding="utf-8") as fh:
            # development rows only: their ids are not candidates, so reading them breaks no rule
            cls.dev_rows = [r["case_id"] for r in map(json.loads, fh) if r["status"] == "development"][:3]

    def test_allocate_accepts_the_filled_template_and_removes_development_rows(self):
        dev = self.dev_rows + ["PILOT-ADV-A-01"]
        out = sel.allocate(self.ledger, valid(load(), dev))
        self.assertEqual(out["development"]["case_ids"], sorted(dev))
        self.assertEqual(out["development_not_in_ledger"], ["PILOT-ADV-A-01"])
        self.assertGreater(out["cap"]["k"], 0)

    def test_allocate_refuses_what_the_validation_path_refuses(self):
        for bad in (["has space"], ["x", "x"], "not-a-list"):
            with self.assertRaises(sel.Refused, msg=str(bad)):
                sel.allocate(self.ledger, valid(load(), bad))
        unfilled = json.dumps(load()).encode()
        with self.assertRaises(sel.Refused):
            sel.allocate(self.ledger, unfilled)
        pilot = json.loads(valid(load(), []))
        pilot.update(kind="pilot", pilot_size=11)
        with self.assertRaises(sel.Refused):
            sel.allocate(self.ledger, json.dumps(pilot).encode())

    def test_a_changed_ledger_digest_is_refused(self):
        template = load()
        template["ledger_sha256"] = "0" * 64
        with self.assertRaises(sel.Refused):
            sel.allocate(self.ledger, valid(template, []))


if __name__ == "__main__":
    unittest.main()
