"""F36 development compliant `read` cases from CRUXEval (facebookresearch/cruxeval, MIT), output prediction.

    py -3 -I corpus/sampling/dev_src_cruxeval.py --src CLONE --index 1|2 --suffix .ds1 --tbound-run EXE --skips FILE
        --out DIR --record FILE

Each item becomes a read task: the item's code and its own `assert f(<input>) == ??` line are the file src/crux-N.py;
the agent reads it, works out the value and writes it to out/crux-N.txt (Python repr text plus LF, the dataset `output`
field). The prompt does not carry the input or the output (leak-prompt).

FILTER (fixed here, before any selection ran): input and output are non-empty single-line text without a control
  character; the dataset output, trimmed, does not occur in the rendered prompt (leak-prompt), checked at selection.
LONGEST-CODE BUCKET (rule fixed before selection): the 40 items (5% of 800) with the longest `code` in characters, ties
  by id. Each carrier takes 1 item of that bucket and 4 of the other 760, each in its own seeded order
  (purposes cruxeval/longest and cruxeval/rest). Index 2 takes the items after the ones index 1 took.
"""
import argparse
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import dev_src as D  # noqa: E402

PIN_KEY = "facebookresearch/cruxeval"
REPO_URL = "https://github.com/facebookresearch/cruxeval"
REPO_REV = "190faf16d175b5847b0af05d937872b1fb395942"
DATA = "data/cruxeval.jsonl"
LONGEST = 40
SLOTS = [("longest", 1), ("rest", 4)]


def prompt_for(n):
    return (f"Use the read tool on src/crux-{n}.py. It defines a Python function f and ends with an assertion whose right-hand "
            f"side is `??`. Work out the value that makes the assertion true. Use the write tool to create out/crux-{n}.txt "
            "containing exactly that value as Python's repr would print it, followed by a newline, then reply done. "
            "Do not change anything else.")


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    for flag in ("src", "index", "suffix", "tbound-run", "skips", "out", "record"):
        ap.add_argument("--" + flag, required=flag != "skips")
    a = ap.parse_args()
    index = int(a.index)
    assert D.git(a.src, "rev-parse", "HEAD").decode().strip() == REPO_REV, "the clone is not at the pinned commit"
    raw = D.git(a.src, "cat-file", "blob", f"{REPO_REV}:{DATA}")
    lic = D.git(a.src, "cat-file", "blob", f"{REPO_REV}:LICENSE")
    items = {}
    for line in raw.decode("utf-8").splitlines():
        r = json.loads(line)
        items[r["id"]] = r
    pin = {"rev": REPO_REV, "license": "MIT", "digests": ["sha256:" + D.sha256_hex(raw)], "repository": REPO_URL,
           "license_files": ["licenses/cruxeval-LICENSE"]}
    by_len = sorted(items, key=lambda i: (-len(items[i]["code"]), i))
    longest = by_len[:LONGEST]
    rest = [i for i in items if i not in set(longest)]
    ext = D.load_skips(a.skips)
    rec = {"source": "CRUXEval", "pin_key": PIN_KEY, "pin": {k: pin[k] for k in ("rev", "license", "digests", "repository")},
           "tool": "corpus/sampling/dev_src_cruxeval.py", "tool_sha256": D.self_sha(__file__), "tool_repo_head": D.repo_head(),
           "domain": D.DOMAIN, "seed": D.SEED, "index": index, "items": len(items), "longest_bucket": len(longest),
           "longest_min_code_chars": len(items[longest[-1]]["code"]), "cases": [], "order": {}, "skips": []}
    os.makedirs(a.out, exist_ok=True)
    counter = 0

    def reject_for(_):
        def reject(i):
            r = items[i]
            for field in ("input", "output"):
                if not r[field].strip() or "\n" in r[field] or any(ord(c) < 32 for c in r[field]):
                    return f"filter: {field} is empty, multi-line or has a control character"
            if any(r["output"].strip() in prompt_for(m) for m in range(1, 6)):
                return "leak-prompt: the trimmed output occurs in the prompt"
            return None
        return reject

    for purpose, ids, per in (("cruxeval/longest", longest, 1), ("cruxeval/rest", rest, 4)):
        taken = []
        for k in range(1, per + 1):
            counter += 1
            n = counter
            take_n = (index - 1) * per + k
            chosen, head, seen = D.take(purpose, ids, take_n, ext, reject_for(n))
            rec["order"][purpose] = head
            rec["skips"] += [dict(s, purpose=purpose, slot=n) for s in seen if not any(s["id"] == t["id"] and t.get("slot") == n for t in rec["skips"])]
            iid = chosen[take_n - 1]
            r = items[iid]
            cid = f"f36dev:cruxeval:{iid.replace('_', '-')}{a.suffix}"
            path, out_path = f"src/crux-{n}.py", f"out/crux-{n}.txt"
            code = D.lf(r["code"]).rstrip("\n") + f"\nassert f({r['input']}) == ??\n"
            answer = r["output"] + "\n"
            art = D.PB.Artifacts(a.tbound_run)
            transforms = [
                f"Materialized the item's code and its own output-prediction assertion `assert f(<input>) == ??` as {path} (the CRUXEval-O prompt form).",
                f"The answer is written to {out_path} as the dataset `output` text plus one LF, instead of being completed in a reply; "
                "the prompt names neither the input nor the output.",
            ]
            lost = ["Execution-based equality of the Python value is replaced by an exact match of its repr text.",
                    "CRUXEval's few-shot and chain-of-thought prompt templates are not used."]
            case = D.cmp_case(
                cid, "read", PIN_KEY, pin, iid, art, {path: code}, ["out"], prompt_for(n),
                f"Read {path}, predict the output of f on the asserted input and record it in {out_path}.",
                "Primary tool read inspects the function; one write records the predicted output.",
                [D.PB.c_sha(out_path, answer), D.PB.c_only(out_path)], [D.PB.s_write(out_path, answer)],
                f"{out_path} is exactly the predicted output text plus a newline; nothing else changed",
                "Predict the output of a Python function on a given input (the CRUXEval output-prediction task).",
                "one CRUXEval output-prediction item", transforms, lost, "taskaccept exact bytes of the recorded out/ file")
            bdir = os.path.join(a.out, cid.replace(":", "_"))
            D.write_bundle(bdir, case, art, PIN_KEY, pin, {"cruxeval-LICENSE": lic}, {})
            rec["cases"].append({"case_id": cid, "kind": "compliant", "role": "read", "source": PIN_KEY, "item": iid,
                                 "bucket": purpose.split("/")[1], "code_chars": len(r["code"]), "bundle": os.path.basename(bdir),
                                 "shared_files": []})
    D.record(a.record, rec)
    print(json.dumps({"cases": [c["case_id"] for c in rec["cases"]]}, indent=1))


if __name__ == "__main__":
    main()
