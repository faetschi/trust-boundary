# TBound task state

Updated 2026-10-14. Branch `codex/tbound-prototype`; source checkpoint `6a0a52a`;
component receipt committed in `5051759`. Claims, receipts, and gates live in
[docs/status.md](docs/status.md). Verify against current code before acting on an item here.

## Current state (2026-10-14)

- **Claims:** G1 false; containment not-established; H1 unexecuted. No positive
  governed production `serve --pi` launch, authentic signed-profile admission, or
  provider exchange through that route has occurred. Research claims are the last
  milestone, after core, deployment, and operations engineering.
- **Checkpoint:** `6a0a52a` (tree `81173e3f…`) is a local persistence checkpoint, not
  a verified release. The receipt committed in `5051759`,
  [docs/evidence/component-checkpoint-6a0a52a.json](docs/evidence/component-checkpoint-6a0a52a.json),
  records Go 1.27.1 linux/amd64 build, vet, formatting, unit, and race commands with
  exit 0; each test suite reports 1,126 passed, 7 skipped, and 0 failed. This is
  component regression evidence only. The earlier handoff's matching figure remains
  unverified; the older 1,041/7/0 result is from a 349-file snapshot.
- **Historical manifest:** `/home/jeli2k/tbcand-81173e3f9981/candidate-files.sha256.tsv`
  has 368 entries. The count was verified; per-file hashes were not recomputed or
  compared. See the [status audit](../review-artifacts/tbound-status-audit-2026-10-14.md).
- **Environment:** the component test reached WSL and observed Landlock ABI 1; invoking
  the probe implicitly starts the distribution if it is stopped. Any earlier `Stopped`
  listing is historical to its observation time, not a current-state claim. The
  read-only [Hyper-V inventory](docs/evidence/host-inventory-20261014.json), committed
  in `8756cca`, records `TBound-Ubuntu-2404` Running (8 vCPUs, 16 GiB fixed RAM,
  40 GiB disk) and no guest channel or ABI probe. ABI 4 is the last observed
  development-guest value and is unmeasured now. D09 requires ABI 8. The user has
  authorized replacing the development VM with a fresh disposable guest of similar
  resources; replacement remains pending.
- **Blockers, in order:**
  1. D20 stale receipt: `ReservePreEffectRetry` advances the owner/root head before
     activation, so `sessionlaunch.RecoveryCandidate.Activate` rejects the prior
     clean-restart receipt. The atomic correction is in progress on this branch;
     its final verification and commit are pending.
  2. D14 SQLite fault acceptance, reserved capacity and power-loss evidence remain
     open. Fault integration and targeted Linux verification are in progress.
  3. Governed lifecycle: LC-04 and LC-05 are offline component cuts. Production
     recovery, ManagedStore/D06 authority binding, and provider-history replay are open.
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

1. **Core engineering:** apply the atomic D20 fix with failing-before and
   fixed-after regressions through preparation and activation. Execute and integrate
   the D14 fault work. Complete LC-04 and LC-05 end to end with independent oracles.
   Publication, provenance, and observer evidence.
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
   Independent runtime/effect oracles and the engineering, security, calibration, and
   friction acceptance criteria remain requirements; see the
   [audit acceptance](../review-artifacts/tbound-status-audit-2026-10-14.md).

### Next steps

1. Implement the D20 atomic fix on this branch test-first, then run pinned Linux
   verification.
2. Decide the fixture-route flag handling (reject or document) through a test-first
   code change.
3. Before any paid provider evaluation, define and record the OpenRouter model, request
   and token limits, and monetary budget.

### Rules

- Never read, print, copy, or commit `vm_start.txt`, provider or admin credentials, or
  real signing keys.
- The fresh disposable VM replacement is authorized by the 2026-10-14 decision. Define
  and record a budget before any paid provider evaluation.
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
