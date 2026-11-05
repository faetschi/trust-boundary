#!/usr/bin/env python3
# R2-0 / RG-2: build a service config (smoke, g1, route probe or pilot) from infra/guest/serve-pi-service.<name>.json of the frozen source.
# Does what the runbook's sed commands do: replace the two <MEASURE> placeholders by CD and SD (for the pilot template also <CORPUS_DIGEST>
# by the optional CORPUS_DIGEST), then require that no placeholder is left and that exactly two (three with CORPUS_DIGEST) "sha256:<64 hex>" members exist.
# Usage (WSL): python3 -I mk-svc-config.py <template.json> <out.json> <CD> <SD> [<CORPUS_DIGEST>]
# An existing out file is accepted only if it is byte-identical to what would be written (idempotent re-run).
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

src, out, cd, sd = sys.argv[1:5]
corpus = sys.argv[5] if len(sys.argv) > 5 else None
dig = re.compile(r"^sha256:[0-9a-f]{64}$")
for name, value in (("CD", cd), ("SD", sd), ("CORPUS_DIGEST", corpus)):
    if value is None:
        continue
    if not dig.match(value):
        sys.exit("%s is not sha256:<64 hex>: %r" % (name, value))
text = open(src, encoding="utf-8", newline="").read()
if "\r" in text:
    sys.exit("%s contains CR (must be LF)" % src)
subs = [("containers_conf_digest", "<MEASURE>", cd), ("storage_conf_digest", "<MEASURE>", sd)]
if corpus:
    subs.append(("corpus_manifest_digest", "<CORPUS_DIGEST>", corpus))
for key, placeholder, value in subs:
    old = '"%s": "%s"' % (key, placeholder)
    if text.count(old) != 1:
        sys.exit("expected exactly one %s in %s" % (old, src))
    text = text.replace(old, '"%s": "%s"' % (key, value))
if "MEASURE" in text or "CORPUS_DIGEST>" in text:
    sys.exit("placeholder left in the output (<MEASURE> or <CORPUS_DIGEST>: the pilot template needs the CORPUS_DIGEST argument)")
members = re.findall(r'"sha256:[0-9a-f]{64}"', text)
if len(members) != len(subs):
    sys.exit("expected %d sha256 members, found %d" % (len(subs), len(members)))
data = text.encode("utf-8")
if os.path.exists(out):
    if open(out, "rb").read() != data:
        sys.exit("%s exists and differs from the config built from CD/SD; refusing to overwrite" % out)
    print("unchanged %s" % out)
else:
    with open(out, "xb") as fh:
        fh.write(data)
    os.chmod(out, 0o600)
    print("wrote %s" % out)
print("sha256 members: %d" % len(members))
