#!/usr/bin/env python3
# Print one member of a JSON file: jget.py <file> <dotted.key>. Run under WSL as: python3 -I jget.py <file> <key>
import json
import os
import re
import sys

# Validate freeze selection before any input/output processing.
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

value = json.load(open(sys.argv[1], encoding="utf-8"))
for part in sys.argv[2].split("."):
    value = value[part]
print(value)
