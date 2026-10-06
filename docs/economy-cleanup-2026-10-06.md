# Economy cleanup implementation — 2026-10-06

This note records the bounded, credential-free cleanup applied after the
`00ce8849..HEAD` review. It is intentionally separate from `TASK_STATE.md`;
the coordinator owns the final integration and commit.

## Implemented surface

- `TBOUND_RUN_REAL_PROVIDER_TESTS=1` is now required by both real-provider
  tests:
  - `supervisor/cmd/tbound/real_provider_durable_linux_test.go`
  - `supervisor/internal/broker/openrouter/provider_exchange_test.go`
  Credentials alone do not enable either test or initiate a network request.
- `/manifest` and `/tests` now use focused locked defensive-copy methods rather
  than constructing an entire `ExplorerSnapshot`. Sorting, redaction, bounds,
  and JSON contracts are unchanged.
- Viewer history writes are batched at tailer startup, each poll/control cycle,
  and shutdown. A batch snapshots observations, catalog/projection state,
  source epochs, and checkpoints consistently, then performs the atomic
  viewer-history replacement outside the live-state mutex. `Tailer.Start`/
  `Done` lifecycle access is serialized. `Buffer.FlushHistory` is available to
  an owning shutdown path; tailer cancellation/error shutdown also flushes
  after closing partial source lines.
- Removed the unused `publish`/`publishLocked` return clone and one redundant
  `json.RawMessage` decode. Full JSON validation still runs first, preserving
  duplicate-key, UTF-8, case, depth, and trailing-document rejection.
- Added focused endpoint-copy/history-consistency tests and credential-free
  benchmarks in `supervisor/internal/webview/webview_bench_test.go`.

No publication, module, adapter, frontend-plan, infrastructure, secret, or
VM files were changed by this cleanup. Publication staging/journal helper
consolidation remains deferred.

## Measured benchmark evidence

The baseline was an isolated archive of `cb63b110eb1dbc9cebb9d2964e736399dec2dd76`;
the comparison was the working tree after this cleanup. Both runs were on
Windows/amd64, the same AMD Ryzen 9 5950X host, with
`go test ./internal/webview -run '^$' -bench ... -benchmem -benchtime=200ms`.
These are single short benchmark runs, so they are directional rather than a
performance guarantee.
The endpoint benchmarks populate a 64-record replay ring; the `/tests`
benchmark also populates 64 test cases.

| Benchmark | Baseline | Cleanup | Direction |
| --- | ---: | ---: | ---: |
| publish event, ns/op | 120.9 | 72.35 | lower |
| publish event, B/op | 80 | 40 | lower |
| publish event, allocs/op | 4 | 2 | lower |
| validated JSON payload, ns/op | 3,412 | 3,128 | lower |
| validated JSON payload, B/op | 1,756 | 1,588 | lower |
| validated JSON payload, allocs/op | 40 | 37 | lower |
| 8-event history batch, ns/op | 10,273,650 | 1,340,851 | lower |
| 8-event history batch, B/op | 172,178 | 24,732 | lower |
| 8-event history batch, allocs/op | 1,005 | 157 | lower |
| `/manifest`, ns/op | 8,470 | 2,327 | lower |
| `/manifest`, B/op | 13,406 | 1,763 | lower |
| `/manifest`, allocs/op | 147 | 15 | lower |
| `/tests`, ns/op | 38,574 | 32,616 | lower |
| `/tests`, B/op | 28,344 | 16,536 | lower |
| `/tests`, allocs/op | 150 | 18 | lower |

The history comparison is the main economy result: the baseline rewrote the
history file for every event, while the cleanup writes once per eight-event
batch. The benchmark does not measure a provider, network, guest, or
authoritative audit path.

## Verification

Passed:

- `go test ./internal/webview ./internal/broker/openrouter`
- focused endpoint, batch/checkpoint, epoch/restart, and resume tests
- `go vet ./internal/webview`
- `git diff --check` on the owned files
- real-provider test selection with the opt-in gate unset performed no network
  operation; the Linux durable test is build-tagged out on this Windows host

Unavailable in this environment:

- `go test -race ./internal/webview`: Go reported that race mode requires cgo;
  enabling cgo then failed because `gcc` is not installed.
- The agent's WSL PATH did not resolve `go`; the coordinator's existing
  `/home/jeli2k/go-sdk/go/bin/go` installation was used for the independent
  Linux verification below. Go is not absent from the distribution.
- full repository suites were intentionally not run.
- `go vet ./internal/broker/openrouter` still reports the pre-existing
  Windows credential-file `unsafe.Pointer` warnings; this cleanup does not
  alter that code.

## Integration contract for the coordinator

### Independent acceptance evidence

Snapshot `/home/jeli2k/tbound-coordinator-cleanup.ddU75W` pins `bb90bd1`
supervisor source plus only this cleanup's webview and two real-provider test
overlays; unfinished SDK/session/workflow changes are excluded. `findmnt`
confirmed `/dev/sdc ext4`, private TMPDIR mode 0700, empty gofmt. Full
`go test -race -count=1 ./...` exited 0. Windows exact-copy build and
internal/e2e suites both exited 0. Modules/adapter unchanged.

Race-log SHA-256:
`ae426f9ce87c8ac4be18eb1a44475105e09705178d663e3d8b1bce327f33a97d`;
pre-test source-hash listing SHA-256:
`0f591426f46394cf1db42241a8ad0f9d9748334c304ef7417b4b61712afccc9c`.
Both real-network tests independently skipped with the opt-in unset even when
configured with a deliberately nonexistent dummy key-file path and the required
model. No actual credential access/request occurred. Skip-log SHA-256:
`27b6631eb758e8d46856c4f1b6cf70ed1d068df3aad0a9b1ad293d4fe35ab70a`.

Coordinator repeated the short Windows benchmark: history batch 1.422ms,
24,748 B/op, 157 allocs/op; manifest 2,456ns, 1,763 B/op, 15 allocs/op;
test catalog 34,677ns, 16,509 B/op, 18 allocs/op. This supports direction, not
a guaranteed speedup. An actual externally started Go JSON suite exited 0;
the viewer recorded 39 sampled observations and schema-valid terminal status.
An initial verification invocation had a mistyped scratch-script path (exit 1)
and was rerun correctly; it did not run tests or affect source. A separate
focused-check shell quoting error also occurred before tests and was corrected
by using an explicit scratch Bash script. Neither is represented as a test pass.
Post-commit byte verification is recorded in `TASK_STATE.md`.

Cherry-pick is not required because this work is uncommitted. Preserve the
following non-overlap when integrating:

- Keep the webview changes under `supervisor/internal/webview/**`, the two
  listed real-provider test files, `docs/test-run-explorer.md`, and this note.
- Do not combine with `cmd/tbound-web`, `supervisor/internal/publication/**`,
  modules, adapter/session-control/chat work, infrastructure, `TASK_STATE.md`,
  frontend plans, credentials, or `vm_start.txt`.
- The tailer owner must call `Buffer.FlushHistory` if a future command-level
  shutdown path bypasses tailer cancellation; normal tailer cancellation and
  error termination already perform the final flush.
- Provider tests stay skipped unless the exact environment gate is `1`.
  Independent full-suite verification should leave that variable unset and
  may run focused Linux/WSL race tests using a private ext4 `TMPDIR`.
