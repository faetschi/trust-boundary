# ADR-0001: The web chat is an optional standalone extension; frontend toolchain direction

- Status: **accepted** for the extension boundary (Phase 0). The Phase 1+
  toolchain direction is accepted as a direction. **Phase 1 (scaffold) is
  implemented and committed in `263b6ca`.** The review fixes made after that
  commit are committed in `2583f5a`. §(v) lists them with their done criteria and
  receipts. **Phase 2 (typed core port and markdown AST) is committed in
  `4700ca5`.** Phase 3 is pending.
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
default until P3 reaches parity.

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
| **P1 scaffold** | **implemented and committed in `263b6ca`; review fixes committed in `2583f5a`; one criterion partly met** | see below | see below |
| P2 port pure logic | implemented and committed in `4700ca5` | `src/core` and `src/markdown` port the logic; legacy parity nets | `phase2-parity.md` §9; `phase2-markdown.md`, Receipts |
| P3 React views | pending, not committed | — | — |

P1 done criteria, each with its evidence:

- **Vite, React, TypeScript, CSS Modules scaffold.** Met. `npm run verify`
  (typecheck, Vitest, build, and the sink and dependency gates, each with its
  self-test) exits 0 in the final run below. The Vitest count there (24 files,
  313 tests) includes the `src/core` and `src/markdown` tests of the Phase 2 work
  (committed since in `4700ca5`), which the same chain typechecks and scans.
- **`dist/` committed with a manifest; Go allowlist derived from it and tested.**
  Met. `app.go` derives the `/app/` routes from `.vite/manifest.json`, and the
  shell is read by a tokenizer that fails closed. Eight `TestApp*` tests and
  `TestContentSecurityPolicyConstantIsPinned` pass. Mutations: a lookup of the
  decoded instead of the escaped path is caught by the traversal test
  (`status=200, want 404`), and skipping `authorizeRequest` for `/app/` fails
  `TestAppRefusesForeignHostAndOrigin`.
- **Headless-browser test enforcing `style-src 'self'` with zero CSP violations.**
  **Partly met.** `scripts/csp-smoke.mjs` checks three things. A committed canary
  page with an inline script and an inline style must produce CSP violations, or
  the observer is treated as blind and the run fails with exit 5. The policy must
  be on seven header probes, read from `handler.go`. The Document response and
  the console of `/app/` and `/` must be clean. It passed a live run (receipts
  below). It is opt-in and manual (CI `workflow_dispatch` job). No gate requires
  it, and it is not run on each push.
- **CI check that rebuilding `dist/` reproduces the committed output with the
  pinned Node and Vite.** Met locally; the `frontend` job also succeeded on
  GitHub at `4700ca5` (run 38580790848). The `frontend` job in
  `webchat-extension.yml` runs `npm run repro` before any build, then
  `npm run verify`, then `git status` on `dist/`. The repro reads the committed
  `dist/` from git HEAD, so it cannot compare a build with itself. Node is read
  from `frontend/.nvmrc` (24.15.0) in every job, and Vite is 8.3.4. Local
  evidence is below.

Receipts (exact commands; exit codes; the working tree at the time of the run;
Node 24.15.0; npm 11.12.1 in WSL):

| Command | Where | Exit | Result |
|---|---|---|---|
Review fixes (committed in `2583f5a`):

- The `/app/` shell is read by a tokenizer that fails closed (single, unquoted and
  spaced attributes, `srcset`, comments, raw text, `<base>`, `<meta http-equiv>`,
  inline scripts). The test reads the shell the same way. `/app/` now has
  foreign-Host and foreign-Origin tests, and the 404, 405 and 503 answers are
  checked for the full header set.
- The CSP string is the `contentSecurityPolicy` constant in `handler.go`. The
  smoke reads it from there and checks it on seven header probes and on the
  Document response of each page.
- `repro-check.mjs` compares a clean rebuild with the `dist/` committed at HEAD,
  read from git before any build. The `verify` and `repro` order is fixed in the
  Makefile and in CI.
- `frontend/.gitattributes` keeps every build input LF, with a binary guard.
- `check-sinks.mjs` scans every non-test file under `src/` and `index.html` with a
  wider rule set and a self-test, wired into `verify`. `check-deps.mjs` requires a
  sha512 integrity and the exact registry tarball on every entry. The one
  fsevents exception is keyed on its lock identity, not on installation, and it
  has a self-test. Both self-tests run inside `verify`.
- `csp-smoke.mjs` has a committed canary (the negative control), DevTools command
  deadlines, signal-safe cleanup, a pinned Go version check, and a vanilla-page
  check of the title, the claim strip and the tabs.
- The workflow pins its three actions to full commit SHAs, reads Node from
  `frontend/.nvmrc` in every job, and runs `repro` before `verify` in the
  `frontend` job.

These receipts are for the working tree after the review fixes. Node is 24.15.0 and
npm 11.12.1 in WSL, with Go 1.27.1 in WSL. The Windows runs use Node 24.15.0, Go
1.27.1 and Chrome 155.0.8059.79. "Throwaway" means a copy under a temporary
directory, outside the worktree and its git repository. The live repository was
only read. Each row's command was run from the stated directory, with
`GIT_DIR` and `GIT_WORK_TREE` set for WSL (see `frontend.md`, Commands).

| Command | Where | Exit | Result |
|---|---|---|---|
| `npm run verify` (typecheck, test, build, check:sinks, check:deps) | WSL, `frontend/`, final tree | 0 | typecheck ok; Vitest 24 files, 313 tests passed; build; `check:sinks` self-test 12 rules, 27 violating fixtures flagged, 2 clean fixtures pass, then the scan: 25 files, 0 findings; `check:deps` self-test 26 cases (23 rejected, 3 accepted), then the tree: 8 direct, 88 locked, 44 installed checked against the lock, fsevents not installed on this platform |
| `npm run repro` | WSL, final tree | 0 | `working-tree dist/ matches HEAD before this run`; two clean builds identical; the rebuild equals the committed `dist/` at HEAD `263b6ca`; tree sha256 `85f7686c45dca416c00df825623098c7687492fd08982125dfb105ecf401add8` (4 files), the same digest as the P1 run |
| same two-step pipeline after an unbuilt source edit, **old** `repro-check.mjs` from HEAD | throwaway copy | 0 (bug) | passed: verify had built the edit, and the old gate compared the build with that build |
| same pipeline, **new** `repro-check.mjs` | throwaway copy | 1 | `the clean rebuild differs from the committed dist/ at HEAD 263b6ca: .vite/manifest.json, assets/index-8XGvVAW-.js, assets/index-Djq9eTqX.js, index.html`; a note reported that the working-tree `dist/` differed from HEAD |
| same pipeline, edit reverted (positive control) | throwaway copy | 0 | the rebuild equals HEAD again |
| `npm ci --ignore-scripts && npm run repro` in a clone with `core.autocrlf=true`, **without** `frontend/.gitattributes` | throwaway clone | 1 | 18 CRLF lines in `App.tsx`; the CRLF sources change the built asset names and hashes (`index-C0Otf54I.css`, `index-EOqaBF5r.js` appear in place of the committed names) |
| same clone shape, **with** `frontend/.gitattributes` committed | throwaway clone | 0 | 0 CRLF lines; the rebuild equals HEAD; tree sha256 `85f7686c…add8` |
| `check-sinks.mjs`, **old** scanner from HEAD, on a probe with six bypass classes and a `data-src` script | throwaway copy | 0 (bug) | passed all of them |
| same probe, **new** scanner | throwaway copy | 1 | 9 findings: `srcdoc`, `.style.`, `cssText`, `createElement("iframe"`, `setAttribute('style')`, `setTimeout(`, `target="_blank"` without `noopener`, computed key `'innerHTML'`, and `index-script-src` for `data-src` |
| `check-sinks.mjs --self-test` with the `script-eval` rule disabled (mutation) | throwaway copy | 1 | 3 `script-eval` fixtures not flagged |
| `check-deps.mjs --self-test` with the fsevents manifest comparison disabled (mutation) | throwaway copy | 1 | 2 evil-fsevents fixtures accepted (an evil install script, an added postinstall) |
| `check-deps.mjs`, macOS-like tree with the pinned fsevents installed: **old**, then **new** | throwaway copies | 1 (bug), 0 | the old gate failed the pinned install, because it keyed the exception on "not installed"; the new gate accepts exactly the pinned identity |
| `check-deps.mjs` with react's integrity deleted; react resolved to another package's tarball; react aliased (`name: evil`): **old**, then **new** | throwaway copies | 0 (bug), 1 | the old gate accepted all three; the new gate fails each one |
| `check-deps.mjs` with an installed fsevents whose manifest gained a postinstall: **old**, then **new** | throwaway copies | 1, 1 | the old gate fails it for the wrong reason (it fails any installed fsevents); the new gate fails it because the scripts differ from the pinned object |
| `gofmt -l .` | WSL, `extensions/webchat`, final tree | 0 | no output |
| `go build ./...`, `go vet -mod=readonly ./...` | WSL, `extensions/webchat` | 0, 0 | — |
| `go test -race -mod=readonly -count=1 -v ./...` | WSL, final tree | 0 | 39 top-level PASS (36 before, plus `TestAppRefusesForeignHostAndOrigin`, `TestAppShellFailsClosedOnAnyUnadmittedURL` and `TestContentSecurityPolicyConstantIsPinned`), 56 subtests PASS, 0 FAIL, 0 SKIP |
| `go test -race ... -run TestApp` with `authorizeRequest` skipped for `/app/` (mutation) | throwaway copy | 1 | `TestAppRefusesForeignHostAndOrigin` fails: 22 `status=200, want 403` cases |
| `node --test internal/webchat/jstest/*.test.mjs` | WSL, final tree | 0 | 258 tests, 258 pass, 0 fail, 0 skipped |
| `sh scripts/check-extension-boundary.sh`, then `… --self-test` | WSL, repo root | 0, 0 | boundary OK; self-test 8 of 8 cases |
| `node scripts/csp-smoke.mjs` (no `--url`: builds the gateway with Go 1.27.1 and starts it on a free port) | Windows Node 24.15.0, Chrome 155.0.8059.79, final tree | 0 | observer live (the canary produced 4 CSP violations); 7 header probes pass; `/app/`: 0 CSP violations, 0 console errors (favicon 404 allowed: 1), 0 `style` and 0 `on*` attributes, CSS Module class applied; `/`: 0 CSP violations, 0 console errors, 0 `style` and 0 `on*` attributes; the claim strip and the tabs present |
| `node scripts/csp-smoke.mjs` **of the HEAD script** against a copy of the gateway with the policy header removed | Windows, throwaway copy | 0 (bug) | PASS on both pages, though no response carried the policy |
| `node scripts/csp-smoke.mjs` against the same copy | Windows, throwaway copy | 1 | observer live; `FAIL header probe` on all 7 probes; the Document policy absent on both pages |
| `node scripts/csp-smoke.mjs` with the observer disabled (hook and log listener removed) | Windows, throwaway copy | 5 | `FAIL: observer is blind: the canary page … produced 0 CSP violations`; the run's temp directories were removed |
| live run, **before** the cleanup fix, then **after** it (the fix: wait for the browser to exit, kill any process naming the profile, retry the removal for 15 s, and report a failed removal) | Windows | 0, 0 | before: each run left one `tbound-csp-profile-*` directory of about 270 files, because `rmSync` got EPERM right after `taskkill`; after: no directory left and no browser process naming the profile |
| DevTools failure path (`--chrome C:\Windows\System32\notepad.exe`: no DevTools endpoint) | Windows | 4 | `NOT VERIFIED: browser … did not open a DevTools endpoint`; no temp directory remained, and no notepad process started by the run remained |
| `node scripts/csp-smoke.mjs --bogus`; `--url http://127.0.0.1:8791`; `--url http://10.0.0.1:8793` | WSL | 2, 2, 2 | usage error; stale port; non-loopback address |
| `node scripts/csp-smoke.mjs` (no browser on PATH) | WSL | 3 | `NOT VERIFIED: no browser` |
| `node scripts/csp-smoke.mjs --chrome /bin/true` with a `go` 1.26.0 first on PATH | WSL | 4 | `the gateway build needs the pinned Go 1.27.x; PATH has "go version go1.26.0 linux/amd64"` |
| `node scripts/csp-smoke.mjs --chrome /bin/true --url http://127.0.0.1:8793` (nothing listening) | WSL | 4 | `NOT VERIFIED: no gateway answered` |
| `.github/workflows/webchat-extension.yml` parsed (Python `yaml.safe_load`) | WSL | 0 | jobs `webchat`, `frontend`, `csp-smoke`; all 8 `uses:` references pinned to full commit SHAs: `actions/checkout` v4.4.0 `11d5960a…`, `actions/setup-go` v5.6.0 `40f1582b…`, `actions/setup-node` v4.4.0 `49933ea5…`; `permissions: contents: read`. Observed later: the `webchat` and `frontend` jobs succeeded on GitHub at `4700ca5` (run 38580790848); `csp-smoke` is dispatch-only and has not run. |
| `make …` targets | — | not run | `make` is not installed in WSL or Windows. The Makefile edits were checked by reading them and by line endings (86 of 86 CRLF lines kept). |
| `npm audit`, `npm audit --omit=dev`; `go build ./... && go vet ./...` in `supervisor/` | — | not rerun | not rerun in this pass. The P1 record in this file at `263b6ca` (git history) is the last run. No dependency changed in this pass. |

Source hashes (sha256, at the time of the runs above):

```
1911fc7df821db6c3ab0156b29fcf84fba08d3b859e6d5a782ca303038d0f9d1  extensions/webchat/internal/webchat/app.go
937fe37cbf22fb11decedd027853ed0786c2ae0756f7a829c59c04987a83e91a  extensions/webchat/internal/webchat/app_test.go
9c2dfc607c84722e32ef88b343900edf4e7484a0ae778bf854f63128538a72e7  extensions/webchat/internal/webchat/handler.go
7edf7fc4c9020430636afc8f17826be98e43c2601634022c8ad8868a45ba83dd  extensions/webchat/cmd/tbound-webchat/main.go
2c065ff22389b4e196b0ac490e970a4c5fd86c963dbb4f03a47c9d9c6f92047e  extensions/webchat/frontend/scripts/check-sinks.mjs
9ffa6ee570f96f247cf88688b4702d3f3ec63a3e704af66d8ec86b0330375e4f  extensions/webchat/frontend/scripts/check-deps.mjs
f56331d37c0370b4a1f246233bd93d122acf78603dab687f0388a750a2bcdc83  extensions/webchat/frontend/scripts/repro-check.mjs
4cb6ca7f2b2cef2edf461cc4728c921c512a8a9127aa83012d83d4fe8b21ebe0  extensions/webchat/scripts/csp-smoke.mjs
f95687b5ccd880025e1c9f374ba2f1809d0dd5bf8c12f1ccdd0ec8b0f654e48f  extensions/webchat/frontend/.gitattributes
7ee98cb272df15251c5d393a0cea88a46cd2d430fde3e3c85a88e53ffc7a5654  extensions/webchat/frontend/package.json
391c83682bf06e2107452efdbc18e87e5a3eb77912fd3980fc9063c5f1dab722  Makefile
62c4e2f84597bfdea2270f73c2b61db5b2f9e794060d6ee1cb883f5ad717e943  .github/workflows/webchat-extension.yml
5039168c6e86801b005206e3ffe70138b515a9d43513469f19f98b6743849830  extensions/webchat/frontend/DEPENDENCIES.md
```

Not verified here: the GitHub Actions runs (since observed at `4700ca5`: `webchat`
and `frontend` succeeded; the `csp-smoke` job has never run); `make` (not installed, so the Makefile
targets were not run); an npm audit and the supervisor build (not rerun in this
pass); a browser other than Chrome 155, and Chrome only on Windows (WSL has no
browser); a build under Node versions other than 24.15.0; a macOS install of
fsevents (the macOS case uses a simulated tree, not a Mac); and the CRLF case,
which was simulated with `core.autocrlf=true` in a Linux clone, not a Windows
checkout. Also not exercised by any run: the SIGINT, SIGTERM and SIGHUP handlers,
and the DevTools command deadlines (the code is in place, but no run forced a
timeout).

Known gaps in the gates, not claims:

- `check-sinks.mjs` does not analyse a property key built from variables at run
  time. It also excludes `src/**/test-support/` (test-only fixtures, not shipped),
  with a guard against any non-test import.
- `check-deps.mjs` checks the lock and the installed tree. It does not verify
  tarball contents against their integrity, since npm does that at install.

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
