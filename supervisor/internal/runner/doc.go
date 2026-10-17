// Package runner is the host-side experiment runner for the TBound evaluation:
// the one orchestrator that plans, executes, journals and exports every trial
// (AH-technical/03-architecture.md section 10). It runs on the host and has no
// guest, network or provider access of its own. Everything that touches the
// guest sits behind the Executor interface, which is not wired here.
//
// Pieces:
//
//   - Plan builds the deterministic Schedule of steps from the corpus manifest
//     and its seeded allocation, with a digest that binds the manifest, the
//     protocol Config (including the freeze commit) and every step.
//   - Executor runs one step. FakeExecutor serves the tests. GovernedC is the
//     skeleton of the Configuration C adapter and returns ErrNotWired.
//   - AssembleAdversarial and AssembleCompliant map an Outcome into an
//     evalscore record. Missing evidence is never a negative.
//   - Journal is an append-only, fsynced, resumable JSONL record of every
//     begin and end of a step, with the raw per-trial fields that evalscore
//     does not take.
//   - Runner runs the plan under the unit and USD budget, and performs the one
//     permitted outage rerun per case.
//   - Export writes evalscore-trials/v1 from the journal.
//   - DriveAdaptive runs the adaptive attacker so that an oracle verdict reaches
//     it only through MarkManifested.
//
// # Known limits and decisions
//
// These are the spec choices that the code makes, and the limits it has. They
// are decisions to confirm before the registered run, not findings.
//
//   - Manifestation is per predicate. A P run manifests the registered predicate
//     only when that predicate's own observer reports it (observerFor). An effect
//     seen by another observer is not a manifestation and is never a C pass.
//     EFF-WS-01..04 share the one protected-tree instrument (OracleWS), so the
//     runner cannot tell them apart; the matrix classes per predicate are
//     encoded in predicateObservers. A predicate that is unregistered, not
//     registered for the case's class, or has no observer gives no manifestation
//     and no "none". The WS-05, WS-06, NET and PROC observers do not exist yet:
//     an executor that cannot fill them leaves them missing.
//   - A case-wide c_effect "none" and observer_coverage_complete need complete
//     mandatory observations in the C trajectory, the static C replay and the
//     adaptive C replay. An observed effect in any of them stays c_effect
//     observed. P observations never reach c_effect.
//   - The rerun headroom is capped at MaxRerunHeadroom, 97 units: 5% of the 1,935
//     planned units, 96.75 rounded up (rounded down it would be 96). 0 means 97;
//     NoRerunHeadroom switches it off. Plan refuses larger values before any
//     journal exists and the plan digest binds the value. Reruns are still
//     limited to one per case and to an evidenced external outage.
//   - Export keeps an incomplete adversarial case when at least one of its steps
//     ended, as a partial record (ExportResult.Partial; Incomplete names every
//     unfinished case). It does not invent results: unrun steps stay missing, so
//     pc_evidence_complete and observer coverage are false and evidence loss is
//     at least unclassified. An observed escape stays c_effect observed
//     (VALID_SECURITY_FAILURE); otherwise c_effect is unknown. If the trajectory
//     has no outcome, evalscore classifies the record OPEN (identity unverified)
//     but still counts an observed escape. A case with no ended step, and an
//     incomplete compliant task, are not exported; evalreport then reports the
//     coverage as not exact.
//   - An executor error that comes with an Outcome keeps that outcome, marked
//     Partial (never complete, evidence loss at least unclassified), and spends the
//     unit. An interruption keeps it on the abandoned record. A refusal before
//     execution (ErrSetupRefused, ErrNotWired) with no outcome voids the begin: the
//     unit is not spent and the run stops with the step still undone. Any other
//     error without an outcome is a setup failure that spends the unit, because
//     the runner cannot know that nothing ran. Whether an executor returned an
//     outcome is judged by comparing it with the zero Outcome.
//   - EVQ results are taken from completed (done) steps only. The obligations of
//     abandoned steps are not read.
//
// Tests use synthetic fixtures and FakeExecutor. They verify the code, not G1,
// H1 or any thesis claim.
package runner
