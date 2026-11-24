#!/usr/bin/env python3
import hashlib
import json
import re
import sys

PATH = "/api/v1/chat/completions"
MODEL = "deepseek/deepseek-v4.1-flash"
STUB_USAGE = (11, 3, 2)
BINDS = {"/work": True, "/runtime": False, "/run/tbound/fwd": False}
SECRET_NAME = re.compile(r"KEY|TOKEN|SECRET|PASSWORD", re.I)


def fail(message):
    sys.exit("vs-check: " + message)


def load(path):
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def key_arg(value):
    return sys.stdin.read().strip() if value == "-" else value


def check_inspect(path, key):
    key = key_arg(key)
    with open(path, encoding="utf-8") as handle:
        text = handle.read()
    if len(key) < 16:
        fail("the stub key is too short to search for")
    if key in text:
        fail("the stub key appears in the container record")
    data = json.loads(text)
    if not isinstance(data, list) or len(data) != 1:
        fail("podman inspect did not give exactly one container")
    container = data[0]
    names = [item.split("=", 1)[0] for item in (container.get("Config") or {}).get("Env") or []]
    secret = [name for name in names if SECRET_NAME.search(name)]
    if secret:
        fail("the container environment has secret-looking names: " + ", ".join(secret))
    binds = {m.get("Destination"): m for m in container.get("Mounts") or [] if m.get("Type") == "bind"}
    if set(binds) != set(BINDS):
        fail("bind mounts are %s, want %s" % (sorted(binds), sorted(BINDS)))
    for destination, writable in BINDS.items():
        if bool(binds[destination].get("RW")) != writable:
            fail("mount %s has RW=%r, want %r" % (destination, binds[destination].get("RW"), writable))
    for destination, mount in binds.items():
        source = mount.get("Source", "")
        if source.startswith("/etc/tbound") or source.endswith(".key"):
            fail("mount %s has the source %s, which can hold a key" % (destination, source))
    host = container.get("HostConfig") or {}
    print("container record: 3 bind mounts (%s), %d environment names, no secret-looking name, stub key absent; ReadonlyRootfs=%r CapDrop=%r Privileged=%r" %
          (", ".join("%s %s" % (d, "rw" if w else "ro") for d, w in BINDS.items()), len(names), host.get("ReadonlyRootfs"), host.get("CapDrop"), host.get("Privileged")))


def check_stublog(path, key, count, profile_path):
    key = key_arg(key)
    profile = load(profile_path)
    want_auth = hashlib.sha256(("Bearer " + key).encode()).hexdigest()
    with open(path, encoding="utf-8") as handle:
        entries = [json.loads(line) for line in handle if line.strip()]
    if len(entries) != int(count) or not entries:
        fail("the stub saw %d request(s), the V document booked %s" % (len(entries), count))
    for entry in entries:
        if entry.get("refused") or entry.get("method") != "POST" or entry.get("path") != PATH:
            fail("the stub saw a request that is not the research POST: %r" % entry)
        if entry.get("auth_sha256") != want_auth:
            fail("a request reached the stub with a credential other than the stub key")
        if entry.get("model") != profile["model"] or entry.get("max_completion_tokens") != profile["max_output_tokens"] or entry.get("stream") is not True:
            fail("a forwarded request is not the profile's shape: %r" % {k: entry.get(k) for k in ("model", "max_completion_tokens", "stream")})
        if entry.get("provider_order") != profile["routing"]["order"]:
            fail("a forwarded request does not carry the profile's provider order: %r" % entry.get("provider_order"))
    print("stub log: %d request(s), each the research POST with the stub key, the profile's model, max_completion_tokens and provider order" % len(entries))


def check_doc(path, case):
    doc = load(path)
    n = doc.get("ledger_records")
    problems = []
    if doc.get("schema") != "tbound-v-run/v1" or doc.get("case_id") != case:
        problems.append("schema or case id")
    if doc.get("exit") != 0 or doc.get("timed_out") is not False:
        problems.append("the container did not exit 0 in time (exit %r, timed_out %r)" % (doc.get("exit"), doc.get("timed_out")))
    if doc.get("accept_error") or doc.get("accept") is None:
        problems.append("the workspace was not evaluated: %r" % doc.get("accept_error"))
    if not isinstance(n, int) or n < 1 or doc.get("usage_events") != n or doc.get("uncertain_units") != 0 or doc.get("overage") is not False:
        problems.append("ledger_records %r, usage_events %r, uncertain_units %r, overage %r" % (n, doc.get("usage_events"), doc.get("uncertain_units"), doc.get("overage")))
    elif (doc.get("tokens_in"), doc.get("tokens_out"), doc.get("cost_micro_usd")) != tuple(v * n for v in STUB_USAGE):
        problems.append("usage %r is not %d x the stub's %r" % ((doc.get("tokens_in"), doc.get("tokens_out"), doc.get("cost_micro_usd")), n, STUB_USAGE))
    if problems:
        fail("the V document is not a clean stub unit: " + "; ".join(problems))
    print("V document: %d ledger record(s), all settled, tokens %d/%d, cost %d micro-USD, accept passed=%r" %
          (n, doc["tokens_in"], doc["tokens_out"], doc["cost_micro_usd"], (doc["accept"] or {}).get("passed")))
    print(n)


def check_ledger(path, head):
    with open(path, "rb") as handle:
        lines = handle.read().split(b"\n")
    if lines[-1] == b"":
        lines.pop()
    if len(lines) < 2 or hashlib.sha256(lines[0]).hexdigest() != head:
        fail("the throwaway ledger does not extend its genesis record")
    print("throwaway ledger: %d records, record 1 is the genesis head %s" % (len(lines), head))


def record(args):
    if len(args) % 2:
        fail("record takes key value pairs")
    print(json.dumps(dict(zip(args[::2], args[1::2])), indent=2, sort_keys=True))


def main():
    command, args = (sys.argv[1], sys.argv[2:]) if len(sys.argv) > 1 else ("", [])
    table = {"inspect": (check_inspect, 2), "stublog": (check_stublog, 4), "doc": (check_doc, 2), "ledger": (check_ledger, 2)}
    if command == "record":
        return record(args)
    if command not in table or len(args) != table[command][1]:
        fail("usage: vs-check.py inspect FILE KEY | stublog FILE KEY COUNT PROFILE | doc FILE CASE | ledger FILE GENESIS_HEAD | record KEY VALUE...")
    table[command][0](*args)


main()
