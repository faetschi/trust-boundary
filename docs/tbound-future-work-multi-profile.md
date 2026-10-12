# TBound future work: multi-profile and pluggable Pi configurations

**Status:** forward-looking engineering design note. This describes intended
extension work, not current capability. It is deliberately **outside the thesis
claim path**: the master thesis stays pinned to one Pi/model/Linux profile so its
results remain reproducible. Nothing here changes `G1=false`, the production
refusal, or any evidence requirement.

Purpose: make TBound easy to extend so that later (non-thesis) development can
support **multiple Pi versions, multiple approved configurations, and a
"bring your own Pi" mode**, without weakening the guarantees the thesis relies on.

---

## 1. Design principle

TBound's guarantee comes from **closure**: it launches a known harness with a known
surface and mediates every effect. "Support more configurations" therefore means
"have more *registered, conformance-tested profiles*", not "accept anything".

> The unit of configurability is the **profile**, not an ad-hoc flag.

A configuration is usable in governed mode only after it passes the same closure
and correlation checks the current pinned profile passes. Everything else is an
explicit, clearly labeled non-claim mode.

---

## 2. What exists today (the extension seeds)

These are the real hooks future work builds on:

| Concern | Current code |
|---|---|
| Trusted profile identity + component digests | `piruntime.HostProfile` / `HostAttestation` / `HostAttestor` / `TrustedHostVerifier` (`supervisor/internal/piruntime/host.go`) |
| Signed host profile loader | `supervisor/internal/piruntime/profile_linux.go`; inputs `/etc/tbound/pi-host-profile.json`, `.ed25519`, `.pub` |
| Exact child launch (argv/cwd/env/FDs) | `supervisor/internal/piruntime/launcher_linux.go`, `descriptor.go`, `child.go` |
| Fixed worker FD contract | `supervisor/internal/piruntime/worker_channels_linux.go`; `PiWorkerDescriptorProfile`, `NewPiWorkerChannels`, `LaunchPiSDKWorker` (`docs/governed-pi-launcher.md`) |
| Real Pi construction / proxy tools | `adapter/src/governed-pi-worker.ts`, `adapter/src/proxy-tools.ts` |
| Locked resource surface | `adapter/src/locked-resource-loader.ts` |
| Go-owned provider/conversation | `supervisor/internal/providerbridge/conversation.go` (`Config`), `docs/provider-conversation-bridge.md` |
| Provider profile contracts | `docs/provider-profiles.md` |
| Native-action policy (refuse/allow routes) | `adapter/src/native-pi-policy.ts`, `adapter/native-pi-policy/**` |
| Session/publication composition | `supervisor/internal/sessionlaunch/**`, `supervisor/cmd/tbound/governed_session_linux.go`, `docs/governed-session-composition.md` |

Today these are wired for **one** profile. The future work is to make the profile
a first-class, selectable object.

---

## 3. The profile abstraction (make it explicit)

Define a versioned **profile document** (JSON, canonically hashed and signed) that
captures everything admission currently hardcodes. Minimum fields, grouped:

1. **Harness**
   - `harness`: `pi` (future: other harnesses via adapters)
   - `pi_version`, locked package digests for `pi-coding-agent` / `pi-ai` / `pi-tui` / `typebox`
   - `runtime_bundle_root` + digest (the worker/bundle tree)
2. **Launch identity**
   - `child_executable` + digest, `node_version`, `argv_profile` + digest,
     `working_directory` + digest, `stdin_profile` + digest, `env_profile` + digest,
     `descriptor_profile` + digest
3. **Surface**
   - `tool_manifest` (names + schema digests), `resource_loader` policy,
     `extension_policy` (see §5), `native_action_policy` id + digest
4. **IPC / observation**
   - `ipc_profile`, `terminal_profile`, `observation_profile` (+ digests)
5. **Provider**
   - `provider_profile` (endpoint protocol family + exact model ID), broker binding
6. **Isolation / lifecycle**
   - `containment_profile`, `cgroup_root`, `settlement_profile` (+ digests)
7. **Provenance**
   - `source_provenance`, `offline_verifier` (+ digests)
8. **Conformance evidence**
   - `closure_report`, `correlation_report`, `containment_report` (ids/digests + date)

This is essentially today's `HostProfile` with a harness/version wrapper and a
conformance section. Keep it backward-compatible: the current fields map 1:1.

---

## 4. Admission modes

Introduce three explicit modes so users are never confused about guarantees:

| Mode | Admission | Guarantee | Use |
|---|---|---|---|
| `governed` | Registered profile with passing conformance | Claim-bearing: closure, correlation, staging, containment, evidence | Thesis + production |
| `governed+extensions` | Registered profile + approved extension set (§5) | Same, for the extended surface only | Teams with custom tools |
| `byo-pi` | Unverified local config; best effort | **None** — advisory only, labeled non-conformant | Onboarding/debug |

`byo-pi` is the honest answer to "point TBound at my customized Pi and it works":
it launches, but TBound will not claim it governs effects it cannot see.

---

## 5. The extension contract (how customization stays governable)

A customization can enter governed mode only if its effects are expressible
through TBound. Rules:

- **New tools** must be **proxy tools**: declared schema + digest, executed through
  the same `IpcProposalSender` → broker correlation → gate → durable executor path.
  A tool that runs directly inside Pi is not covered.
- **New resources / prompts / skills** are allowed only if the loader registry
  pins their exact identity (name + digest); unpinned discovery stays disabled.
- **Provider changes** must route through the trusted broker. "User key inside Pi"
  is incompatible with governed mode; it is a `byo-pi` configuration.
- **Native actions** (`!`/`!!`, `/login`, `/model`, `/reload`, `/import`, session
  fork/resume) are allow-listed or refused per profile. Supporting a new one means
  adding a policy hook that runs *before* the effect, not filtering output after.

Extension surface plugins should implement a small interface, e.g.:

```
type SurfaceExtension interface {
    ToolManifests() []ToolSchema            // proxy-backed only
    ResourcePins() []ResourceIdentity       // name + digest
    NativeActionPolicy() NativeActionPolicy // allow/deny per route
    Validate(profile Profile) error         // fail closed
}
```

Then a profile's `extension_policy` references a registered, digest-pinned set of
extensions. This keeps "custom Pi" a governed, reviewable configuration instead of
an unreviewed bypass.

---

## 6. Profile registry mechanics

- **Layout:** one directory per profile, e.g. `profiles/<profile-id>/` with
  `profile.json`, `profile.ed25519`, `profile.pub`, and `conformance/*.json`.
- **Selection:** `tbound serve --pi --profile <id>` (and config default). Unknown
  or unregistered id → refuse. Keep `serve --pi` refusal until a profile exists.
- **Drift:** verify profile digest + every component digest at admission; any
  mismatch → refuse. This is exactly today's `HostProfile.Verify`/attestation path,
  generalized to N profiles.
- **Harness neutrality:** keep field names harness-agnostic (`harness`,
  `harness_version`) so non-Pi harnesses can be added later via adapters without
  changing the registry or launcher shape.

---

## 7. Per-Pi-version integration checklist

Because TBound integrates against Pi internals (worker bundle, action policy,
schema digests), each new Pi version needs a small, bounded effort:

1. Update locked package digests; rebuild/verify the runtime bundle.
2. Re-run the **closure probe** (`adapter/src/closure-probe.ts`): exactly four
   proxy tools, no native/direct alternatives, empty resource inventory.
3. Re-check tool **schema digests** against the profile.
4. Update the **native-action policy** hook if interactive internals changed.
5. Re-run **correlation** tests (`providerbridge` real Pi SDK test) and the
   Go↔Node↔Pi process round-trip.
6. Re-run **containment/settlement** checks for the target host profile.
7. Record a new **conformance report** and bump the profile only if all pass.

Automate steps 2–5 as `tbound profile conformance <dir>` so adding a version is
mostly running one command and reviewing a report.

---

## 8. Implementation roadmap (phases)

Ordered, each with a clear exit so it can be stopped safely:

- **Phase A — extract the profile document.** Generalize `HostProfile` into a
  versioned profile file with canonical hashing; keep current behavior as the
  single default profile. Exit: identical admission behavior, one profile file.
- **Phase B — registry + selection.** `profiles/` layout, signature verification,
  `--profile` selection, fail-closed on unknown/drift. Exit: two profiles loadable
  (e.g. current + a re-signed copy), tests for mismatch/refusal.
- **Phase C — conformance automation.** `tbound profile conformance <dir>` running
  closure/correlation/schema/containment checks and emitting a report. Exit: adding
  a Pi version is one command + review.
- **Phase D — extension contract.** Proxy-tool/resource/provider/action extension
  interface + `extension_policy`; admit an example extension. Exit: a custom tool
  admitted as a governed proxy tool with tests.
- **Phase E — `byo-pi` mode.** Best-effort launch of an unregistered config, hard
  labeled non-conformant, no containment/G1 claim, output banner. Exit: runs, and
  the UI/CLI/evidence unmistakably say "advisor, no guarantee".
- **Phase F — harness-agnostic core (optional, later).** Move Pi-specific bits
  behind a `harness` adapter boundary. Exit: a second (even toy) harness launches
  through the same core. This is where multi-harness becomes possible.

Phase A–C deliver the "multiple Pi versions/configs" goal with the least risk.
D–E deliver "customized Pi works". F is the long-term generalization.

---

## 9. What must not change (thesis / safety guardrails)

- Keep the thesis pinned to **one** frozen profile; multi-profile is future work and
  any new claim-bearing profile needs its **own** closure/correlation/containment
  evidence before it can be used in results.
- Never admit a profile from a caller boolean, path string, or unsigned receipt.
  Signing + digest binding stay mandatory in `governed` mode.
- Never let a launcher hand Pi a raw private-Git/admin directory; the worker gets a
  verified sealed/exposure view only (see the open D06 contract in
  `docs/governed-pi-launcher.md`).
- Credentials stay in the Go broker; no profile may place them inside Pi.
- `byo-pi` never inherits the security claim and must be visually/structurally
  distinct in CLI, logs, and evidence.

---

## 10. Open questions / decisions needed

1. **Signature trust root** for profiles: one TBound project key, or an
   operator-managed allowlist of keys?
2. **Profile distribution**: profiles in-repo, per-machine `/etc/tbound/profiles`,
   or both? (The thesis uses the fixed `/etc/tbound/pi-host-profile.*` files.)
3. **Extension review process**: who signs off an extension set before it can be
   registered as governed?
4. **Non-Pi harnesses**: is Phase F in scope for the project, or stay Pi-only?
5. **`byo-pi` scope**: launch-only observation, or also attempt best-effort
   tool mediation when the config exposes compatible proxy hooks?

---

## 11. Related documents

- `docs/governed-pi-launcher.md` — worker FD map and current production refusal.
- `docs/governed-session-composition.md` — session/D06 composition.
- `docs/provider-conversation-bridge.md` — provider ownership contract.
- `docs/provider-profiles.md` — provider profile contracts.
- `docs/native-pi-integration.md`, `docs/native-runtime-composition.md` — Pi runtime seams.
- `docs/tbound-readiness-status-2026-10-08.md` — current overall status.

*This note is design guidance for future development and does not assert any
implemented capability beyond what the listed code and evidence documents already
state.*
