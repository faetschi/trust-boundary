#!/usr/bin/env python3
import hashlib
import json
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HOST = "127.0.0.1"
PORT = 18443
PATH = "/api/v1/chat/completions"
MODEL = "deepseek/deepseek-v4.1-flash"
USAGE = {"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002}
MAX_BODY = 4 << 20

if len(sys.argv) not in (4, 5):
    sys.exit("usage: stub-upstream.py <log file> <pid file> <hold seconds> [port]")
LOG, PIDFILE, HOLD = sys.argv[1], sys.argv[2], float(sys.argv[3])
BIND_PORT = int(sys.argv[4]) if len(sys.argv) == 5 else PORT
LOCK = threading.Lock()


def record(entry):
    with LOCK, open(LOG, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(entry, sort_keys=True) + "\n")


def chunk(delta, finish=None, extra=None):
    value = {"id": "stub-1", "model": MODEL, "created": 1, "object": "chat.completion.chunk",
             "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}
    value.update(extra or {})
    return "data: " + json.dumps(value) + "\n\n"


def stream():
    return (chunk({"role": "assistant", "content": "ok"}) + chunk({}, "stop")
            + chunk({}, None, {"choices": [], "usage": USAGE}) + "data: [DONE]\n\n").encode()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def refuse(self, code):
        self.send_response(code)
        self.send_header("Content-Length", "0")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        if self.path != PATH or not 0 < length <= MAX_BODY:
            record({"method": "POST", "path": self.path, "refused": True})
            return self.refuse(404 if self.path != PATH else 411)
        raw = self.rfile.read(length)
        try:
            body = json.loads(raw)
        except ValueError:
            body = {}
        provider = body.get("provider") if isinstance(body.get("provider"), dict) else {}
        record({"method": "POST", "path": self.path, "auth_sha256": hashlib.sha256(self.headers.get("Authorization", "").encode()).hexdigest(),
                "body_sha256": hashlib.sha256(raw).hexdigest(), "model": body.get("model"), "stream": body.get("stream"),
                "max_completion_tokens": body.get("max_completion_tokens"), "provider_order": provider.get("order"),
                "keys": sorted(body), "at": time.time()})
        time.sleep(HOLD)
        payload = stream()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(payload)
        self.wfile.flush()
        self.close_connection = True

    def do_GET(self):
        record({"method": "GET", "path": self.path, "refused": True})
        self.refuse(405)


class Server(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


server = Server((HOST, BIND_PORT), Handler)
with open(PIDFILE, "w", encoding="utf-8") as handle:
    handle.write("%d\n" % os.getpid())
server.serve_forever()
