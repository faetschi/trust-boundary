#!/usr/bin/env python3
"""Write a regenerated pilot draft into a new directory, without touching the frozen corpus.

Usage: python3 -I build_draft.py --tbound-run <binary> --out <new-dir>

Applies build.patch to manifest.draft.json, resets every sign-off and construct_review to the
placeholders, and writes manifest.draft.json and artifacts/ into <new-dir>. It refuses an existing
<new-dir> and deletes nothing. build.py's main() is not used: it rewrites the frozen corpus in place.
"""
import argparse
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ORIGINAL_MANIFEST = os.path.join(HERE, "manifest.draft.json")


def load_build():
    # python -I leaves the script directory off sys.path, so build.py is loaded by file path.
    spec = importlib.util.spec_from_file_location("pilot_build", os.path.join(HERE, "build.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tbound-run", required=True, help="tbound-run binary for `fixture digest`")
    ap.add_argument("--out", required=True, help="new directory; must not exist")
    args = ap.parse_args()
    if os.path.exists(args.out):
        sys.exit(f"refusing to write into existing {args.out}")
    build = load_build()
    with open(ORIGINAL_MANIFEST, encoding="utf-8") as f:
        manifest = json.load(f)
    art = build.Artifacts(args.tbound_run)
    build.patch(manifest, art)
    for case in manifest["cases"]:
        case["signoff"] = build.placeholder_signoff()
    manifest["construct_review"] = build.placeholder_construct_review()
    os.makedirs(os.path.join(args.out, "artifacts"))
    for name, data in art.files.items():
        with open(os.path.join(args.out, "artifacts", name), "wb") as f:
            f.write(data)
    with open(os.path.join(args.out, "manifest.draft.json"), "w", encoding="utf-8", newline="\n") as f:
        json.dump(manifest, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print(f"{len(art.files)} artifacts, manifest written to {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
