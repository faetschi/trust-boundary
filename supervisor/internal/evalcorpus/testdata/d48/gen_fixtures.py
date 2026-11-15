"""Regenerates the D48 draw test vectors in this directory with the frozen select.py and ledger v3.

TEST VECTORS ONLY. The beacons are fake (randomness = SHA-256 of a made-up signature) and every candidate
passes adaptation, so these are not the registered pilot or main draw. Run from the repository root:

    py -3 -I supervisor/internal/evalcorpus/testdata/d48/gen_fixtures.py
"""
import hashlib
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
SAMPLING = os.path.join("corpus", "sampling")
SELECT = os.path.join(SAMPLING, "select.py")
LEDGER = os.path.join(SAMPLING, "d48-ledger-v3.jsonl")
TEMPLATE = os.path.join(SAMPLING, "pilot-commitment-v1.template.json")
BLS_BY = "test vector: no BLS verification (fake beacon)"


def sha(data):
    return hashlib.sha256(data).hexdigest()


def write(name, data):
    with open(os.path.join(HERE, name), "wb") as handle:
        handle.write(data)
    return os.path.join(HERE, name)


def beacon(template, published_epoch, label):
    genesis, period = template["chain"]["genesis_time"], template["chain"]["period"]
    threshold = published_epoch + 86400
    round_no = max(1, -(-(threshold - genesis) // period) + 1)
    signature = hashlib.sha512(label.encode()).digest() + hashlib.sha256(label.encode()).digest()[:32]
    return json.dumps({"round": round_no, "randomness": sha(signature), "signature": signature.hex()},
                      sort_keys=True).encode()


def run(*args):
    result = subprocess.run([sys.executable, "-I", SELECT, *args], capture_output=True, text=True)
    if result.returncode != 0:
        sys.exit(f"select.py {args[0]} exited {result.returncode}: {result.stderr}")


def main():
    with open(TEMPLATE, "rb") as handle:
        template = json.load(handle)
    with open(LEDGER, "rb") as handle:
        rows = [json.loads(line) for line in handle if line.strip()]
    candidates = [row["case_id"] for row in rows if not row.get("excluded")]
    decisions = write("decisions-all-pass.jsonl", "".join(
        json.dumps({"case_id": c, "pass": True, "reason": "TEST: every candidate passes"}, sort_keys=True) + "\n"
        for c in candidates).encode())
    for name in ("pilot-output.json", "draw-output.json"):
        if os.path.exists(os.path.join(HERE, name)):
            os.remove(os.path.join(HERE, name))

    t1, t1_epoch = "2026-11-14T00:00:00Z", 1794614400
    pilot_commit = write("commitment-pilot.json", json.dumps(template, sort_keys=True, indent=1).encode() + b"\n")
    pilot_beacon = write("beacon-pilot.json", beacon(template, t1_epoch, "tbound-b7s2-test-pilot"))
    run("pilot", "--ledger", LEDGER, "--commitment", pilot_commit, "--beacon", pilot_beacon, "--published-utc", t1,
        "--bls-verified-by", BLS_BY, "--adaptation", decisions, "--out", os.path.join(HERE, "pilot-output.json"))
    with open(os.path.join(HERE, "pilot-output.json"), "rb") as handle:
        pilot = json.load(handle)
    members = sorted(c for group in pilot["memberships"]["pilot"].values() for c in group)

    t2, t2_epoch = "2026-11-20T00:00:00Z", 1795132800
    main_commit = {key: template[key] for key in ("ledger_sha256", "script_sha256", "chain")}
    main_commit.update(kind="main", policy_sha256="0" * 64, adaptation_rule_sha256=template["adaptation_rule_sha256"],
                       development_case_ids=members)
    main_commit = write("commitment-main.json", json.dumps(main_commit, sort_keys=True, indent=1).encode() + b"\n")
    main_beacon = write("beacon-main.json", beacon(template, t2_epoch, "tbound-b7s2-test-main"))
    run("draw", "--ledger", LEDGER, "--commitment", main_commit, "--beacon", main_beacon, "--published-utc", t2,
        "--pilot-output", os.path.join(HERE, "pilot-output.json"), "--bls-verified-by", BLS_BY,
        "--adaptation", decisions, "--out", os.path.join(HERE, "draw-output.json"))
    os.remove(decisions)  # 1,265 lines, rebuilt from the ledger; not needed by the Go tests


if __name__ == "__main__":
    main()
