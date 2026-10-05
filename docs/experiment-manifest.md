# Candidate experiment manifest — not G1

> **candidate — not G1; makes no containment/provider claim**

This document is a candidate, documentary record only. It is **not** a G1
manifest, it freezes **nothing**, and it establishes **no** provider, effect,
containment, or durability result. Every value below is transcribed from an
existing source; the source is named next to each value. Where the prototype
does not yet have a required frozen identity, the value is recorded as a gap in
[§7](#7-must-freeze-before-g1-gap-list) rather than implied here.

- Repository branch: `codex/tbound-prototype` (`TASK_STATE.md` "Current verified state" / git).
- This file is the only artifact produced by this task; it supersedes nothing and edits nothing.

---

## 1. Host / VM

| Value | Source |
|---|---|
| Hyper-V guest `TBound-Ubuntu-2404`, Generation 2 | `TASK_STATE.md` "Decisions and constraints" (VM definition) |
| 8 vCPU, 16 GiB RAM, 40 GiB dynamically expanding VHDX | `TASK_STATE.md` "Decisions and constraints" (`16 GiB fixed RAM`, `40 GiB dynamically expanding VHDX`) |
| Ubuntu Server 24.04.5 LTS | `TASK_STATE.md` "VM and guest baseline" |
| Kernel `6.8.0-146-generic`, amd64 | `TASK_STATE.md` "VM and guest baseline"; guest-validation section |
| Root filesystem ext4 on LVM | `TASK_STATE.md` "VM and guest baseline" |
| Account `tboundadmin`, UID 1000 | `TASK_STATE.md` "VM and guest baseline" |
| VM lifecycle task set `\TBoundVmOps\{Status,Start,Stop,Connect,Disconnect}` runnable **unelevated** | `TASK_STATE.md` "Workstreams"/autonomy verification sections; `TASK_STATE.md` "Progress — this session" (fixed-task helper) |
| Network at rest: NIC on `Default Switch` for trusted maintenance only; disconnect + offline gate required before any untrusted execution | `TASK_STATE.md` "Decisions and constraints" |

## 2. Toolchain

| Value | Source |
|---|---|
| Guest Go `go1.27.1` | `TASK_STATE.md` "VM and guest baseline" (also earlier toolline note under `/opt/tbound`) |
| Guest Node `v24.21.0` | `TASK_STATE.md` "VM and guest baseline" |
| Host Node `v24.15.0` | `TASK_STATE.md` "Autonomous continuation plan" (supersedes earlier "Node unavailable"); `adapter/artifacts/pi-closure-report.json` `runtime.node` |
| Rootless Podman `4.9.3` | `TASK_STATE.md` "VM and guest baseline"; `podman_linux.go` `runCell` comment ("podman 4.9.3") |
| crun `1.14.1` | `TASK_STATE.md` "VM and guest baseline" |
| cgroup v2 mounted, **not delegated** to `tboundadmin` | `TASK_STATE.md` guest-validation section; `internal/sandbox/cgroup_linux.go` `ErrCgroupUnsupported` |
| GCC `13.3` | `TASK_STATE.md` "VM and guest baseline" |
| Guest subordinate UID/GID ranges of 65,536 entries | `TASK_STATE.md` "VM and guest baseline" |

## 3. Isolation facts observed

| Value | Source |
|---|---|
| Guest Landlock ABI **4** | `TASK_STATE.md` guest-validation section (kernel `6.8.0-146-generic`) |
| WSL2 (`5.15`) Landlock ABI **1** | `TASK_STATE.md` contained-executor section ("Landlock is ABI 1 here") |
| pidfd present; seccomp-bpf present | `TASK_STATE.md` guest-validation section |
| Ubuntu `kernel.apparmor_restrict_unprivileged_userns=1` blocks raw `clone(CLONE_NEWUSER)` (`unshare --user --map-root-user` also `EPERM`) → the **in-process Option-A cell cannot start on the guest** | `TASK_STATE.md` guest-validation section; `internal/sandbox/doc.go` (dev/WSL, non-claim-bearing profile) |
| Distro AppArmor profiles for `podman`/`crun` (`/etc/apparmor.d/{podman,crun,unprivileged_userns}`) let **rootless Podman/crun create a userns and run** (`podman info` → `rootless=true cgroup=v2 oci=crun`; `podman run --rm docker.io/library/alpine` exited 0) | `TASK_STATE.md` guest-validation section |
| Raw `unshare --user` stays denied on the guest; the claim-bearing path is rootless Podman/crun, no host-config change | `TASK_STATE.md` guest-validation section |
| Podman runner `Profile = "guest-podman-rootless-non-claim-bearing"`; sandbox `EvidenceClass` is `dev-wsl-non-claim-bearing:…`; neither reports containment | `supervisor/internal/podman/podman_linux.go` (`Profile`, `EvidenceClass`); `supervisor/internal/sandbox/sandbox_linux.go` (`Report.EvidenceClass`), `doc.go` |

## 4. Frozen pins currently in code

| Value | Source |
|---|---|
| `docker.io/library/alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6` | `supervisor/internal/podman/podman_linux.go` const `ExpectedImageDigest` |
| Probe/Run fail closed unless a **local** image `RepoDigests` entry equals that digest; `--pull=never`; a merely-present or mismatched tag is rejected | `podman_linux.go` `Probe`, `selectImage`, `cellArguments` |
| `--network=none`, `--read-only`, `--cap-drop=ALL`, `--security-opt no-new-privileges`, pids/memory limits, `--userns=keep-id` cell argument vector | `podman_linux.go` `cellArguments` |
| Embedded `entrypointScript` checks uid/gid, empty `CapEff`, `NoNewPrivs=1`, `Seccomp=2`, `cgroup 0::*`, no external route, then `exec "$@"` | `podman_linux.go` const `entrypointScript` |
| The entrypoint is explicitly marked **DEFERRED**: not a frozen, signed in-image entrypoint and not byte-identity-checked | `podman_linux.go` `entrypointScript` comment |

## 5. Pi closure / adapter surface

| Value | Source |
|---|---|
| Adapter exposes **exactly** `read`, `write`, `edit`, `bash` | `adapter/src/proxy-tools.ts` `declaredToolNames`; `adapter/artifacts/pi-closure-report.json` `active_tool_names` and `tools[]` |
| `provider_stream_attempts = 0` | `adapter/artifacts/pi-closure-report.json` `provider_stream_attempts`; `TASK_STATE.md` adapter IPC-client section |
| Pin: `@earendil-works/pi-coding-agent` `0.87.1` (installed = locked) | `adapter/artifacts/pi-closure-report.json` `pi_package` |
| Native `powershell`/`grep`/`find`/`ls` rejected; resource additions/reload throw | `adapter/artifacts/pi-closure-report.json` `unavailable_native_tool_activation`, `negative_checks[]` |
| `surface_closed` is **not established**; broker manifest enforcement is still required | `adapter/artifacts/pi-closure-report.json` `surface_closed` |

## 6. Claim status

| Claim | Status | Source |
|---|---|---|
| Live provider exchange | **false** | `docs/implementation-status.md` "Current guest run and source scope"; `TASK_STATE.md` "Current verified state" |
| Live effect execution | **false** | same |
| Containment established | **false / not-established** | same; `podman_linux.go` `Profile`; `internal/sandbox/doc.go` |
| G1 durability established | **false** | same |
| Command containment | `not-established` (non-claim-bearing profiles only) | `podman_linux.go` (`Profile`, `EvidenceClass`); `internal/sandbox/sandbox_linux.go` (`Report.EvidenceClass`), `doc.go` |

No value in this document moves any claim above `not-established`.

## 7. "Must freeze before G1" gap list

These identities/artifacts are **not yet present or frozen** in the prototype.
Each must be frozen (and recorded per run) before any containment claim; the
cited requirement is the source that makes the gap a prerequisite.

1. **Signed in-image entrypoint.** The current entrypoint is an embedded shell
   string marked DEFERRED. — `podman_linux.go` `entrypointScript`; `07-1-runtime-flow.md` §6 ("signed entrypoint").
2. **Offline cosign + signer authorization.** Digest pinning alone authenticates
   identity but not signer; Cosign public-key verification is required. —
   `06-4-sandboxing-decision.md` (Docker/Sandbox trade-off paragraph);
   `podman_linux.go` `Probe`/`ExpectedImageDigest` comments defer cosign.
3. **Static seccomp profile digest.** A pinned allowlist exists, but the exact
   static launcher-plus-target profile digest is not recorded. —
   `06-4-sandboxing-decision.md` "Frozen implementation profile";
   `07-1-runtime-flow.md` §6.
4. **Delegated non-threaded cgroup-v2 subtree with writable `cgroup.kill`, plus
   pidfd teardown.** cgroup v2 is mounted but not delegated, so resource bounds
   and kill-confirmed teardown are unestablished. — `TASK_STATE.md`
   guest-validation section; `internal/sandbox/cgroup_linux.go`
   `ErrCgroupUnsupported`; `06-4-sandboxing-decision.md` "Frozen implementation
   profile"; `07-1-runtime-flow.md` §1.
5. **Selected single entrypoint language (Go or minimal Rust, never both).** —
   `todo-implementation-tbound.md` spike 4 ("Time-box the Go entrypoint spike;
   G1 selects one conforming entrypoint (Go or static minimal Rust), never both").
6. **Whole-image manifest digest.** The current pin is a per-platform repo
   digest matched from `RepoDigests`; the manifest-list/index digest covering the
   whole image is not yet recorded. — `podman_linux.go` `ExpectedImageDigest`,
   `selectImage`; `06-4-sandboxing-decision.md` "Frozen implementation profile".
7. **D06 "new private Git repository" item.** Spec §6/D06 call for a newly
   initialized private repository; the prototype has none — a waiver or an
   implementation is still owed. — `TASK_STATE.md` "Open decisions (blocking
   full G1)"; `AH-technical/10-decision-register.md` D06.
8. **Per-run experiment record of these pins.** The frozen identities must be
   captured and compared per run, failing closed on drift. —
   `todo-implementation-tbound.md` "Prepare for WP1" (freeze in the experiment
   manifest); `07-1-runtime-flow.md` §1 preflight and §9 checkpoint.

Not listed above, but also outstanding for a claim-bearing path: the real
provider exchange (privately supplied model ID + API key) and the checkpoint
restore demonstration (needs a Hyper-V checkpoint task outside `\TBoundVmOps`).
— `TASK_STATE.md` "Open decisions (blocking full G1)".

## 8. Reconciliation note

`docs/implementation-status.md` still contains pre-implementation statements
that are now contradicted by green tests and later verified runs. Examples:

- It states the OpenRouter broker slice is "uncompiled, unformatted, and
  untested" with "no HTTP request, Pi wiring, or live provider support"
  (`implementation-status.md` "Current broker slice and provider plan" and the
  exit-criteria table), and that the workspace package has "No Go compile,
  gofmt, or tests" (`implementation-status.md` "Current broker slice and
  provider plan").
- `TASK_STATE.md` records that these packages now compile and that the full
  Linux `-race` suite is green (`TASK_STATE.md` "Progress — session 2026-10-05":
  "WSL `go test -race -count=1 ./...` all packages green", "Windows `go build
  ./...` green", "gofmt -l supervisor empty"), and the adapter closure probe is
  green with exactly four tools and `provider_stream_attempts=0`.
- The same `implementation-status.md` also still carries the stale
  `37405715…` host-automation pin paragraph that `TASK_STATE.md` marks
  superseded.

This is recorded here **for later documentation reconciliation only**. No
existing file is edited by this manifest; `docs/implementation-status.md` should
be reconciled in a separate, reviewed change.

---

### Provenance

- Primary prototype sources: `supervisor/internal/podman/podman_linux.go`,
  `supervisor/internal/sandbox/` (`doc.go`, `sandbox_linux.go`,
  `cgroup_linux.go`, `landlock_linux.go`), `adapter/src/proxy-tools.ts`,
  `adapter/src/closure-probe.ts`, `adapter/artifacts/pi-closure-report.json`.
- Status/reconciliation sources: `TASK_STATE.md`,
  `docs/implementation-status.md`.
- Normative/requirement sources:
  `MSE_MA_Thesis/agentic-harness/todo-implementation-tbound.md`,
  `AH-technical/06-4-sandboxing-decision.md`,
  `AH-technical/07-1-runtime-flow.md`,
  `AH-technical/10-decision-register.md`.
