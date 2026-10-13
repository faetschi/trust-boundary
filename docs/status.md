# Current status

Updated 2026-10-12. This page records the current implementation and blockers;
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
   Production is unqualified, and `LaunchD09PiCLIWorker` still refuses launch.
   Signing a profile alone will not enable it. Pi 0.87.1 was verified on this VM
   only.
2. **Managed Linux service:** provision and validate the service account,
   systemd socket activation, source registration, signed profile, client/server
   handoff, and restart recovery. Source-capture code exists but the real signed
   profile path and a positive service launch remain unqualified.
3. **Containment:** qualify the rootless OCI/cgroup/seccomp/NNP and Landlock
   profile, including independent effect oracles. The reference VM currently
   reports Landlock ABI 4; the entrypoint requires ABI 8. Later HWE boots reached
   userspace but remain unqualified.
4. **Publication and provenance:** exercise sealed-source provenance, command
   settlement, live publication, and recovery with independent workspace,
   process, and network observers.
5. **Research protocol:** prepare and freeze the task corpus, oracle matrix,
   calibration pairs, and analysis plan before any registered experiment. The
   selected recovery denominator is 48 trials (16 scenarios × 3). The proposed
   classification of the eighth crash point as separate E07 conformance must be
   reconciled in a dated protocol amendment before registration.

## What has passed

The recorded final Linux component checks on the existing reference VM passed
before the review follow-up described above: source hash readback matched
306/306 files; Go unit and race runs each reported 781
passed, 0 failed, and 7 skipped; go vet and gofmt were clean; npm ci, the
required actual-CLI check, the native fixture, and the actual Go CLI offline
fixtures passed. The full Windows supervisor Go test suite also passed. The native
fixture records four proposals/results, five scripted provider turns, and
generations g0→g1→g2, with no real provider exchange. These component and offline checks predate the review follow-up and do not
establish H1, G1, production launch, provider behavior, or containment. The
Nine focused Python staging regressions passed against an isolated copy under
local WSL. Linux-only D09 and launcher Go tests and the full Linux suite have not
been run against this follow-up, so no fresh full-validation claim is made.
Saved logs show publication-recovery tests passing in one run; another run
stopped at the audit-journal guard because a temp ancestor was mode 0777, before
recovery logic ran. This indicates a test-temp setup failure, not a
demonstrated publication defect; the logs do not establish a concurrent-state
collision. Rerun Linux checks under a private 0700 TMPDIR beneath HOME and
umask 077.
A fresh capability probe on the VM reported 13 PASS, 0 FAIL, and 2 UNVERIFIED;
its native SSH exit was 2 for the unverified cgroup.kill write and uidmap-helper
behavior. Landlock ABI 4 is a separate unmet requirement for the ABI 8 profile.
These checks are development evidence and do not close any gate above.
For detailed implementation state and the agreed recovery denominator, see
[`TASK_STATE.md`](../TASK_STATE.md). For historical source identities and
immutable evidence, use the [historical source map](evidence/historical-sources.json)
and its [guide](evidence/README.md).

The worker migration and recovery-denominator choice are recorded in the
[2026-10-12 CLI reproduction decision](decisions/2026-10-12-cli-reproduction.md).
