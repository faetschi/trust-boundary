# Narrow demonstration status

**Reviewed:** 2026-10-03. This maps current evidence to the implementation checklist; it does not
claim a completed demonstration.

## Requirement source and scope

The source is the original thesis file
`C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\MSE_MA_Thesis\agentic-harness\todo-implementation-tbound.md`.
The original repository's index and working-tree versions were compared read-only; both staged
and unstaged diffs for this path were empty. They are identical, so the indexed version was
used as the user's current specification.

The todo's narrow goal is one complete Linux/Pi workflow: one pinned Pi version, the four
proxy tools (`read`, `write`, `edit`, `bash`), one compliant task, and one Linux host/VM run.
It excludes the later arms, corpus, and headline trials.

## Exit criteria and evidence

| Todo criterion | Evidence now | Missing proof |
|---|---|---|
| Broker correlation | No provider exchange or E05 run. | A real call-to-proposal-to-verdict-to-result chain bound by IDs and argument digest. |
| Generation visibility | README describes intended invariants; no full suite ran. | Approved-delta mapping, sealed generations, and no live, mount, or Git reachability. |
| Durable authority | No fault-injection run is recorded. | Durable pre-effect/result records and fail-closed write, fsync, and disk-full tests. |
| Containment | Queued runs passed the guest-offline network gate. | Capability and runtime proof for protected paths, unauthorized egress, and surviving children. |
| Reconstructable evidence | A preserved report reconstructs a failed pre-suite attempt. | Raw evidence for a successful workflow, including its decision, effects, and outcome. |

The second queued run (third offline attempt overall) ended at `go_modules` with runner and
launcher exit code 1, `checks={}`, no suites, cleanup PASS with no leftovers, and all claims
false. Root independently verified the preserved JSON and its SHA-256 on Oct 3:
`72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae`. This verifies that
report, not every file in the separately reported host evidence folder.

Earlier attempts stopped before suites at the cgo/PATH check and the held-GCC package parser.
The targeted APT trust-guard regressions, one held-package parser regression, guest AST check,
and launcher Bash syntax check passed. These are component checks, not a full Go/Pi run. The
offline module graph failure is undiagnosed; no full verification has passed.

## WP1/G1 prerequisites and sequencing

The todo also requires five falsification spikes: Pi closure; a real broker exchange and E05;
repository isolation and delta provenance; rootless Podman/crun launcher containment; and
write/fsync/disk-full audit faults. The E04/E06 fixtures and frozen evaluation-profile manifest
are also pending. None has a passing result recorded.

The existing Ubuntu guest and host checks are useful setup evidence, but checkpoint restore is
untested. Inventory reported `Standard` for the existing checkpoint even though the configured
policy was `ProductionOnly`; that inventory value does not establish creation policy or
guaranteed freeze/thaw behavior. Guest capability probes and the full runtime profile remain
unverified. Fresh Linux physical-host migration is **UNVERIFIED**.

The current handoff sequence is to complete the narrow demonstration before starting the agreed
WP1/G1 roadmap. Passing the offline verifier alone does not meet the five exit criteria or
establish G1; the todo additionally requires all five spikes, E04/E06 fixtures, and a frozen
profile on the declared evaluation host.

## Documentation reconciliation

`README.md` correctly labels the repository an architecture scaffold. `docs/setup.md` now
states path-specific outcomes: direct-console success is judged by the verifier's exit status
and JSON; SSH-queued success also requires launcher completion and exit evidence. Its checklist
accepts either path.

Root's independent verification covers the second-run JSON, its SHA-256, and the reported exit
codes. The mechanics agent separately reported a match for all seven copied evidence files;
keep that copy comparison distinct from verification of the JSON report itself.
