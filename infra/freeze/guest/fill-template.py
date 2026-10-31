#!/usr/bin/env python3
# R5-1: fill the D09 cell template (runbook table "Template field / Final value") for the $FREEZE base template (G1 cycle).
# $FREEZE change: the registration digest is the 7th argument (the BASE template is built with an all-zero placeholder; the g1 template is made from it by sign-g1.sh).
# Model: F0-c7f9e9c/dryrun/fill-template.py, but every measured value is READ FROM THE $HF FILES, not from arguments or
# constants: IMGDIGEST, ENTRYPOINT_DIGEST, BASH_DIGEST, COSIGN_PUB_DIGEST (kit steps 30-p3 and 40-sign), LANDLOCK_DIGEST and
# XATTR_VIS_DIGEST (40-r5.sh, measured), XATTR_MOPT and XATTR_MAGIC (20-r1.sh, from xattr-measure of the runtime),
# REGDIGEST (30-r2.sh). The four unchanged tool pins (cosign, seccomp, podman, crun) come from glib.sh as arguments and are
# the digests 10-p10.sh verified on the guest.
# Usage (WSL): python3 -I fill-template.py <template.json> <hfdir> <cosign_digest> <seccomp_digest> <podman_digest> <crun_digest> <registration_digest>
# The template is rewritten in place (compact JSON). Refuses an unknown template key, an empty member, a bad digest.
import hashlib
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

tpl, hfdir, cosign_d, seccomp_d, podman_d, crun_d, reg_arg = sys.argv[1:8]
DIG = re.compile(r"^sha256:[0-9a-f]{64}$")


def rd(name):
    with open(os.path.join(hfdir, name), encoding="utf-8") as fh:
        return fh.read().strip()


def dig(label, value):
    if not DIG.match(value):
        sys.exit("%s is not sha256:<64 hex>: %r" % (label, value))
    return value


img = dig("IMGDIGEST", rd("IMGDIGEST"))
entry = dig("ENTRYPOINT_DIGEST", rd("ENTRYPOINT_DIGEST"))
bash = dig("BASH_DIGEST", rd("BASH_DIGEST"))
cpub = dig("COSIGN_PUB_DIGEST", rd("COSIGN_PUB_DIGEST"))
lld = dig("LANDLOCK_DIGEST", rd("LANDLOCK_DIGEST"))
vis = dig("XATTR_VIS_DIGEST", rd("XATTR_VIS_DIGEST"))
mopt = dig("XATTR_MOPT", rd("XATTR_MOPT"))
reg = dig("registration digest argument", reg_arg)   # $FREEZE: explicit (the base template uses an all-zero placeholder)
magic = int(rd("XATTR_MAGIC"))
for label, value in (("cosign", cosign_d), ("seccomp", seccomp_d), ("podman", podman_d), ("crun", crun_d)):
    dig(label, value)

# Cell user (the image user) and service user; they must differ (runbook R5-1 table).
CELL_UID, CELL_GID, SVC_UID, SVC_GID = 1000, 1000, 995, 986
PROFILE = "rootless-single-own-uid-gid/v1"
identity = ("tbound-d09-user-namespace-map/v1\x00" + PROFILE +
            "\x00uid-inside=%d:0:1\x00gid-inside=%d:0:1\x00uid-host=%d:%d:1\x00gid-host=%d:%d:1\x00parent=initial-userns/v1"
            % (CELL_UID, CELL_GID, CELL_UID, SVC_UID, CELL_GID, SVC_GID))
mapping = "sha256:" + hashlib.sha256(identity.encode()).hexdigest()  # DigestD09UserNamespaceMapping(1000, 1000, 995, 986)

want = {
    "expected_image_digest": img,
    "cosign_path": "/opt/tbound/cell/cosign",
    "cosign_digest": cosign_d,
    "cosign_public_key_path": "/opt/tbound/cell/cosign.pub",
    "cosign_public_key_digest": cpub,
    "oci_layout_path": "/opt/tbound/cell/pi-cell-oci",
    "seccomp_profile_path": "/opt/tbound/cell/seccomp.json",
    "seccomp_profile_digest": seccomp_d,
    "landlock_abi": 8,
    "landlock_ruleset_digest": lld,
    "landlock_ro_path": "/usr,/lib,/lib64,/etc,/runtime,/var/lib/tbound/pi-worker,/proc,/dev",
    "landlock_rw_path": "/work,/tmp",
    "frozen_capabilities": "0000000000000000",
    "entrypoint_digest": entry,
    "cgroups_path": "/sys/fs/cgroup/system.slice/tbound-pi.service/cells",
    "cpus": "1",
    "pids_max": 256,
    "memory_mb": 1024,
    "podman_path": "/usr/bin/podman",
    "podman_digest": podman_d,
    "crun_path": "/usr/bin/crun",
    "crun_digest": crun_d,
    "command_executable": "/bin/bash",
    "command_executable_digest": bash,
    "cell_uid": CELL_UID,
    "cell_gid": CELL_GID,
    "xattr_visibility_profile_digest": vis,
    "xattr_visibility_authority": "root-attested-bounded-no-hidden-trusted/v1",
    "xattr_filesystem_magic": magic,
    "xattr_mount_options_digest": mopt,
    "xattr_scan_uid": SVC_UID,
    "xattr_scan_gid": SVC_GID,
    "xattr_scan_privilege_profile": "service-nonroot-no-extra-groups-empty-caps-nnp-initial-userns/v1",
    "managed_live_root_registration_digest": reg,
    "user_namespace_mapping_profile": PROFILE,
    "user_namespace_mapping_digest": mapping,
}

with open(tpl, encoding="utf-8") as fh:
    t = json.load(fh)
unknown = sorted(k for k in want if k not in t)
if unknown:
    sys.exit("template lacks keys the runbook table sets (schema changed?): %s" % unknown)
if t.get("image") != "localhost/tbound/pi-cell:latest":
    sys.exit("template image is %r, runbook says localhost/tbound/pi-cell:latest" % t.get("image"))
t.update(want)
empty = sorted(k for k, v in t.items() if v in ("", None))
if empty:
    sys.exit("template members still empty after filling: %s" % empty)
if t["cell_uid"] == SVC_UID or t["cell_gid"] == SVC_GID:
    sys.exit("cell uid/gid must differ from the service uid/gid")
with open(tpl, "w", encoding="utf-8") as fh:
    json.dump(t, fh, separators=(",", ":"))
print("filled %s: %d members, mapping digest %s" % (tpl, len(t), mapping))
print("mapping_digest %s" % mapping)
