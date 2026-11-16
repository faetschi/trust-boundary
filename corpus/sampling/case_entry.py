"""Case-entry generator for drawn cases (adaptation rule v1 section 2 and section 3 steps 9 and 10).

Stdlib only; run through `py -3 -I corpus/sampling/case_entry.py build ...`. It turns one ledger row, the adapter's
judgments (adapt.py) and the adapter-written calibration file into a case bundle:

    <out>/case.json, artifacts/fixture-<hex>.json, sources.json, licenses/<name>, notice.json

The bundle is the input of the carrier builder. The case entry mirrors corpus/pilot/build.py `build_adv` and `meaning`.
The tool re-runs adapt.render from the ledger row, so the bundle cannot drift from the rule tooling.

HARD RULE: a ledger row with status "candidate" is refused unless allow_candidate (--allow-candidate) is set (D51 item 13).
"""
import argparse
import base64
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
import carrier  # noqa: E402

REPO = os.path.dirname(os.path.dirname(HERE))
PILOT = os.path.join(REPO, "corpus", "pilot")


PB = carrier.load_build()

TOOLS = ("read", "write", "edit", "bash")
HELPERS = ("post_pivots", "put_pivots", "delete_pivots", "rename_pivots", "chmod_pivots")
FAMILY_DELTA = {"NET": "net-sink/v1", "WS": "admit-all/v1"}  # the pairing build.py uses (egress vs ws)
MIXED_ALL_DELTA = "net-sink+admit-all/v1"  # evalcorpus.PDeltaNetSinkAdmitAll (D55): P must show a NET and a WS part at once
P_DELTAS = set(FAMILY_DELTA.values()) | {MIXED_ALL_DELTA}
DRAWS = ("pilot", "main", "rehearsal")
CAL_KEYS = {"case_id", "script", "pivots", "p_delta", "p_delta_justification", "oracle_mapping", "preserved_intent",
            "tool_mapping", "opportunity_tag", "opportunity_predicate"}


class Refuse(Exception):
    """The input is refused with a clear message; nothing is written."""


def _need(ok, message):
    if not ok:
        raise Refuse(message)


def _text(value, what):
    _need(isinstance(value, str) and value.strip(), f"{what} must be a non-empty string")
    return value


# ------------------------------------------------------------------ the calibration file (step 9)

def _turn(turn, label):
    _need(isinstance(turn, dict) and set(turn) == {"tool", "arguments"}, f"{label}: want exactly tool and arguments")
    _need(turn["tool"] in TOOLS, f"{label}: tool {turn['tool']!r} is not read, write, edit or bash")
    args = turn["arguments"]
    _need(isinstance(args, dict) and args, f"{label}: arguments must be a non-empty object")
    try:
        PB.canon(args)
    except TypeError as exc:
        raise Refuse(f"{label}: arguments hold a value the canonical form does not take ({exc})")
    return {"tool": turn["tool"], "arguments": args}


def load_calibration(cal, case_id):
    """Validate the calibration file; return it with script and pivots as manifest turns (pivots in STEP_IDS order)."""
    _need(isinstance(cal, dict), "the calibration file must be a JSON object")
    _need(set(cal) == CAL_KEYS, f"calibration keys: missing {sorted(CAL_KEYS - set(cal))}, unknown {sorted(set(cal) - CAL_KEYS)}")
    _need(cal["case_id"] == case_id, f"calibration case_id {cal['case_id']!r} is not the ledger case_id {case_id!r}")
    _need(cal["p_delta"] in P_DELTAS, f"p_delta {cal['p_delta']!r} is not one of {sorted(P_DELTAS)}")
    for key in ("p_delta_justification", "oracle_mapping", "preserved_intent", "tool_mapping", "opportunity_predicate"):
        _text(cal[key], key)
    _need(cal["opportunity_tag"] in ("feedback_opportunity", "single_stage"), "opportunity_tag is not feedback_opportunity or single_stage")

    _need(isinstance(cal["script"], list) and cal["script"], "script must be a non-empty list of tool turns")
    script = [_turn(turn, f"script turn {i}") for i, turn in enumerate(cal["script"])]
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
    pivots = {step: _turn(piv[step], f"pivot {step}") for step in PB.STEP_IDS}
    return {**cal, "script": script, "pivots": pivots}


# ------------------------------------------------------------------ licences and the pin (digest file)

def collect_licenses(row, item, clones):
    """(pin, {name: bytes}): the licence files from the clone's git object at the pin, SHA-256 checked against the digest file."""
    with open(adapt.DIGESTS_PATH, "rb") as fh:
        raw = fh.read()
    _need(adapt.sha256_hex(raw) == adapt.commitment_template()["source_digests_sha256"], "the digest file differs from the pilot commitment")
    pin = json.loads(raw)["pins"][item["pin_key"]]
    _need(pin["rev"] == item["rev"], "the digest file pin and the loaded item disagree on the revision")
    clone = adapt.clone_dir(clones, item["pin_key"], pin, row["source"])
    out = {}
    for lf in pin["license_files"]:
        data = adapt._git(clone, "cat-file", "blob", f"{pin['rev']}:{lf['path']}")
        _need(adapt.sha256_hex(data) == lf["sha256"], f"licence {lf['path']} of {item['pin_key']}: SHA-256 differs from the digest file")
        out[f"{os.path.basename(clone)}-{lf['path'].replace('/', '-')}"] = data
    return pin, out


# ------------------------------------------------------------------ the entry (step 10)

def oracle_for(effects, effect_join, p_delta, rendered):
    """Oracle id and version from the first forbidden effect (rule section 3 step 8), after the objects and p_delta checks."""
    fam = [e.split("-")[1] for e in effects]
    oracle = PB.NET_ORACLE if fam[0] == "NET" else PB.WS_ORACLE
    fixture = rendered["fixture_files"]
    need = {"NET": ("canary_files", "canary file"), "WS": ("protected_paths", "protected file")}
    for f in sorted(set(fam)):
        key, what = need[f]
        _need(rendered[key] and all(p in fixture for p in rendered[key]),
              f"{effects} need a {what} ({f}) in the fixture; give judgments.objects")
    # `any`: the calibration realizes the first part (rule step 8), so it takes that part's delta
    want = MIXED_ALL_DELTA if len(set(fam)) > 1 and effect_join == "all" else FAMILY_DELTA[fam[0]]
    _need(p_delta == want, f"p_delta {p_delta} does not fit the effects {effects} (effect_join {effect_join}): use {want}")
    return oracle


def _mode(mode, path):
    if mode == "+x":
        return "0755"
    _need(isinstance(mode, str) and re.fullmatch(r"[0-7]{1,4}", mode), f"chmod mode {mode!r} of {path} cannot be mapped to a fixture mode")
    return mode.zfill(4)


def fixture_spec(files, modes=None, dirs=()):
    """build.tree takes str content, so it lays out the entries (sorted, parents added, modes) and the bytes go in after."""
    modes = {path: _mode(m, path) for path, m in (modes or {}).items()}
    spec = PB.tree({path: ("", modes.get(path, "0644")) for path in files}, dirs)
    entries = {e["path"]: e for e in spec["entries"]}
    for path, mode in modes.items():
        _need(path in entries, f"chmod of {path}, which is not in the fixture")
        entries[path]["mode"] = mode
    for path, data in files.items():
        entries[path]["content_b64"] = base64.b64encode(data).decode()
    return spec


def build_bundle(row, calibration, draw, out, tbound_run, judgments=None, allow_candidate=False, clones=adapt.CLONES,
                 render_log=None, command=""):
    """Write the case bundle for one ledger row under the new directory `out`. Returns a summary dict."""
    _need(not os.path.exists(out), f"--out {out} exists")
    _need(draw in DRAWS, f"--draw {draw!r} is not one of {list(DRAWS)}")
    _need(not (draw == "rehearsal" and row.get("status") != "development"), f"--draw rehearsal needs a development row, not status {row.get('status')!r}")
    _need(not (draw != "rehearsal" and row.get("status") == "development"), f"a development row cannot enter the {draw} draw (rule 7.3)")
    adapt.guard(row, allow_candidate)  # before any source is read
    adapt.log_render(render_log, row, "case_entry.py", command, allow_candidate)
    _need(row.get("stratum") in ("TM-A", "TM-B"), f"stratum {row.get('stratum')!r} has no established adaptation")
    cal = load_calibration(calibration, row["case_id"])

    item = adapt.load_source_item(row, clones, allow_candidate=allow_candidate)
    rendered = adapt.render(row, item, judgments, allow_candidate)
    if "fail" in rendered:
        raise adapt.Fail(rendered["fail"], rendered["reason"])
    _need(rendered["user_prompt"] is not None, f"the rule leaves a judgment open: {rendered['needs_judgment']}")
    open_dest = [n for n in rendered["needs_judgment"] if n.startswith("step 3/6")]
    _need(not open_dest, f"destination candidates without a replacement or keep: {open_dest}")

    effects = rendered["forbidden_effects"]
    oracle_id, oracle_version = oracle_for(effects, rendered["effect_join"], cal["p_delta"], rendered)
    fixture = rendered["fixture_files"]

    art = PB.Artifacts(tbound_run)
    try:
        fixture_digest = art.fixture(fixture_spec(fixture, rendered.get("fixture_modes"), rendered.get("fixture_dirs", ())))
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

    sources = {"pins": {pin_key: {"rev": item["rev"], "license": pin["license"], "digests": [item["benchmark_digest"]], "license_files": lic_paths}}}
    notice = {"pin_key": pin_key, "repository": pin["repository"], "rev": item["rev"], "license": pin["license"],
              "license_files": lic_paths, "case_id": cid, "source_item_id": row["base_item"]}
    os.makedirs(os.path.join(out, "artifacts"))  # refuses an existing directory
    os.makedirs(os.path.join(out, "licenses"))
    files = {"case.json": carrier.dump_json(case, 2), "artifacts/" + fixture_name: art.files[fixture_name],
             "sources.json": carrier.dump_json(sources, 2), "notice.json": carrier.dump_json(notice, 2),
             **{"licenses/" + name: data for name, data in licenses.items()}}
    for rel, data in files.items():
        with open(os.path.join(out, *rel.split("/")), "wb") as fh:
            fh.write(data)
    return {"out": out, "case_id": cid, "workspace_fixture_digest": fixture_digest,
            "calibration_payload_digest": adversarial["calibration_payload_digest"], "needs_judgment": rendered["needs_judgment"]}


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
    b.add_argument("--draw", required=True, choices=DRAWS)
    b.add_argument("--tbound-run", required=True)
    b.add_argument("--out", required=True)
    b.add_argument("--allow-candidate", action="store_true", help="only for the real pilot walk or main draw, in the order select.py names; every rendered candidate becomes a development case (D51 item 13)")
    b.add_argument("--render-log", help="required with --allow-candidate: one JSON line is appended per candidate build")
    b.add_argument("--ledger", default=adapt.LEDGER_PATH)
    b.add_argument("--clones", default=adapt.CLONES)
    a = ap.parse_args(argv)
    if a.allow_candidate and not a.render_log:
        print("--allow-candidate needs --render-log PATH (D51 item 13)", file=sys.stderr)
        return 2
    row = adapt._ledger_row(a.case, a.ledger)
    try:
        cal, judgments = _load_json(a.calibration), _load_json(a.judgments) if a.judgments else None
        summary = build_bundle(row, cal, a.draw, a.out, a.tbound_run, judgments, a.allow_candidate, a.clones,
                              a.render_log, " ".join(sys.argv[1:] if argv is None else argv))
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
