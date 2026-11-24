"""C8: select the 100 confirmatory compliant items and embed them in corpus/third/compliant/sources.json.

Data only: reads the pinned RepoQA release and the MultiPL-E HumanEval-JS row export, runs nothing.
Usage (cwd = F:\\trust-boundary\\.local\\source-screen):
    py -3 -I scripts\\c8-extract.py ..\\worktrees\\corpus-20261107

Selection (fixed before any item was looked at for its content):
  key(kind, id) = SHA-256("tbound-c8-compliant/v1|" + kind + "|" + id), ascending; take the first N.
  read: RepoQA typescript-bucket needles not used by the pilot whose file is at most 16,000 bytes, N = 25.
  write/edit/bash: MultiPL-E HumanEval-JS items not used by the pilot, N = 75; ranks 1-25 write,
  26-50 edit, 51-75 bash.
Solutions, seeded edit bugs and bash stdout are added later by c8-author.py and verified with node.
"""
import gzip
import hashlib
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REPOQA_GZ = os.path.join(ROOT, "fetch-c8", "repoqa", "repoqa-2024-06-23.json.gz")
HE_DIR = os.path.join(ROOT, "..", "archive", "source-screen-own", "MultiPL-E-hf")
REPOQA_SHA = "c050a2ad90a7df89d9dc1f1c3b3b20683edd20a56293b35fcaae43dec115d681"
HE_PARQUET_SHA = "037debd61e3e98ee9da684054ef522d45200ce622ae36b2fab3bbb27769fa51d"
DOMAIN = "tbound-c8-compliant/v1|"
NEEDLE_FILE_LIMIT = 16000
N_READ, N_HE = 25, 75
# Items that cannot be built; each is replaced by the next item in the order, in the same role.
# Items that fail a mechanical admission check, found by running the check over every selected item
# (C8 review 2026-11-08); each is replaced like an unbuildable item. Write and edit prompts show the spec,
# so a spec example that contradicts the dataset tests disqualifies them; bash prompts show no spec.
INADMISSIBLE = {
    "openai/openai-node:src/lib/ChatCompletionStream.ts:str": "leak-prompt: the witness name 'str' occurs in the prompt (inside 'string')",
    "HumanEval_47_median": "spec example median([-10, 4, 6, 1000, 10, 20]) gives 15.0; the dataset tests require 8",
    "HumanEval_116_sort_array": "spec examples contradict the stated rule and the dataset tests (negative inputs, ordering)",
    "HumanEval_148_bf": "spec examples return a string; the dataset tests require an array",
    "HumanEval_149_sorted_list_sum": "spec examples call list_sort, not the function sorted_list_sum",
}
UNBUILDABLE = {"HumanEval_51_remove_vowels": "the dataset tests do not parse: raw newlines inside a string literal "
                                             "(node SyntaxError before any assertion runs)"}


def sha_file(path):
    return hashlib.sha256(open(path, "rb").read()).hexdigest()


def key(kind, item_id):
    return hashlib.sha256((DOMAIN + kind + "|" + item_id).encode("utf-8")).hexdigest()


def main(worktree):
    assert sha_file(REPOQA_GZ) == REPOQA_SHA, "RepoQA release digest"
    assert sha_file(os.path.join(HE_DIR, "humaneval-js.parquet")) == HE_PARQUET_SHA, "MultiPL-E parquet digest"
    pilot = json.load(open(os.path.join(worktree, "corpus", "pilot", "sources.json"), encoding="utf-8"))

    repoqa = json.load(gzip.open(REPOQA_GZ, "rt", encoding="utf-8"))["typescript"]
    used = {f"{x['repo']}:{x['path']}:{x['name']}" for x in pilot["needles"]}
    needles = {}
    for repo in repoqa:
        for n in repo["needles"]:
            nid = f"{repo['repo']}:{n['path']}:{n['name']}"
            needles[nid] = dict(repo=repo["repo"], commit=repo["commit_sha"], path=n["path"], name=n["name"],
                                start_line=int(n["start_line"]), end_line=int(n["end_line"]),
                                desc=n["description"], content=repo["content"][n["path"]])
    assert len(needles) == 100 and used <= set(needles)
    eligible = sorted((nid for nid in needles if nid not in used and len(needles[nid]["content"]) <= NEEDLE_FILE_LIMIT),
                      key=lambda nid: key("read", nid))
    spare = iter(nid for nid in eligible[N_READ:] if nid not in INADMISSIBLE)
    read = [nid if nid not in INADMISSIBLE else next(spare) for nid in eligible[:N_READ]]

    rows = []
    for name in sorted(os.listdir(HE_DIR)):
        if name.startswith("rows-humaneval-js-"):
            rows += [r["row"] for r in json.load(open(os.path.join(HE_DIR, name), encoding="utf-8"))["rows"]]
    he = {r["name"]: r for r in rows}
    assert len(he) == 161 and set(pilot["humaneval"]) <= set(he)
    free = sorted((name for name in he if name not in pilot["humaneval"]), key=lambda name: key("humaneval-js", name))
    roles = ["write"] * 25 + ["edit"] * 25 + ["bash"] * 25
    skip = {**UNBUILDABLE, **INADMISSIBLE}
    reserve = iter(name for name in free[N_HE:] if name not in skip)
    slots = [name if name not in skip else next(reserve) for name in free[:N_HE]]
    chosen = dict(zip(slots, roles))
    for name in chosen:
        tests = he[name]["tests"]
        assert "require('node:assert')" in tests and "require(" not in tests.replace("require('node:assert')", ""), name

    out = {
        "pins": {
            "evalplus/repoqa": {"rev": "ae876deb1365dbf5a15b0533723c8ed123eee586", "release": "2024-06-23",
                                "url": "https://github.com/evalplus/repoqa_release/releases/download/2024-06-23/repoqa-2024-06-23.json.gz",
                                "sha256": REPOQA_SHA, "license": "Apache-2.0 (code); upstream repo licenses apply to snippets",
                                "digests": ["sha256:" + REPOQA_SHA],
                                "license_files": ["licenses/RepoQA-LICENSE"] + sorted(
                                    "licenses/" + f for f in os.listdir(os.path.join(worktree, "corpus", "third", "compliant", "licenses"))
                                    if not f.startswith(("RepoQA", "MultiPL-E", "HumanEval")))},
            "nuprl/MultiPL-E": {"rev": "28441b6024e71d4a1c1c0f6bf171c935cd5a43f2", "config": "humaneval-js",
                                "parquet_sha256": HE_PARQUET_SHA, "license": "MIT (dataset card); repository BSD-3-Clause with a no-training clause",
                                "digests": ["sha256:" + HE_PARQUET_SHA],
                                "license_files": ["licenses/MultiPL-E-dataset-card-license.txt", "licenses/MultiPL-E-repository-LICENSE",
                                                  "licenses/HumanEval-LICENSE"],
                                "rows": "Hugging Face datasets-server rows export of the same revision; prompt and tests "
                                        "match the pilot's pinned items"},
        },
        "selection": {
            "domain": DOMAIN.rstrip("|"),
            "rule": "key = SHA-256(domain + '|' + kind + '|' + id) ascending; first N of the eligible items",
            "read": {"kind": "read", "id": "<repo>:<path>:<name>", "eligible": "typescript-bucket needles not in the pilot, "
                     f"needle file at most {NEEDLE_FILE_LIMIT} bytes", "n_eligible": len(eligible), "n": N_READ,
                     "order": eligible,
                     "inadmissible": {k: v for k, v in INADMISSIBLE.items() if k.count(":")},
                     "limit_note": "the 16,000-byte limit (about 4k tokens per read of the needle file) was set after looking at the "
                                   "aggregate file-size distribution of the 95 free needles, before looking at any item's content, "
                                   "name or rank; every limit from 15,881 to 17,686 bytes gives the same 60-item pool"},
            "humaneval": {"kind": "humaneval-js", "id": "<name>", "eligible": "items not in the pilot",
                          "n_eligible": len(free), "n": N_HE, "roles": "ranks 1-25 write, 26-50 edit, 51-75 bash",
                          "unbuildable": UNBUILDABLE,
                          "inadmissible": {k: v for k, v in INADMISSIBLE.items() if not k.count(":")},
                          "replacement": "an unbuildable item's slot goes to the next buildable item after rank 75, same role",
                          "order": free},
        },
        "needles": [needles[nid] for nid in read],
        "humaneval": {name: {"role": role, "prompt": he[name]["prompt"], "tests": he[name]["tests"]}
                      for name, role in chosen.items()},
    }
    dest = os.path.join(worktree, "corpus", "third", "compliant", "sources.json")
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    existing = json.load(open(dest, encoding="utf-8")) if os.path.exists(dest) else {}
    for name, item in out["humaneval"].items():  # keep authored fields across reruns
        for field in ("solution", "edit_bug", "bash_stdout"):
            if field in existing.get("humaneval", {}).get(name, {}):
                item[field] = existing["humaneval"][name][field]
    with open(dest, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(json.dumps(out, indent=1, ensure_ascii=False) + "\n")
    print("read eligible", len(eligible), "selected", len(read), "| humaneval free", len(free), "selected", len(chosen))


if __name__ == "__main__":
    main(sys.argv[1])
