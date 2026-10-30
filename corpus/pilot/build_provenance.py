#!/usr/bin/env python3
"""Write the T5 provenance snapshot from the git blobs of a base commit.

Usage: python -I build_provenance.py --repo <worktree> --base <commit> --out <new-dir>

Copies the original manifest.draft.json and the seven T5 fixture artifacts byte-for-byte. cases.json
records, per T5 case, the original prompt split into the legitimate task and the injection, the carrier
path, and the sha256 of the original carrier bytes. SHA256SUMS covers every other file in the directory.
It refuses an existing <new-dir>.
"""
import argparse
import base64
import hashlib
import json
import os
import re
import subprocess
import sys

MANIFEST_PATH = "corpus/pilot/manifest.draft.json"
ARTIFACT_DIR = "corpus/pilot/artifacts"
CARRIERS = {
    "PILOT-ADV-A-01": "data/messages.txt",
    "PILOT-ADV-A-02": "data/email-from-lily.txt",
    "PILOT-ADV-A-03": "docs/note.md",
    "PILOT-ADV-A-04": "docs/note.md",
    "PILOT-ADV-A-05": "docs/note.md",
    "PILOT-ADV-A-06": "data/amazon-addresses.txt",
    "PILOT-ADV-A-07": "data/dropbox-listing.txt",
}
PROMPT_SPLIT = re.compile(r'^(?P<legit>.*)\n\nInjected (?P<label>[^:"\n]+): "(?P<instruction>.*)"$', re.DOTALL)


def git_blob(repo, base, path):
    return subprocess.run(["git", "-C", repo, "cat-file", "blob", f"{base}:{path}"],
                          check=True, capture_output=True).stdout


def fixture_entry(fixture_bytes, path):
    for entry in json.loads(fixture_bytes)["entries"]:
        if entry["path"] == path:
            return base64.b64decode(entry["content_b64"])
    sys.exit(f"{path} is not in the fixture")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", required=True)
    ap.add_argument("--base", required=True)
    ap.add_argument("--out", required=True, help="new directory; must not exist")
    args = ap.parse_args()
    if os.path.exists(args.out):
        sys.exit(f"refusing to write into existing {args.out}")
    manifest_bytes = git_blob(args.repo, args.base, MANIFEST_PATH)
    by_id = {case["case_id"]: case for case in json.loads(manifest_bytes)["cases"]}
    files = {"manifest.draft.json": manifest_bytes}
    records = {}
    for cid, carrier in CARRIERS.items():
        prompt = by_id[cid]["trial"]["prompt"]
        match = PROMPT_SPLIT.match(prompt)
        if match is None:
            sys.exit(f"{cid}: prompt has no injection suffix")
        legit, label, instruction = match["legit"], match["label"], match["instruction"]
        if prompt != legit + "\n\nInjected " + label + ': "' + instruction + '"':
            sys.exit(f"{cid}: prompt split does not reconstruct the original")
        fixture_name = "fixture-" + by_id[cid]["adversarial"]["workspace_fixture_digest"][len("sha256:"):] + ".json"
        fixture_bytes = git_blob(args.repo, args.base, f"{ARTIFACT_DIR}/{fixture_name}")
        files[fixture_name] = fixture_bytes
        original_carrier = fixture_entry(fixture_bytes, carrier)
        records[cid] = {
            "original_prompt": prompt,
            "legitimate_prompt": legit,
            "injection_label": label,
            "injection": instruction,
            "carrier": carrier,
            "original_carrier_sha256": hashlib.sha256(original_carrier).hexdigest(),
            "original_fixture_artifact": fixture_name,
        }
    files["cases.json"] = (json.dumps(records, indent=2, ensure_ascii=False) + "\n").encode("utf-8")
    sums = "".join(f"{hashlib.sha256(data).hexdigest()}  {name}\n" for name, data in sorted(files.items()))
    files["SHA256SUMS"] = sums.encode("utf-8")
    os.makedirs(args.out)
    for name, data in files.items():
        with open(os.path.join(args.out, name), "wb") as f:
            f.write(data)
    print(f"{len(files)} files written to {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
