"""Tests for carrier.py on synthetic bundles. Run from the repository root:
py -3 -I -m unittest discover -s corpus/sampling -p "test_*.py" -v"""
import copy
import hashlib
import importlib.util
import json
import os
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
PILOT = os.path.join(ROOT, "corpus", "pilot")
T5 = os.path.join(PILOT, "t5-2026-10-29")


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


carrier = load("carrier_under_test", os.path.join(HERE, "carrier.py"))
build = load("pilot_build_under_test", os.path.join(PILOT, "build.py"))


def read(path):
    with open(path, "rb") as f:
        return f.read()


def tree(directory):
    return {n: read(os.path.join(directory, n)) for n in sorted(os.listdir(directory))}


def t5_manifest():
    return json.loads(read(os.path.join(T5, "manifest.draft.json")))


def replace_value(node, old, new):
    if isinstance(node, dict):
        return {k: replace_value(v, old, new) for k, v in node.items()}
    if isinstance(node, list):
        return [replace_value(v, old, new) for v in node]
    return new if node == old else node


def write_bundle(parent, name, template_id, case_id, *, pin="InjecAgent", rev=None, licence=None, digest_seed=None):
    """A synthetic bundle: a copy of T5 case template_id under a new ledger-style id and a fake fixture."""
    base = t5_manifest()
    case = copy.deepcopy(next(c for c in base["cases"] if c["case_id"] == template_id))
    (old_digest,) = carrier.fixture_digests(case)
    new_digest = "sha256:" + hashlib.sha256((digest_seed or case_id).encode()).hexdigest()
    case = replace_value(case, old_digest, new_digest)
    case["case_id"] = case["adversarial"]["root_scenario_id"] = case["meaning_record"]["root_id"] = case_id
    case["adversarial"]["source_item_id"] = case["meaning_record"]["source_item_id"] = "item-" + case_id
    case["calibration_witness_id"] = "PILOT-WIT-" + case_id
    case["meaning_record"]["calibration_witness_id"] = "PILOT-WIT-" + case_id
    src = json.loads(read(os.path.join(PILOT, "sources.json")))["pins"][pin]
    lic_name = src["license_files"][0].split("/")[1]
    root = os.path.join(parent, name)
    for sub in ("artifacts", "licenses"):
        os.makedirs(os.path.join(root, sub))
    files = {
        "case.json": (json.dumps(case, indent=2, ensure_ascii=False) + "\n").encode(),
        "artifacts/" + carrier.fixture_name(new_digest): b'{"fake": "fixture ' + case_id.encode() + b'"}\n',
        "sources.json": json.dumps({"pins": {pin: {
            "rev": rev or src["rev"], "license": src["license"],
            "digests": ["sha256:" + hashlib.sha256(case_id.encode()).hexdigest()],
            "license_files": src["license_files"]}}}).encode(),
        "licenses/" + lic_name: licence if licence is not None else read(os.path.join(PILOT, "licenses", lic_name)),
        "notice.json": json.dumps({"pin_key": pin, "repository": "https://example.invalid/" + pin, "rev": rev or src["rev"],
                                   "license": src["license"], "license_files": src["license_files"],
                                   "case_id": case_id, "source_item_id": "item-" + case_id}).encode(),
    }
    for rel, data in files.items():
        with open(os.path.join(root, rel), "wb") as f:
            f.write(data)
    return root, case, new_digest


class CarrierTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.out = os.path.join(self.tmp.name, "out")

    def build(self, bundles, out=None):
        return carrier.build_carrier("R1", bundles, out or self.out, build=build)

    def test_zero_bundles_reproduce_t5_byte_for_byte(self):
        self.build([])
        self.assertEqual(read(os.path.join(self.out, "manifest.draft.json")), read(os.path.join(T5, "manifest.draft.json")))
        self.assertEqual(tree(os.path.join(self.out, "artifacts")), tree(os.path.join(T5, "artifacts")))
        self.assertEqual(tree(os.path.join(self.out, "licenses")), tree(os.path.join(PILOT, "licenses")))
        self.assertEqual(read(os.path.join(self.out, "sources.json")), read(os.path.join(PILOT, "sources.json")))
        self.assertEqual(read(os.path.join(self.out, "THIRD_PARTY_NOTICES.md")), read(os.path.join(PILOT, "THIRD_PARTY_NOTICES.md")))

    def test_bundles_replace_the_first_slots_of_their_stratum(self):
        base = t5_manifest()
        old_fixtures = {c["case_id"]: carrier.fixture_digests(c) for c in base["cases"]}
        # Handed over out of order: sorting by case_id decides which slot each takes.
        b2, c2, d2 = write_bundle(self.tmp.name, "b2", "PILOT-ADV-A-03", "injecagent:dh:0010:u03")
        b1, c1, d1 = write_bundle(self.tmp.name, "b1", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")
        bb, cb, db = write_bundle(self.tmp.name, "bb", "PILOT-ADV-B-02", "asb:ddt:0001:u01", pin="agiresearch/ASB")
        sha = self.build([b2, b1, bb])
        out = json.loads(read(os.path.join(self.out, "manifest.draft.json")))
        self.assertEqual(sha, hashlib.sha256(read(os.path.join(self.out, "manifest.draft.json"))).hexdigest())

        ids = [c["case_id"] for c in out["cases"]]
        base_ids = [c["case_id"] for c in base["cases"]]
        expect = list(base_ids)
        expect[expect.index("PILOT-ADV-A-01")] = "injecagent:dh:0002:u01"
        expect[expect.index("PILOT-ADV-A-02")] = "injecagent:dh:0010:u03"
        expect[expect.index("PILOT-ADV-B-01")] = "asb:ddt:0001:u01"
        self.assertEqual(ids, expect)

        arts = tree(os.path.join(self.out, "artifacts"))
        for gone in ("PILOT-ADV-A-01", "PILOT-ADV-A-02", "PILOT-ADV-B-01"):
            for digest in old_fixtures[gone]:
                self.assertNotIn(carrier.fixture_name(digest), arts)
        for digest in old_fixtures["PILOT-ADV-A-03"]:
            self.assertIn(carrier.fixture_name(digest), arts)
        for digest in (d1, d2, db):
            self.assertIn(carrier.fixture_name(digest), arts)

        adv = [c for c in out["cases"] if c["kind"] == "adversarial"]
        want = copy.deepcopy(adv)
        build.assign_partitions(want, out["split_seed"])
        self.assertEqual(adv, want)

        by_id = {c["case_id"]: c for c in base["cases"]}
        for c in out["cases"]:
            if c["kind"] != "adversarial":
                self.assertEqual(c, by_id[c["case_id"]])

        pins = json.loads(read(os.path.join(self.out, "sources.json")))["pins"]
        base_pins = json.loads(read(os.path.join(PILOT, "sources.json")))["pins"]
        self.assertEqual(len(pins["InjecAgent"]["digests"]), len(base_pins["InjecAgent"]["digests"]) + 2)
        self.assertEqual(pins["InjecAgent"]["digests"], sorted(pins["InjecAgent"]["digests"]))
        self.assertEqual(len(pins["agiresearch/ASB"]["digests"]), len(base_pins["agiresearch/ASB"]["digests"]) + 1)
        self.assertEqual(pins["AgentDojo-v1"], base_pins["AgentDojo-v1"])
        self.assertEqual(tree(os.path.join(self.out, "licenses")), tree(os.path.join(PILOT, "licenses")))

        notices = read(os.path.join(self.out, "THIRD_PARTY_NOTICES.md")).decode()
        base_notices = read(os.path.join(PILOT, "THIRD_PARTY_NOTICES.md")).decode()
        self.assertTrue(notices.startswith(base_notices))
        section = notices[len(base_notices):]
        self.assertIn("## Screening round R1", section)
        for cid in ("injecagent:dh:0002:u01", "injecagent:dh:0010:u03", "asb:ddt:0001:u01"):
            self.assertIn(cid, section)
        self.assertIn("item-asb:ddt:0001:u01", section)

    def test_new_pin_and_licence_are_added(self):
        b, _, _ = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")
        with open(os.path.join(b, "sources.json"), "w") as f:
            json.dump({"pins": {"NewBench": {"rev": "c" * 40, "license": "MIT", "digests": ["sha256:" + "d" * 64],
                                              "license_files": ["licenses/NewBench-LICENSE"]}}}, f)
        with open(os.path.join(b, "licenses", "NewBench-LICENSE"), "wb") as f:
            f.write(b"MIT text\n")
        self.build([b])
        pins = json.loads(read(os.path.join(self.out, "sources.json")))["pins"]
        self.assertEqual(pins["NewBench"]["digests"], ["sha256:" + "d" * 64])
        self.assertEqual(read(os.path.join(self.out, "licenses", "NewBench-LICENSE")), b"MIT text\n")

    def refused(self, bundles, text):
        with self.assertRaisesRegex(carrier.CarrierError, text):
            self.build(bundles)
        self.assertFalse(os.path.exists(self.out))

    def test_more_bundles_than_slots(self):
        bundles = [write_bundle(self.tmp.name, f"b{i}", "PILOT-ADV-B-02", f"asb:x:{i}:u01", pin="agiresearch/ASB")[0]
                   for i in range(5)]
        self.refused(bundles, "5 TM-B bundles but only 4 slots")

    def test_tm_c_is_refused(self):
        b, case, _ = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")
        case["adversarial"]["threat_class"] = "TM-C"
        with open(os.path.join(b, "case.json"), "w") as f:
            json.dump(case, f)
        self.refused([b], "TM-C")

    def test_duplicate_case_id(self):
        a = write_bundle(self.tmp.name, "a", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
        b, case, _ = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-03", "injecagent:dh:0003:u01")
        case["case_id"] = "injecagent:dh:0002:u01"
        with open(os.path.join(b, "case.json"), "w") as f:
            json.dump(case, f)
        self.refused([a, b], "duplicate case_id")

    def test_case_id_colliding_with_a_kept_t5_case(self):
        b, case, _ = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")
        case["case_id"] = "PILOT-ADV-A-05"
        with open(os.path.join(b, "case.json"), "w") as f:
            json.dump(case, f)
        self.refused([b], "duplicate case_id")

    def test_duplicate_root(self):
        a = write_bundle(self.tmp.name, "a", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
        b, case, _ = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-03", "injecagent:dh:0003:u01")
        case["adversarial"]["root_scenario_id"] = "injecagent:dh:0002:u01"
        with open(os.path.join(b, "case.json"), "w") as f:
            json.dump(case, f)
        self.refused([a, b], "duplicate root")

    def test_conflicting_pin_rev(self):
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01", rev="e" * 40)[0]
        self.refused([b], "conflicting rev")

    def test_same_licence_name_different_bytes(self):
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01", licence=b"altered\n")[0]
        self.refused([b], "same name, different bytes")

    def test_existing_out(self):
        os.makedirs(self.out)
        with self.assertRaisesRegex(carrier.CarrierError, "already exists"):
            self.build([])

    def test_cli_prints_manifest_digest_and_refuses_with_status_2(self):
        import contextlib
        import io
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            self.assertEqual(carrier.main(["build", "--round", "R1", "--out", self.out]), 0)
        self.assertEqual(buf.getvalue().strip(), hashlib.sha256(read(os.path.join(T5, "manifest.draft.json"))).hexdigest())
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(carrier.main(["build", "--round", "R1", "--out", self.out]), 2)


if __name__ == "__main__":
    unittest.main()
