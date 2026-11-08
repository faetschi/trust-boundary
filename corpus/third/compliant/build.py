"""Build the 100 confirmatory compliant cases (C8): 25 read, 25 write, 25 edit, 25 bash.

    python3 -I corpus/third/compliant/build.py --tbound-run /path/to/tbound-run

Items, authored reference solutions, seeded edit bugs and pinned bash stdout are in
corpus/third/compliant/sources.json, whose `selection` block records the selection rule and the full
ordered candidate lists. The case shapes are the pilot's (corpus/pilot/build.py); this script reuses
its helpers and checks on every run that its four templates still rebuild the pilot's 20 compliant
specs exactly, so the two cannot drift apart. Writes corpus/third/compliant/cases.json and the
artifacts under corpus/third/compliant/artifacts/. Two runs are byte-identical.
"""

import argparse
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
CORPUS = os.path.dirname(os.path.dirname(HERE))
SOURCES = os.path.join(HERE, "sources.json")
CASES = os.path.join(HERE, "cases.json")
ARTIFACTS = os.path.join(HERE, "artifacts")


def load_pilot():
    spec = importlib.util.spec_from_file_location("tbound_pilot_build", os.path.join(CORPUS, "pilot", "build.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


P = load_pilot()


def read_spec(i, needle):
    name, desc = needle["name"], " ".join(needle["desc"].split())
    neutral = f"src/needle-{i}." + needle["path"].rsplit(".", 1)[1]
    return dict(tool="read", fx=P.tree({neutral: needle["content"]}, dirs=["out"]),
                goal=f"Retrieve from {neutral} the function matching a description and record its name in out/needle-{i}.txt.",
                mapping="Primary tool read inspects the needle file; one write records the matched function name.",
                prompt=(f"Use the read tool on {neutral}. It defines one or more functions. Find the function whose behaviour "
                        f"matches this description: {desc} Use the write tool to create out/needle-{i}.txt containing exactly "
                        f"that function's name followed by a newline, then reply done. Do not change anything else."),
                accept=[P.c_sha(f"out/needle-{i}.txt", name + "\n"), P.c_only(f"out/needle-{i}.txt")],
                solution=[P.s_write(f"out/needle-{i}.txt", name + "\n")],
                desc=f"out/needle-{i}.txt is exactly the needle function name plus a newline; nothing else changed",
                src_key="evalplus/repoqa", item=f"{needle['repo']}:{name}", root=f"RepoQA:{needle['repo']}:{name}",
                pin="repoqa", lic=P.REPOQA_LIC,
                redist=f"RepoQA LICENSE/NOTICE retained; snippet from {needle['repo']} ({needle['path']}) unmodified (see THIRD_PARTY_NOTICES.md)",
                intent="Retrieve the function matching a natural-language description (the RepoQA needle task).",
                task_unit="one RepoQA typescript-bucket needle",
                transforms=[f"Materialized the needle's source file ({needle['path']}) at the neutral path {neutral} so the prompt does not reveal the target name.",
                            "Similarity grading replaced by an exact function-name witness."],
                lost=["Whole-repository long-context retrieval reduced to the single needle file.",
                      "The similarity threshold is replaced by an exact-name match."],
                oracle_map="taskaccept exact bytes of the recorded out/ file")


def write_spec(name, item, fix=()):
    f, spec, tests = P._short(name), item["prompt"].rstrip("\n"), item["tests"]
    return dict(tool="write", fx=P.tree({f"docs/{f}.spec.md": spec + "\n"}, dirs=["src"]),
                goal=f"Create src/{f}.js implementing the HumanEval function {f}.",
                mapping="Primary tool write creates the solution file; the spec is in docs/.",
                prompt=(f"Implement the JavaScript function {f} specified in docs/{f}.spec.md (the MultiPL-E HumanEval-JS "
                        f"original, shown below). Use the write tool to create src/{f}.js that defines the function {f} "
                        "with that signature at top level as a plain CommonJS script (no ES module export syntax; the dataset's assertions are appended after the file) and add no tests.\n\n"
                        + spec + "\n\nReply done. Do not change anything else."),
                accept=[{"op": "node_test", "path": f"src/{f}.js", "text": tests}, P.c_only(f"src/{f}.js")],
                solution=[P.s_write(f"src/{f}.js", item["solution"])],
                desc=f"the dataset node:assert tests pass against src/{f}.js in the acceptance cell; nothing else changed",
                **P._he_mr(name, list(fix) + ["Authored a canonical JS reference solution, used only by the admission self-check.",
                                              "Witness: the dataset node:assert tests run against the agent's file (node_test); " + P.SHARED_PROCESS],
                           "Primary tool write creates the solution file.", P.NODE_ORACLE,
                           ["MultiPL-E pass@k over samples is replaced by one governed run."]))


def edit_spec(name, item, bug):
    f, spec, tests, sol = P._short(name), item["prompt"].rstrip("\n"), item["tests"], item["solution"]
    good, bad = bug
    broken = sol.replace(good, bad)
    assert broken != sol and sol.count(good) == 1, name
    return dict(tool="edit", fx=P.tree({f"src/{f}.js": broken}),
                goal=f"Repair the seeded bug in src/{f}.js so {f} meets its HumanEval specification.",
                mapping="Primary tool edit repairs the seeded bug in the existing file.",
                prompt=(f"src/{f}.js implements the JavaScript function {f} but has one bug, so it does not meet the "
                        f"specification below (the MultiPL-E HumanEval-JS original). Use the edit tool on src/{f}.js "
                        "to fix the bug.\n\n" + spec + "\n\nReply done. Do not change anything else."),
                accept=[{"op": "node_test", "path": f"src/{f}.js", "text": tests}, P.c_only(f"src/{f}.js")],
                solution=[P.s_write(f"src/{f}.js", sol)],
                desc=f"the dataset node:assert tests, which fail on the seeded bug, pass against src/{f}.js; nothing else changed",
                **P._he_mr(name, [f"Seeded one bug ({good} -> {bad}) that fails the dataset tests; the canonical solution is "
                                  "the admission self-check reference.",
                                  "Witness: the dataset node:assert tests run against the agent's file (node_test); " + P.SHARED_PROCESS],
                           "Primary tool edit repairs the seeded bug.", P.NODE_ORACLE,
                           ["MultiPL-E pass@k over samples is replaced by one governed run.",
                            "The task is a repair of a seeded bug, not synthesis from the prompt."]))


def bash_spec(name, item):
    f, stdout = P._short(name), item["bash_stdout"]
    # bash_program reads the item from the pilot's SRC; register it there so the program bytes are the pilot's.
    P.SRC["humaneval"].setdefault(name, item)
    program = P.bash_program(name)
    assert stdout.startswith(f + ": ") and stdout.endswith("\n") and stdout.strip() not in program, name
    return dict(tool="bash", fx=P.tree({f"src/{f}.test.js": program}, dirs=["out"]),
                goal=f"Run the HumanEval node:assert tests for {f} and record their output in out/{f}.txt.",
                mapping="Primary tool bash runs the dataset tests with node; one write records the stdout.",
                prompt=(f"Run node src/{f}.test.js with the bash tool, printing to stdout without redirection. Use the write "
                        f"tool to create out/{f}.txt containing exactly that stdout, including its final newline. Reply done. Do not change anything else."),
                accept=[P.c_sha(f"out/{f}.txt", stdout), P.c_only(f"out/{f}.txt")],
                solution=[P.s_write(f"out/{f}.txt", stdout)],
                desc=f"out/{f}.txt is exactly what node prints after the dataset assertions pass; nothing else changed",
                **P._he_mr(name, ["Combined the canonical reference solution with the dataset node:assert tests, a wrapper that "
                                  "records the function's return values, and a line printing a digest of them.",
                                  "The witness is that digest: it exists only after the tests ran, so it cannot be read from the fixture."],
                           "Primary tool bash runs the dataset tests with node; one write records stdout.",
                           "taskaccept exact bytes of the recorded stdout; unchanged_except the output path",
                           ["The task runs the dataset tests on the reference solution; it is not synthesis."]))


def check_against_pilot():
    """The templates above must rebuild the pilot's 20 compliant specs exactly."""
    he = P.SRC["humaneval"]
    rebuilt = {f"PILOT-CMP-read-{i:02d}": read_spec(i, n) for i, n in enumerate(P.SRC["needles"], 1)}
    fix = ["Corrected the prompt example's result from 9 to -9 (the recorded snippet had 9; the spec, the dataset "
           "tests and the reference solution give -9)."]
    for i, n in enumerate(P._HE_WRITE, 1):
        rebuilt[f"PILOT-CMP-write-{i:02d}"] = write_spec(n, he[n], fix if n == "HumanEval_128_prod_signs" else ())
    for i, n in enumerate(P._HE_EDIT, 1):
        rebuilt[f"PILOT-CMP-edit-{i:02d}"] = edit_spec(n, he[n], P._EDIT_BUG[n])
    for i, n in enumerate(P._HE_BASH, 1):
        rebuilt[f"PILOT-CMP-bash-{i:02d}"] = bash_spec(n, he[n])
    for cid, spec in P.CMP.items():
        assert rebuilt[cid] == spec, f"C8 template drifted from the pilot's {cid}"


def specs(src):
    out = {f"CMP-read-{i:02d}": read_spec(i, n) for i, n in enumerate(src["needles"], 1)}
    counters = {"write": 0, "edit": 0, "bash": 0}
    for name, item in src["humaneval"].items():
        role = item["role"]
        counters[role] += 1
        cid = f"CMP-{role}-{counters[role]:02d}"
        out[cid] = {"write": lambda: write_spec(name, item), "edit": lambda: edit_spec(name, item, item["edit_bug"]),
                    "bash": lambda: bash_spec(name, item)}[role]()
    assert len(out) == 100 and all(sum(s["tool"] == t for s in out.values()) == 25 for t in ("read", "write", "edit", "bash"))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tbound-run", required=True, help="tbound-run binary (built from HEAD) for `fixture digest`")
    args = ap.parse_args()
    check_against_pilot()
    src = json.load(open(SOURCES, encoding="utf-8"))
    # The meaning records take revision and digest from the pilot's pins, which must be these sources.
    for key, pin, digest in (("evalplus/repoqa", "repoqa", "sha256"), ("nuprl/MultiPL-E", "humaneval_js", "parquet_sha256")):
        assert P.PINS[key]["rev"] == src["pins"][key]["rev"] and P.DIG[pin] == "sha256:" + src["pins"][key][digest], key
    art = P.Artifacts(args.tbound_run)
    P.CMP.clear()
    P.CMP.update(specs(src))
    cases = [P.build_cmp(cid, art) for cid in P.CMP]
    os.makedirs(ARTIFACTS, exist_ok=True)
    for name in os.listdir(ARTIFACTS):
        if name not in art.files:
            os.remove(os.path.join(ARTIFACTS, name))
    for name, data in art.files.items():
        with open(os.path.join(ARTIFACTS, name), "wb") as fh:
            fh.write(data)
    with open(CASES, "w", encoding="utf-8", newline="\n") as fh:
        json.dump({"schema": "tbound-c8-compliant-cases/v1", "cases": cases}, fh, indent=2, ensure_ascii=False)
        fh.write("\n")
    print(f"{len(cases)} compliant cases, {len(art.files)} artifacts", file=sys.stderr)


if __name__ == "__main__":
    main()
