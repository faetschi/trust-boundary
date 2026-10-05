# Loopback test-run explorer

## Independent acceptance checks — 2026-10-05

Final isolated snapshot `/home/jeli2k/tbound-coordinator-s3final.8DGobj`
contains committed `1bedc0e` supervisor source plus only webview and tbound-web
overlays, excluding concurrent publication work. `/dev/sdc` is ext4; private
TMPDIR mode 0700. Full `go test -race -count=1 ./...` exited 0; gofmt empty.
Race-log SHA-256:
`9395bfa7f5865f7158b351fe18fec879cf381645758d9febe61cab95e0a1aca8`.
Pre-test source-hash listing SHA-256:
`efbf0fbc9f969023918ada07494fb7520998a5628e8d76764d9467961f8a0314`.
Windows exact snapshot build and internal/e2e tests exited 0. Modules unchanged;
adapter untouched. Commit/byte comparison is recorded in `TASK_STATE.md`.

An independently launched Windows `go test -json -count=1 ./...` returned 0.
The viewer observed running tests in 17 of 36 sampled snapshots and retained
203 passed tests across 17 packages, distinguishing expected skips and explicit
no-test-files markers. A second actual webview suite returned 0. Private-profile
Chrome CDP review checked LIVE and historical REPLAY after viewer restart:
81 retained summary rows, named packages, no JavaScript exceptions, desktop
1440px layout and mobile 390px document/scroll width. The mobile run selector
overflow found in the first review was fixed before the final full-suite retest.
Truncating the input for the second run correctly displayed a SOURCE GAP.

Retained scratch evidence under the private OpenCode temp directory:
`tbound-live-a357727c7a20485e86ca9b7efa5fcb9f`. Live-receipt SHA-256:
`9b0569f42665991f67546609ca766244cc0523590645154ffc9100712d464b14`;
final browser observation SHA-256:
`cab71ce82db65f729893bd2610af94a0910980183d3f5439a7a65438e6c1d228`.
Desktop/mobile/replay PNGs remain with those observations. Two initial scratch
driver attempts failed (missing process exit handle, then invalid `finished`
manifest state); neither is represented as a successful product test. The
corrected driver collected actual exit 0 and schema-valid `passed` state.
The private Chrome and viewer processes were explicitly closed. Browser checks
used a private headless profile, not the user's logged-in browser session.

`tbound-web` is an optional, untrusted, read-only viewer for bounded test and
supervisor observations. It is not Pi chat, does not start or control a test
process, and is not in the claim path. The CLI remains the required
claim-bearing interface. Nothing shown here establishes authorization,
containment, external authenticity, or H1.

## Live Go test workflow

The server tails files selected by the operator. Run `go test -json` yourself;
the web server never launches a shell, test binary, provider, VM, or other
executable. For live updates, start the server before appending test events.

In PowerShell, from the Go module directory, prepare one run manifest and an
empty JSONL output file. The run manifest is the lifecycle record of the
external command, not a signed claim:

```powershell
$manifestPath = Join-Path $PWD 'go-test-run.json'
$eventsPath = Join-Path $PWD 'go-test-events.jsonl'
$started = [DateTime]::UtcNow.ToString('o')
$packages = @(go list ./...)
$commit = ((git rev-parse HEAD) -join '').Trim()
$dirty = @((git status --porcelain --untracked-files=no)).Count -gt 0
$sourceIdentity = [ordered]@{
  schema_version = 'tbound-run-source-identity/v1'
  commit = $commit
  source_epoch = [Guid]::NewGuid().ToString()
  dirty = [bool]$dirty
}
$run = [ordered]@{
  schema_version = 'tbound-go-test-run/v1'
  run_id = [Guid]::NewGuid().ToString()
  state = 'running'
  started_at = $started
  go_version = ((go version) -join ' ')
  module = ((go list -m) -join '')
  packages = $packages
  source_identity = $sourceIdentity
}
[IO.File]::WriteAllText($eventsPath, '', [Text.UTF8Encoding]::new($false))
$writeRun = {
  $tmp = $manifestPath + '.tmp'
  [IO.File]::WriteAllText($tmp, ($run | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false))
  Move-Item -Force $tmp $manifestPath
}
& $writeRun
```

Start the viewer in one terminal. Add only source paths you intend to disclose
to the local browser. The optional history file is viewer-owned and should not
be placed over an audit journal, transcript, or other source:

```powershell
go run ./cmd/tbound-web `
  --addr 127.0.0.1:8787 `
  --test-json $eventsPath `
  --run-manifest $manifestPath `
  --history (Join-Path $PWD 'tbound-web-history.json')
```

In a second terminal, run the actual test command and append its structured
output. Then update the manifest to the observed terminal process state. Use
`failed` for any nonzero process exit; `passed` means only that the command
returned zero. If the shell/process is stopped before collecting its exit,
write `interrupted` rather than guessing success.

```powershell
$test = Start-Process -FilePath 'go' -ArgumentList @('test', '-json', './...') `
  -Wait -PassThru -NoNewWindow -WorkingDirectory $PWD.Path `
  -RedirectStandardOutput $eventsPath
$testExitCode = $test.ExitCode
$run.state = if ($testExitCode -eq 0) { 'passed' } else { 'failed' }
$run.exit_code = $testExitCode
$run.finished_at = [DateTime]::UtcNow.ToString('o')
& $writeRun
```

The run manifest schema is `tbound-go-test-run/v1`; duplicate, unknown,
incorrectly cased, invalid-UTF-8 fields and invalid lifecycle combinations are
rejected. It requires `run_id`, `state`, and
`started_at`; terminal states (`passed`, `failed`, `interrupted`) require
`finished_at`; `passed`/`failed` require a consistent `exit_code`, and
`running` must not have a finish time. Within a run ID, lifecycle state is
monotonic. A new run ID starts a fresh current catalog; a prior run that was
still marked `running` is retained as `interrupted` with an explicit warning,
not silently treated as passed. `--run-manifest` and `--test-json` are required
together. Multiple `--test-json` inputs are associated with the single current
manifest/run ID.

`source_identity` is optional and versioned as `tbound-run-source-identity/v1`.
It can declare a 40/64-hex commit, a bounded source epoch, a `dirty` boolean,
and/or a `sha256:<64 hex>` source digest. The sample captures the current HEAD
and tracked-file dirty state; it intentionally excludes untracked files so the
generated event/manifest files do not mark their own run dirty. These are
self-declared labels only: they are not signed, independently checked, or an
authenticity claim. `source_identity` is immutable for one `run_id`.

Transcript streams attach at their current end. A Go test stream is resumed only
from a viewer-history checkpoint bound to the current run ID whose complete
prefix SHA-256 matches; offsets are saved at complete-line boundaries and
prefix verification is capped at 256 MiB. History retains these offsets and a
16-run summary catalog independently of the recent-record ring. Without a
valid checkpoint, a pre-existing or nonmatching Go JSON file attaches at its
end and the source is marked
`SOURCE GAP`; affected catalog states become `unresolved` and a warning is
shown. It is not silently presented as a complete continuation. Use a new empty
event file for each run and keep the viewer running across transitions. If
multiple runs append to a source while the viewer is down, the latest manifest
cannot identify which run owns each line; those lines are not reliable
run-attributed evidence. Audit journals are the exception: they are scanned
from the beginning with `audit.Verify` to derive a typed projection, not
replayed as raw audit rows. The browser uses Go's structured `Action`,
`Package`, `Test`, `Elapsed`, and `Output` fields. Output is limited to 4 KiB
per test and 2 KiB per package; the current catalog retains at most 256 tests
and 128 packages. Retained summaries hold at most 64 tests and 32 packages per
run, and omit prior-run output.
The existing JSONL frame limit is 1 MiB. The browser renders all source text as
text, never HTML. The source registry is capped at 32 total entries, with at
most two audit journals and one evidence bundle/run manifest.

States remain distinct:

- `running`, `paused`, `passed`, `failed`, and `skipped` follow Go test events.
- `no_test_files` is identified only from a Go package output shaped like
  `? <package> [no test files]`. The `go-test-output-marker` provenance is shown;
  it remains observed text, not independent proof.
- Go's `build-output` contributes bounded output and `build-fail` marks the
  package failed. Unknown Go JSON actions become malformed observations,
  warnings, and unresolved states rather than silent no-ops.
- A test left running/paused when a terminal manifest arrives is `interrupted`;
  declarations or states lacking terminal events remain `unresolved`, never
  silently promoted to pass.
- `failed` in the run manifest is the process-level result. Individual tests
  that emitted `pass` remain `passed`; the process result does not rewrite
  individual test evidence.
- Without a valid lifecycle manifest, the viewer cannot distinguish an active
  test process from one whose terminal events were lost. It reports the
  manifest as absent/invalid rather than inferring a run result.

## Read-only endpoints and replay

The embedded page and JSON endpoints are same-origin, GET-only views:

- `/snapshot`: versioned `tbound-observation-snapshot/v1` with current cursor,
  retained records, run/source manifest, Go test catalog, audit projection,
  and generation reconstruction summary.
- `/manifest`: versioned `tbound-observation-manifest/v1`; it reports source
  names/kinds/epochs, not source paths.
- `/tests`: bounded package/test catalog.
- `/runs`: current and retained run summaries; at most 16 runs total survive ring
  eviction and server restart when `--history` is enabled.
- `/runs/{run_id}`: bounded output-free summary for a registered run ID only;
  it does not accept a path and reveals no source file paths.
- `/records`: compatibility endpoint for the bounded recent observation ring.
- `/events`: SSE with `epoch:sequence` IDs; it accepts the browser's
  `Last-Event-ID` on reconnect and an initial `cursor` query parameter.

All other paths are not file-serving APIs. No endpoint accepts execution,
control, filesystem, policy, audit, or other mutation commands. The server is
bound to `127.0.0.1` by default and accepts only literal loopback IP addresses
for `--addr` (not a DNS name such as `localhost`). This is not local user
authentication. Do not expose it through a reverse proxy or network tunnel.

With `--history`, the viewer atomically rewrites a JSON snapshot bounded to
16 MiB, the configured 64-record ring, Go stream checkpoints, and at most 16
run summaries total, including the current run. Summaries survive ring eviction and restart, omit output,
and cap each run at 64 tests/32 packages. History loading is limited to 16 MiB
even if the file grows while it is being read. The temporary replacement file
uses mode 0600 where supported;
this is not a cross-platform access-control guarantee.
Without `--history`, a server restart starts a new observation epoch and the
browser reports a reconnect gap. A configured history preserves the event
epoch across restart; if the requested cursor predates retained history, the
server sends an explicit `gap` SSE event with reason `history_evicted`. Epoch
mismatch, malformed cursor, and cursor-ahead cases are also explicit. Go test
stream replacement, truncation, missing checkpoints, mismatched prefixes, and
run-ID changes with appended bytes are exposed as source gaps and unresolved
catalog states. Complete Go JSON lines are drained before applying a terminal
manifest for that poll, so
already-written terminal events are not prematurely labeled interrupted. A
gap is never filled with invented records.

History is a viewer-owned derivative, not authoritative evidence. Protect it
according to the sensitivity of the sources shown. A persistence error is
reported in the observation manifest and does not alter supervised effects or
source files. Only complete JSONL lines are emitted; an incomplete tail at
shutdown is represented as malformed/incomplete input.

## Audit and generation projections

For each `--journal`, the viewer rereads a bounded complete journal snapshot
and calls `audit.Verify`. It exposes only verified sequence/kind/ID/outcome/hash
and effect summaries; audit event `Data` and result bytes are not copied into
the typed audit projection. A corrupted, partial, unavailable, or over-limit
journal has an invalid/unverified status, not a reconstructed verdict. The
projection is structural hash-chain verification only; it does not authenticate
the machine, operator, or source against an external trust anchor.

On Linux, `--evidence-bundle` consumes an explicitly configured,
size-bounded session-repository evidence bundle and calls
`sessionrepo.Reconstruct` (which itself uses `audit.Verify`). The viewer shows
the re-derived generation IDs and typed disposition counts, while preserving
the bundle's quarantine state. It does not reimplement verdict predicates.
The projection explicitly notes that external policy-decision authenticity,
command-runner settlement, host containment, and provider exchange are not
established by that reconstruction. Non-Linux builds show that this evidence
reconstruction is unavailable. These projections are diagnostic and
nonauthoritative.

## Bounds and failure behavior

The event ring defaults to 64 records. JSONL lines are capped at 1 MiB,
oversized input is reduced to a 256-byte preview, audit projection input is
capped at 16 MiB, evidence bundles at 32 MiB, and the optional history file at
16 MiB. Slow SSE clients are disconnected; reconnect either receives the
retained continuation or an explicit gap. Browser-side rendering is text-only
and caps the recent feed at 256 rows.

Malformed JSON, unknown/invalid run-manifest versions, audit tampering, missing
source files, evidence reconstruction failures, process interruption, and
output/catalog bounds remain visible as failures or limitations. The web
viewer is an observation aid: consult and validate the CLI and original
claim-bearing artifacts independently.
