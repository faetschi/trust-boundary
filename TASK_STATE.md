# TBound task state

Updated 2026-10-13. Implementation branch: codex/tbound-prototype.
Commit reviewed, verified increments; use explicit staging, no blanket reset.
Receipts and detailed logs: ../review-artifacts/tbound-resume-2026-10-13/.

## Claim boundary

- CLI-first read/write/edit/bash using official Pi CLI 0.87.1 and mediation
  extension; no expanded provider tool surface or unmediated fallback.
- G1=false; containment is not-established; H1 unexecuted. No qualified
  successful public production launch or provider exchange through it.
- Hyper-V development guest ABI4; WSL component host ABI1; D09 requires exact
  ABI8. No VM created, provisioned, registered or qualified in this work.
- Thesis draft2c7bf4d is isolated on codex/tbound-recovery-protocol in
  ../MSE_MA_Thesis; main unchanged. Draft51 recovery trials (8 lifecycle plus
  9 incidents, x3) is not frozen/registered; E07 conformance remains separate.

## Completed increments

| Commit | Scope |
|---|---|
| a281699 | Windows ACL/SID bounds |
| 178acdd | Official CLI RPC and worker lifecycle |
| 385982d / 91440d5 | D09 preflight and consumer proof before bootstrap |
| 11da49b / 2b5673e | UNKNOWN launch/admission cleanup ownership retention |
| 22c7cfd / 282bb5e | Exact ABI gates and minimal Ubuntu guest setup recipe |
| ace9592 / 3d30fcf | Independent crash markers and trusted audit hooks |
| 47a6d73 | Guarded public D09 launch and single-owner drained RPC stdout |
| 18c9e0c | Installer rejects incomplete updates before replacing tools |
| abb32ce | Doctor reports live exact-ABI prerequisite |
| 5293d2c | Actionable full profile paths and presence-only diagnostics |

RPC failures require prompt command and exact active request ID. Close failures
remain observable. UNKNOWN retains cleanup/admission owners and blocks
publication. Launch proof is journaled before the admitted prompt is sent.
Minimal Ubuntu Server ISO is the supported local guest recipe; setup artifacts
are preparation only. Media custody and exact ABI checks precede registration.

## Current post-operation admission increment

- Applied Store-owned protected delta-admission journal, full verified input/
  candidate manifests and exact scope policy, before generation promotion.
  Primary outcome references the separate decision. DENY retains evidence
  without advancing gN/ledger; UNKNOWN quarantines without effect replay.
- Canonical v2 intent requires explicit scope; evidence/bundle are v2. Recovery
  and replay verify exact cross-journal references and consume records once.
  Legacy roots missing admission evidence are refused without inference.
- Host authority derives typed scopes and records v2 preauthorization. Durable
  executor clones canonical Bash scope; nil disables Bash, [] permits no changes.
  Terminal-origin verification now accepts only the matching v2 scoped record.
- D09 verifies reconstructed Store evidence; execution-only receipt digest
  stays distinct from the scope-inclusive Store argument digest.
- Real regressions reproduced DENY accepting forged next generation/order and
  evidence-reference omission/aliasing; corrected package/race checks pass.
  Suspected event-outcome acceptance gap was disproved: audit already rejects
  the malformed frame; explicit framing checks are defense in depth only.
- Final full Linux unit/race: each972 passes, 0 failures, 7 skips, 25 packages
  passed; vet clean. Windows294 passes, 0 failures,13 skips; vet clean.
  Logs: delta-admission-final-{unit,race}.jsonl / -vet.txt in isolated Linux logs;
  Windows log in review-artifacts. All 28 tested source files hash-match the WSL copy; committed da09eea.
  Receipt: delta-admission-receipt-da09eea.json.
- Store/host and final independent Luna max safety/integration/thesis reviews
  accepted. Final reviewer checked versioned origin/test consumers; stale
  status documentation has been reconciled.
- Operator --bash-output / --bash-no-outputs wiring is applied and verified;
  Linux CLI unit/race each230 passes,0 failures,2 skips; vet clean. Seven
  tested source files hash-match. Windows source is unchanged from da09eea. Scope travels through signed-UID-authenticated service
  requests and payload journal; the provider cannot set it. Nil disables Bash.
  Independent Luna max review accepted. Focused race tests plus same-ID/different-
  scope socket conflict test pass with no rerun or journal change. CLI docs synced.

## Remaining milestones

1. Admission gate checkpoint completed as da09eea.
2. Checkpoint verified operator-scope wiring; advance the next recovery milestone.
3. Complete trusted lifecycle/provider/Store/checkpoint cuts and independent
   oracles. LC04/05/06 remain incomplete end to end; hooks are component evidence.
4. Qualify clean registered ABI8 host, signed CLI closure/profile, service/source
   admission, containment/settlement and real end-to-end publication/recovery.
5. Freeze corpus, calibration, oracle matrix and analysis before H1 registration.
6. Standalone fresh installation, recovery/upgrades, release custody and support.

## Verification and preservation

- READ-ONLY Luna max preparation/review; parent applies, tests, commits.
  Native delegation avoids prior T3 approval-required PowerShell prompt loop.
- Isolated Linux source: /home/jeli2k/tbound-resume-20261013.kRuoVd/source;
  Go: /home/jeli2k/tbound-go1.27.1.zkiVXZ/bin; private short TMPDIR:
  /home/jeli2k/tb.n2XBZV. Use umask077; long paths exceed AF_UNIX limits.
- Preserve supervisor/tbound and tbound.exe. Build only to private explicit paths.
  Never access vm_start.txt, provider/admin credentials or real signing keys.
  No real provider/headline run or unauthorized host provisioning.
- Exclude pre-existing deletions: docs/native-pi-product-blockers-2026-10-07.md,
  docs/next-evaluation-unlocks-2026-10-07.md,
  docs/tbound-readiness-status-2026-10-08.md,
  docs/thesis-gap-ledger-2026-10-05.md, docs/verifier-blockers-2026-10-05.md.

## Active next-milestone review

Native READ-ONLY Luna max next_milestone_thesis_check is checking trajectory
against authoritative thesis MDs and selecting one concrete next recovery/product
increment. No thesis freeze, experiment, host provisioning or key use is authorized.