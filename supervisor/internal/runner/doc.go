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
// Tests use synthetic fixtures and FakeExecutor. They verify the code, not G1,
// H1 or any thesis claim.
package runner
