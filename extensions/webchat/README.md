# Optional web chat extension (`tbound/webchat`)

This directory is an **optional, standalone** Go module and browser client for
the tbound web chat. tbound core does not need it: the core builds, tests,
installs and runs without this directory, and the core's build and test gate need
no Node. Nothing under `supervisor/` imports it.

## What it is

- A legacy vanilla browser client (`internal/webchat/assets/`, no build step)
  served by a Go gateway (`internal/webchat/`, entrypoint `cmd/tbound-webchat/`).
  The module has no third-party Go dependencies.
- A **React + TypeScript scaffold** (Phase 1, `frontend/`) served under `/app/`.
  It renders a placeholder, not the product UI. Its committed build
  (`internal/webchat/dist/`) is embedded, and the `/app/` routes are derived from
  its Vite manifest. Phase 2 added the typed DOM-free core (`frontend/src/core/`)
  and the markdown AST (`frontend/src/markdown/`), each checked against the legacy
  client by parity tests. The product UI is Phase 3, which is pending. The
  vanilla client at `/` remains the default until Phase 3 parity. See
  `docs/frontend.md`.
  `docs/adr-0001-optional-extension.md` allows this frontend build, so the
  extension as a whole is not zero-build.
- **Read-only today.** It serves embedded fixtures, retained replay documents
  and imported post-run receipts. `--live` fails closed. There is no turn
  submission, cancellation or browser authentication yet.
- An **untrusted client of the supervisor.** Admission, policy, leases, effects,
  journals and publication stay in `supervisor/`. The browser and this gateway
  hold no effect authority. Success is shown only where verification exists.
- **Loopback-only.** The gateway binds a literal loopback address (default
  `127.0.0.1:8791`), serves GET only and rejects request bodies.

## Run the demo

From this directory (`extensions/webchat/`), with the pinned Go 1.27.1:

```sh
go run ./cmd/tbound-webchat --demo        # embedded non-claim-bearing fixture
go run ./cmd/tbound-webchat --receipt-demo
go run ./cmd/tbound-webchat --replay path.json
go run ./cmd/tbound-webchat --receipt path.json
go run ./cmd/tbound-webchat --live        # refused: supervisor service absent
```

The flags `--demo`, `--fixture`, `--replay`, `--receipt`, `--receipt-demo` and
`--live` are mutually exclusive. The startup banner states the source class and
the claim boundary. Open `http://127.0.0.1:8791/` (vanilla UI) or
`http://127.0.0.1:8791/app/` (React scaffold, Phase 1).

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

The React frontend has its own gate. It needs Node 24.15.0 (`frontend/.nvmrc`):

```sh
make extension-webchat-frontend-verify       # npm ci --ignore-scripts, verify, dist reproducibility
```

The CSP smoke is opt-in and needs a Chrome or Chromium binary. It is not part of
any gate. Point it at a demo gateway on a fresh port:

```sh
make extension-webchat-csp CSP_URL=http://127.0.0.1:8793
```

`docs/frontend.md` covers the build, the pins, the manifest-derived `/app/`
allowlist, the CSP smoke and its exit codes.

## Claim boundary

- `G1=false`. Containment is `not-established`. `H1` is unexecuted.
- Fixtures, replay documents and receipt imports are **non-claim-bearing** and
  are labelled FIXTURE, REPLAY or UNAVAILABLE in the UI.
- Green tests here verify code and UI behaviour only. They establish no policy,
  effect, containment, publication, `G1` or `H1` result.

## Dependency rule

- The extension depends on the **core's service contract** only: the projection
  schema `tbound-webchat-observation/v1` today, and an authenticated supervisor
  API later (see `extensions/webchat/docs/web-chat-contract.md`). It imports no `tbound/supervisor`
  package and must not gain a dependency on one.
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
  - `app.go`: the `/app/` route table, derived from the Vite manifest.
  - `dist/`: the committed React build (generated; do not edit by hand).
- `extensions/webchat/frontend/`: the React + TypeScript source (`src/`, with
  `src/core/` and `src/markdown/` from Phase 2), its scripts (`scripts/`), the
  pinned `package.json` and lockfile, and `DEPENDENCIES.md`. Build output goes to
  `internal/webchat/dist/`.
- `extensions/webchat/cmd/tbound-webchat/`: loopback-only gateway entrypoint.
- `extensions/webchat/scripts/csp-smoke.mjs`: opt-in headless-browser CSP check.
- `extensions/webchat/docs/`:
  - `frontend.md`: the React scaffold, pins, committed build, the `/app/`
    allowlist, the CSP smoke, and the Phase 2 and 3 plan.
  - `web-chat.md`: design, authority and claim boundary, verification log.
  - `web-chat-contract.md`: proposed supervisor-owned service contract.
  - `web-chat-ui-plan.md`: design analysis and the harness survey (Appendix A
    and B).
  - `adr-0001-optional-extension.md`: decision record for the extension boundary
    and the Phase 1+ frontend toolchain direction.
  - `phase2-parity.md`: the Phase 2a parity evidence for `frontend/src/core/`.
  - `phase2-markdown.md`: the Phase 2b markdown AST (`frontend/src/markdown/`).
  - `design/`: `design-language.md`, the visual-language spec, with the
    `contrast.mjs` script and its output.
  - `webchat/`: mockups and QA captures. The QA JSON receipts keep the paths and
    hashes they were captured with, so they describe the pre-extraction tree.
  - `webchat/mockups/tbound-redesign-v2.html`: the structural reference for the
    layout until Phase 3 completes. Its visual language is superseded by
    `design/design-language.md`.

Paths in these documents are repository-root-relative, for example
`extensions/webchat/internal/webchat/`. Bare file names such as `store.mjs`
refer to `extensions/webchat/internal/webchat/assets/`.
