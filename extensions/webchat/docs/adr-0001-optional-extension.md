# ADR-0001: The web chat is an optional standalone extension; frontend toolchain direction

- Status: **accepted** for the extension boundary (Phase 0). The Phase 1+
  toolchain direction is accepted as a direction. **Phase 1 (scaffold) is
  implemented** in the working tree and not yet committed; its done criteria and
  receipts are in §(v). Phases 2 and 3 have not started.
- Date: 2026-10-14
- Scope: `extensions/webchat/` and its relation to `supervisor/`.
- Claim boundary: this is an engineering decision. It establishes no `G1`,
  containment or `H1` result. `G1=false`, containment is `not-established`, and
  `H1` is unexecuted.

## Context

The web chat was a zero-build ESM and CSS client served by a Go gateway. It sat
inside the core module (formerly `supervisor/internal/webchat` and
`supervisor/cmd/tbound-webchat`).
Nothing in the core imported it, but the core test, release and review paths
still carried it. The frontend will now grow (a build step, typed state, a
component model), so it needs a boundary that keeps the core free of it.

## Decision

### (i) Optional standalone extension

- The web chat lives in `extensions/webchat/`, as its own Go module
  `tbound/webchat`. It has no third-party Go dependencies.
- **Dependency direction: extension → core contract only.** The extension talks
  to the supervisor through its authenticated service contract
  (`extensions/webchat/docs/web-chat-contract.md`). It never imports a `tbound/supervisor` package.
  The core never imports the extension.
- tbound core builds, tests, installs and runs **without** the extension. Its
  build and test gate needs **no** Node. The Pi harness the core launches is a
  separate Node-based runtime, and this change does not alter it. The core
  `make test` target runs the boundary guard
  (`scripts/check-extension-boundary.sh`, pure sh and grep). Core release
  artifacts build only the core commands, so the extension is not in them.
- The guard, its self-test and its CI job are described in
  `extensions/webchat/README.md`.

### (ii) Phase 1+ frontend toolchain direction

- **React + TypeScript with the project's own components.** No third-party
  component library is a dependency. shadcn/ui, coss/ui and Radix are not used.
- **CSS Modules compiled at build time to static, same-origin `.css` files.**
  The Content-Security-Policy is `style-src 'self'`. There are no inline `style`
  attributes and no runtime style injection (no CSS-in-JS, no `<style>` created
  by a component).
- **Vite build output committed under `extensions/webchat/internal/webchat/dist/`**
  (embedded by the Go gateway), with a manifest. The Go closed-asset allowlist is derived from that manifest and
  tested, so the gateway serves exactly the built files and nothing else.
- **Node is needed only to change the UI.** Running tbound or the Go gateway
  with the committed `dist/` needs no Node.
- Overlays use the platform primitives: native `<dialog>`, the Popover API and
  `<details>`/`<summary>`. No overlay library that injects styles.
- The existing rules hold: zero `innerHTML`, `outerHTML`, `insertAdjacentHTML`
  or `document.write` sinks; the DOM is built with `createElement` and
  `createTextNode`; markdown has no HTML parser.

### (iii) Evidence

The evidence is recorded in `extensions/webchat/docs/web-chat-ui-plan.md`, Appendix A (harness
survey at `d743267`) and Appendix B. The items below were reported in review. The
ones marked *not re-verified* were not re-measured in this change.

- **DeepSeek harness** uses an in-house React component set, CSS Modules and
  lightningcss. It uses no third-party component library, and coss is not in its
  stack. *Reported in review; partly measured in Appendix A.*
- **DeepSeek client packages** are `0.2.1-alpha.2`, not `0.0.1-rc.1`.
  `ui-primitives` peer-depends on `@deepseek-ai/cordis`. *Reported in review; the
  version correction is applied in Appendix A.*
- **shadcn/ui** is Tailwind v4 on Base UI. Base UI's ScrollArea and the Select
  `alignItemWithTrigger` path inject `<style>` elements, which conflicts with
  `style-src 'self'`. Whether `react-remove-scroll` injects styles under CSP is
  **not verified**.
- **coss/ui** licence is **unresolved**. Aggregators list AGPL-3.0; the upstream
  licence was not checked. Its components are built for human-chat and admin
  semantics, not for a governance ledger. *Not re-verified.*
- **Consequence:** a third-party component library would bring style injection
  and an open licence question into the CSP-bound client. Own components with
  build-time CSS Modules keep `style-src 'self'` and avoid both.

### (iv) What this supersedes and what it keeps

**Superseded:** the zero-build stance. This covers `extensions/webchat/docs/web-chat.md` §8.1
("zero-build ESM + CSS"), the "Keep" list in `extensions/webchat/docs/web-chat-ui-plan.md` §4, the
"no build step" recommendation in Appendix A.4, and the design-system header in
`extensions/webchat/internal/webchat/assets/styles.css`. The zero-build client remains the shipped
state until P1 lands.

**Kept, unchanged by this decision:**

- The authority boundary. The browser and the gateway are untrusted. There is no
  effect authority in the client and no per-tool approval path.
- The claim boundary. Fixtures are non-claim-bearing. Success is shown only when
  verified, and the no-success-without-verification gate stays.
- The closed asset allowlist test. In P1 its inputs come from the manifest.
- Zero `innerHTML` sinks, and the decision not to adopt the harness runtime
  (Appendix A.4).
- GET-only, literal-loopback gateway until the authenticated service exists.

### (v) Phase plan

Each phase is shippable on its own. No phase may be called verified without a
receipt (command, exit code, source hash). The extension phases (P) are separate
from the UI phases in `extensions/webchat/docs/web-chat-ui-plan.md` §5.

- **P0 — extraction (this change).** Move the web chat to `extensions/webchat/`,
  add the boundary guard and the CI jobs, and record this ADR. No behaviour
  change.
- **P1 — scaffold.** Add the Vite, React, TypeScript and CSS Modules skeleton.
  Commit `dist/` with a manifest, and derive and test the Go closed-asset
  allowlist from it. Add a headless-browser test that enforces `style-src 'self'`
  with zero CSP violations, and a CI check that rebuilding `dist/` reproduces the
  committed output with the pinned Node and Vite versions.
- **P2 — port the pure logic to TypeScript.** Port `store`, `stream`, `backoff`,
  `markdown`, `filters`, `trajectory`, `anchor`, `format` and `stop-sequence`.
  The existing Node tests (258) are the parity net. Behaviour may not change.
- **P3 — React views.** Replace `render.mjs` and `app.mjs` with React components.
  Use native `<dialog>`, Popover and `<details>` for overlays.

Status (2026-10-14). Each phase is verified only by the receipts listed here.

| Phase | Status | Done criteria | Receipts |
|---|---|---|---|
| P0 extraction | done, committed `18f71e6` | extension boundary, guard, CI job | `scripts/check-extension-boundary.sh` (see README) |
| **P1 scaffold** | **implemented, not committed; one criterion partly met** | see below | see below |
| P2 port pure logic | not started | — | — |
| P3 React views | not started | — | — |

P1 done criteria, each with its evidence:

- **Vite, React, TypeScript, CSS Modules scaffold.** Met. `npm run verify`
  (typecheck, two component tests, build, sink and dependency gates) exits 0.
- **`dist/` committed with a manifest; Go allowlist derived from it and tested.**
  Met. `app.go` derives the `/app/` routes from `.vite/manifest.json`. Six
  `TestApp*` tests pass. A mutation that looks up the decoded instead of the
  escaped path is caught by the traversal test (`status=200, want 404`).
- **Headless-browser test enforcing `style-src 'self'` with zero CSP violations.**
  **Partly met.** `scripts/csp-smoke.mjs` exists and passed once against a demo
  gateway. It is opt-in and manual (CI `workflow_dispatch` job). No gate requires
  it, and it is not run on each push.
- **CI check that rebuilding `dist/` reproduces the committed output with the
  pinned Node and Vite.** Met locally, CI unobserved. The `frontend` job in
  `webchat-extension.yml` runs `npm run repro` and `git status` on `dist/`, with
  Node read from `frontend/.nvmrc` (24.15.0) and Vite 8.3.4. The job has not run
  on GitHub. Local evidence is below.

Receipts (exact commands; exit codes; the working tree at the time of the run;
Node 24.15.0; npm 11.12.1 in WSL):

| Command | Where | Exit | Result |
|---|---|---|---|
| `npm ci --ignore-scripts` | WSL, `frontend/`, clean `node_modules` | 0 | 88 locked packages |
| `npm run verify` | WSL | 0 | typecheck ok; test 2/2; build; `check:sinks` 0 sinks; `check:deps` 0 install scripts, 1 platform-gated exception (fsevents) |
| `npm run repro` | WSL | 0 | two clean builds identical; committed `dist/` identical; tree sha256 `85f7686c45dca416c00df825623098c7687492fd08982125dfb105ecf401add8` (4 files) |
| same build on Windows | Windows Node 24.15.0, temp copy | 0 | all 4 files identical by sha256 |
| `npm audit`, `npm audit --omit=dev` | WSL | 0, 0 | 0 vulnerabilities |
| `gofmt -l .` | WSL, `extensions/webchat` | 0 | no output |
| `go build ./...` | WSL | 0 | — |
| `go vet -mod=readonly ./...` | WSL | 0 | — |
| `go test -race -mod=readonly -count=1 -v ./...` | WSL | 0 | 36 top-level PASS (30 existing + 6 new), 0 FAIL, 0 SKIP |
| `node --test internal/webchat/jstest/*.test.mjs` | WSL | 0 | 258 pass, 0 fail, 0 skipped |
| `sh scripts/check-extension-boundary.sh` | WSL, repo root | 0 | boundary OK |
| `… --self-test` | WSL | 0 | 8 of 8 cases |
| `go build ./... && go vet ./...` | WSL, `supervisor/` | 0, 0 | — |
| `node scripts/csp-smoke.mjs --url http://127.0.0.1:8793` | Windows Node 24.15.0 → Chrome 155.0.8059.79; gateway in WSL `--demo`, built from the final source | 0 | PASS `/app/` and `/`: 0 CSP violations, 0 console errors (favicon 404 allowed), 0 `style` attributes, 0 `on*` attributes; banner CSS Module class applied |
| `node scripts/csp-smoke.mjs` (no `--url`) | WSL (no browser installed) | 3 | `NOT VERIFIED: no browser` |
| negative control (throwaway server, same CSP header, deliberately broken pages) | Windows | 1 | FAIL on both pages: CSP violations, `style=`, `onclick=`, console error, uncaught exception |
| `make …` targets | — | not run | `make` is not installed in WSL or Windows; each target's command was run directly above |

Source hashes (sha256 of the Go files at the time of the `go test` run above):

```
2213858f23934a37bb3def22c69f847e7274402441d97fa30f462ff3064024f1  internal/webchat/app.go
09c5c95b11831505bf58d9db0cdc5dca570a5e00f10b96b1a7267622ae6b0011  internal/webchat/app_test.go
7ce9a693874aa509d5ea18c2143c7af4dc1e794dc3f222f4c0378563025382e1  internal/webchat/handler.go
7edf7fc4c9020430636afc8f17826be98e43c2601634022c8ad8868a45ba83dd  cmd/tbound-webchat/main.go
```

Not verified here: the GitHub Actions runs, a browser other than Chrome 155, and
the reproducibility of a build under Node versions other than 24.15.0.

## Consequences

- Node becomes a build-time dependency for UI changes only. The core gate does
  not change.
- `dist/` is generated output and must be reproducible. This is a P1 requirement,
  not a P0 claim.
- The extension's CI job (`.github/workflows/webchat-extension.yml`) and the
  boundary job (`.github/workflows/extension-boundary.yml`) are separate from the
  core `linux-ci` jobs.

## Alternatives considered

| Option | Result |
|---|---|
| Keep the zero-build client | Simplest. But the state model and the view layer would keep growing without types or a component model. Rejected for Phase 1+. |
| Adopt shadcn/ui, coss/ui or Base UI | Injects styles and has an unresolved licence. Rejected under the CSP. |
| Adopt the DeepSeek harness client | Second runtime, Node host as the server, and a trust-direction inversion (Appendix A.3). Rejected. |
| **Own components, CSS Modules, committed Vite build (chosen)** | Keeps `style-src 'self'`, keeps the Go gateway as the only server, and makes Node a build-time tool only. |
