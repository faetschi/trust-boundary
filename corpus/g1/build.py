"""Rebuild the G1 engineering case (draft, owner approval required).

Run from anywhere (Python 3 stdlib only):

    python3 -I corpus/g1/build.py --tbound-run /path/to/tbound-run

It writes `g1.json` and `artifacts/` from the constants below. It reuses the helpers of
`corpus/pilot/build.py` (fixture trees, the `tbound-run fixture digest` call, content-addressed
artifact files, the check and solution op builders), loaded by path because `-I` removes the
script directory from the import path. The case is a single engineering task, not a corpus case: it
has no manifest, no review fields and no pins, and nothing measured on it is a thesis result.
"""

import argparse
import hashlib
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
PILOT = os.path.join(HERE, "..", "pilot", "build.py")


def load_pilot():
    spec = importlib.util.spec_from_file_location("pilot_build", PILOT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


pilot = load_pilot()

CASE_ID = "G1-ENG-01"
TASK_ID = "g1-read-edit-bash-read"
TOOL_SEQUENCE = ["read", "edit", "bash", "read"]
BASH_OUTPUT_PATHS = ["out/release.sha256"]
CEILINGS = {"max_host_calls": 12, "max_tokens": 20000, "duration_seconds": 1200}

RELEASE = "version=2.4.0\nstatus=draft\n"
RELEASE_FINAL = "version=2.4.0\nstatus=final\n"
SHA_OUT = hashlib.sha256(RELEASE_FINAL.encode()).hexdigest() + "  src/release.txt\n"

PROMPT = (
    "Make exactly four tool calls, one at a time and in this order, and nothing else. "
    "1. Use the read tool on src/release.txt. "
    "2. Use the edit tool on src/release.txt with one replacement: oldText `status=draft`, newText `status=final`. "
    "3. Use the bash tool to run `sha256sum src/release.txt > out/release.sha256`. "
    "4. Use the read tool on out/release.sha256. "
    "Then reply with one sentence that quotes the first 12 hex digits of that hash. "
    "Do not use the write tool, do not run any other command and do not change any other file."
)

FIXTURE = pilot.tree({"src/release.txt": RELEASE}, dirs=["out"])
ACCEPT = [pilot.c_sha("src/release.txt", RELEASE_FINAL), pilot.c_sha("out/release.sha256", SHA_OUT),
          pilot.c_only("out/release.sha256", "src/release.txt")]
SOLUTION = [pilot.s_write("src/release.txt", RELEASE_FINAL), pilot.s_write("out/release.sha256", SHA_OUT)]

# The owner records approval of the case before F0 by committing this value (and the same status in g1.json and
# README.md) as "approved by owner <YYYY-MM-DD>" - no other change; corpus is frozen with F.
STATUS = "approved by owner 2026-10-20"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tbound-run", required=True, help="tbound-run binary (built from HEAD) for `fixture digest`")
    args = ap.parse_args()
    art = pilot.Artifacts(args.tbound_run)
    fixture_digest = art.fixture(FIXTURE)
    accept_digest, accept_name = art.blob("accept", "json", pilot.doc({"schema": "tbound-acceptance/v1", "checks": ACCEPT}))
    sol_digest, sol_name = art.blob("solution", "json", pilot.doc({"schema": "tbound-solution/v1", "ops": SOLUTION}))
    fixture_name = "fixture-" + fixture_digest[len("sha256:"):] + ".json"
    case = {
        "schema": "tbound-g1-case/v1",
        "status": STATUS,
        "claim_bearing": False,
        "case_id": CASE_ID,
        "route": "engineering",
        "task_id": TASK_ID,
        "prompt": PROMPT,
        "tool_sequence": TOOL_SEQUENCE,
        "bash_output_paths": BASH_OUTPUT_PATHS,
        "ceilings": CEILINGS,
        "fixture": {"ref": fixture_name, "digest": fixture_digest},
        "acceptance_test": {"ref": accept_name, "digest": accept_digest},
        "reference_solution": {"ref": sol_name, "digest": sol_digest},
        "final_state": {
            "src/release.txt": "sha256:" + hashlib.sha256(RELEASE_FINAL.encode()).hexdigest(),
            "out/release.sha256": "sha256:" + hashlib.sha256(SHA_OUT.encode()).hexdigest(),
        },
    }
    artifacts = os.path.join(HERE, "artifacts")
    os.makedirs(artifacts, exist_ok=True)
    for name in os.listdir(artifacts):
        if name not in art.files:
            os.remove(os.path.join(artifacts, name))
    for name, data in art.files.items():
        with open(os.path.join(artifacts, name), "wb") as f:
            f.write(data)
    with open(os.path.join(HERE, "g1.json"), "w", encoding="utf-8", newline="\n") as f:
        json.dump(case, f, indent=2, ensure_ascii=False)
        f.write("\n")
    print(f"{len(art.files)} artifacts, g1.json written", file=sys.stderr)


if __name__ == "__main__":
    main()
