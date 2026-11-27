"""Dev-only carrier builder for the F36 development sets: the frozen T5 manifest with its adversarial, `read` compliant and
ambiguous cases replaced by the new bundles (dev_src_llmail.py, dev_src_cruxeval.py, dev_src_oas.py), and the T5 write,
edit and bash compliant cases kept under fresh ids (adaptation rule 7.1 shape; carrier.py places adversarial bundles only).

    py -3 -I corpus/sampling/dev_carrier.py build --round devset-01 --suffix .ds1 --bundles DIR [--bundles DIR ...]
        --records FILE [--records FILE ...] --out corpus/sampling/carriers/devset-01 --selection-out FILE

Placement is by kind and order: bundles of a class (TM-A, TM-B), of a compliant tool or of the ambiguous kind, sorted by
case_id, take the first slots of that class in ascending T5 case_id order, each in its slot's position. Artifacts that no
case references are dropped. The cold and variance partitions are recomputed as evalcorpus.Allocate does. --out must not exist.
"""
import argparse
import json
import os
import sys

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import carrier as C  # noqa: E402
import dev_shell_guard as G  # noqa: E402


def bundle_dirs(roots):
    out = []
    for r in roots:
        out += [os.path.join(r, d) for d in sorted(os.listdir(r)) if os.path.exists(os.path.join(r, d, "case.json"))]
    return out


def referenced(case, attacker):
    """File names a case needs under artifacts/."""
    names = set()
    part = case.get("adversarial") or case.get("compliant") or case.get("ambiguous")
    names.add(C.fixture_name(part["workspace_fixture_digest"]))
    if case["kind"] == "compliant":
        names.add("accept-" + part["acceptance_test"]["digest"].removeprefix("sha256:") + ".json")
        names.add(part["reference_solution"]["ref"].split(":")[0])
    if case["kind"] == "ambiguous":
        names.add(part["rubric"]["ref"])
    if attacker:
        names.add("attacker-" + attacker.removeprefix("sha256:") + ".json")
    return names


def kind_key(case):
    if case["kind"] == "adversarial":
        return ("adversarial", case["adversarial"]["threat_class"])
    if case["kind"] == "compliant":
        return ("compliant", case["compliant"]["primary_tool"])
    return ("ambiguous", "")


def build(a):
    build_mod = C.load_build()
    if os.path.exists(a.out):
        raise C.CarrierError(f"--out {a.out} already exists")
    head, tool_blobs = G.require_clean([__file__])
    bundles = [C.load_bundle(d) for d in bundle_dirs(a.bundles)]
    records = {}
    for p in a.records:
        r = C.read_json(p)
        if r["source"] in records:
            raise C.CarrierError(f"two records for the source {r['source']}")
        records[r["source"]] = r
    rec_ids = [c["case_id"] for r in records.values() for c in r["cases"]]
    if len(set(rec_ids)) != len(rec_ids) or set(rec_ids) != {b["case"]["case_id"] for b in bundles}:
        raise C.CarrierError("the bundle case ids differ from the case ids of the records")
    for b in bundles:
        case = b["case"]
        mr = case["meaning_record"]
        if case["kind"] == "adversarial":
            C.check_bundles([b])
        else:
            if not (case["case_id"] == mr["root_id"]):
                raise C.CarrierError(f"{b['dir']}: case_id differs from root_id")
            C.check_pin(b, mr)
            want = referenced(case, None)
            if set(b["artifacts"]) != want:
                raise C.CarrierError(f"{b['dir']}: artifacts {sorted(b['artifacts'])} differ from the {sorted(want)} the case names")
    manifest = C.read_json(os.path.join(C.BASE_DIR, "manifest.draft.json"))
    cases = manifest["cases"]
    groups = {}
    for b in bundles:
        groups.setdefault(kind_key(b["case"]), []).append(b)
    replaced, retained = [], []
    for key, group in sorted(groups.items()):
        group.sort(key=lambda b: b["case"]["case_id"])
        slots = sorted((i for i, c in enumerate(cases) if kind_key(c) == key), key=lambda i: cases[i]["case_id"])
        if len(group) > len(slots):
            raise C.CarrierError(f"{len(group)} {key} bundles but only {len(slots)} slots")
        for b, i in zip(group, slots):
            replaced.append(cases[i]["case_id"])
            cases[i] = b["case"]
    new_ids = {b["case"]["case_id"] for b in bundles}
    retained_cases = [c for c in cases if c["case_id"] not in new_ids]
    for c in retained_cases:
        if c["kind"] != "compliant" or c["compliant"]["primary_tool"] not in ("write", "edit", "bash"):
            raise C.CarrierError(f"retained case {c['case_id']} is not a compliant write, edit or bash case")
    old_ids = [c["case_id"] for c in retained_cases]
    C.rename_ids(retained_cases, a.suffix)
    retained = [{"case_id": c["case_id"], "t5_case_id": o, "role": c["compliant"]["primary_tool"], "reason": a.retained_reason}
                for c, o in zip(retained_cases, old_ids)]
    ids = [c["case_id"] for c in cases]
    roots = [c["meaning_record"]["root_id"] for c in cases]
    for what, vals in (("case_id", ids), ("root", roots)):
        dup = sorted({v for v in vals if vals.count(v) > 1})
        if dup:
            raise C.CarrierError(f"duplicate {what} {dup[0]}")
    build_mod.assign_partitions([c for c in cases if c["kind"] == "adversarial"], manifest["split_seed"])

    artifacts = C.read_tree(os.path.join(C.BASE_DIR, "artifacts"))
    artifacts = C.merge_files(artifacts, bundles, "artifacts", "artifact")
    attacker = manifest["pins"]["attacker_digest"]
    need = set()
    for c in cases:
        need |= referenced(c, None)
    need.add("attacker-" + attacker.removeprefix("sha256:") + ".json")
    missing = sorted(need - set(artifacts))
    if missing:
        raise C.CarrierError(f"artifacts missing: {missing}")
    dropped = sorted(set(artifacts) - need)
    artifacts = {n: artifacts[n] for n in sorted(need)}

    sources = C.merge_sources(C.read_json(os.path.join(C.PILOT, "sources.json")), bundles)
    licenses = C.merge_files(C.read_tree(os.path.join(C.PILOT, "licenses")), bundles, "licenses", "licence")
    notices = C.read_bytes(os.path.join(C.PILOT, "THIRD_PARTY_NOTICES.md")) + b"\n" + notices_section(a.round, bundles, bool(retained)).encode("utf-8")

    manifest_bytes = C.dump_json(manifest, 2)
    for name in dropped:
        digest = name.split("-", 1)[1].rsplit(".", 1)[0]
        if digest in manifest_bytes.decode("utf-8"):
            raise C.CarrierError(f"the dropped artifact {name} is still named in the manifest")
    os.makedirs(a.out)
    for sub, files in (("artifacts", artifacts), ("licenses", licenses)):
        os.makedirs(os.path.join(a.out, sub))
        for name, data in files.items():
            with open(os.path.join(a.out, sub, name), "wb") as f:
                f.write(data)
    for name, data in (("manifest.draft.json", manifest_bytes), ("sources.json", C.dump_json(sources, 1)), ("THIRD_PARTY_NOTICES.md", notices)):
        with open(os.path.join(a.out, name), "wb") as f:
            f.write(data)
    write_selection(a, records, replaced, retained, dropped, manifest, head, tool_blobs)
    import hashlib
    return hashlib.sha256(manifest_bytes).hexdigest()


def notices_section(round_, bundles, retained):
    pins = {}
    for b in bundles:
        pins[b["notice"]["pin_key"]] = b["notice"]
    lines = [f"## Development round {round_}", "", "| Source | Pinned revision | License | License file |", "|---|---|---|---|"]
    for key in sorted(pins):
        n = pins[key]
        files = ", ".join("`" + f + "`" for f in n["license_files"])
        lines.append(f"| {n['repository']} | `{n['rev']}` | {n['license']} | {files} |")
        if n.get("repo_rev") and n["repo_rev"] != n["rev"]:
            lines.append(f"| {n['repo_url'] if n.get('repo_url') else n['repository']} (inbox files and licence) | `{n['repo_rev']}` | {n['license']} | see above |")
    lines += ["", "Cases:", ""]
    for b in sorted(bundles, key=lambda b: b["case"]["case_id"]):
        n = b["notice"]
        lines.append(f"- `{n['case_id']}`: {n['pin_key']} `{n['source_item_id']}`")
    if retained:
        lines += ["", "The T5 write, edit and bash compliant cases of this carrier keep their MultiPL-E sources above; their ids carry the round suffix."]
    return "\n".join(lines) + "\n"


def write_selection(a, records, replaced, retained, dropped, manifest, head, tool_blobs):
    rows = []
    for r in records.values():
        for c in r["cases"]:
            rows.append({"source": r["pin_key"], **c})
    rec = {"schema": "tbound-f36-devset-selection/v1", "carrier": os.path.basename(a.out.rstrip("/\\")), "round": a.round,
           "split_seed": manifest["split_seed"], "cases": rows, "retained": retained, "replaced_t5_cases": replaced,
           "dropped_artifacts": dropped, "carrier_tool": {"tool_repo_head": head, "tool_blobs": tool_blobs}, "records": records}
    os.makedirs(os.path.dirname(os.path.abspath(a.selection_out)), exist_ok=True)
    with open(a.selection_out, "w", encoding="utf-8", newline="\n") as f:
        json.dump(rec, f, indent=1, ensure_ascii=False)
        f.write("\n")


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("build")
    b.add_argument("--round", required=True)
    b.add_argument("--suffix", required=True)
    b.add_argument("--bundles", action="append", required=True)
    b.add_argument("--records", action="append", required=True)
    b.add_argument("--out", required=True)
    b.add_argument("--selection-out", required=True)
    b.add_argument("--retained-reason", default="T5 case kept under a fresh id", help="why a T5 case stays (recorded per retained case)")
    a = ap.parse_args()
    try:
        print(build(a))
    except C.CarrierError as e:
        print(f"dev_carrier: refused: {e}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
