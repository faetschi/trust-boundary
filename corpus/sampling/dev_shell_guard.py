"""Structural guard for the F36 dev-set tools: benchmark text must never reach a shell.

    py -3 -I corpus/sampling/dev_shell_guard.py [--out FILE]

Fails (exit 1) when
  - a non-Python script (.sh .bash .ps1 .cmd .bat, or a file that starts with #!) exists among corpus/sampling/dev_*,
    the carriers corpus/sampling/carriers/devset-*, or evidence/F36/devset;
  - a dev Python tool (dev_src*.py, dev_carrier.py, dev_overlap.py, this file) uses shell=True, os.system, os.popen,
    os.exec*, os.spawn*, eval or exec, imports names out of subprocess, or calls subprocess with an argument that is not a
    list literal of string constants, names, attributes and f-strings of names;
  - a subprocess list starts with git and its subcommand is not one of GIT_READ, or any other program is started. EXEC_ALLOW
    is empty since the 2026-11-27 host-execution ruling: downloaded or benchmark code runs only in the guest's cell, so no dev
    tool may start node, bash or any other interpreter (dev_src_exercism.py run_node once ran `["node", script_file]`).
The single indirect call `git(clone, *args)` of dev_src.py is allowed by name; every call of that helper elsewhere must give a
constant subcommand out of GIT_READ. Stdlib only. The dev_overlap precheck runs this guard first and stops if it fails.
"""
import argparse
import ast
import glob
import hashlib
import json
import os
import subprocess
import sys

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
GIT_READ = {"rev-parse", "ls-tree", "cat-file", "tag"}
SCRIPT_EXT = (".sh", ".bash", ".ps1", ".cmd", ".bat")
SUBPROCESS_CALLS = {"run", "call", "check_call", "check_output", "Popen", "getoutput", "getstatusoutput"}
FORBIDDEN_OS = {"system", "popen", "execv", "execve", "execl", "execle", "execlp", "execvp", "execvpe", "execlpe",
                "spawnl", "spawnle", "spawnlp", "spawnlpe", "spawnv", "spawnve", "spawnvp", "spawnvpe", "startfile"}
EXEC_ALLOW = {}  # (file, function): program. Empty since the 2026-11-27 host-execution ruling: only git reads start.
INDIRECT_GIT = ("dev_src.py", "git")  # (file, function): `["git", "-C", clone, *args]`, its callers are checked instead


def tool_files():
    names = ["dev_carrier.py", "dev_overlap.py", "dev_shell_guard.py"]
    out = {os.path.join(HERE, n) for n in names if os.path.exists(os.path.join(HERE, n))}
    out |= set(glob.glob(os.path.join(HERE, "dev_src*.py")))
    return sorted(out)


def script_hits():
    roots = glob.glob(os.path.join(HERE, "dev_*")) + glob.glob(os.path.join(HERE, "carriers", "devset-*")) + [os.path.join(REPO, "evidence", "F36", "devset")]
    hits = []
    for r in roots:
        paths = [r] if os.path.isfile(r) else [os.path.join(d, f) for d, _, fs in os.walk(r) for f in fs]
        for p in paths:
            rel = os.path.relpath(p, REPO).replace("\\", "/")
            if p.lower().endswith(SCRIPT_EXT):
                hits.append(f"{rel}: non-Python script")
            elif not p.endswith(".py"):
                try:
                    with open(p, "rb") as f:
                        if f.read(2) == b"#!":
                            hits.append(f"{rel}: starts with #!")
                except OSError as e:
                    hits.append(f"{rel}: unreadable ({e})")
    return hits


def _plain(node):
    """A list element a subprocess argument may hold: a string constant, a name or attribute, *name, or an f-string of those."""
    if isinstance(node, ast.Constant):
        return isinstance(node.value, str)
    if isinstance(node, (ast.Name, ast.Attribute)):
        return True
    if isinstance(node, ast.Starred):
        return isinstance(node.value, ast.Name)
    if isinstance(node, ast.JoinedStr):
        return all(isinstance(v, ast.Constant) or (isinstance(v, ast.FormattedValue) and isinstance(v.value, (ast.Name, ast.Attribute))) for v in node.values)
    return False


def _const(node):
    return node.value if isinstance(node, ast.Constant) and isinstance(node.value, str) else None


class Visitor(ast.NodeVisitor):
    def __init__(self, rel):
        self.rel, self.fn, self.bad, self.stack = rel, None, [], []

    def fail(self, node, why):
        self.bad.append(f"{self.rel}:{node.lineno}: {why}")

    def visit_FunctionDef(self, node):
        self.stack.append(node.name)
        self.generic_visit(node)
        self.stack.pop()

    def visit_ImportFrom(self, node):
        if node.module == "subprocess":
            self.fail(node, "from subprocess import (use `import subprocess` and a list call)")
        self.generic_visit(node)

    def visit_Call(self, node):
        f = node.func
        name = f.id if isinstance(f, ast.Name) else f.attr if isinstance(f, ast.Attribute) else None
        owner = f.value.id if isinstance(f, ast.Attribute) and isinstance(f.value, ast.Name) else None
        if name in ("eval", "exec") and owner is None:
            self.fail(node, f"{name}()")
        if owner == "os" and name in FORBIDDEN_OS:
            self.fail(node, f"os.{name}")
        if owner == "subprocess" and name in SUBPROCESS_CALLS:
            self.subprocess_call(node, name)
        elif name == "git" and not (self.stack and self.stack[-1] == "git" and self.rel.endswith(INDIRECT_GIT[0])):
            sub = _const(node.args[1]) if len(node.args) > 1 else None
            if sub not in GIT_READ:
                self.fail(node, f"git helper with subcommand {sub!r}, not one of {sorted(GIT_READ)}")
        self.generic_visit(node)

    def subprocess_call(self, node, name):
        if name in ("getoutput", "getstatusoutput"):
            return self.fail(node, f"subprocess.{name} runs a shell")
        for kw in node.keywords:
            if kw.arg == "shell" and not (isinstance(kw.value, ast.Constant) and kw.value.value is False):
                self.fail(node, "shell= is not the constant False")
        if not node.args or not isinstance(node.args[0], ast.List):
            return self.fail(node, "subprocess argument is not a list literal")
        elts = node.args[0].elts
        if not all(_plain(e) for e in elts):
            return self.fail(node, "subprocess list holds something other than constants, names, attributes, *names or f-strings of names")
        prog = _const(elts[0]) if elts else None
        fn = self.stack[-1] if self.stack else None
        if prog != "git":
            ok = any(self.rel.endswith(f) and fn == g and prog == exe for (f, g), exe in EXEC_ALLOW.items())
            if not (ok and len(elts) == 2 and isinstance(elts[1], ast.Name)):
                self.fail(node, "subprocess starts a program other than git (or not the allowlisted node call)")
            return
        i = 3 if len(elts) > 2 and _const(elts[1]) == "-C" else 1
        sub = _const(elts[i]) if len(elts) > i else None
        indirect = self.stack and self.stack[-1] == INDIRECT_GIT[1] and self.rel.endswith(INDIRECT_GIT[0])
        if sub not in GIT_READ and not indirect:
            self.fail(node, f"git subcommand {sub!r} is not one of {sorted(GIT_READ)}")


def tool_state(paths):
    """For each tool file: its git blob id in the working tree, the blob id at HEAD, and whether they are equal (clean).
    Records made by a renderer, the carrier builder or the checker say which committed tools made them."""
    top = os.path.dirname(HERE)
    out = {}
    for p in paths:
        rel = os.path.relpath(os.path.abspath(p), REPO).replace("\\", "/")
        with open(p, "rb") as f:
            data = f.read()
        work = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
        r = subprocess.run(["git", "-C", top, "rev-parse", f"HEAD:{rel}"], capture_output=True, text=True)
        head = r.stdout.strip() if r.returncode == 0 else None
        out[rel] = {"blob": work, "head_blob": head, "clean": work == head}
    return out


def require_clean(paths):
    """Returns (repo HEAD, tool_state); exits when a tool file differs from HEAD: records come from committed tools only."""
    state = tool_state(paths)
    dirty = sorted(k for k, v in state.items() if not v["clean"])
    if dirty:
        sys.exit("a tool file differs from HEAD (commit it first, records come from committed tools only): " + ", ".join(dirty))
    top = os.path.dirname(HERE)
    head = subprocess.run(["git", "-C", top, "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
    return head, state


def check(extra=()):
    bad = script_hits()
    scanned = []
    for p in tool_files() + [os.path.abspath(e) for e in extra]:
        rel = os.path.relpath(p, REPO).replace("\\", "/") if p.startswith(REPO) else os.path.basename(p)
        scanned.append(rel)
        with open(p, "rb") as f:
            tree = ast.parse(f.read().decode("utf-8"), filename=rel)
        v = Visitor(rel)
        v.visit(tree)
        bad += v.bad
    return {"schema": "tbound-f36-dev-shell-guard/v1", "tools": scanned, "git_read_subcommands": sorted(GIT_READ), "violations": bad, "passed": not bad}


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--out")
    ap.add_argument("--also", action="append", default=[], help="scan this extra Python file too (negative controls)")
    a = ap.parse_args()
    r = check(a.also)
    text = json.dumps(r, indent=1) + "\n"
    if a.out:
        with open(a.out, "w", encoding="utf-8", newline="\n") as f:
            f.write(text)
    sys.stdout.write(text)
    return 0 if r["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
