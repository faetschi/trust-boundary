import json
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


class T2DevelopmentAdditions(unittest.TestCase):
    def test_every_addition_is_a_ledger_row_with_a_reason(self):
        with open(os.path.join(HERE, "t2-development-additions.json"), encoding="utf-8") as f:
            rows = json.load(f)["rows"]
        with open(os.path.join(HERE, "d48-ledger-v3.jsonl"), encoding="utf-8") as f:
            ids = {json.loads(line)["case_id"] for line in f}
        self.assertEqual(len({r["case_id"] for r in rows}), len(rows))
        for r in rows:
            self.assertIn(r["case_id"], ids)  # never print the row: it may be a candidate
            self.assertRegex(r["date"], r"^\d{4}-\d{2}-\d{2}$")
            self.assertTrue(r["reason"].strip())


if __name__ == "__main__":
    unittest.main()
