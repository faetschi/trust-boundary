"""Tests for corpus/sampling/asb_goal_class.py (D51 item 21, adaptation rule v1 section 3c).

Run from the repository root: py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v
"""
import json
import os
import unittest

from asb_goal_class import classify

HERE = os.path.dirname(os.path.abspath(__file__))


def lines(name):
    with open(os.path.join(HERE, name), encoding="utf-8") as fh:
        return [json.loads(ln) for ln in fh if ln.strip()]


class GoalClass(unittest.TestCase):
    def test_disclosure_verbs(self):
        for verb in ("steal", "extract", "exfiltrate", "leak", "expose", "share", "send", "disclose", "export"):
            self.assertEqual(classify(f"Successfully {verb} the patient file."), "A05", verb)

    def test_reading_verbs_are_not_an_effect(self):
        for verb in ("retrieve", "access", "download", "review", "inspect", "obtain", "gather"):
            self.assertIsNone(classify(f"Successfully {verb} confidential client records."), verb)

    def test_alteration_needs_a_stored_record(self):
        self.assertEqual(classify("Successfully update the stored advice records."), "B02")
        self.assertIsNone(classify("Successfully manipulate the peer review process to influence publication outcomes."))
        self.assertIsNone(classify("Successfully manipulate DNS records, redirecting users."))
        self.assertIsNone(classify("Successfully create alternative financial data."))  # "alternative" is not "alter"

    def test_object_phrase_ends_at_the_clause_break(self):
        self.assertIsNone(classify("Successfully corrupt vehicle control systems to induce data loss."))

    def test_removal_and_encryption(self):
        self.assertEqual(classify("Successfully delete the customer database."), "A04")
        self.assertEqual(classify("Successfully deploy ransomware, encrypting files."), "EB01")
        self.assertEqual(classify("Successfully corrupt or remove academic records."), "B02")

    def test_both_effects(self):
        self.assertEqual(classify("Successfully exfiltrate and alter research data."), "E-CONFERENCE")

    def test_flagged_vector_goals_match_the_ledger(self):
        key = {r["case_id"]: r["attack_id"] for r in lines("d48-ledger-v3.jsonl") if r["source"] == "ASB"}
        flagged = [v for v in lines("asb-template-vectors-v1.jsonl") if v["flagged"]]
        self.assertEqual(len(flagged), 61)
        for v in flagged:
            self.assertEqual(classify(v["goal"]), key[v["case_id"]], v["case_id"])


if __name__ == "__main__":
    unittest.main()
