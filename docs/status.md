# Current status

Updated 2026-10-15. Branch `codex/tbound-prototype`; implementation head `11c9899`.
The source checkpoint `6a0a52a` and its component receipt in `5051759` are a
historical baseline. This working status records implementation state and blockers;
it is not a qualification report.

## Claim boundary

- `G1=false`; containment is `not-established`; `H1` is unexecuted.
- No positive governed production `serve --pi` launch, authentic signed-profile
  admission, or real provider exchange through that route has occurred.
- Component tests, offline fixtures, package checks, capability probes, and
  development receipts do not establish G1, H1, containment, production provider
  behavior, or thesis evidence. The D20 prepare/activate component tests do not
  exercise the production entrypoint or full recovery binder. The separate production
  pin-composition receipt covers the fresh initializer only. Development and fixture
  routes report `claim_bearing: false`.

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
  G1, containment, production recovery, or H1. The receipt predates D20 and records
  the restart/retry stale-receipt defect in that source; the prepare/activate increment
  addressing it is committed in `d29a0ff` below.
- **D20 combined recovery:** [receipt](evidence/governed-combined-recovery-20261014.json),
  committed in `d29a0ff`, records a failing-before/fixed-after result and fresh-process
  preparation/activation across two restart/retry cycles, then refusal at the restart
  cap. The D20-only package tests report 39 top-level workflowrecovery tests (66
  verbose nodes including subtests) and 15 selected cmd/tbound tests (34 nodes).
  Retry exhaustion consumes no restart, the deadline guard refuses before reservation,
  and crash-time evidence prefixes remain unchanged. The source archive uses base
  `296957b` plus the four D20 files. This does not qualify actual owner-cell drain: the
  test's drain verifier is a stub.
- **Production pin composition:** [receipt](evidence/production-pin-composition-20261014.json),
  committed in `8e9293d`, records a baseline-red/current-green fresh-initializer
  regression and Go 1.27.1 WSL unit/race, build, vet, and format checks. The real
  initializer returns a production conversation with a dummy test credential; the
  provider journal remains empty. This does not qualify the full recovery binder or
  actual owner-cell drain.
- **D14-S2 terminal accounting:** [receipt](evidence/sqlite-terminal-faults-20261014.json),
  committed in `ad2479f`, records 71 verbose PASS events in each workflowrecovery unit
  and race run. Its composed source overlay contains the D20 workflowrecovery ledger
  files and S2 test, but excludes the D20 cmd/tbound integration files. Three
  terminal-persistence faults are verified from separate processes: owner-write EFBIG
  / SQLite IOERR 778, engine SQLITE_FULL 13 via `max_page_count` (not filesystem
  ENOSPC), and root-anchor write failure. The test uses synthetic Store pins/verifier
  and an fsynced marker; it does not invoke production `RunEffect` or a real external
  provider/Store effect. It is characterization, not D14 completion.
- **Environment:** the component test reached WSL and observed Landlock ABI 1; invoking
  the probe implicitly starts the distribution if it is stopped. Any `wsl.exe -l -v`
  listing that showed `Stopped` is historical to its observation time, not a claim
  about current state. The earlier `Ubuntu/bintrue` timeout (prefix `85cec`) is also
  historical. The read-only Hyper-V [inventory](evidence/host-inventory-20261014.json),
  committed in `8756cca`, is a historical snapshot that recorded `TBound-Ubuntu-2404`
  Running with 8 vCPUs, 16 GiB fixed RAM, and a 40 GiB disk. It had no guest channel;
  guest kernel and ABI were not
  probed. ABI 4 is the last observed development-guest value and is unmeasured now.
  D09 requires ABI 8. The disposable seed was finalized in `fdc31f2`: the exact
  powered-off VM, standalone dynamic 40 GiB disk hash, Secure Boot, and absence of
  checkpoints are recorded in the [provisioning receipt](evidence/abi8-provisioning-preflight-20261014.json).
  This is candidate metadata; installation correctness and live ABI8 remain
  unverified. The final 8-vCPU/16-GiB guest has not been cloned or qualified.
  The development VM remains untouched. The
  [2026-10-14 decision](decisions/2026-10-14-qualification-and-research-procedure.md)
  authorizes disposing of the development VM and replacing it with a fresh disposable
  guest of similar resources.
- **Documentation checks:** relative links, the 27-record historical source map, and
  `git diff --check`; this documentation pass does not run Go or adapter tests.

## Open gates

1. **Production CLI profile and launcher:** the offline Pi 0.87.1 CLI/RPC development
   route completes its scripted fixture flow with `provider_exchange=false`,
   `settlement=UNKNOWN`, `publication=not-attempted`, and `containment=not-established`.
   The guarded public D09 route is wired (commit `47a6d73`), but there is no authentic
   signed host admission and no positive public host launch.
2. **Recovery entrypoints and managed Linux service:** D20's atomic reservation is
   tested through the prepare/activate component path. Fresh-initializer and catalog
   digest fixes are recorded by the production pin-composition receipt; the full
   recovery binder and actual owner-cell drain remain unqualified. Then provision and
   validate the dedicated service account, socket activation, source registration,
   signed profile,
   client/server handoff, and restart recovery. A real signed-profile path, positive
   service launch, and actual owner-cell drain remain unqualified; the D20 drain verifier
   is a no-op stub.
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
   were not rewritten. The user selected DeepSeek V4.1 Flash
   (`deepseek/deepseek-v4.1-flash`) with a US$5 maximum total spend. Routing, current
   price ceilings, request/token limits, and a full trial cost estimate remain pending;
   no paid run has occurred. Reserve each request's worst-case cost against the shared
   durable budget before sending, including pilot, retry, calibration, and evaluation.
   Protocol 48 is historical. The draft 51-trial recovery amendment (17 scenarios
   across three replications: eight lifecycle, nine incident) remains unadopted,
   unfrozen, unregistered, and unexecuted. E07 remains separate.

## Blockers

- **D20 production recovery (partial):** the atomic restart+retry reservation before
  activation is implemented in `d29a0ff`; its bounded receipt records the failing-before
  case and positive fresh-process prepare/activate path. Fresh-initializer pin
  composition fixes are recorded in the `8e9293d` receipt. Full recovery-binder
  verification, actual owner-cell drain, and interrupted-activation qualification
  remain open; the D20 drain verifier is a stub.
- **D14 durability (partial):** `0ab8b9f` preserves SQLite error causes and adds
  five real reservation-path fault cases with separate fresh-process verification.
  Kernel EFBIG/SQLite IOERR and engine-limited SQLITE_FULL were exercised; package
  unit and race each passed 63 tests without skips or failures. See the
  [reservation-fault receipt](evidence/sqlite-reservation-faults-20261014.json).
  The S2 terminal-persistence characterization in `ad2479f` adds three fresh-process
  cases; the composed workflowrecovery unit/race runs each report 71 verbose PASS
  events. S2 uses synthetic Store pins/verifier and an fsynced test marker, with no
  production `RunEffect` or real external effect.
  `c18a2b6` adds real owner/root SQLite header-corruption cases with separate-process
  refusal checks and preserves the root-open error cause; see the
  [corruption receipt](evidence/sqlite-corruption-20261014.json).
  `9fb5dd6` adds an opt-in isolated ext4 experiment: real ENOSPC, release of a
  physical 64 MiB reserve, an audit frame and terminal writes, and fresh-process
  verification including refusal after a crash following reserve release. See the
  [capacity experiment](evidence/reserved-capacity-ext4-experiment-20261014.json).
  This is feasibility evidence, not a production capacity guarantee: a global
  durable ticket, enforced worst-case bounds, exclusion of other protected-volume
  writers, and declared-platform backing/power-loss qualification remain open.
  `11c9899` adds [syscall fsync-EIO characterization](evidence/sqlite-fsync-faults-20261015.json):
  two exact-WAL-fd fault cases and an unfiltered control pass in Linux unit/race,
  with zero skips. Fresh processes reject the unanchored terminal owner heads and
  admit no second callback. The root case faults clock high-water persistence
  before terminal anchor CAS. This does not establish physical media or power-loss
  safety; production terminal accounting and storage qualification remain partial.
- **Recovery continuity:** the fresh provider constructor refuses retained nonempty
  journals. Inert history, checkpoint/budget recovery, and ManagedStore/D06 binding
  exist as components; provider-history replay and continuation through the production
  route remain unqualified.
- **Protocol authority:** the AH02-1 authority conflict is unresolved.
- **CLI fixture input scope:** both fixture routes reject explicitly supplied
  `--native-host-profile`, `--workspace`, and `--source`, including empty values.
  The [bounded receipt](evidence/fixture-route-cli-flags-20261014.json) records the
  red/green regression and package checks. Broader cancel/history/doctor CLI work
  remains open.

## Priorities

These checked-in priorities follow the implementation order. Research claims are last.

1. **Core engineering:** qualify the full production D20 recovery binder and actual
   owner-cell drain; continue the remaining D14 capacity/fault work;
   complete LC-04 and LC-05 end to end with independent oracles; collect publication,
   provenance, and observer evidence.
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
   decision, and no external registration is claimed. The user selected DeepSeek V4.1
   Flash with a US$5 total cap. Freeze provider routing, current price ceilings,
   request/token limits and the full-trial cost estimate, and enforce a durable
   worst-case reservation per request before any paid call. Then
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
baseline receipt above is the current record for `6a0a52a`.

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
