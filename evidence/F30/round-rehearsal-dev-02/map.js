// Map guest catalog/requests/audits to host plan steps. Read-only. Usage: node map.js <diag/final/audit dir> <out.json>
const fs = require("fs"), path = require("path");
const dir = process.argv[2], out = process.argv[3], since = process.argv[4] || "";  // since: ISO time; earlier admissions (an earlier round on this guest) are dropped
const rd = f => fs.readFileSync(path.join(dir, f), "utf8").trim().split("\n").filter(Boolean).map(JSON.parse);
const dec = e => { try { return JSON.parse(Buffer.from(e.data, "base64").toString()); } catch { return {}; } };
const adm = {}; // admission hash -> record
for (const r of rd("catalog.jsonl")) {
  const d = dec(r.event), k = r.event.kind, s = JSON.stringify(d);
  const ah = d.admission_hash || (d.entry && d.entry.admission_record_hash);
  if (k === "pi_workflow_admission_reserved") { const e = d.entry || d; adm[e.admission_record_hash] = { host_admission_id: e.host_admission_id, request_id: e.request_id, task_id: e.task_id, reserved_at: e.reserved_at, kinds: [] }; }
  const a = adm[ah]; if (!a) continue;
  a.kinds.push(k);
  const m = s.match(/production-run-[0-9a-f]{24}/); if (m && !a.run_id) a.run_id = m[0];
  if (k === "pi_workflow_attacker_usage") a.attacker_usage = d.usage || d.entry || d;
}
const req = {};
for (const r of rd("audit-serve-pi-requests.jsonl")) {
  const d = dec(r.event), k = r.event.kind;
  if (k === "pi_service_request_accepted") req[d.request_id] = { task_id: d.task_id };
  else if (k === "pi_service_request_terminal" && req[d.request_id]) { const resp = d.response || {}; req[d.request_id].status = resp.status; req[d.request_id].error_code = resp.error_code; const rid = resp.receipt && resp.receipt.run_id; if (rid) req[d.request_id].run_id = rid; }
  else if (k === "pi_service_request_abandoned" && req[d.request_id]) req[d.request_id].abandoned = true;
}
const rows = Object.values(adm).filter(a => !since || (a.reserved_at && Date.parse(a.reserved_at) >= Date.parse(since))).map(a => ({ ...a, kinds: undefined, n_events: a.kinds.length, terminal: a.kinds.includes("pi_workflow_terminal"), req: req[a.request_id] || null, run_id: (req[a.request_id] || {}).run_id || a.run_id || null }));
fs.writeFileSync(out, JSON.stringify(rows, null, 1));
const c = {}; for (const r of rows) { const s = r.req ? (r.req.abandoned ? "abandoned" : r.req.status) : "no-request"; c[s] = (c[s] || 0) + 1; }
console.log(rows.length, c, rows.filter(r => !r.run_id).length, "without run_id");
console.log(rows.slice(-3).map(r => JSON.stringify(r).slice(0, 300)).join("\n"));
