#!/usr/bin/env python3
"""Offline, deterministic assembler for the governed D09 cell image.

Why this exists: the cell image is "base image + signed entrypoint + a few
directories". Building it with `podman build` needs mutable-tag pulls, an
in-build `npm install` and a container runtime, and it yields a different
manifest digest on every run. This tool builds the same image content as
packaging/governed/Containerfile directly as an OCI image layout:

  * base image layers are taken verbatim, by digest, from a content-addressed
    cache filled once by `pull` (every blob is verified against its digest; the
    only network use of the whole build);
  * one new uncompressed layer (no zlib dependence) holds the entrypoint, the
    adapter sources and the mount-target directories, with fixed mtime, owner
    and mode;
  * the config and manifest are serialised canonically.

Same inputs give the same manifest digest. Standard library only.

Subcommands:
  resolve REPO TAG             print the digests a tag currently resolves to (network)
  pull    --lock L --cache C   fetch the digest-pinned base images (network, once)
  build   --lock L --cache C --entrypoint BIN --adapter DIR --epoch N --out DIR
  measure --layout DIR         print manifest/config digests and the node, bash and entrypoint sha256
"""
import argparse
import hashlib
import io
import json
import os
import shutil
import stat
import sys
import tarfile
import urllib.error
import urllib.request

REGISTRY = "https://registry-1.docker.io"
AUTH = "https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull"
MT_INDEX = "application/vnd.oci.image.index.v1+json"
MT_MANIFEST = "application/vnd.oci.image.manifest.v1+json"
MT_CONFIG = "application/vnd.oci.image.config.v1+json"
MT_LAYER = "application/vnd.oci.image.layer.v1.tar"
MT_LAYER_GZ = "application/vnd.oci.image.layer.v1.tar+gzip"
DOCKER_INDEX = "application/vnd.docker.distribution.manifest.list.v2+json"
DOCKER_MANIFEST = "application/vnd.docker.distribution.manifest.v2+json"
DOCKER_LAYER_GZ = "application/vnd.docker.image.rootfs.diff.tar.gzip"
ACCEPT = ",".join([MT_INDEX, MT_MANIFEST, DOCKER_INDEX, DOCKER_MANIFEST])
REF_NAME = "localhost/tbound/pi-cell:latest"
LABELS = {
    "org.opencontainers.image.title": "tbound-pi-cell",
    "org.opencontainers.image.description": "Governed D09 Pi worker cell (non-claim-bearing until frozen and signed)",
    "org.opencontainers.image.licenses": "MIT",
}
ENTRYPOINT = "/usr/local/bin/tbound-pi-entrypoint"
# Directories created or re-owned by the Containerfile, as (path, mode, uid, gid).
# uid/gid 1000 is the `node` user of the node base image (checked against the
# base image's own /etc/passwd at build time).
LAYER_DIRS = [
    ("usr/local/lib/tbound", 0o755, 0, 0),
    ("usr/local/lib/tbound/runtime", 0o755, 0, 0),
    ("usr/local/lib/tbound/runtime/src", 0o755, 0, 0),
    ("runtime", 0o755, 0, 0),
    ("work", 0o755, 0, 0),
    ("var/lib/tbound", 0o700, 1000, 1000),
    ("var/lib/tbound/pi-worker", 0o755, 1000, 1000),
]
ADAPTER_FILES = ["package.json", "package-lock.json"]


def sha256_hex(data):
    return hashlib.sha256(data).hexdigest()


def digest_of(data):
    return "sha256:" + sha256_hex(data)


def canonical(obj):
    return json.dumps(obj, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def die(msg):
    sys.stderr.write("cell_image: " + msg + "\n")
    sys.exit(1)


# ---------------------------------------------------------------- registry --

class Registry:
    def __init__(self, repository):
        self.repository = repository
        self.token = None

    def _open(self, url, accept=None):
        if self.token is None:
            with urllib.request.urlopen(AUTH % self.repository, timeout=60) as r:
                self.token = json.load(r)["token"]
        req = urllib.request.Request(url)
        req.add_header("Authorization", "Bearer " + self.token)
        if accept:
            req.add_header("Accept", accept)
        return urllib.request.urlopen(req, timeout=300)

    def manifest(self, ref):
        """Return (digest, media type, bytes). A digest ref is verified."""
        with self._open("%s/v2/%s/manifests/%s" % (REGISTRY, self.repository, ref), ACCEPT) as r:
            data = r.read()
            mt = r.headers.get("Content-Type", "").split(";")[0]
        d = digest_of(data)
        if ref.startswith("sha256:") and d != ref:
            die("manifest %s hashes to %s" % (ref, d))
        return d, mt, data

    def blob(self, digest, dest):
        """Stream a blob into dest, verifying its digest; skip if cached."""
        if os.path.exists(dest) and file_digest(dest) == digest:
            return False
        tmp = dest + ".part"
        h = hashlib.sha256()
        with self._open("%s/v2/%s/blobs/%s" % (REGISTRY, self.repository, digest)) as r, open(tmp, "wb") as f:
            while True:
                chunk = r.read(1 << 20)
                if not chunk:
                    break
                h.update(chunk)
                f.write(chunk)
        if "sha256:" + h.hexdigest() != digest:
            os.unlink(tmp)
            die("blob %s hashes to sha256:%s" % (digest, h.hexdigest()))
        os.replace(tmp, dest)
        return True


def file_digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return "sha256:" + h.hexdigest()


def pick_platform(index, platform="linux/amd64"):
    osname, arch = platform.split("/")
    hits = [m for m in index["manifests"]
            if m.get("platform", {}).get("os") == osname and m.get("platform", {}).get("architecture") == arch
            and "variant" not in m.get("platform", {})]
    if len(hits) != 1:
        die("index has %d %s manifests" % (len(hits), platform))
    return hits[0]


def cmd_resolve(args):
    reg = Registry(args.repository)
    idx_d, mt, raw = reg.manifest(args.tag)
    index = json.loads(raw)
    m = pick_platform(index)
    _, _, mraw = reg.manifest(m["digest"])
    out = {"repository": args.repository, "tag": args.tag, "index_digest": idx_d, "index_media_type": mt,
           "platform": "linux/amd64", "manifest_digest": m["digest"],
           "config_digest": json.loads(mraw)["config"]["digest"]}
    print(json.dumps(out, indent=1, sort_keys=True))


def load_lock(path):
    with open(path, "rb") as f:
        lock = json.load(f)
    if lock.get("schema") != "tbound-base-images/v1":
        die("unexpected lock schema")
    return lock["images"]


def blob_path(cache, digest):
    if not digest.startswith("sha256:") or len(digest) != 71:
        die("bad digest " + digest)
    return os.path.join(cache, "blobs", "sha256", digest[7:])


def cmd_pull(args):
    os.umask(0o077)
    os.makedirs(os.path.join(args.cache, "blobs", "sha256"), exist_ok=True)
    record = {}
    for name, img in sorted(load_lock(args.lock).items()):
        reg = Registry(img["repository"])
        idx_d, _, raw = reg.manifest(img["index_digest"])
        index = json.loads(raw)
        m = pick_platform(index, img["platform"])
        if m["digest"] != img["manifest_digest"]:
            die("%s: pinned index no longer selects the pinned manifest" % name)
        m_d, _, mraw = reg.manifest(m["digest"])
        manifest = json.loads(mraw)
        if manifest["config"]["digest"] != img["config_digest"]:
            die("%s: config digest differs from the lock" % name)
        for d, data in ((idx_d, raw), (m_d, mraw)):
            write_blob(args.cache, d, data)
        reg.blob(img["config_digest"], blob_path(args.cache, img["config_digest"]))
        fetched = 0
        if img.get("pull_layers"):
            for layer in manifest["layers"]:
                fetched += reg.blob(layer["digest"], blob_path(args.cache, layer["digest"]))
        record[name] = {"ref": "%s@%s" % (img["repository"], idx_d), "index_digest": idx_d,
                        "manifest_digest": m_d, "config_digest": img["config_digest"],
                        "layers": [l["digest"] for l in manifest["layers"]] if img.get("pull_layers") else "not pulled",
                        "blobs_fetched": fetched}
    with open(os.path.join(args.cache, "pull-record.json"), "w") as f:
        json.dump(record, f, indent=1, sort_keys=True)
        f.write("\n")
    print(json.dumps(record, indent=1, sort_keys=True))


def write_blob(root, digest, data, check=True):
    if check and digest_of(data) != digest:
        die("internal: blob does not hash to " + digest)
    path = blob_path(root, digest)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    if not os.path.exists(path):
        with open(path, "wb") as f:
            f.write(data)


# ------------------------------------------------------------------- build --

def tar_entry(name, mode, uid, gid, mtime, kind, size=0):
    ti = tarfile.TarInfo(name)
    ti.mode = mode
    ti.uid, ti.gid = uid, gid
    ti.uname = ti.gname = ""
    ti.mtime = mtime
    ti.type = kind
    ti.size = size
    return ti


def build_layer(entrypoint, adapter, epoch):
    """Deterministic, uncompressed layer tar. Returns bytes."""
    entries = {}  # name -> (TarInfo, data or None)
    for path, mode, uid, gid in LAYER_DIRS:
        entries[path + "/"] = (tar_entry(path, mode, uid, gid, epoch, tarfile.DIRTYPE), None)
    with open(entrypoint, "rb") as f:
        data = f.read()
    entries[ENTRYPOINT.lstrip("/")] = (tar_entry(ENTRYPOINT.lstrip("/"), 0o755, 0, 0, epoch, tarfile.REGTYPE, len(data)), data)
    base = "usr/local/lib/tbound/runtime"
    for fname in ADAPTER_FILES:
        add_file(entries, os.path.join(adapter, fname), base + "/" + fname, epoch)
    src = os.path.join(adapter, "src")
    for root, dirs, files in os.walk(src):
        dirs.sort()
        rel = os.path.relpath(root, adapter).replace(os.sep, "/")
        if rel != "src":
            entries[base + "/" + rel + "/"] = (tar_entry(base + "/" + rel, 0o755, 0, 0, epoch, tarfile.DIRTYPE), None)
        for fname in sorted(files):
            add_file(entries, os.path.join(root, fname), base + "/" + rel + "/" + fname, epoch)
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.GNU_FORMAT) as tf:
        for key in sorted(entries):
            ti, payload = entries[key]
            tf.addfile(ti, io.BytesIO(payload) if payload is not None else None)
    return buf.getvalue()


def add_file(entries, path, name, epoch):
    st = os.lstat(path)
    if not stat.S_ISREG(st.st_mode):
        die("not a regular file: " + path)
    with open(path, "rb") as f:
        data = f.read()
    mode = 0o755 if st.st_mode & 0o100 else 0o644
    entries[name] = (tar_entry(name, mode, 0, 0, epoch, tarfile.REGTYPE, len(data)), data)


def layer_passwd_has_node(cache, layers):
    """Confirm uid/gid 1000 is node:node in the base layers (last writer wins)."""
    found_passwd = found_group = None
    for layer in layers:
        with tarfile.open(blob_path(cache, layer["digest"]), "r:*") as tf:
            for m in tf:
                if m.name in ("etc/passwd", "./etc/passwd"):
                    found_passwd = tf.extractfile(m).read().decode()
                elif m.name in ("etc/group", "./etc/group"):
                    found_group = tf.extractfile(m).read().decode()
    ok_p = found_passwd and any(l.startswith("node:x:1000:1000:") for l in found_passwd.splitlines())
    ok_g = found_group and any(l.startswith("node:x:1000:") for l in found_group.splitlines())
    if not (ok_p and ok_g):
        die("uid/gid 1000 is not node:node in the base image")


def cmd_build(args):
    os.umask(0o077)
    if os.path.exists(args.out):
        die("output path exists: " + args.out)
    img = load_lock(args.lock)["node"]
    idx_raw = open(blob_path(args.cache, img["index_digest"]), "rb").read()
    index = json.loads(idx_raw)
    m = pick_platform(index, img["platform"])
    if m["digest"] != img["manifest_digest"]:
        die("cache index selects a different manifest than the lock")
    manifest = json.loads(open(blob_path(args.cache, m["digest"]), "rb").read())
    config_raw = open(blob_path(args.cache, manifest["config"]["digest"]), "rb").read()
    if digest_of(config_raw) != img["config_digest"]:
        die("cached base config does not match the lock")
    config = json.loads(config_raw)
    base_layers = []
    for layer in manifest["layers"]:
        p = blob_path(args.cache, layer["digest"])
        if not os.path.exists(p) or file_digest(p) != layer["digest"]:
            die("base layer missing or corrupt in cache: " + layer["digest"])
        mt = layer["mediaType"]
        if mt == DOCKER_LAYER_GZ:
            mt = MT_LAYER_GZ
        elif mt not in (MT_LAYER_GZ, MT_LAYER):
            die("unsupported base layer media type " + mt)
        base_layers.append({"mediaType": mt, "digest": layer["digest"], "size": layer["size"]})
    layer_passwd_has_node(args.cache, manifest["layers"])

    layer = build_layer(args.entrypoint, args.adapter, args.epoch)
    layer_digest = digest_of(layer)

    created = datetime_utc(args.epoch)
    cfg = config["config"]
    cfg["Env"] = [e for e in cfg.get("Env", []) if not e.startswith("TBOUND_PREFIX=")] + ["TBOUND_PREFIX=/var/lib/tbound"]
    cfg["Entrypoint"] = [ENTRYPOINT]
    cfg.pop("Cmd", None)
    cfg["Labels"] = dict(cfg.get("Labels") or {}, **LABELS)
    config["created"] = created
    config["rootfs"]["diff_ids"] = list(config["rootfs"]["diff_ids"]) + [layer_digest]
    config["history"] = list(config.get("history", [])) + [
        {"created": created, "created_by": "tbound packaging/governed/cell_image.py build (entrypoint, adapter sources, mount targets)"}]
    config.pop("container", None)
    config.pop("docker_version", None)
    config_raw = canonical(config)
    config_digest = digest_of(config_raw)

    new_manifest = {
        "schemaVersion": 2,
        "mediaType": MT_MANIFEST,
        "config": {"mediaType": MT_CONFIG, "digest": config_digest, "size": len(config_raw)},
        "layers": base_layers + [{"mediaType": MT_LAYER, "digest": layer_digest, "size": len(layer)}],
    }
    manifest_raw = canonical(new_manifest)
    manifest_digest = digest_of(manifest_raw)

    os.makedirs(os.path.join(args.out, "blobs", "sha256"))
    for l in base_layers:
        os.link(blob_path(args.cache, l["digest"]), blob_path(args.out, l["digest"])) \
            if same_fs(args.cache, args.out) else shutil.copyfile(blob_path(args.cache, l["digest"]), blob_path(args.out, l["digest"]))
    write_blob(args.out, layer_digest, layer)
    write_blob(args.out, config_digest, config_raw)
    write_blob(args.out, manifest_digest, manifest_raw)
    with open(os.path.join(args.out, "oci-layout"), "wb") as f:
        f.write(b'{"imageLayoutVersion":"1.0.0"}')
    idx = {"schemaVersion": 2, "mediaType": MT_INDEX,
           "manifests": [{"mediaType": MT_MANIFEST, "digest": manifest_digest, "size": len(manifest_raw),
                          "annotations": {"org.opencontainers.image.ref.name": REF_NAME}}]}
    with open(os.path.join(args.out, "index.json"), "wb") as f:
        f.write(canonical(idx))
    print(json.dumps({"manifest_digest": manifest_digest, "config_digest": config_digest, "layer_digest": layer_digest,
                      "entrypoint_sha256": file_digest(args.entrypoint), "base_index_digest": img["index_digest"],
                      "base_manifest_digest": img["manifest_digest"], "layout": args.out}, indent=1, sort_keys=True))


def cmd_measure(args):
    """Digest the identities the launcher pins, straight from the layout's layers."""
    with open(os.path.join(args.layout, "index.json"), "rb") as f:
        index = json.load(f)
    if len(index["manifests"]) < 1:
        die("empty layout index")
    md = index["manifests"][0]["digest"]
    with open(blob_path(args.layout, md), "rb") as f:
        manifest_raw = f.read()
    if digest_of(manifest_raw) != md:
        die("manifest blob does not hash to its digest")
    manifest = json.loads(manifest_raw)
    paths = {"usr/local/bin/node": "node", "usr/bin/bash": "bash", ENTRYPOINT.lstrip("/"): "entrypoint"}
    found = {}
    for layer in manifest["layers"]:
        with tarfile.open(blob_path(args.layout, layer["digest"]), "r:*") as tf:
            for m in tf:
                name = m.name[2:] if m.name.startswith("./") else m.name
                if name in paths and m.isfile():
                    found[paths[name]] = digest_of(tf.extractfile(m).read())
    print(json.dumps({"image_manifest_digest": md, "config_digest": manifest["config"]["digest"],
                      "node_sha256": found.get("node"), "bash_sha256": found.get("bash"),
                      "entrypoint_sha256": found.get("entrypoint")}, indent=1, sort_keys=True))


def same_fs(a, b):
    return os.stat(a).st_dev == os.stat(os.path.dirname(os.path.abspath(b))).st_dev


def datetime_utc(epoch):
    import datetime
    return datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    ms = sub.add_parser("measure")
    ms.add_argument("--layout", required=True)
    r = sub.add_parser("resolve")
    r.add_argument("repository")
    r.add_argument("tag")
    for name in ("pull", "build"):
        s = sub.add_parser(name)
        s.add_argument("--lock", required=True)
        s.add_argument("--cache", required=True)
        if name == "build":
            s.add_argument("--entrypoint", required=True)
            s.add_argument("--adapter", required=True, help="directory holding package.json, package-lock.json, src/")
            s.add_argument("--epoch", required=True, type=int, help="SOURCE_DATE_EPOCH, e.g. the frozen commit time")
            s.add_argument("--out", required=True, help="new OCI layout directory")
    args = p.parse_args()
    {"resolve": cmd_resolve, "pull": cmd_pull, "build": cmd_build, "measure": cmd_measure}[args.cmd](args)


if __name__ == "__main__":
    main()
