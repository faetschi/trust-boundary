"""Rebuild the rehearsal carrier rehearsal-dev-01 from its committed inputs (development rows only).

    py -3 -I corpus/sampling/rehearsal/build_rehearsal.py --tbound-run PATH [--out DIR]

Each rehearsal-dev-01/<safe case id>/ holds judgments.json and calibration.json. For every one this runs
case_entry.py build --draw rehearsal into a temp dir, then carrier.py build --round rehearsal-dev-01. An existing
--out is refused (carrier.py refuses it too, but the check comes first so no bundle work is wasted).
"""
import argparse
import json
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
SAMPLING = os.path.dirname(HERE)
ROUND = "rehearsal-dev-01"
INPUTS = os.path.join(HERE, ROUND)
DEFAULT_OUT = os.path.join(SAMPLING, "carriers", ROUND)


def run(script, *args):
    proc = subprocess.run([sys.executable, "-I", os.path.join(SAMPLING, script), *args], capture_output=True, text=True)
    if proc.returncode:
        sys.exit(f"{script} {' '.join(args[:2])} failed ({proc.returncode}): {proc.stderr.strip() or proc.stdout.strip()}")
    return proc.stdout.strip()


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--tbound-run", required=True)
    ap.add_argument("--out", default=DEFAULT_OUT)
    a = ap.parse_args(argv)
    if os.path.exists(a.out):
        sys.exit(f"refused: --out {a.out} exists")
    with tempfile.TemporaryDirectory() as tmp:
        bundles = []
        for name in sorted(os.listdir(INPUTS)):
            d = os.path.join(INPUTS, name)
            if not os.path.isdir(d):
                continue
            cal = os.path.join(d, "calibration.json")
            with open(cal, encoding="utf-8") as fh:
                case_id = json.load(fh)["case_id"]
            bundle = os.path.join(tmp, name)
            run("case_entry.py", "build", "--case", case_id, "--calibration", cal, "--judgments", os.path.join(d, "judgments.json"),
                "--draw", "rehearsal", "--tbound-run", a.tbound_run, "--out", bundle)
            bundles += ["--bundle", bundle]
        print("manifest sha256:", run("carrier.py", "build", "--round", ROUND, *bundles, "--out", a.out))


if __name__ == "__main__":
    main()
