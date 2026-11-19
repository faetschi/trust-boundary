#!/usr/bin/env python3
"""D42 edit-mismatch count for a rehearsal. Read-only on every input.

D42 (register, 2026-11-01): "An edit whose old text is absent or not unique stays UNKNOWN."
Code path (configs B and C run on the guest through the governed Store):
  sessionrepo.applyReplacements -> ErrInvalidEdit "edit replacements must be unique and non-overlapping"
  -> DurableExecutor wraps it as "durable edit: ..." -> providerbridge appends provider_tool_result_unknown
  ("trusted executor failed; provider result withheld: durable edit: ...") and the run ends UNKNOWN.
  The soft-refusal codes edit_old_text_absent / edit_old_text_ambiguous exist only on the product path.
Configuration V runs stock Pi on the host and keeps only a tool-call count, so a V mismatch is not observable.

Usage:
  python -I count_d42.py --journal runs/pilot/journal.jsonl [--audit diag/final/audit --units units.json]
  python -I count_d42.py --selftest
"""
import argparse
import base64
import glob
import json
import os
import re
from collections import Counter, defaultdict

MISMATCH = re.compile(r"durable edit:|edit replacements must be unique|edit_old_text_(absent|ambiguous)|ErrInvalidEdit")


def dec(s):
    try:
        return json.loads(base64.b64decode(s))
    except Exception:
        return {}


def lines(path):
    with open(path, encoding="utf8") as f:
        for raw in f:
            raw = raw.strip()
            if raw.endswith("}"):  # a partial last line (file still being written) is skipped
                yield json.loads(raw)


def classify_provider_audit(records):
    """Return the edit calls of one provider audit as dicts, and any mismatch text found in any record."""
    gate, result, unknown, hits = {}, {}, {}, []
    for r in records:
        e = r["event"]
        d = dec(e["data"])
        text = json.dumps(d)
        if MISMATCH.search(text):
            hits.append((e["kind"], MISMATCH.search(text).group(0)))
        tc = d.get("tool_call_id")
        if e["kind"] == "provider_gate_decision":
            gate[tc] = d
        elif e["kind"] == "provider_tool_result":
            out = dec(d.get("result", "")).get("output", {})
            result[tc] = (out.get("outcome"), out.get("code") or out.get("reason_code"))
        elif e["kind"] == "provider_tool_result_unknown":
            unknown[tc] = d.get("failure", "")
    calls = []
    for tc, g in gate.items():
        if g.get("tool") != "edit":
            continue
        if tc in unknown:
            cls = "MISMATCH" if MISMATCH.search(unknown[tc]) else "edit-unknown-other"
            detail = unknown[tc][:240]
        elif tc in result:
            outcome, code = result[tc]
            cls = "applied" if outcome == "success" else ("MISMATCH" if code and MISMATCH.search(code) else "edit-result-" + str(outcome))
            detail = outcome
        else:
            cls, detail = "no-result", g.get("verdict")
        if g.get("verdict") != "ALLOW" and cls == "no-result":
            cls = "denied-by-gate"
        calls.append({"tool_call_id": tc, "seq": g.get("sequence"), "verdict": g.get("verdict"), "class": cls, "detail": detail})
    return calls, hits


def journal_units(path):
    latest = {}
    for r in lines(path):
        if r.get("kind") == "done":
            latest[r["step_id"]] = r
    return latest


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--journal")
    ap.add_argument("--audit")
    ap.add_argument("--units")
    ap.add_argument("--selftest", action="store_true")
    a = ap.parse_args()
    if a.selftest:
        return selftest()

    done = journal_units(a.journal)
    print(f"journal: {len(done)} done units")
    by_cfg = Counter()
    for sid, r in done.items():
        cfg = sid.split("|")[2]
        o = r.get("outcome") or {}
        unk = bool(r.get("exec_error")) or o.get("evidence_loss") not in (None, "none")
        by_cfg[(cfg, "unknown" if unk else "ok")] += 1
    print("journal done units by config:", dict(sorted(by_cfg.items())))
    print("journal UNKNOWN units in governed configs (B, C): the upper bound on mismatches without guest audits")
    for sid, r in sorted(done.items(), key=lambda x: x[1]["index"]):
        cfg = sid.split("|")[2]
        if cfg in ("B", "C") and (r.get("exec_error") or (r.get("outcome") or {}).get("evidence_loss") not in (None, "none")):
            print(f"  N={r['index'] + 1} {sid} exec_error={r.get('exec_error')!r}")
    print("edit cases in the journal (case id has -edit-): N, step, task, tool_calls")
    for sid, r in sorted(done.items(), key=lambda x: x[1]["index"]):
        if "-edit-" in sid:
            o = r.get("outcome") or {}
            print(f"  N={r['index'] + 1} {sid} task={o.get('task')} tool_calls={(o.get('metrics') or {}).get('tool_calls')}")

    if not a.audit:
        print("no --audit: guest audits not available, mismatch causes not readable")
        return
    unit_of = {}
    for u in json.load(open(a.units, encoding="utf8")):
        for run in u["runs"]:
            if run.get("run_id"):
                unit_of[run["run_id"]] = (u["N"], u["step_id"])
    per_cfg_units = defaultdict(set)
    classes = Counter()
    mismatches, other_hits, unmapped_edits = [], [], []
    for f in sorted(glob.glob(os.path.join(a.audit, "audit-production-run-*-provider-audit.jsonl"))):
        run = re.search(r"production-run-[0-9a-f]{24}", f).group(0)
        calls, hits = classify_provider_audit(lines(f))
        unit = unit_of.get(run)
        if hits and not any(c["class"] == "MISMATCH" for c in calls):
            other_hits.append((run, unit, hits))
        for c in calls:
            if unit is None:
                unmapped_edits.append((run, c["class"]))
                continue
            classes[c["class"]] += 1
            per_cfg_units[unit[1].split("|")[2]].add(unit)
            print(f"  edit call N={unit[0]} {unit[1]} {run} seq={c['seq']} {c['tool_call_id']} verdict={c['verdict']} -> {c['class']}")
            if c["class"] != "applied":
                mismatches.append((unit, run, c))
    # Any other audit kind (session, command, catalog, requests) that names the mismatch.
    for f in sorted(glob.glob(os.path.join(a.audit, "*.jsonl"))):
        if f.endswith("-provider-audit.jsonl"):
            continue
        for r in lines(f):
            e = r.get("event", r)
            m = MISMATCH.search(json.dumps(dec(e.get("data", ""))) if "data" in e else "")
            if m:
                other_hits.append((os.path.basename(f), None, [(e.get("kind"), m.group(0))]))
    print(f"\nguest edit calls in M3 units, by class: {dict(classes)}")
    for cfg in sorted(per_cfg_units):
        units = sorted(per_cfg_units[cfg])
        print(f"  config {cfg}: {len(units)} units with >=1 gated edit call: {[n for n, _ in units]}")
    print(f"edit calls in runs not mapped to a unit (EVQ, probes): {Counter(c for _, c in unmapped_edits)}")
    print(f"edit calls that did not apply: {len(mismatches)}")
    for unit, run, c in mismatches:
        print(f"  N={unit[0]} {unit[1]} {run} {c}")
    print(f"mismatch text outside a classified edit call: {len(other_hits)}")
    for h in other_hits:
        print("  ", h)


def selftest():
    enc = lambda d: base64.b64encode(json.dumps(d).encode()).decode()
    recs = [
        {"event": {"kind": "provider_gate_decision", "data": enc({"tool_call_id": "a", "tool": "edit", "verdict": "ALLOW", "sequence": 1})}},
        {"event": {"kind": "provider_tool_result_unknown", "data": enc({"tool_call_id": "a", "failure": "trusted executor failed; provider result withheld: durable edit: edit replacements must be unique and non-overlapping"})}},
        {"event": {"kind": "provider_gate_decision", "data": enc({"tool_call_id": "b", "tool": "edit", "verdict": "ALLOW", "sequence": 2})}},
        {"event": {"kind": "provider_tool_result", "data": enc({"tool_call_id": "b", "tool": "edit", "result": enc({"output": {"outcome": "success"}})})}},
        {"event": {"kind": "provider_gate_decision", "data": enc({"tool_call_id": "c", "tool": "bash", "verdict": "ALLOW", "sequence": 3})}},
    ]
    calls, hits = classify_provider_audit(recs)
    assert [c["class"] for c in calls] == ["MISMATCH", "applied"], calls
    assert hits and hits[0][0] == "provider_tool_result_unknown", hits
    print("selftest ok")


if __name__ == "__main__":
    main()
