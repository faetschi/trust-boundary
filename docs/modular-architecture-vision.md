# tbound modular architecture: vision and proposal

> ### Scope and claim status
> **The whole document is VISION.** Nothing here is implemented unless marked **EXISTS**, and every EXISTS item cites its source. This document adds no authority to any component, changes no code, and does not alter `TASK_STATE.md`, the roadmap, or any thesis document.
>
> | Scope | Items | Label |
> |---|---|---|
> | In scope today | `tbound serve --pi` client over the local authenticated Unix socket (`SO_PEERCRED` allowlist), EXISTS as code only; the managed-service host setup is not shipped or verified (§5.2); `tbound-doctor`; `tbound-profile`; the optional read-only, loopback webchat (fixtures and receipts; `--live` refused) | EXISTS (§5.2, §11) |
> | In scope, not yet agreed | Read-only projection over the same socket; interactive turns and cancel through the existing authenticated supervisor authority, once the core thread agrees | PROPOSED (§8 Phases 1–2) |
> | **Post-thesis future work** | `serve --headless` and any daemon or service-manager mode; remote operation beyond an SSH shell on the host; TCP, TLS, token or mTLS control planes; plugins or extensions beyond the existing optional webchat | **FUTURE-WORK** (§8 Phase F) |
>
> **FUTURE-WORK items are not part of the thesis claim, are not evidence-bearing, and must not be presented as qualified.** Claim boundary, unchanged (`TASK_STATE.md`, "Claim boundary"): G1=false; containment not-established; H1 unexecuted; no qualified successful public production launch; receipts carry `claim_bearing=false`. A passing test, a web UI, a plugin boundary, a remote-access demonstrator, or a headless run is not claim-bearing evidence (`extensions/webchat/docs/web-chat-contract.md` §5).
>
> A future headless mode may claim only what it shows after a design preserves the surviving-owner invariant (thesis `02-1` §4.1) and passes the thesis gates (thesis `00-roadmap` §5, G0–G6) in its own registered configuration, with thesis-owner sign-off.

Written 2026-10-14 on `codex/tbound-web-chat`; revised 2026-10-14 to record decisions on the open caveats (§10). The web-chat code and docs moved from `supervisor/` to `extensions/webchat/` in `18f71e6`. Paths below use the post-move location; the pre-move `supervisor/` location is given where it differs. Phase numbers in §5, §7 and §8 are this document's own. The web-chat frontend phases (P1 to P3) are in ADR-0001 §(v).

**Current state (2026-10-14, HEAD `57e6406`).** The web chat is an optional extension (`extensions/webchat/`, module `tbound/webchat`). The core neither requires nor imports it, and a boundary guard checks that. Its Go gateway is read-only and loopback-only. Its browser client is React and TypeScript under `/app/`, and `/` redirects there, so the React client is the default UI. The legacy zero-build client is deprecated at `/legacy/` until its owner signs off its removal. Live projection, turn submission and browser authentication are not built (§5.2, §7).

| Label | Meaning |
|---|---|
| **EXISTS** | Verified in the current tree; file reference given |
| **PROPOSED** | Design only; needs owner agreement, then implementation |
| **DEFERRED** | Recognised; intentionally not designed in detail yet; not scheduled |
| **FUTURE-WORK** | Post-thesis. Outside the thesis claim; not evidence-bearing; enters the plan only through the §8 Phase F entry criteria |
| **OUT** | Explicitly out of scope; must not be built |

Decision statuses (§10 only): **DECIDED-for-now**, **DEFERRED-future-work** (the same thing as FUTURE-WORK), **NEEDS-CORE-THREAD**.

Shorthand: **dsh** = DeepSeek harness shallow clone at `C:\Users\Admin\AppData\Local\Temp\dsh-research` (read-only, untrusted, never executed). **Thesis** = `MSE_MA_Thesis/agentic-harness/AH-technical/`, the sibling of the main `trust-boundary` checkout (`../MSE_MA_Thesis/...` does not resolve from this worktree). Thesis files are cited by prefix, for example `02-1` = `02-1-recovery-and-continuation.md`.

## 1. Purpose and thesis fit

Three statements from the product owner:

| # | Statement | Status |
|---|---|---|
| V1 | `tbound/webchat` is a separate module/extension, usable with tbound; tbound runs without it. | EXISTS in part: separate module `tbound/webchat` (`extensions/webchat/go.mod`, from `18f71e6`), with the boundary guard (§6.3). The core neither requires nor imports it. |
| V2 | tbound is usable CLI-only, with no web service, and can run headless on a Linux host, including over SSH. | Split. CLI-only local client EXISTS (§5.2). Headless and SSH-daemon operation is **FUTURE-WORK** (§8 Phase F). |
| V3 | "Everything is a plugin", under trust tiers: a small trusted core plus plugins that cannot widen authority. | PROPOSED for the tier rules (§3). Plugins beyond the existing optional webchat are **FUTURE-WORK**. |

Thesis constraints this design must respect:

| Thesis source | What it says | Consequence here |
|---|---|---|
| Thesis `03-architecture.md` §3 | Remote Linux operation is "not as a new trust boundary in the thesis MVP"; a custom remote control plane "would require separate architecture and evaluation". | Remote mode is product scope, not a thesis claim. §8 Phase F. |
| Thesis `00-roadmap.md` §4 | Non-goals include MCP, polished TUI/browser UI, production deployment, remote multi-user operation, high availability. | Multi-user, HA and internet exposure stay FUTURE-WORK or OUT. |
| Thesis `02-1-recovery-and-continuation.md` §4.1 | The claim-bearing lifecycle owner is the evaluation runner; "direct supervisor and service-manager deployment modes are outside the thesis." | A systemd-managed daemon is an excluded mode and needs a surviving-owner design (Q1). |
| Thesis `06-1-language-decision.md`, lifecycle ownership | "A supervisor cannot guarantee its own post-mortem cleanup"; "direct `tbound` launch and separate service-manager deployment are out of scope." | Same as above. A future headless design needs an owner outside the supervisor. |
| Thesis `06-1` Decision and D03 amendment | The supervisor is in Go; the selection table accepts TypeScript "only for side-effect-free extension glue". "No presentation client can modify policy, bypass containment, write the live workspace, alter evidence, or turn `DENY` into `ALLOW`." | The invariant every Tier 2 client must satisfy (§3). |
| Thesis `06-1` (pin rule) | Admission must bind executable, runtime, dependencies, extension bytes, argv, environment, descriptors and broker/tool manifest to the signed profile. "A CLI version check or executable fingerprint is not a signed dependency closure." | Drives Tier 1 pinning and the rejection of shared-process plugins (§3.6, §4). |

## 2. Architecture sketch

```text
 trusted host boundary (one Linux host; the service user owns the journals)

 +--------------------------------------------------------------------------+
 | TIER 0  trusted supervisor core (Go, supervisor/)            AUTHORITY    |
 |   admission · policy (gate) · leases · effect execution (cells)          |
 |   durable journals (audit, workflow, sessionrepo) · publication · recovery |
 |                                                                          |
 |   TIER 1  compiled-in, pinned providers and adapters                     |
 |     Pi runtime launcher (internal/piruntime) · OpenRouter broker         |
 |     (internal/broker/openrouter) · D09 cell template · HostProfile       |
 +------------------------------------^-------------------------------------+
                                      | authenticated service API
                                      |   today:    Unix socket, SO_PEERCRED UID/GID allowlist
                                      |   PROPOSED: sessions, snapshot+cursor, submit, cancel, handshake
 +------------------------------------+-------------------------------------+
 | TIER 2  out-of-process clients and extensions (no execution, scoped)     |
 |   tbound CLI (operator, CI) · webchat (extensions/webchat, loopback)     |
 |   receipt importer (EXISTS) · FUTURE-WORK: TUI/IDE, exporters, dashboards |
 +--------------------------------------------------------------------------+

 Remote operator (DECIDED-for-now, Q2): SSH shell on the host, then the local CLI.
   Forwarding the webchat HTTP port is acceptable. Forwarding the Unix socket is
   UNVERIFIED (Q6) and not supported. The core opens no TCP listener.
```

Edge rules:

| Edge | May carry | Must never carry |
|---|---|---|
| Tier 2 → API | Handshake; scoped read, snapshot, subscribe; submit and cancel requests if granted | Tool execution; anything that bypasses the gate; journal paths; provider keys |
| API → Tier 2 | Bounded observation projection (`tbound-webchat-observation/v1`, `extensions/webchat/internal/webchat/contract.go` (`ContractVersion`)); settled results; typed errors | Raw journal bytes; provider payloads; credentials; unbounded tool output |
| Core → Tier 1 | Go interface calls fixed at build time | Runtime-loaded code |

## 3. Trust-tiered plugin model

### 3.1 Tiers

| | Tier 0: core | Tier 1: providers and adapters | Tier 2: clients and extensions |
|---|---|---|---|
| Contents | Admission, policy evaluation, effect execution, durable journals, publication authority, recovery (thesis `03` §3 table) | Pi runtime launcher; OpenRouter broker provider; D09 cell template and HostProfile; in-cell Pi extension `adapter/src/tbound-cli-extension.ts`, which runs inside the untrusted cell and has no authority (thesis `06-1`) | `tbound` CLI as a client; webchat; receipt importer (EXISTS in webchat); TUI/IDE, exporters, dashboards (FUTURE-WORK) |
| What "plugin" means | Nothing. Not pluggable. | A named implementation of one core interface, chosen in the signed host profile | A separate process speaking the service API under a manifest and granted scopes |
| Selected by | Build | Signed HostProfile (`supervisor/cmd/tbound-profile` sign/verify, EXISTS) | Operator: process start plus UID grant (PROPOSED) |
| Loaded as | One signed binary | Compiled Go, or pinned bytes in the cell closure (TypeScript) | Separate executable or module; never in the core's address space |
| Effect authority | Yes, and only here | None. Providers return data or launch an untrusted worker that only proposes | None. Submit and cancel are requests the supervisor decides |
| Sees | Everything it owns | What its interface passes | Bounded projections, for granted scopes only |
| Version rule | One release, release-gated | Exact version and SHA-256 pin; mismatch fails closed (thesis `06-1`: "missing or changed identities fail closed") | Semver'd API; handshake refuses unknown major versions and ungranted scopes |

Tier 1 pins that exist today: `adapter/profile.json` pins `pi_version` `0.87.1` and per-tool `schema_sha256` values (EXISTS). `tbound-profile` provides sign, verify and digest commands (EXISTS).

### 3.2 Tier 2 manifest (PROPOSED for webchat; FUTURE-WORK for other clients)

```json
{
  "schema": "tbound-client-manifest/v1",
  "name": "webchat",
  "version": "0.1.0",
  "requires_api": "tbound-service/v1",
  "scopes": ["snapshot.read", "events.subscribe"],
  "optional_scopes": ["turn.submit", "turn.cancel"],
  "transport": "unix-socket",
  "browser_origin_policy": "loopback-exact"
}
```

Rules: deny by default. A scope absent from the manifest is never requested. A scope the manifest requests but the operator has not granted by UID is refused. Unknown fields are refused.

### 3.3 Lifecycle by tier

| Step | Tier 0 | Tier 1 | Tier 2 |
|---|---|---|---|
| Discover | None | Static registry compiled into the `cmd/` binaries (PROPOSED explicit list; today the choice is hard-wired in `main`) | None automatic. Operator names the manifest path (PROPOSED). No PATH or registry scanning |
| Enable | Build | Profile field, admitted by signed HostProfile | Start the process; grant scopes by UID in service config (PROPOSED) |
| Compatibility check | Build and tests | Profile admission fails closed on any digest or version mismatch | Handshake: client requests, server returns the granted set; refuse on mismatch |
| Disable | n/a | New signed profile | Stop the process; revoke the UID grant |
| Upgrade | New release | New profile and pins | Client restarts; server rechecks grants |

### 3.4 What each tier may contribute

| Contribution | Tier 0 | Tier 1 | Tier 2 |
|---|---|---|---|
| Provider or adapter implementation | Defines the interface | Yes, pinned | No |
| Policy rules | Yes | OUT | OUT |
| Tool routes or tool surface | Closed set `read`/`write`/`edit`/`bash` (`adapter/profile.json`) | OUT | OUT |
| `tbound` subcommands | Yes (today only `serve`, plus `--smoke-listen`) | No | Own binary (for example `tbound-webchat`), never a `tbound` subcommand |
| Listeners inside the core | Yes | No | No. Clients consume cursor events from the API only |
| UI | No | No | Yes; presentation only |

### 3.5 Explicitly out of scope

| OUT item | Reason |
|---|---|
| Dynamic code loading into the supervisor (`plugin.Open`, `dlopen`, runtime import of unpinned code) | Widens the TCB and breaks attribution |
| Plugins that add tool-execution routes or grow the Pi tool surface beyond the four tools | Thesis closed surface; MCP is a thesis non-goal (`00-roadmap` §4) |
| Manual per-tool approval plugins, human override | `TASK_STATE.md`, "Review evaluation / frontend direction": "No human override/per-tool approval conversion." |
| Policy-bypass plugins: anything that turns DENY into ALLOW, writes the live workspace, or edits journals or evidence | Thesis `06-1` presentation invariant |
| Plugins that receive raw journals, provider credentials, or unbounded provider payloads | Projection-only rule (`web-chat-contract.md` §5). Credentials are read from the environment by `openrouter.LoadCredentials` (`supervisor/internal/broker/openrouter/credentials.go`, `LoadCredentials`) and must stay there |
| Runtime discovery from PATH, home directories, or a package registry | Unpinned code would enter the trust picture |
| Advisory LLM plugins | `00-roadmap` §4 |

### 3.6 Why Tier 0 and Tier 1 are not shared-process plugins

- **TCB.** A shared-process plugin runs in the address space of the journal writer and the gate. A bug, or a compromised transitive dependency, there is inside the trusted computing base. Thesis `06-1` ranks "small and pinnable runtime/TCB" as a selection criterion for this reason.
- **Attribution.** Evidence must say which bytes ran. A DI container composes many packages at runtime, so the binding becomes per package rather than per process. The thesis closure is one signed binary plus named cell closures.
- **Blast radius.** Today the webchat has no core connection at all (`--live` is refused). The process boundary is what keeps a UI or exporter defect away from the gate and journals once the API exists. The webchat handler checks Host and Origin before routing (`authorizeRequest` in `extensions/webchat/internal/webchat/handler.go`). It answers GET on every route and HEAD only on `/` and `/favicon.ico`; other methods get `405`. The pre-move copy was `supervisor/internal/webchat/handler.go`. Its CSP forbids inline styles (`TASK_STATE.md`, increment 6).

## 4. Comparison with DeepSeek's Cordis plugin model

Verified from the dsh clone:

| Dimension | DeepSeek harness (verified) | tbound proposal |
|---|---|---|
| Process model | One Node process. `new Context()` is the root container and `ctx.plugin()` returns a Fiber (`dsh:vendor/cordis/README.md`, quick start). The CLI builds the root context in-process (`dsh:apps/cli/src/profile-boot.ts`, `runProfile`). | Tier 0 and Tier 1 in one trusted process. Tier 2 in separate processes. |
| Declaration | A plugin is an object that implements `Service` (the primer's wording), or "a function with optional `inject` and `apply(ctx)`" (`dsh:docs/cordis-primer.md`, section "Cordis In Five Ideas"). Packages extend a shared context by module augmentation, for example `declare module '@deepseek-ai/cordis'` in `dsh:packages/core/session/src/index.ts`. | Go interfaces and a static registry (Tier 1). Manifest JSON (Tier 2). No augmentation of a shared context. |
| Dependency | `inject` makes a plugin wait until named services exist (`dsh:docs/cordis-primer.md`, section "Cordis In Five Ideas"). | Static at build time (Tier 1). Scopes gate Tier 2. Nothing waits. |
| Composition | Profile layers and YAML patches feed a loader; the `disabled` field and overlays select plugins (`dsh:apps/cli/src/profile-boot.ts`, module header; `dsh:docs/cordis-primer.md`, section "Loader Configuration"). | Signed HostProfile selects Tier 1. Operator lists Tier 2 manifests. No config-driven code selection in Tier 0. |
| Lifecycle | Effects, listeners and services are removed when their owning fiber is disposed (`dsh:vendor/cordis/README.md`, section "Quick Start"). Shutdown is bounded (`dsh:apps/cli/src/profile-boot.ts`, `runProfile`). | Supervisor and workflow own lifecycle (`supervisor/internal/workflow`). Disconnect never cancels accepted work (`servePiServiceRequests` in `supervisor/cmd/tbound/pi_service_handoff_linux.go`, EXISTS). Cancel is an explicit request (P4). |
| Integrity | No plugin signature, checksum or integrity check found in `dsh:vendor/cordis/src` (grep for signature, checksum, integrity and sha256 returned nothing). | Signed profile plus digests; fail closed. |
| Dynamic code | Dynamic definitions run in a vm sandbox (`dsh:docs/capability-seams.md`, section "Capability Seams And Core Services", row `ctx.dynamicCordisRunner`). | OUT for the core. |
| Effect execution | Tools run in-process as plugins but spawn child processes through `ctx.subprocess` (`dsh:docs/capability-seams.md`, section "Capability Seams And Core Services", row `ctx.subprocess`). | Effects only through Tier 0 cells. No plugin spawns effects. |
| Headless | Profiles compose one base. `dsh-headless` is "a one-shot runner with no server"; `dsh-sdk-app` adds a JSON-RPC server (`dsh:docs/architecture.md`, section "Profiles and bundles"). | Same idea for the CI persona (§5.1). |
| Clients | `packages/client` has 63 directories; 60 import `@deepseek-ai/cordis` (counted in the clone). The client UI is a plugin of the same runtime; `extensions/webchat/docs/web-chat-ui-plan.md` Appendix A discusses adopting it. | Clients are Tier 2 processes. The UI never shares a process with the core. |

What tbound takes: explicit declarations; bundle-style composition for headless versus server profiles; per-component dispose and bounded shutdown; the rule that clients are plugins.

What tbound deliberately does not take: shared-process DI for Tier 0 and Tier 1; config-driven code selection in the core; dynamic definitions inside the core; and a "trusted" label with no enforcement. DeepSeek's docs use "trusted plugins" for webhook rule registration (`dsh:docs/capability-seams.md`, section "Capability Seams And Core Services", row `ctx.webhookRuntime`), but I found no enforcement point for that label in the clone.

Honesty note: DeepSeek is a general-purpose harness with a different threat model. These choices are not a critique of its engineering. A shared-process design suits a single-user harness. It is the wrong trade for a product whose claim depends on what ran.

## 5. Headless and remote operation (CLI-only local flow in scope; headless is FUTURE-WORK)

### 5.1 Personas

| Persona | Need | Today | Proposed or FUTURE-WORK |
|---|---|---|---|
| Operator on a remote Linux box over SSH | Start the service, check readiness, run a task, read history, cancel | `serve --pi` client against a managed service; no status or cancel | **FUTURE-WORK:** `tbound serve --headless` as a systemd unit. PROPOSED: `tbound session ...` over the local socket, reached over an SSH shell |
| CI | Non-interactive; exit codes; machine-readable receipt | `serve --pi --task-id ... --task ...` (documented; exit 2 without an admitted profile) | PROPOSED: same, plus `--json` on read commands |
| Local developer | Interactive session; read-only view of receipts | `tbound-webchat --receipt` and `--demo` (read-only) | PROPOSED: `tbound session attach`; webchat attaches over loopback |

### 5.2 Current CLI surface (EXISTS, verified against source)

Dispatch: `run()` in `supervisor/cmd/tbound/main.go` routes only `serve`; everything else is flags. The non-Linux `serve` build refuses (`supervisor/cmd/tbound/serve_other.go`).

| Command | Forms (verified) | Purpose | Source |
|---|---|---|---|
| `tbound serve --pi` | Ordinary client: `--task-id`, `--task-request-id`, `--task`, `--bash-output` (repeatable), `--bash-no-outputs`. `--native-host-profile`, `--workspace` and `--source` are refused for the client, because the service owns them | One admitted prompt per request to the managed service | `supervisor/cmd/tbound/serve_linux.go` (`runServe`); client `runProductionPiServiceClient` in `pi_service_handoff_linux.go` |
| `tbound serve --pi --service` | Internal; socket-activated only (`LISTEN_FDS=1`, fd 3) | Managed service process | `serve_linux.go` (`runServe`); `pi_service_handoff_linux.go` (`systemdActivatedPiListener`), `runProductionPiService` |
| `tbound serve --pi --dev-fixture` / `--native-fixture` | Non-claim-bearing fixtures | Development only | `serve_linux.go` (`runServe`) |
| `tbound --smoke-listen --socket-dir DIR` | Synthetic, no-effect Unix-socket listener (Linux) | Integration smoke | `supervisor/cmd/tbound/main.go` (`run`) |
| `tbound-doctor [--json] [--pi PATH] [--prefix DIR] [--version LABEL]` | Read-only; "never changes system state and never installs Pi" | Operator preflight | `supervisor/cmd/tbound-doctor/main.go` (package comment; `run`) |
| `tbound-profile` (15 subcommands plus `help`) | `init`, `sign`, `verify`, `template-init`, `template-digest`, `xattr-*`, `landlock-digest`, `managed-root-*`, `runtime-bundle-digest` | HostProfile and digest tooling | `supervisor/cmd/tbound-profile/main_linux.go` (`run`) |
| `tbound-provider` | No flags; one-shot OpenRouter tool call; credentials from the environment | Broker provider probe | `supervisor/cmd/tbound-provider/main.go` (`main`); `supervisor/internal/broker/openrouter/credentials.go` (`LoadCredentials`) |
| `tbound-webchat` | `--addr` (literal loopback only), `--demo`, `--fixture`, `--replay`, `--receipt`, `--receipt-demo`; `--live` is refused ("not implemented") | Read-only projection UI | `extensions/webchat/cmd/tbound-webchat/main.go` (`run`, `validateLoopbackAddress`) (HEAD `supervisor/cmd/tbound-webchat/`) |
| `tbound-cell-entrypoint` | In-image; not operator-invoked | D09 cell entrypoint | `supervisor/cmd/tbound-cell-entrypoint/main_linux.go` (file header) |

Not present in any source (verified by absence): `serve --headless`, `session ...`, `history`, `export`, `status`, `logs`, `health`, `ready`, `stop`, `auth`, `token`. `doctor`, `profile` and `provider` are separate binaries by design. The doctor header says it "is deliberately a separate binary from tbound, which stays focused on the supervisor runtime."

**Managed-service prerequisites (none shipped in this tree).** The production client needs a running managed service. The service refuses unless systemd passed it the socket (`LISTEN_PID`, `LISTEN_FDS=1`, `LISTEN_FDNAMES=tbound-pi`; `pi_service_handoff_linux.go` (`systemdActivatedPiListener`)). It expects fixed unit paths, `/etc/systemd/system/tbound-pi.socket` and `tbound-pi.service` (the unit path constants in `pi_service_handoff_linux.go`), which it reads as protected files and checks against a fixed contract, including a Podman-profile environment (`validatePiServiceRegistrationContract`, `validatePiServiceUnit`). It also validates a socket registration (`validatePiServiceSocketRegistration`) and needs a signed admitted profile. No unit file ships (grep finds only code, tests and this document). `tbound-doctor` has no socket or unit check (no hits in `supervisor/cmd/tbound-doctor`). `06-1` Decision lists host qualification as pending. The local CLI flow therefore works only on a host where an operator has installed and admitted these pieces.

Recommendation: keep that split. Adding these commands to the core binary grows the Tier 0 surface without adding authority.

### 5.3 Proposed command surface (none of these exist; label per row)

| Command | Behaviour | Builds on | Label |
|---|---|---|---|
| `tbound serve --headless [--socket PATH]` | Daemon: admits sessions, owns journals, serves the API on a 0700 Unix socket. No TTY. Socket activation preferred; direct bind for development only | Promotes `serve --pi --service` (`serve_linux.go`, `runServe`) | FUTURE-WORK |
| `tbound ready [--socket PATH]` | Exit 0 when the service answers `hello`; prints API version and session count | New | FUTURE-WORK (daemon readiness) |
| `tbound session start --task-id ID --workspace DIR --source DIR` | Creates a session; prints `session_id` | Today one task per request; a second concurrent request gets `busy` (`servePiServiceRequests` in `pi_service_handoff_linux.go`) | PROPOSED (Phase 2) |
| `tbound session list [--json]` | Lists sessions owned by the caller's UID | New | PROPOSED (Phase 2) |
| `tbound session status ID [--json]` | State, active turn, recovery disposition | `recovery` panel mapping (`web-chat-contract.md` §5) | PROPOSED (Phase 1) |
| `tbound session turn ID --prompt-file FILE` | Submits a turn; scope is server-configured, never supplied by the client | `piServiceRequest` (in `pi_service_handoff_linux.go`) | PROPOSED (Phase 2) |
| `tbound session attach ID [--from CURSOR]` | Read-only stream of cursor events | `web-chat-contract.md` §2.3 | PROPOSED (Phase 1) |
| `tbound session cancel ID [--turn TID]` | Requests cancel; prints the settled result or `UNKNOWN` (§10 Q5) | Thesis `03` §9.3; `03` §8 step 6 | PROPOSED (Phase 2) |
| `tbound history ID [--format json]`, `tbound export ID` | Bounded observation projection only; never raw journals | `productionPiReceipt` (`web-chat-contract.md` §5); receipt importer (`extensions/webchat/internal/webchat/receipt.go`) | PROPOSED (Phase 1) |
| `tbound logs [--follow]` | Tails bounded audit and request journals; must never expose raw journal lines (contract §3, item 4) | Journal file exists (`/var/lib/tbound/audit/serve-pi-requests.jsonl`, `pi_service_handoff_linux.go`) | FUTURE-WORK |
| `tbound stop` | Sends SIGTERM; drain semantics in §5.4 | Signal handling exists (`serve_linux.go` (`runServe`)) | FUTURE-WORK |
| `tbound client grant --uid N --scope S` (root) | Writes a scoped UID grant into the service config | Extends the UID allowlist (`pi_service_handoff_linux.go`, `piServicePeerAllowed`) | PROPOSED (Phase 2; P6) |
| `tbound token ...` | Token-based remote access (§5.5, §10 Q2) | None | FUTURE-WORK |

### 5.4 Service lifecycle

| Phase | Today | Proposed | Owner |
|---|---|---|---|
| Start | systemd socket activation: `LISTEN_PID` and `LISTEN_FDS=1`, fd 3 (`pi_service_handoff_linux.go`, `systemdActivatedPiListener`). Unit paths are constants (in the same file). No unit file ships in this tree; grep finds only code and tests | FUTURE-WORK: ship units from the installer; keep activation as the default | Core, installer |
| Readiness | None. Pi worker readiness is internal (`supervisor/internal/piruntime/worker_channels_linux.go`) | FUTURE-WORK: `ready` probe; optional READY notification to systemd | Core |
| Logs | JSONL journals (EXISTS) | FUTURE-WORK: `tbound logs`, bounded | Core |
| Graceful stop | SIGINT and SIGTERM cancel the service context (`serve_linux.go` (`runServe`)) | FUTURE-WORK: stop admission; wait a bounded time; settle any in-flight turn as `UNKNOWN` when uncertain; never retry an effect (aligned with thesis `03` §8 step 6) | Core |
| Crash recovery | Core recovery is specified and covered by fresh-process tests (`supervisor/cmd/tbound/governed_crash_linux_test.go`). The claim-bearing lifecycle owner is external (`02-1` §4.1) | FUTURE-WORK: surviving-owner design for a long-running daemon (Q1). Not redesigned here | Core thread, user |
| Concurrency | One task at a time; a second request gets `busy` (`servePiServiceRequests` in `pi_service_handoff_linux.go`) | PROPOSED (Phase 2): multiple sessions, one active turn each (sequential, per the thesis single-agent model) | Core |

### 5.5 Authentication and ownership

| Path | Auth today | Proposed | Status |
|---|---|---|---|
| Local Unix socket | `SO_PEERCRED`: UID in a sorted allowlist and GID equal to the primary GID (`pi_service_handoff_linux.go`, `readPiServicePeer`) | Keep; add a per-session owner UID | EXISTS / PROPOSED |
| Remote over SSH | None in core; no TCP listener | SSH shell on the host, then the local CLI (Q2). Forwarding the Unix socket is not supported (Q6) | DECIDED-for-now; socket forwarding UNVERIFIED |
| Remote over TLS, mTLS or tokens | None | Only after a separate architecture and evaluation (thesis `03` §3) | FUTURE-WORK |
| Browser clients | Host must be loopback or exact; Origin absent or exact; GET on every route and HEAD on `/` and `/favicon.ico` (`authorizeRequest` and `allowedMethods` in `extensions/webchat/internal/webchat/handler.go`) | CSRF token and exact Origin on POST routes; required before any non-loopback deployment (`web-chat-contract.md` §4) | EXISTS (GET) / PROPOSED (POST) |
| Outbound provider | Bearer key; TLS 1.2 minimum (`newHTTPDoer` and `Do` in `supervisor/internal/broker/openrouter/transport.go`) | Unchanged | EXISTS |

### 5.6 How webchat attaches

- **Default: loopback only.** `--addr` must be a literal loopback IP (`validateLoopbackAddress` in `extensions/webchat/cmd/tbound-webchat/main.go`, EXISTS).
- **Today:** the webchat reads fixtures and receipts only. `--live` is refused (EXISTS).
- **Proposed (Phase 1):** the webchat is a Tier 2 read client holding `snapshot.read` and `events.subscribe`. It runs as the operator's user on the same host and connects to the Unix socket. From a laptop, the operator forwards the **webchat HTTP port** with SSH (`ssh -L 8791:127.0.0.1:8791`). The socket is never forwarded. Nothing listens on a non-loopback address.
- **Acceptability (Q2):** forwarding the webchat port keeps the loopback model. The Host check sees loopback and the Origin check is unchanged. It is acceptable for today's GET-only routes. It does not satisfy the POST requirement: contract §4 requires a CSRF token and exact Origin before any POST route, whatever the transport. Forwarding the supervisor's Unix socket is a different thing and is not supported (Q6).
- **Milestone 3** (Phase 2) adds `turn.submit` and `turn.cancel` (`web-chat-contract.md` §4). Browser POST routes need the CSRF and origin layer first.

### 5.7 Example transcripts

Transcripts are illustrations. The remote-host transcript is FUTURE-WORK and is not a supported flow. The webchat transcript is PROPOSED (Phase 1). None of these commands exists.

```text
# FUTURE-WORK (illustration only): operator on a remote Linux host
operator@box$ tbound ready
ready api=tbound-service/v1 sessions=0 active_turn=none

operator@box$ tbound session start --task-id T-42 --workspace /srv/work/app --source /srv/src/app
session=s_7f3a state=idle owner_uid=1001

operator@box$ tbound session turn s_7f3a --prompt-file task.txt
turn=t_01 state=running scope=server-configured

operator@box$ tbound session attach s_7f3a --from 0
seq=1 decision tool=read verdict=ALLOW
seq=4 decision tool=bash verdict=ALLOW
seq=6 result   tool=bash status=succeeded (observed exit 0)
seq=8 turn     t_01 state=settled outcome=completed claim_bearing=false

operator@box$ tbound session cancel s_7f3a --turn t_01
cancel=requested result=UNKNOWN reason=cleanup-pending retry=none
```

The policy verdict and the execution result are separate lines, as the webchat requires: ALLOW is not success.

```text
# EXISTS today: CI route; documented, and refused without an admitted host profile (not re-run for this document)
ci$ tbound serve --pi --task-id T-42 --task-request-id ci-1234 --task "Add a test for parse()"
```

```text
# PROPOSED (Phase 1): webchat over SSH (port forward; the socket is not forwarded)
laptop$ ssh -L 8791:127.0.0.1:8791 operator@box
box$    tbound-webchat --addr 127.0.0.1:8791 --live    # --live exists today as a refusing flag
laptop  open http://127.0.0.1:8791/
```

## 6. Packaging and module layout

### 6.1 Units

| Unit | Location now | Module | Imports | Ships |
|---|---|---|---|---|
| Core | `supervisor/` | `tbound/supervisor` (`supervisor/go.mod`) | stdlib and `internal/*` | `tbound`, `tbound-doctor`, `tbound-profile`, `tbound-provider`, `tbound-cell-entrypoint` |
| Webchat extension | `extensions/webchat/` (moved in `18f71e6`; pre-move `supervisor/cmd/tbound-webchat`, `supervisor/internal/webchat`, `docs/web-chat*.md`) | `tbound/webchat` (`extensions/webchat/go.mod`) | No `tbound/supervisor` import (grep of `extensions/webchat` finds none) | `tbound-webchat` |
| Pi in-cell adapter | `adapter/` | Node/TypeScript (`adapter/package.json`) | Pi `0.87.1`, pinned in `adapter/profile.json` | Inside the cell closure only |
| Installer | `install/`, `packaging/`, `scripts/build-*.sh` | n/a | n/a | Blocked; see §6.4 |

Doc drift, now resolved: `extensions/webchat/docs/web-chat.md` no longer names the `tbound/supervisor` module for the Go toolchain.

### 6.2 Rules (PROPOSED)

1. **Extension depends on the core contract only.** An extension module never imports `tbound/supervisor/internal/...`. Go's `internal` rule already blocks this across modules.
2. **The contract is the observation schema (`tbound-webchat-observation/v1`) and the service API.** Today the webchat keeps its own copy of the shapes, which keeps the import graph clean. Choice for the core thread: keep duplicating (the webchat owns its copy in `contract.go`; no shared package exists), or extract an exported `contract` package that both modules import (one new edge). Recommendation: duplicate until the API is stable, then extract (Q7).
3. **No `replace` directive** from an extension `go.mod` into `supervisor/`.
4. **Frontend assets are committed built assets.** Node is needed only by maintainers changing the UI; consumers never need it. The React client is built with Vite (ADR-0001 supersedes the earlier zero-build rule for it), and its output is committed under `internal/webchat/dist/`. The legacy client stays zero-build until it is removed. Node stays a developer dependency, and the core never needs Node at runtime.
5. **Core installs without extensions.** Extensions install separately, each with its own manifest and checksum.

### 6.3 Boundary guard

`scripts/check-extension-boundary.sh` exists (since `18f71e6`) and has a `--self-test` that proves each rule fails on a violating copy (8 cases). It runs in `make test` and in CI (`.github/workflows/extension-boundary.yml`). Its rules: no Go file under `supervisor/` imports or names the extension; `supervisor/go.mod` has no require or replace for `tbound/webchat`; the extension imports no `tbound/*` package except its own, and its `go.mod` names no other tbound module; no `go.work` joins `supervisor/` with the extension.

### 6.4 Installer implications

- Core installs and runs without `tbound-webchat`.
- Extensions are separately installable and separately versioned.
- PR #10 (`faetschi/trust-boundary#10`) is a draft with security blockers and conflicts (`TASK_STATE.md`, "PR10 reuse integration"). This design does not depend on it.

## 7. API proposals for the core thread

Every row is a PROPOSAL. Each needs core-thread agreement before implementation. The core-thread decisions recorded in §10 are the current recommendation, not agreed.

| # | Proposal | Why | Starting point (EXISTS) | Core-thread decision |
|---|---|---|---|---|
| P1 | Persistent sessions with `session_id` and sequential `turn_id`s | Multi-turn chat; CLI session model | One task per request; a second gets `busy` (`servePiServiceRequests` in `pi_service_handoff_linux.go`) | Recommended: service-minted opaque `session_id` (§10 Q3/Q4). Turn boundaries: `web-chat-contract.md` §6 Q2 |
| P2 | Authenticated `snapshot` and `events` with cursor and epoch | Reconnect without loss; read-only clients | `ContractVersion` in `contract.go`; cursor `epoch:sequence` (`formatCursor` and `parseCursor` in `validate.go`); sequence checks in the webchat store | Recommended: the same authenticated socket, as new read-only kinds (§10 Q3/Q4) |
| P3 | `turn.submit` with server-side scope | Chat input | `piServiceRequest` already carries operator-set `bash_output_paths` (`b1ff84c`) | Turn and message boundary source (`web-chat-contract.md` §6 Q2) |
| P4 | `turn.cancel` as a request; the settled result comes from the supervisor | Honest cancel | Thesis `03` §9.3; the `cancel` case in `Serve` (`supervisor/internal/providerbridge/channel.go`) is an internal cancel kind (not the operator API) | Recommended: durable settled result, `UNKNOWN` when uncertain (§10 Q5). Wire kind and bound: core |
| P5 | Handshake: `hello` with protocol version and requested scopes; reply lists granted scopes | Tier 2 compatibility | `schema_version` strings (`tbound-pi-service-response/v1`) | Major-version policy |
| P6 | Scoped UID grants and session owner | Least privilege | `AllowedClientUIDs` (`pi_service_handoff_linux.go`, `piServicePeerAllowed`) | Grant store location; who may write it (root only) |
| P7 | `health` and `ready` (no session data) | Operator readiness (FUTURE-WORK: daemon readiness, §8 F1) | None | Probe transport |
| P8 | CSRF token and exact Origin on POST | Browser clients | Host and Origin guard (`authorizeRequest` in `handler.go`) | Token lifetime and binding |
| P9 | Bounded `export` and `history` | Receipts for review | `productionPiReceipt`; receipt importer | Redaction classes; no raw journal |
| P10 | Recovery disposition in `status` | Surfaces `UNKNOWN` | Recovery panel mapping (`web-chat-contract.md` §5) | Source of the disposition |

## 8. Phased roadmap

Phases 0–2 are the in-scope plan. Phase F is post-thesis: it has no schedule and enters only through its entry criteria. Phases do not change claim status. A phase that touches Tier 0 authority needs a thesis-impact review first.

| Phase | Scope | Entry | Exit | Owner |
|---|---|---|---|---|
| 0. Boundary | This document (with its scope box); extension move; boundary guard | User accepts the vision | Core thread reviews §3, §7 and §10; guard script landed (`18f71e6`, `63a7b90`); `TASK_STATE.md` updated by its owners | User, core, extension owner |
| 1. Read projection | P2 snapshot and events over the existing authenticated local socket; webchat as a read client on the same host; SSH forward of the webchat HTTP port only (webchat milestone 2) | Phase 0; core agrees Q3/Q4 | Live projection in the browser with gap disclosure; no submit path; qualification panel unchanged (`claim_bearing=false`) | Core, extension |
| 2. Interactive | P1, P3, P4, P6, P8 (webchat milestone 3): turn submit and cancel through the existing authenticated supervisor authority | Phase 1 exit; core agrees Q5; threat model updated for POST | Cancel shows "requested", then the durable result or `UNKNOWN`; no effect retried on reconnect; browser QA | Core, extension |
| F. Future work (post-thesis) | F1 headless daemon (`serve --headless`, shipped units, `ready`, `logs`, `stop`). F2 remote access beyond an SSH shell (TLS, mTLS, tokens, any non-loopback web). F3 multi-client (TUI/IDE, exporters, dashboards, non-webchat manifests). F4 forwarded-socket experiment (§10 Q6) | Every row: (1) a surviving-owner design (`02-1` §4.1) for F1, or a separate remote architecture and evaluation (`03` §3) for F2; (2) an evidence plan and tests, fresh-process for crash and recovery; (3) thesis-owner sign-off. F3 also needs manifest and scope checks. F4 needs a real host and the protocol approved | Each row passes its own gates. None changes the thesis claim unless the thesis is amended | Thesis owner, core, user |

Order note: this order follows the owner's direction (read projection before interactive turns). `web-chat-contract.md` §1 sequences persistent turns and cancellation before snapshot and streaming. The two conflict. The core thread must reconcile them; this document does not.

## 9. Non-goals

- Multi-tenant or internet-facing service; high availability; load balancing (thesis `00-roadmap` §4).
- Dynamic plugins in the core; runtime plugin marketplaces.
- Plugin-provided tools, MCP, or any change to the four-tool surface.
- Per-tool human approval or override.
- Browser or TUI authority over policy, containment, workspace or evidence.
- Headless daemon or remote control-plane claims before the §8 Phase F entry criteria are met (FUTURE-WORK).
- Any claim uplift: G1, H1, containment, or production launch. This design changes none of them.
- Replacing the Pi runtime or adding a second runtime. Thesis `06-1` fixes one implementation profile for H1; alternatives are future work.

## 10. Decision record for the open caveats

The owner accepted these recommendations. Where a source contradicted one, the row says so.

| Caveat | Decision (recommended handling) | Status | Rationale (cited) | Must be true before it can change | Owner |
|---|---|---|---|---|---|
| **Q1** Headless vs the surviving-owner invariant | (a) `serve --headless` and any daemon or systemd-managed mode are FUTURE-WORK. Until a surviving-owner design exists, at most a non-claim dev/ops convenience, never presented as qualified. (b) The in-scope headless-capable path is the CLI-only local flow: `tbound serve --pi` client, local socket, `tbound-doctor` and `tbound-profile`, reached over an SSH shell on the host. | DEFERRED-future-work (a); DECIDED-for-now (b) | (a) Thesis `02-1` §4.1: "The CLI uses this same route; direct supervisor and service-manager deployment modes are outside the thesis." Thesis `06-1` lifecycle ownership: "A supervisor cannot guarantee its own post-mortem cleanup." `serve --pi --service` refuses unless systemd passed the socket (`pi_service_handoff_linux.go`, `systemdActivatedPiListener`). (b) **Not verified today.** The service needs fixed unit paths (in `pi_service_handoff_linux.go`) checked against a fixed contract (`validatePiServiceRegistrationContract`, `validatePiServiceUnit`) and a validated registration (`validatePiServiceSocketRegistration`). No unit ships. `tbound-doctor` has no socket or unit check. `06-1` Decision lists host qualification as pending. **Thesis contradicts the old example:** "systemd plus cgroup kill" as the owner would be a service-manager mode. `02-1` §4.1 puts the owner in the evaluation runner. | (a) An owner outside the supervisor: stop admission, `cgroup.kill`, wait for `populated=0`, identity-matched reconciliation; cleanup failure is `UNKNOWN` (`02-1` §4.1). Fresh-process kill and recovery tests. A registered configuration and the thesis gates (`00-roadmap` §5). Thesis-owner sign-off. (b) Shipped, documented units; a doctor check for the socket and units; one recorded host run with commands, exit codes and receipts. | Core thread (design); user (sign-off) |
| **Q2** Remote access model | SSH only for the foreseeable scope: an SSH shell on the host, then the local CLI. The webchat HTTP port may be SSH-forwarded (§5.6). No core TCP listener. Token, TLS and mTLS are deferred. | DECIDED-for-now (SSH only); DEFERRED-future-work (token, TLS, mTLS) | Thesis `00-roadmap` §4: a demonstrator may "expose it through an existing secure channel such as SSH or a future browser client"; the thesis "does not claim that a custom remote API, multi-tenant service, or highly available deployment has been secured." Thesis `03` §3: a custom remote control plane "would require separate architecture and evaluation." Thesis `10-decision-register` D24: "defer multi-tenancy, high availability, and remote security claims." Repo: webchat answers GET on every route and HEAD only on `/` and `/favicon.ico`, checks a loopback Host and an absent-or-exact Origin (`authorizeRequest` in `extensions/webchat/internal/webchat/handler.go`); the gateway must authenticate to the supervisor and add CSRF and origin protection before any non-loopback deployment (`web-chat-contract.md` §4). | A separate remote architecture and evaluation with a threat model and tests (`03` §3) before any token, TLS or mTLS path. CSRF token and exact Origin before POST routes (contract §4). | User (decision); core thread (any control plane) |
| **Q3, Q4** Projection transport; session identity | Transport: the supervisor serves the projection over the same authenticated local socket, as new read-only request kinds (snapshot; events with subscribe). Not a second listener, not in-process in the extension, not raw journals. Identity: the service mints an opaque `session_id`, bound to the authenticated peer (owner UID), with the cursor `epoch:sequence` from the observation contract. `task_id` and `run_id` are fields, not the identity. | NEEDS-CORE-THREAD (both) | Contract §6 Q1 prefers a "service-owned projection" over reading raw journals. Contract §1 routes requests "to the existing authenticated supervisor authority." The service wire carries only `task_id` (`piServiceRequest` in `pi_service_handoff_linux.go`). Cursor `epoch` exists (`formatCursor` in `extensions/webchat/internal/webchat/validate.go`; the `Epoch` field of `Snapshot` in `contract.go`). `session_id` exists internally (`productionPiSession` in `supervisor/cmd/tbound/production_session_linux.go`) and as an optional webchat field (`SessionHeader` in `contract.go`), not on the service wire. Contract §6 Q3 is open. `06-1` D03 amendment: presentation clients cannot "alter evidence". | Core agrees the P2 kinds, version policy and payload bounds. A minted `session_id` maps to task and run. Tests: the projection never yields journal lines; a second UID cannot read another owner's session; a stale cursor produces a `gap`. | Core thread |
| **Q5** Cancel semantics | Cancel is a request to the supervisor, never a client-side fact. The UI shows "cancel requested". The settled result is whatever the supervisor durably records. An uncertain outcome or an exceeded wait is `UNKNOWN`. No client-side "cancelled" claim. No automatic retry on timeout or reconnect. | DECIDED-for-now (rule); NEEDS-CORE-THREAD (wire kind and bound) | Today there is no operator cancel on the wire: `piServiceRequest` has no cancel field (`piServiceRequest` in `pi_service_handoff_linux.go`). The `cancel` case in `Serve` (`supervisor/internal/providerbridge/channel.go`) is internal to the provider channel. Existing: wire status `unknown` (`writePiServiceFrame` in `pi_service_handoff_linux.go`); run error or empty receipt maps to `unknown` / `task-outcome-unknown` (`servePiServiceRequests`); disconnect never cancels accepted work (`servePiServiceRequests`). Thesis `03` §9.3: cancel "returns only after the required audit record is durable." Thesis `03` §8 step 6: "record the terminal outcome or explicit `UNKNOWN` state." `02-1` §4: `UNKNOWN` requires inspection; recovery "must never blindly replay." `00-roadmap` §6. Contract §4: "reconnect never retries an effect." **Correction:** `03` §9.3 does not mention timeouts or `UNKNOWN`. The timeout rule rests on the other sources in this row. | A `turn.cancel` kind with an idempotent request ID. A bounded wait. The settled result read from the durable record, not from the request. Fresh-process tests: cancel in flight; cancel after settle; reconnect during cancel; supervisor killed mid-cancel shows `UNKNOWN`. | Core thread (kind, bound); webchat owner (display) |
| **Q6** `SO_PEERCRED` over SSH-forwarded Unix sockets | Do not rely on it. No claim that a remote Unix socket can be forwarded. The only supported remote path is an SSH shell on the host, then the local CLI and socket. Forwarding the socket is an experiment for future work (protocol below). | DEFERRED-future-work (experiment); UNVERIFIED | The service reads `SO_PEERCRED` on accept and admits a UID in the sorted allowlist with GID equal to the primary GID (`pi_service_handoff_linux.go`, `readPiServicePeer`). **Expected, not verified:** through a forwarded socket, the connecting process is sshd's forwarding process on the host, so the credentials describe that process and its login user, not the laptop caller. Neither this tree nor any host run has shown it. Thesis `09-security-review.md` High 3: socket permissions or `SO_PEERCRED` "cannot distinguish" an adapter from same-identity code. Principle: credentials identify a holder, not the originating caller. No repo doc describes a forwarded socket. | An experiment on a real host, recorded, shows which peer identity a design can rely on. Until then no design depends on `SO_PEERCRED` through a forward. | Core thread; user (host operator) |

**Q6 experiment protocol (F4; not run).** Hypothesis: through a forwarded socket, the peer tuple is the SSH login user's UID/GID and an sshd child PID, identical whichever laptop-side process forwards.

| Step | Action and log | Pass | Fail |
|---|---|---|---|
| 0. Setup | Disposable Linux VM. Users `tbtest` (allowlisted, primary GID `G`) and `tbother` (not allowlisted). Log `id` for both, `uname -a`, `ssh -V`, and sshd's `AllowStreamLocalForwarding` value. Use a test listener on a throwaway socket path, never `/run/tbound/serve-pi.sock`. | Setup logged | Any step touches the production socket path |
| 1. Baseline | Local login as `tbtest` connects to the test listener. Log each accept's `SO_PEERCRED` tuple (pid, uid, gid), `/proc/<pid>/status` Uid/Gid, and `comm`. | Tuple equals `tbtest`'s `id` | Tuple differs from `id` |
| 2. Forwarded | From a laptop: `ssh -N -L LOCAL.sock:/path/to/test.sock tbtest@host`; connect to `LOCAL.sock`. Repeat three times. Log each tuple, the ancestry of the peer PID (`ps -o pid,ppid,uid,gid,comm`), and `ssh -vvv` forwarding lines. | Uid/Gid = `tbtest`; peer PID is an sshd child; three identical tuples | UID 0 observed; peer PID not an sshd descendant; tuples vary |
| 3. Negative | Forward as `tbother`. Log the listener's decision under the allowlist rule (`pi_service_handoff_linux.go`, `piServicePeerAllowed`). | Rejected (not in allowlist) | Accepted |
| 4. Laptop-side independence | Run the same forward from two different laptop-side processes or users. Log both tuples. | Tuples identical: the service cannot see the laptop caller | Tuples differ with the laptop-side caller |
| 5. Receipt | Record exact commands, exit codes, the test listener's sha256, all tuples, and a one-line conclusion. | Conclusion and receipts recorded | Missing receipts |

PASS confirms the hypothesis, and the design may then use the SSH login identity only, not the laptop caller. Any contrary observation is a FAIL: `SO_PEERCRED` is then unusable through a forward, and no design may rely on it.

| # | Question | Who decides | Recommendation |
|---|---|---|---|
| Q7 | Shared contract package, or duplicated observation shapes? | Core and extension | Duplicate now; extract when the API is stable |
| Q8 | Who owns signing and release custody for extension artifacts? | User | Same custody as core; separate key IDs |

## 11. What exists today versus proposed

| Item | Status | Reference |
|---|---|---|
| `tbound serve --pi` (ordinary client) | EXISTS | `supervisor/cmd/tbound/serve_linux.go` (`runServe`) |
| Socket-activated Pi service; SO_PEERCRED allowlist | EXISTS | `pi_service_handoff_linux.go` (`systemdActivatedPiListener`, `readPiServicePeer`) |
| Managed-service unit and registration validation | EXISTS as code; units not shipped; no host run recorded | `pi_service_handoff_linux.go` (unit path constants; `validatePiServiceSocketRegistration`); §5.2 |
| One task at a time (`busy`) | EXISTS | `pi_service_handoff_linux.go` (`servePiServiceRequests`, `busy` path) |
| Disconnect never cancels accepted work | EXISTS | `pi_service_handoff_linux.go` (`servePiServiceRequests`) |
| Run error or empty receipt maps to `unknown` | EXISTS | `pi_service_handoff_linux.go` (`servePiServiceRequests`) |
| Signal-driven shutdown of the service | EXISTS | `serve_linux.go` (`runServe`) |
| Crash-recovery tests (fresh process) | EXISTS as tests; not a qualification | `supervisor/cmd/tbound/governed_crash_linux_test.go` |
| `tbound-doctor` (separate binary, `--json`; no socket or unit checks) | EXISTS | `supervisor/cmd/tbound-doctor/main.go` (`run`) |
| `tbound-profile` signing and digest tools | EXISTS | `supervisor/cmd/tbound-profile/main_linux.go` (`run`) |
| `tbound-provider` one-shot | EXISTS | `supervisor/cmd/tbound-provider/main.go` (`main`) |
| `tbound-webchat`: read-only, loopback, `--live` refused | EXISTS | `extensions/webchat/cmd/tbound-webchat/main.go` (`run`, `validateLoopbackAddress`) |
| Observation contract `tbound-webchat-observation/v1`; cursor `epoch:sequence` | EXISTS | `extensions/webchat/internal/webchat/contract.go` (`ContractVersion`), `validate.go` (`formatCursor`) |
| Post-run receipt importer | EXISTS | `extensions/webchat/internal/webchat/receipt.go` |
| Pi adapter pins (`pi_version`, tool schema hashes) | EXISTS | `adapter/profile.json` |
| Pi in-cell extension | EXISTS | `adapter/src/tbound-cli-extension.ts` |
| Read-only projection over the socket (P2) | PROPOSED (Phase 1) | §7 |
| `session`, `history`, `export`, `status` CLI | PROPOSED (Phases 1–2) | §5.3 |
| Turn submit, cancel, scoped grants, CSRF | PROPOSED (Phase 2) | §7 |
| Handshake for non-UID clients | FUTURE-WORK (F3) | §7 P5, §8 |
| Tier 2 manifest | PROPOSED for webchat; FUTURE-WORK for other clients | §3.2 |
| `scripts/check-extension-boundary.sh` | EXISTS (since `18f71e6`; `--self-test` in `make test` and CI) | §6.3 |
| Installer shipping extensions | PROPOSED; PR #10 blocked | `TASK_STATE.md` |
| `serve --headless`, `ready`, `logs`, `stop` | FUTURE-WORK | §5.3, §8 F1 |
| Systemd unit files for the service | FUTURE-WORK (constants exist; no shipped file) | §5.4 |
| Remote TLS, mTLS, bearer tokens | FUTURE-WORK (decision: DEFERRED-future-work) | §5.5, §10 Q2, §8 F2 |
| TUI/IDE, exporters, dashboards | FUTURE-WORK | §8 F3 |
| Multi-tenant, HA, internet exposure | OUT | §9 |

## 12. Suggested future-work note for the thesis owner

**Suggestion only. Not applied to the thesis repo.** Copy-ready text for the thesis owner to adapt, for example for `00-roadmap` §4 or `99-future-work` §1:

```text
Headless and remote operation of tbound is post-thesis future work. It is outside
the claim-bearing profile: the evaluation runner remains the sole surviving lifecycle
owner (02-1 §4.1), and direct supervisor and service-manager deployment modes are
outside the thesis. A future headless or remote design may claim only what it
demonstrates after it preserves that surviving-owner invariant: an owner outside the
supervisor stops admission, kills the complete subtree via cgroup.kill, waits for
populated=0, reconciles identity-matched resources only, and records cleanup failure
as UNKNOWN. It must then pass the registered gates (00-roadmap §5) in its own
registered configuration, with thesis-owner sign-off. Remote multi-user operation,
token or mTLS control planes and high availability need separate architecture and
evaluation (03-architecture §3). None of this changes H1, G1 or containment.
```
