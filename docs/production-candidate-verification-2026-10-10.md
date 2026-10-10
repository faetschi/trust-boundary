# Governed Pi product candidate: verification record (2026-10-10)

**Status: locally tested snapshot; central review holds product acceptance.**
This is not a provisioned D09 launch, publication receipt, G1 qualification, or
thesis security result. No VM provisioning or provider exchange was performed.

The independent Luna-max review found the architecture and implemented admission
boundaries aligned, but did not accept a user-usable production path. The reviewed
snapshot lacks a supported CLI task handoff into the registered managed service
and does not pin/capture the effective rootless UID/GID mappings. Additional
service state-write grants need a precise allowlist. Xattr completeness remains
an independently qualified operator/TCB premise, not a result of the identity
measurement command alone. Owners are addressing these code gates; any changed
source needs a new freeze, combined verification and review. The results below
belong only to `aehHOH`, not the subsequent moving candidate.

## Candidate identity

- Base commit: `60631d039e11d5d52a4e939a53b1168391beb0d6`.
- Candidate: the explicit runtime, CLI, session-launch, publication, profile,
  entrypoint, packaging, workflow and supporting documentation overlays on that
  commit. Owners held edits during verification.
- Private frozen snapshot: `tbound-combined-production.aehHOH`.
- SHA-256 of the owned-input manifest:
  `2612ded6fdcf141e705216a97b84ecae16ebcbc15dbacb9025b3f93930809653`.
- SHA-256 of the full snapshot source manifest:
  `e8e2dfa8b65d940518c289bce4150182e7f896d055cd5088d7e6000abc1cef04`.
- Input hashes matched before and after copying; final readback matched the
  frozen source and original owned repository inputs.

The snapshot excludes unrelated dirty work. In particular, the user-owned dirty
`docs/dev-launch.md` was not overlaid; its committed baseline was used. User
infrastructure, root README, adapter closure report and existing build artifacts
were not adopted into the implementation candidate.

## Independent local results

| Check | Result | Scope |
|---|---|---|
| Linux `gofmt -l` and `go vet ./...` | Pass | Frozen supervisor source |
| Linux `go test -race -count=1 -timeout=240s ./...` | Pass | Full supervisor suite |
| D09-tagged vet and tests | Pass with opt-in unset | Does not exercise a provisioned host |
| Private CLI, profile and entrypoint builds | Pass | Linux/amd64; entrypoint also built with CGO disabled and trimpath |
| `serve --pi` and missing-profile launch | Refuse, exit 2; no success receipt | Missing production prerequisites |
| Real pinned-Pi offline development launch | Pass | Four tool results, three generations; explicitly non-claim-bearing, no provider exchange |
| Explicit D09 opt-in with missing template | Fails as expected | Required prerequisites are not silently skipped |
| Strict release-layout verification | 66 passed, 0 failed, 0 skipped | Actual Linux/amd64, Windows/amd64 and Darwin/amd64+arm64 archives; manifests, metadata and DEB contents |
| npm package verification | 21 passed, 0 failed | Local package assembly, including eight actual wrapper-dispatch checks |
| Package-manager layout verification | Pass | Explicitly synthetic layout/validation fixture, not a product runtime |
| Windows build | Pass | Private clone of the same frozen source |
| Windows full tests and vet | Fail, reproduced on base commit | See below; do not report these as passing |

The selected entrypoint uses Landlock ABI 8 TSYNC. Its real enforcement-positive
test cannot qualify this WSL kernel, which reports ABI 1. Unsupported-kernel skips
are not enforcement evidence.

### Failed attempts retained

- Snapshot `KOjGld` aborted because an owned test changed during copying. It was
  not used as test or review evidence.
- The first packaging run on `aehHOH` used the coordinator's test version
  `0.0.0-verify`. RPM correctly rejected its hyphen: 65 checks passed and one
  failed. The failed log was retained. Packaging was rerun with valid version
  `0.0.0`, against unchanged source, producing the passing strict result above.
- Windows full vet reports three `unsafe.Pointer` diagnostics in
  `internal/broker/openrouter/credential_file_windows.go`. Five existing
  `internal/piruntime` binding tests fail on pipe object-identity comparison.
  An independent private archive of the exact base commit reproduced all eight
  diagnostics/failures. They are pre-existing, not a green suite or a candidate
  regression. Correct platform identity and vet handling remain follow-up work.

## Unverified deployment and publication gates

- No registered-host positive D09 Pi/Bash execution or live publication was run.
- The required protected profile, template and managed-root registration are
  absent on the development host. Actual Podman/crun/Cosign, signed image,
  cgroup delegation and registered consumer views must be qualified separately.
- The host-held FD3 Node executable and runtime bundle require the protected,
  immutable installation described in the setup plan; image Node alone does not
  satisfy the host executable contract.
- Managed-root registration authenticates and revalidates policy. It does not
  itself grant quiescence: the trusted runner must hold its writer lease and
  obtain the epoch-bound drained publication permit.
- Internal TCB proofs and receipts are not independent workspace, network or
  process-effect observers. No zero-forbidden-effect claim follows from this run.
- RPM/Homebrew/container product builds were not independently qualified.
- Public release/npm publication remains unperformed. The missing canonical
  license text must be resolved before public release; unsigned distribution
  entrypoints do not establish governed trust.

Only a separately authorized, provisioned-host test can establish the genuine
governed positive path. Code persistence must retain these limits, not describe
local tests or safe refusal as production qualification.
