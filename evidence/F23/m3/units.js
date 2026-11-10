// Join host journal steps to guest admissions/runs and provider audits; copy per-unit audits for extract.py; sum provider usage.
// Usage: node units.js <m3-f23 root>. Read-only on inputs; writes diag/units/* and analysis/units.json.
const fs = require("fs"), path = require("path");
const R = process.argv[2], A = path.join(R, "diag/final/audit"), U = path.join(R, "diag/units");
fs.mkdirSync(U, { recursive: true });
const map = JSON.parse(fs.readFileSync(path.join(R, "diag/final/map.json"), "utf8"));
const J = fs.readFileSync(path.join(R, "runs/pilot/journal.jsonl"), "utf8").trim().split("\n").map(JSON.parse);
const steps = {}; for (const r of J) if (r.step_id) { const s = steps[r.step_id] ||= { index: r.index, kinds: [] }; s.kinds.push(r.kind); if (r.kind === "done") s.done = r; }
const admKey = sid => { const [cs, role, cfg, rep] = sid.split("|");
  if (role === "variance-repeat") return `${cs}/variance-${rep}`;
  return `${cs}/${cfg === "C" ? "" : cfg.toLowerCase() + "-"}${role}`; };
const byKey = {}; for (const a of map) { const k = (a.host_admission_id || "").replace(/\/\d+$/, ""); (byKey[k] ||= []).push(a); }
const dec = s => { try { return JSON.parse(Buffer.from(s, "base64").toString()); } catch { return null; } };
function usage(file) {
  const u = { exchanges: 0, unknown: 0, prompt: 0, completion: 0, reasoning: 0, cost_usd: 0, parallel_streams: 0, failures: [] };
  if (!fs.existsSync(file)) return null;
  for (const line of fs.readFileSync(file, "utf8").split("\n").filter(Boolean)) {
    const r = JSON.parse(line), k = r.event.kind, d = dec(r.event.data) || {};
    if (k === "provider_exchange_unknown") { u.unknown++; u.failures.push(String(d.failure || "").slice(0, 160)); }
    if (k !== "provider_exchange" && k !== "provider_exchange_unknown") continue;
    if (k === "provider_exchange") u.exchanges++;
    const body = typeof d.response === "string" ? Buffer.from(d.response, "base64").toString() : "";
    let par = false;
    for (const l of body.split("\n")) { if (!l.startsWith("data: ") || l === "data: [DONE]") continue;
      let c; try { c = JSON.parse(l.slice(6)); } catch { continue; }
      for (const ch of c.choices || []) for (const tc of (ch.delta && ch.delta.tool_calls) || []) if ((tc.index || 0) >= 1) par = true;
      if (c.usage) { u.prompt += c.usage.prompt_tokens || 0; u.completion += c.usage.completion_tokens || 0;
        u.reasoning += (c.usage.completion_tokens_details || {}).reasoning_tokens || 0; u.cost_usd += Number(c.usage.cost || 0); } }
    if (par) u.parallel_streams++;
  }
  return u;
}
const rows = [];
for (const [sid, s] of Object.entries(steps).sort((a, b) => a[1].index - b[1].index)) {
  const cands = byKey[admKey(sid)] || [], N = s.index + 1;
  const runs = cands.map(a => ({ host_admission_id: a.host_admission_id, request_id: a.request_id, run_id: a.run_id,
    req_status: a.req ? (a.req.abandoned ? "abandoned:" + a.req.status : a.req.status) : null }));
  const ex = s.done && s.done.exec_error || "";
  const row = { N, index: s.index, step_id: sid, host: s.done ? (ex ? "done-unknown" : "done-ok") : s.kinds.includes("abandoned") ? "abandoned" : "other", runs };
  const last = runs[runs.length - 1];
  if (last) { const pf = path.join(A, `audit-${last.run_id}-provider-audit.jsonl`); row.usage = usage(pf);
    if (fs.existsSync(pf)) fs.copyFileSync(pf, path.join(U, `u${N}-provider-audit.jsonl`)); }
  rows.push(row);
}
fs.writeFileSync(path.join(R, "analysis/units.json"), JSON.stringify(rows, null, 1));
const t = { prompt: 0, completion: 0, reasoning: 0, cost_usd: 0, exchanges: 0, unknown: 0, parallel_streams: 0, units_with_model: 0 };
for (const r of rows) if (r.usage) { for (const k of ["prompt", "completion", "reasoning", "cost_usd", "exchanges", "unknown", "parallel_streams"]) t[k] += r.usage[k]; if (r.usage.exchanges + r.usage.unknown) t.units_with_model++; }
console.log("steps", rows.length, "with guest run", rows.filter(r => r.runs.length).length, "multi-admission", rows.filter(r => r.runs.length > 1).map(r => r.N));
console.log("totals", t);
for (const r of rows) if (r.host !== "done-ok") console.log(r.N, r.step_id, r.host, JSON.stringify(r.runs.map(x => x.req_status)), r.usage ? JSON.stringify({ ex: r.usage.exchanges, unk: r.usage.unknown, par: r.usage.parallel_streams, f: r.usage.failures }) : "no provider audit");
