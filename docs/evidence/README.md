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
  in `0ab8b9f`, records five real fault cases and fresh-process recovery checks;
  package unit/race each passed 63 tests with no skips or failures. D14 remains partial.

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
