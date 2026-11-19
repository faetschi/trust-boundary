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


NEW_PIN = {"rev": "c" * 40, "license": "MIT", "license_files": ["licenses/NewBench-LICENSE"]}


def write_bundle(parent, name, template_id, case_id, *, pin="InjecAgent", lic_name=None, licence=None):
    """A synthetic bundle: a copy of T5 case template_id with only its ids, benchmark pin and fixture changed.
    A pin that the pilot sources.json lacks is a new one (NewBench). lic_name renames the licence file."""
    base = t5_manifest()
    case = copy.deepcopy(next(c for c in base["cases"] if c["case_id"] == template_id))
    (old_digest,) = carrier.fixture_digests(case)
    new_digest = "sha256:" + hashlib.sha256(case_id.encode()).hexdigest()
    case = replace_value(case, old_digest, new_digest)
    case["case_id"] = case["adversarial"]["root_scenario_id"] = case["meaning_record"]["root_id"] = case_id
    case["adversarial"]["source_item_id"] = case["meaning_record"]["source_item_id"] = "item-" + case_id
    case["adversarial"]["calibration_witness_id"] = "PILOT-WIT-" + case_id
    src = json.loads(read(os.path.join(PILOT, "sources.json")))["pins"].get(pin, NEW_PIN)
    lic_name = lic_name or src["license_files"][0].split("/")[1]
    lic_files = ["licenses/" + lic_name]
    bench_digest = "sha256:" + hashlib.sha256(b"item " + case_id.encode()).hexdigest()
    mr = case["meaning_record"]
    mr.update(benchmark_key=pin, benchmark_revision=src["rev"], benchmark_digest=bench_digest, license=src["license"])
    root = os.path.join(parent, name)
    for sub in ("artifacts", "licenses"):
        os.makedirs(os.path.join(root, sub))
    pilot_licence = os.path.join(PILOT, "licenses", lic_name)
    files = {
        "case.json": carrier.dump_json(case, 2),
        "artifacts/" + carrier.fixture_name(new_digest): b'{"fake": "fixture ' + case_id.encode() + b'"}\n',
        "sources.json": json.dumps({"pins": {pin: {"rev": src["rev"], "license": src["license"], "digests": [bench_digest],
                                                   "license_files": lic_files}}}).encode(),
        "licenses/" + lic_name: licence if licence is not None else read(pilot_licence) if os.path.exists(pilot_licence) else b"MIT text\n",
        "notice.json": json.dumps({"pin_key": pin, "repository": "https://example.invalid/" + pin, "rev": src["rev"],
                                   "license": src["license"], "license_files": lic_files,
                                   "case_id": case_id, "source_item_id": "item-" + case_id}).encode(),
    }
    for rel, data in files.items():
        with open(os.path.join(root, rel), "wb") as f:
            f.write(data)
    return root, case, new_digest


def patch_json(bundle, name, change):
    path = os.path.join(bundle, name)
    doc = json.loads(read(path))
    change(doc)
    with open(path, "wb") as f:
        f.write(carrier.dump_json(doc, 2))


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
        b, _, _ = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01", pin="NewBench")
        self.build([b])
        pins = json.loads(read(os.path.join(self.out, "sources.json")))["pins"]
        self.assertEqual(len(pins["NewBench"]["digests"]), 1)
        self.assertEqual(read(os.path.join(self.out, "licenses", "NewBench-LICENSE")), b"MIT text\n")

    def test_base_pin_license_files_keep_their_order(self):
        base = {"pins": {"X": {"rev": "r", "license": "MIT", "digests": ["sha256:1"], "license_files": ["licenses/b", "licenses/a"]}}}
        bundle = {"sources": {"X": {"rev": "r", "license": "MIT", "digests": ["sha256:0"], "license_files": ["licenses/a", "licenses/c"]}}}
        pin = carrier.merge_sources(base, [bundle])["pins"]["X"]
        self.assertEqual((pin["digests"], pin["license_files"]), (["sha256:0", "sha256:1"], ["licenses/b", "licenses/a", "licenses/c"]))

    def refused(self, bundles, text):
        with self.assertRaisesRegex(carrier.CarrierError, text):
            self.build(bundles)
        self.assertFalse(os.path.exists(self.out))

    def test_more_bundles_than_slots(self):
        bundles = [write_bundle(self.tmp.name, f"b{i}", "PILOT-ADV-B-02", f"asb:x:{i}:u01", pin="agiresearch/ASB")[0]
                   for i in range(5)]
        self.refused(bundles, "5 TM-B bundles but only 4 slots")

    def tmc_bundle(self, case_id, derived=True):
        b, case, _ = write_bundle(self.tmp.name, case_id, "PILOT-ADV-A-02", case_id)
        case["adversarial"].update(threat_class="TM-C", sample_part="tmc")
        if derived:
            case["meaning_record"].update(case_origin="derived", derived={"author": "test"})
            for name in os.listdir(os.path.join(b, "licenses")):
                os.remove(os.path.join(b, "licenses", name))
            with open(os.path.join(b, "sources.json"), "wb") as f:
                f.write(b'{"pins": {}}')
        with open(os.path.join(b, "case.json"), "wb") as f:
            f.write(carrier.dump_json(case, 2))
        return b

    def test_derived_tm_c_is_appended_after_the_adversarial_cases(self):
        self.build([self.tmc_bundle("QUAL-B"), self.tmc_bundle("QUAL-A")])
        ids = [c["case_id"] for c in json.loads(read(os.path.join(self.out, "manifest.draft.json")))["cases"]]
        base = [c["case_id"] for c in t5_manifest()["cases"]]
        last = max(i for i, c in enumerate(t5_manifest()["cases"]) if c["kind"] == "adversarial")
        self.assertEqual(ids, base[:last + 1] + ["QUAL-A", "QUAL-B"] + base[last + 1:])
        self.assertIn("`QUAL-A`: project-authored derived fixture", read(os.path.join(self.out, "THIRD_PARTY_NOTICES.md")).decode())
        self.assertEqual(read(os.path.join(self.out, "sources.json")), carrier.dump_json(json.loads(read(os.path.join(PILOT, "sources.json"))), 1))

    def test_tm_c_needs_a_derived_record_and_no_sources(self):
        self.refused([self.tmc_bundle("QUAL-A", derived=False)], "derived meaning record")
        b = self.tmc_bundle("QUAL-B")
        with open(os.path.join(b, "sources.json"), "wb") as f:
            f.write(b'{"pins": {"x": {}}}')
        self.refused([b], "carries no sources or licences")

    def test_duplicate_case_id(self):
        a = write_bundle(self.tmp.name, "a", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-03", "injecagent:dh:0002:u01")[0]
        self.refused([a, b], "duplicate case_id")

    def test_case_id_colliding_with_a_kept_t5_case(self):
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "PILOT-ADV-A-05")[0]
        self.refused([b], "duplicate case_id")

    def test_root_colliding_with_a_kept_t5_root(self):
        root = next(c for c in t5_manifest()["cases"] if c["case_id"] == "PILOT-ADV-A-05")["adversarial"]["root_scenario_id"]
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", root)[0]
        self.refused([b], "duplicate root")

    def test_conflicting_pin_rev(self):
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
        patch_json(b, "sources.json", lambda d: d["pins"]["InjecAgent"].update(rev="e" * 40))
        patch_json(b, "case.json", lambda c: c["meaning_record"].update(benchmark_revision="e" * 40))
        patch_json(b, "notice.json", lambda n: n.update(rev="e" * 40))
        self.refused([b], "conflicting rev")

    def test_same_licence_name_different_bytes(self):
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01", licence=b"altered\n")[0]
        self.refused([b], "same name, different bytes")

    def test_ids_must_agree_inside_a_bundle(self):
        for label, change in (("root_scenario_id", lambda c: c["adversarial"].update(root_scenario_id="other")),
                              ("root_id", lambda c: c["meaning_record"].update(root_id="other"))):
            b = write_bundle(self.tmp.name, "b" + label, "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
            patch_json(b, "case.json", change)
            self.refused([b], "differ")

    def test_witness_id_follows_the_case_id(self):
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
        patch_json(b, "case.json", lambda c: c["adversarial"].update(calibration_witness_id="PILOT-WIT-other"))
        self.refused([b], "calibration_witness_id is not PILOT-WIT-injecagent:dh:0002:u01")

    def test_meaning_record_must_match_the_sources_pin(self):
        for label, change in (("key", lambda c: c["meaning_record"].update(benchmark_key="Other")),
                              ("rev", lambda c: c["meaning_record"].update(benchmark_revision="f" * 40)),
                              ("digest", lambda c: c["meaning_record"].update(benchmark_digest="sha256:" + "0" * 64))):
            b = write_bundle(self.tmp.name, "b" + label, "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
            patch_json(b, "case.json", change)
            self.refused([b], "do not match the bundle sources.json pin")

    def test_notice_must_match_the_sources_pin(self):
        for field in ("pin_key", "rev", "license"):
            b = write_bundle(self.tmp.name, "b" + field, "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
            patch_json(b, "notice.json", lambda n: n.update({field: "other"}))
            self.refused([b], "notice.json pin_key, rev and license do not match")

    def test_unreferenced_artifact_is_refused(self):
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
        with open(os.path.join(b, "artifacts", "stray.json"), "wb") as f:
            f.write(b"{}\n")
        self.refused([b], "unreferenced \\['stray.json'\\]")

    def test_names_differing_only_in_case(self):
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01", lic_name="injecagent-licence", licence=b"altered\n")[0]
        self.refused([b], "only in case")
        same = write_bundle(self.tmp.name, "s", "PILOT-ADV-A-02", "injecagent:dh:0002:u01", lic_name="injecagent-licence")[0]
        self.refused([same], "only in case")  # one file on a case-insensitive checkout, two names in sources.json

    def test_bundle_licences_match_its_pins(self):
        b = write_bundle(self.tmp.name, "b", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
        with open(os.path.join(b, "licenses", "STRAY"), "wb") as f:
            f.write(b"x\n")
        self.refused([b], "unreferenced \\['STRAY'\\]")
        c = write_bundle(self.tmp.name, "c", "PILOT-ADV-A-02", "injecagent:dh:0002:u01")[0]
        for name in os.listdir(os.path.join(c, "licenses")):
            os.remove(os.path.join(c, "licenses", name))
        self.refused([c], "licenses differ from the sources.json license_files: missing")

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
