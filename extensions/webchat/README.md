# Optional web chat extension (`tbound/webchat`)

> **Status: frozen for the thesis.** CLI-only support for the thesis; no new
> features. The web chat will be moved to its own repository after the thesis.
> It is not part of the thesis claims.
>
> The product branch adds an optional **live mode** (`--live`, milestone P9, see
> "Live mode" below). It is product work with no thesis claim, and the fixture,
> replay and receipt modes stay labelled demonstrations.

This directory is an **optional, standalone** Go module and browser client for
the tbound web chat. tbound core does not need it: the core builds, tests,
installs and runs without this directory, and the core's build and test gate need
no Node. Nothing under `supervisor/` imports it.

## What it is

- A **React + TypeScript web app** (`frontend/`), served under `/app/`. It is the
  default UI: `/` redirects to `/app/`. Its committed build
  (`internal/webchat/dist/`) is embedded, and the `/app/` routes are derived from
  its Vite manifest. Phase 2 added the typed DOM-free core (`frontend/src/core/`)
  and the markdown AST (`frontend/src/markdown/`), each checked against the legacy
  client by parity tests.
  `docs/adr-0001-optional-extension.md` allows this frontend build, so the
  extension as a whole is not zero-build.
- A legacy vanilla browser client (`internal/webchat/assets/`, no build step), now
  **deprecated**. It is served at `/legacy/`, and `/` serves it only when the React
  app cannot be loaded. It will be removed after sign-off. The Go gateway
  (`internal/webchat/`, entrypoint `cmd/tbound-webchat/`) serves both. The module
  has no third-party Go dependencies.
- **Demonstrations are read-only.** `--demo`, `--fixture`, `--replay`, `--receipt` and
  `--receipt-demo` serve embedded fixtures, retained replay documents and imported
  post-run receipts. They have no turn submission, cancellation or browser
  authentication. `--live --socket PATH` is the live mode: it pairs one browser and
  forwards chat requests to the product service (see "Live mode").
- An **untrusted client of the supervisor.** Admission, policy, leases, effects,
  journals and publication stay in `supervisor/`. The browser and this gateway
  hold no effect authority. Success is shown only where verification exists.
- **Loopback-only.** The gateway binds a literal loopback address (default
  `127.0.0.1:8791`). In the demonstration modes it answers GET and HEAD on `/` (a
  redirect to `/app/`) and on `/favicon.ico` (`204`); every other route is GET-only.
  Other methods get `405` with an `Allow` header, and request bodies are rejected.
  Live mode adds the `POST` routes of its contract and keeps the loopback bind.

## Run the demo

From this directory (`extensions/webchat/`), with the pinned Go 1.27.1:

```sh
go run ./cmd/tbound-webchat --demo        # embedded non-claim-bearing fixture
go run ./cmd/tbound-webchat --receipt-demo
go run ./cmd/tbound-webchat --replay path.json
go run ./cmd/tbound-webchat --receipt path.json
go run ./cmd/tbound-webchat --live --socket /run/tbound-product/product.sock   # live mode, below
```

The flags `--demo`, `--fixture`, `--replay`, `--receipt`, `--receipt-demo` and
`--live` are mutually exclusive. `--socket` goes only with `--live`, and `--live` needs
it. `--listen` is the new name of `--addr`. The startup banner states the source class and
the claim boundary. Open `http://127.0.0.1:<port>/` — React UI (it redirects to
`/app/`); `/legacy/` is deprecated and will be removed after sign-off.

## Live mode (P9)

```sh
go run ./cmd/tbound-webchat --live --socket /run/tbound-product/product.sock [--listen 127.0.0.1:8791]
```

The gateway runs as your user and speaks `tbound-product-protocol/v1` to the product
service over its Unix socket, one request frame and one response frame per call. The
service sees your UID as the principal. The gateway holds no policy, lease, effect or
publication authority, never retries, and never invents a request key.

- **Pairing.** At start the gateway prints `http://127.0.0.1:PORT/pair#TOKEN` to stdout.
  The token is 32 random bytes, valid for 10 minutes, kept only as a hash, and spent by
  its first correct use. Do not keep a redirect of stdout. The pairing page posts it to
  `POST /api/v1/pair` and receives an HttpOnly, SameSite=Strict `tbound_session` cookie
  (12 hours) and a CSRF token. One gateway process pairs one browser; restart it to pair
  again.
- **Every request.** The `Host` must be the bound `127.0.0.1:PORT` or `localhost:PORT`.
  An `Origin` header, when present, must equal `http://` plus that host. Every `/api/`
  request needs the cookie, and every mutation also needs `X-TBound-CSRF`. Bodies are
  at most 64 KiB of strict JSON. No CORS header is sent. The existing security headers
  and CSP stay.
- **Routes, errors and the snapshot mapping** are in
  [`docs/web-chat-contract.md`](docs/web-chat-contract.md) section 9, with the fields the
  product protocol cannot supply (event times, for example). `GET /api/v1/sessions/{id}`
  returns the existing snapshot shape with `source: "live"` and
  `verification_scope: "structural-audit-only"`, plus a `live` field with the service's
  session view (its revision, and each turn's state).
- **Windows.** Use an SSH tunnel to the product host, with pinned host keys:
  `ssh -L 8791:127.0.0.1:8791 user@host`. Never bind a non-loopback address.

The gateway does not claim containment, `G1` or `H1`. Its qualification panel is fixed.

### The browser UI in live mode

The React app asks `GET /api/v1/contract` first. `"mode": "live"` gives the live shell. A
`401` means a live gateway that has not paired this browser, and the app shows how to
pair. Any other answer, or none, gives the read-only demonstration path as before. The
demonstration modes are labelled as such: `source: fixture` or `replay` sits beside a
`demonstration · not a live session` badge.

- **Sessions.** The rail lists `GET /api/v1/sessions` with project, provider profile and
  mode (and `read-only` or `blocked`). Choosing one opens its snapshot, then its event
  stream. A creator takes project, provider profile, publication mode, budget and
  `read_only`, and sends `POST /api/v1/sessions`. The open session is kept per tab
  (`sessionStorage`) and reopens after a reload.
- **Requests.** Every request is same-origin with credentials. Every `POST` carries
  `X-TBound-CSRF`. The token is read from `GET /api/v1/pair` on the first mutation, so a
  reloaded page recovers it from its cookie. Each user action (a prompt, a cancel, a
  decision, a new session) builds one request key. A transport failure repeats the same
  body under the same key (twice, after 0.4 s and 1.2 s). If that fails too, or the gateway
  marks the outcome unknown, the page says "Outcome unknown", keeps the draft, blocks a new
  prompt, and offers **Retry with the same request key**. A refusal such as
  `409 STALE_REVISION` is shown with its code, and the prompt is not sent again.
- **Composer.** `POST .../turns` with `expected_revision` from the `live` view of the last
  snapshot. The page reads the view again after every lifecycle event (a turn admitted,
  linked, awaiting review, resolved, finished), so the revision it sends is the one it
  shows. Send and Cancel stay in the tab order when they cannot act, and the reason is
  written beside them.
- **Event stream.** A long poll of `GET .../events?cursor=N&wait=MS`, driven by the stream
  controller the demonstration path already uses. Every connection is snapshot first and
  continues from the snapshot's cursor. A `gap` flag or an invalid item closes the stream
  and refetches the snapshot, and retries follow the existing backoff. The cursor is
  stored per session, and a reload says how many events arrived while the page was closed.
- **Review.** When a turn awaits review the page reads `GET .../candidate` and shows the
  digest and a diff per path as plain text. Publish and Discard send `POST .../resolve`
  with the digest of the candidate that is displayed. If the service has moved on it
  answers `409 CANDIDATE_MISMATCH`, and the page shows the new candidate.
- **State.** Remaining budget is budget less consumed less reserved. The state of the open
  turn, a blocked session and unknown outcomes have their own notices. The workspace's
  header and panels (Publication, Recovery, decision counts) are refreshed from the same
  read.
- **Safety.** Assistant text goes through the sanitizing markdown path and every other
  value is a text node. The panel, the review and the governance chips are separate
  regions, never part of a turn's text. No unsafe DOM sink is used (`npm run check:sinks`).

## Test

From this directory:

```sh
gofmt -l .                                   # no output
go vet -mod=readonly ./...
go test -race -mod=readonly -count=1 ./...
node --test internal/webchat/jstest/*.test.mjs
```

From the repository root, `make extension-webchat` runs the same four checks.
Node is needed only for the last one and for `make extension-webchat`. The core
`make test` target does not run them. The CI workflow
`.github/workflows/webchat-extension.yml` runs them for changes under
`extensions/webchat/**`.

Live mode is tested against a fake product service on a temporary Unix socket (the
`live*_test.go`, `socket_test.go` and `cmd/tbound-webchat/main_live_unix_test.go` files).
The wire-type parity test reads `internal/webchat/testdata/protocol/*.json`, which the
supervisor generates. After a protocol change, regenerate them from `supervisor/` with
`go test ./internal/productprotocol -run Fixtures -update` and update `wire.go`.

The React frontend has its own gate. It needs Node 24.15.0 (`frontend/.nvmrc`). The live UI
is tested with a fake gateway behind `fetch` (`frontend/src/live/*.test.ts*`,
`frontend/src/test-support/liveGateway.ts`), not with a browser. On a checkout whose path has
a space, `src/core/differential.test.ts` needs
`VITE_WEBCHAT_LEGACY_ASSETS=file:///<checkout>/extensions/webchat/internal/webchat/assets`:

```sh
make extension-webchat-frontend-verify       # npm ci --ignore-scripts, verify, dist reproducibility
```

The CSP smoke is opt-in and needs a Chrome or Chromium binary. It is not part of
any gate. Point it at a demo gateway on a fresh port:

```sh
make extension-webchat-csp CSP_URL=http://127.0.0.1:8793
```

It checks `/` (which must land on `/app/`), `/app/` and `/legacy/`, and the
header responses for each route.

## Claim boundary

- `G1=false`. Containment is `not-established`. `H1` is unexecuted.
- Fixtures, replay documents and receipt imports are **non-claim-bearing** and
  are labelled FIXTURE, REPLAY or UNAVAILABLE in the UI.
- Live mode labels its items `structural-audit-only`. That says the product service
  observed them structurally. It is not a policy, effect, containment or publication
  claim, and the qualification panel stays `G1=false`, `H1` unexecuted.
- Green tests here verify code and UI behaviour only. They establish no policy,
  effect, containment, publication, `G1` or `H1` result.

## Dependency rule

- The extension depends on the **core's service contract** only: the projection
  schema `tbound-webchat-observation/v1`, and the product protocol
  `tbound-product-protocol/v1` over the product socket (see
  `extensions/webchat/docs/web-chat-contract.md`). It keeps its own copy of the wire
  types (`internal/webchat/wire.go`), checked against JSON fixtures the supervisor
  generates. It imports no `tbound/supervisor` package and must not gain a dependency
  on one.
- The core must **never** import this extension.
  `scripts/check-extension-boundary.sh` enforces this. It is pure sh and grep,
  it runs in `make test`, and it runs in CI on every push
  (`.github/workflows/extension-boundary.yml`). The rules are:
  (a) no Go file under `supervisor/` imports or names the extension;
  (b) `supervisor/go.mod` has no require or replace for `tbound/webchat`;
  (c) the extension imports no `tbound/*` package except its own, and its
  `go.mod` names no other tbound module;
  (d) no `go.work` joins `supervisor/` with the extension.
  `sh scripts/check-extension-boundary.sh --self-test` proves each rule fails on a
  violating copy.

## Layout

- `extensions/webchat/internal/webchat/`: bounded projection contract,
  validation, gateway store, HTTP/SSE handler, post-run receipt importer,
  embedded assets (`assets/`), fixtures (`fixtures/`) and Node tests (`jstest/`).
  - `handler.go`: the document routes (`/` redirect or legacy fallback,
    `/legacy/`), the legacy `/static/` assets and the read-only API.
  - `app.go`: the `/app/` route table, derived from the Vite manifest.
  - `live.go`, `live_render.go`: live mode: pairing, cookie, CSRF, Host and Origin
    checks, the `/api/v1/` routes, and the snapshot and item rendering.
  - `wire.go`, `socket.go`: the copy of the product wire types, strict decoding, and the
    one-frame-per-connection socket client. `testdata/protocol/` holds the parity
    fixtures.
  - `dist/`: the committed React build (generated; do not edit by hand).
- `extensions/webchat/frontend/`: the React + TypeScript source (`src/`, with
  `src/core/` and `src/markdown/` from Phase 2, and `src/live/`, the live client, event
  stream, controller and components), its scripts (`scripts/`), the
  pinned `package.json` and lockfile, and `DEPENDENCIES.md`. Build output goes to
  `internal/webchat/dist/`.
- `extensions/webchat/cmd/tbound-webchat/`: loopback-only gateway entrypoint (the
  demonstration modes and `--live`).
- `extensions/webchat/scripts/csp-smoke.mjs`: opt-in headless-browser CSP check.
- `extensions/webchat/docs/`:
  - `web-chat.md`: design, authority and claim boundary, verification log.
  - `web-chat-contract.md`: the service contract. Its section 9 is what live mode
    implements.
  - `web-chat-reasoning-records.md`: proposal to show the model's reasoning (not implemented).
  - `web-chat-ui-plan.md`: design analysis and the harness survey (Appendix A
    and B).
  - `adr-0001-optional-extension.md`: decision record for the extension boundary
    and the Phase 1+ frontend toolchain direction.
  - `phase2-parity.md`: the Phase 2a parity evidence for `frontend/src/core/`.
  - `phase2-markdown.md`: the Phase 2b markdown AST (`frontend/src/markdown/`).
  - `design/`: `design-language.md`, the visual-language spec, with the
    `contrast.mjs` script and its output.
    Desktop frames: buttons with a matching frame jump to it; the governance tabs swap in place.

Paths in these documents are repository-root-relative, for example
`extensions/webchat/internal/webchat/`. Bare file names such as `store.mjs`
refer to `extensions/webchat/internal/webchat/assets/`.
