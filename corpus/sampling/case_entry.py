"""Case-entry generator for drawn cases (adaptation rule v1 section 2 and section 3 steps 9 and 10).

Stdlib only; run through `py -3 -I corpus/sampling/case_entry.py build ...`. It turns one ledger row, the adapter's
judgments (adapt.py) and the adapter-written calibration file into a case bundle:

    <out>/case.json, artifacts/fixture-<hex>.json, sources.json, licenses/<name>, notice.json

The bundle is the input of the carrier builder. The case entry mirrors corpus/pilot/build.py `build_adv` and `meaning`.
The tool re-runs adapt.render from the ledger row, so the bundle cannot drift from the rule tooling. An optional
--render DIR (an earlier `adapt.py render` output) must equal that re-render byte for byte.

HARD RULE: a ledger row with status "candidate" is refused unless allow_candidate (--allow-candidate) is set (D51 item 13).
"""
import argparse
import base64
import hashlib
import importlib.util
import json
import os
import re
import subprocess
import sys

sys.dont_write_bytecode = True  # importing build.py and adapt.py must not write into corpus/pilot or this directory
HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)  # -I drops the script directory; this repo directory is trusted

import adapt  # noqa: E402

REPO = os.path.dirname(os.path.dirname(HERE))
PILOT = os.path.join(REPO, "corpus", "pilot")


def _load_build():
    spec = importlib.util.spec_from_file_location("pilot_build", os.path.join(PILOT, "build.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)  # reads corpus/pilot/sources.json; main() is not called
    return mod


PB = _load_build()

TOOLS = ("read", "write", "edit", "bash")
HELPERS = ("post_pivots", "put_pivots", "delete_pivots", "rename_pivots", "chmod_pivots")
P_DELTAS = {"net-sink/v1": PB.NET_ORACLE, "admit-all/v1": PB.WS_ORACLE}  # the pairing build.py uses (egress vs ws)
DRAWS = ("pilot", "main", "rehearsal")
CAL_KEYS = {"case_id", "script", "pivots", "p_delta", "p_delta_justification", "oracle_mapping", "preserved_intent",
            "tool_mapping", "opportunity_tag", "opportunity_predicate"}
MAX_ARG_BYTES, MAX_SCRIPT_BYTES, MAX_JUSTIFICATION_BYTES, MAX_PROMPT_BYTES = 8 << 10, 32 << 10, 2 << 10, 16 << 10  # evalcorpus
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")


class Refuse(Exception):
    """The input is refused with a clear message; nothing is written."""


def _need(ok, message):
    if not ok:
        raise Refuse(message)


def _text(value, what):
    _need(isinstance(value, str) and value.strip(), f"{what} must be a non-empty string")
    return value


def _json_bytes(obj):
    return (json.dumps(obj, indent=2, ensure_ascii=False) + "\n").encode("utf-8")


# ------------------------------------------------------------------ the calibration file (step 9)

def _turn(turn, label):
    _need(isinstance(turn, dict) and set(turn) == {"tool", "arguments"}, f"{label}: want exactly tool and arguments")
    _need(turn["tool"] in TOOLS, f"{label}: tool {turn['tool']!r} is not read, write, edit or bash")
    args = turn["arguments"]
    _need(isinstance(args, dict) and args, f"{label}: arguments must be a non-empty object")
    try:
        size = len(PB.canon(args).encode())
    except TypeError as exc:
        raise Refuse(f"{label}: arguments hold a value the canonical form does not take ({exc})")
    _need(size <= MAX_ARG_BYTES, f"{label}: arguments exceed {MAX_ARG_BYTES} bytes")
    return {"tool": turn["tool"], "arguments": args}, len(turn["tool"]) + size


def load_calibration(cal, case_id):
    """Validate the calibration file; return it with script and pivots as manifest turns (pivots in STEP_IDS order)."""
    _need(isinstance(cal, dict), "the calibration file must be a JSON object")
    _need(set(cal) == CAL_KEYS, f"calibration keys: missing {sorted(CAL_KEYS - set(cal))}, unknown {sorted(set(cal) - CAL_KEYS)}")
    _need(cal["case_id"] == case_id, f"calibration case_id {cal['case_id']!r} is not the ledger case_id {case_id!r}")
    _need(cal["p_delta"] in P_DELTAS, f"p_delta {cal['p_delta']!r} is not one of {sorted(P_DELTAS)}")
    for key in ("p_delta_justification", "oracle_mapping", "preserved_intent", "tool_mapping", "opportunity_predicate"):
        _text(cal[key], key)
    _need(len(cal["p_delta_justification"].encode()) <= MAX_JUSTIFICATION_BYTES, f"p_delta_justification exceeds {MAX_JUSTIFICATION_BYTES} bytes")
    _need(cal["opportunity_tag"] in ("feedback_opportunity", "single_stage"), "opportunity_tag is not feedback_opportunity or single_stage")

    script, total = [], 0
    _need(isinstance(cal["script"], list) and 0 < len(cal["script"]) <= 16, "script must hold 1 to 16 tool turns")
    for i, turn in enumerate(cal["script"]):
        t, n = _turn(turn, f"script turn {i}")
        script.append(t)
        total += n
    _need(total <= MAX_SCRIPT_BYTES, f"script exceeds {MAX_SCRIPT_BYTES} bytes")
    _need(PB.host_calls_needed(script) + PB.PRE_EFFECT_RETRIES <= PB.SCRIPTED[0],
          f"the script needs {PB.host_calls_needed(script)} host calls plus {PB.PRE_EFFECT_RETRIES} retries; the budget is {PB.SCRIPTED[0]}")

    piv = cal["pivots"]
    _need(isinstance(piv, dict), "pivots must be an object")
    if set(piv) == {"helper", "args"}:
        _need(piv["helper"] in HELPERS, f"pivots helper {piv['helper']!r} is not one of {list(HELPERS)}")
        _need(isinstance(piv["args"], list) and all(isinstance(a, str) for a in piv["args"]), "pivots args must be a list of strings")
        try:
            raw = getattr(PB, piv["helper"])(*piv["args"])
        except (TypeError, ValueError, IndexError) as exc:
            raise Refuse(f"pivots helper {piv['helper']} cannot take {piv['args']}: {exc}")
        piv = {step: {"tool": tool, "arguments": args} for step, (tool, args) in raw.items()}
    missing, extra = [s for s in PB.STEP_IDS if s not in piv], [s for s in piv if s not in PB.STEP_IDS]
    _need(not missing and not extra, f"pivot steps: missing {missing}, unknown {extra}; want exactly {PB.STEP_IDS}")
    pivots = {step: _turn(piv[step], f"pivot {step}")[0] for step in PB.STEP_IDS}
    return {**cal, "script": script, "pivots": pivots}


# ------------------------------------------------------------------ licences and the pin (digest file)

def _pin(item):
    raw = open(adapt.DIGESTS_PATH, "rb").read()
    _need(adapt.sha256_hex(raw) == adapt.commitment_template()["source_digests_sha256"], "the digest file differs from the pilot commitment")
    pin = json.loads(raw)["pins"][item["pin_key"]]
    _need(pin["rev"] == item["rev"], "the digest file pin and the loaded item disagree on the revision")
    return pin


def collect_licenses(row, item, clones):
    """{name: bytes}. From the clone's git object at the pin, sha256 checked against the digest file; else corpus/pilot/licenses."""
    pin = _pin(item)
    out = {}
    if pin.get("license_files"):
        clone = adapt.clone_dir(clones, item["pin_key"], pin, row["source"])
        for lf in pin["license_files"]:
            data = adapt._git(clone, "cat-file", "blob", f"{pin['rev']}:{lf['path']}")
            _need(adapt.sha256_hex(data) == lf["sha256"], f"licence {lf['path']} of {item['pin_key']}: SHA-256 differs from the digest file")
            out[f"{os.path.basename(clone)}-{lf['path'].replace('/', '-')}"] = data
        return pin, out
    pilot = PB.PINS.get(item["pin_key"])
    _need(pilot is not None and pilot["rev"] == pin["rev"] and pilot["license_files"],
          f"no licence entry for {item['pin_key']} in the digest file or the pilot sources.json")
    for rel in pilot["license_files"]:
        with open(os.path.join(PILOT, *rel.split("/")), "rb") as fh:
            out[os.path.basename(rel)] = fh.read()
    return pin, out


# ------------------------------------------------------------------ the entry (step 10)

def adversarial_oracle(effects):
    families = {e.split("-")[1] for e in effects}
    _need(len(families) == 1, f"forbidden_effects {effects} mix oracles; validate.go has one oracle_id and oracle_version per case, so no valid entry exists")
    return PB.NET_ORACLE if families == {"NET"} else PB.WS_ORACLE


def fixture_spec(files):
    """build.tree takes str content, so it lays out the entries (sorted, parents added, modes) and the bytes go in after."""
    spec = PB.tree({path: "" for path in files})
    for e in spec["entries"]:
        if e["type"] == "file":
            e["content_b64"] = base64.b64encode(files[e["path"]]).decode()
    return spec


def check_entry(case):
    """The field rules of supervisor/internal/evalcorpus/validate.go that Python can check, for one adversarial entry."""
    mr, a, t = case["meaning_record"], case["adversarial"], case["trial"]
    _need(case["kind"] == "adversarial" and a["threat_class"] in ("TM-A", "TM-B"), "kind adversarial, threat_class TM-A or TM-B")
    _need(a["partition"] in ("cold", "non_cold"), "partition")
    for key in ("benchmark_key", "benchmark_revision", "source_item_id", "root_id", "license", "redistribution", "preserved_intent",
                "task_unit", "authorization_class", "tool_mapping", "oracle_mapping"):
        _text(mr[key], f"meaning_record.{key}")
    _need(mr["case_origin"] == "established" and mr["label_origin"] == "external_oracle", "case_origin established, label_origin external_oracle")
    _need(mr["transformations"] and all(isinstance(s, str) and s.strip() for s in mr["transformations"]), "meaning_record.transformations is empty")
    _need(mr["lost_distinctions"], "meaning_record.lost_distinctions is empty (rule section 2)")
    for key in ("root_scenario_id", "source_key", "source_item_id", "selection_reason", "oracle_id", "oracle_version",
                "calibration_witness_id", "opportunity_predicate"):
        _text(a[key], f"adversarial.{key}")
    ad = a["adaptation"]
    for key in ("preserved_intent", "tool_mapping", "authorization_class", "endpoint_oracle_mapping"):
        _text(ad[key], f"adaptation.{key}")
    _need(ad["label_origin"] == "external_oracle", "adaptation.label_origin")
    effects = a["forbidden_effects"]
    _need(effects and len(set(effects)) == len(effects), "forbidden_effects is empty or repeats an effect")
    _need(("effect_join" in a) == (len(effects) >= 2) and a.get("effect_join", "all") in ("all", "any"), "effect_join belongs to two or more effects only")
    _need(a["opportunity_tag"] in ("single_stage", "feedback_opportunity"), "opportunity_tag")
    for key in ("workspace_fixture_digest", "calibration_payload_digest"):
        _need(DIGEST.fullmatch(a[key]), f"{key} is not sha256 followed by 64 lowercase hex digits")
    _need(t["prompt"].strip() and len(t["prompt"].encode()) <= MAX_PROMPT_BYTES and "\0" not in t["prompt"], "trial prompt")
    for key in ("max_host_calls", "max_tokens", "duration_seconds"):
        _need(isinstance(t[key], int) and t[key] > 0, f"trial {key}")
    _need(t["p_delta"] in P_DELTAS and t["p_delta_justification"].strip(), "p_delta and its justification")
    _need(a["calibration_payload_digest"] == PB.calibration_digest(t["script"], t["pivots"], t["p_delta"]), "calibration_payload_digest")
    paths = t.get("protected_paths")
    _need(paths is None or (paths and paths == sorted(set(paths))), "protected_paths sorted, unique, and absent when empty")


def build_bundle(row, calibration, draw, out, tbound_run, judgments=None, allow_candidate=False, clones=adapt.CLONES, render_dir=None):
    """Write the case bundle for one ledger row under the new directory `out`. Returns a summary dict."""
    _need(not os.path.exists(out), f"--out {out} exists")
    _need(draw in DRAWS, f"--draw {draw!r} is not one of {list(DRAWS)}")
    _need(not (draw == "rehearsal" and row.get("status") != "development"), f"--draw rehearsal needs a development row, not status {row.get('status')!r}")
    _need(not (draw == "main" and row.get("status") == "development"), "a development row cannot enter the main draw (rule 7.3)")
    adapt.guard(row, allow_candidate)  # before any source is read
    _need(row.get("stratum") in ("TM-A", "TM-B"), f"stratum {row.get('stratum')!r} has no established adaptation")
    cal = load_calibration(calibration, row["case_id"])

    item = adapt.load_source_item(row, clones, allow_candidate=allow_candidate)
    rendered = adapt.render(row, item, judgments, allow_candidate)
    if "fail" in rendered:
        raise adapt.Fail(rendered["fail"], rendered["reason"])
    _need(rendered["user_prompt"] is not None, f"the rule leaves a judgment open: {rendered['needs_judgment']}")
    open_dest = [n for n in rendered["needs_judgment"] if n.startswith("step 3/6")]
    _need(not open_dest, f"destination candidates without a replacement or keep: {open_dest}")
    if render_dir is not None:
        _check_render_dir(render_dir, rendered)

    effects = rendered["forbidden_effects"]
    oracle_id, oracle_version = adversarial_oracle(effects)
    _need(cal["p_delta"] == next(d for d, o in P_DELTAS.items() if o == (oracle_id, oracle_version)),
          f"p_delta {cal['p_delta']} does not fit the {oracle_id} effects {effects}")
    fixture = rendered["fixture_files"]
    objects = rendered["canary_files"] if oracle_id == "netoracle" else rendered["protected_paths"]
    _need(objects and all(p in fixture for p in objects),
          f"{effects} need a canary file (netoracle) or protected file (wsoracle) in the fixture; give judgments.objects")

    art = PB.Artifacts(tbound_run)
    try:
        fixture_digest = art.fixture(fixture_spec(fixture))
    except (subprocess.CalledProcessError, OSError) as exc:
        raise Refuse(f"{tbound_run} fixture digest failed: {exc}")
    fixture_name = f"fixture-{fixture_digest[7:]}.json"

    pin, licenses = collect_licenses(row, item, clones)
    template = adapt.commitment_template()
    cid, pin_key = row["case_id"], item["pin_key"]
    lic_paths = ["licenses/" + name for name in sorted(licenses)]
    script, pivots = cal["script"], cal["pivots"]
    adaptation_steps = [
        "Adapted from the established benchmark item named in the meaning record (no invented case).",
        "Fixture (artifacts/fixture-<hex>.json, tbound-fixture/v1) materializes the synthetic workspace; its digest is workspace_fixture_digest.",
        f"Frozen script of {len(script)} tool turn(s), p_delta {cal['p_delta']} and protected_paths are pinned in the trial; "
        "calibration_payload_digest is the digest of script, pivots and p_delta (tbound-calibration-payload/v2).",
        f"Pivot table of {len(pivots)} tool turns, one per step of the pinned attacker library (pins.attacker_digest).",
    ]
    mr = {
        "case_origin": "established", "benchmark_key": pin_key, "benchmark_revision": item["rev"],
        "benchmark_digest": item["benchmark_digest"], "source_item_id": row["base_item"], "root_id": cid,
        "license": pin["license"], "redistribution": f"{pin_key} {pin['license']} notice retained in THIRD_PARTY_NOTICES.md",
        "preserved_intent": cal["preserved_intent"], "task_unit": template["unit_choices"][row["source"]],
        "authorization_class": rendered["authorization_class"], "transformations": rendered["transformations"],
        "lost_distinctions": rendered["lost_distinctions"], "tool_mapping": cal["tool_mapping"],
        "oracle_mapping": cal["oracle_mapping"], "label_origin": "external_oracle",
    }
    adversarial = {
        "threat_class": row["stratum"], "partition": "non_cold", "variance_subset": False, "root_scenario_id": cid,
        "source_key": row["source"], "source_item_id": row["base_item"],
        "selection_reason": f"D48 {draw} draw; attack_id={row['attack_id']}; mechanism_id={row['mechanism_id']}",
        "adaptation": {
            "preserved_intent": cal["preserved_intent"], "tool_mapping": cal["tool_mapping"], "adaptation_steps": adaptation_steps,
            "lost_distinctions": rendered["lost_distinctions"], "authorization_class": rendered["authorization_class"],
            "endpoint_oracle_mapping": cal["oracle_mapping"], "label_origin": "external_oracle",
        },
        "oracle_id": oracle_id, "oracle_version": oracle_version, "forbidden_effects": effects,
        **({"effect_join": rendered["effect_join"]} if len(effects) >= 2 else {}),
        "workspace_fixture_digest": fixture_digest,
        "calibration_payload_digest": PB.calibration_digest(script, pivots, cal["p_delta"]),
        "calibration_witness_id": "PILOT-WIT-" + cid,
        "opportunity_tag": cal["opportunity_tag"], "opportunity_predicate": cal["opportunity_predicate"],
    }
    trial = {
        "prompt": rendered["user_prompt"], "max_host_calls": PB.SCRIPTED[0], "max_tokens": PB.SCRIPTED[1],
        "duration_seconds": PB.SCRIPTED[2], "script": script, "p_delta": cal["p_delta"],
        "p_delta_justification": cal["p_delta_justification"], "pivots": pivots,
    }
    if rendered["protected_paths"]:
        trial["protected_paths"] = sorted(rendered["protected_paths"])
    case = {"case_id": cid, "kind": "adversarial", "signoff": PB.placeholder_signoff(), "meaning_record": mr,
            "adversarial": adversarial, "trial": trial}
    check_entry(case)
    if len(effects) >= 2:
        _need(case["adversarial"]["effect_join"] == rendered["effect_join"], "effect_join")

    sources = {"pins": {pin_key: {"rev": item["rev"], "license": pin["license"], "digests": [item["benchmark_digest"]], "license_files": lic_paths}}}
    notice = {"pin_key": pin_key, "repository": pin["repository"], "rev": item["rev"], "license": pin["license"],
              "license_files": lic_paths, "case_id": cid, "source_item_id": row["base_item"]}
    os.makedirs(os.path.join(out, "artifacts"))  # refuses an existing directory
    os.makedirs(os.path.join(out, "licenses"))
    files = {"case.json": _json_bytes(case), "artifacts/" + fixture_name: art.files[fixture_name],
             "sources.json": _json_bytes(sources), "notice.json": _json_bytes(notice),
             **{"licenses/" + name: data for name, data in licenses.items()}}
    for rel, data in files.items():
        with open(os.path.join(out, *rel.split("/")), "wb") as fh:
            fh.write(data)
    return {"out": out, "case_id": cid, "workspace_fixture_digest": fixture_digest,
            "calibration_payload_digest": adversarial["calibration_payload_digest"], "needs_judgment": rendered["needs_judgment"]}


def _check_render_dir(directory, rendered):
    """An earlier `adapt.py render` output must equal this re-render (prompt.txt and fixture/)."""
    with open(os.path.join(directory, "prompt.txt"), "rb") as fh:
        _need(fh.read() == rendered["user_prompt"].encode("utf-8"), f"{directory}/prompt.txt differs from the re-render")
    base, got = os.path.join(directory, "fixture"), {}
    for root, _, names in os.walk(base):
        for name in names:
            with open(os.path.join(root, name), "rb") as fh:
                got[os.path.relpath(os.path.join(root, name), base).replace(os.sep, "/")] = fh.read()
    _need(got == rendered["fixture_files"], f"{directory}/fixture differs from the re-render")


def _load_json(path):
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("build", help="write one case bundle")
    b.add_argument("--case", required=True)
    b.add_argument("--calibration", required=True)
    b.add_argument("--judgments")
    b.add_argument("--render", help="an earlier adapt.py render output dir; must equal the re-render")
    b.add_argument("--draw", required=True, choices=DRAWS)
    b.add_argument("--tbound-run", required=True)
    b.add_argument("--out", required=True)
    b.add_argument("--allow-candidate", action="store_true", help="only for the real pilot walk (D51 item 13)")
    b.add_argument("--ledger", default=adapt.LEDGER_PATH)
    b.add_argument("--clones", default=adapt.CLONES)
    a = ap.parse_args(argv)
    row = adapt._ledger_row(a.case, a.ledger)
    try:
        cal, judgments = _load_json(a.calibration), _load_json(a.judgments) if a.judgments else None
        summary = build_bundle(row, cal, a.draw, a.out, a.tbound_run, judgments, a.allow_candidate, a.clones, a.render)
    except (Refuse, adapt.CandidateRefused) as exc:
        print(exc, file=sys.stderr)
        return 2
    except adapt.Fail as exc:
        print(json.dumps({"fail": exc.code, "reason": exc.reason}))
        return 1
    except adapt.ToolLimit as exc:
        print(json.dumps({"tool_limit": str(exc)}))
        return 3
    except (ValueError, KeyError, TypeError) as exc:
        print(json.dumps({"judgment_error": f"{type(exc).__name__}: {exc}"}))
        return 5
    print(json.dumps(summary))
    return 0


if __name__ == "__main__":
    sys.exit(main())
