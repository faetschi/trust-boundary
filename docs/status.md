# Current status

Updated 2026-10-13. This page records the current implementation and blockers;
it is not a qualification report.

## Claim boundary

- `G1=false`; containment is `not-established`.
- There has been no positive governed production Pi CLI launch, real provider
  exchange through that candidate path, independent forbidden-effect result,
  or thesis experiment. A retained historical receipt records a narrower real
  development-provider exchange; it does not qualify this CLI path.
- Go component tests, offline fixtures, package checks, and capability probes
  do not establish H1, G1, provider behavior, containment, or thesis evidence.

## Open gates

1. **Production CLI profile and launcher:** an isolated Pi 0.87.1 extension
   test passed on the existing VM with four proxy tools and a deny-only mock
   host. The Go `--dev-fixture` also completed its offline scripted Pi CLI/RPC
   flow: four proposals/results, five provider turns, generations g0→g1→g2,
   and worker-ready, prompt-admitted, turn-completed true. Its receipt still
   reports `provider_exchange=false`, `settlement=UNKNOWN`,
   `publication=not-attempted`, `containment=not-established`, and
   `claim_bearing=false`; these development checks do not qualify production.
   The profile scaffold and runtime bindings describe the Pi 0.87.1 CLI RPC
   contract with the explicit mediation extension. The D09 profile validator
   and both D09 and legacy launcher argv guards match `PiWorkerCellArguments()`.
   The closure stager checks installed versions against exact npm-shrinkwrap
   locations, including nested packages, and records the lock digest from the
   verified staged lockfile. It materializes in-package file symlinks as
   regular files to match the production verifier's unchanged no-symlink policy;
   directory and escaping symlinks are refused. The output remains unsigned.
   Commit `47a6d73` wires the guarded public D09 CLI route through the
   concrete launcher, admission, and proof checks. The route revalidates
   source/runtime bindings and launch proof before task release. Its committed
   tests cover component and simulated lower-helper paths; no positive public
   host launch, authentic signed host admission, or production qualification
   has occurred. Signing alone does not qualify a profile.
2. **Managed Linux service:** provision and validate the service account,
   systemd socket activation, source registration, signed profile, client/server
   handoff, and restart recovery. Source-capture code exists but the real signed
   profile path and a positive service launch remain unqualified.
3. **Containment:** qualify the rootless OCI/cgroup/seccomp/NNP and Landlock
   profile, including independent effect oracles. The reference VM currently
   reports Landlock ABI 4; the entrypoint requires ABI 8. Later HWE boots reached
   userspace but remain unqualified.
4. **Post-operation delta admission:** Store now records a separate decision on
   the verified candidate before promoting a generation. The policy requires
   canonical exact output paths, retains both candidate manifests, and binds
   the primary outcome to a protected admission journal. Recovery and bundle
   replay verify those bindings; UNKNOWN remains quarantined without replay.
   Operator-facing `--bash-output` and `--bash-no-outputs` flags carry scope
   through the authenticated service request to the authority and executor.
   See [the admission contract](delta-admission.md). Component checks do not
   qualify production containment or publication.
5. **Publication and provenance:** exercise sealed-source provenance, command
   settlement, live publication, and recovery with independent workspace,
   process, and network observers.
6. **Research protocol:** prepare and freeze the task corpus, oracle matrix,
   calibration pairs, and analysis plan before any registered experiment. A
   draft amendment proposes 51 recovery trials (17 scenarios × 3: eight
   lifecycle cases and nine incident cases). It is not approved, adopted,
   frozen, registered, or executed. E07 publication conformance remains
   separate. The draft corrects the cross-journal atomicity claim, specifies
   the store authorization binding, and marks lifecycle hooks as not
   implemented end to end.

## What has passed

The guarded launch checkpoint `47a6d73` passed Linux unit and race suites
with 925 tests each, seven skips, and clean vet; Windows passed 281 tests with
13 skips and clean vet. Its source bindings are in the
[guarded launch receipt](../../review-artifacts/tbound-resume-2026-10-13/guarded-d09-launch-receipt-47a6d73.json).

The reviewed recovery increment adds trusted journal observation hooks and
four native crash fixtures: durable intent, actual callback body entry, visible
effect inside the callback, and callback return before durable outcome.
Independent marker checks and fresh-process recovery verify no replay.
Corrected Linux unit and race suites each passed 935 tests with seven skips;
focused race checks passed 36 test events over three repetitions.
Windows passed 287 tests with 13 skips; vet was clean on both platforms.
These are component checks. The eight lifecycle scenarios remain unimplemented
end to end; none has been registered or executed as a headline trial.

The development VM reports Landlock ABI4; D09 requires exactly ABI8.
WSL component testing reports ABI1. A development capability probe reported
13 PASS, zero FAIL and two UNVERIFIED; cgroup.kill write and UID-map helper
behavior remain unverified. The reproducible guest recipe is prepared, but no
clean guest has been registered or qualified. These checks do not establish
a successful public production launch, provider exchange, containment, G1 or H1.

For implementation state, the historical denominator decision and current proposal, see
[`TASK_STATE.md`](../TASK_STATE.md). For historical source identities and
immutable evidence, use the [historical source map](evidence/historical-sources.json)
and its [guide](evidence/README.md).

The worker migration and historical recovery-denominator choice are recorded
in the [2026-10-12 CLI reproduction decision](decisions/2026-10-12-cli-reproduction.md).
The proposed recovery amendment is in
[`2026-10-13-recovery-boundary-amendment.md`](decisions/2026-10-13-recovery-boundary-amendment.md);
it supersedes the older recovery count only if adopted after review.
