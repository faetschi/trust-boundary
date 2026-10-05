# TBound handoff state

Last updated: 2026-10-05 (Europe/Vienna) — resumed session; rootless Podman runner committed (`c0a0522`) and independently re-verified on the guest

## Progress — session 2026-10-05

- Resumed the interrupted Podman runner subagent and completed it. Committed `c0a0522`
  (`supervisor/internal/podman/`: a non-claim-bearing rootless Podman/crun `sessionrepo.CommandRunner`).
  Independently verified by the orchestrator: WSL `go test -race -count=1 ./...` all packages green
  (podman skips cleanly without podman), Windows `go build ./...` green, `gofmt -l supervisor` empty,
  `git diff --check` clean, no module/lock change, and a **guest conformance re-run** on kernel 6.8
  with the exact committed artifact (all three file SHA-256s match the tested hashes): `TestProbe`,
  `TestConformance`, and `TestStoreRunBashIntegration` all PASS. Probe measured podman 4.9.3, rootless,
  crun 1.14.1, cgroup v2, userns usable, image `docker.io/library/alpine@sha256:294b68…77e6` (digest
  pinned); containment recorded `not-established`.
- Guest DHCP moved `172.24.195.3` → `172.25.31.157` (VM Running on `Default Switch`), rediscovered from
  MAC `00-15-5D-0C-40-00`.
- Committed `ebf7e53`: E05 two-generation `read → edit → bash → read` workflow over
  ipc → broker correlation → gate → durable executor, plus the durable (non-mutating) `read` path
  (bounded strict-JSON summary, fail-closed path/argument/bound checks, no generation advance), with
  ambient-bypass denial and cancellation tests. Independently verified (WSL `-race`, Windows
  build/test, gofmt/diff, no module change).
- Committed `42c82d7`: `Store.Delete` (same durable intent/transition/ledger/recovery semantics as
  Write/Edit) and the E04 create-delete-revert / modify-revert observation fixtures (both intervening
  transitions retained even when the final tree matches), plus the E06 denied-read matrix for the
  sessionrepo read path and the sandbox cell boundary. Independently verified (WSL `-race`, Windows
  build/test, gofmt/diff, no module change). Delete is a store capability only, not wired into the
  registered four-tool surface.
- Committed `2b13e7d`: Podman hardening — manifest-frozen digest pin
  (`sha256:294b68…77e6`), `--pull=never`, exact `RepoDigests`-only matching, and a real settlement
  observer (`--cidfile` FIFO that survives `--rm`, then `podman container exists`, a `/proc`
  cmdline/environ scan, a bounded `/sys/fs/cgroup` scan, and mountinfo) that fails closed unless all
  signals agree; containment stays `not-established`. Independently re-verified on the guest with the
  exact artifact (Probe digest-pinned; unpinned pin rejected fail-closed; conformance and RunBash
  integration PASS).
- Committed `de936bf`: real kernel durability-fault tests for the audit journal (ENOSPC via
  `/dev/full`, `EINVAL` fsync via a FIFO, `EFBIG` via `RLIMIT_FSIZE` on a real journal) proving
  intent-before-effect and outcome-before-release fail closed, with poisoning and unresolved-intent
  quarantine reproduced on reopen. Independently verified (WSL `-race`, Windows, gofmt/diff, no module
  change).
- Committed `abfe515`: D1 self-contained evidence bundle + pure `Reconstruct` replay (exit #5), with
  reconstruct/quarantine and eight tamper cases; independently verified (WSL `-race`, Windows,
  gofmt/diff, no module change).
- Committed `ce3ec63`: D2 sealed-generation reachability + read-only proof (exit #2) — live-source
  independence, no Git/mount reachability, O_PATH-only mount source, and sealed-tree plus
  durable-manifest tamper fail closed. Independently verified (WSL `-race`, Windows, gofmt/diff, no
  module change).
- **Open decisions (blocking full G1):** (a) spec §6/D06 call for a new private Git repository; the
  prototype has none — record a waiver or implement. Items (b) and (c) are now **resolved**:
  (b) genuine provider exchange achieved (see the `cmd/tbound-provider` bullet above);
  (c) `\TBoundVmCheckpoint\{List,Create,Restore,Delete}` installed (one-time UAC approved) and a
  checkpoint create → restore was demonstrated (see below).
- Checkpoint tasks **installed** (2026-10-05): `C:\Users\Admin\AppData\Local\Temp\opencode\elev-vmcheckpoint-setup-wrapper.ps1`
  installed `\TBoundVmCheckpoint\{List,Create,Restore,Delete}` for the pinned VM and a pinned
  `tbound-demo-checkpoint` (S4U, RunLevel Highest, `FABIAN\Admin` run/query only), modeled on the
  reviewed VmOps installer.
- Committed `4c5d218`: candidate G1 experiment manifest (`docs/experiment-manifest.md`) recording the
  verified environment/toolchain/isolation facts, the frozen image pin, Pi closure, the four-false
  claim status, the "must freeze before G1" gap list, and a docs-reconciliation note.
- Committed `b63f201`: **P1 (generation-exposure read-only view)** — `exposure_linux.go`
  (`Expose`/`Refresh`/`Observed`/`ReadOnlyMountSource`/`Close` with an injected `Binder` and
  `ViewRecreated`/`ViewIndirection` modes), `exposure_linux_test.go` (E01-style recreated + indirection,
  the stale-view negative, exposure-directory recovery, and a real mount-backed read-only `EROFS`
  refresh), plus the private `exposed/` directory wiring in `sessionrepo_linux.go`/`recovery_linux.go`.
  Independently verified: WSL full `-race` green (mount-backed test PASS), Windows build/tests green,
  gofmt/diff/module clean, and the guest `sessionrepo` package green (the mount-backed test skips
  explicitly under the guest's AppArmor userns restriction, as designed). No containment/Pi/G1 claim;
  live publication/apply is out of scope.
- Committed `2a39170`: real OpenRouter transport + secure credential loading + `cmd/tbound-provider`
  one-shot exchange (request clone, single `Bearer` header, redirects forbidden, TLS ≥1.2, cross-read
  key redaction; the key is never logged/serialized/recorded and is passed via closure; model + key via
  env or a 0600 owner-only file). Independently verified (WSL `-race`, Windows build/tests, gofmt/diff,
  no module change); the real-network exchange test skips until credentials are supplied.
- **Genuine provider exchange achieved (2026-10-05):** `cmd/tbound-provider` performed a real
  OpenRouter exchange — HTTP 200, response id `gen-1791225506-zYER5MjxF0PnnrPQhzL6`, model
  `nvidia/nemotron-3.5-lightning:free`, `captured_tool_calls=1`, ordered four-tool manifest respected,
  21,683 bounded response bytes; containment `not-established`, `g1=false`. The API key was supplied via
  `F:\TBoundAssets\secrets\openrouter.key` (ownership/DACL tightened to owner-only; never
  printed/logged/committed). Forcing the exchange surfaced three real provider-compatibility fixes
  (reasoning / reasoning_details / service_tier metadata, and a trailing usage trailer after
  `finish_reason`), committed in `602f111`. This is a one-shot **transport + trusted-capture** proof:
  the adapter→Pi session wiring is still not live, and the free endpoint logs sessions (only a synthetic
  prompt was sent).
- **Hyper-V checkpoint/restore demonstrated (2026-10-05):** installed
  `\TBoundVmCheckpoint\{List,Create,Restore,Delete}` for the pinned VM + a pinned
  `tbound-demo-checkpoint` (S4U, RunLevel Highest, `FABIAN\Admin` run/query only). A running
  production checkpoint fails (the guest lacks VSS/`hv_vss_daemon`), so `Create` is done while the VM
  is Off; then `\TBoundVmOps\Stop` → `Restore` → `Start` returned the guest to the checkpoint state:
  `Running` on `Default Switch`, new DHCP `172.25.23.3`, pinned-key SSH OK (`tboundadmin`, kernel
  `6.8.0-146-generic`, fresh boot). This satisfies the todo's "create a clean snapshot and demonstrate
  restore" item.
- Remaining implementable items: publication path / frozen profile, the controller
  `ReviewedSourceCommit` + guest offline re-verification, and the webserver observability frontend
  below.

### Five-slice completion plan (2026-10-05)

Executing the remaining items as separate, committed, independently verified slices. Evidence standard
for each: WSL2 `go test -race -count=1 ./...` with a private mode-0700 TMPDIR, Windows `go build ./...`
plus `go test ./internal/... ./e2e`, adapter `npm run check` when the adapter is touched,
`gofmt -l supervisor` empty, `git diff --check` clean, no `go.mod`/`go.sum` change, and a pinned-key-SSH
guest re-run where guest-relevant. Containment stays `not-established`; spec §6/D06 private-Git-repo is
recorded as a documented waiver (not implemented); the CLI stays the claim-bearing frontend and the web
client untrusted/read-only.

1. Real provider capture → ipc → broker correlation → gate → durable effect (in progress).
2. Publication / live-apply: repository lock, single-use commit token, per-object apply, recovery
   record, disposable restore.
3. Extend `cmd/tbound-web` + `internal/webview` into a live read-only test-run dashboard per
   `docs/frontend-observability-plan.md` (P0–P3 first).
4. Update the reviewed verifier controller's `ReviewedSourceCommit` and re-run the guest offline
   verifier against a frozen committed snapshot (may need the one-time reviewed-helper install).
5. Claim-bearing containment hardening toward G1 (signed in-image entrypoint, offline cosign, delegated
   non-threaded cgroup-v2 with `cgroup.kill`/pidfd, real settlement observer) — expected to be blocked
   on tooling/elevation and reported as such.

## Resume point — next

HEAD = `c0a0522`. Working tree holds only the pre-existing unrelated `docs/` + `infra/hyperv/`
modifications and `vm_start.txt` (untouched).

Next steps:
1. Claim-bearing hardening of the Podman runner: frozen signed in-image entrypoint, manifest-frozen
   image digest pin, offline cosign, and a real settlement observer (`cgroup.kill`/`populated=0`,
   pidfd, mount-namespace inspection); only then may containment move beyond `not-established`.
2. Publication path, E04/E06 fixtures, frozen profile, disposable restore.
3. Update the controller `ReviewedSourceCommit`; re-run the guest offline verifier against a committed
   snapshot.
4. VM/SSH autonomy verified: `\TBoundVmOps\{Status,Start,Stop,Connect,Disconnect}` run unelevated, a
   clean Stop→Start cycle works, and the guest IP is rediscovered from MAC `00-15-5D-0C-40-00` via
   `Get-NetNeighbor` with pinned-key SSH.

### Planned: webserver observability frontend (future todo)

User request (2026-10-05): a visual "frontend" to watch sessions/tests in real time, ideally a
**webserver** rather than a TUI. The thesis already scopes this:

- `tbound-thesis.md:113,346,463,963`: the required operator frontend is the **CLI-first,
  client-independent supervisor API**; a TUI or web client is an **optional untrusted presentation
  client** that must use the same supervisor API and does not change H1.
- `AH-technical/06-1-language-decision.md:11,16`: a future TUI/browser client is deferred; browser
  access may use **WebSockets/SSE** and needs separate authentication and session-management
  evaluation.
- `AH-technical/00-roadmap.md:77`, `01-decisions-glossary.md:37`, `todo-implementation-tbound.md:60`:
  a polished TUI/browser UI is a **deferred non-goal** for the MVP; `visualizations-plan.md:417,543`
  keeps presentation dashboards/live charts out of the claim path.

First slice committed (`93fb8bf`): `cmd/tbound-web` + `internal/webview` — a loopback-only
(`127.0.0.1:8787`; non-loopback `--addr` rejected, no remote auth), read-only Go server that tails the
audit-journal JSONL and supervisor-transcript NDJSON (opened read-only, never written) and serves an
embedded live page + SSE `/events` + `records` (bounded buffer; malformed/oversized lines surfaced
honestly; truncation/rotation handled). Verified (WSL `-race`, Windows build/tests, gofmt/diff, no
module change). Remaining for later: wire it to a live supervisor session/event source and enrich the
views (preflight, session lifecycle, generation transitions, exposure/refresh). Keep it an untrusted
client with **no** policy/workspace/audit authority, loopback by default, separate auth before any
non-loopback exposure, and never in the claim path. The CLI remains the claim-bearing entry point.

## Continuation plan — 2026-10-04 (this session)

Objective: continue from the verified state (`ca9f227`); fix remaining open issues; advance the
durable end-to-end path; and keep VM lifecycle + SSH autonomous (no manual VMConnect).

### Autonomy focus
- Confirm the pre-installed fixed tasks in `\TBoundVmOps` (`Status`, `Start`, `Stop`, `Connect`,
  `Disconnect`) still run unelevated as `FABIAN\Admin` via `schtasks /run`; that a clean guest
  shutdown reaches Hyper-V `Off` without force-off; and that after `Start` the guest DHCP address is
  rediscovered from the pinned MAC `00-15-5D-0C-40-00` with pinned-key SSH succeeding. If any step
  needs a one-time elevation, stop and ask for that single approval only.

### Autonomy verification (2026-10-04)

- `\TBoundVmOps\{Status,Start,Stop,Connect,Disconnect}` are all present and `Ready` and were invoked
  unelevated via `schtasks /run`. Full cycle re-proven this session: `Status` Running → `Stop`
  reached Hyper-V `Off` (graceful, no force-off) → `Start` returned `Running` on `Default Switch`.
  Guest DHCP moved `172.24.196.229` → `172.24.195.3`, rediscovered from pinned MAC
  `00-15-5D-0C-40-00` via `Get-NetNeighbor`, and pinned-key SSH returned exit 0 (`tboundadmin`,
  `tboundubuntu2404`; toolchains `go1.27.1`, `node-v24.21.0-linux-x64` under `/opt/tbound`). VM
  start/stop/connect/disconnect and SSH require no manual VMConnect and no new elevation.

### Progress — durable decision/effect wiring (2026-10-04)

- Committed `08af31b`: `Supervisor` gains a nil-safe, fail-closed `DecisionRecorder` hook invoked
  after the gate decision and before the executor, recording every ALLOW and DENY as a benign
  `gate_decision` audit event (`cmd/tbound/decisions_test.go`, `durable_decisions_linux.go`). A Linux
  `DurableExecutor` (`durable_executor_linux.go`) maps a trusted ALLOW `write`/`edit` proposal onto
  one `sessionrepo` mutation on the current tip and releases a strict-JSON settlement summary only
  after the effect is durable. The gate's versioned `tbound-policy/v1:sha256:…` digest is never
  equated with the sessionrepo bare `sha256:…` commitments; operation/decision IDs are derived
  deterministically from the trusted response issuer/opaque + sequence + tool-call ID.
  `durable_integration_linux_test.go` proves ordering (gate_decision → intent → effect → outcome →
  result), exactly one durable write effect, `store.Verify`/`Evidence` consistency, and close+reopen
  recovery; `TestDurableDenyDecisionIsRecorded` proves deny is recorded pre-effect and never
  executes; `TestServeRecordsDecisionFailClosed` proves the hook withholds on failure on every
  platform. Independently verified: Linux `-race` all 12 packages green, Windows build/tests green,
  `gofmt`/`git diff --check` clean, no module change.
- Found a real pre-existing bug during verification: `sessionrepo.Store.Write` passed its `0o644`
  default as `executableBits`, so a **new** file was created mode `0755` instead of `0644` (`Edit` was
  unaffected because `readRegularAt` returns `Mode & 0o111`). Fixed and committed (`26b71dd`):
  new paths default to `0644`, an existing file's executable bit is preserved, and
  `TestSessionRepositoryWriteCanonicalModeE2E` proves new-file `0644`, overwrite non-executable
  `0644`, and overwrite executable `0755` across a verified `g0→g3` chain. Independently verified
  (Linux `-race` all packages, Windows build, gofmt/diff clean).
- **Contained executor (Option-A) committed** (`b609bad`, runner `wsl-dev-sandbox-non-claim-bearing`):
  `internal/sandbox` composes user/mount/PID/IPC/UTS/network namespaces + `no_new_privs` + a Landlock
  ABI-1 ruleset + a pinned default-deny seccomp-bpf allowlist + cgroup v2 memory/pids/cpu when
  delegated + loopback-only networking + read-only runtime binds with a writable cell root, reaps via
  pidfd/`wait4`, and tears down with `cgroup.kill` or process-group SIGKILL confirming in-namespace
  scope emptiness. `internal/executor` adapts it to `sessionrepo.CommandRunner`. A fail-closed
  `Probe()` records effective mechanisms. Independently verified on WSL2 5.15.153 (full `-race`,
  private TMPDIR): real Landlock denials, no external network/DNS, path-escape denial, `Seccomp: 2`,
  read-only `/usr` vs writable workspace, surviving-child fail-closed, cancellation, an explicit
  cgroup unsupported-skip, and a real `store.RunBash` import of `g1` with containment
  `not-established`. Landlock is ABI 1 here (no REFER/TRUNCATE/network rights) and cgroup v2 is not
  delegated, so resource bounds are unestablished and carried honestly in every evidence class.
- **Bash command lease committed** (`0941557`): `DurableExecutor` takes an injected
  `sessionrepo.CommandRunner` and maps a trusted ALLOW `bash` to a `delta.OperationLease` through
  `Store.RunBash`; nil runner fails closed; inconsistent settlement quarantines; malformed args are
  rejected before any command runs.
- Next containment steps: validate the sandbox on the guest kernel 6.8 (higher Landlock ABI, possible
  cgroup delegation, unprivileged-userns/AppArmor behaviour) and compose the real runner into the
  `cmd/tbound` bash path end-to-end. The claim-bearing rootless Podman 4.9.3/crun + signed-entrypoint
  profile and Landlock ABI ≥5 rights remain guest-only and unimplemented.
- **Guest validation result** (kernel `6.8.0-146-generic`, HEAD `5e503642`, private module cache):
  Landlock ABI **4**, seccomp-bpf and pidfd present, cgroup v2 mounted but **not delegated** to
  `tboundadmin`. The Option-A cell could **not** start because Ubuntu 24.04's AppArmor
  unprivileged-userns restriction (`kernel.apparmor_restrict_unprivileged_userns=1`) denies
  `clone(CLONE_NEWUSER)` (`fork/exec /proc/self/exe: permission denied`; `unshare --user --map-root-user`
  also EPERM). The sandbox stayed honest — `Probe()` reported `UserNamespaces=false`, mechanism-requiring
  tests failed fast (2 PASS / 1 SKIP / 10 FAIL), and the cgroup test emitted an explicit unsupported
  skip; nothing silently passed and no host/guest security setting was changed. This blocks only the
  raw in-process userns cell (the **non-claim-bearing** dev/WSL profile). **Rootless Podman is viable
  on the guest**: Ubuntu ships AppArmor profiles for `podman`/`crun` (`/etc/apparmor.d/{podman,crun,unprivileged_userns}`),
  `podman info` reports `rootless=true cgroup=v2 oci=crun`, and `podman run --rm docker.io/library/alpine`
  exited 0 printing `podman-rootless-ok` (uid 0 in-container). So the claim-bearing container path needs
  **no host-config change**; raw `unshare --user` stays denied. Implement the rootless Podman 4.9.3/crun
  `CommandRunner` as the next containment milestone (image digest pin, signed entrypoint, offline cosign,
  and a real settlement observer remain to be added before any containment claim).

### Workstreams (ordered)
1. **Durable integration (next):** wire `internal/sessionrepo` + pre-effect durable audit into the
   `cmd/tbound` decision path (proposal → broker correlation → gate → durable intent/audit → effect
   → settled result) and assert generation state; keep the synthetic no-effect smoke as the
   transport-level harness.
2. **Contained executor:** a bounded, contained effect implementation (Landlock/seccomp/rootless
   Podman) behind the `Executor` seam.
3. **Publication + fixtures:** publication path, E04/E06 fixtures, frozen profile, disposable
   restore.
4. **Controller + docs reconciliation:** update the reviewed verifier helper's
   `ReviewedSourceCommit`; reconcile the stale host-automation pins/paragraphs.
5. **Guest re-verification:** deploy a committed snapshot that includes the integration slice and
   re-run the offline verifier.
6. **Real provider exchange:** still blocked on a privately supplied model ID + API key.

### Constraints
- Preserve unrelated working-tree changes; never read/stage/alter/delete `vm_start.txt`.
- Never request, record, print, or commit secrets, passwords, or private keys.
- No push without a separate instruction. Verify critical facts directly; delegate mechanical work
  to GPT-6 Luna (xhigh) subagents.

## Autonomous continuation plan — 2026-10-04

Goal: continue from the verified current state; fix all open issues, then advance the prototype.
The orchestrator delegates mechanical exploration, implementation, and testing to GPT-6 Luna
(xhigh) subagents and independently verifies critical facts.

### Verified at session start (repo + host + guest)

- VM `TBound-Ubuntu-2404` is **Running**, NIC connected to `Default Switch`; pinned-key SSH
  works to `172.24.196.42`; guest booted `2026-10-04T13:56:03Z`; a tty1 console login is
  active; no tmux/verifier/launcher/Go process is running.
- The VFH offline run is **PASS**. `verification.json` SHA-256
  `fbed5d86a9afc79c5889d782bcbe5d03f2cffa921fe4df42f0c16d6ced8667f8` and
  `artifact-manifest.json` SHA-256
  `0c4864a585c9870ab5a2bd4cdb0c97f3608c9cbd0489dc3f489b5f84c5321598` were recomputed and
  match; 6/6 checks PASS; 85/86 Go events (one expected skip); ownership fixture 1/1; Pi
  0.87.1 with zero provider-stream attempts; **all four claims remain false**.
- The Windows host now has **Go 1.27.1** (`windows/amd64`) and Node `v24.15.0`; Python is
  absent. (The earlier note "Go is unavailable on the host" is superseded.)
- `F:` has 868,471,529,472 bytes (~808.8 GiB) free; storage gates pass.
- Host automation is **not installed**: `C:\ProgramData\TBoundHostAutomation` is absent and no
  `\TBound\*` tasks exist. `FABIAN\CodexSandboxOffline` (SID `…-1004`) exists and is enabled.
  This session runs as **`FABIAN\Admin`** (SID `…-1001`), **unelevated**, with UAC secure-desktop
  consent (`ConsentPromptBehaviorAdmin=5`).
- Host-automation bundle pins verified against the actual files: bootstrap `0EF86511…`;
  internal pins Install `7638C052…`, Actions `BB39E08E…`, Policy `C06BAE16…`, Initializer
  `E521CCB7…`; the installer action pin `9EF7B338…` reproduces exactly as the canonical-LF
  SHA-256 of `TBoundHostActions.ps1`. The `37405715…` paragraph in TASK_STATE/implementation
  status is **stale** and should be marked superseded.

### Open issues found (must fix / follow up)

1. `supervisor/internal/broker/openrouter/openrouter.go:417` does not compile:
   `integer(o,"created")` while `integer` takes `[]byte`. This package has never compiled.
2. `supervisor/internal/workspace/workspace_linux.go:590` uses undefined `syscall.O_PATH`.
   Never compiled. Fix with a package-local Linux constant; **do not add a new module** because
   the offline verifier resolves the module graph with downloads disabled.
3. `supervisor/internal/sessionrepo` is blocked by (2); the uncommitted lifecycle slice has no
   compile/test evidence.
4. `gofmt` is not clean on the sessionrepo files and the modified delta/audit files.
5. The controller `ReviewedSourceCommit` (`5ccc52b2…`, `Invoke-TBoundTrustedOfflineVerifier.ps1`
   line 21) is stale relative to current source and must be frozen/reviewed/updated before an
   offline trial.
6. Host-automation docs disagree on deferral vs. the reauthorized narrow helper, and carry the
   stale `37405715…` pin.
7. **VM power/NIC autonomy is blocked**: this session is unelevated, the fixed helper is not
   installed, the installer requires the VM `Off`, and the reviewed policy targets SID `…-1004`
   rather than this session's `…-1001`. A one-time elevated step plus a small reviewed policy
   decision is required; the orchestrator asks before proceeding.

### Workstreams

- **WS1 (now):** fix the compile errors, gofmt, run the portable Windows tests, cross-compile
  `GOOS=linux`, then get the sessionrepo lifecycle compiling and (on Linux) tested; commit the
  reviewed lifecycle slice in isolated commits; reconcile docs/pins.
- **WS2 (blocked on user decision):** one-time elevated host step so this session can start,
  shut down, disconnect, and reconnect the VM autonomously. Options: (A) install the reviewed
  fixed-task helper granting this session's SID (recommended; requires VM `Off` first);
  (B) add `FABIAN\Admin` to Hyper-V-Administratoren (broader; needs a new logon); (C) keep VM
  operations manual for now.
- **WS3 (after WS1/WS2):** deploy the fixed source to the guest, re-run the offline verifier
  pipeline, then continue the thesis path (provider/broker + E05, generation/sealing/publish,
  containment/executor, durable-authority faults, reconstructable evidence, E04/E06, frozen
  profile, disposable restore).

### Constraints

- Preserve unrelated working-tree changes; do not read, stage, alter, or delete `vm_start.txt`.
- Never request, record, print, or commit secrets, passwords, or private keys.
- No host-automation install without the user's one-time elevation approval.
- Orchestrator keeps context lean and verifies critical results directly.

### Progress — this session (2026-10-04)

- **Host autonomy installed and verified.** Created protected `C:\ProgramData\TBoundVmOps`
  (DACL: SYSTEM/Administrators full control, `FABIAN\Admin` SID `…-1001` read/run only) and five
  fixed tasks in `\TBoundVmOps`: `Status`, `Start`, `Stop`, `Connect`, `Disconnect`. Each runs as
  `FABIAN\Admin` (S4U, highest) with a fixed action script that hardcodes VM name/GUID
  `c676170c-31a1-48e3-aa74-1d845411d253`, adapter `Netzwerkkarte`, and `Default Switch`; there are
  no caller-controlled VM/switch arguments. Verified from the unelevated session: `schtasks /run`
  works, ACL modification is denied (`E_ACCESSDENIED`), and `Status` returns live state. Each
  install/recon step used a single UAC elevation.
- Guest IP after a restart is rediscovered from the pinned MAC `00-15-5D-0C-40-00` via the host
  neighbor table (`172.24.196.42`). Guest `sudo` requires a password and the guest has no
  `hv-kvp-daemon`; no persistent `NOPASSWD` was added, so clean shutdown is Hyper-V-side.
- **Supervisor now builds and passes all tests on Linux and Windows.** Fixed: the `openrouter`
  `integer` call; the malformed `edit` tool JSON schema (miscounted braces / `required` nested
  inside `properties`); `workspace` `syscall.O_PATH` (package-local `linuxOPath`); `sessionrepo`
  Linux compile issues; and the `sessionrepo` runtime `ErrInvalidOptions` — tree digests are
  profile-qualified (`tbound-tree-jcs-rfc8785/v1:sha256:…`) and are now validated by
  `validTreeDigest`. Added `sessionrepo/context_linux_test.go` proving plain/unknown/uppercase/short
  digests remain rejected. `gofmt` is clean and no Go module dependency was added.
- Verified with WSL2 Ubuntu + Go 1.27.1 (private mode-0700 `TMPDIR`, because `audit` rejects
  world-writable ancestors): `go test ./...` green across e2e, audit, protocol, openrouter, delta,
  sessionrepo, workspace. Windows host `go build ./...` and the portable tests are green.
- **Autonomous VM lifecycle proven** (2026-10-04 ~19:55 local, from the unelevated session):
  `Disconnect` dropped the guest (TCP 22 false), `Connect` restored it (TCP 22 true), `Stop`
  reached clean Hyper-V `Off` in ~2 s (guest `hv_utils` shutdown integration works; no force-off),
  and `Start` returned it to `Running`. Final `Status`: Running, adapter on `Default Switch`.
  The guest DHCP address changed across the restart (`172.24.196.42` → `172.24.196.229`), and
  Hyper-V exposes no adapter IP (no guest `hv-kvp-daemon`), so after every start the address must be
  resolved from the pinned MAC `00-15-5D-0C-40-00` via the host neighbor table before pinned-key
  SSH (host-key alias stays `172.19.207.142`). This address step is the one manual/wrapper gap in
  the VM loop; the rest is fully autonomous.
- Still open: host-automation (reviewed verifier helper) decision; controller `ReviewedSourceCommit`
  update; wire the Linux WSL build/test into the normal workflow. Code fixes are committed
  (`a4a83ee`, docs `726dda5`).

### Approved next steps — 2026-10-04 (both directions)

The user approved doing both, concurrently where safe:

1. **Guest re-verification.** Export the committed source with `git archive HEAD` and deploy it into
   the guest tree `~/tbound-handoff-9a73af56e8a8/tbound` (backup first), then run the offline
   verifier autonomously: queue `run-offline-queued.sh` in the guest, disconnect the NIC with the
   `\TBoundVmOps\Disconnect` task, wait for the offline gate + run, reconnect with `\TBoundVmOps\Connect`,
   resolve the guest IP from the pinned MAC, collect and hash-verify the report. The guest verifier
   discovers its package set via `go list ./...`, so `sessionrepo`/`workspace`/`openrouter` are
   included, and it already provides a private `TMPDIR` and `HOME` for the Go tests.
2. **End-to-end path.** Implement `cmd/tbound` (supervisor entry point), `internal/ipc` (bounded,
   session-bound proposal/result transport), and `internal/gate` (canonicalization + deterministic
   policy decision), against the normative specs, developing/testing on WSL2 (Go 1.27.1) and the
   Windows host. A real provider exchange still requires a model ID + API key supplied privately.

Sequencing: direction 1 deploys the committed `HEAD`, so concurrent direction-2 working-tree edits
cannot contaminate the deployed tree. Direction 2 must not weaken the existing security checks and
must not break the current green test suite.

### Direction-1 progress — guest offline re-verification (2026-10-04)

- First attempt against `726dda5` returned **FAIL**, solely in `sessionrepo` (`go_tests`/`go_race`);
  `adapter_dependencies`, `go_modules`, `node_adapter`, and `ownership_fixture_test` passed
  (report SHA-256 `87b443ff4e2dc3a922cb6c0526e000297ad226e187169cf39a318055d283a048`, evidence copy
  under `%TEMP%\opencode\evidence-726dda5`). All failures were `newE2EStore` → `audit.Open`.
- Root cause (guest-specific, not a product bug): the new E2E test used raw `t.TempDir()` (→ `/tmp`,
  mode 1777) for the audit-journal base, which `audit.checkTrustedAncestor` correctly rejects, and
  its fixtures relied on the process umask (the verifier sets `umask 077`, turning requested
  0644/0755 into 0600/0700, which the canonical-seed import correctly rejects).
- Fixed in `supervisor/internal/sessionrepo/sessionrepo_e2e_test.go` (commit `bab67bd`): a private
  test base with a trusted ancestor chain (prefers HOME, mirroring audit's `privateJournalTestDir`)
  and explicit `chmod` for fixture files/directories. No product check or test assertion was
  weakened. Re-verified with a verifier-like WSL `-race` run (`TMPDIR` under `/tmp`, `umask 077`)
  and the guest sessionrepo tests: all pass.
- Re-run against `bab67bd` **PASSED** at `2026-10-04T18:34:08Z` (run
  `~/.tbound-offline-queued.6u8CoWrEvN`; `verification.json` SHA-256
  `9b2533eb856281a6d2470dceebe9e8eb4f7c15cebf1c7c11410d7fcc0b890921`, size 124,192 bytes; runner and
  launcher exit 0; `supervisor_source_sha256`
  `41a6e1edac91aec0926265c7c5e8897d416d1d15cfbf77982117a94b8128f1d0`). All six checks PASS
  (`adapter_dependencies`, `go_modules`, `go_race`, `go_tests`, `node_adapter`,
  `ownership_fixture_test`), cleanup PASS, no failures, and the four provider/effect/containment/G1
  claims remain false. This is the first offline verifier run whose package set includes
  `sessionrepo`, `workspace`, and `openrouter` (normal + `-race`). Evidence copy under
  `%TEMP%\opencode\evidence-bab67bd`.
- Note on pins: the archived tree's `supervisor/go.sum` is the CRLF variant
  (`fffd308c722c967519775e13035f57c57edb8c50403365c8b39741b9124b1f3f`) of the committed LF file
  (`2ff7956b…`) because this Windows Git has `core.autocrlf=true`; the verifier's `go_modules` check
  passed on it and the repository working tree is unchanged at the LF pin. Use
  `git -c core.autocrlf=false archive` for future exports so deployed blobs match the commit exactly.

### Direction-2 progress — end-to-end path (2026-10-04)

- Implemented and committed (`923abb1`): `supervisor/cmd/tbound`, `supervisor/internal/ipc`,
  `supervisor/internal/gate`, plus bounded proposal/result codecs in `broker/protocol`.
  - `ipc`: strict length-prefixed JSON envelope (4-byte big-endian length, 1 MiB message cap,
    256-bit lowercase-hex binding token checked on every frame, sequence starting at 1 and strictly
    +1, per-direction caps of 1,024 frames / 16 MiB), with a Linux unix-socket transport
    (supervisor-owned mode-0700 parent, socket 0600, one connection per session) and a portable
    `net.Pipe` backend. Duplicate/unknown fields/kinds, replay, wrong-direction, truncation, and
    oversize input fail closed; clean EOF returns `ErrClosed`. The token is explicitly not peer
    authentication.
  - `gate`: versioned `tbound-policy/v1` per-tool allow/deny rules, deny-by-default, unknown
    versions/tools and duplicate rules rejected, stable policy digest; requires a broker-correlation
    receipt before policy evaluation/argument canonicalization.
  - `cmd/tbound`: injected broker/gate/executor loop; synthetic broker and stub executor only. No
    real provider call, containment, durable audit wiring, session-repository operation, or
    publication path. The executable refuses unconfigured session startup.
- Independently verified: full Linux `-race` suite under verifier-like `TMPDIR`/`umask 077` (all 10
  packages including the three new ones), Windows `go test ./internal/... ./e2e`, `gofmt -l` empty,
  `git diff --check` clean, and no `go.mod`/`go.sum` change.
- Extended (`1cba0ba`): `internal/broker` — a concrete supervisor broker. Given a trusted registered
  profile it validates the exact ordered four-tool manifest, builds the OpenRouter request, records
  bounded request/response bytes in memory through an injected `HTTPDoer`, and correlates IPC
  proposals against registered provider calls (trusted response/call issuers, exact tool name,
  canonical argument digest, sequence, and one-use status) before gate policy evaluation. Wired into
  the `cmd/tbound` loop (ipc → broker correlation → gate → result) with a fake transport. Records
  are process-local, not durable audit evidence; no real network, containment, or publication.
  Verified with the full Linux `-race` suite (11 packages incl. `internal/broker`) under
  verifier-like `TMPDIR`/`umask 077` and the Windows suite.
- Adapter-side IPC client committed (`89c5320`): `adapter/src/ipc-transport.ts` implements the framed
  transport matching the Go `tbound-ipc/v1` wire format (4-byte big-endian prefix; 1,048,576-byte
  message / 1,048,580-byte frame caps; 64-lowercase-hex binding token checked per frame; strictly
  increasing sequence from 1; 1,024-frame / 16 MiB per-direction caps; fail-closed), with a Linux
  Unix-socket dialer and an in-memory duplex for tests, plus `ipc-transport.test.ts`. `proxy-tools.ts`
  gains `createIpcProxyTools` while keeping the injected `ProposalSender` for the closure probe.
  Verified: constants match `supervisor/internal/ipc` exactly; `npm run check` passes (14/14 tests);
  the probe still exposes exactly `read/write/edit/bash` with `provider_stream_attempts=0`; no new
  dependency (`package-lock.json` unchanged).
- Supervised Unix-socket integration committed (`ca9f227`): `cmd/tbound` gains an explicit
  `--smoke-listen --socket-dir DIR` mode that validates a caller-created, supervisor-owned
  mode-0700 directory, creates a mode-0600 Unix socket and a transient mode-0600 binding-token
  file, accepts one connection through `internal/ipc`'s Linux transport, and runs exactly four
  canned SSE captures through the concrete broker with a fake `HTTPDoer` and a no-effect stub
  executor across the existing ipc → broker correlation → gate → result loop. Standard output is a
  bounded JSONL proposal/correlation/decision/result transcript. Added a Linux-only real-socket
  round-trip test (`unix_integration_test.go`, skips on non-Linux), `adapter/src/ipc-smoke-client.ts`
  (transport-only Node CLI sending the fixed read/write/edit/bash proposals), and
  `supervisor/ipc-smoke.sh`, which copies the module to a private ext4 workdir, builds, starts the
  listener, runs Node `--experimental-strip-types` with the pinned Linux Node, and asserts all four
  transcript/result pairs. Independently verified by the orchestrator: full Linux
  `-race -count=1 ./...` green (all 12 packages), `gofmt -l` empty, `git diff --check` clean, no
  `go.mod`/`go.sum`/`package-lock.json` change, adapter `npm run check` exit 0 (typecheck + 14/14
  tests + closure probe with `provider_stream_attempts=0`), and the cross-language smoke exit 0
  (`Linux cross-language IPC smoke passed (4 proposal/correlation/gate/result records).`). This is
  synthetic-only: no provider/network call, no real tool effect, no containment, publication, or
  durable audit, and the binding token is not peer authentication.

### End-to-end integration follow-ups (remaining)

The synthetic cross-language path (real Unix socket, real `tbound-ipc/v1` framing, four proposals)
is now proven. What it deliberately does **not** cover, and what remains on the thesis path:

1. Wire the durable session repository and pre-effect audit into the decision path, and assert
   generation state across the exchange (the smoke uses a no-effect stub executor and process-local
   broker records only; it does not touch `sessionrepo`, sealing, or audit).
2. Implement a contained executor (Landlock/seccomp/rootless Podman) for real tool effects.
3. Implement the publication path, add E04/E06 fixtures, freeze the profile, and add the
   disposable-restore test.
4. A real provider exchange still requires a model ID and API key supplied privately; only a
   scripted fake `HTTPDoer` is configured.
5. Update the reviewed verifier helper's `ReviewedSourceCommit` and re-run the guest offline
   verifier against a committed snapshot that includes the integration slice.

## Current goal

Continue the existing TBound prototype on branch codex/tbound-prototype in this standalone repository:

C:\Users\Admin\Desktop\FH\Master Software Engineering\MA Thesis\trust-boundary

The VFH offline verifier run now has a hash-verified PASS for the guest source pins recorded in its report. It does not accept the current repository working tree or establish any of the four provider, effect, containment, or G1 claims. See the current guest-state section below.

Next, complete and review the durable session-repository slice in the current repository. Then implement and verify the actual Pi/provider/broker flow, runtime controls, publication path, reconstructable evidence, and disposable restore. Keep the full thesis scope and checklist unchanged; the VFH report does not complete the end-to-end demonstration. The full thesis experiments will later run on a separate dedicated Linux environment.

## Current host access helper handoff — 2026-10-04

The user reauthorized the narrow TBound helper preparation and one-time Admin installation. This supersedes the earlier host-automation deferral only for this trusted-verifier helper. It does not grant broad Hyper-V group/Admin membership. No helper installation, task registration, VM operation, key generation, guest enrollment, or private-key access has occurred.

The helper changes are in `infra/hyperv/Bootstrap-TBoundHostAutomation.ps1`, `Install-TBoundHostAutomation.ps1`, `TBoundHostAccessPolicy.psm1`, `Initialize-TBoundOperatorSsh.ps1`, and `Invoke-TBoundTrustedOfflineVerifier.ps1`; supporting notes are in `TBOUND-HOST-AUTOMATION.md`, `INSTALLATION-RECORD.md`, and this file. The policy grants only the exact FABIAN\CodexSandboxOffline SID (`S-1-5-21-2350865082-651554413-1548572510-1004`) run/query access on five fixed SYSTEM tasks. The old Admin-owned SSH key remains untouched. The new separate operator key is not generated or enrolled.

The source bundle uses an external hash guard because the repository is operator-writable. The raw bootstrap SHA-256 is `0EF86511A6A00F0EB3F285E9CDD6D9703892C9F311B86A427E367130CC335469`; the exact byte-capture/hash-check/ScriptBlock command is recorded in the “One-time administrative install” section of `infra/hyperv/TBOUND-HOST-AUTOMATION.md`. The internal source-pin chain and outer documentation pin passed 9/9 checks. Windows PowerShell 5.1.19041.6456 policy/native tests passed 52 assertions, zero failures, including a root-side repeat; no installer or bootstrap was executed. Independent final static review found the hash guard and ACL design coherent. Live Task Scheduler SDDL readback remains unproven until a human installation. The repository uninstaller is not in the protected bundle: never run it elevated from the repository; task removal needs its own reviewed hash guard and protected copy.

Next: finish that review and recheck the exact bundle hashes. Before any Admin install, inspect current guest work and host state. The last user report says the VM is Running; the installer requires it Off and the NIC disconnected. Since the helper is not installed, any shutdown must follow a user-approved manual path after confirming no active guest work. Do not force-off or restore. Then run the exact hash-guarded bootstrap from elevated Windows PowerShell/UAC. Inspect installed task definitions, task/folder SDDL, protected file ACLs and an `Inspect` receipt. Run the protected key initializer as the operator, then have a human Admin enroll only its public key through the old protected credential with OpenSSH `restrict`; this still gives the key normal `tboundadmin` shell/SCP access. Finally verify pinned SSH. The controller's older `ReviewedSourceCommit` (`5ccc52b2fa35be3dde9d3839f63ec613627b796b`) will reject the current lifecycle source; freeze and review the exact source snapshot and update that gate before an offline test.

## Session-repository source handoff — 2026-10-04

The current uncommitted lifecycle slice updates `supervisor/internal/sessionrepo/` and `supervisor/internal/delta/delta.go` plus `delta_test.go`. It binds durable transitions to tool and normalized execution-argument digests, keeps Bash view/context identity separate, snapshots caller-owned edit and command inputs, and reopens by revalidating command intent, receipt, settlement, and transition evidence. Bash view IDs are random and historical IDs are reconstructed from successful intents and denial records. The E2E fixtures cover mismatched receipts/contexts, no-run denial, input-slice mutation, reopen recovery, and historical view-ID collisions. Normalized sessionrepo argument digests are not asserted to equal raw Pi proposal JSON digests.

Static review and `git diff --check` completed; no Go test, race test, or `gofmt` run was possible because Go is unavailable on the host and WSL/VM access is denied for this non-admin account. Deterministic source-swap coverage and injected persistence-failure tests remain pending. This source snapshot is not deployed to the VM; runtime containment, provider exchange, and complete demonstration claims remain unverified. The `sessionrepo/` package is untracked, while the delta files are modified tracked files; preserve unrelated audit, documentation, infrastructure, and `vm_start.txt` changes.

## Current guest and automation state — 2026-10-04

The user reports the existing VM is Running and guest eth0 is UP at 172.24.196.42/20. The latest-boot Basic Session attestation is pending; the previous baseline attestation is not current. The host adapter state was not queried during report retrieval. Use this session only for trusted maintenance; no new untrusted guest run is authorized.

The saved run /home/tboundadmin/.tbound-offline-queued.VFHmYRa5HO passed its offline gate at 2026-10-04T09:06:39Z and completed at 09:07:19Z. Its eight-file capture is at F:\TBoundAssets\ProvisioningSSH\TBoundTrustedCapture-VFHmYRa5HO; all source and local hashes matched the reviewed metadata. verification.json SHA-256 is fbed5d86a9afc79c5889d782bcbe5d03f2cffa921fe4df42f0c16d6ced8667f8; artifact-manifest.json SHA-256 is 0c4864a585c9870ab5a2bd4cdb0c97f3608c9cbd0489dc3f489b5f84c5321598.

That report has schema tbound.guest-verification/v2, status PASS, six checks PASS, no failures, cleanup PASS with zero leftovers, runner and launcher exit 0, and host-adapter-disconnected attestation true. Normal and race runs each covered five packages and 86 test events: 85 passed, zero failed, with the one expected ownership test skip; the separate ownership fixture passed 1/1. All four claims (live_provider_exchange, live_effect_execution, containment_established, g1_durability_established) are false. This is a successful trusted offline verifier run against the source pins recorded in that report, not acceptance of the current repository working tree, a provider exchange, containment proof, or completion of the thesis checklist. The later OpenRouter and workspace source packages have not been deployed to the guest.

The VFH capture recorded 29,200,952,811 tracked bytes and 867,972,812,800 bytes free on F:, within the 100-GiB/700-GiB limits. Helper installation remains pending; see the current host-access handoff above.
## Decisions and constraints

- FABIAN is Windows 10 Education. Keep its current Windows version.
- Use the existing Hyper-V guest `TBound-Ubuntu-2404`, Ubuntu Server 24.04.5 LTS, Gen 2, 8 vCPU, 16 GiB fixed RAM, 40 GiB dynamically expanding VHDX, with at most one checkpoint.
- The configured storage budget is at most 100 GiB of tracked TBound files on F: and at least 700 GiB free. VM creation required an 800 GiB free-space gate. Do not expand the VHDX or create a golden export without a renewed budget review.
- Hyper-V Basic Session is the intended console mode. “Erweiterte Sitzung” was observed greyed out; do not enable clipboard or device redirection. This visual observation alone is not a complete security proof.
- Temporary Default Switch networking is allowed for trusted provisioning and maintenance. Disconnect it and verify the offline gate before any untrusted execution or offline trial. The provider-only runtime route is not implemented or verified.
- OpenRouter is the primary planned provider. OpenCode Go is optional for testing and may be selectable later; exact authentication compatibility for a second profile is unverified. These are portable design plans, not implemented or verified provider support. Model IDs are deferred. Never request or record API keys or passwords in chat, logs, Git, or this file. Accurately identify any hosted provider/model actually used.
- User approved this initial workspace metadata policy: admit regular files/directories; preserve file contents and executable bits; normalize ownership and other permissions; exclude timestamps from generation identity; reject symlinks, hard links, special files, ACLs, extended attributes, and file capabilities; represent sparse files as ordinary file contents; fail explicitly on unsupported entries. The workspace package implements directory mode `0755`, regular-file mode `0644` or `0755` when executable, normalized ownership, and a canonical manifest from the existing delta types. Its static-reviewed Linux code has not been compiled or tested.
- Keep TBound files in this standalone repo so it can later become its own repository.
- Orchestrate at a high level. Delegate mechanical code reading, implementation, and tests to GPT-6 Luna agents with xhigh reasoning. Preserve unrelated work.
- Document a reproducible fresh Windows setup. Clearly mark fresh Linux-host migration as untested; do not invent a tested KVM procedure.
- Do not push or publish without a separate instruction.

## Current verified state

### Git and files

- Branch: `codex/tbound-prototype`
- Repository HEAD before this documentation update: `9e1855fecea598d5d38539246e44221244d37710`. The new OpenRouter and workspace packages remain uncompiled and untested.
- This evidence reconciliation updates `TASK_STATE.md`, `docs/implementation-status.md`, and `infra/hyperv/INSTALLATION-RECORD.md`. The README remains unchanged because its architecture-scaffold status is still accurate. The preexisting untracked `vm_start.txt` is unrelated; do not read, stage, alter, or delete it.
- This `TASK_STATE.md` is a handoff note requested by the user; it is not a test result or implementation.
- Preserve unrelated working-tree changes, including `vm_start.txt` and any other untracked paths. Do not read or alter `vm_start.txt`; do not stage unrelated work.

Recent relevant commits:

- `55c41e1` initial standalone repository import (adapter, supervisor, guest and Hyper-V scaffolds).
- `4878b7e` bounded delta-chain validation.
- `68de01b` APT trust-guard keyring-path correction and regression tests.
- `c8bdaa2` preserve system PATH for the offline Go cgo check.
- `5306d4a` record baseline and the first offline verifier failure.
- `b336602` persistent-tmux offline queue launcher and guest instructions.
- `764bff3` treat held-but-installed Debian packages as installed in the verifier and add a parser regression test.
- `588804b` fresh-host setup guide and README link.
- `7d71231` record the second-run held-package fix and failure.
- `2e342f4` add the TBound task handoff state.
- `980f36d` clarify setup paths and implementation status.
- `9bcf88b` add Go module failure diagnostics.
- `711e641` complete offline Go dependency preparation.
- `99f87dd` add the planned restore acceptance runbook (untested).
- `d317e7a` add the OpenRouter request/parser/capture mapping only; no provider request or Pi wiring.
- `e187650` commit the five-file bounded workspace package candidate; uncompiled and untested.
- `2644a9f` record the preserved Oct 3 offline Go test failure evidence.
- `833915a` retain bounded Go-phase failure diagnostics in the verifier.
- `7c91b47` fix the unused local in the audit duplicate-effect check; guest deployment and the later test result are recorded below.
- `1f03c76` add the follow-on Linux audit symlink-classification fix; it is not applied to the guest or tested.
- `9e1855f` record the applied guest source overlay.

The OpenRouter request/parser/capture mapping is committed at `d317e7a85c09b6d71c1dce557eeb759ce8fa79e4` in `supervisor/internal/broker/openrouter/`. Strict Unicode validation and pre-marshal size bounds passed static review only. It has not been compiled, formatted, or tested; there have been no HTTP requests, Pi wiring, or live provider support proven.

The committed five-file workspace candidate at `e187650` includes `supervisor/internal/workspace/`: descriptor-relative scanning/import, bounded directory reads and streaming, mount identity checks on fresh opened file descriptors before and after reads, disjoint-root validation, and normalization into existing `delta.TreeManifest` types. Static author and independent review accepted these fixes. It has not been compiled, gofmt-checked, or tested; no mount-regression test has run. It does not seal or publish generations, connect approved deltas to a durable ledger, or prove containment. Quiescence and complete xattr visibility remain caller/profile obligations and are not proven on the guest. The new workspace and OpenRouter packages have not been deployed to the guest.

The implementation tree includes `adapter/`, `supervisor/`, `infra/guest/`, `infra/hyperv/`, and `docs/setup.md`. The latest README correctly labels the project “architecture scaffold only” and states that no provider exchange, containment run, conformance result, or WP1/G1 evidence has been established. Review the normative thesis checklist/specs in the original thesis repository when resuming implementation.

### VM and guest baseline

The installed VM was configured with the verified Ubuntu 24.04.5 AMD64 ISO, Linux Secure Boot template, one disconnected NIC for baseline verification, and the intended disk/CPU/RAM settings. One checkpoint named `ubuntu-24.04.5-golden-baseline` exists. Its inventory reported `SnapshotType=Standard` even though the configured checkpoint policy was ProductionOnly with VSS disabled; that inventory field does not prove creation policy or guest freeze/thaw behavior. The checkpoint restore has **not** been tested.

Prior host Verify passed with the VM Off, one checkpoint, expected disk chain, one disconnected adapter, ISO ejected, installed disk first in boot order, Basic Session attestation, and the F: storage gates. At that time F: had 826.61 GiB free and tracked TBound data used 10.44 GiB. Those figures are historical. Recheck the live host before changes.

Guest facts previously verified: Ubuntu 24.04.5 LTS, kernel `6.8.0-146-generic`, amd64; account `tboundadmin`, UID 1000; German keyboard mapping; root filesystem ext4 on LVM; 65,536-entry subordinate UID and GID ranges. Node.js `v24.21.0`, Go `go1.27.1`, Podman `4.9.3`, crun `1.14.1`, GCC `13.3.0` were provisioned/pinned. Go and npm dependency preparation ran online; npm lifecycle scripts were disabled. The Oct 3 console reported `tmux list-sessions` output `tbound-offline: 1 windows (created Fri Oct 2 17:33:58 2026) (attached)`. Record the session as currently observed; do not infer how it survived host or guest shutdown, and inspect actual state before attaching.

The setup script does not install tmux. The setup guide now says to verify `command -v tmux`, install it during trusted provisioning if absent, record its version, and confirm it before baseline creation.

### Isolation and maintenance access

The VM was previously in Basic Session. During offline verification, the network gate observed every guest non-loopback carrier at 0 and no IPv4 or IPv6 default route; the launcher also recorded the host adapter-disconnected attestation. These are offline prerequisites, not proof of runtime containment.

SSH maintenance was explicitly authorized and a temporary key was installed. Never read, print, copy into the guest, or commit the private key:
`F:\TBoundAssets\ProvisioningSSH\guest-maintenance-ed25519`.
Its dedicated known-hosts file is `F:\TBoundAssets\ProvisioningSSH\known_hosts`. The last verified guest host-key fingerprint was `SHA256:22ZTQXN5L54AhovENJ0Eo7ZViomYVvlHRWfbG3izY5g` (the substring `YVvl` contains lowercase `l` after `v`). The Oct 3 console reported guest IP `172.24.148.77/20` (metric 100), and pinned-key SSH succeeded there; `172.19.207.142` is the existing host-key alias, not the destination. Reconfirm the destination IP at the Ubuntu console before each use. The following PowerShell command runs read-only identity, kernel, and tmux status checks with strict host-key verification:
~~~powershell
ssh -F NUL -o "UserKnownHostsFile=F:\TBoundAssets\ProvisioningSSH\known_hosts" -o GlobalKnownHostsFile=NUL -o StrictHostKeyChecking=yes -o HostKeyAlias=172.19.207.142 -o HostKeyAlgorithms=ssh-ed25519 -o PubkeyAcceptedAlgorithms=ssh-ed25519 -o KexAlgorithms=curve25519-sha256,curve25519-sha256@libssh.org -o ForwardAgent=no -o ClearAllForwardings=yes -o BatchMode=yes -o IdentitiesOnly=yes -o IdentityAgent=none -i "F:\TBoundAssets\ProvisioningSSH\guest-maintenance-ed25519" tboundadmin@172.24.148.77 "id -u && hostname && uname -r && tmux list-sessions"
~~~

After the second failed run completed on Oct 2, the user confirmed a maintenance reconnection. On Oct 3, user-provided elevated host inventory reported the VM Running, Generation 2, `ProductionOnly`, with automatic checkpoints disabled; one adapter `Netzwerkkarte` on `Default Switch`; one checkpoint; and F: free space of 810.17 GiB. The inventory reported an attached disk path ending in `TBound-Ubuntu-2404_7434EDE1-4C24-46FB-932E-DA4CC74AD396.avhdx`; that path alone does not establish the full disk chain or restore success. Root has not independently queried the host. The Oct 3 console showed `Enhanced Session` grey and the attached tmux session noted above. The user confirmed current guest IP `172.24.148.77/20` (metric 100), and a pinned-key SSH maintenance probe succeeded as `tboundadmin` UID `1000`, hostname `tboundubuntu2404`, kernel `6.8.0-146-generic`. The first process-filter query had quoting errors; the corrected bounded read-only query found no active Go, npm, Node, verifier, or launcher process. The tmux session and an expected system unattended-upgrade/shutdown Python process were present. At that earlier console check, no verifier had been started and no new offline run had yet been queued. Do not change VM or network state, restore, or queue another run until the live process state and next action are reviewed.

The user once typed a long command in VMConnect and the keyboard produced garbled symbols; a reboot restored input. Prefer SSH for trusted maintenance and use VMConnect for observing/reattaching to the existing terminal. The earlier `tmux a` command was mistakenly tried in Windows PowerShell once and then redundantly inside the already attached Ubuntu tmux, producing the nesting warning. Do not run Windows-side `tmux`; do not nest/unset `TMUX`.

## Prior offline verifier evidence through 19:59 UTC

The first two queued runs failed before their Go/Pi suites. The Oct 3 afternoon run passed the offline gate and produced an incomplete Go test summary. The 19:25 UTC run passed the gate but failed compilation; the later 19:59 UTC run compiled and reached the audit tests, where one symlink-classification assertion failed. Its evidence is pinned below. No successful full Go/Pi suite is established.

1. Earlier report `~/tbound-offline-verify.json`: failed because the sanitized Go check omitted system `PATH`, so cgo reported disabled. Host-side diagnosis verified adding `PATH=/usr/bin:/bin` fixed the cgo reading. Commit `c8bdaa2` updated the verifier.
2. First queued run, private guest directory `~/.tbound-offline-queued.234fHWnyYm`: failed because the manifest parser treated held-but-installed `gcc-13-x86-64-linux-gnu` (`dpkg` status `hi `) as missing. A targeted pure-stdlib parser regression passed; updated verifier was deployed to the guest.
3. Second queued run, private guest directory `~/.tbound-offline-queued.2xrGRICwtY`: queued `2026-10-02T18:21:33Z`, offline gate passed `18:26:38Z`, completed `18:26:39Z` with runner and launcher exit 1. JSON SHA-256 was `72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae`. It failed at `go_modules`: “offline Go module graph resolution failed; provision go.mod dependencies first.” It recognized Go 1.27.1 and held GCC 13.3.0. Before dependency preparation, during trusted Default Switch maintenance, a read-only metadata-only `go list -m all` ran as UID `1000` from a private fresh staging directory with safe `HOME`/`GOCACHE`, the actual `GOMODCACHE`, and the same downloads-disabled verifier environment (`GOPROXY=off`, `GOSUMDB=off`, and related settings). It exited `1`; stderr consisted of six repetitions of `go: module lookup disabled by GOPROXY=off` and named no modules. This confirms the actual cache cannot resolve the graph with downloads disabled, but does not identify the blocking lookup. A separate module-cache inventory found .mod metadata, but did not verify full module archives, for six transitive requirements: testify v1.7.0, go-spew v1.1.0, go-difflib v1.0.0, objx v0.1.0, yaml.v3 v3.0.0-20200313102051-9f266ea9e77c, and check.v1 v0.0.0-20161208181325-20d25e280405. This does not establish which lookup caused the failure. The host repository go.sum exists with two JCS checksum lines. The guest metadata baseline go.sum had eight lines: those two JCS lines plus six transitive go.mod hashes, so it differs from the host file. go.mod declares module tbound/supervisor, Go 1.21, and jcs v1.0.1. An earlier wrong-directory invocation returned `go.mod not found`; it was corrected and is separate from the recorded graph failure. The earlier diagnostic staging was cleaned; no provider call, Go application, or test suite ran.
4. Root independently read the preserved host `verification.json` for the Oct 2 second run; SHA-256 `72927f8e5f160af51c66f60cb15fa12a89dcd10acb2aa39acdcf00ab1548b6ae` matches the guest report. This verifies only that JSON file; the full seven-file evidence folder has not been independently compared.
5. Third queued run (2026-10-03): the user refreshed `sudo -v`; the assistant queued one launcher over SSH at 13:09:52 UTC. Root read `WAITING_FOR_OFFLINE` at 13:13:05 UTC; launcher PID `9019` had a sleep child and Bash parent PID `1949`. The user confirmed the guarded NIC disconnect, PowerShell showed blank `SwitchName`, and the offline gate passed at 13:18:33 UTC. The verifier completed at `2026-10-03T13:19:19+00:00` with runner and launcher exit code `1`, status `FAIL`.
6. Preserved evidence is in `F:\TBoundAssets\Evidence\offline-20261003-131833`. Root independently verified `verification.json` SHA-256 `5c5fa36fea3d91c36f70c32100f4f1cfaca2f86fe6ec1169a452ddbb8baa8350` and `capture-manifest` SHA-256 `48170022e98ed0c15216074e8b8f276b3f83389f81e7273c555c016062397878`. An agent verified size/hash pairs for all nine captured guest files; root's independent file verification is limited to those two named files. The report records `go_modules`, `adapter_dependencies`, and `node_adapter` PASS; Pi `0.87.1`, `provider_stream_attempts=0`; `go_tests` and `go_race` counters of 62 passed, 0 failed, 0 skipped; an overall exit 1 and `unexpected_skips`/failed-packages condition naming expected `TestOpenRejectsUntrustedOwnership`. The summary omitted `package_results`; do not treat these counters as a successful full suite or infer the underlying Go error from the unexpected-skip entry. Captured stdout/stderr were only in memory and scratch cleanup deleted them; saved `verification.stderr` and `sudo.stderr` are empty. The exact Go error is unknown. The report records ownership audit package-fail `0`, run/pass `0/0`, noninteractive sudo true, cleanup and global cleanup PASS, offline carrier `0` and no default routes with host-disconnect attestation true, and all four claims false. The reported source comparison matched six of seven files; the captured setup installer came from an earlier source base than the corrected host installer, so this is not a full-tree match.
6. Preserved evidence is in `F:\TBoundAssets\Evidence\offline-20261003-131833`. Root independently verified `verification.json` SHA-256 `5c5fa36fea3d91c36f70c32100f4f1cfaca2f86fe6ec1169a452ddbb8baa8350` and `capture-manifest` SHA-256 `48170022e98ed0c15216074e8b8f276b3f83389f81e7273c555c016062397878`. An agent verified size/hash pairs for all nine captured guest files; root's independent file verification is limited to those two named files. The report records `go_modules`, `adapter_dependencies`, and `node_adapter` PASS; Pi `0.87.1`, `provider_stream_attempts=0`; `go_tests` and `go_race` counters of 62 passed, 0 failed, 0 skipped; an overall exit 1 and `unexpected_skips`/failed-packages condition naming expected `TestOpenRejectsUntrustedOwnership`. The summary omitted `package_results`; do not treat these counters as a successful full suite or infer the underlying Go error from the unexpected-skip entry. Captured stdout/stderr were only in memory and scratch cleanup deleted them; saved `verification.stderr` and `sudo.stderr` are empty. The exact Go error is unknown. The report records ownership audit package-fail `0`, run/pass `0/0`, noninteractive sudo true, cleanup and global cleanup PASS, offline carrier `0` and no default routes with host-disconnect attestation true, and all four claims false. The reported source comparison matched six of seven files; the captured setup installer came from an earlier source base than the corrected host installer, so this is not a full-tree match.
7. An intervening Oct 3 launcher attempt in `~/.tbound-offline-queued.KVSVFHQErF` ended with `SUDO_AUTHORIZATION_MISSING` and exit `1` before the verifier started; it did not reach the offline gate or run tests. A local expected-hash transcription omitted the first character, but the corrected full guest verifier hash matched; no verifier change was made for that comparison.
8. Later queued run `~/.tbound-offline-queued.LPvHct5VqZ`: the offline gate passed at `2026-10-03T19:25:35Z`; the terminal reported completion at `19:26:17Z` with exit `1`. The captured report records `go_tests`, `go_race`, and `privileged_ownership_test` failing at compile time with `internal/audit/audit.go:248:5: declared and not used: state`. The ownership test itself did not run; do not weaken its expected-test gate. Root independently verified `verification.json` SHA-256 `f536204b261f7ca4f099082fd7758337906f9fdc12b12b834cb0511136b75761` and read its failure details. The capture agent verified `capturemanifest.json` SHA-256 `68b6502667ef37e2ab101793d14a1f3ca2674fdd95af963890070d4b2de36d01` and matched all nine captured source/local file size and hash pairs (123,503 bytes total) under `F:\TBoundAssets\Evidence\offline-20261003-192535`. The JSON records status `FAIL`, all four claims false, and `sudo_noninteractive=true` but ownership audit run/pass `0/0`; the test functions did not run because compilation failed. Saved `verification.stderr` and `sudo.stderr` are empty; the compiler error is recorded in the report. A metadata-only host inventory recorded 29,043,137,045 tracked bytes across 60 files and 869,740,834,816 free bytes on F:; both the 100-GiB tracked-file and 700-GiB free-space limits passed at that measurement.
9. Latest queued run `~/.tbound-offline-queued.vlH6WMLbws`: the offline gate passed at `2026-10-03T19:59:47Z`; the verifier completed at `20:00:34Z` with runner and launcher exit `1`. `go_tests` and `go_race` failed in `TestOpenRejectsBroadPermissionsAndLeafSymlink` (`audit_test.go:431`): expected `ErrJournalSymlink`, got `open audit journal: open audit path directory "parent-link": not a directory`. The privileged ownership phase passed, run/pass `1/1`. Overall status is `FAIL`; all four claims are false. Root verified the report hash and failure details; the capture agent verified the manifest and all nine guest-file size/hash pairs. Preserved evidence is `F:\TBoundAssets\Evidence\offline-20261003-195947`: `verification.json` SHA-256 `095af79e04c4b1587e890fd66c2b4f2d5fdf0440f33cc59d9272fcc6b05e5890`, `capturemanifest.json` SHA-256 `29fc15b13a4f542578e09cb9bfcbd53b0f8d66ab73a79204e46b01d189287281`, and nine regular evidence files totaling 124,538 bytes. The earlier `LPvHct5VqZ` compile failure is a separate historical run. Post-capture metadata-only inventory measured 4,081,694,378 bytes across 62 `TBoundAssets` files and 25,028,681,596 bytes across 8 `TBoundVMs` files, total 29,110,375,974 bytes; there were no reparse points, and F: had 869,672,910,848 bytes free. Both the 100-GiB tracked-data and 700-GiB free-space limits passed.

Before the Oct 3 dependency apply, the guest verifier SHA-256 was `9665d4004585515bfd90e7774cb4a8cbf54b972178bea4f580e4bd20f7d46d18` and guest `supervisor/go.sum` SHA-256 was `7913bff96ace35f736cabee75fb7b469f97976bb09804ad91a4794f705fe8c21`. The prior queue launcher was recorded as SHA-256 `12bc7d88bcbff75e9848b42ee46401c9014a594d73ded62fc623573650f41497`; its current hash, mode, and owner must be rechecked before reuse. Preserve all failure reports and backups; never overwrite/retry an unknown report directory.

### Latest guest application and pane state - 2026-10-03

- Host commit `711e6416424766e8aa7e0d7ff8beac4dcb171002` committed the prepared `supervisor/go.sum` and README; it did not change `go.mod` or application source.
- An atomic guest apply guarded by exact old-file hashes installed `supervisor/go.sum` SHA-256 `2ff7956b129800bfed3b5f9ca671ece52317fc4fa4786c76f4672abb075f1aea` and `infra/guest/verify-offline.py` SHA-256 `bbcefad25a5184bb87b2efaba3b73b2480be609f6daff30a376aed9d184b2272`; both are mode `0600`, UID `1000`. Guest `go.mod` remained byte-identical, SHA-256 `72a77c58ae54b3c6e4a76efe96f0ad4313c03693809d85e2d3304ca3adad0713`.
- The private rollback directory `/home/tboundadmin/.tbound-maintenance-backup-2Y2Yh0` is UID `1000`, mode `0700`, and retains the old sum and verifier hashes above. The apply log SHA-256 is `60799d933f6cfa77baf306f93d7f29d4e823655434d7408941c332d285e4e097`.
- `tbound-offline` window 0, pane 0, terminal `pts/1`, Bash PID `1949`, attached client tty `1`, and `FIONREAD=0` were observed before the latest queue. At that point, no Go, npm, Node, launcher, or verifier process was active.
- Metadata-only `go list -m all` and `go mod verify` passed while the NIC was connected; they are not an offline verifier pass. After the `LPvHct5VqZ` run, the user confirmed reconnection to `Default Switch` for trusted maintenance, and no active guest test job remained at that observation. The guest application remains the earlier `9a73` tree with the separately recorded dependency-sum update, diagnostics verifier, and audit source patch applied; the new OpenRouter and workspace packages have not been deployed.
- Host commit `833915a` retains bounded Go-phase failure diagnostics. The diagnostic verifier was applied to the guest as SHA-256 `a0cabda405c58e5bcfe8453eaa0e892579dbe51ee81d0f9d8add45d4d5476de5`. Host-only Python and Go module-helper tests passed 5/5 each, with AST/compile checks; no Go build or tests for the new broker/workspace packages or provider requests occurred.
- Host commit `7c91b47cf54c71cfd6e24b9107329bc2ba2095a6` fixed the unused `state` local in `supervisor/internal/audit/audit.go`. The guest file matched SHA-256 `3a6f97639aa9b2d38f21de8f274867c7c3f97dc430624dbfc88fdce22e761c82`, UID `1000`, mode `0600`, link count `1`. Its prior version (SHA-256 prefix `0362265`) is backed up at `/home/tboundadmin/.tbound-audit-fix-tp2e7cqj/audit.go.before`; `patch-lineage.json` SHA-256 is `9258a3ebbe54ed9707c6d2e40a24331862d0347044b6fcabfe293d9738e7cf1e`. The later `vlH6WMLbws` run compiled and exposed the symlink-classification test failure recorded above. No claim is made that a follow-on symlink fix has been deployed.

Targeted checks already reported: APT trust-guard regression suite (7 tests) passed; one held-package parser regression passed; guest Python AST validation passed; queued-launcher Bash syntax checks passed; and the host Go-phase diagnostic helper tests passed 5/5 with Python tests 5/5. These are component checks. The Oct 3 afternoon Go report has incomplete test summaries; the 19:25 run failed compilation, and the 19:59 run compiled but failed the audit symlink-classification assertion. No successful full Go/Pi suite is independently verified. No provider call, real E05 exchange, live effect execution, containment proof, G1 durability/destruction proof, or end-to-end TBound demonstration is established.

## Immediate next steps recorded before the latest diagnostic run

1. The latest preserved run is `vlH6WMLbws` at `F:\TBoundAssets\Evidence\offline-20261003-195947`; its report/manifest pins and nine-file capture verification are recorded above. The 19:25 compile failure is a separate historical run.
2. The latest run verified that the `7c91b47` audit.go compile fix reached Go tests, where normal and race phases failed at the symlink-classification assertion. At that point, host commit `1f03c76` contained the reviewed follow-on fix but had not been deployed or tested. Do not relax the expected-test gate, queue automatically, or restore.
3. The user approved transfer of the selected source bundle at `05245378a3df82fbf2cb413aa1ac59085dfc0bf6`; the queue agent reports eight selected files applied and hash-verified under the guest handoff tree, not the full HEAD. Archive SHA-256 is `afc1610495ec34c754460351cd1e73e1fb8f351913d5e1762eed4feb1a2fb1f1`; deployment-manifest SHA-256 is `a792425bcc8916b62bc42b46a6403f8450d7a441170e50be737806f7e1fcf1ef`. The five preimage backups are preserved. Per-file hashes and paths are in `docs/implementation-status.md`. The static root-owned fixture bootstrap is still being prepared; it has not been installed or run, and no post-transfer fixture test or offline verification exists. The next verification must wait for the reviewed bootstrap and use the updated launcher as `tboundadmin` without per-run `sudo`.
4. The post-capture F: inventory passed the 100-GiB tracked-data and 700-GiB free-space limits; older host inventory values remain historical. No successful full Go/Pi suite, provider call, real E05 exchange, containment proof, G1 durability/destruction proof, or end-to-end demonstration is established.

## Current status — diagnostic run and follow-up

The latest queued run `q0swUUj1Kl` passed the offline gate at `2026-10-03T21:41:28Z` and completed at `21:42:09Z` with runner/launcher exit 1. Overall verifier status is FAIL solely on `failed_packages` in both modes; all four claims are false.

Both normal and race Go phases report 5/5 packages, 86 test-run events, 85 passes, 0 failures, and one expected test-level skip for `TestOpenRejectsUntrustedOwnership`. The correlation package has a separate package-level skip with the exact `[no test files]` marker; the current parser flags that marker as failure.

The dedicated `ownership_fixture_test` passed without per-run sudo: UID/GID 1000, one named test/run/pass, zero failures or skips. A static root-owned fixture bootstrap was installed once; no persistent NOPASSWD sudoers entry was added.

The report records UID 1000, offline `eth0` carrier 0, no IPv4/IPv6 default route, cleanup PASS, and all claims false. Saved `verification.stderr` is empty. Root verified report SHA-256 `1fd2bec7cfb4d3cabf0031f163c6e323418914db5190d77b962e5469a45da561` (123,858 bytes), manifest SHA-256 `58bc33702a40464aa7ddc424e96add1941c327ade19e0209df3344562b8d9934`, and the capture confirmation matching all eight captured guest files at `F:\TBoundAssets\Evidence\offline-20261003-214128`.

The host parser correction was not deployed to or tested in this guest run. Passing targeted Go and fixture phases do not establish a successful overall verifier, provider exchange, end-to-end demonstration, or containment.

After this run, source commit `90ee23e` corrected the package-skip parser and passed seven host-only pure tests. A guarded two-file guest deployment was verified as UID 1000 without sudo: `infra/guest/verify-offline.py` SHA-256 `cb4d8d2a235548b2a3a1528befb880e0dc5f1619d5ac46591672ed17547b204a` and `infra/guest/test_go_phase_diagnostic.py` SHA-256 `61a56ea541e5ed5648b02b79643a2b6a667144a99b2129b77ea7622dc1e6099f`. The preimages are preserved under `/home/tboundadmin/.tbound-verifier-backup-90ee23e34b60f4e2`; deployment manifest SHA-256 is `07c791f66507a3125ca73ed96696c50853c5509422f07f32aa8e870693e90af4`. The VFH report and capture were later retrieved and verified; see the current state above.

The queued follow-up VFHmYRa5HO passed its offline gate and reached terminal completion. Its report and capture were later retrieved and hash-verified. The report is PASS for its recorded inputs, but all four claims are false; this is not full checklist completion or containment evidence. See the current state above for capture hashes and scope.

Host automation source remains independently security-reviewed. The retry reached the installer’s firmware gate but rejected the valid Ubuntu entry because BootType is the native enum Microsoft.HyperV.PowerShell.VMBootSourceType, while the guard required a string. The guard now accepts only string File or the exact native enum value File, along with the pinned VM identity, null Device, empty checkpoint IDs/names, IsDeleted false, and the structural GPT path to \EFI\ubuntu\shimx64.efi. The installer captures that exact FirmwarePath in protected profile schema 2; runtime requires the same first-entry path. Windows PowerShell 5.1 parsing passed for four scripts and 45 pure fixtures passed using the real Hyper-V enum metadata; canonical LF action SHA-256 and installer pin are 9EF7B338B18DA61204F84E3B74FD1ADA609652E4FC7A25DEB5CF633605581D26. The enum correction is prepared but not installed or live-tested; the failed attempt stopped before persistent changes. The later user decision deferred host automation, then the user reauthorized the narrow trusted-verifier helper on Oct 4; see the current handoff above. No helper install has been run.

A focused firmware guard fix now accepts only the target VM’s File entry with null Device, matching VM identity, empty checkpoint IDs/names, IsDeleted false, and a local GPT path to \EFI\ubuntu\shimx64.efi. The installer captures that validated FirmwarePath in protected profile schema 2; runtime requires an exact match to the pinned path at boot-order position one. The partition GUID and geometry are validated structurally and captured per installation. Secure Boot’s Linux template and the single attached disk remain separate existing checks. Windows PowerShell 5.1 parsed the three source scripts and the pure fixture harness passed 36 accept/reject cases; canonical LF action SHA-256 and installer pin are 3740571565DBDA14A3517D9A891FAA98E9DFEF80D22A9BBC67F9389575CDA716.

These source fixes were prepared but not installed or live-tested at that point. This work did not inspect or alter the live VM, firmware, Hyper-V settings, tasks, or SSH state. The later Oct 4 user reauthorization for a narrow helper supersedes that historical deferral; see the current handoff above. The VFH report and capture were retrieved through pinned trusted maintenance and hash-verified; the report scope and limits are recorded above.

## Resume VMConnect and the existing tmux terminal

Run the following in **elevated Windows PowerShell** to inspect the live VM, adapter, and F: space:

~~~powershell
$vmName = 'TBound-Ubuntu-2404'
Get-VM -Name $vmName | Select-Object Name,State,CheckpointType,AutomaticCheckpointsEnabled
Get-VMNetworkAdapter -VMName $vmName | Select-Object Name,SwitchName
Get-PSDrive -Name F | Select-Object Name,Free
~~~

If the VM is already Running, open its Basic Session console:

~~~powershell
vmconnect.exe localhost 'TBound-Ubuntu-2404'
~~~

If it is Off, do not start it until the full Hyper-V Test-UbuntuHyperVVm.ps1 -Mode Verify -BasicSessionConfirmed gate passes with the existing VM/ISO arguments, the checkpoint/disk chain is inspected, and no offline run or unknown job is active. Then this guarded command starts it only if it is still Off, its single NIC remains disconnected, and the F: reserve is intact; it also opens VMConnect:

~~~powershell
& { $ErrorActionPreference='Stop'; $n='TBound-Ubuntu-2404'; $v=Get-VM -Name $n; if ([string]$v.State -ne 'Off') { throw 'VM must still be Off after Verify.' }; if ((Get-PSDrive -Name F).Free -lt 700GB) { throw 'F: free space is below 700 GiB.' }; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName) { throw 'Expected exactly one disconnected adapter.' }; Start-VM -Name $n; vmconnect.exe localhost $n }
~~~

Once the console is open, sign in as `tboundadmin` and inspect the terminal/session:

~~~bash
if [ -n "$TMUX" ]; then
  tmux display-message -p -t "$TMUX_PANE" 'session=#{session_name} pane=#{pane_id} tty=#{pane_tty} command=#{pane_current_command}'
else
  tmux list-sessions
fi
~~~

If this shows the existing `tbound-offline` session and you are already inside it, continue at that prompt; do **not** attach again. If you are outside tmux, list sessions first and attach only if the named session exists:

~~~bash
tmux list-sessions
tmux attach -t tbound-offline
~~~

If the session does not exist, first inspect the launcher/report directories and process list to confirm there is no active or unknown run. Create a session only after that inspection. The prior terminal was an attached pane `%0`, `/dev/pts/1`, shell PID 1949, but those values are historical and may have changed.

For trusted SSH maintenance, verify the current guest IP and pinned host key first. Use the protected private key only with the existing restricted SSH configuration; never output its contents. If SSH is unavailable while disconnected, use VMConnect. An SSH timeout after a deliberate NIC disconnect is expected.

### Queue procedure for a later run

The Oct 3 queued runs are complete; do not reissue or retry them. The eight-file source overlay is applied but untested. Do not queue another offline run until the static root-owned fixture bootstrap is prepared, reviewed, and installed once during connected trusted maintenance. The human refreshes `sudo -v` privately in the attached pane for that one-time bootstrap only; the updated launcher and verifier run afterward as `tboundadmin` without per-run `sudo`.

For the later SSH path, run the updated launcher while the guest is connected in trusted maintenance mode. Before disconnecting the host adapter, confirm the terminal prints `queued; waiting...`, get the exact report-directory path for that invocation, and verify its `status` file contains `WAITING_FOR_OFFLINE`. Do not select a report directory by wildcard or queue a second launcher. Keep the adapter disconnected through verifier completion, then reconnect only for trusted evidence collection after confirming the launcher and verifier have exited.

Before disconnecting, elevated PowerShell must confirm the VM is Running, F: has at least 700 GiB free, and exactly one adapter is connected to `Default Switch`. The guarded disconnect form used previously is:

~~~powershell
& { $ErrorActionPreference='Stop'; $n='TBound-Ubuntu-2404'; if ((Get-PSDrive -Name F).Free -lt 700GB) { throw 'F: free space is below 700 GiB.' }; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName -ne 'Default Switch') { throw 'Unexpected adapter configuration.' }; Disconnect-VMNetworkAdapter -VMName $n -Name $a[0].Name; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName) { throw 'Network disconnect verification failed.' }; $a | Format-List Name,SwitchName }
~~~

Do not use that disconnect command until a new run is actually queued and waiting. Reconnect only after the guest pane shows terminal completion and the launcher has returned; verify no runner/launcher remains. Then collect the report and stderr over pinned-key SSH for trusted maintenance. If inspection shows the VM is Running and the adapter is disconnected, this guarded one-line command reconnects it for trusted maintenance:

~~~powershell
& { $ErrorActionPreference='Stop'; $n='TBound-Ubuntu-2404'; if ([string](Get-VM -Name $n).State -ne 'Running') { throw 'VM must be running.' }; if ((Get-PSDrive -Name F).Free -lt 700GB) { throw 'F: free space is below 700 GiB.' }; $a=@(Get-VMNetworkAdapter -VMName $n); if ($a.Count -ne 1 -or $a[0].SwitchName) { throw 'Expected exactly one disconnected adapter.' }; Connect-VMNetworkAdapter -VMName $n -Name $a[0].Name -SwitchName 'Default Switch'; Get-VMNetworkAdapter -VMName $n | Format-List Name,SwitchName }
~~~

If the adapter is already connected, do not run that command. After maintenance, shut down cleanly, verify the adapter disconnected and the VM Off, and run the complete host Verify gate before any subsequent offline trial.

## Important limitations at handoff

- The VM is provisioned and has passed prior host configuration checks, but its checkpoint restore is untested. User-provided Oct 3 inventory reports it Running on `Default Switch`; root has not independently checked the host or full disk chain.
- The latest offline-gated run reached Go tests but failed the audit symlink-classification assertion; no full Go/Pi suite has passed. The privileged ownership phase passed 1/1 in that run, which does not establish containment or an overall verifier pass.
- Provider-only network enforcement, provider request capture, a real model exchange, live effect execution, verified Pi closure, full containment, durable-effect fault injection, G1 fixtures, and the narrow end-to-end todo demonstration remain unfinished.
- A regular-filesystem policy has been agreed but not implemented in the generation store.
- Linux-host migration instructions exist only as a stated future task. Fresh physical Linux setup is untested.
- Keep claims aligned with these limits.
