#!/usr/bin/env python3
# RG-1 for NAME=dr: the route-probe service config with a NEW admission id (the one field set by hand, PROVISIONING-SIGNING.md RG table).
# Same as mk-svc-config.py (two <MEASURE> placeholders -> CD, SD) plus exactly one replacement: the value of "workflow_admission_id".
# The dispatcher (tbound-op cfg-install dr, PY_CFGCHECK mode dr) accepts exactly this: the shipped route-probe template with CD/SD and a
# different admission id matching [a-z0-9][a-z0-9-]{4,62}[a-z0-9], on the line `  "workflow_admission_id": "<id>",`, and nothing else changed.
# Usage (WSL): python3 -I mk-dr-config.py <route-probe template.json> <out.json> <CD> <SD> <NEW_ADMISSION_ID>
# An existing out file is accepted only if byte-identical to what would be written (idempotent re-run).
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

src, out, cd, sd, new_id = sys.argv[1:6]
dig = re.compile(r"^sha256:[0-9a-f]{64}$")
for name, value in (("CD", cd), ("SD", sd)):
    if not dig.match(value):
        sys.exit("%s is not sha256:<64 hex>: %r" % (name, value))
if not re.fullmatch(r"[a-z0-9][a-z0-9-]{4,62}[a-z0-9]", new_id):
    sys.exit("admission id %r does not match [a-z0-9][a-z0-9-]{4,62}[a-z0-9]" % new_id)
text = open(src, encoding="utf-8", newline="").read()
if "\r" in text:
    sys.exit("%s contains CR (must be LF)" % src)
old_ids = re.findall(r'^  "workflow_admission_id": "([^"\\]+)",$', text, re.M)
if len(old_ids) != 1:
    sys.exit("expected exactly one workflow_admission_id line in %s, found %d" % (src, len(old_ids)))
if old_ids[0] == new_id:
    sys.exit("the new admission id equals the template's (%s): dr needs a NEW id" % new_id)
old = '"workflow_admission_id": "%s"' % old_ids[0]
if text.count(old) != 1:
    sys.exit("admission id member not unique")
text = text.replace(old, '"workflow_admission_id": "%s"' % new_id)
for key, value in (("containers_conf_digest", cd), ("storage_conf_digest", sd)):
    o = '"%s": "<MEASURE>"' % key
    if text.count(o) != 1:
        sys.exit("expected exactly one %s in %s" % (o, src))
    text = text.replace(o, '"%s": "%s"' % (key, value))
if "MEASURE" in text:
    sys.exit("MEASURE placeholder left in the output")
members = re.findall(r'"sha256:[0-9a-f]{64}"', text)
if len(members) != 2:
    sys.exit("expected 2 sha256 members, found %d" % len(members))
data = text.encode("utf-8")
if os.path.exists(out):
    if open(out, "rb").read() != data:
        sys.exit("%s exists and differs from the config built from CD/SD/id; refusing to overwrite" % out)
    print("unchanged %s" % out)
else:
    with open(out, "xb") as fh:
        fh.write(data)
    os.chmod(out, 0o600)
    print("wrote %s" % out)
print("admission id %s (template had %s); sha256 members: %d" % (new_id, old_ids[0], len(members)))
