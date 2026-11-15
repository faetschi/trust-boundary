"""Screening carrier builder (adaptation rule 7.1): the frozen T5 pilot manifest with some adversarial
slots replaced by newly adapted benchmark cases, for lane P's screening rounds.

    py -3 -I corpus/sampling/carrier.py build --round R --bundle DIR [--bundle DIR ...] --out DIR

DIR is a case bundle (see .local/handoffs/carrier-design-20261115.md, data shape 1). --out must not exist.
"""
import argparse
import hashlib
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
PILOT = os.path.join(os.path.dirname(HERE), "pilot")
BASE_DIR = os.path.join(PILOT, "t5-2026-10-29")
SLOT_CLASSES = ("TM-A", "TM-B")


class CarrierError(Exception):
    pass


def load_build():
    spec = importlib.util.spec_from_file_location("pilot_build_for_carrier", os.path.join(PILOT, "build.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
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
        if cls == "TM-C":
            raise CarrierError(f"{b['dir']}: TM-C is not carried (no T5 slot)")
        if cls not in SLOT_CLASSES:
            raise CarrierError(f"{b['dir']}: threat class {cls!r} has no T5 slots")
        root = case["adversarial"]["root_scenario_id"]
        if case["case_id"] in seen_ids:
            raise CarrierError(f"duplicate case_id {case['case_id']}")
        if root in seen_roots:
            raise CarrierError(f"duplicate root {root}")
        seen_ids.add(case["case_id"])
        seen_roots.add(root)
        for digest in fixture_digests(case):
            if fixture_name(digest) not in b["artifacts"]:
                raise CarrierError(f"{b['dir']}: missing artifacts/{fixture_name(digest)}")


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
            have["license_files"] = sorted(set(have["license_files"]) | set(pin["license_files"]))
    return dict(base, pins=pins)


def merge_files(base, bundles, field, what):
    out = dict(base)
    for b in bundles:
        for name, data in b[field].items():
            if out.setdefault(name, data) != data:
                raise CarrierError(f"{what} {name}: same name, different bytes")
    return out


def notices_section(round_, bundles):
    pins = {b["notice"]["pin_key"]: b["notice"] for b in bundles}
    lines = [f"## Screening round {round_}", "",
             "| Source | Pinned revision | License | License file |", "|---|---|---|---|"]
    for key in sorted(pins):
        n = pins[key]
        files = ", ".join("`" + f + "`" for f in n["license_files"])
        lines.append(f"| {n['repository']} | `{n['rev']}` | {n['license']} | {files} |")
    lines += ["", "Cases:", ""]
    for b in sorted(bundles, key=lambda b: b["case"]["case_id"]):
        n = b["notice"]
        lines.append(f"- `{n['case_id']}`: {n['pin_key']} `{n['source_item_id']}`")
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
    return 0


if __name__ == "__main__":
    sys.exit(main())
