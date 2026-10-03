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
| Broker correlation | An uncommitted OpenRouter first slice is in review; no provider exchange or E05 run. | A real call-to-proposal-to-verdict-to-result chain bound by IDs and argument digest. |
| Generation visibility | Three uncommitted workspace package files are in progress; review found an unbounded `ReadDir(-1)` memory issue and a fix is underway. No Go tests ran. | Approved-delta mapping, sealed generations, and no live, mount, or Git reachability; sealing and publish are not implemented. |
| Durable authority | No fault-injection run is recorded. | Durable pre-effect/result records and fail-closed write, fsync, and disk-full tests. |
| Containment | Three queued runs passed the guest-offline network gate; the latest verifier exit 1 is user-reported, but suite outcomes are unknown until its report is collected. | Capability and runtime proof for protected paths, unauthorized egress, and surviving children. |
| Reconstructable evidence | A preserved report reconstructs a failed pre-suite attempt. | Raw evidence for a successful workflow, including its decision, effects, and outcome. |

The second queued run (third offline attempt overall) ended at `go_modules` with runner and
launcher exit code 1, `checks={}`, no suites, cleanup PASS with no leftovers, and all claims
false. Root independently verified the preserved JSON and its SHA-256 on Oct 3:
`72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae`. This verifies that
report, not every file in the separately reported host evidence folder.

Earlier attempts stopped before suites at the cgo/PATH check and the held-GCC package parser.
The targeted APT trust-guard regressions, one held-package parser regression, guest AST check,
and launcher Bash syntax check passed. These are component checks, not a full Go/Pi run. The second queued run failed its Go module graph check. Reviewed dependency preparation and a guarded guest apply followed. The user reported that the third queued verifier run failed with exit code 1 at `2026-10-03T13:19:19Z`; its report and stderr have not been independently collected, so both the failure cause and suite outcomes are unknown. The guest network is last confirmed disconnected; a guarded trusted-maintenance reconnect is pending user confirmation. After confirmation, collect the report and stderr over pinned-key SSH. Do not retry, requeue, or restore. No successful full Go/Pi suite has been independently verified.

## Current broker slice and provider plan

The uncommitted OpenRouter slice is in `supervisor/internal/broker/openrouter/openrouter.go` and `openrouter_test.go`, covering a fixed profile, bounded SSE handling, and trusted capture. Independent review by `/root/broker_firstslice_review_luna` found invalid UTF-16 surrogate IDs; `/root/broker_integration_plan_luna` is updating both files with the repair, a pre-marshal size bound, and trust-boundary comments. The slice is uncompiled and under review. No Go or gofmt host checks, guest tests, provider requests, or live E05 exchange have run. `/root/generation_integration_plan_luna` has created three uncommitted files under `supervisor/internal/workspace/` (documentation, Linux implementation, and test). Static review found that `ReadDir(-1)` can load an entire directory into memory; a fix is in progress. No Go tests have run, and sealing/publish are not implemented.

OpenRouter is the primary planned provider. OpenCode Go is optional for testing and may be selectable later; exact second-profile authentication compatibility is unverified. These are portable design plans, not actual supported integrations.

## WP1/G1 prerequisites and sequencing

The todo also requires five falsification spikes: Pi closure; a real broker exchange and E05;
repository isolation and delta provenance; rootless Podman/crun launcher containment; and
write/fsync/disk-full audit faults. The E04/E06 fixtures and frozen evaluation-profile manifest
are also pending. None has a passing result recorded.

The existing Ubuntu guest and host checks are useful setup evidence, but checkpoint restore remains untested. Commit `99f87dd` adds the planned restore acceptance runbook, but the procedure has not been exercised. Inventory reported `Standard` for the existing checkpoint even though the configured
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
