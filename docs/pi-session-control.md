# Restricted Pi session control (first slice)

This slice adds the first coherent browser-to-Pi bridge without changing the
existing read-only observability explorer. It is an implementation increment,
not evidence that governed production Pi execution is closed.

## Claim boundary

`supervisor/cmd/tbound-chat` refuses to start unless `--fixture` is supplied.
The registered fixture uses Pi `0.87.1`'s actual `createAgentSession` and
`AgentSession.prompt()` APIs, `LockedResourceLoader`, and the four existing
proxy tools (`read`, `write`, `edit`, `bash`). Its provider is the installed
Pi faux provider. It emits the registered read/write/edit/bash tool-call
sequence and final responses over multiple prompts through the actual SDK
event stream; the worker subscribes before sending each prompt. The worker
also verifies the installed `pi-coding-agent` and `pi-ai` versions are exactly
`0.87.1` and checks the registered provider, credential, resource, and tool
inventories before admitting the session.

The fixture is explicitly **non-claim-bearing**:

- no provider network request or browser credential is used;
- the worker has no Pi TUI, RPC, shell, login, model selection, reload,
  extension, skill, prompt-template, or command-expansion surface;
- fixture tool results are produced through the existing Go IPC, broker
  correlation, and gate seam, but the executor returns `effect:not-executed`;
- the fixture broker is synthetic registration evidence and must not be used as
  provider authenticity, containment, or production source evidence.

Real launch remains refused until the reviewed closure, peer-authenticated
containment, frozen offline profile, current-source verifier, durable session
admission, cleanup/settlement, and publication prerequisites are integrated.
Client JSON cannot attest to those prerequisites.

## Owned paths

- `adapter/src/sdk-worker.ts`: restricted in-process SDK worker and fixture
  provider. It accepts only a bounded line control stream and connects to the
  existing framed IPC transport for tool proposals.
- `adapter/src/sdk-worker.test.ts`: real SDK fixture-session and all-four-tool
  proxy tests.
- `supervisor/internal/sessioncontrol/`: authenticated manager, bounded
  session/turn admission, ownership, cancellation/UNKNOWN handling, event ring,
  exact Host/Origin checks, bounded JSON, and bounded SSE.
- `supervisor/cmd/tbound-chat/`: loopback-only launcher, worker parent, fixture
  broker/gate composition, embedded no-framework Pi workspace, and IPC tests.

## HTTP contract

The listener must be bound to the exact loopback host `127.0.0.1`. The handler
also compares the HTTP `Host` header exactly with the bound host value.

`GET /` is an unauthenticated static bootstrap only; it never sets an owner
cookie. Pairing is an explicit `POST /api/v1/auth` with the generated 32-byte
lowercase hex capability in the JSON body. A successful pair sets a same-origin,
HttpOnly, SameSite-Strict cookie. The API also accepts the capability as
`Authorization: Bearer ...` for a private local client. The raw capability is
never returned by an API endpoint or placed in an error/event. `--token-file`
is owner-private metadata: Unix uses mode `0600`; Windows removes inherited
ACLs and grants only the launching credential, failing closed if that cannot be
enforced. Only the token digest is suitable for diagnostics.

Cookie-authenticated POST requests require the exact Origin
`http://127.0.0.1:<port>`. Bearer-authenticated requests are intended for the
private launcher/client path and do not rely on a browser Origin header. Every
authenticated request is mapped to the server-owned local owner; no owner ID
from JSON is trusted.

| Method | Route | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/auth` | Authenticated status and server-owned owner label |
| `POST` | `/api/v1/sessions` | Admit `{ "mode": "fixture" }`; `real` is refused |
| `GET` | `/api/v1/sessions/{id}/snapshot?after=N` | Bounded session/event projection |
| `GET` | `/api/v1/sessions/{id}/events?after=N` | One bounded SSE batch, then reconnect with cursor |
| `POST` | `/api/v1/sessions/{id}/turns` | Submit `{ "text": "..." }`, max 16 KiB |
| `POST` | `/api/v1/sessions/{id}/stop` | Close admission and settle worker |

JSON is bounded, strict, and rejects unknown fields, trailing values, malformed
JSON, and oversized bodies. Sessions, turns, event count, event text, and SSE
batch size are all capped. A stale event cursor reports a gap rather than
pretending that the bounded ring is complete.

## Event and identity rules

The manager assigns event IDs, session IDs, turn IDs, and generation IDs. The
worker supplies only text/error content. Backend fixture decisions expose
linked `proposal_ref`, `decision_ref`, and `result_ref` values, with explicit
`pending`, `completed`, `denied`, or `failed` status; these are fixture
lineage, not provider authenticity. `trust_level` is assigned by the server
(`server` or `fixture`), never by the browser. Cleanup uncertainty is exposed
as session `UNKNOWN`, not silently rendered as completion.

The existing concrete `Supervisor` implementation is currently in the
`supervisor/cmd/tbound` `main` package and therefore cannot be imported by this
new command. The fixture command uses a deliberately smaller, no-effect
`supervisorAdapter` to exercise the existing `ipc`, broker-correlation, and
`gate` APIs; it is not a second production supervisor. A coordinator-owned
refactor should extract the shared proposal/result serve loop into an internal
package before any real worker is admitted. That is a required next-increment
seam, not a claim of duplicated production authority.

The embedded UI is the primary small interactive fixture workspace: it performs
explicit local pairing, launches/stops the registered fixture, submits bounded
tasks, renders SDK text deltas, and shows linked tool lifecycle references. It
renders all event content with `textContent`; hostile model or user output is
data, not HTML. It shows generation and session state while honestly marking
publication unavailable and project test/action output context unsupported. It
reconnects SSE with the last event cursor and handles explicit gap events.

## Worker and shutdown workflow

1. Start the explicit fixture launcher, for example:

   ```text
   go run ./cmd/tbound-chat --fixture --addr 127.0.0.1:8788 --token-file .tbound-chat-token
   ```

   Read the token only through the owner-private metadata channel. Do not print
   or log its contents.

2. Open `http://127.0.0.1:8788/`, enter the capability from the metadata file,
   and explicitly pair the browser. The page then admits a fixture session and
   subscribes to the bounded event projection.

3. A session manager owns the child worker. A turn is admitted only when the
   session is running, owned by the authenticated local principal, below its
   turn cap, and not already busy. Browser disconnect is observation-only after
   admission; explicit Stop closes admission and asks the parent to stop the
   worker. If transport/process cleanup does not settle with certainty, the
   session is `UNKNOWN`, not `STOPPED`.

4. The SDK worker creates the real Pi session with the locked resource loader,
   exact four-tool allowlist, in-memory settings/session state, offline model
   runtime, and `expandPromptTemplates:false` for every prompt. It subscribes
   to `message_update` before calling `prompt()` and forwards text deltas as
   bounded JSON lines.

5. Tool proposals use the existing `tbound-ipc/v1` framing and are correlated
   before gate evaluation. Fixture execution returns only bounded JSON saying
   that no effect was executed.

## Verification

Safe focused checks that do not contact a provider or start a VM:

```text
cd adapter
npm exec -- tsc --noEmit
node --experimental-strip-types --test src/sdk-worker.test.ts

cd ../supervisor
go test ./internal/sessioncontrol ./cmd/tbound-chat
```

The first increment’s tests cover unauthorized access, exact host/origin,
unknown/oversized/malformed requests, ownership, real-launch refusal,
cancellation, hostile output, bounded SSE, the actual SDK fixture stream, all
four proxy calls, the existing IPC/gate path, and replay rejection in the
fixture correlation stream. WSL/private-ext4 race evidence and the adapter’s
existing full check remain separate coordinator verification tasks.

## Independent coordinator verification — 2026-10-06

Final snapshot `/home/jeli2k/tbound-coordinator-chatbrowserfinal.O0yNav`
pins `7a80f5a` plus sessioncontrol and tbound-chat only; unfinished publication
integration is excluded. Full Linux `go test -race -count=1 ./...` exited 0,
gofmt empty, private TMPDIR 0700 on ext4. Windows exact-copy build/internal/e2e
and extra `go test -count=1 ./cmd/tbound-chat` passed (exit 0), including new
pairing-file overwrite/creation tests. Race-log SHA-256:
`13d0003230575020f1e7a2341fded039d41ade8cd7752cff8f2103dce554e746`;
pre-test source-hash listing SHA-256:
`104ce5220d5e57c98699e1d3059226a2a41842e3bf49f30e0d37609693dd755a`.

An isolated adapter archive plus the two SDK-worker source overlays passed
`npm run check` and both SDK tests. It uses pinned installed dependencies with
no installation or lockfile change. Snapshot:
`tbound-adapter-4f47685a379f432fb4f362f687e71bea`. One earlier PowerShell `-File`
array invocation omitted the SDK-test overlay; it is not counted as SDK test
acceptance. Direct script invocation corrected that and returned both exits 0.

Independent private-profile Chrome E2E paired through the real page, launched
the actual parent/Node/Pi SDK worker and submitted **four prompts**. Scripted
provider responses caused SDK read/write/edit/bash calls, then consumed the
returned fixture results. Browser and backend receipts confirmed four pending,
four completed/no-effect events with turn/proposal/decision/result links and
terminal STOPPED. Root issued no cookie; unauthenticated API returned 401;
capability DACL was protected and owner-only. Hostile input/output stayed text;
desktop and 390px mobile worked with no JS exceptions. Rendering is capped at
512 entries with bounded text and replay deduplication.

Coordinator corrections before final testing: reserve a new empty Windows
capability file with exclusive creation, enforce DACL **before writing** bytes;
bound browser rendering; show user prompts; coalesce SDK deltas into readable
messages; attach active-turn IDs from manager state. Initial browser acceptance
timed out because separate delta rows broke the expected readable response;
that revealed the rendering issue and was fixed before the final run. Earlier
partial snapshots are superseded, not presented as final source acceptance.

Allowlisted receipt: `docs/evidence/pi-sdk-fixture-2026-10-06.json`. Raw receipts
and screenshots retained in private scratch directory
`tbound-chat-e2e-2cdb78d131314ebba1e9d5f61d047fc7`. Both private Chrome profiles
were closed; the earlier fixture session was explicitly stopped before its
app process was closed. Current demo listener may remain available locally;
no active worker remained after the E2E stop. No provider key was used, exposed,
logged or transferred. The generated local pairing capability was consumed
only through the private test driver and never printed or captured in images.
Linux development Node is unavailable, so this real SDK/browser process evidence
is **Windows**, not current-source Linux guest evidence. Post-commit Go/adapter
byte comparisons are recorded in `TASK_STATE.md`.

## Next increment

The next claim-bearing slice must replace the fixture-only factory with a
reviewed isolated SDK worker and broker-owned multi-turn provider/result
lineage, then connect durable session admission and current-source
containment/verifier evidence. Until that integration is complete, the UI and
fixture are useful contract tests only; they must not be described as governed
real Pi chat.
