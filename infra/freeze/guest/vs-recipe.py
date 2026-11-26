#!/usr/bin/env python3
import http.client
import json
import os
import socket
import subprocess
import sys
import threading
import time

CLIENT = ["setpriv", "--reuid", "994", "--regid", "985", "--clear-groups", "--bounding-set=-all", "--inh-caps=-all",
          "--ambient-caps=-all", "--no-new-privs", "--", "/usr/local/bin/tbound", "serve", "--pi"]
ENV = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C"}
MODEL = "deepseek/deepseek-v4.1-flash"
BODY = json.dumps({"model": MODEL, "stream": True, "stream_options": {"include_usage": True}, "messages": [{"role": "user", "content": "hi"}]})
WRONG_MODEL = json.dumps({"model": "openai/gpt-x", "stream": True, "messages": [{"role": "user", "content": "hi"}]})
SERVICE, SOCKET_UNIT = "tbound-pi.service", "tbound-pi.socket"


def emit(**fields):
    print(json.dumps(fields, sort_keys=True), flush=True)


def fail(message):
    emit(step="FAIL", message=message)
    sys.exit(1)


class UnixConnection(http.client.HTTPConnection):
    def __init__(self, path, timeout):
        super().__init__("forwarder", timeout=timeout)
        self.unix_path = path

    def connect(self):
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(self.timeout)
        sock.connect(self.unix_path)
        self.sock = sock


def lines_of(path):
    with open(path, "rb") as handle:
        return [line for line in handle.read().split(b"\n") if line.strip()]


def ledger_kinds():
    return [json.loads(line)["kind"] for line in lines_of(LEDGER)]


def wait_for(predicate, seconds, what):
    end = time.time() + seconds
    while time.time() < end:
        if predicate():
            return
        time.sleep(0.5)
    fail("timed out waiting for " + what)


def systemctl(*args):
    return subprocess.run(["systemctl", *args], capture_output=True, text=True, timeout=120)


def active():
    return systemctl("is-active", SERVICE).stdout.strip() == "active"


class Unit:
    def __init__(self, case, request_id):
        self.case = case
        self.proc = subprocess.Popen(CLIENT + ["--task-id", case, "--attempt", "1", "--role", "v", "--task-request-id", request_id, "--bash-no-outputs"],
                                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, cwd="/", env=ENV)
        line = self.proc.stdout.readline()
        if not line:
            fail("the V client for %s printed no ready frame: %s" % (case, self.proc.stderr.read()[:600].decode(errors="replace")))
        ready = json.loads(line)
        if ready.get("status") != "ready":
            fail("the V unit for %s was not opened: %r" % (case, ready))
        self.dir = ready["receipt"]["dir"]
        with open(os.path.join(self.dir, "token"), encoding="utf-8") as handle:
            self.token = handle.read().strip()
        emit(step="open", case=case)

    def post(self, mode, body=BODY, timeout=60):
        with open(MODE_FILE, "w", encoding="utf-8") as handle:
            handle.write(mode + "\n")
        os.chmod(MODE_FILE, 0o644)
        before = len(lines_of(STUB_LOG))
        conn = UnixConnection(os.path.join(self.dir, "fwd.sock"), timeout)
        try:
            conn.request("POST", "/v1/chat/completions", body, {"Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
            response = conn.getresponse()
            response.read()
            status = response.status
        except OSError as err:
            status = "error: %s" % err
        finally:
            conn.close()
        return status, len(lines_of(STUB_LOG)) - before

    def finish(self):
        try:
            self.proc.stdin.close()
        except OSError:
            pass
        out, err = self.proc.communicate(timeout=180)
        frames = [json.loads(line) for line in out.decode().splitlines() if line.strip().startswith("{")]
        return frames[-1] if len(frames) > 1 else {"status": "no final frame", "exit": self.proc.returncode, "stderr": err.decode(errors="replace")[:400]}


def expect(case_step, got, want_status, want_reached):
    status, reached = got
    emit(step=case_step, status=status, reached_stub=reached)
    if status != want_status or reached != want_reached:
        fail("%s: status %r reaching the stub %d time(s), want %r and %d" % (case_step, status, reached, want_status, want_reached))


def restart_after_kill():
    systemctl("reset-failed", SERVICE)
    started = systemctl("start", SOCKET_UNIT)
    started2 = systemctl("start", SERVICE)
    if started.returncode != 0 or started2.returncode != 0:
        fail("the service did not start after the kill: %s %s" % (started.stderr[:300], started2.stderr[:300]))
    wait_for(active, 60, "the service to be active after the kill")
    emit(step="restart", active=True)


def stoprule(case2, case3, prefix):
    unit = Unit(case2, prefix + "-2")
    expect("quick settles", unit.post("quick"), 200, 1)
    expect("wrong model is refused before booking", unit.post("quick", WRONG_MODEL), 400, 0)
    expect("upstream 500 ends uncertain", unit.post("fail500"), 500, 1)
    expect("no usage ends uncertain", unit.post("nousage"), 200, 1)
    reserves = ledger_kinds().count("reserve")
    held = {}
    thread = threading.Thread(target=lambda: held.update(result=unit.post("hold", timeout=90)))
    thread.start()
    wait_for(lambda: ledger_kinds().count("reserve") == reserves + 1, 30, "the hold request's reservation")
    killed = systemctl("kill", "-s", "KILL", SERVICE)
    if killed.returncode != 0:
        fail("systemctl kill failed: " + killed.stderr[:300])
    wait_for(lambda: not active(), 60, "the service to stop after SIGKILL")
    thread.join(120)
    emit(step="crash during a held call", status=str(held.get("result", ("pending", 0))[0]), service_killed=True)
    unit.finish()
    restart_after_kill()
    after = Unit(case3, prefix + "-3")
    for i in range(3):
        expect("upstream 500 ends uncertain (after restart, %d)" % (i + 1), after.post("fail500"), 500, 1)
    expect("the stop rule refuses before sending", after.post("quick"), 503, 0)
    final = after.finish()
    emit(step="unit totals after the stop rule", totals=final.get("receipt"), status=final.get("status"))


def overage(case4, prefix):
    unit = Unit(case4, prefix + "-4")
    expect("a cost above the reservation settles as an overage", unit.post("over"), 200, 1)
    expect("after an overage the ledger refuses before sending", unit.post("quick"), 503, 0)
    final = unit.finish()
    receipt = final.get("receipt") or {}
    emit(step="unit totals after the overage", totals=receipt, status=final.get("status"))
    if receipt.get("overage") is not True:
        fail("the unit's totals do not report the overage: %r" % final)


phase, MODE_FILE, STUB_LOG, LEDGER, prefix = sys.argv[1:6]
if phase == "stoprule" and len(sys.argv) == 8:
    stoprule(sys.argv[6], sys.argv[7], prefix)
elif phase == "overage" and len(sys.argv) == 7:
    overage(sys.argv[6], prefix)
else:
    fail("usage: vs-recipe.py stoprule MODE_FILE STUB_LOG LEDGER PREFIX CASE2 CASE3 | overage MODE_FILE STUB_LOG LEDGER PREFIX CASE4")
emit(step="DONE", phase=phase)
