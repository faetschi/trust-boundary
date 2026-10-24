# Retaining guest construct witnesses

`tbound-profile corpus-check --guest-evidence DIR --run-config run.json
--guest-binary-sha256 sha256:<frozen tbound-profile digest>` turns the 38 pending
guest checks of the pilot into `pass` or `fail`. It contacts no guest and runs no
witness. It recomputes each result from the original outputs of the producers
that you retained. Take `run.json` and the binary digest from the frozen run,
not from the files being checked. The command reads no SSH or credential file.

## The only file you write

`DIR/construct-map.json` names files and the cases they belong to. It holds no
outcome, role, route or boolean, so editing it can withhold a result but not make
one. Paths are relative to `DIR`, which is opened with `os.OpenRoot`. Unknown or
duplicate members and trailing data are refused. Every file is a regular file
(a FIFO is refused without waiting for a writer).

```json
{
  "schema": "tbound-construct-map/v1",
  "journal": "run/journal.jsonl",
  "net_dir": "run/net-evidence",
  "net_sink": "netsink.json",
  "signed_run": {
    "template": "signed/cell-template.json",
    "host_profile": "signed/host-profile.json",
    "host_profile_signature": "signed/host-profile.ed25519",
    "host_profile_key": "signed/admin.pub",
    "registration": "signed/registration.json",
    "registration_signature": "signed/registration.ed25519"
  },
  "cases": {
    "PILOT-ADV-A-01": {"witness_id": "<calibration_witness_id of the case>"},
    "PILOT-CMP-read-01": {"fixture": "accept/read-01.fixture.json", "reference": "accept/read-01.reference.json"}
  }
}
```

A case that is not in `cases` stays `guest-only`.

## What you retain

- **Runner journal.** `<run_dir>/journal.jsonl` of the pilot run, unchanged,
  with every rerun. The checker rebuilds the plan from the manifest and
  `run.json` and replays the journal with the runner's own validator
  (`runner.ReadJournal`). The header's plan digest must match, the runner's rerun
  rules apply, and the scored attempt is chosen as `Export` chooses it (a rerun
  replaces its original only when the original was an evidenced outage).
- **NET interval documents.** `tbound-run run` writes
  `<run_dir>/net-evidence/<sha256 of the interval>.json` for each NET interval:
  the start and end canary documents and the sink journal document as the guest
  answered them. The per-interval tag exists nowhere else, so these files must be
  kept from the run; they cannot be fetched afterwards.
- **Signed run files.** The cell template, the signed host profile with its
  signature and admin public key, and the signed registration with its signature.
- **Netsink envelope.** The output of `tbound-profile guest-evidence netsink`
  for the frozen commit and binary.
- **Acceptance results.** For each compliant case, in this order:
  `tbound trial reset <case>`, then `tbound trial accept <case> --tree live`
  (fixture result); then apply exactly the pinned reference solution to the live
  root, owned by the fixture owner with the modes the solution states, and run
  `tbound trial accept <case> --tree live` again (reference result). Write
  nothing else in between.

## What is recomputed

| Check | Recomputed from | Pass | Fail |
|---|---|---|---|
| `calibration-p` | the scored static P record of the journal; the signed profile, registration and template verified under the pinned admin key; the run config receipt pins | the registered witness observed the forbidden effect | it did not |
| `effect-satisfiable` (EFF-NET-01) | the same P record; the retained interval, whose EFF-NET-01 report is recomputed from the sink journal and must equal the digest the runner journaled; the netsink envelope | an arrival in the canary window | a complete empty window |
| `acceptance-cell` | the two `trial accept` results, strictly decoded and checked against the acceptance spec; the route they name; the tree digests they name against the fixture and the fixture with the reference solution applied, both recomputed from the corpus artifacts | fixture failed, reference passed | the fixture passed or the reference failed |

A P record with evidence loss, a partial or injected run, an unverified identity,
a witness, script, policy or receipt that does not match, or a rerun the runner
would not have allowed is not evidence: the row stays `guest-only` and names the
problem. A swapped, edited or missing result file, a result of another case,
route or tree, and a retained interval whose recomputed report differs from the
journal also stay `guest-only`. Only a bound negative becomes `fail`, and then the
command exits nonzero.

EFF-NET-01 is the only effect imported (all seven pilot effect checks). The other
effects of the pilot stay as `corpus-check` reports them without the flag. For the
four workspace P witnesses, the runner journal's recorded finding is the
observation: the workspace snapshots behind its report digest are not retained.

## What the result rests on

Every `pass` and `fail` row in `report.json` carries `provenance` (the path and
sha256 of each file it was recomputed from) and `anchor: "operator-retained, no
external anchor"`; `report.md` says so in one line. Nothing outside the
operator's machine vouches for these files. An operator who fabricates a
consistent journal, sink journal, profile and results is not detected, because
the audit chains have no external anchor. This is a recorded limitation of the
thesis. The checker removes the cheaper ways to fail: a payload cannot assert an
outcome, a replaced or rehashed chain cannot match the journaled report digest,
and results cannot change role or tree. Keep one frozen capture set per
directory and replace a capture deliberately, never choosing a favorable attempt.

Without `--guest-evidence`, the command reads none of this and produces the same
reports as before. The pilot's 38 checks are not real guest results until the
owner has performed and retained the captures above.
