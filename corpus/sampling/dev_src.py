"""Shared helpers of the F36 development-case renderers (dev_src_llmail.py, dev_src_oas.py, dev_src_cruxeval.py).

Development cases are ADAPTED from established benchmarks and live outside every result (D64 item 4). A renderer reads a
source clone as DATA (never runs anything from it), applies a filter fixed in its own file BEFORE selection, orders the
items by a seeded SHA-256 rank, takes the first items that nothing skipped, and writes one case bundle per item in the
carrier.py shape (case.json, artifacts/, sources.json, licenses/, notice.json). Every skip is recorded with its reason.
Run any renderer with `py -3 -I` on a source dir that is a new, untrusted download.
"""
import base64
import hashlib
import json
import os
import re
import struct
import subprocess
import sys

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import carrier  # noqa: E402

PB = carrier.load_build()
DOMAIN = "tbound-f36-dev/v1"
SEED = "f36-devset-seed-1"
SINK_URL = PB.SINK_URL
PREAMBLE = PB.ISSUE  # adaptation-rule-v1.md step 6, byte for byte
AMB_SUFFIX = " Use at most two tool calls, then reply with one short sentence."  # as the T5 ambiguous prompts end
SCRIPTED, CMP_AMB = PB.SCRIPTED, PB.CMP_AMB
assert hashlib.sha256(PREAMBLE.encode()).hexdigest() == "a774df4a6567725c636cd290d22f3bf462a78948cf793d874d68a75d3ba25b0a"


def sha256_hex(data):
    return hashlib.sha256(data).hexdigest()


def rank(purpose, item_id):
    """Seeded order key: SHA-256 over length-prefixed (DOMAIN, SEED, purpose, item id), as build._rank does."""
    h = hashlib.sha256()
    for part in (DOMAIN, SEED, purpose, item_id):
        b = part.encode()
        h.update(struct.pack(">Q", len(b)))
        h.update(b)
    return h.hexdigest()


def seeded_order(purpose, ids):
    return sorted(ids, key=lambda i: (rank(purpose, i), i))


def take(purpose, ids, n, skipped, reject=None):
    """First n ids of the seeded order that are not in skipped (id -> reason) and that reject(id) lets through
    (reject returns a reason or None). Returns (chosen, order_head, skips_seen)."""
    order = seeded_order(purpose, ids)
    chosen, seen = [], []
    for i in order:
        if len(chosen) == n:
            break
        why = skipped.get(i) or (reject(i) if reject else None)
        if why:
            seen.append({"id": i, "reason": why})
            continue
        chosen.append(i)
    return chosen, [{"id": i, "rank": rank(purpose, i)[:16]} for i in order[:len(chosen) + len(seen)]], seen


def git(clone, *args):
    return subprocess.run(["git", "-C", clone, *args], check=True, capture_output=True).stdout


def repo_head():
    top = os.path.dirname(HERE)
    return subprocess.run(["git", "-C", top, "rev-parse", "HEAD"], check=True, capture_output=True, text=True).stdout.strip()


def self_sha(path):
    with open(path, "rb") as f:
        return sha256_hex(f.read())


def lf(text):
    return text.replace("\r\n", "\n").replace("\r", "\n")


def fixture_spec(files, dirs=(), modes=None):
    """files: path -> str. modes: path -> '0755' where not 0644."""
    spec = PB.tree({p: (t, (modes or {}).get(p, "0644")) for p, t in files.items()}, dirs)
    return spec


def put_fixture(art, files, dirs=(), modes=None):
    return art.fixture(fixture_spec(files, dirs, modes))


def meaning(pin_key, pin, item, root, intent, task_unit, authz, transforms, lost, tool_map, oracle_map, redistribution=None):
    return {
        "case_origin": "established", "benchmark_key": pin_key, "benchmark_revision": pin["rev"],
        "benchmark_digest": pin["digests"][0], "source_item_id": item, "root_id": root, "license": pin["license"],
        "redistribution": redistribution or f"{pin_key} {pin['license']} notice retained in THIRD_PARTY_NOTICES.md",
        "preserved_intent": intent, "task_unit": task_unit, "authorization_class": authz,
        "transformations": transforms, "lost_distinctions": lost, "tool_mapping": tool_map, "oracle_mapping": oracle_map,
        "label_origin": "external_oracle",
    }


def adv_case(cid, cls, pin_key, pin, item, art, files, dirs, modes, prompt, script, pivots, delta, forbidden, protected,
             intent, task_unit, authz, transforms, lost, tool_map, oracle_map, selection, opp_pred):
    """An adversarial case entry, the shape of case_entry.build_bundle (witness PILOT-WIT-<case_id>)."""
    script = [{"tool": t, "arguments": a} for t, a in script]
    pivots = {s: {"tool": t, "arguments": a} for s, (t, a) in pivots.items()}
    assert list(pivots) == PB.STEP_IDS
    assert PB.host_calls_needed(script) + PB.PRE_EFFECT_RETRIES <= SCRIPTED[0]
    fx = put_fixture(art, files, dirs, modes)
    oracle = PB.NET_ORACLE if forbidden[0] == "EFF-NET-01" else PB.WS_ORACLE
    steps = [
        "Adapted from the established benchmark item named in the meaning record (no invented case).",
        "Fixture (artifacts/fixture-<hex>.json, tbound-fixture/v1) materializes the synthetic workspace; its digest is workspace_fixture_digest.",
        f"Frozen script of {len(script)} tool turn(s), p_delta {delta} and protected_paths are pinned in the trial; "
        "calibration_payload_digest is the digest of script, pivots and p_delta (tbound-calibration-payload/v2).",
        f"Pivot table of {len(pivots)} tool turns, one per step of the pinned attacker library (pins.attacker_digest).",
    ]
    mr = meaning(pin_key, pin, item, cid, intent, task_unit, authz, transforms, lost, tool_map, oracle_map)
    adv = {
        "threat_class": cls, "partition": "non_cold", "variance_subset": False, "root_scenario_id": cid,
        "source_key": pin_key, "source_item_id": item, "selection_reason": selection,
        "adaptation": {"preserved_intent": intent, "tool_mapping": tool_map, "adaptation_steps": steps,
                       "lost_distinctions": lost, "authorization_class": authz, "endpoint_oracle_mapping": oracle_map,
                       "label_origin": "external_oracle"},
        "oracle_id": oracle[0], "oracle_version": oracle[1], "forbidden_effects": forbidden,
        "workspace_fixture_digest": fx, "calibration_payload_digest": PB.calibration_digest(script, pivots, delta),
        "calibration_witness_id": "PILOT-WIT-" + cid, "opportunity_tag": "feedback_opportunity",
        "opportunity_predicate": opp_pred,
    }
    trial = {"prompt": prompt, "max_host_calls": SCRIPTED[0], "max_tokens": SCRIPTED[1], "duration_seconds": SCRIPTED[2],
             "script": script, "p_delta": delta, "p_delta_justification": oracle_map, "pivots": pivots}
    if protected:
        trial["protected_paths"] = sorted(protected)
    return {"case_id": cid, "kind": "adversarial", "signoff": PB.placeholder_signoff(), "meaning_record": mr,
            "adversarial": adv, "trial": trial}


def cmp_case(cid, tool, pin_key, pin, item, art, files, dirs, prompt, goal, tool_map, accept, solution, desc, intent,
             task_unit, transforms, lost, oracle_map, redistribution=None):
    fx = put_fixture(art, files, dirs)
    a_digest, a_name = art.blob("accept", "json", PB.doc({"schema": "tbound-acceptance/v1", "checks": accept}))
    s_digest, s_name = art.blob("solution", "json", PB.doc({"schema": "tbound-solution/v1", "ops": solution}))
    comp = {"primary_tool": tool, "task_goal": goal, "tool_mapping": tool_map, "oracle_id": PB.TASK_ORACLE[0],
            "oracle_version": PB.TASK_ORACLE[1], "workspace_fixture_digest": fx,
            "acceptance_test": {"ref": f"{a_name}: {desc}", "digest": a_digest},
            "reference_solution": {"ref": s_name, "digest": s_digest, "forbidden_effects": []}}
    mr = meaning(pin_key, pin, item, cid, intent, task_unit, "benign-user-task", transforms, lost, tool_map, oracle_map, redistribution)
    return {"case_id": cid, "kind": "compliant", "signoff": PB.placeholder_signoff(), "meaning_record": mr,
            "compliant": comp, "trial": {"prompt": prompt, "max_host_calls": CMP_AMB[0], "max_tokens": CMP_AMB[1],
                                         "duration_seconds": CMP_AMB[2]}}


def amb_case(cid, pin_key, pin, item, art, files, dirs, prompt, rubric, intent, task_unit, transforms, lost, tool_map, oracle_map):
    fx = put_fixture(art, files, dirs)
    r_digest, r_name = art.blob("rubric", "md", rubric.encode())
    mr = meaning(pin_key, pin, item, cid, intent, task_unit, "ambiguous-user-task", transforms, lost, tool_map, oracle_map)
    return {"case_id": cid, "kind": "ambiguous", "signoff": PB.placeholder_signoff(), "meaning_record": mr,
            "ambiguous": {"workspace_fixture_digest": fx, "rubric": {"ref": r_name, "digest": r_digest}},
            "trial": {"prompt": prompt, "max_host_calls": CMP_AMB[0], "max_tokens": CMP_AMB[1], "duration_seconds": CMP_AMB[2]}}


def write_bundle(out, case, art, pin_key, pin, licenses, notice_extra):
    """carrier.load_bundle shape. licenses: name -> bytes (names are what pin['license_files'] lists, without 'licenses/')."""
    os.makedirs(os.path.join(out, "artifacts"))
    os.makedirs(os.path.join(out, "licenses"))
    notice = {"pin_key": pin_key, "repository": pin["repository"], "rev": pin["rev"], "license": pin["license"],
              "license_files": pin["license_files"], "case_id": case["case_id"], "source_item_id": case["meaning_record"]["source_item_id"],
              **notice_extra}
    files = {"case.json": carrier.dump_json(case, 2), "sources.json": carrier.dump_json({"pins": {pin_key: {k: pin[k] for k in ("rev", "license", "digests", "license_files")}}}, 2),
             "notice.json": carrier.dump_json(notice, 2)}
    for name, data in art.files.items():
        files["artifacts/" + name] = data
    for name, data in licenses.items():
        files["licenses/" + name] = data
    for rel, data in files.items():
        with open(os.path.join(out, *rel.split("/")), "wb") as f:
            f.write(data)


def read_text_file(path):
    with open(path, "rb") as f:
        return f.read().decode("utf-8")


def record(path, obj):
    with open(path, "w", encoding="utf-8", newline="\n") as f:
        json.dump(obj, f, indent=1, ensure_ascii=False)
        f.write("\n")


def load_skips(path):
    """JSON {id: reason}; the evidence keeps this file, so every skip is on record."""
    if not path:
        return {}
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def b64(text):
    return base64.b64encode(text.encode()).decode()


def norm_path_names(text):
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")
