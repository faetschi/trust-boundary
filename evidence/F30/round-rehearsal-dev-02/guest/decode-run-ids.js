// Decodes the receipt run_id of every terminal request in diag/final/requests-now.jsonl (base64 event data) -> all-run-ids.txt, round-run-ids.txt (run_id request_id).
const fs = require("fs"), F = process.argv[2];
const L = fs.readFileSync(F + "/requests-now.jsonl", "utf8").trim().split("\n").map(JSON.parse);
const rows = [];
for (const r of L) {
  if (r.event.kind !== "pi_service_request_terminal") continue;
  const d = JSON.parse(Buffer.from(r.event.data, "base64").toString());
  const id = ((d.response || {}).receipt || {}).run_id;
  if (id) rows.push([id, d.request_id]);
}
fs.writeFileSync(F + "/all-run-ids.txt", rows.map(x => x[0]).join("\n") + "\n");
fs.writeFileSync(F + "/round-run-ids.txt", rows.map(x => x.join(" ")).join("\n") + "\n");
console.log("terminal with run_id:", rows.length);
