# Real provider → durable synthetic write

## Scope and reproducible test

`supervisor/cmd/tbound/real_provider_durable_linux_test.go` exercises the genuine
OpenRouter HTTP transport and captures the actual response/tool call. Its
proposal uses that call's ID, tool and raw arguments; the durable proposal ID is
derived by the executor from the trusted response/call/sequence tuple. The
registered manifest allows only the ordered `read/write/edit/bash` surface.

The sole nonconfidential prompt requests exactly one `write` of
`provider-write.txt` containing `from-real-provider`. The free endpoint logs
data: no repository, thesis, private fixture or identifying material is sent.
Only `nvidia/nemotron-3.5-lightning:free` is admitted by this test. The secure
credential loader, not test code or shell interpolation, reads the configured
key; no key bytes are printed, copied to the guest or retained as evidence.

The test carries the captured call through the real Linux Unix-domain IPC path,
concrete broker correlation, deterministic gate with audit-backed decision,
durable executor and independently seeded session repository. It verifies the
delivered result, gate-before-intent-before-outcome ordering, exactly one write
effect, on-disk audit verification, `Store.Verify()`, sealed promoted file, and
fresh close/reopen recovery. Missing/invalid provider calls fail when configured;
an unconfigured test skips explicitly.

## Verification record

Independently executed on 2026-10-05 against an isolated committed-HEAD snapshot
plus this test's verified bytes (SHA-256 `e89e6569…6f7207`):

- WSL Ubuntu `go test -race -count=1 ./...`: exit 0, private ext4 source copy and
  mode-0700 TMPDIR. Windows snapshot `go build ./...` and
  `go test -count=1 ./internal/... ./e2e`: both exit 0.
- Genuine `go test -race -count=1 -v -run '^TestRealProviderDurableLinux$'
  ./cmd/tbound`: exit 0; HTTP 200/captured single write checked by the test;
  delivery, audit ordering/chain, promoted file, Store.Verify and reopen all PASS.
- Actual response `gen-1791230451-K3yGWV65fIsMKy4gB3wt`; five journal records.
  Full allowlisted receipt is in
  [evidence/real-provider-durable-2026-10-05.json](evidence/real-provider-durable-2026-10-05.json).
- An earlier loader attempt failed explicitly (exit 1, no network exchange)
  because a temporary WSL mount had disappeared. Its failed log was retained;
  the succeeding attempt mounted and unmounted the same owner-only Windows file
  in one bounded lifecycle. Read-only drvfs mapping presented mode 0400/UID 1000
  to the Linux secure loader; no key-byte copy or guest transfer occurred.
- Empty gofmt output, clean `git diff --check`, unchanged Go module files.
  Adapter untouched (npm check not applicable). Pinned guest SSH maintenance
  succeeded, but current-source guest offline verification is blocked by the
  absent reviewed helper/operator context, not represented as passed.

Captured RFC 8785 arguments and normalized sessionrepo execution arguments have
distinct digest namespaces. Independent review caught an erroneous equality
assertion before the real exchange; the corrected evidence assertions also ran
in a credential-free real-store synthetic companion test.

## Limits

This is a Linux integration test using an injected, fixed synthetic policy and
receipt authority. It is not a live Pi SDK/adapter session, a multi-turn provider
tool-result lineage demonstration, authenticated cell-membership IPC, a live
workspace publication, or a claim-bearing evaluation-guest containment trial.
The existing IPC binding token is not peer/cell authentication. WSL evidence is
development-only. `Containment=not-established`, `G1=false`; the sessionrepo
artifact does not independently assert provider exchange or Pi wiring.
