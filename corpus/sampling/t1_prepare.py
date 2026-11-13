"""Prepare the T1 pilot commitment for publication, without publishing anything.

    py -3 -I corpus/sampling/t1_prepare.py build --repo <repo root> --tag corpus-d48-ledger-v3 --out <new dir>
    py -3 -I corpus/sampling/t1_prepare.py round --commitment <file> --t1 2026-11-12T10:00:01Z
    py -3 -I corpus/sampling/t1_prepare.py record --id <Zenodo record id> --file <the uploaded file>

`build` reads the template from the tag's commit, checks every recorded SHA-256 against the committed blob, fills
the two `signed_tag` fields with the tag's object and commit, and writes `pilot-commitment-v1.json` byte for byte
otherwise (no JSON re-serialization). It refuses an unsigned or lightweight tag, an existing output file and any
remaining FILL. `round` prints the drand round R for a T1 (the first round scheduled at least 24 h after T1). `record` reads a
published Zenodo record, checks that it holds exactly the local file (SHA-256 of the downloaded bytes), and prints
T1 (its `created` timestamp rounded up to the next whole second, D53) with R.
Stdlib only.
"""
import argparse
import datetime
import hashlib
import json
import os
import subprocess
import sys
import urllib.request

TEMPLATE = "corpus/sampling/pilot-commitment-v1.template.json"
OBJECT_FILL = '"FILL: git rev-parse corpus-d48-ledger-v3"'
COMMIT_FILL = '"FILL: git rev-parse corpus-d48-ledger-v3^{commit}"'
ROUND_DELAY = 24 * 3600


def git(repo, *args, text=True):
    done = subprocess.run(["git", "-C", repo, *args], capture_output=True, check=False)
    if done.returncode:
        sys.exit(f"git {' '.join(args)}: {done.stderr.decode(errors='replace').strip()}")
    return done.stdout.decode().strip() if text else done.stdout


def fill(template_text, tag_object, tag_commit):
    """Replace the two FILL values. Each must be present exactly once and nothing else may say FILL."""
    for fill_value in (OBJECT_FILL, COMMIT_FILL):
        if template_text.count(fill_value) != 1:
            sys.exit(f"template does not hold {fill_value} exactly once")
    out = template_text.replace(OBJECT_FILL, json.dumps(tag_object)).replace(COMMIT_FILL, json.dumps(tag_commit))
    if "FILL" in out:
        sys.exit("template still holds a FILL value")
    return out


def check_digests(commitment, blob):
    """Every <name>_sha256 with a <name>_path must equal the SHA-256 of that file at the tag's commit."""
    checked = 0
    for key, value in commitment.items():
        if key.endswith("_sha256") and not value.startswith("FILL"):
            digest = hashlib.sha256(blob(commitment[key[: -len("_sha256")] + "_path"])).hexdigest()
            if digest != value:
                sys.exit(f"{key}: template says {value}, the file at the tag's commit is {digest}")
            checked += 1
    return checked


def build(args):
    repo, tag = os.path.abspath(args.repo), args.tag
    if git(repo, "cat-file", "-t", tag) != "tag":
        sys.exit(f"{tag} is not an annotated tag")
    git(repo, "verify-tag", tag)  # exits nonzero when the signature cannot be verified
    tag_object, tag_commit = git(repo, "rev-parse", tag), git(repo, "rev-parse", f"{tag}^{{commit}}")
    blob = lambda path: git(repo, "show", f"{tag_commit}:{path}", text=False)
    text = fill(blob(TEMPLATE).decode("utf-8"), tag_object, tag_commit)
    commitment = json.loads(text)
    checked = check_digests(commitment, blob)
    if commitment["kind"] != "pilot" or commitment["pilot_size"] != 11:
        sys.exit("template is not the 11-case pilot commitment")
    os.makedirs(args.out, exist_ok=True)
    path = os.path.join(args.out, "pilot-commitment-v1.json")
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
    print(f"record {args.id}: file equals {args.file}")
    print(f"created {record['created']}")
    print(f"T1 = {t1}")
    chain = json.loads(local)["chain"]
    r, scheduled = round_of(chain, parse_utc(t1))
    print(f"R = {r}, scheduled {datetime.datetime.fromtimestamp(scheduled, datetime.timezone.utc):%Y-%m-%dT%H:%M:%SZ}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = parser.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("build")
    b.add_argument("--repo", required=True)
    b.add_argument("--tag", required=True)
    b.add_argument("--out", required=True)
    b.set_defaults(run=build)
    r = sub.add_parser("round")
    r.add_argument("--commitment", required=True)
    r.add_argument("--t1", required=True)
    r.set_defaults(run=show_round)
    z = sub.add_parser("record")
    z.add_argument("--id", required=True)
    z.add_argument("--file", required=True)
    z.add_argument("--host", default="zenodo.org", help="sandbox.zenodo.org for a rehearsal")
    z.set_defaults(run=show_record)
    args = parser.parse_args(argv)
    args.run(args)


if __name__ == "__main__":
    main()
