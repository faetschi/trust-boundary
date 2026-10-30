#!/usr/bin/env python3
"""Sign the governed cell image with cosign and export the offline layout.

Runs on the owner machine only; the signing key never goes to the guest.

Two cosign v3.1.3 facts shape this tool (both measured, see
PROVISIONING-SIGNING.md B2):

  * `cosign sign` cannot sign an OCI layout (it needs a registry endpoint) and
    always writes a Sigstore bundle. `verify --local-image` prints an EMPTY
    docker-manifest-digest for a bundle, and the launcher requires that claim.
    So the signature is written in the classic "simple signing" format
    instead: the payload is signed with one `cosign sign-blob --key` call (the
    only key operation) and attached as the `sha256-<digest>.sig` manifest.
  * `cosign save` is the producer of the on-disk layout that
    `cosign verify --local-image` reads, so the image and signature are served
    to it from a throwaway in-process registry on 127.0.0.1 (random port, in
    memory, stdlib only, no container runtime, no image to pin). Nothing leaves
    the host; no tlog, TUF or signing-service contact is made.

Verification needs `--insecure-ignore-tlog=true`: a key signature that was not
uploaded to Rekor cannot pass the transparency-log check, so the launcher
(containment_linux.go verifyOfflineSignature) passes that flag. The trust root
is the pinned cosign.pub digest, not a log.

  sign_cell_image.py sign   --layout L --cosign C --key K --out DIR
  sign_cell_image.py verify --layout DIR --cosign C --pub P [--expect sha256:...]

The key passphrase is read by cosign from COSIGN_PASSWORD, or prompted on the
terminal; this tool never reads, copies or logs it.
"""
import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
import threading
import urllib.parse
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


REPO = "tbound/pi-cell"
MT_MANIFEST = "application/vnd.oci.image.manifest.v1+json"


def die(msg):
    sys.stderr.write("sign_cell_image: " + msg + "\n")
    sys.exit(1)


def digest_of(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


class Store:
    def __init__(self, layout):
        self.layout = layout
        self.blobs = {}      # digest -> bytes (uploaded during signing)
        self.manifests = {}  # reference (tag or digest) -> (digest, media type)
        self.uploads = {}
        self.lock = threading.Lock()
        with open(os.path.join(layout, "index.json"), "rb") as f:
            index = json.load(f)
        if len(index["manifests"]) != 1:
            die("layout must hold exactly one image manifest")
        self.digest = index["manifests"][0]["digest"]
        self.manifests["latest"] = (self.digest, MT_MANIFEST)
        self.manifests[self.digest] = (self.digest, MT_MANIFEST)

    def blob(self, digest):
        with self.lock:
            if digest in self.blobs:
                return self.blobs[digest]
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            return None
        path = os.path.join(self.layout, "blobs", "sha256", digest[7:])
        if os.path.isfile(path):
            with open(path, "rb") as f:
                return f.read()
        return None


def make_handler(store):
    name_re = re.escape(REPO)

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *a):
            pass

        def reply(self, code, body=b"", headers=None, head=False):
            self.send_response(code)
            for k, v in (headers or {}).items():
                self.send_header(k, v)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if not head:
                self.wfile.write(body)

        def read_body(self):
            n = int(self.headers.get("Content-Length") or 0)
            return self.rfile.read(n) if n else b""

        def do_HEAD(self):
            self.do_GET(head=True)

        def do_GET(self, head=False):
            path = self.path.split("?")[0]
            if path in ("/v2/", "/v2"):
                return self.reply(200, b"{}", {"Content-Type": "application/json"}, head)
            m = re.fullmatch(r"/v2/%s/manifests/(.+)" % name_re, path)
            if m:
                with store.lock:
                    hit = store.manifests.get(m.group(1))
                if not hit:
                    return self.reply(404, b'{"errors":[{"code":"MANIFEST_UNKNOWN"}]}', head=head)
                data = store.blob(hit[0])
                return self.reply(200, data, {"Content-Type": hit[1], "Docker-Content-Digest": hit[0]}, head)
            m = re.fullmatch(r"/v2/%s/blobs/(sha256:[0-9a-f]{64})" % name_re, path)
            if m:
                data = store.blob(m.group(1))
                if data is None:
                    return self.reply(404, b'{"errors":[{"code":"BLOB_UNKNOWN"}]}', head=head)
                return self.reply(200, data, {"Content-Type": "application/octet-stream", "Docker-Content-Digest": m.group(1)}, head)
            if re.fullmatch(r"/v2/%s/tags/list" % name_re, path):
                with store.lock:
                    tags = sorted(k for k in store.manifests if not k.startswith("sha256:"))
                return self.reply(200, json.dumps({"name": REPO, "tags": tags}).encode(), {"Content-Type": "application/json"}, head)
            # No referrers API (like registry:2): clients fall back to tag schema.
            return self.reply(404, b"{}", head=head)

        def do_POST(self):
            body = self.read_body()
            if not re.fullmatch(r"/v2/%s/blobs/uploads/?" % name_re, self.path.split("?")[0]):
                return self.reply(404)
            uid = uuid.uuid4().hex
            with store.lock:
                store.uploads[uid] = bytearray(body)
            self.reply(202, headers={"Location": "/v2/%s/blobs/uploads/%s" % (REPO, uid), "Docker-Upload-UUID": uid, "Range": "0-0"})

        def do_PATCH(self):
            body = self.read_body()
            m = re.fullmatch(r"/v2/%s/blobs/uploads/([0-9a-f]+)" % name_re, self.path.split("?")[0])
            with store.lock:
                if not m or m.group(1) not in store.uploads:
                    return self.reply(404)
                store.uploads[m.group(1)].extend(body)
            self.reply(202, headers={"Location": self.path, "Docker-Upload-UUID": m.group(1), "Range": "0-0"})

        def do_PUT(self):
            body = self.read_body()
            path, _, query = self.path.partition("?")
            m = re.fullmatch(r"/v2/%s/blobs/uploads/([0-9a-f]+)" % name_re, path)
            if m:
                q = {k: v[0] for k, v in urllib.parse.parse_qs(query).items()}
                with store.lock:
                    data = bytes(store.uploads.pop(m.group(1), b"") ) + body
                    want = q.get("digest", "")
                    if digest_of(data) != want:
                        return self.reply(400, b'{"errors":[{"code":"DIGEST_INVALID"}]}')
                    store.blobs[want] = data
                return self.reply(201, headers={"Location": "/v2/%s/blobs/%s" % (REPO, want), "Docker-Content-Digest": want})
            m = re.fullmatch(r"/v2/%s/manifests/(.+)" % name_re, path)
            if m:
                data = body
                d = digest_of(data)
                ref = m.group(1)
                if ref.startswith("sha256:") and ref != d:
                    return self.reply(400, b'{"errors":[{"code":"DIGEST_INVALID"}]}')
                mt = self.headers.get("Content-Type", MT_MANIFEST)
                with store.lock:
                    store.blobs[d] = data
                    store.manifests[ref] = (d, mt)
                    store.manifests[d] = (d, mt)
                return self.reply(201, headers={"Location": path, "Docker-Content-Digest": d})
            self.reply(404)

    return Handler


def run(cmd, env):
    r = subprocess.run(cmd, env=env, stdin=subprocess.DEVNULL if "COSIGN_PASSWORD" in env else None)
    if r.returncode != 0:
        die("command failed (%d): %s" % (r.returncode, " ".join(cmd[:2])))


def clean_env(home):
    keep = {k: v for k, v in os.environ.items() if k in ("PATH", "COSIGN_PASSWORD", "TERM")}
    keep["HOME"] = home
    return keep


SIMPLE_SIGNING = "application/vnd.dev.cosign.simplesigning.v1+json"
SIG_ANNOTATION = "dev.cosignproject.cosign/signature"
CONFIG_MT = "application/vnd.oci.image.config.v1+json"


def attach_signature(store, signature_b64, payload):
    """Add the classic cosign signature manifest (tag sha256-<hex>.sig) to the store."""
    pd = digest_of(payload)
    config = json.dumps({"architecture": "", "created": "0001-01-01T00:00:00Z",
                         "history": [{"created": "0001-01-01T00:00:00Z"}], "os": "",
                         "rootfs": {"type": "layers", "diff_ids": [pd]}, "config": {}}, separators=(",", ":")).encode()
    manifest = json.dumps({"schemaVersion": 2, "mediaType": MT_MANIFEST,
                           "config": {"mediaType": CONFIG_MT, "size": len(config), "digest": digest_of(config)},
                           "layers": [{"mediaType": SIMPLE_SIGNING, "size": len(payload), "digest": pd,
                                       "annotations": {SIG_ANNOTATION: signature_b64}}]}, separators=(",", ":")).encode()
    md = digest_of(manifest)
    with store.lock:
        store.blobs.update({pd: payload, digest_of(config): config, md: manifest})
        store.manifests["sha256-" + store.digest[7:] + ".sig"] = (md, MT_MANIFEST)
        store.manifests[md] = (md, MT_MANIFEST)


def cmd_sign(args):
    os.umask(0o077)
    for p in (args.layout, args.cosign, args.key, args.out):
        if not os.path.isabs(p):
            die("absolute path required: " + p)
    if os.path.exists(args.out):
        die("output path exists (in-place signing is refused): " + args.out)
    store = Store(args.layout)
    payload = json.dumps({"critical": {"identity": {"docker-reference": "localhost/" + REPO},
                                       "image": {"docker-manifest-digest": store.digest},
                                       "type": "cosign container image signature"}, "optional": None},
                         separators=(",", ":")).encode()
    with tempfile.TemporaryDirectory(prefix="cosign-sign-") as tmp:
        env = clean_env(tmp)
        payload_file = os.path.join(tmp, "payload.json")
        with open(payload_file, "wb") as f:
            f.write(payload)
        bundle_file = os.path.join(tmp, "bundle.json")
        run([args.cosign, "sign-blob", "--key", args.key, "--tlog-upload=false", "--use-signing-config=false",
             "--yes", "--bundle", bundle_file, payload_file], env)
        with open(bundle_file) as f:
            signature = json.load(f)["messageSignature"]["signature"]
        attach_signature(store, signature, payload)
        server = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(store))
        threading.Thread(target=server.serve_forever, daemon=True).start()
        ref = "127.0.0.1:%d/%s@%s" % (server.server_address[1], REPO, store.digest)
        try:
            run([args.cosign, "save", "--dir", args.out, "--allow-http-registry=true",
                 "--allow-insecure-registry=true", ref], env)
        finally:
            server.shutdown()
    print(json.dumps({"signed_manifest_digest": store.digest, "layout": args.out}, indent=1))


def cmd_verify(args):
    """Same command and payload rule as the launcher's verifyOfflineSignature."""
    r = subprocess.run([args.cosign, "verify", "--offline", "--insecure-ignore-tlog=true", "--key", args.pub,
                        "--local-image", args.layout],
                       capture_output=True, env={"PATH": os.environ.get("PATH", ""), "HOME": tempfile.gettempdir()})
    if r.returncode != 0:
        die("cosign verify exited %d: %s" % (r.returncode, r.stderr.decode(errors="replace").strip()[-300:]))
    entries = json.loads(r.stdout)
    digests = [e["critical"]["image"]["docker-manifest-digest"] for e in entries
               if re.fullmatch(r"sha256:[0-9a-f]{64}", e["critical"]["image"]["docker-manifest-digest"].lower())]
    if not digests:
        die("verification payload has no trusted image digest claim")
    if args.expect and digests[0].lower() != args.expect.lower():
        die("signature binds %s, not %s" % (digests[0], args.expect))
    print(digests[0])


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("sign")
    s.add_argument("--layout", required=True)
    s.add_argument("--cosign", required=True)
    s.add_argument("--key", required=True)
    s.add_argument("--out", required=True)
    v = sub.add_parser("verify")
    v.add_argument("--layout", required=True)
    v.add_argument("--cosign", required=True)
    v.add_argument("--pub", required=True)
    v.add_argument("--expect")
    args = p.parse_args()
    {"sign": cmd_sign, "verify": cmd_verify}[args.cmd](args)


if __name__ == "__main__":
    main()
