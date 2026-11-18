#!/usr/bin/env python3
"""F29 round checks (from the F28 rehearsal checks) that extract.py does not make. Read-only on every input.

Joins the runner journal to the guest request journal, the admission catalog and the per-run provider
audits (map.json from map.js), and writes round-checks.json plus round-checks.md:
  1. per-configuration status, including the journal kind "abandoned";
  2. the guest request of every non-done-ok unit;
  3. per guest unit: limits from the admission entry, host calls (provider calls + executed effects),
     reserved provider tokens, receipt tokens, provider-audit tokens, elapsed time;
  4. the steps after each recovery and their oracles;
  5. decision-13 resample traces in the provider audits;
  6. cap, deadline and settlement markers in the audits and the service journal;
  7. the leftover-container check output at each recovery.

Usage: python -I f28_checks.py <rehearsal root> <out dir>
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
        status = ("done-unknown" if (done.get("exec_error") or oc.get("evidence_loss") not in ("none", None)) else "done-ok") \
            if done else ("abandoned" if "abandoned" in s["kinds"] else "other")
        row = {"N": s["N"], "step_id": s["step_id"], "config": cfg, "role": role, "status": status,
               "journal_kinds": s["kinds"], "reasons": s.get("reasons", []),
               "elapsed_ms": (done or {}).get("elapsed_ms"), "oracles": oc.get("oracles"),
               "tool_attempt": oc.get("tool_attempt"), "journal_in_tokens": metrics.get("in_tokens"),
               "journal_out_tokens": metrics.get("out_tokens"), "journal_cost_micro_usd": metrics.get("cost_micro_usd"),
               "exec_error": (done or {}).get("exec_error")}
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
    summary = {
        "by_config": {k: dict(v) for k, v in sorted(by_cfg.items())},
        "units": len(rows),
        "non_ok": [{k: r.get(k) for k in ("N", "step_id", "status", "reasons", "request_id", "run_id", "request_status",
                                          "request_abandoned", "receipt_settlement", "exec_error")} for r in rows if r["status"] != "done-ok"],
        "full_horizon_steps": [r["N"] for r in rows if (r["elapsed_ms"] or 0) > 1_100_000],
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
        "max_elapsed_ms": {f"{lo}-{hi}": max((r["elapsed_ms"] or 0 for r in guest if lo <= r["N"] <= hi and r["config"] in ("B", "C") and r["role"] in ("compliant", "ambiguous")), default=None) for lo, hi in ((1, 123),)},
        "limits_seen": sorted({(r["role"], r.get("max_host_calls"), r.get("max_tokens")) for r in guest}),
        "governed_done_ok": len(governed),
        "token_mismatch_units": tok_mismatch,
        "token_totals_governed": {k: sum(r[k] or 0 for r in governed) for k in ("journal_in_tokens", "journal_out_tokens", "receipt_in_tokens", "receipt_out_tokens")}
        | {"audit_prompt": sum(r["usage"]["prompt"] for r in governed), "audit_completion": sum(r["usage"]["completion"] for r in governed),
           "audit_reasoning": sum(r["usage"]["reasoning"] for r in governed)},
        "journal_cost_micro_usd": sum(r["journal_cost_micro_usd"] or 0 for r in rows),
    }
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "round-checks.json"), "w", encoding="utf8") as f:
        json.dump({"summary": summary, "units": rows}, f, indent=1, default=list)

    md = ["# F29 round rehearsal-dev-01 checks (round_checks.py output)", "", "## Summary", "", "```json",
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
    main(sys.argv[1], sys.argv[2])
