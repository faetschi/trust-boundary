#!/usr/bin/env python3
# Reconciliation state of ONE request in a collected request journal (serve-pi-requests.jsonl, `tbound-op collect requests`).
# Usage (WSL): python3 -I reqstate.py <serve-pi-requests.jsonl> <request-id>
# Used by 29-g1-recover.sh (gate). Read-only; prints key=value lines and ends with
#   verdict=completed        terminal record with response.status "completed"                      -> settled
#   verdict=abandoned        terminal "unknown" + a pi_service_request_abandoned record that names the same request, payload and journal key -> reconciled
#   verdict=unreconciled     terminal "unknown", no abandoned record                                -> run 28-abandon-g1.sh
#   verdict=no-terminal      accepted but no terminal record (the request may still run)
#   verdict=terminal-other   terminal with another status (neither completed nor unknown): not covered by the abandon rule, the owner decides
#   verdict=inconsistent     an abandoned record next to a terminal that is not "unknown", or an abandoned record that does not match the request
#   verdict=ambiguous        more than one journaled request carries this id
#   verdict=not-journaled    no accepted record for this id
# Exit 0 for every verdict (the caller decides); exit 2 when the file is empty, unreadable, not a record chain (sequence gap) or not parsable.
# Record layout (tbound supervisor/cmd/tbound/pi_service_*.go): {"version","sequence","previous_hash","event":{"kind","id","data"}}, data = base64 of a JSON object.
# The abandoned record id is "<accepted id>/abandoned", the terminal id "<accepted id>/terminal". The hash chain is NOT re-verified here (tbound-op does that when it appends).
import base64
import json
import os
import re
import sys


def _frozen_commit_guard():
    freeze = os.environ.get("KIT_FREEZE")
    if not freeze:
        sys.exit("KIT_FREEZE is not set (e.g. KIT_FREEZE=F13): refusing to run")
    if not re.fullmatch(r"F[0-9]+", freeze):
        sys.exit("KIT_FREEZE must match ^F[0-9]+$: refusing to run")
    values = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "freezes", freeze + ".env")
    try:
        with open(values, encoding="utf-8") as fh:
            vals = dict(line.split("=", 1) for line in (ln.strip() for ln in fh) if line and not line.startswith("#") and "=" in line)
    except OSError as exc:
        sys.exit("cannot read the freeze values file %s: %s" % (values, exc))
    if vals.get("FREEZE") != freeze:
        sys.exit("%s says FREEZE=%r but KIT_FREEZE=%r: refusing to run" % (values, vals.get("FREEZE"), freeze))
    commit = vals.get("FREEZE_COMMIT", "")
    if commit == "FROZEN_COMMIT_TBD" or not re.fullmatch(r"[0-9a-f]{40}", commit):
        sys.exit("FREEZE_COMMIT in %s is not a 40-hex commit (placeholder FROZEN_COMMIT_TBD still set?): refusing to run" % values)
    if re.fullmatch(r"F0*([0-9]|1[0-2])", freeze):
        sys.exit("%s is historical and read-only (F12 and older): refusing to run" % freeze)
    return commit


_FROZEN_COMMIT = _frozen_commit_guard()

if len(sys.argv) != 3:
    sys.exit(2)
path, rid = sys.argv[1], sys.argv[2]
if not re.fullmatch(r"[A-Za-z0-9._-]{4,128}", rid):
    print("bad request id %r" % rid, file=sys.stderr)
    sys.exit(2)

records = []
with open(path, encoding="utf-8") as fh:
    for number, line in enumerate(fh, 1):
        line = line.strip()
        if not line:
            continue
        try:
            rec = json.loads(line)
            event = rec["event"]
            seq = int(rec["sequence"])
            data = json.loads(base64.b64decode(event["data"]))
            kind, key = str(event["kind"]), str(event["id"])
        except (ValueError, KeyError, TypeError) as exc:
            print("line %d is not a journal record: %s" % (number, exc), file=sys.stderr)
            sys.exit(2)
        records.append((seq, kind, key, data))
if not records:
    print("%s holds no records (0 bytes or empty journal)" % path, file=sys.stderr)
    sys.exit(2)
for (a, *_), (b, *_) in zip(records, records[1:]):
    if b != a + 1:
        print("sequence gap %d -> %d: the file is not a complete record chain" % (a, b), file=sys.stderr)
        sys.exit(2)

accepted = [(k, d) for _, kind, k, d in records if kind == "pi_service_request_accepted" and d.get("request_id") == rid]
print("rid=%s" % rid)
print("records=%d" % len(records))
print("accepted=%d" % len(accepted))
verdict = ""
if not accepted:
    verdict = "not-journaled"
elif len(accepted) > 1:
    verdict = "ambiguous"
else:
    key, acc = accepted[0]
    print("key=%s" % key)
    terminals = [d for _, kind, k, d in records if kind == "pi_service_request_terminal" and k == key + "/terminal"]
    abandons = [d for _, kind, k, d in records if kind == "pi_service_request_abandoned" and k == key + "/abandoned"]
    status = "none"
    if len(terminals) == 1:
        status = str(terminals[0].get("response", {}).get("status", "?"))
    elif len(terminals) > 1:
        status = "multiple"
    print("terminal=%s" % status)
    ab_ok = False
    if abandons:
        a = abandons[0]
        ab_ok = (len(abandons) == 1 and a.get("request_id") == rid and a.get("payload_digest") == acc.get("payload_digest")
                 and a.get("settlement") == "abandoned-unknown" and str(a.get("schema_version", "")).startswith("tbound-pi-service-request-abandoned/"))
    print("abandoned=%s" % ("yes" if ab_ok else ("INVALID" if abandons else "no")))
    if abandons and not ab_ok:
        verdict = "inconsistent"
    elif status == "none":
        verdict = "inconsistent" if abandons else "no-terminal"
    elif status == "completed":
        verdict = "inconsistent" if abandons else "completed"
    elif status == "unknown":
        verdict = "abandoned" if ab_ok else "unreconciled"
    else:
        verdict = "inconsistent" if abandons or status == "multiple" else "terminal-other"
print("verdict=%s" % verdict)
