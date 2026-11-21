"""C8: verify authored HumanEval-JS solutions with node and merge them into corpus/third/compliant/sources.json.

Usage (cwd = F:\\trust-boundary\\.local\\source-screen):
    py -3 -I scripts\\c8-verify.py ..\\worktrees\\corpus-20261107 output\\c8-solutions.json [--merge]

c8-solutions.json: {name: {"solution": "<js>", "edit_bug": ["good", "bad"]}}; edit_bug only for edit items.
Checks per item, the same ones admission runs: solution + dataset tests exit 0; for edit, the seeded bug
(good occurs once) makes the tests fail; for bash, the pilot's bash_program prints "<fn>: <16 hex>\\n" and
that line is recorded as bash_stdout. node runs under --permission with read access to its temp dir only.
"""
import importlib.util
import json
import os
import subprocess
import sys
import tempfile

NODE = "node"


def run(program):
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "program.js")
        open(path, "w", encoding="utf-8", newline="\n").write(program)
        proc = subprocess.run([NODE, "--permission", f"--allow-fs-read={tmp}", path],
                              capture_output=True, text=True, timeout=30)
        return proc.returncode, proc.stdout, proc.stderr


def pilot_module(worktree):
    spec = importlib.util.spec_from_file_location("pilot_build", os.path.join(worktree, "corpus", "pilot", "build.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def main(worktree, solutions_path, merge):
    dest = os.path.join(worktree, "corpus", "third", "compliant", "sources.json")
    src = json.load(open(dest, encoding="utf-8"))
    solutions = json.load(open(solutions_path, encoding="utf-8"))
    pilot = pilot_module(worktree)
    failures = []
    for name, item in src["humaneval"].items():
        got = solutions.get(name)
        if not got:
            failures.append(f"{name}: no solution")
            continue
        sol, tests = got["solution"], item["tests"]
        code, _, err = run(sol + "\n" + tests)
        if code != 0:
            failures.append(f"{name}: reference fails its tests: {err.strip()[-200:]}")
            continue
        if item["role"] == "edit":
            good, bad = got.get("edit_bug") or ("", "")
            if not good or sol.count(good) != 1 or good == bad:
                failures.append(f"{name}: edit_bug must name a substring that occurs exactly once")
                continue
            if run(sol.replace(good, bad) + "\n" + tests)[0] == 0:
                failures.append(f"{name}: seeded bug still passes the tests")
                continue
            item["edit_bug"] = [good, bad]
        if item["role"] == "bash":
            pilot.SRC["humaneval"][name] = {"solution": sol, "tests": tests}
            code, out, err = run(pilot.bash_program(name))
            fn = pilot._short(name)
            if code != 0 or not out.startswith(fn + ": ") or not out.endswith("\n") or out.count("\n") != 1:
                failures.append(f"{name}: bash program output {out!r} {err.strip()[-200:]}")
                continue
            item["bash_stdout"] = out
        item["solution"] = sol
    for failure in failures:
        print("FAIL", failure)
    print(f"verified {len(src['humaneval']) - len(failures)} of {len(src['humaneval'])}")
    if merge and not failures:
        with open(dest, "w", encoding="utf-8", newline="\n") as fh:
            fh.write(json.dumps(src, indent=1, ensure_ascii=False) + "\n")
        print("merged into", dest)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1], sys.argv[2], "--merge" in sys.argv[3:]))
