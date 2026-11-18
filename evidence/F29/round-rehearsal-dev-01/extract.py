#!/usr/bin/env python3
"""Read-only summary of a pilot rehearsal (M3 on F23, then F26 and F28).

Reads the runner journal, plan.txt and the optional per-unit provider audits in
diag/. Writes OUTDIR/rehearsal-summary.json and rehearsal-summary.md and prints
the markdown. Unit mapping: diag u<N> belongs to the journal step with index N-1
(N = index + 1; unit 1 = index 0).
"""
import argparse
import base64
import binascii
import json
import math
import re
import statistics
import sys
import time
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MODEL_CONFIGS = {"B", "C", "V"}
MODEL_ROLES = {"trajectory", "compliant", "ambiguous", "variance-repeat"}
PARALLEL_TEXT = ("tool-call index must be zero", "multiple or malformed tool calls")
TRIGGER_PCT = 2.0
RUN_KINDS = ("begin", "started", "done", "void", "abandoned")
CAUSES = ["provider-parallel-tool-calls", "provider-overload-then-transport",
          "provider-transport", "capture-other", "no-diag", "other"]


def read_bytes(path):
    # The runner can hold a write lock on the journal while it appends; retry briefly.
    for attempt in range(5):
        try:
            return Path(path).read_bytes()
        except PermissionError:
            if attempt == 4:
                raise
            time.sleep(0.5)


def as_int(value):
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def nearest_rank(values, pct):
    ordered = sorted(values)
    if not ordered:
        return None
    return ordered[max(math.ceil(pct / 100 * len(ordered)), 1) - 1]


def split_jsonl(raw, label, warnings):
    """Parse complete lines. An unterminated last line is a partial append and is dropped."""
    partial = 0
    if raw and not raw.endswith(b"\n"):
        cut = raw.rfind(b"\n") + 1
        partial, raw = len(raw) - cut, raw[:cut]
    records = []
    for number, line in enumerate(raw.split(b"\n"), 1):
        if not line.strip():
            continue
        try:
            records.append(json.loads(line))
        except ValueError as exc:
            warnings.append(f"{label} line {number} is not JSON and was skipped: {exc}")
    return records, partial


def parse_plan(path):
    plan = {"units": None, "configurations": {}, "roles": {}, "digests": {}}
    for text in read_bytes(path).decode("utf-8").splitlines():
        parts = text.split()
        if len(parts) < 2:
            continue
        key = parts[0]
        if key == "units":
            plan["units"] = int(parts[1])
        elif key == "configuration":
            plan["configurations"][parts[1]] = int(parts[2])
        elif key == "role":
            plan["roles"][parts[1]] = int(parts[2])
        elif key.endswith("digest") or key == "run_binding":
            plan["digests"][key] = parts[1]
    return plan


def new_run(rec):
    return {"attempt": rec.get("attempt"), "begin_seq": rec.get("seq"), "terminal": None,
            "terminal_seq": None, "outcome": None, "exec_error": None, "elapsed_ms": None,
            "reason": None}


def build_runs(records, warnings):
    """Group records into runs per step_id. A run is begin..(done|void); a second begin
    before any terminal record marks the earlier run interrupted. Void does not bump
    the attempt number, so runs are kept in journal order."""
    runs, index_of, headers, unknown_kinds = {}, {}, [], Counter()
    for rec in records:
        if not isinstance(rec, dict):
            warnings.append("journal record is not an object; skipped")
            continue
        kind = rec.get("kind")
        if kind == "header":
            headers.append(rec)
            continue
        if kind not in RUN_KINDS:
            unknown_kinds[str(kind)] += 1
            continue
        sid = rec.get("step_id")
        if not isinstance(sid, str):
            warnings.append(f"seq {rec.get('seq')}: {kind} record has no step_id")
            continue
        index_of.setdefault(sid, rec.get("index"))
        lst = runs.setdefault(sid, [])
        open_run = lst[-1] if lst and lst[-1]["terminal"] is None else None
        if kind == "begin":
            if open_run is not None:
                open_run["terminal"] = "interrupted"
                warnings.append(f"{sid}: begin at seq {rec.get('seq')} while an earlier run had no terminal record")
            lst.append(new_run(rec))
            continue
        if open_run is None:
            open_run = new_run(rec)
            lst.append(open_run)
            warnings.append(f"{sid}: {kind} at seq {rec.get('seq')} without an open begin")
        if kind == "started":
            continue
        open_run.update(terminal=kind, terminal_seq=rec.get("seq"))
        if kind == "done":
            open_run.update(outcome=rec.get("outcome") or {}, exec_error=rec.get("exec_error"),
                            elapsed_ms=rec.get("elapsed_ms"))
        else:
            open_run["reason"] = rec.get("reason")
    return runs, index_of, headers, unknown_kinds


def run_status(run):
    if run["terminal"] in ("void", "abandoned"):
        return run["terminal"]
    if run["terminal"] == "done":
        err = str(run["exec_error"] or "")
        if "unknown" in err.lower() or (run["outcome"] or {}).get("evidence_loss") != "none":
            return "done-unknown"
        return "done-error" if err else "done-ok"
    return "pending"


def calibration(run):
    oc = (run or {}).get("outcome") or {}
    cal = oc.get("calibration")
    if not isinstance(cal, dict):
        return None
    pred = cal.get("witness_predicate_id")
    seen = (oc.get("oracles") or {}).get(pred)
    return {"predicate": pred, "oracle": seen, "manifested": seen == "observed"}


def oracles_text(run):
    oc = (run or {}).get("outcome") or {}
    pairs = sorted((k, v) for k, v in (oc.get("oracles") or {}).items() if v != "none")
    return ", ".join(f"{k}={v}" for k, v in pairs) or "all none"


def build_units(runs, index_of, planned, warnings):
    units = []
    for sid, lst in runs.items():
        if sid.count("|") != 3:
            warnings.append(f"step_id is not CASE|role|config|n: {sid}")
        case, role, cfg = (sid.split("|") + ["?", "?", "?"])[:3]
        index = index_of.get(sid)
        if not isinstance(index, int) or not 0 <= index < planned:
            warnings.append(f"{sid}: index {index} is outside the planned range 0..{planned - 1}")
        units.append({"step_id": sid, "case_id": case, "role": role, "config": cfg, "index": index,
                      "runs": [dict(run, status=run_status(run)) for run in lst]})
    begun = {u["index"] for u in units}
    for dup in sorted(i for i, c in Counter(u["index"] for u in units).items() if c > 1):
        warnings.append(f"index {dup} is shared by several step_ids")
    for index in range(planned):
        if index not in begun:
            units.append({"step_id": None, "case_id": None, "role": None, "config": None,
                          "index": index, "runs": []})
    for u in units:
        u["N"] = u["index"] + 1 if isinstance(u["index"], int) else None
        u["latest"] = u["runs"][-1] if u["runs"] else None
        u["status"] = u["latest"]["status"] if u["latest"] else "not-begun"
        u["calibration"] = calibration(u["latest"])
    units.sort(key=lambda u: (u["index"] if isinstance(u["index"], int) else planned, u["step_id"] or ""))
    return units


def decode_data(b64):
    """Audit event data is base64 of a JSON object."""
    try:
        obj = json.loads(base64.b64decode(b64 or "", validate=True))
    except (binascii.Error, TypeError, ValueError):
        return None
    return obj if isinstance(obj, dict) else None


def stream_shows_parallel(resp_b64):
    """True if the retained SSE stream has a tool_calls delta with index >= 1, or several calls in one array."""
    try:
        text = base64.b64decode(resp_b64, validate=True).decode("utf-8", "replace")
    except (binascii.Error, TypeError, ValueError):
        return False
    for line in text.splitlines():
        if not line.startswith("data:"):
            continue
        try:
            for choice in json.loads(line[5:]).get("choices") or []:
                calls = choice["delta"].get("tool_calls") or []
                if len(calls) > 1 or any((as_int(c.get("index")) or 0) >= 1 for c in calls):
                    return True
        except (ValueError, AttributeError, KeyError, TypeError):
            continue
    return False


def read_audit(path, warnings):
    """Return (events in file order, chain note, partial tail bytes)."""
    records, partial = split_jsonl(read_bytes(path), path.name, warnings)
    events, chain, prev_hash, prev_seq = [], "ok", None, None
    for rec in records:
        if not isinstance(rec, dict):
            warnings.append(f"{path.name}: non-object line skipped")
            continue
        seq = as_int(rec.get("sequence"))
        if chain == "ok" and prev_seq is not None and seq != prev_seq + 1:
            chain = f"sequence gap before {seq}"
        if chain == "ok" and prev_hash is not None and rec.get("previous_hash") != prev_hash:
            chain = f"hash link broken at sequence {seq}"
        prev_seq, prev_hash = seq, rec.get("hash")
        ev = rec.get("event") if isinstance(rec.get("event"), dict) else {}
        data = decode_data(ev.get("data"))
        if data is None:
            warnings.append(f"{path.name}: sequence {seq} event data is not base64 JSON")
            data = {}
        events.append({"sequence": seq, "kind": ev.get("kind"), "data": data})
    return events, chain, partial


def is_resample(event):
    """Decision-13 resample (PR #131): the refused parallel-call attempt is a status-200 retryable failure."""
    data = event["data"]
    return (event["kind"] == "provider_exchange_retryable_failure" and as_int(data.get("status_code")) == 200
            and stream_shows_parallel(data.get("response")))


def resamples(units):
    rows = [{"N": u["N"], "step_id": u["step_id"], "resamples": u["diag"]["resamples"], "status": u["status"]}
            for u in units if u["diag"]["resamples"]]
    return {"units": len(rows), "attempts": sum(r["resamples"] for r in rows),
            "recovered": sum(r["status"] == "done-ok" for r in rows), "rows": rows}


def overloaded(code):
    return code == 429 or (code is not None and 500 <= code <= 599)


def classify(events):
    """Cause of a done-unknown unit, from its provider audit events."""
    if not events:
        return "no-diag", ""
    unk = [i for i, e in enumerate(events) if e["kind"] == "provider_exchange_unknown"]
    if not unk:
        return "other", "audit has no provider_exchange_unknown event"
    i = unk[-1]
    data = events[i]["data"]
    failure = str(data.get("failure") or "")
    status = as_int(data.get("status_code"))
    if any(text in failure for text in PARALLEL_TEXT):
        if stream_shows_parallel(data.get("response")):
            return "provider-parallel-tool-calls", failure[:160]
        return "other", "parallel-call failure text, but retained stream shows no index>=1 or multi-call array"
    if status == 0:
        prior = [e for e in events[:i] if e["kind"] == "provider_exchange_retryable_failure"]
        if any(overloaded(as_int(e["data"].get("status_code"))) for e in prior):
            return "provider-overload-then-transport", failure[:160]
        # A status-0 unknown after a non-429/5xx retry is also transport-level.
        return "provider-transport", failure[:160]
    return "capture-other", f"status {status}: {failure[:140]}"


def attach_diag(units, diag, warnings):
    files = {}
    if diag.is_dir():
        for path in diag.iterdir():
            m = re.fullmatch(r"u(\d+)-provider-audit\.jsonl", path.name)
            if m:
                files[int(m.group(1))] = path
    for u in units:
        u["diag"] = {"file": None, "events": 0, "chain": None, "partial_bytes": 0,
                     "stream_parallel_unknown": False}
        u["cause"], u["cause_detail"] = None, ""
        path = files.pop(u["N"], None) if u["N"] is not None else None
        events = []
        if path is not None:
            if u["step_id"] is None:
                warnings.append(f"{path.name} exists but journal has no step at index {u['index']}")
            events, chain, partial = read_audit(path, warnings)
            u["diag"].update(file=path.name, events=len(events), chain=chain, partial_bytes=partial)
            if chain != "ok":
                warnings.append(f"{path.name}: {chain}")
            if partial:
                warnings.append(f"{path.name}: ignored an unterminated {partial}-byte tail")
        if u["status"] == "done-unknown":
            u["cause"], u["cause_detail"] = classify(events)
        unk = [e for e in events if e["kind"] == "provider_exchange_unknown"]
        u["diag"]["stream_parallel_unknown"] = bool(unk) and stream_shows_parallel(unk[-1]["data"].get("response"))
        u["diag"]["resamples"] = sum(1 for e in events if is_resample(e))
    for n in sorted(files):
        warnings.append(f"{files[n].name} has no journal unit at index {n - 1}")


def read_abandons(diag, by_n):
    found = []
    if not diag.is_dir():
        return found
    for path in sorted(diag.iterdir()):
        m = re.fullmatch(r"abandon-u(\d+)-confirm\.txt", path.name)
        if not m:
            continue
        lines = read_bytes(path).decode("utf-8", "replace").splitlines()
        try:
            rec = json.loads(lines[0]) if lines else {}
        except ValueError:
            rec = {}
        if not isinstance(rec, dict):
            rec = {}
        n = int(m.group(1))
        unit = by_n.get(n)
        found.append({"N": n, "step_id": unit["step_id"] if unit else None,
                      "settlement": rec.get("settlement"), "journal_sequence": rec.get("journal_sequence")})
    return found


def bucket_table(field, planned_map, units, warnings):
    seen = [u for u in units if u["step_id"]]
    rows = []
    for key in sorted(set(planned_map) | {u[field] for u in seen}):
        group = [u for u in seen if u[field] == key]
        counts = Counter(u["status"] for u in group)
        planned = planned_map.get(key, 0)
        if len(group) > planned:
            warnings.append(f"{field} {key}: {len(group)} begun units but plan.txt has {planned}")
        rows.append({"key": key, "planned": planned, "done-ok": counts["done-ok"],
                     "done-unknown": counts["done-unknown"], "done-error": counts["done-error"],
                     "void-only": counts["void"], "abandoned": counts["abandoned"], "pending": counts["pending"],
                     "not-begun": max(planned - len(group), 0)})
    return rows


def totals(units):
    done_runs = [r for u in units for r in u["runs"] if r["terminal"] == "done"]

    def metric(run, key):
        return ((run["outcome"] or {}).get("metrics") or {}).get(key) or 0

    return {
        "done_units": sum(u["status"].startswith("done") for u in units),
        "done_attempts": len(done_runs),
        "wall_ms": sum(r["elapsed_ms"] or 0 for r in done_runs),
        "in_tokens": sum(metric(r, "in_tokens") for r in done_runs),
        "out_tokens": sum(metric(r, "out_tokens") for r in done_runs),
        "tool_calls": sum(metric(r, "tool_calls") for r in done_runs),
        "cost_micro_usd": sum(metric(r, "cost_micro_usd") for r in done_runs),
    }


def role_stats(units):
    by_role = {}
    for u in units:
        if u["status"].startswith("done"):
            by_role.setdefault(u["role"], []).append(u["latest"]["elapsed_ms"] or 0)
    return {role: {"n": len(v), "median_ms": statistics.median(v), "p95_ms": nearest_rank(v, 95)}
            for role, v in sorted(by_role.items())}


def decision13(units):
    def ratio(member):
        group = [u for u in units if member(u)]
        done = sum(u["status"].startswith("done") for u in group)
        par = sum(u["cause"] == "provider-parallel-tool-calls" for u in group)
        rate = round(100.0 * par / done, 2) if done else None
        return {"done_units": done, "parallel_causes": par, "rate_pct": rate,
                "over_trigger": None if rate is None else rate > TRIGGER_PCT}
    return {"model": ratio(lambda u: u["config"] in MODEL_CONFIGS and u["role"] in MODEL_ROLES),
            "P": ratio(lambda u: u["config"] == "P")}


def recovery(units, abandons):
    abandoned = {a["N"] for a in abandons}
    rows = []
    for u in units:
        if not any(r["status"] == "done-unknown" for r in u["runs"]):
            continue
        if u["status"] == "done-ok":
            state = "recovered (rerun done-ok)"
        elif u["status"] == "pending":
            state = "rerun pending"
        elif u["N"] in abandoned:
            state = "abandoned by operator"
        else:
            state = "not recovered"
        rows.append({"N": u["N"], "step_id": u["step_id"], "latest_status": u["status"], "state": state})
    return rows


def table(headers, rows):
    def cell(v):
        return "" if v is None else str(v).replace("|", "\\|").replace("\n", " ")
    lines = ["| " + " | ".join(headers) + " |", "|" + "---|" * len(headers)]
    lines += ["| " + " | ".join(cell(c) for c in row) + " |" for row in rows]
    return lines


def unit_cells(u):
    run = u["latest"]
    if run is None:
        return [u["N"], "(not begun)", "-", "", u["status"]] + [""] * 11
    oc = run["outcome"] or {}
    met = oc.get("metrics") or {}
    cal = u["calibration"]
    cal_txt = "n/a" if cal is None else f"{'yes' if cal['manifested'] else 'no'} ({cal['predicate']}={cal['oracle']})"
    err = run["exec_error"] or (f"void: {run['reason']}" if run["terminal"] == "void" else "")
    earlier = "; ".join(f"a{r['attempt']} {r['status']}" for r in u["runs"][:-1])
    return [u["N"], u["step_id"], f"a{run['attempt']}", earlier, u["status"], run["elapsed_ms"],
            met.get("latency_ms"), met.get("in_tokens"), met.get("out_tokens"), met.get("tool_calls"),
            met.get("retries"), oc.get("evidence_loss"), oracles_text(run), cal_txt, str(err)[:120], u["cause"]]


def rate_text(d):
    if d["rate_pct"] is None:
        return "n/a (no done units)"
    return f"{d['rate_pct']:.2f}% (threshold {TRIGGER_PCT:g}%: {'over' if d['over_trigger'] else 'not over'})"


def render_md(s):
    j, h, t = s["journal"], s["header"], s["totals"]
    lines = ["# pilot rehearsal summary (read only)", "",
             f"- generated: {s['generated_at']}",
             f"- journal: `{s['inputs']['journal']}`: {j['records']} records; unterminated tail ignored: {j['partial_bytes']} bytes",
             f"- plan: `{s['inputs']['plan']}`; diag: `{s['inputs']['diag']}`",
             f"- plan_digest: `{h['plan_digest']}`; run_binding: `{h['run_binding']}`",
             f"- header records: {h['records']}; match plan.txt (digest, run_binding, planned units): {'yes' if h['match'] else 'NO'}",
             f"- planned units: {s['planned_units']} (configurations sum {s['plan_sums']['configurations']}, roles sum {s['plan_sums']['roles']})",
             f"- unknown record kinds counted: {j['unknown_kinds'] or 'none'}",
             "",
             f"Unit mapping: diag file `u<N>-provider-audit.jsonl` belongs to the journal step with index N-1, where N = index + 1 (unit 1 = index 0). Every planned index 0..{s['planned_units'] - 1} has a row; an index with no journal record is `not-begun`.",
             "", "## 1. Header and plan partition", ""]
    keys = ["done-ok", "done-unknown", "done-error", "void-only", "abandoned", "pending", "not-begun"]
    for label, rows in (("config", s["by_config"]), ("role", s["by_role"])):
        body = [[r["key"], r["planned"]] + [r[k] for k in keys] for r in rows]
        total = ["total"] + [sum(b[i] for b in body) for i in range(1, 2 + len(keys))]
        lines += table([label, "planned"] + keys, body + [total]) + [""]
    lines += ["## 2. Per unit (latest attempt; earlier attempts listed)", "",
              "Status: done-ok = done, evidence_loss none, no exec_error; done-unknown = exec_error mentions unknown or evidence_loss is not none; done-error = done with another exec_error; void = voided with no later run; abandoned = the runner journaled the step as abandoned (it started in an earlier process and never finished; not retried); pending = begun with no terminal record (running, or interrupted by a restart); not-begun = no journal record.",
              ""]
    lines += table(["N", "step_id", "latest", "earlier", "status", "elapsed_ms", "latency_ms", "in_tok",
                    "out_tok", "tools", "retries", "evidence_loss", "oracles (non-none)", "calibration",
                    "exec_error / void reason", "cause"], [unit_cells(u) for u in s["units"]]) + [""]
    lines += ["## 3. Totals", "",
              f"- units done: {t['done_units']}; done attempts including reruns: {t['done_attempts']}",
              f"- wall time, sum over done attempts: {t['wall_ms']} ms ({t['wall_ms'] / 3.6e6:.2f} h)",
              f"- tokens: in {t['in_tokens']}, out {t['out_tokens']}; tool calls: {t['tool_calls']}",
              f"- cost: {t['cost_micro_usd']} micro-USD ({t['cost_micro_usd'] / 1e6:.4f} USD)", "",
              "Elapsed per role, latest done attempt of each unit (p95 nearest-rank):", ""]
    lines += table(["role", "done units", "median_ms", "p95_ms"],
                   [[r, v["n"], v["median_ms"], v["p95_ms"]] for r, v in s["role_stats"].items()]) + [""]
    d13 = s["decision13"]
    lines += ["## 4. UNKNOWN causes (done-unknown units, latest attempt)", ""]
    lines += table(["cause", "units"], [[c, s["unknown_causes"].get(c, 0)] for c in CAUSES]) + [""]
    unk_rows = [[u["N"], u["step_id"], u["cause"], u["cause_detail"], u["diag"]["chain"] or "no diag"]
                for u in s["units"] if u["status"] == "done-unknown"]
    lines += table(["N", "step_id", "cause", "detail", "audit chain"], unk_rows) + [""]
    lines += ["Decision-13 inputs:", "",
              f"- model units (configs B/C/V, roles trajectory/compliant/ambiguous/variance-repeat): done {d13['model']['done_units']}, parallel-tool-call causes {d13['model']['parallel_causes']}, rate {rate_text(d13['model'])}",
              f"- P units: done {d13['P']['done_units']}, parallel-tool-call causes {d13['P']['parallel_causes']}, rate {rate_text(d13['P'])}",
              f"- stream-only parallel (retained stream shows index>=1 but failure text differs; not counted above): {s['stream_only_parallel']}",
              f"- decision-13 resamples (status-200 retryable failure with index>=1): {s['resamples']['attempts']} attempts in {s['resamples']['units']} units; units done-ok afterwards: {s['resamples']['recovered']}",
              ""]
    lines += table(["N", "step_id", "resamples", "status"],
                   [[r["N"], r["step_id"], r["resamples"], r["status"]] for r in s["resamples"]["rows"]]) + [""]
    lines += ["## 5. Recovery effort", "",
              f"- void records in journal: {j['voids']}",
              f"- operator abandon files: {len(s['abandons'])}"]
    lines += table(["N", "step_id", "settlement", "journal_sequence"],
                   [[a["N"], a["step_id"] or "(no journal record)", a["settlement"], a["journal_sequence"]]
                    for a in s["abandons"]]) + [""]
    lines += table(["N", "step_id", "latest status", "state"],
                   [[r["N"], r["step_id"], r["latest_status"], r["state"]] for r in s["recovery"]]) + [""]
    lines += ["## Warnings", ""] + ([f"- {w}" for w in s["warnings"]] or ["- none"]) + [""]
    return "\n".join(lines)


def main(argv=None):
    ap = argparse.ArgumentParser(description="Read-only rehearsal summary (see module docstring).")
    ap.add_argument("--journal", type=Path, default=ROOT / "runs" / "pilot" / "journal.jsonl")
    ap.add_argument("--plan", type=Path, default=ROOT / "runs" / "plan.txt")
    ap.add_argument("--diag", type=Path, default=ROOT / "diag")
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args(argv)
    out = args.out.resolve()
    for live in (args.journal.resolve().parent, args.plan.resolve().parent, args.diag.resolve()):
        if out == live or live in out.parents:
            print(f"refusing --out {out}: it is inside live evidence {live}", file=sys.stderr)
            return 2
    warnings = []
    plan = parse_plan(args.plan)
    planned = plan["units"]
    if planned is None:
        print("plan.txt has no units line", file=sys.stderr)
        return 2
    records, partial = split_jsonl(read_bytes(args.journal), "journal", warnings)
    if partial:
        warnings.append(f"journal: ignored an unterminated {partial}-byte tail (live append in progress)")
    runs, index_of, headers, unknown_kinds = build_runs(records, warnings)
    units = build_units(runs, index_of, planned, warnings)
    by_n = {u["N"]: u for u in units if u["N"] is not None}
    attach_diag(units, args.diag, warnings)
    abandons = read_abandons(args.diag, by_n)
    digests = plan["digests"]
    header_ok = bool(headers) and all(
        h.get("plan_digest") == digests.get("plan_digest") and h.get("run_binding") == digests.get("run_binding")
        and h.get("planned_units") == planned for h in headers)
    if headers and not header_ok:
        warnings.append("journal header does not match plan.txt (digest, run_binding or planned units)")
    last = headers[-1] if headers else {}
    summary = {
        "generated_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "inputs": {"journal": str(args.journal), "plan": str(args.plan), "diag": str(args.diag)},
        "journal": {"records": len(records), "partial_bytes": partial, "unknown_kinds": dict(unknown_kinds),
                    "voids": sum(r["terminal"] == "void" for u in units for r in u["runs"])},
        "header": {"records": len(headers), "plan_digest": last.get("plan_digest"),
                   "run_binding": last.get("run_binding"), "planned_units": last.get("planned_units"),
                   "match": header_ok},
        "planned_units": planned,
        "plan_sums": {"configurations": sum(plan["configurations"].values()),
                      "roles": sum(plan["roles"].values())},
        "by_config": bucket_table("config", plan["configurations"], units, warnings),
        "by_role": bucket_table("role", plan["roles"], units, warnings),
        "units": units,
        "totals": totals(units),
        "role_stats": role_stats(units),
        "unknown_causes": dict(Counter(u["cause"] for u in units if u["cause"])),
        "decision13": decision13(units),
        "stream_only_parallel": sum(1 for u in units if u["status"] == "done-unknown"
                                    and u["diag"]["stream_parallel_unknown"]
                                    and u["cause"] != "provider-parallel-tool-calls"),
        "resamples": resamples(units),
        "abandons": abandons,
        "recovery": recovery(units, abandons),
        "warnings": warnings,
    }
    md = render_md(summary)
    out.mkdir(parents=True, exist_ok=True)
    (out / "rehearsal-summary.json").write_text(json.dumps(summary, indent=2, default=str), encoding="utf-8")
    (out / "rehearsal-summary.md").write_text(md, encoding="utf-8")
    print(md.encode("ascii", "backslashreplace").decode("ascii"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
