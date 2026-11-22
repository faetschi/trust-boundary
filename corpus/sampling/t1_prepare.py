"""Prepare the T1 pilot or the T2 main commitment for publication, without publishing anything.

    py -3 -I corpus/sampling/t1_prepare.py build --repo <repo root> --tag h1-sampling-frame --out <new dir>
    py -3 -I corpus/sampling/t1_prepare.py build --kind main --repo <repo root> --tag <T2 tag> --out <new dir> \
        --policy-file <policy file> --dev-ids <dev_ids.py output> [--rule-path corpus/sampling/adaptation-rule-v2.md]
    py -3 -I corpus/sampling/t1_prepare.py round --commitment <file> --t1 2026-11-12T10:00:01Z
    py -3 -I corpus/sampling/t1_prepare.py record --id <Zenodo record id> --file <the uploaded file>

`build` reads the template from the tag's commit, checks every recorded SHA-256 against the committed blob, fills
the two `signed_tag` fields with the tag's object and commit, and writes `pilot-commitment-v1.json` byte for byte
otherwise (no JSON re-serialization). With `--kind main` it reads `main-commitment-v1.template.json`, also fills the
tag name, `adaptation_rule_sha256` (the SHA-256 of `--rule-path` at the tag's commit; the template's path when
omitted), `policy_sha256` (the SHA-256 of the local `--policy-file`) and `development_case_ids` (the JSON list in
`--dev-ids`), checks the result with select.py's own commitment parser, and writes `main-commitment-v1.json`.
It refuses an unsigned or lightweight tag, an existing output file and any
remaining FILL. `round` prints the drand round R for a T1 (the first round scheduled at least 24 h after T1). `record` reads a
published Zenodo record, checks that it holds exactly the local file (SHA-256 of the downloaded bytes), and prints
T1, or T2 for a main commitment (its `created` timestamp rounded up to the next whole second, D53), with R.
Stdlib only.
"""
import argparse
import datetime
import hashlib
import importlib.util
import json
import os
import subprocess
import sys
import urllib.request

TEMPLATE = "corpus/sampling/pilot-commitment-v1.template.json"
MAIN_TEMPLATE = "corpus/sampling/main-commitment-v1.template.json"
MAIN_FILLS = {  # the FILL values of the main template, by what fills them
    "name": '"FILL: the signed tag name (G18)"',
    "object": '"FILL: git rev-parse <tag>"',
    "commit": '"FILL: git rev-parse <tag>^{commit}"',
    "rule": '"FILL: sha256 of the rule file named by adaptation_rule_path at the tag\'s commit (v1 or v2, G19)"',
    "policy": '"FILL: sha256 of the evaluated policy file (G17)"',
    "dev_ids": '"FILL: sorted list built by dev_ids.py"',
}
OBJECT_FILL = '"FILL: git rev-parse h1-sampling-frame"'
COMMIT_FILL = '"FILL: git rev-parse h1-sampling-frame^{commit}"'
ROUND_DELAY = 24 * 3600


def git(repo, *args, text=True):
    done = subprocess.run(["git", "-C", repo, *args], capture_output=True, check=False)
    if done.returncode:
        sys.exit(f"git {' '.join(args)}: {done.stderr.decode(errors='replace').strip()}")
    return done.stdout.decode().strip() if text else done.stdout


def fill_values(template_text, replacements):
    """Replace each FILL value (key) with its JSON text (value). Each must be present exactly once and nothing
    else may say FILL."""
    out = template_text
    for fill_value, replacement in replacements.items():
        if template_text.count(fill_value) != 1:
            sys.exit(f"template does not hold {fill_value} exactly once")
        out = out.replace(fill_value, replacement)
    if "FILL" in out:
        sys.exit("template still holds a FILL value")
    return out


def fill(template_text, tag_object, tag_commit):
    return fill_values(template_text, {OBJECT_FILL: json.dumps(tag_object), COMMIT_FILL: json.dumps(tag_commit)})


def check_digests(commitment, blob):
    """Every <name>_sha256 with a <name>_path must equal the SHA-256 of that file at the tag's commit."""
    checked = 0
    for key, value in commitment.items():
        if key.endswith("_sha256") and key != "policy_sha256" and key[: -len("_sha256")] + "_path" not in commitment:
            sys.exit(f"{key}: no {key[: -len('_sha256')]}_path names its file, so it cannot be checked")
        if key.endswith("_sha256") and not value.startswith("FILL") and key[: -len("_sha256")] + "_path" in commitment:
            digest = hashlib.sha256(blob(commitment[key[: -len("_sha256")] + "_path"])).hexdigest()
            if digest != value:
                sys.exit(f"{key}: template says {value}, the file at the tag's commit is {digest}")
            checked += 1
    return checked


def select_module():
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "select.py")
    spec = importlib.util.spec_from_file_location("select_for_prepare", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def read_file(path):
    with open(path, "rb") as fh:
        return fh.read()


def fill_main(blob, args, tag, tag_object, tag_commit):
    """The main template with the tag, the rule digest (of the file at the tag's commit), the policy digest
    (of the local file) and the development list filled."""
    text = blob(MAIN_TEMPLATE).decode("utf-8")
    rule_path = json.loads(text)["adaptation_rule_path"]
    if args.rule_path and args.rule_path != rule_path:
        old = f'"adaptation_rule_path": {json.dumps(rule_path)}'
        if text.count(old) != 1:
            sys.exit("template does not hold adaptation_rule_path exactly once")
        text, rule_path = text.replace(old, f'"adaptation_rule_path": {json.dumps(args.rule_path)}'), args.rule_path
    ids = json.loads(read_file(args.dev_ids).decode("utf-8"))
    if not isinstance(ids, list) or not ids:
        sys.exit("--dev-ids must hold a nonempty JSON list of case ids")
    listing = json.dumps(sorted(ids), indent=1).replace("\n", "\n ")  # nested one level, as the template is written
    return fill_values(text, {
        MAIN_FILLS["name"]: json.dumps(tag),
        MAIN_FILLS["object"]: json.dumps(tag_object),
        MAIN_FILLS["commit"]: json.dumps(tag_commit),
        MAIN_FILLS["rule"]: json.dumps(hashlib.sha256(blob(rule_path)).hexdigest()),
        MAIN_FILLS["policy"]: json.dumps(hashlib.sha256(read_file(args.policy_file)).hexdigest()),
        MAIN_FILLS["dev_ids"]: listing,
    })


def build(args):
    repo, tag = os.path.abspath(args.repo), args.tag
    if git(repo, "cat-file", "-t", tag) != "tag":
        sys.exit(f"{tag} is not an annotated tag")
    git(repo, "verify-tag", tag)  # exits nonzero when the signature cannot be verified
    tag_object, tag_commit = git(repo, "rev-parse", tag), git(repo, "rev-parse", f"{tag}^{{commit}}")
    blob = lambda path: git(repo, "show", f"{tag_commit}:{path}", text=False)
    kind = getattr(args, "kind", "pilot")
    if kind == "main":
        text = fill_main(blob, args, tag, tag_object, tag_commit)
    else:
        text = fill(blob(TEMPLATE).decode("utf-8"), tag_object, tag_commit)
    commitment = json.loads(text)
    if commitment["signed_tag"]["name"] != tag:  # the published file must name the tag it was built from
        sys.exit(f"--tag {tag} is not the tag the template names ({commitment['signed_tag']['name']})")
    checked = check_digests(commitment, blob)
    if kind == "main":
        if commitment["kind"] != "main":
            sys.exit("template is not the main commitment")
        module = select_module()
        try:
            module.parse_commitment(text.encode("utf-8"))  # select.py's own validation, as `draw` runs it
        except module.Refused as exc:
            sys.exit(f"select.py refuses the filled commitment: {exc}")
    elif commitment["kind"] != "pilot" or commitment["pilot_size"] != 11:
        sys.exit("template is not the 11-case pilot commitment")
    os.makedirs(args.out, exist_ok=True)
    path = os.path.join(args.out, f"{kind}-commitment-v1.json")
    with open(path, "xb") as fh:  # "x": never overwrite
        fh.write(text.encode("utf-8"))
    print(f"wrote {path}\ntag object {tag_object}\ntag commit {tag_commit}\n{checked} file digests checked")
    print("sha256", hashlib.sha256(text.encode("utf-8")).hexdigest())


def parse_utc(value):
    if not value.endswith("Z") or "." in value:
        sys.exit("T1 must be whole-second UTC ending in Z (round fractions up)")
    return int(datetime.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc).timestamp())


def round_of(chain, t1_epoch):
    """The first round R with genesis + (R - 1) * period >= T1 + 24 h."""
    genesis, period = chain["genesis_time"], chain["period"]
    delta = t1_epoch + ROUND_DELAY - genesis
    r = (0 if delta <= 0 else -(-delta // period)) + 1
    return r, genesis + (r - 1) * period


def show_round(args):
    with open(args.commitment, encoding="utf-8") as fh:
        chain = json.load(fh)["chain"]
    r, scheduled = round_of(chain, parse_utc(args.t1))
    when = datetime.datetime.fromtimestamp(scheduled, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    print(f"R = {r}, scheduled {when}\nbeacon: https://api.drand.sh/{chain['hash']}/public/{r}")


def created_to_t1(created):
    """Zenodo `created` (ISO with fractions) -> whole-second UTC ending in Z, rounded up (D53)."""
    moment = datetime.datetime.fromisoformat(created).astimezone(datetime.timezone.utc)
    if moment.microsecond:
        moment = moment.replace(microsecond=0) + datetime.timedelta(seconds=1)
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


def check_record(record, content, local_bytes):
    files = record.get("files", [])
    if len(files) != 1:
        sys.exit(f"the record holds {len(files)} files, expected exactly one")
    if hashlib.sha256(content).hexdigest() != hashlib.sha256(local_bytes).hexdigest():
        sys.exit("the file in the record differs from the local file")
    return created_to_t1(record["created"])


def show_record(args):
    url = f"https://{args.host}/api/records/{args.id}"
    get = lambda u: urllib.request.urlopen(u, timeout=60).read()
    record = json.loads(get(url))
    with open(args.file, "rb") as fh:
        local = fh.read()
    t1 = check_record(record, get(record["files"][0]["links"]["self"]), local)
    commitment = json.loads(local)
    label = "T1" if commitment.get("kind") == "pilot" else "T2"
    print(f"record {args.id}: file equals {args.file}")
    print(f"created {record['created']}")
    print(f"updated {record.get('updated')}")  # read right after publishing, this is about the publish time
    print(f"{label} = {t1}")
    chain = commitment["chain"]
    r, scheduled = round_of(chain, parse_utc(t1))
    print(f"R = {r}, scheduled {datetime.datetime.fromtimestamp(scheduled, datetime.timezone.utc):%Y-%m-%dT%H:%M:%SZ}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = parser.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("build")
    b.add_argument("--repo", required=True)
    b.add_argument("--tag", required=True)
    b.add_argument("--out", required=True)
    b.add_argument("--kind", choices=("pilot", "main"), default="pilot")
    b.add_argument("--policy-file", help="main only: the local file whose SHA-256 is policy_sha256 (G17)")
    b.add_argument("--dev-ids", help="main only: the dev_ids.py output")
    b.add_argument("--rule-path", help="main only: the rule file at the tag (default: the template's path)")
    b.set_defaults(run=build)
    r = sub.add_parser("round")
    r.add_argument("--commitment", required=True)
    r.add_argument("--t1", "--t2", dest="t1", required=True)
    r.set_defaults(run=show_round)
    z = sub.add_parser("record")
    z.add_argument("--id", required=True)
    z.add_argument("--file", required=True)
    z.add_argument("--host", default="zenodo.org", help="sandbox.zenodo.org for a rehearsal")
    z.set_defaults(run=show_record)
    args = parser.parse_args(argv)
    if args.run is build and args.kind == "main" and not (args.policy_file and args.dev_ids):
        parser.error("--kind main needs --policy-file and --dev-ids")
    args.run(args)


if __name__ == "__main__":
    main()
