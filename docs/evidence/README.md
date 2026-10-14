# Evidence and historical sources

This directory preserves bounded engineering receipts. They are not proof of a
qualified Linux boundary or registered thesis results. The older Pi SDK fixture
receipt describes the pre-CLI implementation and does not qualify the CLI migration.

## Current checkpoint receipts

- [Component checkpoint](component-checkpoint-6a0a52a.json), committed in `5051759`,
  records Go 1.27.1 linux/amd64 checks on source commit `6a0a52a`: build, vet, format,
  unit, and race commands exited 0; unit and race each report 1,126 passed, 7 skipped,
  and 0 failed. This is component regression evidence only.
- [Hyper-V host inventory](host-inventory-20261014.json), committed in `8756cca`,
  records a read-only inventory sourced from `5051759`: `TBound-Ubuntu-2404` was
  Running with 8 vCPUs, 16 GiB startup RAM, and a 40 GiB disk. It had no guest channel;
  guest kernel and Landlock ABI were not probed. It is not host qualification evidence.
- [SQLite reservation faults](sqlite-reservation-faults-20261014.json), committed
  in `0ab8b9f` (D14-S1), records five real reservation-path fault cases and
  fresh-process recovery checks; package unit/race each passed 63 tests with no skips
  or failures. D14 remains partial.
- [SQLite terminal faults](sqlite-terminal-faults-20261014.json), committed in
  `ad2479f` (D14-S2), records three terminal-persistence fault cases verified by
  fresh processes. Unit and race logs each contain 71 verbose PASS events on a
  composed overlay of base `296957b`, the D20 workflowrecovery ledger files, and the
  S2 test; the D20 cmd/tbound integration files are excluded. The faults reach the
  real SQLite owner/root persistence boundaries, but Store pins/verifier and the
  fsynced effect marker are synthetic: production `RunEffect` and a real external
  provider/Store effect are not exercised. SQLite `max_page_count` is not filesystem
  ENOSPC; genuine fsync failure, corruption, reserved capacity, and declared-platform
  power-loss evidence remain open.
- [Governed combined recovery](governed-combined-recovery-20261014.json), committed
  in `d29a0ff` (D20), records the atomic clean-restart plus pre-effect-retry
  reservation and separate-process prepare/activate checks across two cycles followed
  by refusal at the restart cap. Its isolated source archive overlays all four D20
  files on base `296957b`; its 39 top-level / 66 verbose workflowrecovery counts and
  15 top-level / 34 verbose cmd/tbound counts are distinct from the S2 composite run.
  Full production recovery-binder verification and actual owner-cell drain remain
  open; the D20 test uses a no-op drain verifier. Neither receipt establishes G1, H1,
  containment, or D14 completion.
- [Production pin composition](production-pin-composition-20261014.json), committed
  in `8e9293d`, records a baseline-red/current-green fresh-initializer regression and
  Go 1.27.1 WSL unit/race, build, vet, and format checks. With a dummy test credential,
  the real initializer returned a production conversation and left the provider
  journal empty. This covers fresh-initializer pin composition only; the complete
  recovery binder and actual owner-cell drain remain unqualified.
- [Fixture-route CLI flag rejection](fixture-route-cli-flags-20261014.json) records
  baseline-red/current-green coverage for explicit fixture-route rejection of
  production profile, workspace, and source inputs, plus the `cmd/tbound` package
  checks. It does not qualify the production launch or broader operator CLI lifecycle.

The dated `../evaluation-evidence-index-2026-10-07.json` is an immutable structural
ledger. Its original source references may name documents retired from active
navigation. Do not rewrite the ledger or receipts to make historical outcomes
look current.

[historical-sources.json](historical-sources.json) maps the retired source paths,
generated snapshots, and removed binary to their original SHA-256, byte length,
and preserved Git source at commit
`29196d664316886e929c963ce2a404a876e4c6cf`. Retrieve original bytes from that commit
and verify the recorded hash before citing them. Source retirement does not erase
the original record. The commit is the pre-cleanup source identity, not a frozen
experimental profile.

The primary hashes and sizes identify the raw committed Git blobs. The preserved
Windows archive export converted line endings for some text; separate
`baselineExportSha256` and `baselineExportBytes` fields identify those export
variants. Do not substitute a checkout or converted export for a cited blob.

For example, from the repository root on Linux:

```sh
git show 29196d664316886e929c963ce2a404a876e4c6cf:docs/implementation-status.md > /tmp/tbound-historical-status.md
sha256sum /tmp/tbound-historical-status.md
```

Use byte-preserving redirection for binary artifacts and on platforms whose shell
rewrites encodings. Active status belongs in [status.md](../status.md), not these
dated snapshots. New results require their own source/profile identities and
evidence class; missing observations must remain unsupported.
