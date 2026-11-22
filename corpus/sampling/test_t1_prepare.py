"""Tests for t1_prepare.py. Run from the repository root: py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v"""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import subprocess
import sys
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
        self.assertEqual(json.loads(out)["signed_tag"], {"commit": COMMIT, "name": "h1-sampling-frame", "object": OBJECT})

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


MAIN_TEXT = pathlib.Path(ROOT, prep.MAIN_TEMPLATE).read_text(encoding="utf-8")
RULE_V1 = "corpus/sampling/adaptation-rule-v1.md"


def main_args(tmp, ids, *, rule_path=None):
    """Arguments of `build --kind main`, with a policy file and a development list written under tmp."""
    policy, dev = os.path.join(tmp, "policy.bin"), os.path.join(tmp, "dev.json")
    pathlib.Path(policy).write_bytes(b"policy bytes")
    pathlib.Path(dev).write_text(json.dumps(ids), encoding="utf-8")
    return type("A", (), {"policy_file": policy, "dev_ids": dev, "rule_path": rule_path})()


class FillMain(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.asked = []

        def blob(path):
            self.asked.append(path)
            return pathlib.Path(ROOT, path).read_bytes() if path != prep.MAIN_TEMPLATE else MAIN_TEXT.encode("utf-8")

        self.blob = blob

    def fill(self, ids, **kw):
        return prep.fill_main(self.blob, main_args(self.tmp.name, ids, **kw), "tag-x", OBJECT, COMMIT)

    def test_every_fill_is_replaced_by_its_input(self):
        text = self.fill(["b:2", "a:1"])
        commitment = json.loads(text)
        self.assertNotIn("FILL", text)
        self.assertEqual(commitment["development_case_ids"], ["a:1", "b:2"])
        self.assertEqual(commitment["signed_tag"], {"commit": COMMIT, "name": "tag-x", "object": OBJECT})
        self.assertEqual(commitment["policy_sha256"], hashlib.sha256(b"policy bytes").hexdigest())
        self.assertEqual(commitment["adaptation_rule_sha256"], hashlib.sha256(pathlib.Path(ROOT, RULE_V1).read_bytes()).hexdigest())
        self.assertEqual(prep.check_digests(commitment, self.blob), 7)
        self.assertEqual(sel.parse_commitment(text.encode("utf-8"))["development_case_ids"], ["a:1", "b:2"])

    def test_only_the_filled_values_differ_from_the_template(self):
        text = self.fill(["a:1"])
        self.assertEqual({k for k, v in json.loads(text).items() if v != json.loads(MAIN_TEXT)[k]},
                         {"adaptation_rule_sha256", "policy_sha256", "development_case_ids", "signed_tag"})

    def test_the_rule_digest_comes_from_the_chosen_rule_file(self):
        out = json.loads(self.fill(["a:1"], rule_path="corpus/sampling/select.py"))
        self.assertEqual(out["adaptation_rule_path"], "corpus/sampling/select.py")
        self.assertEqual(out["adaptation_rule_sha256"], hashlib.sha256(pathlib.Path(ROOT, "corpus/sampling/select.py").read_bytes()).hexdigest())
        self.assertIn("corpus/sampling/select.py", self.asked)

    def test_an_empty_development_list_is_refused(self):
        with self.assertRaises(SystemExit):
            self.fill([])


class BuildMain(unittest.TestCase):
    """The whole build against a temporary repository. verify-tag is stubbed: signing needs the owner's key."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = os.path.join(self.tmp.name, "repo")
        self.out = os.path.join(self.tmp.name, "out")
        self.args = type("A", (), {"repo": self.repo, "tag": "t2-tag", "out": self.out, "kind": "main", "rule_path": None})()
        self.args.policy_file = os.path.join(self.tmp.name, "policy.bin")
        self.args.dev_ids = os.path.join(self.tmp.name, "dev.json")
        pathlib.Path(self.args.policy_file).write_bytes(b"policy bytes")
        pathlib.Path(self.args.dev_ids).write_text(json.dumps(["a:1"]), encoding="utf-8")
        real = prep.git
        prep.git = lambda repo, *a, **kw: "" if a[0] == "verify-tag" else real(repo, *a, **kw)
        self.addCleanup(setattr, prep, "git", real)

    def tag(self, changes=()):
        """Commit the committed files (with `changes` as path -> bytes) and tag them with an annotated tag."""
        commitment = json.loads(MAIN_TEXT)
        paths = [prep.MAIN_TEMPLATE] + [commitment[k] for k in commitment if k.endswith("_path")]
        for path in paths:
            target = pathlib.Path(self.repo, path)
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(dict(changes).get(path, pathlib.Path(ROOT, path).read_bytes()))
        git = lambda *a: subprocess.run(["git", "-C", self.repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "core.autocrlf=false", *a], check=True, capture_output=True)
        git("init", "-q")
        git("add", ".")
        git("commit", "-q", "-m", "x")
        git("tag", "-a", "-m", "x", "t2-tag")

    def test_builds_the_main_commitment_and_never_overwrites(self):
        self.tag()
        prep.build(self.args)
        written = pathlib.Path(self.out, "main-commitment-v1.json").read_text(encoding="utf-8")
        self.assertEqual(json.loads(written)["kind"], "main")
        self.assertEqual(json.loads(written)["signed_tag"]["name"], "t2-tag")
        self.assertNotIn("\r", written)
        with self.assertRaises(FileExistsError):
            prep.build(self.args)

    def test_a_digest_that_differs_from_the_committed_file_is_refused(self):
        self.tag(changes={"corpus/sampling/asb_goal_class.py": b"changed"})
        with self.assertRaises(SystemExit):
            prep.build(self.args)
        self.assertFalse(os.path.exists(os.path.join(self.out, "main-commitment-v1.json")))

    def test_a_left_over_fill_is_refused(self):
        self.tag(changes={prep.MAIN_TEMPLATE: MAIN_TEXT.replace('"kind": "main"', '"kind": "FILL"').encode("utf-8")})
        with self.assertRaises(SystemExit):
            prep.build(self.args)

    def test_a_development_id_with_bad_grammar_is_refused_by_select(self):
        self.tag()
        pathlib.Path(self.args.dev_ids).write_text(json.dumps(["has space"]), encoding="utf-8")
        with self.assertRaises(SystemExit):
            prep.build(self.args)

    def test_an_unsigned_tag_is_refused(self):
        self.tag()
        prep.git = lambda repo, *a, **kw: sys.exit("no signature") if a[0] == "verify-tag" else subprocess.run(["git", "-C", repo, *a], capture_output=True).stdout.decode().strip()
        with self.assertRaises(SystemExit):
            prep.build(self.args)


class RecordMain(unittest.TestCase):
    def test_a_main_record_prints_t2(self):
        commitment = json.loads(MAIN_TEXT)
        local = json.dumps(commitment).encode("utf-8")
        record = {"created": "2026-12-01T10:00:00.5+00:00", "files": [{"links": {"self": "https://x/f"}}]}

        class Reply:
            def __init__(self, data):
                self.data = data

            def read(self):
                return self.data

        replies = {"https://zenodo.org/api/records/7": json.dumps(record).encode(), "https://x/f": local}
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "c.json")
            pathlib.Path(path).write_bytes(local)
            real = prep.urllib.request.urlopen
            prep.urllib.request.urlopen = lambda url, timeout: Reply(replies[url])
            try:
                buf = io.StringIO()
                with contextlib.redirect_stdout(buf):
                    prep.main(["record", "--id", "7", "--file", path])
            finally:
                prep.urllib.request.urlopen = real
        self.assertIn("T2 = 2026-12-01T10:00:01Z", buf.getvalue())
        self.assertNotIn("T1 =", buf.getvalue())


if __name__ == "__main__":
    unittest.main()
