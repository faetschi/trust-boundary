"""The T1 template binds each file by SHA-256; this test keeps the recorded digests equal to the committed bytes.

Run from the repository root: py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v
"""
import hashlib
import json
import os
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
TEMPLATE = os.path.join(ROOT, "corpus", "sampling", "pilot-commitment-v1.template.json")


class PilotTemplate(unittest.TestCase):
    def test_recorded_digests_equal_file_digests(self):
        with open(TEMPLATE, encoding="utf-8") as fh:
            template = json.load(fh)
        pairs = [k[: -len("_sha256")] for k in template if k.endswith("_sha256")]
        self.assertGreaterEqual(len(pairs), 6)
        for name in pairs:
            with open(os.path.join(ROOT, template[name + "_path"]), "rb") as fh:
                self.assertEqual(hashlib.sha256(fh.read()).hexdigest(), template[name + "_sha256"], name)

    def test_only_the_owner_tag_fields_are_unfilled(self):
        with open(TEMPLATE, encoding="utf-8") as fh:
            text = fh.read()
        self.assertEqual(text.count("FILL"), 2)
        self.assertEqual(sorted(json.loads(text)["signed_tag"]), ["commit", "name", "object"])


if __name__ == "__main__":
    unittest.main()
