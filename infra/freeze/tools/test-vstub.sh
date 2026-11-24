#!/bin/bash
set -euo pipefail
umask 077
case $(uname -s) in MINGW* | MSYS*) echo "FATAL: run test-vstub.sh inside WSL or on Linux, not from Git Bash" >&2; exit 2 ;; esac
K=$(cd -P "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
ROOT=$(cd -P "$K/../.." && pwd)
case $ROOT in /mnt | /mnt/*) echo "FATAL: the checkout is under /mnt" >&2; exit 2 ;; esac
STUB=$ROOT/infra/guest/v-stub/stub-upstream.py
CHECK=$K/guest/vs-check.py
PROFILE=$ROOT/infra/guest/research/research-profile.json
T=$(mktemp -d "${TMPDIR:-/tmp}/vstub-test.XXXXXX")
case $T in /tmp/vstub-test.* | "${TMPDIR:-/tmp}"/vstub-test.*) ;; *) echo "FATAL: unexpected temp dir '$T'" >&2; exit 2 ;; esac
STUB_PID=""
cleanup() { [ -z "$STUB_PID" ] || kill "$STUB_PID" 2>/dev/null || true; rm -rf -- "$T"; }
trap cleanup EXIT
FAILS=0
pass() { printf 'PASS %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1"; FAILS=$((FAILS + 1)); }
expect_ok() { local l=$1; shift; if out=$("$@" 2>&1); then pass "$l"; else fail "$l: $out"; fi; }
expect_refuse() {
  local l=$1 want=$2; shift 2
  if out=$("$@" 2>&1); then fail "$l: accepted"; elif [[ $out == *"$want"* ]]; then pass "$l"; else fail "$l: refused for another reason: $out"; fi
}
py() { python3 -I "$@"; }

KEY=sk-vstub-0123456789abcdef0123456789abcdef0123456789abcdef
PORT=$(py -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
py "$STUB" "$T/stub.log" "$T/stub.pid" 0 "$PORT" &
STUB_PID=$!
for _ in $(seq 1 50); do py -c 'import socket,sys; socket.create_connection(("127.0.0.1", int(sys.argv[1])), 1)' "$PORT" 2>/dev/null && break; sleep 0.1; done

cat >"$T/post.py" <<'EOF'
import http.client
import json
import sys

port, path, method, token = int(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
body = json.dumps({"model": "deepseek/deepseek-v4.1-flash", "stream": True, "max_completion_tokens": 4096,
                   "provider": {"order": ["deepinfra/fp8"]}, "messages": [{"role": "user", "content": "hi"}]})
conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
conn.request(method, path, body if method == "POST" else None, {"Authorization": "Bearer " + token, "Content-Type": "application/json"})
response = conn.getresponse()
data = response.read().decode()
print(json.dumps({"status": response.status, "type": response.getheader("Content-Type"), "body": data}))
EOF

reply=$(py "$T/post.py" "$PORT" /api/v1/chat/completions POST "$KEY")
cat >"$T/sse.py" <<'EOF'
import json
import sys

reply = json.loads(sys.stdin.read())
assert reply["status"] == 200 and reply["type"] == "text/event-stream", reply
events = [line[6:] for line in reply["body"].split("\n") if line.startswith("data: ")]
assert events[-1] == "[DONE]", events
usage = [json.loads(e)["usage"] for e in events[:-1] if '"usage"' in e]
assert usage == [{"prompt_tokens": 11, "completion_tokens": 3, "cost": 0.000002}], usage
assert all(json.loads(e)["model"] == "deepseek/deepseek-v4.1-flash" for e in events[:-1])
print(json.dumps(usage[0]))
EOF
expect_ok "stub answers the research POST with an SSE stream that ends in one usage chunk and [DONE]" bash -c 'printf "%s" "$1" | python3 -I "$2"' _ "$reply" "$T/sse.py"
expect_ok "stub refuses another path with 404" bash -c '[[ "$(python3 -I "$1" "$2" /v1/other POST "$3")" == *"\"status\": 404"* ]]' _ "$T/post.py" "$PORT" "$KEY"
expect_ok "stub refuses GET with 405" bash -c '[[ "$(python3 -I "$1" "$2" /api/v1/chat/completions GET "$3")" == *"\"status\": 405"* ]]' _ "$T/post.py" "$PORT" "$KEY"
cat >"$T/log.py" <<'EOF'
import hashlib
import json
import sys

entries = [json.loads(line) for line in open(sys.argv[1], encoding="utf-8") if line.strip()]
assert len(entries) == 3, entries
good = entries[0]
assert good["auth_sha256"] == hashlib.sha256(("Bearer " + sys.argv[2]).encode()).hexdigest()
assert good["model"] == "deepseek/deepseek-v4.1-flash" and good["max_completion_tokens"] == 4096 and good["stream"] is True
assert good["provider_order"] == ["deepinfra/fp8"] and good["keys"] == ["max_completion_tokens", "messages", "model", "provider", "stream"]
assert entries[1].get("refused") and entries[2].get("refused")
EOF
expect_ok "stub log holds the auth hash, model, cap, provider order and the two refusals" py "$T/log.py" "$T/stub.log" "$KEY"
head -n 1 "$T/stub.log" >"$T/stub-one.log"
expect_ok "stublog accepts the stub's own record" py "$CHECK" stublog "$T/stub-one.log" "$KEY" 1 "$PROFILE"
expect_refuse "stublog refuses a count that differs from the V document" "the stub saw 1 request(s)" py "$CHECK" stublog "$T/stub-one.log" "$KEY" 2 "$PROFILE"
expect_refuse "stublog refuses another credential" "credential other than the stub key" py "$CHECK" stublog "$T/stub-one.log" "${KEY}x" 1 "$PROFILE"
sed 's/"max_output_tokens": 4096/"max_output_tokens": 16384/' "$PROFILE" >"$T/profile-other.json"
expect_refuse "stublog refuses a cap that is not the profile's" "not the profile's shape" py "$CHECK" stublog "$T/stub-one.log" "$KEY" 1 "$T/profile-other.json"

cat >"$T/mkinspect.py" <<'EOF'
import json
import sys

variant = sys.argv[1]
mounts = [{"Type": "bind", "Source": "/var/lib/tbound/v-work/ab/ws", "Destination": "/work", "RW": True},
          {"Type": "bind", "Source": "/var/lib/tbound/runtime", "Destination": "/runtime", "RW": False},
          {"Type": "bind", "Source": "/run/tbound-pi/v-fwd/v-1", "Destination": "/run/tbound/fwd", "RW": False},
          {"Type": "tmpfs", "Source": "", "Destination": "/tmp", "RW": True}]
env = ["HOME=/tmp/home", "TMPDIR=/tmp/tbound-tmp", "LANG=C.UTF-8", "PI_OFFLINE=1"]
containers = [{"Name": "tbound-v-ab", "Config": {"Env": env, "Cmd": ["-c", "launch"]}, "Mounts": mounts,
               "HostConfig": {"ReadonlyRootfs": True, "CapDrop": ["ALL"], "Privileged": False}}]
if variant == "key":
    containers[0]["Config"]["Cmd"] = ["-c", "echo " + sys.argv[2]]
elif variant == "extra-mount":
    mounts.append({"Type": "bind", "Source": "/etc/tbound/v-stub", "Destination": "/etc/k", "RW": False})
elif variant == "rw-runtime":
    mounts[1]["RW"] = True
elif variant == "key-source":
    mounts[2]["Source"] = "/run/x/stub.key"
elif variant == "env":
    env.append("TBOUND_V_KEY=1")
elif variant == "two":
    containers.append(containers[0])
print(json.dumps(containers))
EOF
for v in ok key extra-mount rw-runtime key-source env two; do py "$T/mkinspect.py" "$v" "$KEY" >"$T/inspect-$v.json"; done
expect_ok "inspect accepts three bind mounts, two read-only, no key, no secret-looking name" py "$CHECK" inspect "$T/inspect-ok.json" "$KEY"
expect_refuse "inspect refuses the stub key anywhere in the record" "stub key appears" py "$CHECK" inspect "$T/inspect-key.json" "$KEY"
expect_refuse "inspect refuses a fourth bind mount" "bind mounts are" py "$CHECK" inspect "$T/inspect-extra-mount.json" "$KEY"
expect_refuse "inspect refuses a writable runtime mount" "RW=True" py "$CHECK" inspect "$T/inspect-rw-runtime.json" "$KEY"
expect_refuse "inspect refuses a key file as a mount source" "can hold a key" py "$CHECK" inspect "$T/inspect-key-source.json" "$KEY"
expect_refuse "inspect refuses a secret-looking environment name" "secret-looking names" py "$CHECK" inspect "$T/inspect-env.json" "$KEY"
expect_refuse "inspect refuses two containers" "exactly one container" py "$CHECK" inspect "$T/inspect-two.json" "$KEY"

cat >"$T/mkdoc.py" <<'EOF'
import json
import sys

variant = sys.argv[1]
n = 2
doc = {"schema": "tbound-v-run/v1", "case_id": "PILOT-CMP-read-01", "image_digest": "sha256:" + "a" * 64, "fixture_digest": "sha256:" + "b" * 64,
       "exit": 0, "timed_out": False, "tokens_in": 11 * n, "tokens_out": 3 * n, "cost_micro_usd": 2 * n, "tool_calls": 0, "usage_events": n,
       "ledger_records": n, "uncertain_units": 0, "overage": False, "accept": {"passed": False}, "started_ns": 1, "finished_ns": 2}
if variant == "uncertain":
    doc["uncertain_units"] = 1
elif variant == "cost":
    doc["cost_micro_usd"] = 5
elif variant == "exit":
    doc["exit"] = 1
elif variant == "accept":
    doc["accept"] = None
    doc["accept_error"] = "tree"
elif variant == "case":
    doc["case_id"] = "other"
elif variant == "none":
    doc["ledger_records"] = 0
print(json.dumps(doc))
EOF
for v in ok uncertain cost exit accept case none; do py "$T/mkdoc.py" "$v" >"$T/doc-$v.json"; done
expect_ok "doc accepts a clean stub unit and prints the record count last" bash -c '[ "$(python3 -I "$1" doc "$2" PILOT-CMP-read-01 | tail -n 1)" = 2 ]' _ "$CHECK" "$T/doc-ok.json"
for v in uncertain cost exit accept case none; do
  expect_refuse "doc refuses variant $v" "not a clean stub unit" py "$CHECK" doc "$T/doc-$v.json" PILOT-CMP-read-01
done

printf 'genesis-record' >"$T/ledger-a"
printf 'genesis-record\nsecond\nthird\n' >"$T/ledger-b"
HEAD=$(printf 'genesis-record' | sha256sum | cut -c1-64)
expect_ok "ledger accepts records after the genesis head" py "$CHECK" ledger "$T/ledger-b" "$HEAD"
expect_refuse "ledger refuses a ledger that only holds the genesis record" "does not extend its genesis" py "$CHECK" ledger "$T/ledger-a" "$HEAD"
expect_refuse "ledger refuses another genesis head" "does not extend its genesis" py "$CHECK" ledger "$T/ledger-b" "$(printf 'x' | sha256sum | cut -c1-64)"
expect_ok "record prints a JSON object" bash -c 'python3 -I "$1" record commit abc case c | python3 -I -c "import json,sys; d=json.load(sys.stdin); assert d == {\"commit\": \"abc\", \"case\": \"c\"}"' _ "$CHECK"
expect_refuse "record refuses an odd number of arguments" "key value pairs" py "$CHECK" record commit

for f in "$K"/guest/34-v-stub-probe.sh "$K"/guest/sign-run.sh "$K"/guest/40-guest-evidence.sh "$K"/guest/glib.sh "$K"/tools/guest-chain.sh; do expect_ok "bash -n ${f#"$K"/}" bash -n "$f"; done
expect_ok "sign-run.sh takes the vs target" grep -q 'case "$NAME" in rp|dr|pilot|rr|vs)' "$K/guest/sign-run.sh"
expect_ok "glib.sh WGA_FILES lists vs-check.py" grep -q 'guest/vs-check.py' "$K/guest/glib.sh"
expect_ok "the v-stub service config template differs from the pilot one in provider_route only" bash -c 'diff <(tr -d "\r" < "$1") <(tr -d "\r" < "$2") | grep "^[<>]" | tr "\n" "|" | grep -qx "<   \"provider_route\": \"engineering\",|>   \"provider_route\": \"v-stub\",|"' _ "$ROOT/infra/guest/serve-pi-service.corpus-pilot.json" "$ROOT/infra/guest/serve-pi-service.v-stub.json"
expect_ok "the frozen research profile bytes of RA-2 come from the committed profile" bash -c '[ "$(tr -d "\r" < "$1" | sed "s|\"frozen_at\": null|\"frozen_at\": \"2026-11-21T13:35:13Z\"|" | sha256sum | cut -c1-64)" = 3d0eaf6411975434daffb265c7911dfc8c11745f512b63959db64d8059a2c357 ]' _ "$PROFILE"

if [ "$FAILS" -ne 0 ]; then printf '%d FAIL\n' "$FAILS"; exit 1; fi
echo "all passed"
