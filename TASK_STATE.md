# TBound task state

Updated 2026-10-11. Current-state tracker only; keep short, remove stale items.

## Claim boundary (do not overstate)

- `G1=false`; containment `not-established`; `serve --pi` (production) refuses by design.
- Component/fixture/offline passes are NOT H1/G1, containment, provider, or thesis evidence.
- No stock CLI/SDK fallback, no invented profile. Pi keeps conversation/tool/UI behavior; TBound owns launch, broker/tool authority, lifecycle, durable generations, publication, evidence.
- D15: CLI-first claim-bearing path. Native TUI/companion UI are optional.
- D06: private-Git primitive is conformance-only; prototype waiver stays until trusted integration + independent proof.
- Publication is per-object durable reconciliation (no global atomicity, no blind replay).
- E07 conformance vs. secondary recovery study: 7 vs 8 crash points, 48 vs 51 trials unresolved. Needs dated reconciliation before registration/execution.

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
2. Managed service handoff — characterization (2026-10-11, commit `fdfbd38`): protocol, socket activation, kernel-credential auth, journaling/idempotency, and both endpoints EXIST (`serve --pi` with `--task-id/--task-request-id/--task` is the client; `--service` is the systemd-activated server). Residual work, in dependency order: (a) service-mode source capture — binder hard-refuses at `production_binder_linux.go:295-301` (`ErrManagedSourceCaptureUnavailable`); the managed-RW source/xattr lease API is the single functional blocker; (b) host provisioning tooling (units, service account, `tbound-pi-clients` group, `/run/tbound` tmpfs mount, config, measure/sign sequence) — no code authors any of it; (c) tests for the systemd listener, end-to-end client↔server, cross-restart journal replay, and the untested cmd/tbound unit validators (parallel duplicate of tested piruntime validators — drift hazard, e.g. duplicated config path constants); (d) client UX (request-ID generation, status/receipt command, error surfacing). Note: on Linux, production refusal is now "service unavailable", not the old unconditional design refusal; docs/governed-session-composition.md is stale on this.
3. Rootless UID/GID maps must be signed and proof-bound. Use distinct host service IDs; the map contract is `--uidmap CellUID:0:1` / `--gidmap CellGID:0:1` with no `--userns`.
4. Exact service state-write allowlist — largely pinned by the unit validator (`ReadWritePaths` == 8 registered paths, 7-entry StateDirectory); needs provisioning + tests.
5. D06 sealed source: export/consumer-view chain implemented through the cell entrypoint (`rebindConsumerFD` with same-inode + ro-mount checks); only ever exercised against fixtures — needs a real Podman/crun environment.
6. Publication: managed-root lease/epoch/permit machinery implemented; per-command D09 receipts minted, validated, and journal-bound, but NOT surfaced to the CLI (final receipt carries only counts/digest). Bash-lease terminal origin resolves via `ResolveTerminalLeaseOrigin`.
7. Cleanup: effective crun/seccomp binding; owned scope/cgroup cleanup; UNKNOWN keeps references and authority.
8. Xattr visibility needs independent operator qualification, not scanner metrics alone.
9. Packaging: strict Linux layout and npm checks pass on the frozen source. RPM, Homebrew, and container builds are not verified. Canonical license text is a public-release blocker.

## Environment limits

- WSL lacks Podman/crun/Cosign and a usable delegated cgroup. Direct cgroup launcher is not the D09 profile.
- Linux Node: `/home/jeli2k/node-v24.15.0-linux-x64/bin/node`. Go: `/home/jeli2k/go-sdk/go/bin`.
- Windows: full vet/tests fail on baseline `60631d0` (5 pipe-alias tests, 3 unsafe.Pointer vet diagnostics). Do not report Windows as green. UPDATE 2026-10-11 on `fdfbd38`: the five pipe-identity tests now pass (`descriptor_identity_*`); Windows suite fully green; only the 3 pre-existing `unsafe.Pointer` vet diagnostics remain.
- Hyper-V VM `TBound-Ubuntu-2404` (authorized for experiments 2026-10-11): running, fresh boot, guest IP `172.30.32.185` on Default Switch (DHCP works after reboot; Hyper-V reports no guest IP — no KVP daemon). SSH BLOCKED: no local credential reaches the guest; old operator-key infrastructure (`C:\ProgramData\TBoundHostAutomation`) does not exist on this machine. Pending: user adds the host `id_ed25519` public key to `tboundadmin` via VM console; then run `infra/guest/probe-capabilities.sh`-equivalent checks (Podman/crun/Cosign/cgroup v2 delegation/Landlock ABI).
- VM checkpoints present: `ubuntu-24.04.5-golden-baseline`, `tbound-demo-checkpoint` (restore capability untested).

## Commands

```sh
cd supervisor
/home/jeli2k/go-sdk/go/bin/go build -o /tmp/tbound ./cmd/tbound
/tmp/tbound serve --pi --native-fixture   # exit 0, claim_bearing=false (fixture, no Pi)
/tmp/tbound serve --pi                     # exit 2 (expected refusal)
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

Finish open code gates 2-8 in disjoint ownership. Then freeze, verify, and send to central review. Do not claim readiness until the review passes.

## Source references

- `../MSE_MA_Thesis/agentic-harness/AH-technical/10-decision-register.md` (decisions)
- `../MSE_MA_Thesis/agentic-harness/AH-technical/11-evidence-contract.md` (evidence)
- `../MSE_MA_Thesis/agentic-harness/AH-technical/02-1-recovery-and-continuation.md` (recovery)
- `../MSE_MA_Thesis/agentic-harness/AH-technical/02-process-model.md`
- `../MSE_MA_Thesis/agentic-harness/AH-technical/00-roadmap.md`
- `docs/central-review-corrections-2026-10-07.md`, `docs/production-candidate-verification-2026-10-10.md`
