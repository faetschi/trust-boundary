"""Tests for t1_prepare.py. Run from the repository root: py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v"""
import hashlib
import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


prep, sel = load("t1_prepare_under_test", "t1_prepare.py"), load("select_under_test", "select.py")
TEMPLATE_TEXT = pathlib.Path(ROOT, prep.TEMPLATE).read_text(encoding="utf-8")
OBJECT, COMMIT = "a" * 40, "b" * 40


class Fill(unittest.TestCase):
    def test_only_the_two_values_change(self):
        out = prep.fill(TEMPLATE_TEXT, OBJECT, COMMIT)
        self.assertEqual(out.replace(OBJECT, "X").replace(COMMIT, "Y"), TEMPLATE_TEXT.replace(prep.OBJECT_FILL, '"X"').replace(prep.COMMIT_FILL, '"Y"'))
        self.assertEqual(json.loads(out)["signed_tag"], {"commit": COMMIT, "name": "corpus-d48-ledger-v2", "object": OBJECT})

    def test_refuses_a_template_with_another_fill(self):
        with self.assertRaises(SystemExit):
            prep.fill(TEMPLATE_TEXT.replace('"kind": "pilot"', '"kind": "FILL"'), OBJECT, COMMIT)

    def test_refuses_a_missing_fill(self):
        with self.assertRaises(SystemExit):
            prep.fill(prep.fill(TEMPLATE_TEXT, OBJECT, COMMIT), OBJECT, COMMIT)


class Digests(unittest.TestCase):
    def test_committed_template_matches_its_files(self):
        commitment = json.loads(prep.fill(TEMPLATE_TEXT, OBJECT, COMMIT))
        blob = lambda path: pathlib.Path(ROOT, path).read_bytes()
        self.assertEqual(prep.check_digests(commitment, blob), 7)

    def test_a_changed_file_is_refused(self):
        commitment = json.loads(prep.fill(TEMPLATE_TEXT, OBJECT, COMMIT))
        with self.assertRaises(SystemExit):
            prep.check_digests(commitment, lambda path: b"changed")


class Round(unittest.TestCase):
    def test_round_equals_select_py(self):
        chain = json.loads(TEMPLATE_TEXT)["chain"]
        for t1 in ("2026-11-12T10:00:00Z", "2026-11-12T10:00:01Z", "2026-12-31T23:59:59Z", "2027-01-01T00:00:00Z"):
            t1_epoch = prep.parse_utc(t1)
            r, scheduled = prep.round_of(chain, t1_epoch)
            self.assertEqual(r, sel.round_for(chain["genesis_time"], chain["period"], t1_epoch))
            self.assertGreaterEqual(scheduled, t1_epoch + 24 * 3600)
            self.assertLess(scheduled - chain["period"], t1_epoch + 24 * 3600)

    def test_fractions_are_refused(self):
        with self.assertRaises(SystemExit):
            prep.parse_utc("2026-11-12T10:00:00.5Z")


class Record(unittest.TestCase):
    def test_created_rounds_up_to_the_next_second(self):
        self.assertEqual(prep.created_to_t1("2026-11-11T19:57:09.863454+00:00"), "2026-11-11T19:57:10Z")
        self.assertEqual(prep.created_to_t1("2026-11-11T19:57:09+00:00"), "2026-11-11T19:57:09Z")
        self.assertEqual(prep.created_to_t1("2026-11-11T19:59:59.000001+00:00"), "2026-11-11T20:00:00Z")

    def test_record_must_hold_exactly_the_local_file(self):
        record = {"created": "2026-11-11T19:57:09.5+00:00", "files": [{"key": "pilot-commitment-v1.json"}]}
        self.assertEqual(prep.check_record(record, b"same", b"same"), "2026-11-11T19:57:10Z")
        with self.assertRaises(SystemExit):
            prep.check_record(record, b"other", b"same")
        with self.assertRaises(SystemExit):
            prep.check_record({**record, "files": record["files"] * 2}, b"same", b"same")


class Build(unittest.TestCase):
    def test_a_lightweight_tag_is_refused(self):
        with tempfile.TemporaryDirectory() as repo:
            git = lambda *a: subprocess.run(["git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", *a], check=True, capture_output=True)
            git("init", "-q")
            git("commit", "-q", "--allow-empty", "-m", "x")
            git("tag", "light")
            with self.assertRaises(SystemExit):
                prep.build(type("A", (), {"repo": repo, "tag": "light", "out": os.path.join(repo, "out")})())


if __name__ == "__main__":
    unittest.main()
