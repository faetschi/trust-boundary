# Retaining guest construct witnesses

`tbound-profile corpus-check --guest-evidence DIR --run-config run.json
--guest-binary-sha256 sha256:<frozen tbound-profile digest>` imports evidence
without contacting the guest or executing a witness. Retain the independently
frozen run config and binary digest; do not derive these expectations from the
evidence being checked. The config is read only for the existing
`guestevidence.ParseRunBinding` fields: `freeze_commit`, `v_image_digest`, and
`pins.{frozen_source_digest,base_template_digest,admin_pubkey_digest,runtime_bundle_digest,runner_digest}`.
No SSH or credential file named by that config is opened.
P receipts also bind `admission_policy.{policy_digest,metadata_policy_digest}`
from that config, using the production admission-policy digest calculation.

There is no existing complete construct witness format. Runner journal entries
retain P outcomes but not their manifest/freeze bindings; exported trials and
reports reduce those observations to scoring records. `trial accept` retains
one `tbound-accept-result/v1` document for the live tree, without manifest,
fixture/reference role, freeze or profile bindings. `corpus-digest` prints only
the admitted digest; its local Node self-check is not an acceptance-cell
witness. `guest-evidence netsink` proves confinement/liveness, without a case
or an egress observation. None alone can close a construct check.

The minimal additional capture format below reuses the existing
`tbound-guest-evidence/v1` envelope, `ProfileBinding`, runner terminal entry,
acceptance result, netsink envelope, and net-journal/canary documents. The three
`guest-construct-*` kinds are for `corpus-check`, not EVQ. Existing producers
do **not** yet emit these assembled envelopes automatically. The trusted
operator/capture procedure must retain the original outputs unchanged and
assemble their bindings while the relevant signed profile, registration and
trees are still available. A label or digest is not independent proof that a
run happened: like the other guest envelopes, this is consistency checking of
trusted captures, not a new signature or remote attestation scheme.

For each case/check, write exactly `DIR/<case_id>/<check>.json` (regular file,
at most 1 MiB). Files outside these paths are ignored. Unknown/duplicate JSON
members, trailing data, missing required booleans and unsupported combinations
are refused. The directory is opened with `os.OpenRoot`; paths cannot escape
it. Keep one frozen capture set per directory; replace a capture deliberately,
never choose a favorable attempt from repeated adverse outcomes.

Envelope structure (angle-bracket values describe original captured records):

```json
{
  "schema": "tbound-guest-evidence/v1",
  "kind": "guest-construct-acceptance-cell",
  "commit": "<40 lowercase hex: run.json freeze_commit>",
  "binary_sha256": "sha256:<frozen tbound-profile build>",
  "data": {
    "manifest_digest": "sha256:<evalcorpus.Digest of the checked manifest>",
    "case_id": "PILOT-CMP-read-01",
    "fixture_digest": "sha256:<workspace_fixture_digest>",
    "binding": "<original guestevidence.ProfileBinding object>",
    "acceptance": {
      "solution_digest": "sha256:<reference_solution.digest>",
      "route": "trial-accept-cell",
      "fixture": "<original tbound-accept-result/v1 object>",
      "reference": "<original tbound-accept-result/v1 object>"
    }
  }
}
```

`binding` contains `profile_digest`, `template_digest`, `base_template_digest`,
`registration_digest`, `image_digest`, `source_digest`, `admin_pubkey_digest`,
`runtime_bundle_digest`, and `runner_digest`, as in the existing guest evidence.
Capture these from the run's verified signed host profile, template and managed
registration, using the same procedure as the existing guest producers. The
shared six artifact pins must equal the frozen config; the profile, registered
template and registration must be valid digests. A base template cannot stand
in for a registered template. P receipt profile/template/source must also
match that binding. Every envelope must match the checked manifest, case,
fixture, expected commit and producer digest.

## P calibration

Use kind `guest-construct-calibration-p`, omit `acceptance`, and set `data.p`
to the original `done` entry of the case's static P step from
`tbound-run run`'s `journal.jsonl` (`runner.Entry`, including its full outcome).
Use the final permitted attempt and retain the full journal for audit. A
partial, evidence-lost, failed/setup-failed execution, an unverified identity or an injected
oracle result is insufficient. The entry must name the same case and
`<case_id>|static-replay|P|0`; the outcome must name that interval and attempt,
registered witness id and predicate. Its scripted receipt must carry the
manifest script digest and targeted P delta. The oracle reference must name
the current registered oracle version and a report digest; positive calibration
must cite that exact report as `oracle:<predicate>:<report hex>`.

An `observed` finding closes the **step-1 static observability witness**;
`none` is a failed calibration. Missing, lost or indeterminate findings remain
pending. This does not establish the later static/adaptive P/C pair, adaptive
caps or the G4 gate. For EFF-NET-01, include `data.net` as described below.

## Acceptance cell

Capture two `tbound trial accept <case_id> --tree live` results: first after
resetting to the untouched pinned fixture, then after applying exactly the
pinned reference solution. No agent run or other write may intervene. Both
must use the guest's isolated acceptance runner (`cellNodeRunner`); set
`route` to `trial-accept-cell` only for that route. A local `corpus-digest`
self-check or a passing B/C/V agent solution cannot substitute for this pair.
Record the pinned reference solution digest and the verified profile binding.

Both documents must match the case and acceptance spec digest, have exactly
the spec's checks in order with matching operations/paths, and have a `passed`
value consistent with every result. The untouched fixture must fail and the
reference must pass. Either opposite outcome is `fail`; a malformed document,
different solution, or unqualified route is `guest-only`.

## Network effect satisfiability

Use kind `guest-construct-effect-satisfiable` and the same bound static P
terminal as calibration. Include `data.net` on this and NET-01 calibration:

```json
{
  "sink": "<original guest-evidence netsink envelope object>",
  "start": "<original tbound evidence net-canary start document>",
  "end": "<original tbound evidence net-canary end document>",
  "journal": "<original tbound evidence net-journal document>"
}
```

Retain the outputs for the P interval from the evidence transport, before the
next reset/run. `sink` must pass the existing validator at the same frozen
commit and binary digest. The canary tag, phases, sequences and hashes must
match the journal; its full audit chain must verify through the acknowledged
end canary. `netoracle.EvaluateNET01` is rerun over the retained journal with
the P interval and tag. Its recomputed digest must equal the P oracle
reference; there must be no evidence gaps, and its finding must agree with P.
An observed receiving-side arrival passes; a complete empty window fails.
An unrelated canary, a netsink capability probe alone, a stale envelope, or a
contradictory/lost report stays pending. Guest effect import currently supports
EFF-NET-01 (all seven pilot guest effect checks); other guest effects stay pending.

Missing input, malformed JSON, absent/mismatched bindings or unqualified
observations keep `guest-only` and name the problem in `report.json`.
A bound negative makes the report fail and the command exit nonzero.
Without `--guest-evidence`, the command never reads these inputs and produces
the same reports as before. The pilot's 38 pending checks are not real guest
results until the owner performs and retains the captures above.
