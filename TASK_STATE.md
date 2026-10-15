# TBound task state

Updated 2026-10-15. Work only on `codex/tbound-prototype`.

## Current evidence

- Settlement gate `e0d48d0`/`ebbbbbc`; D09 hash binding `89b76d0` committed. Positive production settlement remains unproven.
- Snapshot `a7ac97e` found two stale service fixtures. Corrections pass focused/unit checks; package race found a separate fresh-process owner-incarnation refusal. Baseline investigation is active; do not call the overall gate green.
- D09 cell-template still skips for supplementary groups under no-new-privileges. SQLite patch: four source hashes and 13 log hashes independently matched; focused/package/consumer race checks pass, ext4 opt-in remains skipped.
- Bootstrap unmount safety fix: two source hashes match; AST-extracted five-case harness fails before fix and passes after. Receipt `docs/evidence/abi8-host-recovery-20261015.json`.
- No G1, containment or H1 claim established. Storage ceilings alone do not establish D14.

## Active / next

1. Fixture task: diagnose recovery race refusal against untouched source, finish corrected receipt and narrow link cleanup. Then combined Go 1.27.1 module gate on six pending Go files; commit reviewed increments separately. Positive production settlement fixture remains next.
2. Host task: disposable GUID `df4745b2-fd1b-48ed-b9b9-f36f34d3c19b` Off, SCSI 0:0 and boot order restored. Trusted disk lacks client authorization. Parent authorized public-key injection on a copy, preserving old disk; recover pinned SSH, then measure/install exact ABI 8. Original Saved; seed preserved. No blind HCS retry/recreation.
3. Headroom review: derive logical SQLite headroom and physical reserve from actual post-effect writes; check incompatible retained-WAL refusal. Then implement/qualify bounded D14, real D20 drain and fresh-process recovery. Root is available through WSL for isolated ext4 tests.
4. Research Claims last: reconcile thesis; complete gates; freeze protocol/corpus/oracles/calibration/scoring and costed OpenRouter DeepSeek V4.1 Flash plan within US$5. User/advisor construct review and independent runtime/effect oracles; no invented outcomes.

## Constraints

- Canonical thesis: `../MSE_MA_Thesis/tbound/{tbound-thesis,tbound}.md`; broader product features deferred. Haiku 5.5/max delegation.
- Preserve ten other-session dirty docs. Stage explicit reviewed paths only. Never read/print credentials, real signing keys or `vm_start.txt`.
- Linux Go 1.27.1; shared SDK/caches; private temporary directories under `/home/jeli2k`, removed on exit. Logs on F:, C: guard ≥5 GiB. No WSL shutdown while tests run.
- PowerShell 5.1 safe temporary writes/replacement. Preserve `supervisor/tbound` and `tbound.exe`. Fully replace OVERVIEW at increments; keep this file short; commit verified work.
