#!/usr/bin/env python3
"""Per-step table of one case from the runner journal (rehearsal-dev-02: the mixed NET-01 + WS-01 'all' case). Read-only.
Usage: python -I mixed_case.py <journal.jsonl> <case_id>"""
import json, sys

journal, case = sys.argv[1], sys.argv[2]
print(f"# Steps of `{case}` (mixed_case.py)\n")
print("| N | step | delta | provider | proposals (tool verdict reason [status]) | oracles (non-none) | calibration evidence | tool_attempt | evidence_loss | elapsed_ms | latency_ms |")
print("|---|---|---|---|---|---|---|---|---|---|---|")
for line in open(journal, encoding="utf8"):
    d = json.loads(line)
    if d.get("kind") != "done" or d.get("case_id") != case:
        continue
    o = d.get("outcome") or {}
    r = o.get("receipt") or {}
    props = "; ".join(f"{p.get('tool')} {p.get('verdict')} {p.get('reason_code')}" + (f" [{p['status']}]" if p.get("status") else "") for p in r.get("proposals") or [])
    orc = ", ".join(f"{k}={v}" for k, v in sorted((o.get("oracles") or {}).items()) if v != "none") or "all none"
    cal = ", ".join(e.split(":")[1] for e in (o.get("calibration") or {}).get("evidence") or []) or "n/a"
    step = d["step_id"].split("|", 1)[1].replace("|", chr(92) + "|")
    print(f"| {d['index'] + 1} | {step} | {r.get('delta')} | {r.get('provider_mode')} | {props} | {orc} | {cal} | {o.get('tool_attempt')} | "
          f"{o.get('evidence_loss')} | {d.get('elapsed_ms')} | {(o.get('metrics') or {}).get('latency_ms')} |")
