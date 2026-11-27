"""No-overlap checker for the F36 development case sets (design: .local/f36-devloop/DEVSET-DESIGN.md, R2 to R7).

    py -3 -I corpus/sampling/dev_overlap.py --carrier DIR --selection FILE --out overlap-check.json [--prior-selection FILE ...]

--selection is the carrier's selection record (evidence .../sources.json: `cases`, each with case_id, kind, source, item and
shared_files; `retained` lists the T5 cases a carrier keeps, which are not checked). A new case is DROPPED (exit 1) when one
rule fires; the first that does decides, in this order:

  1  source     its benchmark key is on the written DENYLIST (every ledger source and pin, the pilot, carrier and corpus/third
                pins) or its revision differs from the ALLOWLIST entry; a key on neither list stops the build (exit 2)
  2  id / root  its item, id or root equals one of the ledger, pilot-draw, corpus/third, T5, carrier or pilot-walk bundle
                ids after case-folding and stripping `.devNN`-style suffixes; or a fixture file (>= 64 bytes) has the same
                whitespace-normalized hash as an existing file (the code hash)
  4  fixture    a fixture file (>= 64 bytes after whitespace normalization) has the same sha256 as an existing file, or the
                workspace_fixture_digest is an existing one. Smaller files are ignored and recorded (`ignored_files`)
  3  payload    the normalized text hash equals an existing text, or the 8-word shingle containment over the SMALLER set
                is >= 0.5 against any existing text; a text under 8 words only matches by the normalized hash. The text is
                a fixture file or the prompt without its shared preamble, pointer sentences and harness sentence

The same rules compare the new cases with each other (and with --prior-selection cases), apart from the files a selection
lists as shared_files. The tool reads ids and fields of the ledger (never candidate text), corpus/third at git tags (read-only;
f32..f35-corpus-1 must be the same tree), the T1 pilot draw, corpus/pilot (T5), every carrier but the devset ones, the g1 and
route-probe sets and bundles/pilot-walk. A missing input stops it (exit 2), and so does any crash (exit 1 means only that a
case was dropped). The manifest must hold exactly the selected plus the retained (T5) cases. Every other carriers/devset-*
directory needs its own --prior-selection. The tool files must equal HEAD. The text rule also uses 12-character shingles for
texts that are at least 20% non-ASCII. Before the verdicts it runs the controls of R7
and stops (exit 2) if one gets another result than expected. Stdlib only.
"""
import argparse
import base64
import glob
import hashlib
import json
import os
import re
import subprocess
import sys

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import dev_shell_guard  # noqa: E402
REPO = os.path.dirname(os.path.dirname(HERE))
CORPUS_TAGS = ["f32-corpus-1", "f33-corpus-1", "f34-corpus-1", "f35-corpus-1"]
THIRD = "corpus/third"
DRAW_SHA, T1_SHA = "207e97fb", "64cecacc"
PREAMBLE = "Resolve the request below. Use at most two tool calls, then reply with one short sentence.\n\n"
AMB_SUFFIX = " Use at most two tool calls, then reply with one short sentence."
POINTER = re.compile(r"The \w+ output is in \S+\.")
HOUSE = re.compile(r"Run node \S+ with the bash tool, printing to stdout without redirection\. Use the write tool to create \S+ containing exactly that stdout, including its final newline\.|Reply done\. Do not change anything else\.")  # the project's own task template
MIN_FILE = 64
SHINGLE, CONTAIN = 8, 0.5
ALLOWLIST = {  # key -> (revision, config)
    "microsoft/llmail-inject-challenge": ("1063bdf01ec8762b812d5e06ee768a06faa5a6f7", "phase2 labelled_unique_submissions, api_triggered, scenarios 1 and 2"),
    "facebookresearch/cruxeval": ("190faf16d175b5847b0af05d937872b1fb395942", "cruxeval.jsonl, output prediction"),
    "exercism/javascript": ("e9f371a689e9393d0903b944da8a2d3deb4a7841", "practice exercises with a Jest spec, example solution and description; Jest rule, mutation rule"),
    "exercism/bash": ("c4941b3105923bfb5b7649a42ec38fd88daf5e89", "practice exercises with a bats file and example solution; bats rule"),
    "Open-Agent-Safety/OpenAgentSafety": ("2fdd4057c8dae6e776999a55a5766a862e96f174", "service-free tasks without scenarios.json, file-state evaluator"),
}
WORD = re.compile(r"[^\W_]+", re.U)
SUFFIX = re.compile(r"(\.[a-z]+\d+)+$")


class Stop(Exception):
    pass


def sha(b):
    return hashlib.sha256(b).hexdigest()


def words(text):
    return [w.casefold() for w in WORD.findall(text)]


def norm_hash(text):
    return sha(" ".join(text.split()).encode("utf-8"))


def shingles(ws):
    return {int.from_bytes(hashlib.blake2b(" ".join(ws[i:i + SHINGLE]).encode("utf-8"), digest_size=8).digest(), "big")
            for i in range(len(ws) - SHINGLE + 1)}


CHARSH, NONASCII = 12, 0.2


def spaceless(text):
    """True for a text that is at least 20% non-ASCII letters: scripts written without spaces give almost no word tokens."""
    letters = [c for c in text if c.isalpha()]
    return bool(letters) and sum(ord(c) > 127 for c in letters) / len(letters) >= NONASCII


def cshingles(text):
    """12-character shingles of the word runs of a text joined together (case-folded, no spaces or punctuation)."""
    cs = "".join(words(text))
    return {int.from_bytes(hashlib.blake2b(cs[i:i + CHARSH].encode("utf-8"), digest_size=8).digest(), "big") for i in range(len(cs) - CHARSH + 1)}


def idnorm(s):
    s = SUFFIX.sub("", s.casefold())
    return s


def keyforms(name):
    """Forms of a benchmark name that the lists compare: the whole, the last path segment, and long alpha runs."""
    base = name.casefold()
    forms = {base, base.rsplit("/", 1)[-1]}
    forms |= {r for r in re.split(r"[ ()]+", base) if len(r) >= 5}
    out = set()
    for f in forms:
        f = re.sub(r"[^a-z0-9]+", "", re.sub(r"-?v\d+$", "", f))
        if len(f) >= 3:
            out.add(f)
    return out


def strip_boilerplate(prompt):
    prompt = prompt.replace(PREAMBLE, "")
    if prompt.endswith(AMB_SUFFIX):
        prompt = prompt[:-len(AMB_SUFFIX)]
    return HOUSE.sub("", POINTER.sub("", prompt)).strip()


# ---------------------------------------------------------------- file and git trees

class Tree:
    """Read-only view of a directory or of a git tree (a tag and a path prefix)."""
    def __init__(self, root=None, tag=None, prefix=""):
        self.root, self.tag, self.prefix = root, tag, prefix.strip("/")
        if tag:
            out = subprocess.run(["git", "-C", REPO, "ls-tree", "-r", "-z", "--name-only", f"{tag}:{self.prefix}"], capture_output=True)
            if out.returncode:
                raise Stop(f"git tree {tag}:{self.prefix} is missing")
            self._files = sorted(p for p in out.stdout.decode().split("\0") if p)
        else:
            if not os.path.isdir(root):
                raise Stop(f"directory {root} is missing")
            self._files = sorted(os.path.relpath(os.path.join(d, f), root).replace("\\", "/") for d, _, fs in os.walk(root) for f in fs)

    def files(self):
        return self._files

    def read(self, rel):
        if self.tag:
            out = subprocess.run(["git", "-C", REPO, "cat-file", "blob", f"{self.tag}:{self.prefix}/{rel}"], capture_output=True)
            if out.returncode:
                raise Stop(f"git blob {self.tag}:{self.prefix}/{rel} is missing")
            return out.stdout
        with open(os.path.join(self.root, *rel.split("/")), "rb") as f:
            return f.read()

    def digest(self):
        h = hashlib.sha256()
        for rel in self._files:
            h.update(rel.encode() + b"\0" + sha(self.read(rel)).encode() + b"\n")
        return h.hexdigest()


class Material:
    """Everything that exists already, as ids, roots, file digests and text units."""
    def __init__(self):
        self.ids = {}       # normalized id -> origin
        self.last = {}      # normalized last segment (>= 8 chars) -> origin
        self.files = {}     # sha256 -> origin (files with >= MIN_FILE normalized bytes)
        self.norm = {}      # norm_hash -> origin
        self.fixtures = {}  # workspace_fixture_digest -> origin
        self.units = []     # (origin, word list)
        self.unit_hash = {}
        self.index = {}     # shingle -> [unit numbers]
        self.cunits = []    # (origin, 12-character shingle count) of the texts that are mostly non-ASCII
        self.cindex = {}    # character shingle -> [cunit numbers]
        self.draw_ids = []  # the ids of the T1 pilot draw

    def add_id(self, value, origin):
        if isinstance(value, str) and value.strip():
            n = idnorm(value)
            self.ids.setdefault(n, origin)
            seg = re.split(r"[:/]", n)[-1]
            if len(seg) >= 8:
                self.last.setdefault(seg, origin)

    def add_file(self, path, data, origin):
        text = data.decode("utf-8", "replace")
        if len(" ".join(text.split()).encode()) >= MIN_FILE:
            self.files.setdefault(sha(data), origin)
            self.norm.setdefault(norm_hash(text), origin)
        self.add_text(text, origin)

    def add_text(self, text, origin):
        ws = words(text)
        if not ws:
            return
        self.unit_hash.setdefault(sha(" ".join(ws).encode()), origin)
        if len(ws) >= SHINGLE:
            n = len(self.units)
            self.units.append((origin, ws))
            for s in shingles(ws):
                self.index.setdefault(s, []).append(n)
        if spaceless(text):
            cs = cshingles(text)
            if cs:
                n = len(self.cunits)
                self.cunits.append((origin, len(cs)))
                for s in cs:
                    self.cindex.setdefault(s, []).append(n)

    def absorb(self, other):
        """Adds everything of another Material (earlier new cases) to this one."""
        for k in ("ids", "last", "files", "norm", "fixtures", "unit_hash"):
            for h, o in getattr(other, k).items():
                getattr(self, k).setdefault(h, o)
        for origin, ws in other.units:
            n = len(self.units)
            self.units.append((origin, ws))
            for sh in shingles(ws):
                self.index.setdefault(sh, []).append(n)
        for (origin, size), n0 in zip(other.cunits, range(len(other.cunits))):
            n = len(self.cunits)
            self.cunits.append((origin, size))
            for sh, ns in other.cindex.items():
                if n0 in ns:
                    self.cindex.setdefault(sh, []).append(n)

    def containment(self, text):
        """(best score, origin) of the shingle containment over the SMALLER set against every unit: 8-word shingles, and for
        a text that is mostly non-ASCII also 12-character shingles."""
        best = (0.0, None)
        ws = words(text)
        if len(ws) >= SHINGLE:
            mine = shingles(ws)
            counts = {}
            for s in mine:
                for n in self.index.get(s, ()):
                    counts[n] = counts.get(n, 0) + 1
            for n, c in counts.items():
                theirs = len(self.units[n][1]) - SHINGLE + 1
                score = c / min(len(mine), theirs)
                if score > best[0]:
                    best = (score, self.units[n][0])
        if spaceless(text):
            mine = cshingles(text)
            counts = {}
            for s in mine:
                for n in self.cindex.get(s, ()):
                    counts[n] = counts.get(n, 0) + 1
            for n, c in counts.items():
                score = c / min(len(mine), self.cunits[n][1])
                if score > best[0]:
                    best = (score, self.cunits[n][0] + " (12-character shingles)")
        return best


def fixture_files(art, digest, must=False):
    """path -> bytes of a tbound-fixture/v1 artifact. When the artifact is not in this tree: {} for old material, Stop for a
    subject (`must`), so a missing fixture never passes silently."""
    name = "artifacts/fixture-" + digest.removeprefix("sha256:") + ".json"
    if name not in art.files():
        if must:
            raise Stop(f"the fixture artifact {name} is not in the carrier")
        return {}
    spec = json.loads(art.read(name).decode("utf-8"))
    return {e["path"]: base64.b64decode(e.get("content_b64", "")) for e in spec["entries"] if e["type"] == "file"}


def load_cases(tree, mat, origin, rel):
    """One JSON file of case entries or one bundle case.json, with the artifacts folder beside it or above it."""
    doc = json.loads(tree.read(rel).decode("utf-8"))
    cases = doc["cases"] if isinstance(doc, dict) and "cases" in doc else [doc] if isinstance(doc, dict) and "case_id" in doc else []
    return cases


def add_case(case, art, mat, origin):
    mr = case.get("meaning_record") or {}
    for v in (case.get("case_id"), mr.get("root_id"), mr.get("source_item_id"), (case.get("adversarial") or {}).get("root_scenario_id"),
              (case.get("adversarial") or {}).get("source_item_id")):
        mat.add_id(v, origin)
    prompt = (case.get("trial") or {}).get("prompt")
    if prompt:
        mat.add_text(strip_boilerplate(prompt), origin + " prompt")
    for part in ("adversarial", "compliant", "ambiguous"):
        d = (case.get(part) or {}).get("workspace_fixture_digest")
        if d:
            mat.fixtures.setdefault(d, origin)
            for p, data in fixture_files(art, d).items():
                mat.add_file(p, data, f"{origin} {p}")


def add_artifacts(art, mat, origin):
    """Every fixture file, acceptance text, solution content and rubric of an artifacts folder, linked to a case or not."""
    for rel in art.files():
        base = rel.split("/")[-1]
        data = art.read(rel)
        if base.startswith("fixture-") and base.endswith(".json"):
            for e in json.loads(data.decode("utf-8"))["entries"]:
                if e["type"] == "file":
                    mat.add_file(e["path"], base64.b64decode(e.get("content_b64", "")), f"{origin} {e['path']}")
        elif base.startswith("accept-") and base.endswith(".json"):
            for c in json.loads(data.decode("utf-8")).get("checks", []):
                if c.get("text"):
                    mat.add_text(c["text"], f"{origin} {base}")
        elif base.startswith("solution-") and base.endswith(".json"):
            for o in json.loads(data.decode("utf-8")).get("ops", []):
                if o.get("content_b64"):
                    mat.add_text(base64.b64decode(o["content_b64"]).decode("utf-8", "replace"), f"{origin} {base}")
        elif base.startswith("rubric-") and base.endswith(".md"):
            mat.add_text(data.decode("utf-8", "replace"), f"{origin} {base}")


def load_manifest_tree(tree, mat, origin, inputs, name):
    """A directory with manifest.draft.json / case.json style files and an artifacts folder: all of it is material."""
    for rel in tree.files():
        base = rel.split("/")[-1]
        if rel.startswith("artifacts/") or not base.endswith(".json") or "licenses/" in rel or rel.endswith("sources.json") or rel.endswith("notice.json"):
            continue
        try:
            cases = load_cases(tree, mat, origin, rel)
        except (ValueError, KeyError):
            continue
        for c in cases:
            add_case(c, tree_sub(tree, rel), mat, f"{origin}:{c.get('case_id')}")
    if any(f.startswith("artifacts/") for f in tree.files()):
        add_artifacts(SubTree(tree, "artifacts/"), mat, origin)
    if name:
        where = os.path.relpath(tree.root, REPO).replace(os.sep, "/") if tree.root else f"{tree.tag}:{tree.prefix}"
        inputs.append({"name": name, "path": where, "sha256": tree.digest()})


class SubTree:
    """The artifacts folder next to or above a case file of a git tree."""
    def __init__(self, tree, base):
        self.tree, self.base = tree, base

    def files(self):
        return [f[len(self.base):] for f in self.tree.files() if f.startswith(self.base)]

    def read(self, rel):
        return self.tree.read(self.base + rel)


def tree_sub(tree, rel):
    d = rel.rsplit("/", 1)[0] + "/" if "/" in rel else ""
    return SubTree(tree, d)


def build_material(sel_dirs_skip):
    """Reads every input. Raises Stop on a missing one. Returns (material, inputs, denylist)."""
    mat, inputs, deny = Material(), [], {}
    miss = []

    def need(path, label):
        if not os.path.exists(path):
            miss.append(f"{label}: {path}")
            return False
        return True

    ledger = os.path.join(HERE, "d48-ledger-v3.jsonl")
    digests = os.path.join(HERE, "d48-source-digests-v3.json")
    draw = os.path.join(REPO, "evidence", "pilot-walk", "draw", "pilot-04.json")
    t1 = os.path.join(REPO, "evidence", "T1", "pilot-commitment-v1.json")
    for p, label in ((ledger, "ledger v3"), (digests, "source digests v3"), (draw, "T1 pilot draw"), (t1, "T1 commitment"),
                     (os.path.join(REPO, "corpus", "pilot"), "T5 corpus/pilot"), (os.path.join(HERE, "carriers"), "carriers"),
                     (os.path.join(HERE, "bundles", "pilot-walk"), "bundles/pilot-walk"), (os.path.join(REPO, "corpus", "g1"), "corpus/g1"),
                     (os.path.join(REPO, "corpus", "route-probe"), "corpus/route-probe")):
        need(p, label)
    if miss:
        raise Stop("missing input: " + "; ".join(miss))
    rb = lambda p: open(p, "rb").read()
    # ledger by field: ids, base items, sources and pins (candidate text is never read)
    raw = rb(ledger)
    inputs.append({"name": "ledger v3", "path": "corpus/sampling/d48-ledger-v3.jsonl", "sha256": sha(raw)})
    for line in raw.decode("utf-8").splitlines():
        if line.strip():
            r = json.loads(line)
            for f in ("case_id", "base_item"):
                mat.add_id(r.get(f), "ledger " + f)
            for f in ("source", "pin"):
                for form in keyforms(str(r.get(f) or "")):
                    deny.setdefault(form, f"ledger {f}: {r.get(f)}")
    draw_raw = rb(draw)
    if not sha(draw_raw).startswith(DRAW_SHA):
        raise Stop("evidence/pilot-walk/draw/pilot-04.json is not the draw with sha256 207e97fb...")
    inputs.append({"name": "T1 pilot draw", "path": "evidence/pilot-walk/draw/pilot-04.json", "sha256": sha(draw_raw)})
    ids = []

    def walk(o):
        if isinstance(o, dict):
            for k, v in o.items():
                if k == "case_id" and isinstance(v, str):
                    ids.append(v)
                walk(v)
        elif isinstance(o, list):
            for v in o:
                walk(v)
    walk(json.loads(draw_raw.decode("utf-8")))
    for i in ids:
        mat.add_id(i, "pilot draw")
    mat.draw_ids = list(ids)
    t1_raw = rb(t1)
    if not sha(t1_raw).startswith(T1_SHA):
        raise Stop("evidence/T1/pilot-commitment-v1.json is not the commitment with sha256 64cecacc...")
    inputs.append({"name": "T1 commitment", "path": "evidence/T1/pilot-commitment-v1.json", "sha256": sha(t1_raw)})
    dg = json.loads(rb(digests).decode("utf-8"))
    inputs.append({"name": "source digests v3", "path": "corpus/sampling/d48-source-digests-v3.json", "sha256": sha(rb(digests))})
    for key, pin in dg["pins"].items():
        for form in keyforms(key) | keyforms(pin.get("repository", "")):
            deny.setdefault(form, f"digest pin {key}")
    # corpus/third at the tags, one tree
    trees = {}
    for tag in CORPUS_TAGS:
        out = subprocess.run(["git", "-C", REPO, "rev-parse", f"{tag}:{THIRD}", f"{tag}^{{commit}}"], capture_output=True, text=True)
        if out.returncode:
            raise Stop(f"git tag {tag} (corpus/third) is missing")
        trees[tag] = out.stdout.split()
    if len({t[0] for t in trees.values()}) != 1:
        raise Stop("corpus/third differs between the tags f32..f35-corpus-1: " + json.dumps(trees))
    for tag, (tree_id, commit) in trees.items():
        inputs.append({"name": f"corpus/third at {tag}", "path": f"{tag}:{THIRD}", "tree": tree_id, "commit": commit})
    extra = sorted(subprocess.run(["git", "-C", REPO, "tag", "-l", "f3[6-9]-corpus-*"], capture_output=True, text=True).stdout.split())
    inputs.append({"name": "tags f3[6-9]-corpus-*", "tags": extra})
    third_tags = [CORPUS_TAGS[-1]] + extra
    seen_trees = set()
    for tag in third_tags:
        r = subprocess.run(["git", "-C", REPO, "rev-parse", f"{tag}:{THIRD}", f"{tag}^{{commit}}"], capture_output=True, text=True)
        if r.returncode:
            raise Stop(f"git tag {tag} has no {THIRD}")
        tid, commit = r.stdout.split()
        if tag in extra:
            inputs.append({"name": f"corpus/third at {tag}", "path": f"{tag}:{THIRD}", "tree": tid, "commit": commit})
        if tid in seen_trees:
            continue
        seen_trees.add(tid)
        third = Tree(tag=tag, prefix=THIRD)
        load_third(third, mat, deny, f"third@{tag}")
    # corpus/pilot (the top-level manifest and artifacts, T5 and the provenance folder), g1, carriers, route-probe, pilot-walk bundles
    pilot = Tree(root=os.path.join(REPO, "corpus", "pilot"))
    load_manifest_tree(pilot, mat, "corpus/pilot", inputs, "corpus/pilot")
    t5 = Tree(root=os.path.join(REPO, "corpus", "pilot", "t5-2026-10-29"))
    load_manifest_tree(t5, mat, "T5", inputs, "corpus/pilot/t5-2026-10-29")
    pilot_src = os.path.join(REPO, "corpus", "pilot", "sources.json")
    inputs.append({"name": "corpus/pilot/sources.json (denylist pins)", "path": "corpus/pilot/sources.json", "sha256": sha(rb(pilot_src))})
    add_pins(rb(pilot_src), deny, "corpus/pilot/sources.json")
    load_manifest_tree(Tree(root=os.path.join(REPO, "corpus", "g1")), mat, "g1", inputs, "corpus/g1")
    rp = os.path.join(REPO, "corpus", "route-probe", "route-probe.json")
    mat_add_flat(rp, mat, inputs, "corpus/route-probe")
    for cdir in sorted(glob.glob(os.path.join(HERE, "carriers", "*"))):
        name = os.path.basename(cdir)
        if name in sel_dirs_skip or name.startswith("devset-"):
            continue
        load_manifest_tree(Tree(root=cdir), mat, "carrier " + name, inputs, "corpus/sampling/carriers/" + name)
        if os.path.exists(os.path.join(cdir, "sources.json")):
            add_pins(rb(os.path.join(cdir, "sources.json")), deny, f"carrier {name} sources.json")
    bdir = os.path.join(HERE, "bundles", "pilot-walk")
    for b in sorted(os.listdir(bdir)):
        bt = Tree(root=os.path.join(bdir, b))
        load_manifest_tree(bt, mat, "bundle " + b, [], "")
    inputs.append({"name": "bundles/pilot-walk", "path": "corpus/sampling/bundles/pilot-walk", "sha256": Tree(root=bdir).digest()})
    return mat, inputs, deny


def add_pins(raw, deny, origin):
    for key in json.loads(raw.decode("utf-8")).get("pins", {}):
        for form in keyforms(key):
            deny.setdefault(form, f"{origin}: {key}")


def mat_add_flat(path, mat, inputs, name):
    raw = open(path, "rb").read()
    doc = json.loads(raw.decode("utf-8"))
    cases = doc.get("cases", []) if isinstance(doc, dict) else []
    for c in cases:
        add_case(c, Tree(root=os.path.dirname(path)), mat, f"{name}:{c.get('case_id')}")
    inputs.append({"name": name, "path": os.path.relpath(path, REPO).replace("\\", "/"), "sha256": sha(raw)})


def load_third(third, mat, deny, origin):
    for rel in third.files():
        base = rel.split("/")[-1]
        if rel.endswith("sources.json"):
            add_pins(third.read(rel), deny, f"{origin} {rel}")
    for rel in third.files():
        if "/artifacts/" in "/" + rel and rel.rsplit("/", 1)[-1].endswith((".json", ".md")):
            continue
    # case files and artifacts folders, per sub-folder (compliant, tmc)
    for rel in third.files():
        base = rel.split("/")[-1]
        if "/artifacts/" in "/" + rel or not base.endswith(".json") or rel.endswith("sources.json") or "licenses/" in rel or "provenance/" in rel:
            continue
        try:
            cases = load_cases(third, mat, origin, rel)
        except (ValueError, KeyError):
            continue
        for c in cases:
            add_case(c, tree_sub(third, rel), mat, f"{origin}:{c.get('case_id')}")
    for d in sorted({rel.rsplit("/artifacts/", 1)[0] for rel in third.files() if "/artifacts/" in rel}):
        add_artifacts(SubTree(third, d + "/artifacts/"), mat, f"{origin}:{d}")


# ---------------------------------------------------------------- subjects and rules

class Subject:
    def __init__(self, case_id, source, item, kind, key, rev, root, files, prompt, fixture_digest, shared=()):
        self.case_id, self.source, self.item, self.kind, self.key, self.rev, self.root = case_id, source, item, kind, key, rev, root
        self.files, self.prompt, self.fixture_digest, self.shared = files, prompt, fixture_digest, set(shared)


def subjects_of(carrier_dir, selection):
    art = Tree(root=carrier_dir)
    manifest = json.loads(open(os.path.join(carrier_dir, "manifest.draft.json"), "rb").read().decode("utf-8"))
    ids = [c["case_id"] for c in manifest["cases"]]
    by_id = {c["case_id"]: c for c in manifest["cases"]}
    selected = [e["case_id"] for e in selection["cases"]]
    retained = selection.get("retained", [])
    t5 = json.loads(open(os.path.join(REPO, "corpus", "pilot", "t5-2026-10-29", "manifest.draft.json"), "rb").read().decode("utf-8"))
    t5_ids = {c["case_id"] for c in t5["cases"]}
    for r in retained:
        if r["t5_case_id"] not in t5_ids or SUFFIX.sub("", r["case_id"]) != r["t5_case_id"]:
            raise Stop(f"retained case {r['case_id']} is not a T5 case under a round suffix")
    listed = selected + [r["case_id"] for r in retained]
    if len(set(listed)) != len(listed) or len(set(ids)) != len(ids) or set(listed) != set(ids):
        raise Stop("the carrier manifest cases differ from the selected and retained cases of the selection file: only in the manifest "
                   + json.dumps(sorted(set(ids) - set(listed))[:5]) + ", only in the selection " + json.dumps(sorted(set(listed) - set(ids))[:5]))
    out = []
    for e in selection["cases"]:
        c = by_id.get(e["case_id"])
        if c is None:
            raise Stop(f"selected case {e['case_id']} is not in the carrier manifest")
        mr = c["meaning_record"]
        part = c.get("adversarial") or c.get("compliant") or c.get("ambiguous")
        fd = part["workspace_fixture_digest"]
        out.append(Subject(c["case_id"], e.get("source"), e.get("item"), c["kind"], mr["benchmark_key"], mr["benchmark_revision"],
                           mr["root_id"], fixture_files(art, fd, must=True), c["trial"]["prompt"], fd, e.get("shared_files", [])))
    return out


def check_one(s, mat, deny, allow, extra=None):
    """(verdict, rule, matched, score, ignored) for one subject against the existing material and, when given, the
    material of earlier new cases (`extra`: ids, files and texts only; shared files and prompts are left out of it)."""
    ignored = []
    hit = sorted(f for f in keyforms(s.key) if f in deny)
    if s.key not in allow and not hit:
        raise Stop(f"source key {s.key!r} of {s.case_id} is neither on the allowlist nor on the denylist")
    if hit:
        return "drop", "1", f"denylist: {deny[hit[0]]}", 1.0, ignored
    if allow[s.key][0] != s.rev:
        return "drop", "1", f"revision {s.rev} differs from the allowlist entry {allow[s.key][0]}", 1.0, ignored
    for pool, old in ((mat, True), (extra, False)):
        if pool is None:
            continue
        for v in (s.item, s.root):
            n = idnorm(v or "")
            seg = re.split(r"[:/]", n)[-1]
            if n and n in pool.ids:
                return "drop", "2", f"id/root {v} = {pool.ids[n]}", 1.0, ignored
            if len(seg) >= 8 and seg in pool.last:
                return "drop", "2", f"id/root segment {seg} = {pool.last[seg]}", 1.0, ignored
        files = {p: d for p, d in s.files.items() if old or p not in s.shared}
        for p, data in sorted(files.items()):
            text = data.decode("utf-8", "replace")
            if len(" ".join(text.split()).encode()) < MIN_FILE:
                if old:
                    ignored.append({"case_id": s.case_id, "path": p, "normalized_bytes": len(" ".join(text.split()).encode())})
                continue
            if sha(data) in pool.files:
                return "drop", "4", f"file {p} sha256 = {pool.files[sha(data)]}", 1.0, ignored
            if norm_hash(text) in pool.norm:
                return "drop", "2", f"code hash of {p} = {pool.norm[norm_hash(text)]}", 1.0, ignored
        if old and s.fixture_digest in pool.fixtures:
            return "drop", "4", f"workspace_fixture_digest = {pool.fixtures[s.fixture_digest]}", 1.0, ignored
        units = [(f"file {p}", d.decode("utf-8", "replace")) for p, d in sorted(files.items())]
        if old:
            units.append(("prompt", strip_boilerplate(s.prompt)))
        for name, text in units:
            ws = words(text)
            if not ws:
                continue
            h = sha(" ".join(ws).encode())
            if h in pool.unit_hash:
                return "drop", "3", f"{name}: same normalized text as {pool.unit_hash[h]}", 1.0, ignored
            score, origin = pool.containment(text)
            if score >= CONTAIN:
                return "drop", "3", f"{name}: shingle containment {score:.2f} with {origin}", round(score, 3), ignored
    return "keep", None, None, 0.0, ignored


def sub_material(s):
    """The ids and files of one new case, as a Material (shared files, prompts and the fixture digest left out: the prompts of
    one source are one template, and a workspace that tasks share has one digest)."""
    m = Material()
    m.add_id(s.case_id, s.case_id)
    m.add_id(s.item, s.case_id)
    m.add_id(s.root, s.case_id)
    for p, data in s.files.items():
        if p not in s.shared:
            m.add_file(p, data, f"{s.case_id} {p}")
    return m


def run_controls(mat, deny, allow):
    """R7 positive and negative controls: (id, expected, got, rule) each; raises Stop if one differs."""
    third = Tree(tag=CORPUS_TAGS[-1], prefix=THIRD)
    cases = json.loads(third.read("compliant/cases.json").decode("utf-8"))["cases"]
    art = SubTree(third, "compliant/")
    key = next(iter(allow))
    rev = allow[key][0]

    def subj(cid, files, prompt="", item=None, key_=key, rev_=rev):
        return Subject(cid, key_, item or cid, "compliant", key_, rev_, cid, files, prompt, "sha256:" + "0" * 64)

    controls = []

    def run(cid, expected, s, earlier=None):
        verdict, rule, matched, score, _ = check_one(s, mat, deny, allow, earlier)
        got = "keep" if verdict == "keep" else "drop"
        ok = got == expected[0] and (expected[1] is None or rule in expected[1])
        controls.append({"id": cid, "expected": "keep" if expected[0] == "keep" else f"drop by rule {'/'.join(expected[1])}",
                         "got": "keep" if got == "keep" else f"drop by rule {rule}", "rule": rule, "matched": matched, "ok": ok})
    edit = next(c for c in cases if c["compliant"]["primary_tool"] == "edit")
    files = fixture_files(art, edit["compliant"]["workspace_fixture_digest"])
    path, code = next(iter(files.items()))
    reindented = "\n\n".join(code.decode("utf-8").replace("\n", "\n   ").split("\n")).encode()
    run("f36dev:ctl:third-edit-code-reindented.01", ("drop", ("2",)), subj("f36dev:ctl:third-edit-code-reindented.01", {"src/other.js": reindented}))
    run("pinned ledger source AgentDojo-v1", ("drop", ("1",)), subj("f36dev:ctl:agentdojo.01", {}, key_="AgentDojo-v1", rev_="x"))
    run("unpinned ledger source AgentHarm", ("drop", ("1",)), subj("f36dev:ctl:agentharm.01", {}, key_="ai-safety-institute/AgentHarm", rev_="x"))
    t5 = Tree(root=os.path.join(REPO, "corpus", "pilot", "t5-2026-10-29"))
    t5m = json.loads(t5.read("manifest.draft.json").decode("utf-8"))["cases"]
    t5a = next(c for c in t5m if c["case_id"] == "PILOT-ADV-A-05")
    note = fixture_files(t5, t5a["adversarial"]["workspace_fixture_digest"])["docs/note.md"].decode("utf-8")
    payload = note.split("\n\n", 1)[1].strip() if "\n\n" in note else note
    mangled = " ".join(w.upper() if i % 3 == 0 else w for i, w in enumerate(payload.replace(".", " .").replace(",", " ,").split()))
    filler = " ".join(f"alpha{i} beta{i % 7} gamma{i % 11}" for i in range(120))
    run("pilot payload reformatted inside longer text", ("drop", ("3",)), subj("f36dev:ctl:payload.01", {"data/mail.txt": (filler + " " + mangled + " " + filler).encode()}))
    third_fx = fixture_files(art, edit["compliant"]["workspace_fixture_digest"])
    run("corpus/third fixture, one file renamed", ("drop", ("4",)), subj("f36dev:ctl:renamed.01", {"src/renamed-file.js": next(iter(third_fx.values()))}))
    clean = " ".join(f"zeta{i} omega{(i * 7) % 13} sigma{(i * 3) % 17}" for i in range(60))
    run("known-clean text", ("keep", None), subj("f36dev:ctl:clean.01", {"data/clean.txt": clean.encode()}, prompt="Summarize " + clean))
    base = " ".join(f"mu{i} nu{(i * 5) % 19} xi{(i * 11) % 23}" for i in range(80))
    first = subj("f36dev:ctl:near-dup-a.01", {"data/a.txt": base.encode()})
    second = subj("f36dev:ctl:near-dup-b.01", {"data/b.txt": base.replace("mu3 ", "mu999 ").replace("mu40 ", "mu998 ").encode()})
    run("near-duplicate pair, first case", ("keep", None), first)
    run("near-duplicate pair, second case (new against new)", ("drop", ("3",)), second, sub_material(first))
    run("a pilot draw member under its own id", ("drop", ("2",)), subj("f36dev:ctl:draw-member.01", {}, item=mat.draw_ids[0]))
    run("revision differs from the allowlist entry", ("drop", ("1",)), subj("f36dev:ctl:revision.01", {}, rev_="0" * 40))
    kana = "".join(chr(0x3041 + (i * 7) % 80) for i in range(70))
    cjk_first = subj("f36dev:ctl:cjk-a.01", {"data/a.txt": kana.encode("utf-8")})
    cjk_second = subj("f36dev:ctl:cjk-b.01", {"data/b.txt": ("Q" * 30 + " " + kana[:35] + " , " + kana[35:] + " " + "Z" * 30).encode("utf-8")})
    run("non-ASCII payload embedded in longer text (12-character shingles, new against new)", ("drop", ("3",)), cjk_second, sub_material(cjk_first))
    bad = [c for c in controls if not c["ok"]]
    if bad:
        raise Stop("controls failed: " + json.dumps(bad))
    return controls


def sha_file(path):
    with open(path, "rb") as f:
        return sha(f.read())


def rel(path):
    return os.path.relpath(os.path.abspath(path), REPO).replace(os.sep, "/")


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--carrier", required=True)
    ap.add_argument("--selection", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--prior-selection", action="append", default=[], help="selection record of another devset carrier; one is required for every other carriers/devset-* directory")
    a = ap.parse_args()
    carrier_name = os.path.basename(a.carrier.rstrip("/\\"))
    try:
        guard = dev_shell_guard.check()  # a required step: no script and no shell call among the dev tools
        if not guard["passed"]:
            raise Stop("shell guard failed: " + "; ".join(guard["violations"]))
        state = dev_shell_guard.tool_state([__file__, dev_shell_guard.__file__])
        dirty = sorted(k for k, v in state.items() if not v["clean"])
        if dirty:
            raise Stop("a tool file differs from HEAD (commit it first, records come from committed tools only): " + ", ".join(dirty))
        head = subprocess.run(["git", "-C", REPO, "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
        with open(a.selection, encoding="utf-8") as f:
            selection = json.load(f)
        if selection.get("carrier") != carrier_name:
            raise Stop(f"the selection file is for the carrier {selection.get('carrier')!r}, not {carrier_name!r}")
        others = sorted(os.path.basename(d) for d in glob.glob(os.path.join(HERE, "carriers", "devset-*")) if os.path.basename(d) != carrier_name)
        priors = {}
        for pfile in a.prior_selection:
            with open(pfile, encoding="utf-8") as f:
                psel = json.load(f)
            name = psel.get("carrier")
            if name not in others or name in priors:
                raise Stop(f"--prior-selection {pfile} is for {name!r}, which is not another devset carrier or is given twice")
            priors[name] = (pfile, psel)
        missing = [o for o in others if o not in priors]
        if missing:
            raise Stop("a --prior-selection is required for every other devset carrier; missing: " + ", ".join(missing))
        mat, inputs, deny = build_material({carrier_name})
        clash = {k: sorted(f for f in keyforms(k) if f in deny) for k in ALLOWLIST}
        if any(clash.values()):
            raise Stop("an allowlist key is also on the denylist: " + json.dumps({k: v for k, v in clash.items() if v}))
        controls = run_controls(mat, deny, ALLOWLIST)
        subs = subjects_of(a.carrier, selection)
        earlier = Material()
        prior_rows = []
        for name, (pfile, psel) in sorted(priors.items()):
            pcar = os.path.join(HERE, "carriers", name)
            for ps in subjects_of(pcar, psel):
                if ps.kind != "ambiguous":  # D4: an ambiguous task may repeat under a fresh id
                    earlier.absorb(sub_material(ps))
            prior_rows.append({"carrier": name, "manifest_sha256": sha_file(os.path.join(pcar, "manifest.draft.json")), "carrier_tree_sha256": Tree(root=pcar).digest(),
                               "selection": rel(pfile), "selection_sha256": sha_file(pfile)})
        rows, ignored = [], []
        for s in subs:
            verdict, rule, matched, score, ign = check_one(s, mat, deny, ALLOWLIST, earlier)
            ignored += ign
            rows.append({"id": s.case_id, "source": s.key, "item": s.item, "kind": s.kind, "verdict": verdict, "rule": rule,
                         "matched_input": matched, "score": score})
            if verdict == "keep":  # a later case is compared with the kept earlier ones
                earlier.absorb(sub_material(s))
        report = {"schema": "tbound-f36-dev-overlap/v1", "shell_guard": guard, "tool": {"tool_repo_head": head, "tool_blobs": state},
                  "tool_sha256": sha_file(__file__),
                  "carrier": {"name": carrier_name, "manifest_sha256": sha_file(os.path.join(a.carrier, "manifest.draft.json")), "carrier_tree_sha256": Tree(root=a.carrier).digest(),
                              "selection": rel(a.selection), "selection_sha256": sha_file(a.selection)},
                  "prior_carriers": prior_rows, "inputs": inputs,
                  "allowlist": {k: {"revision": v[0], "config": v[1]} for k, v in ALLOWLIST.items()},
                  "denylist": sorted(set(deny.values())), "denylist_clash_with_allowlist": clash,
                  "exercism_in_denylist": sorted(v for f, v in deny.items() if "exercism" in f or "exercism" in v.casefold()),
                  "controls": controls, "cases": rows, "ignored_files": ignored,
                  "rules": {"min_file_bytes_normalized": MIN_FILE, "shingle_words": SHINGLE, "shingle_characters_for_non_ascii_text": CHARSH,
                            "non_ascii_share": NONASCII, "containment_threshold": CONTAIN, "containment_divides_by": "the smaller shingle set"}}
        text = json.dumps(report, indent=1, ensure_ascii=False) + "\n"
        for bad in (REPO, REPO.replace("\\", "/"), json.dumps(REPO)[1:-1]):
            if bad in text:
                raise Stop("the report holds a local absolute path")
    except Exception as e:  # any crash (also SyntaxError from the guard's ast.parse, StopIteration, AssertionError) is 2, never 1
        print(f"dev_overlap: stopped: {type(e).__name__}: {e}", file=sys.stderr)
        return 2  # 1 is reserved for "a case was dropped"
    with open(a.out, "w", encoding="utf-8", newline="\n") as f:
        f.write(text)
    dropped = [r for r in rows if r["verdict"] == "drop"]
    print(json.dumps({"cases": len(rows), "kept": len(rows) - len(dropped), "dropped": [(r["id"], r["rule"], r["matched_input"]) for r in dropped],
                      "controls_ok": all(c["ok"] for c in controls)}, indent=1))
    return 1 if dropped else 0


if __name__ == "__main__":
    sys.exit(main())
