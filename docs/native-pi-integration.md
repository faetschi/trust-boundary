# Native Pi host integration (Pi 0.87.1)

This is a deliberately bounded native-TUI research seam. It is not a
production CLI, provider broker, or contained executor. The stock fixture
remains render-only. A separately labeled experimental private patch admits
only the fixture's fixed prompt/steer subset; production/authenticated-provider
construction remains refused until a reviewed provider-stream seam, admitted
Pi profile, and native action governance are available.

## Construction seam

Implementation: `adapter/src/native-pi-host.ts`.

```ts
const host = await createNativePiHost({
  mode: "fixture",             // explicit; never an implicit default
  ipc,                          // existing binding-token-authenticated IpcClient
  cwd,
  terminal,                    // caller-owned @earendil-works/pi-tui Terminal
  onObservation,                // structured, non-authoritative channel
});

await host.init();              // constructs/initializes real InteractiveMode
await host.prompt("...");       // controlled prompt seam
await host.steer("...");
await host.followUp("...");
await host.dispose();
```

`createNativePiFixtureHost()` is an explicit stock-input fixture alias. It creates a real
`AgentSessionRuntime` and real `InteractiveMode` using the public Pi APIs, an
in-memory `SessionManager`, `SettingsManager.inMemory`, a
`LockedResourceLoader`, a memory-only credential store, and Pi's real
`fauxProvider`. The faux response budget is eight provider requests: one
provider-generated tool call plus one provider-generated continuation for each
of the four tools. The four tool calls go through the existing
`IpcProposalSender`/`IpcClient`; the host does not call `tool.execute` itself.

The exact active and registered tool set is checked after every created
session: `read`, `write`, `edit`, `bash`, each with source `sdk` and path
`<sdk:${name}>`. Resource inventories are checked empty for extensions,
extension errors, skills, prompts, themes, context files, system prompts, and
appended prompts. `LockedResourceLoader.reload()` and resource extension remain
refusals.

The host does not expose Pi's raw `AgentSession`, `Model`,
`AgentSessionRuntime`, resource loader, or mutable model/runtime services.
`sessionView` exposes only the current in-memory session ID and pending-message
count, and it becomes invalid after disposal. `observations` is a read-only
bounded snapshot with an explicit gap record if the record/byte budget is
exceeded.

Runtime, UI, terminal, IPC, and lifecycle internals use JavaScript private
fields (not TypeScript-only `private`). This prevents casual same-process
property access; it is still a cooperative in-process host, not a security
boundary against hostile code in the same isolate.

The fixture wraps Pi's `ModelRuntime` behind a closed provider facade. The
facade exposes exactly `tbound-native-fixture/tbound-native-fixture-model`, no
credentials, and no provider/model/auth mutation methods. Before construction,
known ambient provider environment variable names—including
`COPILOT_GITHUB_TOKEN`—are checked for presence with `Object.hasOwn`; the guard
does not retrieve, log, or mask their values. If one is present the fixture
refuses. This is not environment isolation: the process HOME and default
credential locations are not sanitized or exhaustively probed by this
in-process fixture. The memory credential store and closed model facade avoid
using built-in provider auth paths here; any real child launch must use an
empty controlled HOME/agent directory and a minimal inherited environment.

`createNativePiProductionHost()` and `createNativePiHost({ mode: "production" })`
return `NativePiHostRefusal` with
`ERR_NATIVE_PI_HOST_REFUSED`; they do not construct Pi, contact a provider, or
read credentials. The refusal is intentional and is not an SDK/browser
fallback.

### Production CLI channel contract proposal (not implemented)

The eventual native executable should retain Pi's real `InteractiveMode` and
real terminal editor. A concrete runtime-owner entrypoint candidate is
`createNativePiCliHost({ terminal, bootstrap, brokerIpc, observations,
profileAdmission })`; it should not call stock `InteractiveMode.run()` or
rebuild the UI. A parent launcher would start the child with standard FDs 0/1/2
for terminal stdin/stdout/stderr, FD 3 for a bounded bootstrap/profile
handshake, FD 4 for binding-token-authenticated broker IPC, and FD 5 for
structured observations. The bootstrap channel supplies only the admitted
profile/artifact identity and one-time IPC binding; secrets must not be command
line arguments or environment variables. FD 5 is non-authoritative and
bounded. Terminal output remains presentation-only; neither stdout nor stderr
is parsed for policy decisions.

The runtime owner still has to implement and review the launcher, FD ownership,
minimal child environment/HOME, Pi provider-stream adapter, broker registration
of provider response IDs and argument digests, and profile-admission evidence.
Until then the proposed entrypoint is not exported or advertised as a usable
production CLI. External OS signing, elevation, or machine-specific trust
prerequisites are not inferred by this adapter.

## Native TUI and action boundary

`host.init()` initializes the real pinned stock `InteractiveMode` and renders
through the caller's `Terminal`, but wraps its input callback with an inert
sink. Consequently the stock host is explicitly render-only for native
terminal input; `prompt`, `steer`, and `followUp` are the only admitted input
paths.
`host.run()` is intentionally a fail-closed refusal rather than unrestricted
`InteractiveMode.run()`. Pi 0.87.1's private submit/action handlers execute
these routes before a host-level structured callback can govern them:

| Native action | Pinned behavior observed | Current host decision |
| --- | --- | --- |
| `!` / `!!` | `handleBashCommand()` calls `session.executeBash()`; `!!` only changes context recording | Refuse stock run; never parse terminal output as authority |
| `/login`, `/logout` | Pi auth UI reaches `ModelRuntime.login/logout` | Refuse stock run; fixture credentials are memory-only |
| `/model`, model cycling | Pi selectors/cyclers call `session.setModel/cycleModel` | Refuse stock run; fixture has one model |
| `/reload` | Calls `session.reload()` and the resource loader | Locked loader rejects reload; stock run still refused |
| `/import` | Calls runtime JSONL import | Not admitted; stock run refused |
| `/new`, `/resume`, `/fork` | Calls runtime/session replacement and selectors | Public runtime seam exists, but no interactive authority is claimed |
| package checks/updates | `run()` checks packages unless `PI_OFFLINE`; package settings are empty | Offline; no package discovery/update is admitted |
| extensions/skills/prompts/themes | Resource discovery is bypassed by `LockedResourceLoader` | Empty locked inventory; additions/reload reject |

The ordinary programmatic prompt, steering, and follow-up seams remain usable
after `init()`. They use `source: "interactive"` and
`expandPromptTemplates: false`, so a direct string beginning with `!` is model
input rather than a shell action. Bytes sent to the caller-owned terminal are
discarded by the host's input wrapper, including normal text, `!`/`!!`, slash
commands, aliases, and shortcuts. This is deliberate: rendering the native
editor does not make its private action handlers governed. This stock host
therefore makes no native-typing claim; the separately labeled experimental
private-policy fixture is described below and is not a production integration.

### Public-hook result and remaining upstream seam

The pinned public extension API provides one useful effect hook:
`ExtensionAPI.on("user_bash", ...)` can return a structured `BashResult` and
prevent local execution of `!`/`!!`. It cannot see the native editor's slash
dispatch or app-keybinding callbacks. `input` is also too late for those routes:
`InteractiveMode` handles built-in slash commands and bash prefixes before it
calls `AgentSession.prompt()`. `model_select` is notification-only, and the
runtime's replacement methods have no host policy callback; the
`session_before_switch`/`session_before_fork` extension events are not a
complete import/model/auth/action boundary.

The smallest reviewable upstream integration is a pinned `InteractiveMode`
action-policy option, not terminal-byte filtering or monkeypatching:

1. Add an exported policy type and constructor option to Pi `0.87.1`'s
   `InteractiveMode`.
2. Invoke it before every submit classification, `user_bash` dispatch, direct
   slash-command handler, selector/model/auth/reload/import/session handler,
   package/version check, and every app shortcut/escape/Ctrl-D/paste/suspend
   callback. The callback receives a structured action identifier and relevant
   validated arguments; denial must prevent the handler and return a structured
   refusal to the caller.
3. Add a runtime policy callback or immutable runtime capability token for
   session replacement, model/auth mutation, and resource reload. The host
   factory must reassert the fixed cwd, agent directory, locked resource
   loader, exact four-tool inventory, and provider capability after every
   replacement.
4. Add an integration test in the Pi source that feeds virtual-terminal bytes
   for normal text, `!`/`!!`, `/login`, `/logout`, `/model`, `/reload`,
   `/import`, `/new`, `/resume`, `/fork`, `/share`, package/extension commands,
   and app shortcuts, asserting each policy decision precedes any effect.

The repository contains a separately owned experimental artifact under
`adapter/native-pi-policy/`. It is rebuilt only from the exact installed Pi
`0.87.1` `InteractiveMode` source hash and refuses source/version/context/output
drift. The loader validates output bytes, then imports that exact byte snapshot
via a `data:` URL with static imports rewritten to installed Pi module file
URLs; it does not reopen the output pathname after hashing. The generated
output/hash contains absolute file URLs and is checkout-specific. This binds
the patched module bytes for this local experimental run, not the transitive
dependency tree or an upstream-signed package, and is not a production trust
boundary.

`createNativePiPolicyFixtureHost()` runs the real private-patched
`InteractiveMode` with a mandatory fail-closed policy. Its immutable fixture
action set admits only normal prompt/steer plus a small set of non-mutating
editor actions. Caller policies cannot grant shell, slash commands, selectors,
auth/model/session mutation, reload, extensions, exit, or paste. Missing,
throwing, rejected, or malformed policy callbacks deny. Temporary startup
Ctrl-C/Ctrl-D/submit handlers are guarded before TUI input starts. This is an
experimental integration patch, not stock Pi, a signed patch, or a
production/native-typing acceptance receipt. Package files, lockfiles, live
`node_modules`, production construction, and unrestricted `run()` remain
unchanged/refused.

`adapter/native-pi-policy/NOTICE.md` records the installed package metadata:
author Mario Zechner, declared `MIT` license, and upstream repository. The
installed package payload had no license text file, so the notice does not
invent or reproduce license wording.

## Observation and IPC

`NativePiObservation` is a separate callback/list buffer. It records host-ready,
agent-event type, structured tool proposal, structured IPC result, IPC
error/cancellation telemetry, policy decisions, explicit host refusals, and
fixture-only continuation receipts emitted only after the faux provider
confirms the preceding tool result. The retained snapshot is bounded to 1,024
records and 256 KiB of serialized records; overflow is represented by an
`observation_gap` record. Sink callbacks receive a clone so they cannot mutate
the retained bounded snapshot.
It never parses ANSI/terminal output and observer exceptions cannot change
authorization, refusal, or agent control flow.

`IpcClient` retains the existing 32-byte binding-token validation, frame
correlation, sequence, and size limits. The native host receives an already
constructed client; provider credentials and provider API keys are not inputs
to this host and must not be inherited by its child process. A future
supervisor launcher must provide only a minimal inherited environment and the
IPC binding, with credentials retained by the trusted broker.

The generic IPC result schema still permits an omitted
`canonical_arguments_digest` for backward compatibility. The native host
narrows this: every result, including `DENY`, must include the exact canonical
digest of that proposal. Missing or mismatched digest is an
`ERR_IPC_CORRELATION_MISMATCH`, is not reported as a bound tool result, and
cannot feed a provider continuation. This native-only check avoids silently
changing the generic wire contract; broker response-ID authentication remains
the trusted supervisor's responsibility.

## Offline fixture boundary and limits

- Pi and Pi AI are pinned to `0.87.1`; Node 24.15.0 was used for verification.
- The host verifies installed versions of Pi, Pi AI, and Pi TUI at runtime and
  verifies the four pinned TypeBox schema digests before admitting a session.
- `ModelRuntime.create()` uses `modelsPath: null`,
  `allowModelNetwork: false`, and `refreshOnCreate: false`.
- `allowModelNetwork: false` is not treated as credential isolation. Before
  `ModelRuntime.create()`, the host checks presence of known provider variable
  names only; it does not retrieve their values or globally strip/restore the
  environment. Default credential locations are not comprehensively enumerated
  or sanitized in this same-process fixture. The closed model facade avoids
  built-in provider auth calls in the exercised fixture, but this is not an OS
  credential boundary. A future launcher must supply an empty controlled HOME,
  private agent directory, and minimal child environment.
- `host.init()` serializes its temporary `PI_OFFLINE=1` ownership across
  concurrent fixture initializations, so one host cannot restore the process
  global while another host is still in Pi's fd/rg bootstrap. The prior value is
  restored after the serialized initialization.
- Prompt size is bounded to 16 KiB UTF-8 bytes.
- The fixture has exactly eight queued provider responses. An unexpected ninth
  provider request receives Pi faux-provider exhaustion rather than an invented
  response.
- Existing IPC limits remain 1 MiB per message, 1,024 frames per direction,
  and 16 MiB per stream (`adapter/src/ipc-transport.ts:5-15`).
- Fixture tests use DENY results and an in-memory duplex pair; no workspace
  files, shell commands, provider network, dependency installation, or
  credential store are used.
- Faux continuation factories assert that each preceding structured DENY result
  (tool-call ID and result payload) is present in the next provider transcript;
  tests also assert response IDs and canonical-argument digests. An absent
  digest produces no bound-result observation and no continuation receipt. IPC
  mismatch and cancellation paths emit structured telemetry and do not broaden
  authority.

## Verification

From `adapter/`:

```text
npm exec tsc -- --noEmit                         exit 0
node --experimental-strip-types --test \
  --test-concurrency=1 \
  src/native-pi-host.test.ts \
  src/native-pi-policy.test.ts                    exit 0
node --experimental-strip-types --test \
  --test-concurrency=1 \
  src/ipc-transport.test.ts                         exit 0
```

The stock native test uses a private virtual terminal and the real public
`InteractiveMode`; native bytes are inert there. It then drives four
provider-generated tool calls, accepts four structured IPC DENY results, and
verifies four provider-generated continuation messages in the native TUI,
including exact result-ID/digest binding. It also rejects missing/mismatched
native result digests. The experimental policy tests load the pinned private
patched artifact, exercise real editor prompt/steer/follow-up input, deny
shell/slash/session/model/shortcut routes under caller allow-all, inject bytes
during initialization, and verify prompt-wait cancellation on disposal and
source/output drift refusal. These tests remain separate from the unchanged
package `npm test` script. Production construction and unrestricted `run()`
remain refusals.

## Pinned source observations and hashes

The following installed Pi 0.87.1 files were the source observations used for
the construction/action decision. Hashes are SHA-256 of the exact installed
bytes on 2026-10-07 (uppercase output normalized to lowercase here):

| Path | Relevant lines | SHA-256 |
| --- | --- | --- |
| `adapter/node_modules/@earendil-works/pi-coding-agent/dist/modes/interactive/interactive-mode.d.ts` | 18-41, 128, 138, 147, 327-372, 457 | `f4beb43004161ab6d38807c8641ff3e35c77cc3adfc513141654e37947b91f0c` |
| `adapter/node_modules/@earendil-works/pi-coding-agent/dist/modes/interactive/interactive-mode.js` | 333-355, 626-762, 777-877, 2440-2635, 5651-5721 | `7dc366e8609d7d1e81fb85952e466aea9933b13e74842c2a9b9df678d267d12e` |
| `adapter/node_modules/@earendil-works/pi-coding-agent/dist/core/agent-session-runtime.d.ts` | 44-117 | `4a18c0f51669b62a297c83334731c8bb5fed58016619a14d42edce414ff8d105` |
| `adapter/node_modules/@earendil-works/pi-coding-agent/dist/core/sdk.d.ts` | 10-65, 73-107 | `fbb34394c71e113b1f1fb5dd2e3d1da8d559580b339daace88826cd1a666cc4c` |
| `adapter/node_modules/@earendil-works/pi-coding-agent/dist/utils/tools-manager.js` | 296-311 | `28e24a2022460ed4e17ac10d0766984b878c4f69eb6286efed9382b958eb2963` |
| `adapter/node_modules/@earendil-works/pi-coding-agent/package.json` | package/version/exports | `627631b613ba4ca29eba8df793f5280fd20b19f01d73826e9ffda14c15def5dc` |
| `adapter/package-lock.json` | locked dependency graph | `2055c228154c615c4f29a6422afe16b0d2372742ec9497af0406903ba37a6bf2` |
| `adapter/src/native-pi-host.ts` | host implementation | `c86f180578f72d23bee4300fea854e133e8e0d0f1be600723132902c9fc1f550` |
| `adapter/src/native-pi-host.test.ts` | native fixture/refusal tests | `ea556f52919983a210469a6db929af65984c046ca48514e0ca26b51d2d0b7387` |
| `adapter/src/native-pi-policy.ts` | private policy artifact guard/loader | `5d56df2f78b0711b1ba153a4651ea5d08e0888691e4cced1c74c94206392163b` |
| `adapter/src/native-pi-policy.test.ts` | private policy virtual-terminal tests | `381b05b4d2175aa4a595e98dc3e9dc8b7ff2fb06f77f00f100a9cbf2c24b7709` |
| `adapter/native-pi-policy/apply-patch.mjs` | reproducible private source patch | `ac7fdd684165e653714676b0144bcdc9d0f3988e11cf49cd41b5eb2fa2c47aa9` |
| `adapter/native-pi-policy/patched-interactive-mode.js` | pinned experimental output | `d48a7c98e2ee2f97ecf4b148b37098dae35fb5afe874318879c5d7d953cab953` |
| `adapter/native-pi-policy/manifest.json` | patch input/output manifest | `f71a85fc08a7b49727ab1cfd8f2c67f004624ce53593bb9cff014dcca327100d` |
| `adapter/native-pi-policy/NOTICE.md` | installed package attribution notice | `8cc829278495397e29aa41b44cda4e7a1b0c1d22a93995f67afeca9e0e851f77` |
| `adapter/node_modules/@earendil-works/pi-coding-agent/package.json` | installed package metadata declaring author/MIT/repository | `627631b613ba4ca29eba8df793f5280fd20b19f01d73826e9ffda14c15def5dc` |

These hashes identify the observed local installation and implementation only;
they are not provider, containment, or product-acceptance receipts.
