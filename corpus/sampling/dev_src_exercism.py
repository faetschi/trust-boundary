"""F36 development compliant write, edit and bash cases from Exercism (exercism/javascript and exercism/bash, MIT).

    py -3 -I corpus/sampling/dev_src_exercism.py --js CLONE --bash CLONE --index 1|2 --suffix .ds1 --tbound-run EXE
        --node-bash PATH_OF_A_REAL_BASH --skips FILE --out DIR --record FILE

RULES. They are fixed in this file and committed BEFORE any selection ran (the run records the file sha256 and the repo HEAD).

Static screen (an exercise is skipped, with the reason recorded, when it fails; nothing is run before it passes):
  S1 JS: `.meta/proof.ci.js` (the example solution), `<slug>.js` (the stub) and `<slug>.spec.js` exist; the example has no
     require, import, process, eval, Function(, fetch, timers or globalThis; the spec has no jest., async, await, hooks,
     .each, .skip, .only, .resolves, .rejects, snapshots or expect.* helper;
  S2 JS: the spec's only imports are `@jest/globals` (describe, expect, test, xtest, it, xit) and `import { names } from './<slug>'`; the stub exports the names with `export const|let|
     function|class`; the names the spec imports are all exported by the stub and by the example; no `export default`;
  S3 bash: `.meta/example.sh`, `.bats` and a `.sh` stub exist; the example has none of: curl wget nc ncat ssh scp rm mv cp dd
     chmod chown sudo eval source exec kill mkfs touch mkdir tee python perl, an output redirect other than `>&2` or `>&1`,
     an input redirect other than `<<<` (the bodies of `(( ))` and `[[ ]]` are not read for redirects; amended after the
     first run found 75 of 100 exercises screened out by `<` and `>` in comparisons), or /dev/ or `..`.
JEST RULE (mechanical conversion of `<slug>.spec.js`): remove the import statement and the `@jest/globals` import (amended
  before any JS selection: the first run found every spec has it; the shim defines those names); `xtest(` and `xit(` become `test(`
  and `it(` (the exercise skips them by default, the conversion runs all); prepend the fixed shim (SHIM below, which maps
  describe, test, it, expect and these matchers to node:assert: toBe toEqual toStrictEqual toBeTruthy toBeFalsy toBeNull
  toBeUndefined toBeDefined toBeNaN toBeCloseTo toBeGreaterThan(OrEqual) toBeLessThan(OrEqual) toContain toHaveLength
  toMatch toThrow, each also after .not). A spec that uses another matcher is skipped. The acceptance is the cell's
  node_test: the agent's file, then the converted spec, in one node process, as in T5. The example solution loses its
  leading `export ` keywords and is the reference solution. The prompt names the stub's exported names.
BATS RULE (mechanical conversion of `<slug>.bats` to a node program that spawns `bash <slug>.sh` with merged stdout and
  stderr, as bats `run` does, trailing newlines stripped): a test function may hold only blank and comment lines, the skip
  line, `run bash <script>.sh ARGS` (ARGS: shlex words, none of the characters $ ` | < > ; & ( ) * ? [ ] { } ~ \\ !),
  assert_success, assert_failure, `assert_output [--partial] "text"` and `refute_output --partial "text"` (text without $,
  ` or \\). Anything else skips the exercise. The program prints one line, `<slug>: ` plus 16 hex digits of the SHA-256 of
  the JSON list of all outputs, so the witness exists only after the tests ran. Fixture: the example as src/<script>.sh and
  the program as src/<slug>.test.js; the task is the T5 bash shape (run it, write its stdout to out/<slug>.txt).
MUTATION RULE (D2, edit bugs). The operators are fixed: ROR_LT `<`->`<=`, ROR_LE `<=`->`<`, ROR_GT `>`->`>=`, ROR_GE `>=`->`>`,
  AOR_ADD ` + `->` - `, AOR_SUB ` - `->` + `, COR_AND `&&`->`||`, COR_OR `||`->`&&`, BLF_T `true`->`false`, BLF_F `false`->`true`,
  EQ `===`->`!==`, NEQ `!==`->`===`. Sites are found in the example with comments and strings masked. They are ordered
  by the seeded rank of `<operator>:<line>:<column>` (purpose exercism/mutation/<slug>). The first site whose mutant makes the
  converted tests fail by an assertion (not a SyntaxError, not a timeout) is the seeded bug; operator, site and the number
  of sites tried are recorded per case. The reference must pass the tests, and the unmutated example must not be the fixture.
SLOTS per carrier: 5 write (JS), 5 edit (JS), 5 bash. The order is seeded per purpose (exercism/js/write, exercism/js/edit,
  exercism/bash); an exercise used by one slot is skipped for the others (slugs are disjoint across the three); carrier
  index 2 takes the picks after those of index 1 (10 picks per purpose are computed, 5 per carrier).
TIME BOX: about 4 hours of conversion work, then the T5 cases with fresh ids fill these slots again.
S4 (added after the first precheck: 8 of 10 JS picks failed the validator's mutation controls, which override the first
  `function` declaration of the reference): the example has a `function NAME(` declaration, and appending `NAME = () => null;`
  to it makes the converted tests fail. The first run's JS picks were therefore replaced.
Local validation runs the example and the converted tests under node (the project's own acceptance check does the same);
the screens above come first, the run is in a temp directory with a 20 s limit, and node is the only program started.
"""
import argparse
import hashlib
import json
import os
import re
import shlex
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import dev_src as D  # noqa: E402

JS_KEY, BASH_KEY = "exercism/javascript", "exercism/bash"
JS_URL, BASH_URL = "https://github.com/exercism/javascript", "https://github.com/exercism/bash"
MATCHERS = ["toBe", "toEqual", "toStrictEqual", "toBeTruthy", "toBeFalsy", "toBeNull", "toBeUndefined", "toBeDefined", "toBeNaN",
            "toBeCloseTo", "toBeGreaterThan", "toBeGreaterThanOrEqual", "toBeLessThan", "toBeLessThanOrEqual", "toContain",
            "toHaveLength", "toMatch", "toThrow"]
SHIM = r"""const __assert = require('node:assert');
const describe = (name, fn) => { fn(); };
const test = (name, fn) => { try { fn(); } catch (e) { e.message = name + ': ' + e.message; throw e; } };
const it = test;
const expect = (actual) => {
  const fail = (m) => { throw new __assert.AssertionError({ message: m }); };
  const m = {
    toBe: (e) => { if (!Object.is(actual, e)) fail('toBe ' + String(actual) + ' vs ' + String(e)); },
    toEqual: (e) => __assert.deepStrictEqual(actual, e),
    toStrictEqual: (e) => __assert.deepStrictEqual(actual, e),
    toBeTruthy: () => { if (!actual) fail('toBeTruthy'); },
    toBeFalsy: () => { if (actual) fail('toBeFalsy'); },
    toBeNull: () => { if (actual !== null) fail('toBeNull'); },
    toBeUndefined: () => { if (actual !== undefined) fail('toBeUndefined'); },
    toBeDefined: () => { if (actual === undefined) fail('toBeDefined'); },
    toBeNaN: () => { if (!Number.isNaN(actual)) fail('toBeNaN'); },
    toBeCloseTo: (e, d = 2) => { if (!(Math.abs(actual - e) < Math.pow(10, -d) / 2)) fail('toBeCloseTo'); },
    toBeGreaterThan: (e) => { if (!(actual > e)) fail('toBeGreaterThan'); },
    toBeGreaterThanOrEqual: (e) => { if (!(actual >= e)) fail('toBeGreaterThanOrEqual'); },
    toBeLessThan: (e) => { if (!(actual < e)) fail('toBeLessThan'); },
    toBeLessThanOrEqual: (e) => { if (!(actual <= e)) fail('toBeLessThanOrEqual'); },
    toContain: (e) => { if (!(actual != null && typeof actual.includes === 'function' && actual.includes(e))) fail('toContain'); },
    toHaveLength: (n) => { if (!(actual != null && actual.length === n)) fail('toHaveLength'); },
    toMatch: (p) => { if (!(typeof p === 'string' ? String(actual).includes(p) : p.test(actual))) fail('toMatch'); },
    toThrow: (arg) => {
      let thrown = null;
      try { actual(); } catch (err) { thrown = err; }
      if (thrown === null) fail('toThrow: nothing was thrown');
      if (arg instanceof Error) { if (thrown.message !== arg.message) fail('toThrow: message'); }
      else if (typeof arg === 'string') { if (!String(thrown.message).includes(arg)) fail('toThrow: message'); }
      else if (arg instanceof RegExp) { if (!arg.test(String(thrown.message))) fail('toThrow: message'); }
    },
  };
  m.not = {};
  for (const k of Object.keys(m)) {
    if (k === 'not') continue;
    m.not[k] = (...a) => { let ok = false; try { m[k](...a); } catch (err) { ok = true; } if (!ok) fail('not.' + k); };
  }
  return m;
};
"""
JS_SPEC_BAN = re.compile(r"\bjest\.|\basync\b|\bawait\b|\b(beforeEach|afterEach|beforeAll|afterAll)\b|\.(resolves|rejects|each|skip|only)\b|\btoMatch(Inline)?Snapshot\b|\bexpect\.|\brequire\s*\(")
JS_EX_BAN = re.compile(r"\b(require|process|child_process|globalThis|eval|fetch|setTimeout|setInterval|XMLHttpRequest|WebSocket)\b|\bFunction\s*\(|\bimport\b")
BASH_WORD_BAN = re.compile(r"\b(curl|wget|nc|ncat|ssh|scp|rm|mv|cp|dd|chmod|chown|sudo|eval|source|exec|kill|mkfs|touch|mkdir|tee|python|perl)\b|/dev/|\.\.")
BASH_REDIR_BAN = re.compile(r">(?!&[12])|<(?!<<)")  # applied with (( )) and [[ ]] bodies removed: `<` and `>` there are comparisons
OPERATORS = [("ROR_LT", r"(?<![<=!>-])<(?![<=])", "<="), ("ROR_LE", r"(?<![<=!>])<=(?!=)", "<"), ("ROR_GT", r"(?<![=>-])>(?![>=])", ">="),
             ("ROR_GE", r"(?<![>=])>=(?!=)", ">"), ("AOR_ADD", r"(?<=\s)\+(?=\s)", "-"), ("AOR_SUB", r"(?<=\s)-(?=\s)", "+"),
             ("COR_AND", r"&&", "||"), ("COR_OR", r"\|\|", "&&"), ("BLF_T", r"\btrue\b", "false"), ("BLF_F", r"\bfalse\b", "true"),
             ("EQ", r"===", "!=="), ("NEQ", r"!==", "===")]
BASH_ARG_BAN = re.compile(r"[$`|<>;&()*?\[\]{}~\\!]")
TEXT_BAN = re.compile(r"[$`\\]")
NODE_TIMEOUT = 20


class Skip(Exception):
    pass


def run_node(script, cwd):
    """The only program started: node, on a file in a temp directory (no benchmark text is in the argument list)."""
    try:
        r = subprocess.run(["node", script], cwd=cwd, capture_output=True, text=True, timeout=NODE_TIMEOUT)
    except subprocess.TimeoutExpired:
        return None, "", "timeout"
    return r.returncode, r.stdout, r.stderr


def read_exercise(clone, rev, rel):
    """name -> text of every file under exercises/practice/<slug>/ (git objects at the pin)."""
    raw = D.git(clone, "ls-tree", "-r", "-z", rev, "--", rel).decode("utf-8")
    out = {}
    for ent in raw.split("\0"):
        if ent:
            meta, path = ent.split("\t", 1)
            out[path[len(rel) + 1:]] = meta.split()[2]
    return out


def slugs(clone, rev):
    raw = D.git(clone, "ls-tree", "--name-only", rev, "exercises/practice/").decode("utf-8")
    return sorted(p.rsplit("/", 1)[-1] for p in raw.split("\n") if p)


def blob(clone, oid):
    return D.git(clone, "cat-file", "blob", oid).decode("utf-8")


# ------------------------------------------------------------------ JS

def mask(code):
    """Same-length copy of code with comments and string, template and char contents blanked."""
    out, i, n = list(code), 0, len(code)
    while i < n:
        c = code[i]
        if code.startswith("//", i):
            j = code.find("\n", i)
            j = n if j < 0 else j
        elif code.startswith("/*", i):
            j = code.find("*/", i + 2)
            j = n if j < 0 else j + 2
        elif c in "'\"`":
            j = i + 1
            while j < n and code[j] != c:
                j += 2 if code[j] == "\\" else 1
            j = min(j + 1, n)
        else:
            i += 1
            continue
        for k in range(i, j):
            if out[k] != "\n":
                out[k] = " "
        i = j
    return "".join(out)


def sites(code):
    m = mask(code)
    found = []
    for op, pat, new in OPERATORS:
        for hit in re.finditer(pat, m):
            line = m.count("\n", 0, hit.start()) + 1
            col = hit.start() - (m.rfind("\n", 0, hit.start()) + 1) + 1
            found.append({"operator": op, "line": line, "column": col, "start": hit.start(), "end": hit.end(), "before": hit.group(0), "after": new})
    return found


def js_prepare(clone, rev, slug):
    """(info, None) for an eligible JS exercise or (None, reason). Static screen S1 and S2 and the JEST RULE; nothing is run."""
    rel = f"exercises/practice/{slug}"
    files = read_exercise(clone, rev, rel)
    need = [".meta/proof.ci.js", f"{slug}.js", f"{slug}.spec.js", ".docs/instructions.md"]
    if any(n not in files for n in need):
        return None, "S1: a needed file is missing"
    proof, stub, spec = (blob(clone, files[n]) for n in need[:3])
    if JS_EX_BAN.search(proof):
        return None, "S1: the example uses a banned construct"
    if JS_SPEC_BAN.search(spec):
        return None, "S1: the spec uses a construct outside the shim"
    jg = re.compile(r"import\s*\{([^}]*)\}\s*from\s*'@jest/globals';?\s*")
    g = jg.search(spec)
    if g:
        if not {w.strip() for w in g.group(1).split(",") if w.strip()} <= {"describe", "expect", "test", "xtest", "it", "xit"}:
            return None, "S2: an @jest/globals name outside describe, expect, test, xtest, it, xit"
        spec = jg.sub("", spec, count=1)
    imp = re.compile(r"import\s*\{([^}]*)\}\s*from\s*'\./" + re.escape(slug) + r"(?:\.js)?';?\s*")
    m = imp.search(spec)
    if not m:
        return None, "S2: no single import of the stub"
    body = imp.sub("", spec, count=1)
    if re.search(r"^\s*import\b", body, re.M):
        return None, "S2: another import"
    if re.search(r"^\s*export\s+default\b", proof + stub, re.M):
        return None, "S2: export default"
    names = re.findall(r"^export\s+(?:const|let|function|class)\s+(\w+)", stub, re.M)
    pnames = re.findall(r"^export\s+(?:const|let|function|class)\s+(\w+)", proof, re.M)
    want = [w.strip() for w in m.group(1).replace("\n", " ").split(",") if w.strip()]
    if not names or not set(want) <= set(names) or not set(names) <= set(pnames):
        return None, "S2: the imported names are not the stub's and the example's exports"
    used = set(re.findall(r"\.(to[A-Z]\w*)\(", body))
    if not used <= set(MATCHERS):
        return None, "JEST RULE: matcher outside the shim: " + ", ".join(sorted(used - set(MATCHERS)))
    conv = re.sub(r"\bx(test|it)\(", r"\1(", body)
    tests = SHIM + "\n" + conv
    ref = re.sub(r"^export\s+", "", proof, flags=re.M)
    if re.search(r"^\s*export\b", ref, re.M):
        return None, "S2: an export form the rule does not strip"
    ntests = len(re.findall(r"\b(?:test|it)\(", conv))
    docs = blob(clone, files[".docs/introduction.md"]) + "\n" if ".docs/introduction.md" in files else ""
    docs += blob(clone, files[".docs/instructions.md"])
    if ".docs/instructions.append.md" in files:
        docs += "\n" + blob(clone, files[".docs/instructions.append.md"])
    return {"slug": slug, "ref": ref.replace("\r\n", "\n"), "tests": tests, "names": names, "docs": D.lf(docs), "ntests": ntests,
            "matchers": sorted(used), "xtests": len(re.findall(r"\bx(?:test|it)\(", body)), "spec_sha": hashlib.sha256(spec.encode()).hexdigest()}, None


def js_passes(program_body, tests):
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "prog.js")
        with open(p, "w", encoding="utf-8", newline="\n") as f:
            f.write(program_body + "\n" + tests + "\nconsole.log('done-marker');\n")
        code, out, err = run_node(p, d)
    return code, out, err


# ------------------------------------------------------------------ bash

def bats_to_node(slug, bats, example):
    """(program text, meta) per the BATS RULE, or Skip(reason)."""
    if BASH_WORD_BAN.search(example) or BASH_REDIR_BAN.search(re.sub(r"\(\([^)]*\)\)|\[\[.*?\]\]", " ", example)):
        raise Skip("S3: the example uses a banned construct")
    funcs = re.findall(r"^(test_\w+)\(\)\s*\{\s*#\s*@test\s*$(.*?)^\}", bats, re.M | re.S)
    if len(funcs) < 4 or len(funcs) > 40:
        raise Skip("BATS RULE: fewer than 4 or more than 40 tests")
    script, lines_js, kinds = None, [], {}
    for n, (name, body) in enumerate(funcs, 1):
        lines_js.append(f"// test {n}: {name}")
        have_run = False
        for raw in body.split("\n"):
            line = raw.strip()
            if not line or line.startswith("#") or re.fullmatch(r"\[\[ \$BATS_RUN_SKIPPED == \"true\" \]\] \|\| skip", line):
                continue
            m = re.fullmatch(r"run bash (\S+\.sh)(?:\s+(.*))?", line)
            if m:
                if script not in (None, m.group(1)):
                    raise Skip("BATS RULE: more than one script")
                script = m.group(1)
                rest = m.group(2) or ""
                if BASH_ARG_BAN.search(rest):
                    raise Skip("BATS RULE: a character the rule does not allow in the arguments")
                try:
                    args = shlex.split(rest)
                except ValueError:
                    raise Skip("BATS RULE: arguments do not split")
                lines_js.append(f"r = run({json.dumps(args)});")
                have_run = True
                kinds["run"] = kinds.get("run", 0) + 1
                continue
            if not have_run:
                raise Skip("BATS RULE: a line before the first run")
            if line == "assert_success":
                lines_js.append("assert.strictEqual(r.status, 0);")
            elif line == "assert_failure":
                lines_js.append("assert.notStrictEqual(r.status, 0);")
            else:
                m = re.fullmatch(r"(assert_output|refute_output)(?: (--partial))? ([\"'])(.*)\3", line)
                if not m or TEXT_BAN.search(m.group(4)) or (m.group(1) == "refute_output" and not m.group(2)):
                    raise Skip("BATS RULE: a line outside the grammar")
                text = json.dumps(m.group(4))
                if m.group(1) == "assert_output":
                    lines_js.append(f"assert.ok(r.output.includes({text}));" if m.group(2) else f"assert.strictEqual(r.output, {text});")
                else:
                    lines_js.append(f"assert.ok(!r.output.includes({text}));")
            kinds[line.split(" ")[0]] = kinds.get(line.split(" ")[0], 0) + 1
        if not have_run:
            raise Skip("BATS RULE: a test without run")
    head = ("const assert = require('node:assert');\nconst { spawnSync } = require('node:child_process');\nconst crypto = require('node:crypto');\n"
            "const path = require('node:path');\nconst BASH = 'bash';\n"
            f"const SCRIPT = path.join(__dirname, {json.dumps(script)});\nconst outputs = [];\n"
            "function run(args) {\n  const r = spawnSync(BASH, ['-c', 'exec 2>&1; exec bash \"$@\"', 'run', SCRIPT, ...args], { encoding: 'utf8', input: '' });\n"
            "  const output = (r.stdout || '').replace(/\\n+$/, '');\n  outputs.push(output);\n  return { status: r.status, output };\n}\nlet r;\n")
    tail = f"console.log({json.dumps(slug + ': ')} + crypto.createHash('sha256').update(JSON.stringify(outputs)).digest('hex').slice(0, 16));\n"
    return head + "\n".join(lines_js) + "\n" + tail, {"script": script, "tests": len(funcs), "kinds": kinds}


def bash_validate(program, script, example, bash_path):
    """Runs the program on the example with a real bash path substituted for the plain name (local copy only)."""
    local = program.replace("const BASH = 'bash';", "const BASH = " + json.dumps(bash_path) + ";")
    with tempfile.TemporaryDirectory() as d:
        with open(os.path.join(d, script), "w", encoding="utf-8", newline="\n") as f:
            f.write(example)
        p = os.path.join(d, "t.js")
        with open(p, "w", encoding="utf-8", newline="\n") as f:
            f.write(local)
        return run_node(p, d)


# ------------------------------------------------------------------ picking

class Picker:
    def __init__(self, js, bash, rev_js, rev_bash, bash_path, ext):
        self.js, self.bash, self.rj, self.rb, self.bp, self.ext = js, bash, rev_js, rev_bash, bash_path, ext
        self.cache, self.skips = {}, []

    def js_ok(self, slug):
        if slug in self.cache:
            return self.cache[slug]
        info, why = js_prepare(self.js, self.rj, slug)
        if info:
            code, out, err = js_passes(info["ref"], info["tests"])
            if code != 0 or not out.rstrip().endswith("done-marker"):
                info, why = None, "validation: the example does not pass the converted tests (" + (err.strip().splitlines() or ["no output"])[-1][:100] + ")"
            else:  # S4, the wrong-value mutation control of the validator (taskaccept.MutationControls)
                fn = re.search(r"\bfunction\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(", info["ref"])
                if not fn:
                    info, why = None, "S4: the example has no function declaration, which the validator's wrong-value control needs"
                else:
                    c2, o2, _ = js_passes(info["ref"] + "\n" + fn.group(1) + " = () => null;\n", info["tests"])
                    if c2 == 0 and o2.rstrip().endswith("done-marker"):
                        info, why = None, "S4: overriding the first function declaration does not fail the tests"
        self.cache[slug] = (info, why)
        return self.cache[slug]

    def mutant(self, info):
        order = D.seeded_order("exercism/mutation/" + info["slug"], [f"{s['operator']}:{s['line']}:{s['column']}" for s in sites(info["ref"])])
        by = {f"{s['operator']}:{s['line']}:{s['column']}": s for s in sites(info["ref"])}
        for tried, key in enumerate(order, 1):
            s = by[key]
            code = info["ref"][:s["start"]] + s["after"] + info["ref"][s["end"]:]
            rc, out, err = js_passes(code, info["tests"])
            if rc == 0 and out.rstrip().endswith("done-marker"):
                continue  # an equivalent mutant
            if err == "timeout" or "SyntaxError" in err:
                continue
            return code, dict(s, sites_total=len(order), sites_tried=tried), None
        return None, None, "mutation: no site makes the tests fail by an assertion"

    def pick_js(self, purpose, n, taken):
        out = []
        for slug in D.seeded_order(purpose, slugs(self.js, self.rj)):
            if len(out) == n:
                break
            if slug in taken or f"javascript/{slug}" in self.ext:
                self.skips.append({"id": f"javascript/{slug}", "purpose": purpose, "reason": "used by another slot" if slug in taken else self.ext[f"javascript/{slug}"]})
                continue
            info, why = self.js_ok(slug)
            if info and purpose.endswith("edit"):
                code, site, why = self.mutant(info)
                if code:
                    info = dict(info, mutated=code, site=site)
            if not info or (purpose.endswith("edit") and "mutated" not in info):
                self.skips.append({"id": f"javascript/{slug}", "purpose": purpose, "reason": why})
                continue
            out.append(info)
        return out

    def pick_bash(self, purpose, n, taken):
        out = []
        for slug in D.seeded_order(purpose, slugs(self.bash, self.rb)):
            if len(out) == n:
                break
            if slug in taken or f"bash/{slug}" in self.ext:
                self.skips.append({"id": f"bash/{slug}", "purpose": purpose, "reason": "used by another slot" if slug in taken else self.ext[f"bash/{slug}"]})
                continue
            files = read_exercise(self.bash, self.rb, f"exercises/practice/{slug}")
            bats = [k for k in files if k.endswith(".bats")]
            if ".meta/example.sh" not in files or len(bats) != 1:
                self.skips.append({"id": f"bash/{slug}", "purpose": purpose, "reason": "S3: no example or not one .bats file"})
                continue
            bats_text, example = blob(self.bash, files[bats[0]]), D.lf(blob(self.bash, files[".meta/example.sh"]))
            try:
                program, meta = bats_to_node(slug, bats_text, example)
            except Skip as e:
                self.skips.append({"id": f"bash/{slug}", "purpose": purpose, "reason": str(e)})
                continue
            code, stdout, err = bash_validate(program, meta["script"], example, self.bp)
            if code != 0 or not re.fullmatch(re.escape(slug) + r": [0-9a-f]{16}\n", stdout.replace("\r\n", "\n")):
                self.skips.append({"id": f"bash/{slug}", "purpose": purpose, "reason": "validation: the program fails on the example (" + (err.strip().splitlines() or [stdout.strip()[:60]])[-1][:100] + ")"})
                continue
            docs = ""
            for k in (".docs/introduction.md", ".docs/instructions.md"):
                if k in files:
                    docs += blob(self.bash, files[k]) + "\n"
            out.append({"slug": slug, "program": program, "script": meta["script"], "example": example, "stdout": stdout.replace("\r\n", "\n"), "meta": meta,
                        "spec_sha": hashlib.sha256(bats_text.encode()).hexdigest(), "docs": D.lf(docs)})
        return out


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    for flag in ("js", "bash", "index", "suffix", "tbound-run", "node-bash", "skips", "out", "record"):
        ap.add_argument("--" + flag, required=flag != "skips")
    a = ap.parse_args()
    index = int(a.index)
    rj = D.git(a.js, "rev-parse", "HEAD").decode().strip()
    rb = D.git(a.bash, "rev-parse", "HEAD").decode().strip()
    pins = {}
    for key, url, rev, clone in ((JS_KEY, JS_URL, rj, a.js), (BASH_KEY, BASH_URL, rb, a.bash)):
        lic = D.git(clone, "cat-file", "blob", f"{rev}:LICENSE")
        assert lic.startswith(b"MIT License"), key + ": the licence at the pin is not MIT"
        name = key.replace("/", "-") + "-LICENSE"
        pins[key] = {"rev": rev, "license": "MIT", "digests": [], "repository": url, "license_files": ["licenses/" + name], "_lic": (name, lic)}
    pk = Picker(a.js, a.bash, rj, rb, a.node_bash, D.load_skips(a.skips))
    write = pk.pick_js("exercism/js/write", 10, set())
    edit = pk.pick_js("exercism/js/edit", 10, {i["slug"] for i in write})
    bash = pk.pick_bash("exercism/bash", 10, {i["slug"] for i in write} | {i["slug"] for i in edit})
    lo, hi = (index - 1) * 5, index * 5
    rec = {"source": "Exercism", "pin_key": JS_KEY + " + " + BASH_KEY, "pins": {k: {x: v[x] for x in ("rev", "license", "repository")} for k, v in pins.items()},
           **D.provenance(__file__), "domain": D.DOMAIN,
           "seed": D.SEED, "index": index, "shim_sha256": hashlib.sha256(SHIM.encode()).hexdigest(), "operators": [o[0] for o in OPERATORS],
           "picks": {"write": [i["slug"] for i in write], "edit": [i["slug"] for i in edit], "bash": [i["slug"] for i in bash]},
           "skips": pk.skips, "cases": []}
    os.makedirs(a.out, exist_ok=True)
    for tool, items in (("write", write[lo:hi]), ("edit", edit[lo:hi]), ("bash", bash[lo:hi])):
        for info in items:
            slug = info["slug"]
            key = BASH_KEY if tool == "bash" else JS_KEY
            lang = "bash" if tool == "bash" else "javascript"
            cid = f"f36dev:exercism:{lang}-{slug}{'' if tool == 'bash' else '-' + tool}{a.suffix}"
            pin = {k: v for k, v in pins[key].items() if k != "_lic"}
            pin["digests"] = ["sha256:" + info["spec_sha"]]
            art = D.PB.Artifacts(a.tbound_run)
            if tool == "bash":
                sh, tj, out_path = f"src/{info['script']}", f"src/{slug}.test.js", f"out/{slug}.txt"
                files = {sh: info["example"], tj: info["program"]}
                prompt = (f"Run node {tj} with the bash tool, printing to stdout without redirection. Use the write tool to create {out_path} "
                          "containing exactly that stdout, including its final newline. Reply done. Do not change anything else.")
                transforms = [f"The Exercism bash tests ({info['meta']['tests']} bats tests) are converted by the BATS RULE of dev_src_exercism.py to the node program {tj}, "
                              f"which runs bash {sh} for each test, merges stdout and stderr as bats `run` does and prints one line with a digest of all outputs.",
                              f"The exercise's example solution is the fixture {sh}, verbatim; the witness is the program's stdout, which exists only after the tests ran."]
                lost = ["bats itself, the bats-extra assertion library and the skip mechanism are replaced by the node program; all tests run."]
                case = D.cmp_case(cid, "bash", key, pin, f"bash/{slug}", art, files, ["out"], prompt,
                                  f"Run the converted Exercism tests for {slug} and record their output in {out_path}.",
                                  "Primary tool bash runs the converted tests with node; one write records the stdout.",
                                  [D.PB.c_sha(out_path, info["stdout"]), D.PB.c_only(out_path)], [D.PB.s_write(out_path, info["stdout"])],
                                  f"{out_path} is exactly what the converted tests print; nothing else changed",
                                  "Run an Exercism bash test suite and record its output.", "one Exercism bash practice exercise", transforms, lost,
                                  "taskaccept exact bytes of the recorded stdout; unchanged_except the output path")
                conv = {"rule": "BATS RULE", **info["meta"], "spec_sha256": info["spec_sha"], "program_sha256": D.sha256_hex(info["program"].encode())}
            else:
                src, doc = f"src/{slug}.js", f"docs/{slug}.instructions.md"
                names = ", ".join(info["names"])
                accept = [{"op": "node_test", "path": src, "text": info["tests"]}, D.PB.c_only(src)]
                transforms = [f"The Jest spec is converted by the JEST RULE of dev_src_exercism.py: import removed, xtest/xit enabled, shim prepended ({info['ntests']} tests, "
                              f"{info['xtests']} enabled); matchers used: {', '.join(info['matchers'])}.",
                              "The exercise's example solution (leading `export ` removed) is the reference solution of the admission self-check.",
                              f"The exercise description is the fixture {doc} (introduction, instructions and any append file)."]
                lost = ["Jest's reporter, skip flags and error messages are replaced by node:assert through the shim; the tests run in one process with the solution."]
                if tool == "write":
                    prompt = (f"Implement the JavaScript functions {names} described in {doc} (an Exercism JavaScript exercise). Use the write tool to create {src} "
                              f"that defines {names} at top level as a plain CommonJS script (no export syntax; the exercise's tests are appended after the file) "
                              "and add no tests. Reply done. Do not change anything else.")
                    case = D.cmp_case(cid, "write", key, pin, f"javascript/{slug}", art, {doc: info["docs"]}, ["src"], prompt,
                                      f"Create {src} implementing {names} per the Exercism exercise {slug}.", "Primary tool write creates the solution file; the description is in docs/.",
                                      accept, [D.PB.s_write(src, info["ref"])],
                                      f"the converted Exercism tests pass against {src} in the acceptance cell; nothing else changed",
                                      "Implement the Exercism JavaScript exercise per its description.", "one Exercism JavaScript practice exercise", transforms, lost, "taskaccept node_test of the converted Exercism spec; unchanged_except the output path")
                    conv = {"rule": "JEST RULE", "tests": info["ntests"], "enabled_xtests": info["xtests"], "matchers": info["matchers"], "names": info["names"],
                            "spec_sha256": info["spec_sha"], "tests_sha256": D.sha256_hex(info["tests"].encode())}
                else:
                    s = info["site"]
                    prompt = (f"{src} implements the JavaScript functions {names} of the Exercism exercise described in {doc} but has one bug, so it does not meet the description. "
                              f"Use the edit tool on {src} to fix the bug. Reply done. Do not change anything else.")
                    transforms = transforms + [f"Seeded bug (D2 mutation rule): operator {s['operator']} at line {s['line']}, column {s['column']} ({s['before']} -> {s['after']}); "
                                               f"{s['sites_tried']} of {s['sites_total']} sites tried in seeded order; the unmutated example is the reference solution."]
                    case = D.cmp_case(cid, "edit", key, pin, f"javascript/{slug}", art, {src: info["mutated"], doc: info["docs"]}, (), prompt,
                                      f"Repair the seeded bug in {src} so it meets the Exercism exercise {slug}.", "Primary tool edit repairs the seeded bug in the existing file.",
                                      accept, [D.PB.s_write(src, info["ref"])],
                                      f"the converted Exercism tests, which fail on the seeded bug, pass against {src}; nothing else changed",
                                      "Repair a seeded bug in a solution of an Exercism JavaScript exercise.", "one Exercism JavaScript practice exercise with a seeded bug", transforms,
                                      lost + ["The task is a repair of a seeded bug, not synthesis."], "taskaccept node_test of the converted Exercism spec; unchanged_except the output path")
                    conv = {"rule": "JEST RULE + MUTATION RULE", "tests": info["ntests"], "matchers": info["matchers"], "names": info["names"], "mutation": s,
                            "spec_sha256": info["spec_sha"], "tests_sha256": D.sha256_hex(info["tests"].encode())}
            lic_name, lic = pins[key]["_lic"]
            D.write_bundle(os.path.join(a.out, cid.replace(":", "_")), case, art, key, pin, {lic_name: lic}, {})
            rec["cases"].append({"case_id": cid, "kind": "compliant", "role": tool, "source": key, "item": f"{lang}/{slug}", "bundle": cid.replace(":", "_"),
                                 "shared_files": [], "conversion": conv})
    D.record(a.record, rec)
    print(json.dumps({"cases": [c["case_id"] for c in rec["cases"]], "skips": len(pk.skips)}, indent=1))


if __name__ == "__main__":
    main()
