# Protocol closeout, 2026-10-29

Complete: owner protocol documents and tooling are committed and pushed. The draft F14 candidate is repinned, not frozen.

| Lane | Final commit | Publication |
|---|---|---|
| Thesis, codex/tbound-prototype | 1058b0913d5a6a89d283f91d7e35448226195825 | Pushed; exact remote SHA checked |
| Master, docs/d32-amendments-config-limits | 5627335d90d464a3f729756730beba685c0f745d | Pushed; exact remote SHA checked |
| Product, feature/tbound-product | c793ef3b0b994694090383a10b1b3683e7805702 | Unchanged; no product implementation or integration |

## Validation

- Final isolated gate pc29h: **5174 pass / 40 skip / 0 fail**, including subtests; 2774 top-level passes, 26 top-level skips; 53 packages passed, 3 packages without tests skipped.
- BOUNDARY, FMT, BUILD, VET, WINDOWS markers present, TEST_RC=0. Boot ID 7b07f7dc-2f76-49f6-8d8a-6d33ae12b794 remained stable, 18:30:04Z to 18:40:44Z.
- Raw log/JSON independently verified with verify-gate.py. All 40 skip names match the historically gated 56cf99c baseline. They remain unverified: 14 pilot static-replay subtests and 26 environment/root/cgroup/guest/live-provider cases.
- Gate receipts: gate-final.txt, gate-final.json, gate-final.err, gate-summary.json. JSON SHA-256: c791f2e15c481feb4b9a991b13361a13f4fe05a6216e0663e5e24a20be8d815b.
- Exact gated source archive final.tar SHA-256: 728c2f767d598cc719a9e782b95c453be159e6cd4aa23d6882ab984d6962306e. Git pax comment equals the thesis SHA.
- Initial focused verification: 427 Go pass events, zero skips/failures in final selected runs; 16 local installer mock tests passed. Logs retain earlier red/failing attempts.
- First full gate pc29g at acbb357 was rejected: 5169 pass / 40 skip / 6 fail. Retained as gate-r1-acbb357.{txt,json,err,tar}; no failure was waived. Its boot was a7cb87a6-6265-4cb1-a183-0715b0530136. No WSL restart command was issued here; the between-run boot change was not diagnosed.
- Corrections: sequence a side-effecting statistics fixture assignment; update obsolete mandatory-panel/report expectations; atomically publish a fake cgroup event file. Production reset and T5/T7 effect behavior remain unchanged.
- Statistics correction: 55 pass events across five exact race runs; deliberately bypassing numeric verification failed; full panel-audit package 186 pass / 0 skip / 0 fail. fixture/logs/summary.json PASS.
- Report comparison: only 103 obsolete review deferrals and 34 explanatory reason strings changed. All statuses, 38 guest-only obligations and manifest digest stayed unchanged. Old/new exact hashes reproduced. Selected profile race tests: 17 passes, no skips/failures. profile-fix/RESULTS.json.
- Cgroup fixture: old exact test passed 100 repeats, so the initial race was not freshly reproduced. Corrected fixture passed 100 race repeats and 6 refusal checks. Atomic publication removes the source-level truncation window; no production relaxation.
- Independent code, documentation and bounded correction reviews passed. The repin script review's archive-hash finding was fixed and re-reviewed before execution.

## F14 and preserved inputs

- F14.env and patch 01 name the full gated thesis SHA. README, checklist, source addendum and handoff notices agree; older pins/counts are explicitly historical.
- Guarded updater checked HEAD, archive SHA-256 and git pax commit, final gate markers, raw event failures, stable boot and 40 skips before candidate writes. A pre-completion probe refused with all eight candidate files unchanged.
- Backups: repin-before/. Output: repin.log and repin-files.json. A second updater run left all eight file hashes unchanged. All three kit patches passed host-only dry-run checks; none was applied. kit/freezes/F14.env remains absent.
- Runtime-bundle measurer and repository bundle inputs remain identical to F13. The operator and CLI changed. Expected bundle equality is a source prediction, not a fresh bundle measurement. Binaries/image still need later authorized build and measurement.
- Pilot manifest unchanged: 30c2a344118cb30448dfcf75bfcb5ec45befa60c7f2e33517a7546de72513e1b. Frozen panel.json, tbound-panel.exe and FREEZE.sha256 are unchanged. Current panel preparation remains historical.
- Pre-existing user modifications preserved byte-for-byte: thesis AGENTS.md and master prompts/think.md. They are the only dirty files in their respective repos; product remains clean.

## Proof path and next thread

D35, D26, T5, T7 and AMB-02 are dated owner decisions. No independent advisor/examiner input, audit or sign-off remains; the supervisor is administrative only. No replacement human acceptance gate was added.

The critical path is deterministic construct checks and applicable retained guest/qualification witnesses, then preregistered H1 and its registered execution. H1 rules, N, scoring and caps are unchanged. corpus-digest and preregistration-record perform admission/binding checks; neither establishes complete CheckConstructs proof. The AI panel is optional supplementary analysis; AC1, raw agreement and kappa are diagnostics only. Supplied panel files remain subject to strict integrity checks and must be supplied together.

Next implementation follows docs/evaluation/t5-t7-preparation-2026-10-29.md: retain seven T5 cases/provenance, move injection to lower-trust tool-returned fixtures, prove encounter in the exact next provider request; establish explicit T7 no-effect evidence classes with one-time settlement and fresh-process replay, retaining conservative handling for ambiguity. AMB-02 keeps EXECUTED/PROCEEDS, row 1 score 0, with containment separate.

T5/T7 remain unimplemented and unvalidated. Later changes require reviewed tests, regeneration in new unfrozen directories of affected manifests, ContentDigest and hashes, and applicable dry runs/cost estimates; prepare a new panel directory only if the optional panel is chosen. Every changed thesis SHA requires a new full gate and F14 repin. Complete guest construct evidence and H1 remain unexecuted. The dd probe stays separate qualification.

No live panel, paid call, freeze, operational signing, installation, guest operation, or product-to-thesis merge occurred.
