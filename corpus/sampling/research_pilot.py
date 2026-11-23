"""Research-pilot manifest builder (register D53 item 5): the frozen T5 pilot manifest with its 11 adversarial cases
replaced by the 11 cases of the pilot draw (TM-A 9, TM-B 2), pinned to that draw. The 20 compliant and 2 ambiguous
T5 cases stay. Partitions and digests are recomputed as carrier.py does it.

    py -3 -I corpus/sampling/research_pilot.py build --round R --draw PILOT_OUTPUT.json --id-suffix .rp1 --bundle DIR [--bundle DIR ...] --out DIR

PILOT_OUTPUT.json is the exact file `select.py pilot` wrote; its SHA-256 becomes pins.draw_digest and the file goes
into artifacts/ as draw-<hex>.json. DIR is a case bundle of a drawn case (case_entry.py build --draw pilot). --out must
not exist. Not T1-bound: select.py, the ledger and carrier.py are untouched.

Every case gets a fresh case_id (the id plus --id-suffix), because the guest admits each case/role/attempt once per
freeze and the pilot walk already used the ledger ids and the T5 ids. A drawn case keeps root_scenario_id and
meaning_record.root_id, which is how the draw's members are matched (evalcorpus/draw.go drawRules).
"""
import argparse
import hashlib
import json
import os
import sys

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)  # -I drops the script directory; this repo directory is trusted

import carrier  # noqa: E402
from carrier import CarrierError  # noqa: E402

PILOT_SCHEMA = "tbound-d48-pilot/v1"


def draw_members(draw_bytes):
    """The pilot places per stratum from the select.py pilot output (the same fields draw.go ParseDraw reads first)."""
    draw = json.loads(draw_bytes.decode("utf-8"))
    if draw.get("schema") != PILOT_SCHEMA or draw.get("inputs", {}).get("commitment", {}).get("kind") != "pilot":
        raise CarrierError(f"--draw is not a {PILOT_SCHEMA} output of a pilot commitment")
    if draw.get("status") != "complete":
        raise CarrierError(f"--draw status {draw.get('status')!r}; only a complete pilot fixes its members")
    pilot = draw["memberships"]["pilot"]
    return {cls: list(pilot[cls]) for cls in carrier.SLOT_CLASSES}


def check_drawn(bundles, members):
    """The bundles are exactly the draw's places, matched by root, each in its drawn stratum, with no sample_part."""
    drawn = {root: cls for cls, roots in members.items() for root in roots}
    got = {}
    for b in bundles:
        a = b["case"]["adversarial"]
        if a.get("sample_part"):
            raise CarrierError(f"{b['dir']}: a pilot case carries no sample_part, got {a['sample_part']!r}")
        got[a["root_scenario_id"]] = a["threat_class"]
    wrong = sorted(r for r in got if got[r] != drawn.get(r))
    missing = sorted(set(drawn) - set(got))
    if wrong or missing:
        raise CarrierError(f"bundles are not the pilot draw: not drawn or in another stratum {wrong}, drawn but missing {missing}")


def build_manifest(round_, draw_path, bundle_dirs, out, id_suffix, build=None, base_dir=carrier.BASE_DIR):
    build = build or carrier.load_build()
    if os.path.exists(out):
        raise CarrierError(f"--out {out} already exists")
    draw_bytes = carrier.read_bytes(draw_path)
    digest = hashlib.sha256(draw_bytes).hexdigest()
    bundles = [carrier.load_bundle(d) for d in bundle_dirs]
    carrier.check_bundles(bundles)
    check_drawn(bundles, draw_members(draw_bytes))

    manifest = carrier.read_json(os.path.join(base_dir, "manifest.draft.json"))
    artifacts = carrier.read_tree(os.path.join(base_dir, "artifacts"))
    base = [c for c in manifest["cases"] if c["kind"] != "adversarial"]
    dropped = [c for c in manifest["cases"] if c["kind"] == "adversarial"]
    kept = set().union(*(carrier.fixture_digests(c) for c in base))
    for case in dropped:
        for fixture in carrier.fixture_digests(case) - kept:
            artifacts.pop(carrier.fixture_name(fixture), None)
    artifacts = carrier.merge_files(artifacts, bundles, "artifacts", "artifact")
    artifacts[f"draw-{digest}.json"] = draw_bytes

    drawn = sorted((b["case"] for b in bundles), key=lambda c: (c["adversarial"]["threat_class"], c["case_id"]))
    roots = [c["adversarial"]["root_scenario_id"] for c in drawn]
    carrier.rename_ids(base, id_suffix)
    carrier.rename_ids(drawn, id_suffix)  # also renames the root, which the draw match needs unchanged
    for case, root in zip(drawn, roots):
        case["adversarial"]["root_scenario_id"] = case["meaning_record"]["root_id"] = root
    manifest["cases"] = drawn + base
    manifest["pins"]["draw_digest"] = "sha256:" + digest
    build.assign_partitions(drawn, manifest["split_seed"])

    sources = carrier.merge_sources(carrier.read_json(os.path.join(carrier.PILOT, "sources.json")), bundles)
    licenses = carrier.merge_files(carrier.read_tree(os.path.join(carrier.PILOT, "licenses")), bundles, "licenses", "licence")
    notices = carrier.read_bytes(os.path.join(carrier.PILOT, "THIRD_PARTY_NOTICES.md")) + b"\n"
    notices += carrier.notices_section(round_, bundles).replace("## Screening round", "## Research pilot", 1).encode("utf-8")

    manifest_bytes = carrier.dump_json(manifest, 2)
    os.makedirs(out)
    for sub, files in (("artifacts", artifacts), ("licenses", licenses)):
        os.makedirs(os.path.join(out, sub))
        for name, data in files.items():
            with open(os.path.join(out, sub, name), "wb") as f:
                f.write(data)
    for name, data in (("manifest.draft.json", manifest_bytes), ("sources.json", carrier.dump_json(sources, 1)),
                       ("THIRD_PARTY_NOTICES.md", notices)):
        with open(os.path.join(out, name), "wb") as f:
            f.write(data)
    return hashlib.sha256(manifest_bytes).hexdigest()


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="command", required=True)
    p = sub.add_parser("build")
    p.add_argument("--round", required=True)
    p.add_argument("--draw", required=True, help="the select.py pilot output, byte for byte")
    p.add_argument("--id-suffix", required=True, help="appended to every case id: '.' and lowercase letters or digits, unused on this freeze")
    p.add_argument("--bundle", action="append", default=[])
    p.add_argument("--out", required=True)
    args = ap.parse_args(argv)
    try:
        print(build_manifest(args.round, args.draw, args.bundle, args.out, args.id_suffix))
    except CarrierError as e:
        print(f"research_pilot: refused: {e}", file=sys.stderr)
        return 2
    except (KeyError, TypeError, ValueError) as e:
        print(f"research_pilot: refused: malformed input ({type(e).__name__}: {e})", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
