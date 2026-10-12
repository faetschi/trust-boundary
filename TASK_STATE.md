# TBound task state

Concise summary of current tbound state. Make sure not to bloat this file or accumulate outdated information. Always update this file and remove old information so it stays focused and concise

Updated 2026-10-12. Current-state tracker only; keep short, remove stale items.

## Claim boundary (do not overstate)

- `G1=false`; containment `not-established`; Linux `serve --pi` is now a managed-service client. M1 source-capture code is present; production admission remains unverified pending host provisioning and validation of the real signed-profile path.
- Component/fixture/offline passes are NOT H1/G1, containment, provider, or thesis evidence.
- No stock CLI/SDK fallback, no invented profile. Pi keeps conversation/tool/UI behavior; TBound owns launch, broker/tool authority, lifecycle, durable generations, publication, evidence.
- D15: CLI-first claim-bearing path. Native TUI/companion UI are optional.
- D06: private-Git primitive is conformance-only; prototype waiver stays until trusted integration + independent proof.
- Publication is per-object durable reconciliation (no global atomicity, no blind replay).
- E07 conformance vs. secondary recovery study: user selected 48 recovery trials (16 scenarios × 3); the 8th crash point is E07 conformance, not part of that denominator. Needs dated protocol amendment before registration/execution.

## Status

- Reviewed and committed foundations: `8d2e478` (docs `640cf30`). Central review: component code accepted; integration NOT ready; thesis alignment open.
- Working branch: `feature/tbound-runtime-batch` (PR #7 is the persistence target).
- WIP checkpoint: `87e969a` (all prior unstaged state). Not pushed, not an acceptance commit. Do not reset/discard it.
- Freeze 2026-10-11 (user-authorized): commit `fdfbd38` preserves the 39-file runtime/service-handoff/sealed-source overlay. Pre-commit checks on Windows: `go build ./...` pass (explicit private `-o`), `GOOS=linux go build ./...` pass (earlier reported `internal/protocol` break no longer exists in the owner's current file), `gofmt -l` clean after formatting 5 overlay files, `go vet` shows only the 3 documented pre-existing `unsafe.Pointer` diagnostics in `broker/openrouter/credential_file_windows.go`. Full Windows `go test -count=1 ./...` passes including `internal/piruntime` — the five previously failing pipe-identity tests are fixed by the new `descriptor_identity_*` files. This is a preservation commit, not an acceptance commit; central review gate still applies.
- Not yet established: a provisioned positive governed launch; a real provider turn; a usable production `serve --pi` path.
- User authorizations (2026-10-11): freeze/commit granted; Hyper-V Ubuntu VM on this machine authorized for Linux experiments (must also work natively on Linux); real OpenRouter key at `F:\TBoundAssets\secrets` authorized with $1 spend budget; M1 (CLI thesis path) primary, M2 (product/packaging) parallel.
- Protocol reconciliation decision (user-delegated, adjustable later): recovery suite stays at 48 registered trials (16 scenarios × 3); the 8th enumerated crash point belongs to the E07 publication-conformance fault set, not the recovery denominator. Record as a dated protocol amendment before any registration/execution; never silently change counts.

## Open code gates (product)

1. Real Pi launch through one CLI-owned session: protected profile -> Pi -> Go broker -> durable executor -> `read->edit->bash->read`, with result and generation lineage.
2. Managed service handoff — characterization (2026-10-11, commit `fdfbd38`): protocol, socket activation, kernel-credential auth, journaling/idempotency, and both endpoints EXIST (`serve --pi` with `--task-id/--task-request-id/--task` is the client; `--service` is the systemd-activated server). Residual work, in dependency order: (a) service-mode source capture — code now qualifies the current managed-RW root under the recovered C lease, brackets baseline import with source revalidation, leaves `ExposureBinder` unset in service mode, and durably binds the qualification to g0/tree/manifest digests; focused empty-ledger, qualified-receipt, and pre/post-seed refusal tests were added. A post-seed mismatch emits a durable refusal record, retains g0 only in the private per-run Store for audit, and returns no generation to the caller (no worker/D09 exposure); cleanup/reconciliation of that retained Store is follow-up work. Linux `GOOS=linux GOARCH=amd64 go test -c` compiles both `./cmd/tbound` and `./internal/piruntime` test binaries, and Linux-target `go vet ./cmd/tbound ./internal/piruntime` passes; focused Linux race tests now pass on the existing private ext4 guest with `go test -race -mod=readonly -count=1 -run 'TestManagedSource|TestManagedLiveRootQualificationIssuesWorkspaceXattrAttestation|TestD09XattrEnumeration' ./cmd/tbound ./internal/piruntime`, covering empty-ledger recovery evidence, g0 receipt binding, pre/post-seed refusal, and xattr qualification. The first exact-source run exposed raw audit-hash formatting rejected by typed evidence validation and a test expecting an EvidenceBundle despite no baseline; both are fixed. The real signed-profile source path is also unverified. Xattr qualification inventories paths, metadata, and xattr names rather than file contents, so host provisioning must establish that the dedicated service UID has no other source-tree writers; the lease only serializes cooperating TBound sessions. (b) host provisioning tooling (units, service account, `tbound-pi-clients` group, `/run/tbound` tmpfs mount, config, measure/sign sequence) — no code authors any of it; (c) tests for the systemd listener, end-to-end client↔server, cross-restart journal replay, and the untested cmd/tbound unit validators (parallel duplicate of tested piruntime validators — drift hazard, e.g. duplicated config path constants); (d) client UX (request-ID generation, status/receipt command, error surfacing). Note: on Linux, production refusal is now "service unavailable", not the old unconditional design refusal.
3. Rootless UID/GID maps must be signed and proof-bound. Use distinct host service IDs; the map contract is `--uidmap CellUID:0:1` / `--gidmap CellGID:0:1` with no `--userns`.
4. Exact service state-write allowlist — largely pinned by the unit validator (`ReadWritePaths` == 8 registered paths, 7-entry StateDirectory); needs provisioning + tests.
5. D06 sealed source: export/consumer-view chain implemented through the cell entrypoint (`rebindConsumerFD` with same-inode + ro-mount checks); only ever exercised against fixtures — needs a real Podman/crun environment.
6. Publication: managed-root lease/epoch/permit machinery implemented; per-command D09 receipts minted, validated, and journal-bound, but NOT surfaced to the CLI (final receipt carries only counts/digest). Bash-lease terminal origin resolves via `ResolveTerminalLeaseOrigin`.
7. Cleanup: effective crun/seccomp binding; owned scope/cgroup cleanup; UNKNOWN keeps references and authority.
8. Xattr visibility needs independent operator qualification, not scanner metrics alone.
9. Packaging: canonical MIT text is included in release tarballs and generated npm root/platform packages, with metadata digest and offline layout assertions. On the existing Ubuntu VM, strict release layout passed 73/73, npm package checks 27/27, and synthetic DEB/RPM layout passed. Actual RPM, Homebrew, and container builds remain unverified.

## Environment limits

- WSL lacks Podman/crun/Cosign and a usable delegated cgroup. Direct cgroup launcher is not the D09 profile.
- Linux Node: `/home/jeli2k/node-v24.15.0-linux-x64/bin/node`. Go: `/home/jeli2k/go-sdk/go/bin`.
- Windows: baseline `60631d0` had 5 pipe-alias test failures and 3 `unsafe.Pointer` vet diagnostics. UPDATE 2026-10-11 on `fdfbd38`: the five pipe-identity tests pass (`descriptor_identity_*`); suite passes with only the 3 pre-existing vet diagnostics. UPDATE 2026-10-12: full `go test -count=1 ./...` passed on rerun; first run had a transient piruntime cleanup `TerminateProcess: Access is denied`, and the targeted piruntime rerun passed. Windows tests do not execute Linux-tagged M1 code.
- Linux verification (2026-10-12): a 233-file source/fixture snapshot matching the current Windows working tree was SHA-256 checked in the existing VM. Go 1.27.1 full `go test -mod=readonly -count=1 ./...`, full `go test -race -mod=readonly -count=1 ./...`, and `go vet -mod=readonly ./...` passed with umask 077 and a private 0700 ext4 TMPDIR. A private-output build and `serve --pi --native-fixture` also passed (non-claim-bearing). These are component and fixture checks; native bare-metal setup, provisioned managed-service admission, real provider exchange, D09 containment, and G1 remain unverified.
- Hyper-V VM `TBound-Ubuntu-2404` (authorized for experiments 2026-10-11): on 2026-10-12, `CodexSandboxUsers` established strict SSH as `tboundadmin` to `192.168.251.23/20` after operator console login; the ED25519 host fingerprint matched `infra/hyperv/INSTALLATION-RECORD.md`. DHCP address is dynamic. The Hyper-V ACL still denies `Get-VM` to this execution context and the host alias does not resolve. Help-sheet IP `172.19.193.190` was unreachable. Root is LVM (`ubuntu-vg/ubuntu-lv`); checkpoints and a two-AVHDX chain exist. Resume with elevated `Get-VM` / `Start-VM` / `vmconnect.exe`; shut down gracefully with guest `sudo poweroff`.
- Fresh guest probe (`infra/guest/probe-capabilities.sh`, 2026-10-12): **PASS=13 / FAIL=0 / UNVERIFIED=2**, exit 2 for the two UNVERIFIED rows. SSH reported `192.168.251.23/20`, kernel `6.8.0-146-generic`, and Landlock ABI 4. Installed HWE 7.0.0-38 reached userspace on repeated boots for minutes; inspected journal tails showed no panic/oops, but reboot cause remains unknown and HWE is unqualified. G1 remains false; the probe is not containment or G1 evidence. PASS: cgroup v2 domain, `cgroup.kill` interface (write behavior remains untested), pidfd, seccomp, subuid/subgid >=65536, user namespace creation, rootless Podman/crun 1.14.1, Node 24.21.0, Go 1.27.1. A separate direct check previously reported Podman 4.9.3; the probe's Podman-version observation was `unknown` on this run. `kernel.apparmor_restrict_unprivileged_userns=0` applied; this system-wide change needs an explicit security warning. Cosign v3.1.3 is at `~/.local/bin/cosign` (may not be on non-login SSH PATH). An offline recovery edit changed `/boot/grub/grub.cfg` kernel references back to 6.8; backup is `/boot/grub/grub.cfg.tbound-bootfix-bak`; this is not a clean G1 image. No more offline disk edits; inspect via console and restore/qualify boot selection before any G1 freeze.
- Researcher setup UX WIP: `infra/repro/{bootstrap-linux.sh,bootstrap-hyperv-windows.ps1,probe-host.ps1,README.md}` and `docs/reproducible-setup.md` are uncommitted, syntax/fixture checked but not end-to-end qualified on a clean native host or new Hyper-V VM. Root `README.md` now documents start/resume and setup options plus actual VM/probe status. Review before acceptance.

## Commands

```sh
cd supervisor
go build -o /tmp/tbound ./cmd/tbound
/tmp/tbound serve --pi --native-fixture   # exit 0, claim_bearing=false (fixture, no Pi)
/tmp/tbound serve --pi                     # managed-service client; currently unavailable without registration
```

Opt-in Go<->Node<->Pi dev test (needs `TBOUND_GOVERNED_PI_NODE`, `TBOUND_GOVERNED_PI_NODE_MODULES`, `TBOUND_GOVERNED_PI_TEST_ROOT` = private 0700 ext4):
`go test -mod=readonly -run TestDevelopmentPiSDKWorkerInheritedFDProcessRoundTrip -v ./internal/piruntime`

Companion (read-only): `cd supervisor && go run ./cmd/tbound-governance --addr 127.0.0.1:8790`

Build outputs must use explicit private `-o` paths. Never overwrite `supervisor/tbound` (tracked ELF) or `supervisor/tbound.exe` (untracked, SHA-256 `2a49661c91aeae671b1d2c3d1b463fedb00091955618f2fa8077796dfe221089`).

## Rules

- Central review gate before any acceptance or commit: freeze all combined changes, run a central read-only review, and get separate verdicts for code safety, integration readiness, and thesis alignment. Re-verify after fixes. Individual passes do not count.
- Verification evidence needs: full Linux race suite (private ext4, 0700 TMPDIR), vet/gofmt, Windows build, npm check if adapter touched, and unchanged-source readback. Report unrun checks.
- Commit only explicit paths after acceptance, with tested-byte comparison. Coordinator alone commits. No blanket staging.
- Preserve all unrelated dirty work (user docs, Hyper-V infra, `docs/frontend-observability-plan.md`, `docs/dev-launch.md`). Never adopt wholesale.
- Never access `vm_start.txt` or inspect/log/copy key material. No provider credentials in fixtures or ambient env.
- No reset/discard, new worktree, dependency/lock change, or broad deletion without explicit coordination.
- No VM, kernel, package, systemd, account, mount, network, provider, trust, or signing operations without explicit operator authorization. Provisioned launch is a separate gate.
- Keep ownership disjoint. Do not poll or duplicate dispatch to running workers.
- Stop and report when the high-level product goal (real Pi + four governed tools + observed cleanup + validated publication + usable install) is reached or blocked.

## Next step

1. Review/freeze researcher setup UX; validate scripts on a disposable clean host before claiming reproducibility.
2. Keep this reference VM on reachable kernel 6.8 for development; inspect HWE boot behavior from console/logs before any further disk edits. Landlock ABI 8 remains a G1 blocker.
3. Resume M1 code gates: qualify the signed managed-source registration/calibration and provisioned service path on Linux; then run the authorized real-provider E05 within the $1 cap, a positive D09 session, and independent effect oracles. Central review before acceptance; passing component tests and capability probes are not G1/H1 evidence.

## Source references

- `../MSE_MA_Thesis/agentic-harness/AH-technical/10-decision-register.md` (decisions)
- `../MSE_MA_Thesis/agentic-harness/AH-technical/11-evidence-contract.md` (evidence)
- `../MSE_MA_Thesis/agentic-harness/AH-technical/02-1-recovery-and-continuation.md` (recovery)
- `../MSE_MA_Thesis/agentic-harness/AH-technical/02-process-model.md`
- `../MSE_MA_Thesis/agentic-harness/AH-technical/00-roadmap.md`
- `docs/central-review-corrections-2026-10-07.md`, `docs/production-candidate-verification-2026-10-10.md`
