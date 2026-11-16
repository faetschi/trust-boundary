# Retaining guest construct witnesses

`tbound-profile corpus-check --guest-evidence DIR --run-config run.json
--guest-binary-sha256 sha256:<frozen tbound-profile digest>` turns the 38 pending
guest checks of the pilot into `pass` or `fail`. It contacts no guest and runs no
witness. It recomputes each result from the original outputs of the producers
that you retained. Take `run.json` and the binary digest from the frozen run,
not from the files being checked. The command reads no SSH or credential file.

## Collecting the witnesses before registration

Before registration a corpus may collect only what `corpus-check` requires (D41).
To collect the witnesses, set `"purpose": "construct-witness"` in `run.json`:

```json
"purpose": "construct-witness"
```

The plan is then one static P replay per admitted adversarial case and nothing else:
no trajectory, adaptive replay, C, B or V run, and no variance repeat. `tbound-run plan`
shows `subset true` and that many units, and the guest sees Configuration P only.

A witness run is not a study result. `tbound-run export` and `report` refuse it, and
the study plan cannot resume its journal, because the purpose is part of the plan digest.
With `--quota-profile thesis`, `corpus-check` imports a journal only from a witness run.
`tbound-profile preregistration-record` refuses it.

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
  "ws_dir": "run/ws-evidence",
  "proc_dir": "run/proc-evidence",
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
- **Workspace interval inputs.** `tbound-run run` writes
  `<run_dir>/ws-evidence/<sha256 of the interval>.json` with the complete
  `wsoracle.EvaluateE04Input` actually evaluated, including the baseline, six
  observation points, approved ledger, scope, threat class and host protected-path
  adjustments. Retention is bounded to 256 MiB per interval and fails closed if
  it cannot be written. Keep this directory with the journal; the four workspace
  P rows require a recomputed report digest and finding equal to the journal.
- **Process interval inputs.** `tbound-run run` writes
  `<run_dir>/proc-evidence/<sha256 of the interval>.proc-interval.json` with
  everything the PROC oracle evaluated (registration, snapshots, monitor polls
  and policy), beside the interval's termination record `<sha256 of the
  interval>.proc-closure.json`. A `no-reap/v1` run cannot be reconstructed, so
  these files and the journal are the whole basis of an `EFF-PROC-01` or
  `EFF-PROC-03` P row. The checker recomputes the report, binds it to the
  termination record and requires the journaled reference and all three PROC
  findings. It also requires the run config's cell uid and gid, teardown grace
  and authorized sockets, `lingering_init` exactly for a `no-reap/v1` case,
  every capture under the registration, and cell-identity flags that agree
  with the user-namespace chains of the same capture. `EFF-PROC-02` is untested
  (D48); its P row is never imported.
- **Signed run files.** The cell template, the signed host profile with its
  signature and admin public key, and the signed registration with its signature.
- **Netsink envelope.** The output of `tbound-profile guest-evidence netsink`
  for the frozen commit and binary.
- **Acceptance results.** For each compliant case, in this order:
  `tbound trial reset <case>`, then `tbound trial accept <case> --tree live --commit <freeze_commit>`
  (fixture result); then apply exactly the pinned reference solution to the live
  root, owned by the fixture owner with the modes the solution states, and run
  `tbound trial accept <case> --tree live --commit <freeze_commit>` again (reference result). Write
  nothing else in between. Results record `commit`, `binary_sha256` (the actual
  `tbound` executable), `image_digest` (the signed acceptance-cell image) and
  `manifest_digest` (the canonical pinned corpus manifest). The commit label
  follows `guest-evidence --commit`; it is checked against `run.json`, rather than
  adding a new trust mechanism. The binary must match `pins.runner_digest`, not
  `--guest-binary-sha256`, which pins the separate `tbound-profile` executable.
  Old results without these fields are refused by this importer. The runner
  passes its `freeze_commit` automatically for ordinary compliant trials.

  Acceptance evaluates a private in-memory copy (at most 8 MiB per file and
  256 MiB total). The tree digest is computed from that exact copy's bytes and
  metadata. Before/after live snapshots remain an additional consistency check;
  restoring a live tree cannot relabel the evaluated copy's digest.

## What is recomputed

| Check | Recomputed from | Pass | Fail |
|---|---|---|---|
| `calibration-p` | the scored static P record of the journal; the signed profile, registration and template verified under the pinned admin key; the run config receipt pins; retained NET or WS inputs whose recomputed report digest and finding match the journal | the registered witness observed the forbidden effect | it did not |
| `effect-satisfiable` (EFF-NET-01) | the same P record; the retained interval, whose EFF-NET-01 report is recomputed from the sink journal and must equal the digest the runner journaled; the netsink envelope | an arrival in the canary window | a complete empty window |
| `acceptance-cell` | the two `trial accept` results, strictly decoded and checked against the acceptance spec; their commit, tbound binary, image and manifest identity against the frozen run; the aggregate passed against every per-check passed; the route they name; the tree digests they name against the fixture and the fixture with the reference solution applied, both recomputed from the corpus artifacts | fixture failed, reference passed | the fixture passed or the reference failed |

A P record with evidence loss, a partial or injected run, an unverified identity,
a witness, script, policy or receipt that does not match, or a rerun the runner
would not have allowed is not evidence: the row stays `guest-only` and names the
problem. A swapped, edited or missing result file, a result of another case,
route or tree, and a retained interval whose recomputed report differs from the
journal also stay `guest-only`. Only a bound negative becomes `fail`, and then the
command exits nonzero.

EFF-NET-01 is the only effect imported (all seven pilot effect checks). The other
effects of the pilot stay as `corpus-check` reports them without the flag. The four workspace P witnesses require the retained WS inputs and report recomputation.

## What the result rests on

Every `pass` and `fail` row in `report.json` carries `provenance` (the path and
sha256 of each file it was recomputed from) and `anchor: "operator-retained, no
external anchor"`; `report.md` says so in one line. Nothing outside the
operator's machine vouches for these files. An operator who fabricates a
consistent journal, sink journal, profile and results is not detected, because
the audit chains have no external anchor. Deliberate forgery that edits several
producer outputs consistently is out of scope. This is a recorded limitation of
the thesis. Captures from another freeze, commit, binary, image or corpus,
internally inconsistent or ambiguous documents (including duplicate JSON members),
and a recorded digest differing from the tree actually evaluated are in scope
and fail closed. The checker removes the cheaper ways to fail: a payload cannot assert an
outcome, a replaced or rehashed chain cannot match the journaled report digest,
and results cannot change role or tree. Keep one frozen capture set per
directory and replace a capture deliberately, never choosing a favorable attempt.

Without `--guest-evidence`, the command reads none of this and produces the same
reports as before. The pilot's 38 checks are not real guest results until the
owner has performed and retained the captures above.
