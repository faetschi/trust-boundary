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
| Broker correlation | The OpenRouter request/parser/capture mapping is committed at `d317e7a85c09b6d71c1dce557eeb759ce8fa79e4`; static review accepted strict Unicode and pre-marshal size fixes. No compile, gofmt, tests, HTTP requests, Pi wiring, or E05 run. | A real call-to-proposal-to-verdict-to-result chain bound by IDs and argument digest. |
| Generation visibility | The five-file bounded Linux workspace package is committed at `e187650`. Static review accepted bounded directory reads, mount checks on fresh opened file descriptors before and after reads, and disjoint-root rejection. No compile, gofmt, or tests ran. | Approved-delta mapping, sealed generations, and no live, mount, or Git reachability; sealing and publish are not implemented. |
| Durable authority | No fault-injection run is recorded. | Durable pre-effect/result records and fail-closed write, fsync, and disk-full tests. |
| Containment | Three queued runs passed the guest-offline network gate. The preserved third-run report has an overall exit 1 and mixed phase results; the Go test summary is incomplete. | Capability and runtime proof for protected paths, unauthorized egress, and surviving children. |
| Reconstructable evidence | Preserved evidence reconstructs two pre-suite failures and a third run that reached Go test phases but failed overall. | Raw evidence for a successful workflow, including its decision, effects, and outcome. |

The second queued run (third offline attempt overall) ended at `go_modules` with runner and
launcher exit code 1, `checks={}`, no suites, cleanup PASS with no leftovers, and all claims
false. Root independently verified the preserved JSON and its SHA-256 on Oct 3:
`72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae`. This verifies that
report, not every file in the separately reported host evidence folder.

Earlier attempts stopped before suites at the cgo/PATH check and the held-GCC package parser.
The targeted APT trust-guard regressions, one held-package parser regression, guest AST check,
and launcher Bash syntax check passed. These are component checks, not a full Go/Pi run. The
second queued run failed its Go module graph check. After dependency preparation and guarded
guest apply, the third run passed the offline gate and completed at `2026-10-03T13:19:19Z`
with runner and launcher exit code 1. Its preserved evidence is under
`F:\TBoundAssets\Evidence\offline-20261003-131833`; root verified the JSON hash
`5c5fa36fea3d91c36f70c32100f4f1cfaca2f86fe6ec1169a452ddbb8baa8350` and manifest hash
`48170022e98ed0c15216074e8b8f276b3f83389f81e7273c555c016062397878`. An agent checked all
nine captured guest-file size/hash pairs; root's independent comparison covers those two
files. Six of seven source hashes matched the current host; the captured setup installer came
from an earlier source base than the corrected host installer, so there is no full-tree match.

The report records `go_modules`, `adapter_dependencies`, and `node_adapter` PASS; Pi `0.87.1`
with zero provider stream attempts; and 62 passed, zero failed, zero skipped counters for each
of `go_tests` and `go_race`. Overall status is FAIL. The report also names an
`unexpected_skips`/failed-packages condition for expected test `TestOpenRejectsUntrustedOwnership`
but omits `package_results`, so the counters do not establish a successful suite or identify
the cause. Captured Go output was only held in memory and removed during scratch cleanup;
`verification.stderr` and `sudo.stderr` are empty. The actual Go error remains unknown. The
report records ownership audit package-fail 0 and run/pass 0/0, noninteractive sudo true,
cleanup and global cleanup PASS, offline carrier 0 with no default routes and host-disconnect
attestation true, and all four claims false.

The user has since reconnected the VM to `Default Switch` for trusted maintenance. Fresh
pinned-key SSH found no active guest test processes. The uncommitted bounded Go-phase
diagnostics patch is under independent review; host-only Python tests passed 3/3 and the Go
module helper tests passed 5/5. It has not been compiled, gofmt-checked, or deployed, and no
guest rerun has occurred. Do not retry, requeue, or restore before review and evidence-backed
diagnosis. No successful full Go/Pi suite has been independently verified.

## Current broker slice and provider plan

The OpenRouter request/parser/capture mapping is committed at `d317e7a85c09b6d71c1dce557eeb759ce8fa79e4` in `supervisor/internal/broker/openrouter/`. Strict Unicode validation and pre-marshal size bounds passed static review only. The code remains uncompiled, unformatted, and untested; no HTTP request, Pi wiring, or live provider support has been verified.

The five-file workspace package committed at `e187650` adds a bounded descriptor-relative Linux scanner/importer, streaming byte copying, normalized modes and ownership, mount checks on fresh opened file descriptors before and after reads, disjoint-root validation, and canonical manifests using existing `delta.TreeManifest` types. The approved policy currently maps directories to `0755`, regular files to `0644` or `0755` when executable, and normalizes ownership; these values and implementation have not been tested. Static author and independent review accepted the targeted fixes. The package does not seal or publish generations, map approved deltas into a durable ledger, or prove containment. Caller-provided quiescence and complete xattr visibility remain unproven against the actual guest profile. Hashes: `doc.go` `DD9702867E6C03898D79D0FDC97A6E94EDC42C12CB72049BEC609B7A867D1DC8`; `workspace_linux.go` `848A695AD7B6F92D02081CDD40D1928C333996D273F86736ABAA8644DF4D389D`; `workspace_linux_test.go` `18E94E12EBC887A623426EDACEE6EF675DAFBEE1FC9B80D45BC64696CDD2220E`. No Go compile, gofmt, or tests have run.

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
