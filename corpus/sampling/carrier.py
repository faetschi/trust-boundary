"""Screening carrier builder (adaptation rule 7.1): the frozen T5 pilot manifest with some adversarial
slots replaced by newly adapted benchmark cases, for lane P's screening rounds. A derived TM-C bundle (a
development qualification fixture, D45) has no T5 slot and is appended after the adversarial cases instead.

    py -3 -I corpus/sampling/carrier.py build --round R --bundle DIR [--bundle DIR ...] --out DIR

DIR is a case bundle (see .local/handoffs/carrier-design-20261115.md, data shape 1). --out must not exist.
"""
import argparse
import hashlib
import importlib.util
import json
import os
import sys

sys.dont_write_bytecode = True  # importing build.py must not write corpus/pilot/__pycache__
HERE = os.path.dirname(os.path.abspath(__file__))
PILOT = os.path.join(os.path.dirname(HERE), "pilot")
BASE_DIR = os.path.join(PILOT, "t5-2026-10-29")
SLOT_CLASSES = ("TM-A", "TM-B")


class CarrierError(Exception):
    pass


def load_build():
    spec = importlib.util.spec_from_file_location("pilot_build_for_carrier", os.path.join(PILOT, "build.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)  # reads corpus/pilot/sources.json; main() is not called
    return module


def read_bytes(path):
    with open(path, "rb") as f:
        return f.read()


def read_json(path):
    return json.loads(read_bytes(path).decode("utf-8"))


def read_tree(directory):
    return {name: read_bytes(os.path.join(directory, name)) for name in sorted(os.listdir(directory))}


def load_bundle(directory):
    return {
        "dir": directory,
        "case": read_json(os.path.join(directory, "case.json")),
        "artifacts": read_tree(os.path.join(directory, "artifacts")),
        "sources": read_json(os.path.join(directory, "sources.json"))["pins"],
        "licenses": read_tree(os.path.join(directory, "licenses")),
        "notice": read_json(os.path.join(directory, "notice.json")),
    }


def fixture_digests(case):
    found = set()
    stack = [case]
    while stack:
        node = stack.pop()
        if isinstance(node, dict):
            for key, value in node.items():
                if key == "workspace_fixture_digest":
                    found.add(value)
                else:
                    stack.append(value)
        elif isinstance(node, list):
            stack.extend(node)
    return found


def fixture_name(digest):
    return "fixture-" + digest.removeprefix("sha256:") + ".json"


def check_bundles(bundles):
    seen_ids, seen_roots = set(), set()
    for b in bundles:
        case = b["case"]
        if case.get("kind") != "adversarial":
            raise CarrierError(f"{b['dir']}: case kind {case.get('kind')!r} is not adversarial")
        cls = case["adversarial"]["threat_class"]
        if cls not in SLOT_CLASSES + ("TM-C",):
            raise CarrierError(f"{b['dir']}: threat class {cls!r} has no T5 slots")
        root = case["adversarial"]["root_scenario_id"]
        mr = case["meaning_record"]
        if not case["case_id"] == root == mr["root_id"]:
            raise CarrierError(f"{b['dir']}: case_id {case['case_id']!r}, root_scenario_id {root!r} and meaning_record.root_id {mr['root_id']!r} differ")
        if case["adversarial"]["calibration_witness_id"] != "PILOT-WIT-" + case["case_id"]:
            raise CarrierError(f"{b['dir']}: calibration_witness_id is not PILOT-WIT-{case['case_id']}")
        if cls == "TM-C":
            # derived (D48): no benchmark pin, so no sources, licences or notice row
            if mr.get("case_origin") != "derived" or "derived" not in mr or case["adversarial"].get("sample_part") != "tmc":
                raise CarrierError(f"{b['dir']}: a TM-C bundle needs a derived meaning record and sample_part tmc")
            if b["sources"] or b["licenses"]:
                raise CarrierError(f"{b['dir']}: a derived TM-C bundle carries no sources or licences")
        else:
            check_pin(b, mr)
        if case["case_id"] in seen_ids:
            raise CarrierError(f"duplicate case_id {case['case_id']}")
        if root in seen_roots:
            raise CarrierError(f"duplicate root {root}")
        seen_ids.add(case["case_id"])
        seen_roots.add(root)
        want = {fixture_name(d) for d in fixture_digests(case)}
        if set(b["artifacts"]) != want:
            raise CarrierError(f"{b['dir']}: artifacts differ from the fixtures the case names: "
                               f"missing {sorted(want - set(b['artifacts']))}, unreferenced {sorted(set(b['artifacts']) - want)}")


def check_pin(b, mr):
    """A benchmark bundle: its meaning record, notice and licences match its sources.json pin."""
    pin = b["sources"].get(mr["benchmark_key"])
    if pin is None or pin["rev"] != mr["benchmark_revision"] or mr["benchmark_digest"] not in pin["digests"]:
        raise CarrierError(f"{b['dir']}: meaning_record benchmark_key, revision and digest do not match the bundle sources.json pin")
    n = b["notice"]
    if (n["pin_key"], n["rev"], n["license"]) != (mr["benchmark_key"], pin["rev"], pin["license"]):
        raise CarrierError(f"{b['dir']}: notice.json pin_key, rev and license do not match the sources.json pin")
    want_lic = {f.split("/", 1)[1] for p in b["sources"].values() for f in p["license_files"]}
    if set(b["licenses"]) != want_lic:
        raise CarrierError(f"{b['dir']}: licenses differ from the sources.json license_files: "
                           f"missing {sorted(want_lic - set(b['licenses']))}, unreferenced {sorted(set(b['licenses']) - want_lic)}")


def place_cases(manifest, bundles):
    """Replace the first k same-stratum T5 adversarial cases (sorted by case_id) with the k bundles
    (sorted by case_id), each in its slot's position. Returns the replaced cases."""
    cases = manifest["cases"]
    replaced = []
    for cls in SLOT_CLASSES:
        group = sorted((b for b in bundles if b["case"]["adversarial"]["threat_class"] == cls),
                       key=lambda b: b["case"]["case_id"])
        slots = sorted((i for i, c in enumerate(cases)
                        if c["kind"] == "adversarial" and c["adversarial"]["threat_class"] == cls),
                       key=lambda i: cases[i]["case_id"])
        if len(group) > len(slots):
            raise CarrierError(f"{len(group)} {cls} bundles but only {len(slots)} slots")
        for b, i in zip(group, slots):
            replaced.append(cases[i])
            cases[i] = b["case"]
    tmc = sorted((b["case"] for b in bundles if b["case"]["adversarial"]["threat_class"] == "TM-C"), key=lambda c: c["case_id"])
    if tmc:
        last = max(i for i, c in enumerate(cases) if c["kind"] == "adversarial")
        cases[last + 1:last + 1] = tmc
    ids = [c["case_id"] for c in cases]
    roots = [c["adversarial"]["root_scenario_id"] for c in cases if c["kind"] == "adversarial"]
    for what, values in (("case_id", ids), ("root", roots)):
        dup = sorted({v for v in values if values.count(v) > 1})
        if dup:
            raise CarrierError(f"duplicate {what} {dup[0]}")
    return replaced


def merge_sources(base, bundles):
    pins = json.loads(json.dumps(base["pins"]))
    for b in bundles:
        for key, pin in b["sources"].items():
            have = pins.setdefault(key, {"rev": pin["rev"], "license": pin["license"],
                                         "digests": [], "license_files": []})
            for field in ("rev", "license"):
                if have[field] != pin[field]:
                    raise CarrierError(f"pin {key}: conflicting {field} {have[field]!r} vs {pin[field]!r}")
            if set(pin["digests"]) - set(have["digests"]):
                have["digests"] = sorted(set(have["digests"]) | set(pin["digests"]))
            have["license_files"] += [f for f in pin["license_files"] if f not in have["license_files"]]
    return dict(base, pins=pins)


def merge_files(base, bundles, field, what):
    out = dict(base)
    folded = {name.casefold(): name for name in out}  # a case-insensitive checkout must not merge two names
    for b in bundles:
        for name, data in b[field].items():
            prior = folded.setdefault(name.casefold(), name)
            if prior != name:
                raise CarrierError(f"{what} {name}: differs from {prior} only in case")
            if out.get(name, data) != data:
                raise CarrierError(f"{what} {name}: same name, different bytes")
            out.setdefault(name, data)
    return out


def notices_section(round_, bundles):
    derived = [b for b in bundles if b["case"]["adversarial"]["threat_class"] == "TM-C"]
    bundles = [b for b in bundles if b not in derived]
    pins = {b["notice"]["pin_key"]: b["notice"] for b in bundles}
    lines = [f"## Screening round {round_}", ""]
    if pins:
        lines += ["| Source | Pinned revision | License | License file |", "|---|---|---|---|"]
    for key in sorted(pins):
        n = pins[key]
        files = ", ".join("`" + f + "`" for f in n["license_files"])
        lines.append(f"| {n['repository']} | `{n['rev']}` | {n['license']} | {files} |")
    lines += ["", "Cases:", ""] if pins else ["Cases:", ""]
    for b in sorted(bundles, key=lambda b: b["case"]["case_id"]):
        n = b["notice"]
        lines.append(f"- `{n['case_id']}`: {n['pin_key']} `{n['source_item_id']}`")
    for b in sorted(derived, key=lambda b: b["case"]["case_id"]):
        lines.append(f"- `{b['case']['case_id']}`: project-authored derived fixture (no third-party source)")
    return "\n".join(lines) + "\n"


def dump_json(obj, indent):
    return (json.dumps(obj, indent=indent, ensure_ascii=False) + "\n").encode("utf-8")


def build_carrier(round_, bundle_dirs, out, build=None, base_dir=BASE_DIR):
    build = build or load_build()
    if os.path.exists(out):
        raise CarrierError(f"--out {out} already exists")
    bundles = [load_bundle(d) for d in bundle_dirs]
    check_bundles(bundles)
    manifest = read_json(os.path.join(base_dir, "manifest.draft.json"))
    artifacts = read_tree(os.path.join(base_dir, "artifacts"))
    replaced = place_cases(manifest, bundles)

    new_ids = {b["case"]["case_id"] for b in bundles}
    kept = set()
    for case in manifest["cases"]:
        if case["case_id"] not in new_ids:
            kept |= fixture_digests(case)
    for case in replaced:
        for digest in fixture_digests(case) - kept:
            artifacts.pop(fixture_name(digest), None)
    artifacts = merge_files(artifacts, bundles, "artifacts", "artifact")

    build.assign_partitions([c for c in manifest["cases"] if c["kind"] == "adversarial"], manifest["split_seed"])

    sources = merge_sources(read_json(os.path.join(PILOT, "sources.json")), bundles)
    licenses = merge_files(read_tree(os.path.join(PILOT, "licenses")), bundles, "licenses", "licence")
    notices = read_bytes(os.path.join(PILOT, "THIRD_PARTY_NOTICES.md"))
    if bundles:
        notices += b"\n" + notices_section(round_, bundles).encode("utf-8")

    manifest_bytes = dump_json(manifest, 2)
    os.makedirs(out)
    for sub, files in (("artifacts", artifacts), ("licenses", licenses)):
        os.makedirs(os.path.join(out, sub))
        for name, data in files.items():
            with open(os.path.join(out, sub, name), "wb") as f:
                f.write(data)
    for name, data in (("manifest.draft.json", manifest_bytes), ("sources.json", dump_json(sources, 1)),
                       ("THIRD_PARTY_NOTICES.md", notices)):
        with open(os.path.join(out, name), "wb") as f:
            f.write(data)
    return hashlib.sha256(manifest_bytes).hexdigest()


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="command", required=True)
    p = sub.add_parser("build")
    p.add_argument("--round", required=True)
    p.add_argument("--bundle", action="append", default=[])
    p.add_argument("--out", required=True)
    args = ap.parse_args(argv)
    try:
        print(build_carrier(args.round, args.bundle, args.out))
    except CarrierError as e:
        print(f"carrier: refused: {e}", file=sys.stderr)
        return 2
    except (KeyError, TypeError, ValueError) as e:
        print(f"carrier: refused: malformed bundle ({type(e).__name__}: {e})", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
