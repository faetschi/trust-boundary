# TBound takeover results, 2026-10-29

Status: authorized takeover delivery completed. Thesis and product are gated, pushed and bundled. The F14 candidate is pinned, with the freeze and owner/advisor decisions still pending. No freeze, signing, installation, guest work, live panel or paid provider calls were performed.

## Thesis

- Exact gated and pushed SHA: `56cf99c212514454e5fef2e455226bb3c454d9a5`.
- Full gate: 5217 pass / 40 skip / 0 fail, including subtests. Boundary self-tests, gofmt, Linux build/vet, Windows cross-build/vet and race suite passed.
- Evidence: `F:/TBoundAssets/gates-20261027/gate-takeover-thesis-56cf99c-short.txt` and `thesis-56cf99c-test.json` here.
- First long-label attempt failed 4 test events at Unix socket bind because TMPDIR exceeded AF_UNIX path length. No source fix was needed. The unchanged SHA passed with label `t56cf99c`.
- Forty skips comprise the existing 26 guest/root/cgroup/live checks and 14 pilot static-replay subtests unable to execute Node. These remain unverified.
- Git bundle: `C:/Users/Admin/Desktop/FH/Master Software Engineering/MA Thesis/handoff-20261026/trust-boundary-20261029.bundle`.
- Bundle SHA-256: `eef5fa4930cd9e8159c8c2088c63a97d15935d8347aa91e151af74547289a9a2`.
- Bundle verification reports complete history and the exact thesis branch ref. The pre-existing AGENTS.md edit remains untouched.

## Product

- P1-P3 audit branch `8bf704f` was independently reviewed and merged as `a7f3a601064320ff261c63c3d4825640485fa2cc`; merge state recorded by `43dc4928178f2f4d8876e10f6b46e11c48cfb53b`.
- Full baseline gate on immutable `43dc492` archive passed 5745 pass / 31 skip / 0 fail and was pushed. Its complete-history audit snapshot bundle SHA-256 is `08b49e3c27ab771d1cc1a3a907161c6d6bf703ad88cb4b9f4e18ed92311dfb4d`. The earlier long-label product attempt was aborted and supplies no gate result.
- P4 port: `b7eefe3144b494bd4e5271f04a32ca638c4b9721`, with protected alias denial/continuation/predecessor replay coverage. P5 port is committed as `b824d0d0291e546d3ecf1457ae56addfe1865022`.
- Red tests reproduced unknown alias writes and replay-tip mismatch before ports. Focused green product suite: 161 pass / 3 skip / 0 fail. Focused recovery: 6 pass / 0 skip / 0 fail.
- Providerbridge focused harness failed twice at journal setup because cp-a copied overlay directory mode0777 onto the export root despite umask. A direct mode check identified the cause; cp-r plus a700 assertion now prevents it. The fresh g3-providerbridge run passed 214 tests / 0 skips / 0 fails. Both failed logs remain retained.
- Two independent static reviews accepted the ports after the protected-alias regression was added. The first combined e59f45b full gate failed5755/31/4 at a new fixture directory mode before production assertions; commit41ddfde only normalizes that mode. Eight race checks underumask077 then passed, and a third independent static review accepted the fixture-only fix. The prior archive and raw logs are retained as product-e59f45b-failed-*. The41ddfde fullgate ended5758/31/1. The alias fixture passes; TestMountNamespaceScanSkipsOtherUsersProcessesItCannotInspect failed on a permission-denied /proc/PID/fd read. Evidence is product-41ddfde-mountns-failed-*. The same error was reproduced with an owned zombie in five fresh processes. The independently reviewed test-only correction `df8b655` passed ten fresh race-enabled zombie checks, ownership mutation checks, 35 focused race checks and the piruntime race suite (403 pass / 1 existing skip / 0 fail). It adapts thesis `9759b42` and `aee0722` while failing, rather than skipping, inconclusive ownership. Production scanning remains unchanged. PID 12822's historical state remains unproven. The fresh isolated full gate on `53c577b` passed; final delivery is recorded below.
- Final gated and pushed SHA: `53c577b32cf72df3d5665d2646a0ed96711465c1`.
- Final clean-export full gate: **5764 pass / 31 skip / 0 fail**, including subtests; 57 packages passed and 3 skipped. All six gate markers passed, the boot ID stayed constant, and the skip set exactly matches baseline `43dc492`.
- Evidence: `product-final-gate.txt`, `product-final-test.json`, `product-final-gate-summary.json` and the exact `product-final.tar` archive. Final remote branch refs are in `final-remote-refs.txt`.
- Final Git bundle: `C:/Users/Admin/Desktop/FH/Master Software Engineering/MA Thesis/handoff-20261026/trust-boundary-product-20261029.bundle`.
- Final bundle SHA-256: `698be1a55283602392b9d3c004a3fe82c7613d089756844e93e2e3249f634ece`. Complete history verified; a fresh bare restore at the exact branch SHA passed `git fsck --full`.
- Both primary bundles and the intermediate audit snapshot have full SHA-256 hashes in `handoff-20261026/TBOUND-20261029-SHA256SUMS`. The product tree is clean; the thesis retains only its pre-existing AGENTS.md edit.
- P6 is unchanged. Options and P7-P16 triage are in `trust-boundary-product/docs/product-audit-handoff-2026-10-29.md`.
- The new P5 regression verifies repeated replay in one process, matching the recovery path. No additional fresh-process crash fixture was added for that exact sequence.

## F14 and panel handoff

- Candidate `F:/TBoundAssets/recovery-20261015/F14-candidate-20261027` is pinned, not frozen. F14.env, patch01 and README name the exact gated thesis SHA.
- Patch01 dry-run against the kit passed. Kit patches remain unapplied. Listed runtime bundle inputs and the runRuntimeBundleDigest function are unchanged from F13, proved in f14-input-check.txt. This does not cover every freeze input: tbound-op changed and must be taken from the pinned export. No runtime bundle or image was rebuilt or remeasured.
- Panel binary/config hashes match their frozen values; ten dependency package directories plus go.mod/go.sum are unchanged between `66a3dea` and `56cf99c`. Paths are in `panel-dependency-paths.txt` here.
- Candidate README, OWNER-CHECKLIST, EVIDENCE-NEXT and TAKEOVER-HANDOFF, plus PANEL-READY, mark T5/T7 as blockers before paid panel under the current user instruction. Frozen panel artifacts and corpus were not changed.
- Audit sample remains OPEN. Advisor items 1/3 need confirmation of existing owner decisions; item 4/T7 and item 5/AMB-02 remain open. T5 needs owner/advisor construct judgment. Content/rubric changes require updated digest, manifest hash and dry-run/prep before any paid run.
- The dd probe's freeze placement and the unconfirmed guest IP remain owner items. Freeze/signing/guest steps require a later owner session.
- No product-to-thesis merge was attempted. Proposed after freeze: integration/f14-product from the frozen thesis SHA, merge the gated product there, preserve thesis evidence and product soft refusals, independently review and run combined gates.

## Remaining release work

P6 contract decision and recovery path; real Pi worker per turn; host qualification; publication mismatch handling; prompt/evidence versioning. P14 receipt bounds are the next deferred engineering priority because oversize receipts also lead to unknown. Its producer/catalog identity bounds differ: 256 versus 128 bytes; align that rule and derive an encoder-based ceiling rather than guessing a cap. P7 serial scheme and P13 stdout compatibility need decisions before changes. Other deferred findings retain their recorded dispositions.

Decision trail: decisions.tsv. Raw focused logs and prepared exports: p45/. No prior audit thread was resumed and no new audit was run.

## Evidence review

Reviewed by Claude Haiku5.5. Its material flags were addressed with retained socket error JSON, explicit current measurer proof, updated stale candidate instructions, and the encoding recovery record. The repin script briefly read UTF8 using a Windows default decoder, then restored four files and reran with explicitUTF8; independent byte checks found no corruption. Decision timestamps record append time, not exact action time. Boot IDs changed between completed gates without any explicit shutdown/restart command; each gate is checked for one stable boot internally. The reviewer did not read the full root transcript; parent directly verified gate JSON, remote refs and complete-history bundle verification.
