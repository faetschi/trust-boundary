#!/usr/bin/env python3
"""F30 round checks (the F29 round_checks.py plus the typed budget end of #192, D56) that extract.py does not make. Read-only on every input.

Joins the runner journal to the guest request journal, the admission catalog and the per-run provider
audits (map.json from map.js), and writes round-checks.json plus round-checks.md:
  1. per-configuration status, including the journal kind "abandoned";
  2. the guest request of every non-done-ok unit;
  3. per guest unit: limits from the admission entry, host calls (provider calls + executed effects),
     reserved provider tokens, receipt tokens, provider-audit tokens, elapsed time;
  4. the steps after each recovery and their oracles;
  5. decision-13 resample traces in the provider audits;
  6. cap, deadline and settlement markers in the audits and the service journal;
  7. the leftover-container check output at each recovery;
  8. the typed budget end: provider requests sent with tools disabled, refused tool calls (audit, receipt, journal)
     and the shape of every reply to a tools-disabled request.

Usage: python -I round_checks.py <rehearsal root> <out dir>
"""
import base64
import glob
import json
import os
import re
import sys
from datetime import datetime
from collections import Counter

MARKERS = re.compile(r"exhausted|effect_not_reserved|pending-call-disposition|allowance|deadline exceeded|"
                     r"request deadline|workflow paused|retryable_failure|resample")


def dec(s):
    try:
        return json.loads(base64.b64decode(s))
    except Exception:
        return {}


def jsonl(path):
    with open(path, encoding="utf8") as f:
        return [json.loads(x) for x in f if x.strip()]


def adm_key(sid):
    cs, role, cfg, rep = sid.split("|")
    if role == "variance-repeat":
        return f"{cs}/variance-{rep}"
    return f"{cs}/{'' if cfg == 'C' else cfg.lower() + '-'}{role}"


TOOL_MARKUP = re.compile(r'<tool_call|</?function|\[TOOL_CALLS\]|<\|tool|"tool_calls"|"name"\s*:\s*"(read|write|edit|bash|ls|grep|find)"', re.I)


def sse_reply(body):
    """Text, reasoning and tool-call count of one provider response (an SSE stream or one JSON document)."""
    chunks = []
    for line in body.split("\n"):
        if line.startswith("data: ") and line != "data: [DONE]":
            try:
                chunks.append(json.loads(line[6:]))
            except ValueError:
                pass
    if not chunks:
        try:
            chunks = [json.loads(body)]
        except ValueError:
            chunks = []
    text, reasoning, calls = [], [], 0
    for c in chunks:
        for ch in (c.get("choices") or []) if isinstance(c, dict) else []:
            part = ch.get("delta") or ch.get("message") or {}
            text.append(part.get("content") or "")
            reasoning.append(part.get("reasoning") or part.get("reasoning_content") or "")
            calls += len(part.get("tool_calls") or [])
    return "".join(text), "".join(reasoning), calls


def reply_shape(text, reasoning, calls, refused):
    if refused:
        return "tool call (refused)"
    if calls:
        return "tool call"
    if text.strip():
        return "tool markup in text" if TOOL_MARKUP.search(text) else "text"
    return "reasoning-only" if reasoning.strip() else "empty"


def exchanges(path):
    """Every provider exchange of a run in audit order: kind, tools_disabled, reply shape, failure."""
    out = []
    if not os.path.exists(path):
        return out
    for r in jsonl(path):
        k = r["event"]["kind"]
        if k not in ("provider_exchange", "provider_exchange_unknown"):
            continue
        d = dec(r["event"]["data"])
        body = base64.b64decode(d["response"]).decode("utf8", "replace") if isinstance(d.get("response"), str) else ""
        text, reasoning, calls = sse_reply(body)
        refused = d.get("refused_tool_call")
        out.append({"kind": k, "tools_disabled": bool((d.get("request") or {}).get("tools_disabled")),
                    "shape": reply_shape(text, reasoning, calls, refused), "text_chars": len(text),
                    "reasoning_chars": len(reasoning), "refused_tool": (refused or {}).get("name"),
                    "failure": (d.get("failure") or "")[:200], "text_head": text[:200]})
    return out


def provider_usage(path):
    u = {"host_call_reservations": 0, "token_upper_bound": 0, "exchanges": 0, "tool_results": 0,
         "tool_results_unknown": 0, "prompt": 0, "completion": 0, "reasoning": 0, "kinds": Counter(), "markers": []}
    if not os.path.exists(path):
        return None
    for r in jsonl(path):
        k = r["event"]["kind"]
        d = dec(r["event"]["data"])
        u["kinds"][k] += 1
        if k == "provider_host_call_reserved":
            u["host_call_reservations"] += 1
            u["token_upper_bound"] += int((d.get("binding") or {}).get("token_upper_bound") or 0)
        elif k == "provider_exchange":
            u["exchanges"] += 1
            body = base64.b64decode(d["response"]).decode("utf8", "replace") if isinstance(d.get("response"), str) else ""
            for line in body.split("\n"):
                if not line.startswith("data: ") or line == "data: [DONE]":
                    continue
                try:
                    c = json.loads(line[6:])
                except ValueError:
                    continue
                if c.get("usage"):
                    u["prompt"] += c["usage"].get("prompt_tokens") or 0
                    u["completion"] += c["usage"].get("completion_tokens") or 0
                    u["reasoning"] += (c["usage"].get("completion_tokens_details") or {}).get("reasoning_tokens") or 0
        elif k == "provider_tool_result":
            u["tool_results"] += 1
        elif k == "provider_tool_result_unknown":
            u["tool_results_unknown"] += 1
        text = json.dumps({kk: vv for kk, vv in d.items() if kk not in ("response", "request")})
        m = MARKERS.search(k + " " + text)
        if m:
            u["markers"].append(f"{k}: {m.group(0)}")
    u["kinds"] = dict(u["kinds"])
    return u


def main(root, out):
    mon = json.load(open(os.path.join(root, "run.json"), encoding="utf8"))["proc"]["monitor_seconds"]
    fin = os.path.join(root, "diag", "final")
    aud = os.path.join(fin, "audit")
    journal = jsonl(os.path.join(root, "runs", "pilot", "journal.jsonl"))
    amap = json.load(open(os.path.join(fin, "map.json"), encoding="utf8"))
    by_key = {}
    for a in amap:
        by_key.setdefault(re.sub(r"/\d+$", "", a["host_admission_id"]), []).append(a)

    entries, receipts = {}, {}
    for r in jsonl(os.path.join(aud, "catalog.jsonl")):
        if r["event"]["kind"] == "pi_workflow_admission_reserved":
            e = dec(r["event"]["data"]).get("entry") or {}
            entries[e.get("request_id")] = e
    for r in jsonl(os.path.join(aud, "audit-serve-pi-requests.jsonl")):
        if r["event"]["kind"] == "pi_service_request_terminal":
            d = dec(r["event"]["data"])
            receipts[d.get("request_id")] = d.get("response") or {}

    steps = {}
    for r in journal:
        sid = r.get("step_id")
        if not sid:
            continue
        s = steps.setdefault(sid, {"N": r["index"] + 1, "step_id": sid, "kinds": []})
        s["kinds"].append(r["kind"])
        if r["kind"] == "done":
            s["done"] = r
        if r["kind"] in ("void", "abandoned"):
            s.setdefault("reasons", []).append(f"{r['kind']}: {r.get('reason')}")

    rows = []
    for s in sorted(steps.values(), key=lambda x: x["N"]):
        cs, role, cfg, rep = s["step_id"].split("|")
        done = s.get("done")
        oc = (done or {}).get("outcome") or {}
        metrics = oc.get("metrics") or {}
        status = ("setup-failure" if done.get("setup_failure") else "done-unknown" if (done.get("exec_error") or oc.get("evidence_loss") not in ("none", None)) else "done-ok") \
            if done else ("abandoned" if "abandoned" in s["kinds"] else "other")
        row = {"N": s["N"], "step_id": s["step_id"], "config": cfg, "role": role, "status": status,
               "journal_kinds": s["kinds"], "reasons": s.get("reasons", []),
               "elapsed_ms": (done or {}).get("elapsed_ms"), "oracles": oc.get("oracles"),
               "tool_attempt": oc.get("tool_attempt"), "task": oc.get("task"),
               "e01_verified": (oc.get("e01") or {}).get("verified"), "journal_in_tokens": metrics.get("in_tokens"),
               "journal_out_tokens": metrics.get("out_tokens"), "journal_cost_micro_usd": metrics.get("cost_micro_usd"),
               "exec_error": (done or {}).get("exec_error"), "setup_failure": (done or {}).get("setup_failure")}
        cands = by_key.get(adm_key(s["step_id"]), [])
        if cands:
            a = cands[-1]
            e = entries.get(a["request_id"]) or {}
            lim = e.get("limits") or {}
            rec = (receipts.get(a["request_id"]) or {}).get("receipt") or {}
            u = provider_usage(os.path.join(aud, f"audit-{a['run_id']}-provider-audit.jsonl"))
            row.update({"admissions": len(cands), "request_id": a["request_id"], "run_id": a["run_id"],
                        "request_status": (a.get("req") or {}).get("status"),
                        "request_abandoned": bool((a.get("req") or {}).get("abandoned")),
                        "max_host_calls": lim.get("max_host_calls"), "max_tokens": lim.get("max_tokens"),
                        "reserved_at": e.get("reserved_at"), "deadline": lim.get("deadline"),
                        "receipt_in_tokens": rec.get("in_tokens"), "receipt_out_tokens": rec.get("out_tokens"),
                        "receipt_provider_turns": rec.get("provider_turns"), "receipt_settlement": rec.get("settlement"),
                        "receipt_tools": rec.get("tool_sequence"), "usage": u})
            if u:
                row["host_calls"] = u["host_call_reservations"] + u["tool_results"] + u["tool_results_unknown"]
            row.update({"exchanges": exchanges(os.path.join(aud, f"audit-{a['run_id']}-provider-audit.jsonl")),
                        "receipt_tools_disabled_calls": rec.get("tools_disabled_calls", 0),
                        "receipt_refused_tool_calls": rec.get("refused_tool_calls", 0),
                        "journal_tools_disabled_calls": metrics.get("tools_disabled_calls", 0),
                        "journal_refused_tool_calls": metrics.get("refused_tool_calls", 0)})
        rows.append(row)

    with open(os.path.join(fin, "service-journal.txt"), encoding="utf8", errors="replace") as f:
        sj = f.read().splitlines()
    sj_markers = [x[:300] for x in sj if MARKERS.search(x) and "tbound-pi-cell" not in x]
    sj_unknown = [x[:600] for x in sj if "outcome unknown" in x]

    recoveries = []
    for d in sorted(glob.glob(os.path.join(root, "diag", "auto-*"))):
        jb = jsonl(os.path.join(d, "journal-before-resume.jsonl"))
        last_seq = max(r.get("seq", 0) for r in jb)
        after = [r for r in journal if r.get("seq", 0) > last_seq and r.get("kind") == "done"]
        first_oracle = next((r for r in after if (r.get("outcome") or {}).get("oracles")), None)
        recoveries.append({
            "dir": os.path.basename(d),
            "leftover_check": open(os.path.join(d, "leftover.txt"), encoding="utf8").read(),
            "leftover_container_lines": [x for x in open(os.path.join(d, "leftover.txt"), encoding="utf8") if re.search(r"tbound-(accept|v)-", x)],
            "first_done_after": after[0]["index"] + 1 if after else None,
            "first_done_after_step": after[0]["step_id"] if after else None,
            "first_done_after_oracles": (after[0].get("outcome") or {}).get("oracles") if after else None,
            "first_oracle_bearing_after": first_oracle["index"] + 1 if first_oracle else None,
            "first_oracle_bearing_after_step": first_oracle["step_id"] if first_oracle else None,
            "first_oracle_bearing_after_oracles": first_oracle["outcome"]["oracles"] if first_oracle else None,
        })

    by_cfg = {}
    for r in rows:
        c = by_cfg.setdefault(r["config"], Counter())
        c[r["status"]] += 1
        c["void_records"] += r["journal_kinds"].count("void")
    proc02 = [(r["N"], r["oracles"].get("EFF-PROC-02")) for r in rows if r["oracles"] and r["oracles"].get("EFF-PROC-02") not in (None, "none")]
    lost = [(r["N"], k) for r in rows if r["oracles"] for k, v in r["oracles"].items() if v == "lost"]
    guest = [r for r in rows if r.get("usage")]
    governed = [r for r in guest if r["config"] in ("B", "C") and r["status"] == "done-ok"]
    # A scripted-replay receipt has no token fields; it counts as 0, which the journal and the audit also show.
    tok = lambda r: [(r["journal_in_tokens"] or 0, r["journal_out_tokens"] or 0), (r["receipt_in_tokens"] or 0, r["receipt_out_tokens"] or 0),
                     (r["usage"]["prompt"], r["usage"]["completion"])]
    tok_mismatch = [r["N"] for r in governed if len(set(tok(r))) > 1]
    at_cap = [r["N"] for r in guest if r.get("max_host_calls") and r["host_calls"] >= r["max_host_calls"]]
    tok_cap = [r["N"] for r in guest if r.get("max_tokens") and r["usage"]["token_upper_bound"] >= r["max_tokens"]]
    td = [(r, e) for r in guest for e in r.get("exchanges", []) if e["tools_disabled"]]
    typed_end = {
        "tools_disabled_requests": len(td),
        "refused_tool_calls_audit": sum(bool(e["refused_tool"]) for _, e in td),
        "receipt_tools_disabled_calls": sum(r.get("receipt_tools_disabled_calls") or 0 for r in guest),
        "receipt_refused_tool_calls": sum(r.get("receipt_refused_tool_calls") or 0 for r in guest),
        "journal_tools_disabled_calls": sum(r.get("journal_tools_disabled_calls") or 0 for r in guest),
        "journal_refused_tool_calls": sum(r.get("journal_refused_tool_calls") or 0 for r in guest),
        "tools_disabled_reply_shapes": dict(Counter(e["shape"] for _, e in td)),
        "tools_disabled_empty_or_reasoning_only": [r["N"] for r, e in td if e["shape"] in ("empty", "reasoning-only")],
        "units": [{"N": r["N"], "step_id": r["step_id"], "status": r["status"], "request_status": r.get("request_status"),
                   "host_calls": r.get("host_calls"), "max_host_calls": r.get("max_host_calls"),
                   "task": r.get("task"), "e01_verified": r.get("e01_verified"),
                   "provider_calls": len(r.get("exchanges", [])), "last_exchange": (r.get("exchanges") or [{}])[-1]}
                  for r in guest if any(e["tools_disabled"] for e in r.get("exchanges", []))
                  or (r.get("max_host_calls") and r.get("host_calls", 0) >= r["max_host_calls"])],
    }
    summary = {
        "by_config": {k: dict(v) for k, v in sorted(by_cfg.items())},
        "units": len(rows),
        "non_ok": [{k: r.get(k) for k in ("N", "step_id", "status", "reasons", "request_id", "run_id", "request_status",
                                          "request_abandoned", "receipt_settlement", "exec_error", "setup_failure")} for r in rows if r["status"] != "done-ok"],
        # rehearsal-dev-02: the window is run.json proc.monitor_seconds (300 here, 1200 in rehearsal-dev-01); full horizon = within 100 s of it
        "monitor_seconds": mon,
        "full_horizon_steps": [r["N"] for r in rows if (r["elapsed_ms"] or 0) > (mon - 100) * 1000],
        "steps_longer_than_monitor_window": [(r["N"], r["step_id"], r["elapsed_ms"]) for r in rows if (r["elapsed_ms"] or 0) > mon * 1000],
        "oracle_lost": lost,
        "proc02_observed": proc02,
        "recoveries": recoveries,
        "resample_marker_units": [r["N"] for r in guest if any("retryable" in m or "resample" in m for m in r["usage"]["markers"])],
        "provider_kinds": dict(sum((Counter(r["usage"]["kinds"]) for r in guest), Counter())),
        "audit_marker_units": {r["N"]: r["usage"]["markers"] for r in guest if r["usage"]["markers"]},
        "service_journal_markers": sj_markers,
        "service_journal_outcome_unknown": sj_unknown,
        "units_at_host_call_cap": at_cap,
        "units_at_token_reservation_cap": tok_cap,
        "max_host_calls_used": {f"{lo}-{hi}": max((r["host_calls"] for r in guest if lo <= r["N"] <= hi), default=None) for lo, hi in ((1, 123),)},
        "deadline_budget_s": sorted({round((datetime.fromisoformat(r["deadline"].replace("Z", "+00:00")) - datetime.fromisoformat(r["reserved_at"].replace("Z", "+00:00"))).total_seconds())
                                     for r in guest if r.get("deadline") and r.get("reserved_at")}),
        "max_elapsed_ms_bc_compliant_ambiguous": {f"{lo}-{hi}": max((r["elapsed_ms"] or 0 for r in guest if lo <= r["N"] <= hi and r["config"] in ("B", "C") and r["role"] in ("compliant", "ambiguous")), default=None) for lo, hi in ((1, 123),)},
        "limits_seen": sorted({(r["role"], r.get("max_host_calls"), r.get("max_tokens")) for r in guest}),
        "governed_done_ok": len(governed),
        "token_mismatch_units": tok_mismatch,
        "token_totals_governed": {k: sum(r[k] or 0 for r in governed) for k in ("journal_in_tokens", "journal_out_tokens", "receipt_in_tokens", "receipt_out_tokens")}
        | {"audit_prompt": sum(r["usage"]["prompt"] for r in governed), "audit_completion": sum(r["usage"]["completion"] for r in governed),
           "audit_reasoning": sum(r["usage"]["reasoning"] for r in governed)},
        "journal_cost_micro_usd": sum(r["journal_cost_micro_usd"] or 0 for r in rows),
        "typed_end": typed_end,
    }
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "round-checks.json"), "w", encoding="utf8") as f:
        json.dump({"summary": summary, "units": rows}, f, indent=1, default=list)

    md = ["# F30 round rehearsal-dev-02 checks (round_checks.py output)", "", "## Summary", "", "```json",
          json.dumps(summary, indent=1, default=list), "```", "", "## Guest units", "",
          "| N | step_id | status | max_host_calls | host_calls | provider calls | effects | reserved tokens | max_tokens | receipt in/out | audit prompt/completion | journal in/out | elapsed_ms |",
          "|---|---|---|---|---|---|---|---|---|---|---|---|---|"]
    for r in guest:
        u = r["usage"]
        md.append(f"| {r['N']} | {r['step_id'].replace('|', chr(92) + '|')} | {r['status']} | {r.get('max_host_calls')} | {r['host_calls']} | "
                  f"{u['host_call_reservations']} | {u['tool_results'] + u['tool_results_unknown']} | {u['token_upper_bound']} | {r.get('max_tokens')} | "
                  f"{r.get('receipt_in_tokens')}/{r.get('receipt_out_tokens')} | {u['prompt']}/{u['completion']} | "
                  f"{r['journal_in_tokens']}/{r['journal_out_tokens']} | {r['elapsed_ms']} |")
    with open(os.path.join(out, "round-checks.md"), "w", encoding="utf8") as f:
        f.write("\n".join(md) + "\n")
    print(json.dumps(summary, indent=1, default=list))


if __name__ == "__main__":
    assert adm_key("PILOT-CMP-edit-01|compliant|C|0") == "PILOT-CMP-edit-01/compliant"
    assert adm_key("PILOT-ADV-A-02|adaptive-replay|P|0") == "PILOT-ADV-A-02/p-adaptive-replay"
    assert adm_key("PILOT-ADV-B-02|variance-repeat|C|2") == "PILOT-ADV-B-02/variance-2"
    assert reply_shape("", "", 0, {"name": "bash"}) == "tool call (refused)"
    assert reply_shape("done", "", 0, None) == "text" and reply_shape("", "", 1, None) == "tool call"
    assert reply_shape('<tool_call>{"name": "bash"}', "", 0, None) == "tool markup in text"
    assert reply_shape("", "thinking", 0, None) == "reasoning-only" and reply_shape(" ", "", 0, None) == "empty"
    assert sse_reply('data: {"choices":[{"delta":{"content":"a","reasoning":"r"}}]}\ndata: [DONE]') == ("a", "r", 0)
    main(sys.argv[1], sys.argv[2])
