# TBound task state

Updated 2026-10-14. Branch `codex/tbound-prototype`; implementation head `8e9293d`.
The `6a0a52a` source checkpoint and its `5051759` component receipt remain a
historical baseline. Claims, receipts, and open gates live in [docs/status.md](docs/status.md).

## Current state (2026-10-14)

- **Claims:** G1 false; containment not-established; H1 unexecuted. No positive
  governed production `serve --pi` launch, authentic signed-profile admission, or
  provider exchange through that route has occurred. Research claims are the last
  milestone, after core, deployment, and operations engineering.
- **Historical baseline:** `6a0a52a` (tree `81173e3f…`) is a local persistence
  checkpoint, not a verified release or the current source head. The receipt committed in `5051759`,
  [docs/evidence/component-checkpoint-6a0a52a.json](docs/evidence/component-checkpoint-6a0a52a.json),
  records Go 1.27.1 linux/amd64 build, vet, formatting, unit, and race commands with
  exit 0; each test suite reports 1,126 passed, 7 skipped, and 0 failed. This is
  component regression evidence only. The earlier handoff's matching figure remains
  unverified; the older 1,041/7/0 result is from a 349-file snapshot.
- **Current increments:** D20's atomic clean-restart plus pre-effect-retry
  reservation is committed in `d29a0ff`. Its
  [bounded receipt](docs/evidence/governed-combined-recovery-20261014.json) records
  positive fresh-process preparation/activation across two cycles and refusal at the
  restart cap, with retry-exhaustion and deadline guards. The tested archive overlays
  all four D20 files on `296957b`; this component result does not exercise the
  production entrypoint, full recovery binding, or actual drain. The production
  pin-composition
  [receipt](docs/evidence/production-pin-composition-20261014.json), committed in
  `8e9293d`, records fixed fresh-initializer/catalog digest pin composition and a
  successful production conversation return with a dummy test credential and zero
  provider HTTP exchanges. Source-bound Go 1.27.1 WSL unit/race, build, vet, and format
  checks passed; this does not qualify the full recovery binder or actual drain.
  D14-S2 terminal-accounting characterization is committed in `ad2479f`; its
  [receipt](docs/evidence/sqlite-terminal-faults-20261014.json)
  reports 71 verbose PASS events in each unit/race run on a distinct overlay containing
  the D20 workflowrecovery ledger files plus the S2 test. Its synthetic marker and Store
  pins do not exercise production `RunEffect` or a real provider/Store effect.
- **Historical manifest:** `/home/jeli2k/tbcand-81173e3f9981/candidate-files.sha256.tsv`
  has 368 entries. The count was verified; per-file hashes were not recomputed or
  compared. See the [status audit](../review-artifacts/tbound-status-audit-2026-10-14.md).
- **Environment:** the component test reached WSL and observed Landlock ABI 1; invoking
  the probe implicitly starts the distribution if it is stopped. Any earlier `Stopped`
  listing is historical to its observation time, not a current-state claim. The
  read-only [Hyper-V inventory](docs/evidence/host-inventory-20261014.json), committed
  in `8756cca`, is a historical snapshot that recorded `TBound-Ubuntu-2404` Running
  (8 vCPUs, 16 GiB fixed RAM,
  40 GiB disk) and no guest channel or ABI probe. ABI 4 is the last observed
  development-guest value and is unmeasured now. D09 requires ABI 8. The user has
  authorized replacing the development VM with a fresh disposable guest of similar
  resources. Replacement preflight (`WhatIf`) is verified. Two guest-creation attempts
  failed after preflight; the current host fix is prepared, with installation pending.
  No replacement guest has been created or qualified.
- **Blockers, in order:**
  1. Production recovery: the D20 atomic prepare/activate increment is committed,
     and the fresh-initializer/catalog digest pin fixes are recorded by the production
     pin-composition receipt. The full recovery binder, actual owner-cell drain, and
     interrupted-activation qualification remain open; the D20 drain verifier is a
     test stub.
  2. D14 remains partial. `0ab8b9f` covers five reservation-path faults with
     fresh-process checks; `ad2479f` adds three terminal-persistence fault cases and
     fresh-process verification. The S2 Store pins/verifier and fsynced effect marker
     are synthetic, and production `RunEffect` is not called. Reserved audit capacity
     is unimplemented; filesystem ENOSPC, genuine fsync faults, corruption, and
     declared-platform power-loss evidence remain open.
  3. Governed lifecycle: LC-04 and LC-05 are offline component cuts. ManagedStore/D06
     authority binding and provider-history replay through the production route remain open.
  4. Deployment: a fresh ABI-8 qualification guest (replacing the development VM is
     authorized by the [2026-10-14 decision](docs/decisions/2026-10-14-qualification-and-research-procedure.md),
     not yet performed), signed Pi CLI closure and host profile, and managed-service
     admission and recovery.
  5. Operator CLI: the fixture routes ignore `--workspace` and `--source`, and
     `--native-fixture` also ignores `--native-host-profile` (see
     [governed host setup](docs/governed-setup.md#cli-routes-and-exit-codes)). The
     documentation consolidation does not change this.
- **Protocol:** protocol 48 is historical. Protocol 51 is a draft, not frozen or
  registered. The AH02-1 authority conflict is unresolved. E07 remains separate.

### Milestones

1. **Core engineering:** qualify the full production D20 recovery binder and actual
   owner-cell drain. Complete remaining D14 capacity/fault work and
   LC-04/LC-05 end to end with independent oracles; collect publication, provenance,
   and observer evidence.
2. **Deployment engineering:** preserve required artifacts before disposing of the
   development VM; qualify a fresh ABI-8 guest and verify its live ABI, filesystem,
   runtime closure, and service profile. Freeze the signed closure and profile. Admit the
   managed service, drain prior owners and cells, and recover after a fresh service
   restart. Installer, update, and rollback.
3. **Operations engineering:** distinct operator states (ALLOW, DENY, UNKNOWN or
   quarantine, pause or exhaustion, publication complete); sanitized receipts and
   history; bounded shutdown; a diagnostics and recovery runbook; a narrow CLI pilot.
   The frontend follows the same authenticated rules and is not a claim gate.
4. **Research claims (last):** follow the [2026-10-14 decision](docs/decisions/2026-10-14-qualification-and-research-procedure.md):
   the user performs construct review with thesis-advisor guidance; multiple independent
   human construct reviewers and OSF/Zenodo accounts are not required. Record the actual
   procedure and limitations; do not describe self-review as independent or claim
   external registration. Freeze corpus, oracle matrix, calibration and scoring
   locally before execution. Perform the clean-machine G4 reproduction and execute
   H1, retaining the raw results and subsequent review records.
   The user-set provider is DeepSeek V4.1 Flash with a US$5 total spend cap; request,
   token, routing, and current price/cost limits still need to be frozen and enforced
   with durable worst-case reservation before any paid call. No paid run has occurred.
   Independent runtime/effect oracles and the engineering, security, calibration, and
   friction acceptance criteria remain requirements; see the
   [audit acceptance](../review-artifacts/tbound-status-audit-2026-10-14.md).

### Next steps

1. Verify the full production recovery binder, then qualify actual owner-cell drain
   and interrupted activation in the D20 recovery path.
2. Decide the fixture-route flag handling (reject or document) through a test-first
   code change.
3. Complete remaining D14 capacity, filesystem, corruption, and platform-fault work.
4. Before any paid provider evaluation, freeze DeepSeek V4.1 Flash routing, request and
   token limits, current price ceilings, and a full-trial cost estimate inside the
   US$5 total cap; enforce worst-case reservation per request. Do not reduce the
   protocol denominator if the approved budget cannot cover it.

### Rules

- Never read, print, copy, or commit `vm_start.txt`, provider or admin credentials, or
  real signing keys.
- The fresh disposable VM replacement is authorized by the 2026-10-14 decision.
- Preserve `supervisor/tbound` and `tbound.exe`. Build only to explicit private paths.
- Commit reviewed, verified increments with explicit staging. No blanket reset.
- Green results never establish G1, containment, or H1.
- Any future delegated work uses GPT-6 Luna with reasoning `max`.

### History links (source-bound)

- Pre-edit working-tree patch for the consolidation: [initial-working-tree.patch](../review-artifacts/tbound-completion-2026-10-14/initial-working-tree.patch)
  (sha256 `90517f28`, 67,447 bytes; equal to `git diff` against `6a0a52a` taken before
  these edits). It
  preserves the 401-line draft of this file.
- Prior committed task state: `git show 6a0a52a:TASK_STATE.md` (225 lines).
- Current decision record: [2026-10-14 qualification and research procedure](docs/decisions/2026-10-14-qualification-and-research-procedure.md)
  (commit `79d2b61`, blob `c9190aa7`, sha256 `d6c74338`).
- Increment receipts in [review-artifacts/tbound-resume-2026-10-13/](../review-artifacts/tbound-resume-2026-10-13/):
  `47a6d73` [guarded D09 launch](../review-artifacts/tbound-resume-2026-10-13/guarded-d09-launch-receipt-47a6d73.json)
  (sha256 `df2dd763`); `da09eea` [delta admission](../review-artifacts/tbound-resume-2026-10-13/delta-admission-receipt-da09eea.json)
  (`4e8ff9a3`); `b1ff84c` [operator scope](../review-artifacts/tbound-resume-2026-10-13/operator-scope-receipt-b1ff84c.json)
  (`42fb1915`); `09a1901` [promotion crash](../review-artifacts/tbound-resume-2026-10-13/promotion-crash-receipt-09a1901.json)
  (`458b7486`); `0e39619` [governed crash](../review-artifacts/tbound-resume-2026-10-13/governed-crash-receipt-0e39619.json)
  (`8b700ef4`); D09 receipts `385982d`, `91440d5`, `11da49b`, `2b5673e`; doctor receipts `abb32ce`, `5293d2c`.
- Thesis repository (`../MSE_MA_Thesis`, `codex/tbound-recovery-protocol`): commit
  `6834181`, recovery-documentation sync, [receipt](../review-artifacts/tbound-resume-2026-10-13/recovery-count-sync-receipt-6834181.json)
  (sha256 `e3e1196a`).
- Historical source identities: [docs/evidence/historical-sources.json](docs/evidence/historical-sources.json),
  27 records at source commit `29196d6`. The removed runtime-composition and adoption
  documents are retrievable at `29196d6:docs/native-runtime-composition.md` and
  `29196d6:docs/governed-linux-adoption.md`.
