# Current status

Updated 2026-10-14 for documentation reconciliation. Branch
`codex/tbound-prototype`; source checkpoint `6a0a52a`; its component receipt is
committed in `5051759`. This working status records implementation state and
blockers. It is not a qualification report.

## Claim boundary

- `G1=false`; containment is `not-established`; `H1` is unexecuted.
- No positive governed production `serve --pi` launch, authentic signed-profile
  admission, or real provider exchange through that route has occurred.
- Component tests, offline fixtures, package checks, capability probes, and
  development receipts do not establish G1, H1, containment, provider behavior, or
  thesis evidence. Development and fixture routes report `claim_bearing: false`.

## Current checkpoint and validation

- **Checkpoint:** `6a0a52a8af1d830ca20a3c8142dc9ae5a3dbab1b` (tree
  `81173e3f998195f9f394ff1e7153e554a13202a5`, parent `981bdd2`) is a local
  persistence checkpoint, not a verified release. The retained Linux manifest at
  `/home/jeli2k/tbcand-81173e3f9981/candidate-files.sha256.tsv` has 368 entries.
  The count was verified; no per-file hash recomputation or comparison is claimed.
  The [status audit](../../review-artifacts/tbound-status-audit-2026-10-14.md)
  records this manifest and its limits.
- **Fresh component receipt:** [`docs/evidence/component-checkpoint-6a0a52a.json`](evidence/component-checkpoint-6a0a52a.json)
  (receipt commit `5051759`, blob `b32ae107`, sha256 `42db5621`) records checks of
  source commit `6a0a52a` with Go 1.27.1 linux/amd64: build, vet, formatting, unit,
  and race commands all exited 0. Each suite reports 1,126 passed, 7 skipped, and
  0 failed test events. The export archive SHA-256 (`433a0ee5`) recomputes
  from `git archive`. The receipt's six listed log hashes match the logs, and the
  logged pass, skip, and fail counts and skip names match the raw JSON. This is
  component regression evidence from the WSL run whose logs sit under
  `review-artifacts/tbound-completion-2026-10-14/`. It does not establish
  G1, containment, production recovery, or H1. The receipt records a known
  restart/retry stale-receipt defect in this source.
- **Historical handoff figure:** a handoff reports 1,126 passes, 7 skips, and 0
  failures in both full suites at the committed checkpoint. No receipt from that
  handoff has been recovered, and it has not been independently validated. Attribute
  the figure to the handoff. The fresh receipt above reproduces these counts but
  does not validate the handoff run.
- **Older snapshot:** 1,041 passes, 7 skips, and 0 failures in both normal and race
  suites, on a 349-file snapshot (`/home/jeli2k/tbrcore.20261014.3/`, WSL only). These
  results do not establish the 368-file checkpoint.
- **Environment:** the component test reached WSL and observed Landlock ABI 1; invoking
  the probe implicitly starts the distribution if it is stopped. Any `wsl.exe -l -v`
  listing that showed `Stopped` is historical to its observation time, not a claim
  about current state. The earlier `Ubuntu/bintrue` timeout (prefix `85cec`) is also
  historical. The read-only Hyper-V [inventory](evidence/host-inventory-20261014.json),
  committed in `8756cca`, records `TBound-Ubuntu-2404` Running with 8 vCPUs, 16 GiB
  fixed RAM, and a 40 GiB disk. It had no guest channel; guest kernel and ABI were not
  probed. ABI 4 is the last observed development-guest value and is unmeasured now.
  D09 requires ABI 8. No clean guest has been registered or qualified. The
  [2026-10-14 decision](decisions/2026-10-14-qualification-and-research-procedure.md)
  authorizes disposing of the development VM and replacing it with a fresh disposable
  guest of similar resources. Replacement remains pending.
- **Documentation checks:** relative links, the 27-record historical source map, and
  `git diff --check`; this documentation pass does not run Go or adapter tests.

## Open gates

1. **Production CLI profile and launcher:** the offline Pi 0.87.1 CLI/RPC development
   route completes its scripted fixture flow with `provider_exchange=false`,
   `settlement=UNKNOWN`, `publication=not-attempted`, and `containment=not-established`.
   The guarded public D09 route is wired (commit `47a6d73`), but there is no authentic
   signed host admission and no positive public host launch.
2. **Managed Linux service:** provision and validate the dedicated service account,
   socket activation, source registration, signed profile, client/server handoff, and
   restart recovery. Source-capture code exists; a real signed-profile path and a
   positive service launch remain unqualified.
3. **Containment:** qualify the rootless OCI, cgroup, seccomp, no-new-privileges, and
   Landlock profile on a clean ABI-8 host, with independent effect oracles. ABI 4 is the
   last observed development-guest value and is unmeasured now; the live WSL component
   test reached ABI 1. Later HWE boots reached userspace but remain unqualified.
4. **Post-operation delta admission and Bash scope:** Store records a separate decision
   on the verified candidate before generation promotion. Canonical exact output paths
   and a protected admission journal bind the primary outcome. `--bash-output` and
   `--bash-no-outputs` carry scope through the authenticated service request. These
   component checks do not qualify production containment or publication.
5. **Publication and provenance:** exercise sealed-source provenance, command
   settlement, live publication, and recovery with independent workspace, process,
   and network observers.
6. **Research claims (last):** under the [2026-10-14 decision](decisions/2026-10-14-qualification-and-research-procedure.md),
   the user performs construct review with thesis-advisor guidance. Multiple independent
   human construct reviewers and OSF/Zenodo accounts are not required by this
   implementation procedure; no external registration is claimed. The thesis sources
   were not rewritten. Record the actual procedure and limitations, and freeze the
   executed protocol, corpus, analysis, and artifact identities locally with review
   records and raw results. Protocol 48 is historical. The draft 51-trial recovery
   amendment (17 scenarios across three replications: eight lifecycle, nine incident)
   remains unadopted, unfrozen, unregistered, and unexecuted. E07 remains separate.

## Blockers

- **D20 stale receipt (critical):** `ReservePreEffectRetry` advances the protected
  owner/root head before activation. The prior clean-restart receipt is then stale,
  and `sessionlaunch.RecoveryCandidate.Activate` rejects it. The atomic
  combined-reservation correction is in progress on this branch and is not yet
  verified or committed. Required: a failing-before/fixed-after regression through the actual
  preparation and activation path. It must cover fresh-process recovery, retained
  markers, interrupted activation, no double charge, third-retry and restart exhaustion,
  and preserved deadline.
- **D14 durability (partial):** `0ab8b9f` preserves SQLite error causes and adds
  five real reservation-path fault cases with separate fresh-process verification.
  Kernel EFBIG/SQLite IOERR and engine-limited SQLITE_FULL were exercised; package
  unit and race each passed 63 tests without skips or failures. See the
  [source-bound receipt](evidence/sqlite-reservation-faults-20261014.json).
  The earlier private test `19c2919c…` was replaced after it faulted the wrong
  store and used weak recovery checks. Terminal accounting, reserved audit capacity,
  filesystem ENOSPC, genuine fsync faults, corruption and power-loss evidence remain open.
- **Recovery continuity:** the fresh provider constructor refuses retained nonempty
  journals. Inert history, checkpoint/budget recovery and ManagedStore/D06 binding
  are implemented components; actual production continuation through them remains unqualified.
- **Protocol authority:** the AH02-1 authority conflict is unresolved.
- **CLI fixture routes:** `--native-fixture` ignores `--native-host-profile`,
  `--workspace`, and `--source`, and `--dev-fixture` ignores `--workspace` and
  `--source`. The production client and `--service` routes reject them. This was read
  from `supervisor/cmd/tbound/serve_linux.go` and not executed. A test-first code change
  is needed; this documentation review made none.

## Priorities

These checked-in priorities follow the implementation order. Research claims are last.

1. **Core engineering:** integrate the atomic D20 recovery fix with positive
   production recovery regressions; execute and integrate the D14 fault work; complete
   the governed lifecycle cuts end to end (LC-04 and LC-05) with independent oracles;
   publication, provenance, and observer evidence.
2. **Deployment engineering:** preserve required artifacts before disposing of the
   development VM, then qualify a fresh ABI-8 guest (replacement authorized
   2026-10-14, not yet performed); freeze the signed Pi CLI closure and host profile; admit the managed service; drain the prior owner
   and cells; recover after a fresh service restart; installer, update, and rollback.
3. **Operations engineering:** distinct operator states for ALLOW, DENY,
   UNKNOWN or quarantine, pause or exhaustion, and completed publication; sanitized
   receipts and history; bounded shutdown; diagnostics and recovery instructions; a
   narrow CLI pilot runbook. The frontend follows the same authenticated service rules
   and is not a claim gate.
4. **Research claims (last):** follow the decision's advisor-guided construct-review
   procedure, and freeze the corpus, adaptation records, oracle matrix,
   static and adaptive P/C calibration, and scoring algorithm locally in version
   control, and retain review records and raw results. Perform the clean-machine G4
   reproduction. Describe the actual review procedure: self-review is not independent,
   independent human construct reviewers and registration are not required by the
   decision, and no external registration is claimed. Define and record the OpenRouter
   model, request and token limits, and monetary budget before any paid provider
   evaluation. Then
   execute H1: AC-EVQ mandatory obligations with raw artifacts, zero forbidden effects in
   299 Configuration C trajectories plus paired P/C calibration, and AC-Friction ORR at
   most 10% on 100 compliant tasks with B and C each completing at least 90 of 100 (per
   the [audit acceptance](../../review-artifacts/tbound-status-audit-2026-10-14.md)).
   Independent runtime and effect oracles, the engineering gates, the D14/D20
   constraints, and the security, calibration, and friction criteria remain requirements.
   G1 and H1 stay unestablished until their evidence is executed and reviewed.

## Historical checkpoints (not current)

Counts are as reported for each cited snapshot. Earlier candidate captures are not
treated as evidence for `6a0a52a`.

| Checkpoint | Reported result | Source |
|---|---|---|
| `47a6d73` guarded D09 launch | Linux unit and race 925 each, 7 skips; Windows 281 passed, 13 skipped | [receipt](../../review-artifacts/tbound-resume-2026-10-13/guarded-d09-launch-receipt-47a6d73.json) (sha256 `df2dd763`) |
| Reviewed recovery crash fixtures | Linux unit and race 935 each, 7 skips; focused race 36 events over three runs; Windows 287, 13 skips | no receipt file in this repo |
| LC-04 process-crash cut | Linux unit and race 986 each, 7 skips; a first race run was a setup error (snapshot missing root `docs/`), not a test result | WSL logs `/home/jeli2k/tbound-lc04-20261013-A7c/` (not in repo) |
| LC-05 Bash runner-entry cut | Linux unit and race 987 each, 7 skips; focused race test passed | WSL logs `/home/jeli2k/tblc05.4_prr5zv/logs/` (not in repo) |
| Delta admission `da09eea` | Linux unit and race 972 each, 7 skips; Windows 294 passed, 13 skipped | [receipt](../../review-artifacts/tbound-resume-2026-10-13/delta-admission-receipt-da09eea.json) (sha256 `4e8ff9a3`) |
| Operator scope `b1ff84c` | Linux CLI unit and race 230 each, 2 skips | [receipt](../../review-artifacts/tbound-resume-2026-10-13/operator-scope-receipt-b1ff84c.json) (sha256 `42fb1915`) |
| Store promotion crash `09a1901` | Store package unit and race 85 each, no skips | [receipt](../../review-artifacts/tbound-resume-2026-10-13/promotion-crash-receipt-09a1901.json) (sha256 `458b7486`) |
| Governed crash `0e39619` | Full race 978 passed, 0 failed, 7 skipped | [receipt](../../review-artifacts/tbound-resume-2026-10-13/governed-crash-receipt-0e39619.json) (sha256 `8b700ef4`) |
| Governed continuation refusal | Full race 986 passed, 0 failed, 7 skipped; recovery-documentation sync is thesis-repo commit `6834181` | [receipt](../../review-artifacts/tbound-resume-2026-10-13/recovery-count-sync-receipt-6834181.json) (sha256 `e3e1196a`) |
| Transcript replay projection (2026-10-13) | Normal and race 1,008 each, 0 failed, 7 skipped; 349-file snapshot | WSL logs `/home/jeli2k/tbrcore.20261013.4/logs/` (not in repo) |
| Private Git reopen (2026-10-14) | Normal and race 1,041 each, 0 failed, 7 skipped; 349-file snapshot | WSL logs `/home/jeli2k/tbrcore.20261014.3/` (not in repo) |
| Earlier candidate capture (audit) | Build, vet, format, and unit recorded; no completed race command; its unit and race stdout were empty | `/home/jeli2k/tbq-candidate-20261014T145517Z-109039/capture.json` (WSL; described in the [audit](../../review-artifacts/tbound-status-audit-2026-10-14.md)) |

An earlier race runner (`84675`) ended with exit 1 and empty output at 15:47:43, and no
race exit receipt was recovered for it. That run is not treated as evidence. The
fresh receipt above is the current record for `6a0a52a`.

Earlier D09 and doctor receipts for the Linux runtime path are kept in
`review-artifacts/tbound-resume-2026-10-13/` (`385982d`, `91440d5`, `11da49b`,
`2b5673e`, `abb32ce`, `5293d2c`). They are historical component checks.

## Related

- [Task state](../TASK_STATE.md) for the current working state and milestone order.
- [2026-10-14 qualification and research procedure](decisions/2026-10-14-qualification-and-research-procedure.md)
  (commit `79d2b61`): authorizes the replacement guest and fixes the research order.
- [Architecture](architecture.md) for the runtime seams.
- [Governed host setup](governed-setup.md) for operator prerequisites and CLI routes.
- [Historical source map](evidence/historical-sources.json): 27 records, source commit `29196d6`.
- [Prior audit](../../review-artifacts/tbound-status-audit-2026-10-14.md) (sha256 `51246c45`).
- Decisions: [CLI reproduction](decisions/2026-10-12-cli-reproduction.md) and the
  proposed [recovery-boundary amendment](decisions/2026-10-13-recovery-boundary-amendment.md),
  which supersedes the older recovery count only if adopted after review.
