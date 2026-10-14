# Web chat frontend (React client)

Status at HEAD `57e6406` (2026-10-14):

- **The React client is the default UI.** `/` answers `302` to `/app/` (GET and HEAD).
  The vanilla UI is deprecated at `/legacy/` until the owner signs off its removal.
  `/` serves the legacy page only if the React build cannot be loaded (see the last section).
- **Phases 1 to 3 are committed.** P1 scaffold `263b6ca` (review fixes `2583f5a`); P2 typed
  logic port `4700ca5`; P3 React views `58492c5`, `57bcd95`, `a2aaf30`, `60ca5b0`, `6d03657`
  and `57e6406`. The decision is in `adr-0001-optional-extension.md`.
- **Final QA fix round (2026-10-14, uncommitted):** the Final browser QA's findings are addressed. First paint and
  the lossy wording are fixed, the ALLOW shape and the stale lines are fixed, and the backoff timing is confirmed. The
  browser QA passes 9 of 9 against a Linux-built gateway. See the last section.
- **Verified on Linux (WSL, Go 1.27.1, Node 24.15.0):** vitest 896/896 (60 files, 0 skipped);
  extension Go `-race` 54 top-level PASS, 0 SKIP; legacy node tests 258/258; sink and
  dependency gates with their self-tests; boundary guard; the Linux rebuild equals the
  committed `dist/`. Receipts: "Claim gate in the core" and "Default UI switch" below.
- **Open:** no screen-reader testing; the narrow drawer scrolls with the page (not pinned);
  stream `complete` wording (owner decision); legacy removal (owner sign-off). The favicon
  allowance is closed: the final QA fix round removed it (see its section below).
- Sections below that say "scaffold" or "not committed" describe the state when they were
  written. Where a later commit changed that state, the section says so.

The Phase 1 scaffold rendered one placeholder heading and the build version, and it proved
the toolchain: TSX, CSS Modules extracted to a static `.css` file, a build-time version
string, and the no-inline-style rule. *(Superseded by `58492c5`: the placeholder is replaced
by the Phase 3a shell. The toolchain checks still apply.)*

## Layout

- `extensions/webchat/frontend/`: the source (`src/`), the scripts
  (`scripts/`), the pinned `package.json` and `package-lock.json`, `.npmrc`
  (`ignore-scripts=true`), `.nvmrc` (24.15.0), `.gitattributes` (LF for every
  build input) and `DEPENDENCIES.md`.
- `extensions/webchat/internal/webchat/dist/`: the committed build output. The
  Go gateway embeds it with `//go:embed all:dist`, including the `.vite/`
  manifest.
- `extensions/webchat/scripts/csp-smoke.mjs`: the opt-in browser CSP check.

## Commands

Run from `extensions/webchat/frontend/` with Node 24.15.0 (`.nvmrc`):

```sh
npm ci --ignore-scripts      # install exactly the lockfile, no install scripts
npm run verify               # typecheck, tests, build, sink check, dependency check
npm run repro                # two clean builds, compared with each other and with the dist/ committed at HEAD
```

`npm run repro` reads the committed `dist/` from git HEAD before it builds
anything. So `npm run verify && npm run repro` cannot compare a build with
itself: verify's build overwrites `dist/`, and HEAD is never overwritten. Run
`repro` first, as CI does. A WSL worktree of a Windows checkout cannot be read by
WSL git, because the `.git` file points at a Windows path. There, set
`GIT_DIR` and `GIT_WORK_TREE` to the worktree's git directory. If git cannot read
HEAD, the gate exits 1 and says so.

From the repository root, the same chain runs as one target:

```sh
make extension-webchat-frontend-verify
```

That target is not a core gate. The core `make test` and the core release never
need Node. Only the extension's own targets and its CI job do.

The Go side needs no Node:

```sh
cd extensions/webchat
go build ./...
go test -race -mod=readonly -count=1 ./...
```

## Why `dist/` is committed

- The Go module embeds the build. A `go build` or a core release therefore
  needs no Node toolchain, which keeps the core gate unchanged.
- The reviewed bytes are the bytes that are served. A reviewer reads
  `internal/webchat/dist/` in the diff.
- The reproducibility gate (`npm run repro`, also the CI job) fails when a clean
  rebuild does not match the `dist/` committed at git HEAD. A stale or
  hand-edited file cannot pass unnoticed. Its caveat: the gate checks HEAD, not
  the working tree. A `dist/` change must be committed before the gate can pass.
  An uncommitted `dist/` is reported as a note, not as a failure.

Build inputs are LF on every checkout platform. `frontend/.gitattributes` sets
`* text eol=lf`, and `internal/webchat/.gitattributes` pins `dist/**` to LF. The
CRLF case is real and was reproduced: with `core.autocrlf=true` and no source
attributes, a clone's `npm ci && npm run repro` exited 1, because CRLF sources
changed the built asset names and contents. With the attributes committed, the
same clone reproduced the committed `dist/` (ADR receipts). The P1 record in the
ADR at `263b6ca` also recorded one Windows build of the same sources that matched
byte for byte.

## Version pins

Every direct dependency is pinned to an exact version. The list and the
licence census are in `frontend/DEPENDENCIES.md`.

| Package | Version |
|---|---|
| react, react-dom | 19.3.0 |
| vite | 8.3.4 |
| @vitejs/plugin-react | 6.1.2 |
| vitest | 5.0.4 |
| typescript | 7.0.2 (native compiler, `tsc`) |
| @types/react, @types/react-dom | 19.3.0 |

Node: `.nvmrc` 24.15.0, read by every CI job. `engines` in `package.json` is
`^22.12.0 || ^24.0.0 || >=26.0.0`, which is Vitest 5's range. The lock entry for
Vite 8.3.4 requires `^20.19.0 || >=22.12.0`. The `.nvmrc` version satisfies both
ranges, and `engines` is not changed here.

## Vite configuration and what it guarantees

`frontend/vite.config.ts` sets these options. Each one protects the closed
allowlist and the CSP.

- `base: '/app/'`: the app is served under `/app/` and nowhere else.
- `build.outDir: '../internal/webchat/dist'`, `emptyOutDir: true`.
- `build.manifest: true`: writes `.vite/manifest.json`, which the gateway
  derives its routes from.
- `build.assetsInlineLimit: 0`: no assets are inlined as `data:` URLs.
- `build.modulePreload.polyfill: false`: Vite emits no inline `<script>`.
- `build.sourcemap: false`: no `.map` files are produced or served.
- `frontend/public/theme-boot.js` is copied unchanged to `dist/theme-boot.js`. Vite
  rewrites the shell's `src="/theme-boot.js"` to `/app/theme-boot.js`. The gateway admits
  that one load (see step 7 of the allowlist).
- `__APP_VERSION__` comes from `package.json`, never a timestamp. The output
  depends only on the source bytes, which `frontend/.gitattributes` keeps LF,
  and on the pinned toolchain.

The generated `index.html` has one classic `<script src="/app/theme-boot.js">` first in
`<head>`, then one `<script type="module" src="/app/assets/…">` and one
`<link rel="stylesheet" href="/app/assets/…">`, with no inline script or style. CSS Modules
compile to hashed class names in the static `.css` file.

## The /app/ allowlist is derived from the manifest

The route table is built once at startup (`internal/webchat/app.go`,
`deriveAppRoutes`). It is not a list written by hand.

1. Read `.vite/manifest.json`. A missing or corrupt manifest is an error.
2. Start from the manifest entries marked `isEntry`. The entry for `index.html`
   must exist.
3. Follow `css`, `assets`, `imports` and `dynamicImports` transitively. A
   reference to a key that has no entry is an error.
4. Admit only `index.html` and paths that match
   `^assets/[A-Za-z0-9][A-Za-z0-9._-]*\.(js|css)$`. Any other file type is an
   error, so the gateway will not serve an image, a font or a source map by
   accident.
5. Every admitted file must exist in the build. Its type comes from the
   extension: `text/html`, `text/javascript` or `text/css`, all with
   `charset=utf-8`.
6. Every URL attribute in `index.html` must be an admitted route. The shell is
   read with a tokenizer that follows the browser's rules for quoted, single-quoted
   and unquoted values, whitespace around `=`, repeated attributes, `srcset`,
   comments and raw-text bodies. Each `src`, `href` and other URL attribute is
   checked, and `srcset` is checked candidate by candidate. Derivation fails on
   an unadmitted URL, on a tag the tokenizer cannot read, on a `<base>`, on a
   `<meta http-equiv>`, and on an inline `<script>` (no `src`, or a body). A
   `data-src` is not a `src`. `on*` and `style` attributes are not checked here.
   The test checks them.
7. The theme script is the one file outside the manifest. It is `theme-boot.js`,
   copied from `public/`, and the browser runs it before the first paint. The
   derivation admits `/app/theme-boot.js` only when the shell loads it as its first
   `<script>`: a classic script whose only attribute is `src`, before `<body>`, and
   as its only reference (`admitBootScript`). The file must be a regular file in the
   build. Any other load of it fails the derivation, and so does a missing file. A
   file beside it, or a path under it, stays 404, and so does the script itself when
   the shell does not load it.

Files outside that set are never served. A file present in `dist/` but not
reachable from the manifest is not served. A test enforces this, and a second
test fails if `dist/` contains a file that is neither served nor the manifest.

Requests are matched by exact map lookup on the escaped request path. Nothing is
joined to a file system path, nothing is normalised, and nothing is listed. So
these all return 404: `..` and `.` segments, `//`, a percent-encoded `/`, a
percent-encoded letter in a real name, double-encoded dots, a trailing slash,
and `/app` without the slash. The shell is served at `/app/` only, not at
`/app/index.html`.

When derivation fails, `/app/` answers 503 with the reason. Every other path
under `/app/` is 404. `/` then serves the legacy workspace with a fallback notice
instead of redirecting (see the last section), and the API keeps working.
`tbound-webchat` prints a `note:` on stderr with the same reason at startup.

The response headers are the same on every route. The
Content-Security-Policy string is unchanged:

```
default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'
```

The string is the `contentSecurityPolicy` constant in `handler.go`, and every
response sets it. `app_test.go` pins the constant, and `csp-smoke.mjs` reads it
from `handler.go`, so a change to the policy is one edit in one place.

## Tests

Go, in `internal/webchat/app_test.go`:

- manifest-derived routes are served with the exact content type, the full
  header set, and the exact file bytes; POST is still 405, and the 404, 405 and
  503 answers carry the full header set too;
- a foreign Host or Origin is 403 on `/app/`, on every app file and on the
  embedded shell, with the full header set. A mutation that skips
  `authorizeRequest` for `/app/` fails this test;
- the policy constant in `handler.go` equals the pinned string;
- a synthetic build serves only the manifest-reachable files, and the
  unreachable `assets/orphan.js`, the manifest itself and other files are 404;
- traversal, dot-segment, percent-encoding and double-encoding attempts are 404,
  with a positive control in the same test;
- missing, corrupt, dangling, unsupported and traversing manifests, and a shell
  that references an unlisted file, make `/app/` 503 with the reason while `/`
  and `/api/v1/contract` still return 200;
- a shell with an unadmitted URL in any spelling fails the derivation: single
  quotes, no quotes, whitespace around `=`, uppercase names, a duplicate `src`, a
  slash before an attribute, `srcset`, an entity-encoded path, a `javascript:`
  URL, a `<base>`, a `<meta http-equiv>`, an inline `<script>` (with a body, or
  with `data-src` instead of `src`), an unterminated tag, quote or comment. The
  same URLs inside a comment are inert, and admitted URLs in each spelling pass;
- the shell has no inline `<script>` body, no `<style>`, no `style=`, no `on*=`
  handler and no `javascript:` URL, and every URL is a same-origin `/app/` route.
  The test reads the shell with the same tokenizer as the derivation;
- the stylesheet has no `@import` and no external or `data:` `url(`;
- every file in the embedded build is served or is the manifest.

Frontend, in `frontend/src/`: `App.test.tsx` renders the component to a string
with `react-dom/server` and checks the heading, the build version, that the
banner has a class, and that there is no `style=` or `on*=` attribute.

Checks outside the tests:

- `scripts/check-sinks.mjs` reads every non-test file under `src/` (`.ts`,
  `.tsx`, `.js`, `.jsx`, `.mjs`, `.cjs`, `.css`, `.html`) and `index.html`,
  whole-file, so a sink split over lines is found. It fails on `innerHTML`,
  `outerHTML`, `insertAdjacentHTML`, `setHTMLUnsafe`, `parseFromString`,
  `document.write`, `srcdoc`, a `style` attribute in any quoting, `style={`, a
  style object, `el.style.x` or `el.style =` writes, `cssText`, `<style>`,
  `<iframe>`, `<object>`, `<base>` and `<script>` elements (and the same tags
  passed to `createElement`), `setAttribute` with a name that is not a string
  literal or is `style`, `srcdoc` or `on*`, `eval`, `new Function`, a string
  passed to a timer, `javascript:` and `vbscript:` URLs, `target="_blank"`
  without `noopener` on the same element, a computed key built from string
  literals that spells a sink (`el['inner' + 'HTML']`), and in `index.html` a
  `<script>` without a non-empty `src`, a `<script>` with a body, a `style` or
  `on*` attribute, and a `<base>`. Excluded, and printed in the output: files named
  `*.test.*`, and files under `src/**/test-support/`, which are test fixtures and
  DOM shims. A guard fails the scan if any other file imports from `test-support`.
  The self-test runs first, in `npm run verify`. It proves each rule on a
  violating fixture and a clean file. The limit: a key built from a variable at
  run time is not analysed.
- `scripts/check-deps.mjs` fails on a non-exact pin, a missing `ignore-scripts`,
  a root that does not match `package.json`, a lock entry that is not a
  registry tarball of its own name (an alias, a `resolved` URL other than the
  exact tarball URL, or no sha512), an install script except the pinned
  darwin-only `fsevents` (see `frontend/DEPENDENCIES.md`), and an installed
  package whose name or version differs from the lock, that declares an install
  hook, that is not in the lock, or that is a symbolic link. Its self-test builds
  26 simulated trees: nothing installed, the pinned fsevents installed, and
  tampered variants. Each tampered variant must fail, and each clean one must pass.

## Opt-in browser CSP smoke

`scripts/csp-smoke.mjs` is not part of `go test`, `make test` or any core gate.
It uses Node built-ins only and Node 22 or later for the global WebSocket. It
drives a headless Chrome, Chromium or Edge over the DevTools protocol, with a
temporary profile. The browser and its temporary directories are removed when the
run ends, on success and on failure (both checked). SIGINT, SIGTERM and SIGHUP run
the same cleanup, but no run exercised them. Every DevTools command has a
15-second deadline, so a browser that stops answering ends the run as NOT
VERIFIED. No run forced a timeout either.

```sh
# against a demo gateway already running on a fresh port (not 8791)
make extension-webchat-csp CSP_URL=http://127.0.0.1:8793

# or let the script build the gateway and start it on a free port
make extension-webchat-csp
```

Without `CSP_URL`, the script builds the gateway with `go` from `PATH`. That must
be the pinned go1.27.x, and the script prints the `go version` line it used. Any
other version stops the run as NOT VERIFIED.

The browser comes from `CHROME_BIN`, then `--chrome`, then the usual Chrome,
Chromium and Edge install paths and `PATH`. It must be a Chromium-family binary.
A WSL checkout has no browser by default. Run the smoke from Windows, either
against a gateway running in WSL (reachable at `127.0.0.1` through WSL's localhost
forwarding) or with its own build, as in the recorded runs.

Each run, in order:

1. **Observer self-test: the negative control, committed.** The script serves a
   canary page with the gateway's policy. The page has an inline `<script>` and an
   inline `style` attribute, and the policy must refuse both. The browser must
   report at least one CSP violation for it. If it reports none, the observer is
   blind, and the run stops with exit `5` before any gateway page is judged. The
   canary is the only negative control in the tree. An earlier throwaway control,
   which also covered `onclick=`, `console.error` and a thrown exception, is not
   in the tree, and this check does not claim it.
2. **Header probes.** Over plain HTTP, sixteen responses must carry the policy, the
   expected status and the expected `Location`. `GET`, `HEAD` and `GET /?x=//evil.com`
   on `/` are 302 to `/app/`, and no other probe is a redirect. The rest are
   `POST /` (405), `GET /api/v1/contract`, `GET /app/` (200), a missing `/app/`
   asset (404), `POST /app/` (405), `GET /legacy/` (200), `POST /legacy/` (405), an
   unknown path (404), and a foreign `Host` or `Origin` (403) on `/`, `/app/` and
   `/legacy/`. The policy is read from the `contentSecurityPolicy` constant in
   `handler.go`. `routes_test.go` checks that the routes this script audits are the
   Go route table, with the same kind and landing.
3. **Browser audit of `/`, `/app/` and `/legacy/`.** `/` must land on `/app/`. For
   each page, the Document response's
   `Content-Security-Policy`, as the browser received it, must equal the policy.
   There must be zero CSP violations (from `securitypolicyviolation` events and
   `security`-source console entries), zero console errors and zero uncaught
   exceptions. There is no favicon allowance. *(Superseded: this text allowed one 404
   for `/favicon.ico`. The final QA fix round removed that allowance. `/favicon.ico`
   answers 204, so a favicon 404 is a console error and fails the run.)* There must be
   zero `style` attributes and zero `on*` attributes.
   `/app/` (and `/`, which lands there) must render its banner with a CSS Modules
   class whose computed background differs from a default probe element, and a
   linked stylesheet. `/legacy/` must show the "Deprecated: legacy workspace" notice,
   and have its title, the claim strip, the chat view, and the chat and trajectory
   tabs.

Exit codes: `0` PASS; `1` a check failed; `2` usage error (including port 8791,
which is a stale build); `3` `NOT VERIFIED: no browser`; `4` `NOT VERIFIED`: the
gateway, the Go toolchain or the DevTools connection was unavailable; `5` `FAIL:
observer is blind`. A PASS is printed only when every check passed.

The mutation runs and the live run are recorded with their commands in
`adr-0001-optional-extension.md`, §(v).

## Phase plan

- **Phase 1 (committed in `263b6ca`, review fixes in `2583f5a`):** the scaffold, the manifest-derived `/app/` route,
  the tests, the reproducibility gate, and the opt-in CSP smoke. No product UI.
  The vanilla UI was unchanged and remained the default. *(Superseded by `6d03657`:
  the React client is the default at `/`.)*
- **Phase 2 (committed in `4700ca5`):** port the pure logic to TypeScript: `store`,
  `stream`, `backoff`, `markdown`, `filters`, `trajectory`, `anchor`, `format` and
  `stop-sequence`. The existing 258 Node tests are the parity net, and behaviour
  may not change. Evidence: `phase2-parity.md` and `phase2-markdown.md`.
  The Go allowlist keeps deriving from the manifest, so no hand-written route is
  added.
- **Phase 3 (committed in `58492c5`, `57bcd95`, `a2aaf30`, `60ca5b0`, `6d03657` and
  `57e6406`):** replace `render.mjs` and `app.mjs` with React components, with native
  `<dialog>`, Popover and `<details>` for overlays. Parity is shown by the browser QA
  captures and the CSP smoke before `/` switches to the React client. The switch is
  done in `6d03657` (see "Default UI switch"): `/` redirects to `/app/`. The vanilla
  assets stay under `/static/` and `/legacy/` until sign-off, and are removed only
  after it.

## Claim boundary

This is an engineering client. It establishes no `G1`, containment or `H1`
result. `G1=false`, containment is `not-established`, and `H1` is unexecuted. The
UI shows no policy, effect or publication result that the projection does not
carry, and fixtures and replays are labelled non-claim-bearing. Passing checks here
verify code and UI wiring only.

## Verification receipts

The exact commands, exit codes and hashes for Phase 1 are in
`adr-0001-optional-extension.md`, §(v), the P1 status table (the restored `263b6ca`
rows are historical). The Phase 2 receipts are in `phase2-parity.md` §9 and
`phase2-markdown.md`. The Phase 3 receipts are in the Phase 3 sections of this file
and in `phase3-parity.md`. On GitHub, the `webchat` and `frontend` jobs succeeded at
`4700ca5` (run 38580790848). The dispatch-only `csp-smoke` job has not run there.
No GitHub Actions run for the later commits (`58492c5` to `57e6406`) is recorded in
these docs, so the Linux and Windows receipts in the sections below are the
verification of record.

## Phase 2a addendum: `src/core/` (DOM-free logic)

`frontend/src/core/` holds the pure logic ported from `internal/webchat/assets/`
in strict TypeScript. Nothing imports it yet, so `dist/` is unchanged. The
evidence and the parity table are in `phase2-parity.md`.

- **Contents.** `contract.ts` (typed observation contract and closed vocabularies),
  `validate.ts` (the strict runtime check that mirrors `validate.go`; the only
  place untrusted JSON becomes a typed value), and the ports of `format`, `filters`,
  `store`, `trajectory`, `anchor`, `backoff`, `stream`, `stop-sequence` and `api`.
  `testing/` holds test-only helpers. `markdown` is not here; it lives in
  `src/markdown/`.
- **Purity rule.** No file in `src/core` imports `react` or `react-dom`, or references
  `document`, `window`, `navigator` or storage as a value. Modules do not call browser
  or timer globals directly (`setTimeout`, `fetch`, `EventSource`,
  `AbortController`); those come in through injected interfaces. `check-purity.test.ts`
  enforces this on every `npm run test`.
- **Commands** (from `frontend/`, Node 24.15.0):

  ```sh
  npm run typecheck    # tsc over src/, including src/core, strict + noUncheckedIndexedAccess
  npm run test         # vitest run: src/core parity, fixtures, boundary, purity, differential
  npx vitest run src/core   # the core alone
  ```

- **Parity net.** The legacy suite still runs (`node --test internal/webchat/jstest/*.test.mjs`,
  258 tests). The core suite ports its 159 tests for the ported modules. The
  differential test loads the legacy modules and compares them with the ports on
  seeded corpora. `VITE_WEBCHAT_LEGACY_ASSETS` points it at another copy for a mutation
  check.
- **Claim boundary.** `UNKNOWN` stays `UNKNOWN`. A policy `ALLOW` is a permission and
  is never shown as a success. An unverified success is shown as `reported`, and
  verified success needs a `VerifiedEvidence` value that only `verifyIn` creates (core `verification.ts`).

## Phase 3a: the React foundation (2026-10-14)

The foundation is the shell that the Chat and Trajectory views (3b, 3c) sit in. `/` still
served the vanilla UI at this commit. *(Superseded by `6d03657`: `/` now redirects to `/app/`.)*

**What it contains.**

- Tokens: `src/styles/tokens.css` (three tiers, `design/design-language.md` section 7) and
  `src/styles/base.css` (reset only, no colour value). Theme: `src/theme/` writes a
  `data-theme` attribute on `<html>`. `bootstrap.ts` applies a stored choice before the first
  render. The choices are System, Light (the default) and Dark.
- Primitives in `src/ui/`: `Chip`, `Disclosure`, `Kbd`, `Notice`, `StateDot`, `StateMessage`
  (loading, empty and error states, none with a live role), `Tabs` (roving tab stop; a
  `TabPanel` mounts only its selected children), `ToggleGroup`, `VisuallyHidden`,
  `LiveRegion` with `announcer.ts` (the one polite announcer).
- The workspace: `src/app/workspaceStore.ts` holds the core state and publishes an immutable
  `WorkspaceModel` after each change. `WorkspaceProvider.tsx` owns one store for the page, and
  the stream opens in an effect and closes on unmount. `browser.ts` is the only module that
  reads browser globals.
- The shell: `Shell.tsx` (skip link to the visible view, header with the source and connection
  badges, the claim boundary, theme and governance controls, the sessions rail, the main
  workspace, the inspector, the footer and the live region), with `Rail.tsx`, `Footer.tsx`,
  `GovernancePanels.tsx` and `Inspector.tsx`. The inspector is a column at wide widths and an
  off-canvas drawer at or below the breakpoint in `layout.ts`.
- Pure readings: `panels.ts` (governance panel words and tones) and `status.ts` (connection
  words, the gap and window notices, the announcement rules).

**Hooks and props.**

- `useWorkspace(): WorkspaceModel` re-renders only when the store publishes. `model.actions`
  has `setView`, `setGroup`, `setQuery`, `setFilter`, `selectRecord`, `clearRecord` and
  `repinTail` (see Phase 3 polish).
- `useShell(): ShellControls` (`shellContext.ts`) gives `narrow`, `drawerOpen`,
  `openInspector`, `closeInspector`, `selectRecord(id, row)` and `clearRecord`. Only the shell
  provides it.
- `useNarrow()` (`layout.ts`), `useAnnouncer()` (`ui/LiveRegion.tsx`) and
  `useTheme()` (`theme/useTheme.ts`, which returns the choice and its setter).
- `WorkspaceProvider({ deps? })` takes `WorkspaceDeps` (fetch, event source, timers, online
  state, clock). The app omits it and gets `browserDeps()`. `<Shell version>` takes the build
  version.

**How to test.** The tests run in happy-dom under Vitest. `test-support/fakes.ts` provides
`fakeWorkspace(snapshot)`: a fake fetch, event source, timer set and clock. Its
`sources[0].emit(...)` sends stream events, and `reply` sets the next snapshot.
`test-support/dom.tsx` provides `mount`, `click`, `settle` and `installMatchMedia(width)`.
Run `npx vitest run src/app` in `frontend/`, or the Windows gate with `-Filter src/app`. The
legacy test that each row covers is in `phase3-parity.md`.

**Not verified by the tests.** Layout, focus rings on screen, forced colours and the no-flash
theme start. The browser captures from 3b and 3c are the only layout evidence. They do not
cover forced colours, and no no-flash check is recorded. Screen-reader output is not tested.

## Phase 3b: the React Chat view (2026-10-14)

The Chat view replaces its placeholder, and the markdown AST has a React mapping. The
Shell and the Trajectory view are not part of this change, and `/` still serves the
vanilla UI. *(Superseded by `6d03657`: `/` now redirects to `/app/`.)*

**Built.**

- `src/markdown/react.tsx`: the AST to React mapping. It is an exhaustive switch that
  ends in `assertNever`, with no raw-HTML property and no style value. A link is an
  anchor only for a `SafeHref`, and its scheme is checked again. Each top-level block
  is memoised and keyed by position, so a block whose node object is unchanged is not
  rendered again. The classes `md-body`, `md-code` and `md-truncated` are global
  (`src/markdown/markdown.css`), because the parity test pins them.
- `src/app/views/ChatView.tsx` with `src/app/views/chat/`: the posture badges, the filter
  toolbar, the notices, the `role="log"` transcript with `aria-live="off"`, the loading,
  error, empty and no-match states, and the Jump control. `readings.ts` holds the
  wording and tones, `tail.ts` the tail window, `Transcript.tsx` and `ToolCard.tsx` the
  records, and `Chat.module.css` the styles.

**Honesty rules,** asserted on the DOM in `ChatView.test.tsx` and on the readings in
`chat/readings.test.ts`:

- ALLOW has the `allow` tone and a note that says it is a permission. It is never the
  `ok` tone.
- A success reads `succeeded` only with `VerifiedEvidence` from `verifyIn`, in a live
  structural-audit projection. Otherwise it reads `reported`.
- UNKNOWN, lost, unknown, pending, failed and denied each keep their own label, and none
  of them is in the success tone.
- Lossy output is a chip. A verified lossy success reads `succeeded (output lossy, not
  confirmed)` on the unknown tone, never the success tone (corrected in the 2026-10-14
  review fixes; the first 3b build drew it in the success tone).
- An unverified record is marked explicitly. A uniformly unverified view says so once, and a
  mixed view marks each record (Phase 3 polish).
- The tail count comes only from `store.applied`. Gaps, duplicates and snapshot loads
  add nothing to it.
- The tail pin is kept by the workspace store (`repinTail`, `tailPending`), so a switch to the
  Trajectory view and back keeps the count (Phase 3 polish).
- Announcements: one debounced announcement of 400 ms, through the shell's announcer.

**Decisions to confirm.**

1. The filters are the four legacy ones: All, Denied, Unknown and Failed. "Reported"
   in the brief is a posture badge, not a filter.
2. Filter counts use `countFilter`, which has the legacy meaning. Denied counts DENY
   decisions and denied results. `governanceCounts` feeds the posture badges.
3. The verified gate needs the projection (live and structural-audit) and the item's
   evidence. This is stricter than the legacy item-only check, and it makes no difference
   to the demo. Corrected in the 2026-10-14 review fixes: the Trajectory view applied only
   the item's evidence, so a fixture projection with a streamed observed record read it as
   verified. Every view now asks the one gate in `app/verification.ts`.
4. The gap, window and eviction notices render inside the transcript. The Shell also
   renders the same three notices above the tabs, so two copies appear. (Resolved in Phase 3
   polish: one copy, above the tabs, and a pointer at the top of the transcript.)
5. A tool card's `data-anchor` is on its wrapper, not on the disclosure element.
6. Turn sections are regions, not headings, so the Shell's heading order is unchanged.

**Open.**

- Duplicate notices, as in decision 4. Resolved in Phase 3 polish (below).
- `ui/LiveRegion.test.tsx`, "is the only aria-live or live role in the application
  source", failed at the 3b build. Resolved in the 3b/3c pass (commit 57bcd95): the scan
  allows `role="log"` only in the same tag as `aria-live="off"`, and that exemption has its
  own self-test in the same file. See the gates note under Phase 3c.
- The Jump control was not exercised in Chrome. The demo has no live arrivals, so its
  count could not be shown. It is covered in happy-dom with stubbed page metrics only.
- This file has no Phase 3a section, although `phase3-parity.md` refers to one. Resolved:
  see "Phase 3a" above.

**Receipts (Windows; WSL was not used).** The rows are in `phase3-parity.md`.

- Typecheck: `tsc` exit 0. Vitest: 720 tests, 719 passing, 1 failing (the LiveRegion
  scan), 0 skipped. Sinks: self-test OK, 0 findings in 82 files. Deps: OK.
- The 3b suites: `markdown/react.test.tsx` 80, `app/views/chat/readings.test.ts` 19 and
  `app/views/ChatView.test.tsx` 39. All pass, with 0 skipped.
- Go: `vet` and `test` exit 0, gofmt clean.
- `node scripts/csp-smoke.mjs --url http://127.0.0.1:8821`: PASS, with the observer
  live. Chrome 155.0.8059.79.
- Screenshots: `docs/webchat/qa/p3b-chat-{1440,1024,390}-{light,dark}.png`, all with a
  tool card expanded. Their hashes and the source and dist tree hashes are in
  `docs/webchat/qa/p3b-chat-receipt.json`.
- Keyboard, in Chrome, at 1440 light: the Tab order runs skip link, theme, view tab,
  filter, then the transcript. Enter opens a disclosure and Space closes it. Arrow keys
  move between the filters without changing the pressed one, and Enter presses it. The
  receipt records no CSP violation, and the only console error is the favicon 404, which
  is allowed.

## Phase 3c: the React Trajectory view and the record inspector (2026-10-14)

The Trajectory view is a React view over the same bounded window as Chat. Grouping and
search are presentation: they never change the projection, a verdict or the timeline's
finding filter. The rules are the ones in `web-chat.md` §4.4 and §4.5.

- **Files.** `app/views/TrajectoryView.tsx` (the composition, the focus contract, the
  drawer). `app/views/trajectory/`: `Toolbar.tsx`, `SequenceRail.tsx`, `Ledger.tsx`,
  `RecordRow.tsx`, `useTailWindow.ts`, `Trajectory.module.css`. `app/inspector/`:
  `RecordInspector.tsx` and its CSS, and `recordChip.ts` (the chip tone and status-word
  rules, pure). The shell files, `Inspector.tsx` and the chat view are unchanged. Two
  existing assertions in `app/Shell.test.tsx` and `app/Inspector.test.tsx` checked the
  Phase 3c placeholder text, so they now check the real view and body.
- **Tests.** `app/views/TrajectoryView.test.tsx` (37 tests, mounted in the real shell and
  its store), `app/inspector/RecordInspector.test.tsx` (13) and
  `app/inspector/recordChip.test.ts` (7). The parity rows are in `phase3-parity.md`.
- **Toolbar.** Turn, Tool and Verdict form the shared toggle group (one tab stop, arrows
  move focus, Enter presses). The legacy hint for the pressed group is visible text under
  it. The search field is labelled and reads only the allowlisted fields of
  `core/trajectory.searchRecords`. The summary reads `N of M record(s) matching …` while
  a search is active.
- **Sequence rail.** Built from every retained record, never from the search result. Its
  three lanes are Model, Policy and Tools. Each track is an image whose name states the
  tone of every segment in words. Segments are weight classes, not inline styles. The
  axis note says the axis is record sequence, not time, and while a search is active it
  says the rail shows all N retained records.
- **Ledger.** Groups are named regions. Each row is a button with `aria-pressed`. Its
  accessible name carries the status in words (the dot is decorative), followed by the
  payload preview. The ledger has one tab stop, on the last row that had focus, else the
  selected row, else the first. ArrowUp and ArrowDown move between rows across group
  boundaries, and Home and End go to the ends. Enter and Space select, as native buttons
  do. The container is a named region, not a list.
- **Chips and dots.** An unverified success reads `reported` in its chip, never the
  confirmed word. A verified lossy success reads `succeeded (output lossy, not confirmed)`
  on the unknown tone, and the record's name adds `output lossy, not counted as confirmed`
  (corrected in the 2026-10-14 review fixes; the 3c build read `not confirmed` alone). LOST
  is drawn in the unknown family with its own shape, never as a DENY (also a review fix:
  the 3c rail and chip drew it in the deny tone). Other states read as the core names them.
  A policy ALLOW is never drawn in the success tone.
- **Tail.** `N new record(s) · Jump to latest` sits at the bottom of the view. Its count
  comes only from `store.applied` through `core/anchor.ts`. The pin moves only while the
  reader is at the tail, and the first load does not follow. A settled burst is announced
  once through the status region, after 400 ms. Jump scrolls to the end and moves focus
  to the ledger.
- **Focus contract.** A row calls `selectRecord(id, row)`. At the drawer breakpoint the
  shell opens the drawer and moves focus into it. Escape and Close return focus to the
  row. At wide widths the shell has no return target, so the view returns focus to the
  row when the selection is cleared and no live element holds focus. When a record is
  evicted, the store clears the selection and the view moves focus to the ledger, or
  closes an open drawer through the shell.
- **Record inspector.** A header with the source chip and the position, then the status
  in words, then the core's sections: Summary, Payload (when the record has one),
  Decision, Result, Evidence and Timing. Timing is always present and says that no
  duration is recorded. A policy digest is shown shortened, with the full value in its
  `title`. Close is the shell's `Close record` control in the aside's head, so the body
  has no second Close control.

### Deviations from the legacy UI

These are behaviour changes, not gaps.

1. The aside is named `Record inspector` while a record is shown, as the legacy UI names
   it. The 3c build kept `Governance inspector` there; Phase 3 polish changed it.
2. The group hints are visible text. The toggle primitive has no per-button name, so the
   legacy per-button label is not reproduced.
3. The ledger has one tab stop (a roving stop), not one per row.
4. A row's accessible name includes its payload preview. The legacy name omitted it, so a
   screen reader heard the status but not the payload.
5. The digest's full value is in its `title` only. There is no hidden copy.
6. The tail pin did not survive a view switch, because `TabPanel` unmounts the inactive view
   (`ui/Tabs.tsx`). Resolved in Phase 3 polish: the pin is kept by the workspace store.

### Not verified here

- The happy-dom tests do not turn Enter into a click on a button, so they assert the click
  path. The browser walk below did press Enter.
- Scroll anchoring for records that arrive while the reader is away is left to the browser's
  own anchoring. The browser walk did not drive a live stream, so the tail count was
  checked by the unit tests only.
- Screen-reader output is not tested.
- On first load of `/app/` at 390 px the page started at `scrollY` 825, with the Chat view
  showing. Resolved in Phase 3 polish (cause and receipt below). The 390 px captures from
  3c scrolled to the top before the capture, which hid it.

### Gates (Windows gate runner; WSL not used)

Each line is a Windows run at this tree. Exit codes are from `gate.ps1` or the command shown.

| Gate | Command | Exit | Result |
|---|---|---|---|
| typecheck | `gate.ps1 -Task typecheck` | 0 | `tsc --noEmit` clean |
| app tests | `gate.ps1 -Task test -Filter src/app` | 0 | 184 passed, 0 skipped |
| full tests | `gate.ps1 -Task test` | 1 | 719 passed, 1 failed, 0 skipped (below) |
| sinks | `gate.ps1 -Task sinks` | 0 | 0 findings, 82 files |
| verify | `gate.ps1 -Task verify` | 1 | typecheck, sinks and deps pass; `test` fails as below |
| build | `gate.ps1 -Task build` | 0 | one fresh `dist/` copied back; its hashes are in the QA receipt |
| go | `gate.ps1 -Task go` | 0 | `go vet`, `go test` ok for both packages, `gofmt -l` empty |
| CSP smoke | `node scripts/csp-smoke.mjs --url http://127.0.0.1:8822` | 0 | PASS: 7 header probes, the observer live |

The one failing test is `ui/LiveRegion.test.tsx` ("is the only aria-live or live role in
the application source"). It flags `app/views/ChatView.tsx`, which has `role="log"` with
`aria-live="off"`, and `app/views/chat/Transcript.tsx`, whose `data-role={…}` matches the
guard's regex as a live role. Neither file is Phase 3c's. The table above was taken before
the scan was changed. The change is in commit 57bcd95, not in the 2026-10-14 review fixes: the
scan now allows `role="log"` only in the same tag as `aria-live="off"`, and its self-test is
in `ui/LiveRegion.test.tsx` ("the silent-log exemption accepts only role=log together with
aria-live=off"). On the current tree the test passes (see "Phase 3 review fixes").

### Visual and keyboard QA (Chrome 155, demo gateway on 8822)

- Captures: `docs/webchat/qa/p3c-trajectory-{1440,1024,390}-{light,dark}.png`. At 1440 and
  1024 the DENY decision `dec-03-bash-egress` is selected, so the inspector shows it, and
  at 1024 the drawer is open. At 390 the ledger is shown with no selection. The hashes,
  the Chrome version, the gateway hash, the `dist/` hashes and the source tree hash are in
  `docs/webchat/qa/p3c-trajectory-qa-receipt.json`.
- Keyboard walk at 1440, light: Tab goes skip link, theme (Light pressed), Trajectory tab,
  group toolbar (Turn), search, first row, then leaves the page. ArrowRight moves focus
  inside the group toolbar and changes no grouping. Enter presses Tool. ArrowDown, End and
  Home move through the ledger. Enter selects the focused record, and Escape clears it and
  returns focus to that row. At 1024, Enter on a row opens the drawer and moves focus to its
  heading, and Escape closes it and returns focus to the row.
- The page has zero CSP violations, zero console errors except the favicon 404 (allowed),
  zero uncaught exceptions, and no `style` or `on*` attribute.
- These checks verify UI wiring only. They establish no `G1`, containment or `H1` result.

## Phase 3 polish (2026-10-14)

A pass over the known polish items left by 3b and 3c. Each behaviour change was first a
failing test, except where the table says otherwise. The Trajectory view is not changed,
except for the tail pin and the inspector name.

**1. Duplicate notices: one copy, above the tabs.** The gap, window and eviction notices
render once, in the shell (`Shell.tsx`, unchanged). The Chat transcript no longer repeats
them. The shell copy sits above the tabs, where a reader inside the transcript cannot see
it, so the top of the transcript carries a short pointer, "Notices for this transcript are
above the tabs.", whenever a notice applies. The pointer repeats no notice text. The
Trajectory view had no inline notices, so it has no pointer. The announcer is unchanged:
the store still announces each gap once.
- Tests: `app/Shell.views.test.tsx` "shows a gap, a window gap and an eviction once each, in
  the chat view and in the trajectory view" counts the notice text as shown. It leaves out
  the live status region, which holds the last announcement. `app/views/ChatView.test.tsx`
  "does not repeat the window notice…", "does not repeat the eviction notice…" and "marks the
  top of the transcript with a pointer…".
- Red: "does not repeat the window notice…" failed with one `role="note"` in the transcript
  where none was wanted.

**2. Tool headline: no repeated words.** `app/views/chat/headline.ts`,
`headlineSummary(tool, summary)`, removes leading repeats of the tool name that end at a word
boundary (space, colon, middle dot, dash; a hyphen is not one, so `bash -c` keeps its flag).
It compares case-insensitively after NFC normalisation. A summary that is only the name
gives an empty headline. A blank or absent summary gives the "no bounded summary" text.
- Tests: `app/views/chat/headline.test.ts` (12) covers the fixture summaries, a repeat of a
  repeat (`read read service/x` gives `service/x`), a longer word that starts with the name,
  case, a flag, unicode, an empty summary and the tool name `tool`. `ChatView.test.tsx` "says
  the tool name once in a tool card's headline".
- Red: the DOM test read `bash`, `bash` (two occurrences) where one was wanted.

**3. Unverified chips: one provenance line.** `app/views/chat/provenance.ts`:
- Uniform (every visible record shares authority, verification scope and verification): one
  line at the top of the transcript, for example "All 18 records in this view are unverified
  · source: fixture". The singular reads "The one record in this view is …". No item marker.
- Mixed: each record is marked. An unverified record has an `unverified` chip. A record with
  verified evidence has a `verified` chip in the success tone. Both use the stricter gate:
  the projection is live and audited, and the item has evidence.
- Empty: no line.
- A tool card that draws no unverified chip keeps "unverified" in its accessible name, as
  hidden text.
- Tests: `app/views/chat/provenance.test.ts` (9) covers uniform unverified, the singular,
  uniform verified, a mix by verification, a mix by authority, a mix by scope, the marker
  rule and the empty case. `ChatView.test.tsx` covers the uniform line, the accessible name,
  the mixed view, the verified-only view and the empty projection.
- The mixed-view DOM test renders from a model, not a live snapshot. The validator refuses
  a live projection that mixes unobserved and observed records, so that state cannot come
  from the stream.
- Red: the uniform and verified-only DOM tests found no provenance line. The provenance
  module's tests failed on import until it existed.

**4. Tail count across view switches.** The pin moved from per-view state (a module
`WeakMap` in the chat view, a per-mount ref in the trajectory view) into the workspace store.
The store has `actions.repinTail(atTail)` and derives `model.tailPending`, which is
`pendingSince(applied, pin)`. The count still comes only from `applied`. The pin follows the
applied count while the reader is at the tail, and it clears when the reader reaches the tail
or presses Jump. A count already on screen when a view mounts is not announced again.
- Tests: `app/Shell.views.test.tsx` "carries the count into the trajectory view and back, and
  clears it at the tail". It switches between the views, counts applied records in each, and
  clears the count at the tail. The existing tail tests in `ChatView.test.tsx` and
  `TrajectoryView.test.tsx` still pass.
- Red: the test failed before any fix, on its first count (no count was shown before the
  switch). I did not isolate that cause in the old code. The store-level pin passes the
  test.

**5. Inspector name.** `app/Inspector.tsx` names the aside `Record inspector` while a record
is shown, and `Governance inspector` otherwise. Focus handling is unchanged.
- Test: `app/Shell.views.test.tsx` "is 'Record inspector' while a record is shown…".
- Red: "expected 'Governance inspector' to be 'Record inspector'".

**6. First load scroll at 390 px.** Cause: the chat tail hook measured the page at the
first commit, while the projection was still loading and the page was short. It therefore
counted the reader as "at the tail". When the records arrived, it scrolled the page to its
new bottom. The reported scroll of 825 px at 390 px implies a page of 1669 px (825 + 844).
The trajectory view does not do this, because it waits until it has shown records. The chat
view now waits in the same way. At 390 px the page is now 1708 px tall. The 39 px difference
from the implied 1669 px is most likely the new provenance line. I did not measure the old
build, so that attribution is not verified.
- Test: `app/Shell.views.test.tsx` "starts at the top when the page grows on the first load…".
- Red: `scrollTop` was 4500, and the test expects 0.
- Chrome 155, light. At 390 × 844, `scrollY` was 0 after load, after switching to
  Trajectory, and after switching back. At 1440 × 900 it was 0 after load. See the receipt
  below.

**7. The 3a section** is above, in "Phase 3a".

**Parity rows.** `phase3-parity.md` rows 17, 29, 30, 31, S3 and the status line are updated.

**Receipts (Windows `gate.ps1` unless marked Linux).**

| Gate | Command | Exit | Result |
|---|---|---|---|
| test, baseline | not re-run in this pass; from the Linux run recorded in `TASK_STATE.md` (Phase 3b/3c) | n/a | 49 files, 721 passed, 0 skipped |
| typecheck | `gate.ps1 -Task typecheck` | 0 | `tsc --noEmit` clean |
| app tests | `gate.ps1 -Task test -Filter src/app` | 0 | 16 files, 215 passed, 0 skipped |
| verify | `gate.ps1 -Task verify` | 0 | tsc clean; 52 files, 752 passed, 0 failed, 0 skipped; sinks self-test OK (12 rules) and 0 findings in 84 files; deps self-test OK (26 cases) and check OK |
| build | `gate.ps1 -Task build` | 0 | one fresh `dist/` copied back |
| go | `gate.ps1 -Task go` | 0 | `go vet` clean; `go test` ok for both packages; `gofmt -l` empty |
| CSP smoke | `node scripts/csp-smoke.mjs --url http://127.0.0.1:8823` | 0 | PASS: 7 header probes; `/app/` and `/` with 0 CSP violations; the observer live (4 violations on the canary); Chrome 155.0.8059.79 |

The 752 tests are 721 plus 31: 25 in the three new test files, and 6 net more in
`ChatView.test.tsx`.

The browser receipt is `docs/webchat/qa/p3-polish-qa-receipt.json`. It lists the scroll
checks, the Chrome product, the gateway hash (demo gateway on 8823), the `dist/` hashes and
the source tree hash. The screenshots are `p3-polish-390-light.png` and
`p3-polish-1440-light.png`. The 390 and 1440 captures show the chat view as first loaded,
scrolled to the top. The script that produced them is not in the repository.

**Not done, or deliberately deferred.**
- The Trajectory ledger's per-row "unverified" marker is unchanged. Item 3 is about the
  transcript.
- The Trajectory view has no notice pointer, because it never had inline notices.
- Live arrivals and Jump to latest were not driven in Chrome. The demo has no live arrivals.
  The count across view switches is covered in happy-dom only.
- Screen-reader output is not tested.
- Linux (WSL, Go 1.27.1, Node 24.15.0), run on the working tree of this pass: an export of
  HEAD 57bcd95 plus the uncommitted changes, 606 files. Go: `go build`, `go vet -mod=readonly`,
  `gofmt -l` empty, and `go test -race -mod=readonly -count=1 -v` in `extensions/webchat`
  (39 PASS, 0 FAIL, 0 SKIP) and in `supervisor` (488 PASS, 0 FAIL, 7 SKIP). The seven are
  environment skips. The count is one above the 487 recorded in `TASK_STATE.md`, and that +1 is
  not reconciled test by test. `supervisor` has no changes in this pass. The boundary guard
  self-test passes 8/8, and the guard passes. Frontend: `npm ci --ignore-scripts` reports 0
  vulnerabilities; typecheck 0; vitest 52 files, 752 passed, 0 skipped; `check:sinks` 84 files,
  0 findings; `check:deps` OK; legacy `node --test` 258/258, 0 skipped. The source hash of
  `frontend/src` (90 files) matches the QA receipt, and the Linux rebuild of `dist/` is
  byte-identical to the Windows build. Logs are in WSL under `~/tbwc-verify/logs/`.
- The CI repro gate (`npm run repro`) was RED on this working tree before the commit: exit 1,
  because HEAD's committed `dist/` predated this pass. Resolved by the commit below, which
  carries the rebuilt `dist/`. *(Superseded by `57e6406`, which commits the rebuilt `dist/`;
  the Linux rebuild equals it.)*
- csp-smoke first ran on Windows only, against a gateway built with Windows `go`. It has since
  been re-run against a Linux-built gateway hosted in WSL (see the next section).
- `TASK_STATE.md` records this pass in its "Phase 3 polish" section.
- Committed as a2aaf30; `npm run repro` was re-run on Linux on that commit and passes (tree
  sha256 22f06f14...).

## Browser check against a Linux-hosted gateway (2026-10-14)

tbound targets Linux (native or WSL), so the gateway under test should be the Linux build.
`csp-smoke` can be pointed at it with `--url`; the browser may run on another OS.

Verified once at HEAD a2aaf30: a Linux ELF built with Go 1.27.1 inside WSL2 (kernel 5.15,
`--demo --addr 127.0.0.1:8831`), Chrome 155 on Windows reaching it over the WSL2 localhost
forward: 7/7 header probes carry the policy, `/app/` and `/` have 0 CSP violations and 0
console errors (favicon 404 allowed), the canary produced 4 violations (observer live), and
the CSS Module class applied. `csp-smoke` still prints "toolchain not checked" for `--url`
runs: it cannot see how the gateway was built; the receipt above is the evidence.

Recipe (the pitfalls are real):
- Build in WSL: `go build -mod=readonly -o ~/tbwc-gw-linux ./cmd/tbound-webchat`.
- Start it from Windows with an ABSOLUTE path, not `$HOME` (Windows expands `$HOME` before
  WSL sees it) and keep the `wsl.exe` client alive; a background job started inside a
  short-lived `wsl -e bash -c` session is torn down when that session ends:
  `Start-Process wsl.exe -ArgumentList '-d','Ubuntu','-e','/home/<user>/tbwc-gw-linux','--demo','--addr','127.0.0.1:8831' -WindowStyle Hidden`.
- From Windows run `node scripts/csp-smoke.mjs --url http://127.0.0.1:8831`.
- Stop it afterwards and never reuse port 8791 (a stale old-build gateway may hold it).

Remote access. The gateway is loopback-only by design: it binds 127.0.0.1, pins the Host header
to the bound address, and the CSP/Origin checks assume that origin. A user on another machine
should reach it through an SSH local forward using the SAME port number
(`ssh -L 8831:127.0.0.1:8831 host`), so the browser's Host stays `127.0.0.1:8831`. A forward
to a different local port, or a reverse proxy that rewrites Host, is rejected (403) by design.
This tunnel path was reasoned from the Host pin and an earlier proxy experiment; it has NOT
been run over a real SSH connection. Binding the gateway to a public interface is not
supported and would need the authenticated, origin-protected design that
docs/modular-architecture-vision.md lists as future work.

## Phase 3 review fixes: the claim boundary (2026-10-14)

Eight findings from the Phase 3 review were fixed under strict TDD. Each test that reproduces a
finding was run and failed (RED) before its fix, then the suite was run again (GREEN). The gates
are Windows (`gate.ps1`); WSL was not used. Core (`src/core/`), markdown, Go, scripts and the
legacy UI are unchanged.

**One verification gate.** The gate is core `verification.ts`: `isVerifiedIn(projection, item)` is
true only in a live, structurally audited projection, and only for a record with its own observed
evidence. `app/verification.ts` re-exports it, and `confirmedIn` adds "not lossy". The record model
takes the projection, so the record alone never decides (see the claim gate section below). The
Evidence section states the record's own authority and scope. Chat, the trajectory rows and rail,
the inspector, the posture and the footer all ask this gate. No announcement carries a verification claim.

1. **Verified gate inconsistent (fixed).** A streamed observed success in a fixture projection
   read `succeeded, verified` in the Trajectory view while Chat read it `reported`. The stream
   does not refuse an observed record against the snapshot's source (only the snapshot is
   checked), so the fixture projection can hold one. Tests: `app/verification.surfaces.test.tsx`
   "the trajectory row, its dot and the sequence rail read the streamed success as reported,
   never ok" and "the record inspector reads independently verified: no, with the state as
   reported" (RED: the row read `succeeded, verified`; the inspector read `yes`). "a live
   structural-audit projection reads the same records as verified, on every surface" stays
   green. `app/verification.test.ts` (RED: module missing).
2. **Verified lossy success green in Chat (fixed).** A verified lossy success reads
   `succeeded (output lossy, not confirmed)` on the unknown tone in Chat, the Trajectory chip and
   the inspector, and its accessible name says the same. The posture does not count it as
   `verified`; it counts it under `reported`, whose hint now reads "not confirmed: reported, or
   verified with lossy output". Tests: `app/views/ChatView.test.tsx` "never gives a verified lossy
   success the ok tone" (RED: `succeeded` in the ok tone), `app/views/TrajectoryView.test.tsx`
   (chip text), `app/inspector/recordChip.test.ts`, `app/inspector/RecordInspector.test.tsx` and
   `app/views/chat/readings.test.ts` (RED).
3. **LOST drawn as DENY (fixed).** LOST is in the unknown family. The rail draws it as a hatched
   segment in the unknown colour, the dot as a solid diamond, and the chip as a double border on the
   unknown ink and tint. Its words are `LOST, not success`. Under forced colours it is a double
   outline, and the diamond is a clip-path, so the shape holds without colour. The rail no longer
   folds LOST into DENY: `views/trajectory/rail.ts` builds the lanes from the core's record dot. The
   legacy UI already drew LOST in the unknown tone (`internal/webchat/assets/render.mjs`, the posture
   count). Tests: `app/views/TrajectoryView.test.tsx` "a LOST result beside a DENY differs by tone and
   by words" and the rail-words test (RED); `ui/StateDot.test.tsx` "draws LOST in the unknown family
   with a diamond of its own" (RED); `ui/Chip.test.tsx` "LOST is in the unknown family, with a shape
   of its own" (RED); two contrast rows in `styles/tokens.test.ts` for the LOST chip and diamond
   (both pass; they use the unknown family's pairs).
4. **Drawer reopened (fixed).** Leaving the narrow layout closes the drawer and forgets its request,
   so a later narrow layout starts closed. Narrowing with the drawer closed moves focus out of the
   inert inspector to the toggle. Tests: `app/Shell.test.tsx` "does not reopen after a widen and a
   narrow again" and "a record selected at narrow width opens the drawer; after a widen and a narrow
   the drawer is closed and focus is not in it" (RED: `data-open` was `true`). "never opens by itself
   when the viewport narrows" is a control and passed before the fix.
5. **Loading and error together (fixed).** `status.ts` `projectionPhase` gives one state: loading
   (before the first snapshot, or a recovery fetch in flight with no data), failed (a failure with no
   data, or offline or terminal), or loaded. A failure replaces the skeleton, and it names the
   reconnect status in words. After data, a failure is a notice beside the data. The chat, and the
   trajectory's rail and ledger, follow the same rule. Tests: `app/Shell.views.test.tsx`, describe
   "projection states: loading, failure and recovery are never shown together" (four tests). Before
   the fix, the first-load failure showed `['error', 'loading']`, as the review's probe did.
6. **Absent reads "no" (fixed), and the audit.** Absence is "unavailable" with its reason in every
   panel, footer, rail and inspector row. The provenance panel's "Independently verified" read `no`
   when the panel was absent; it is now unavailable, and `yes` needs both the panel's availability and
   the projection's verification. Before the first snapshot, the footer counts, the rail's streams,
   the warnings, the posture, the chat summary and the trajectory count read `unavailable (no
   projection loaded)`, not zero or none. A record with no authority or scope has an unavailable
   Evidence row. The recovery panel no longer throws when its panel is missing. Tests:
   `app/panels.test.ts` "absent input reads unavailable, never no, zero, none, complete or ok"
   (table-driven), `app/Footer.test.ts`, `app/Shell.test.tsx` "before the first snapshot, every panel,
   rail and footer value reads unavailable", `app/Shell.views.test.tsx` "before the first snapshot, no
   count reads as zero", and `app/inspector/RecordInspector.test.tsx` "reads absent authority and
   verification as unavailable".
7. **StrictMode announces twice (fixed).** Under StrictMode the effect is torn down and started again.
   The teardown made the core report `offline`, and the store announced `Stream offline.` before
   `Stream connected.`. A stream that is no longer current now publishes nothing. Test:
   `app/WorkspaceProvider.test.tsx` "speaks the connection exactly once under StrictMode" (RED: the
   announcer's serial was 2). The non-StrictMode control and the listener-removal test pass.
8. **Docs.** The LiveRegion note above, the 3b and 3c statements the review changed, the "Open" item
   on LiveRegion, and the repro note in the polish receipts are corrected in place, each with its
   date. The LiveRegion scan change is in commit 57bcd95 (the 3b/3c commit), not in this review pass,
   and this pass did not touch `ui/LiveRegion.test.tsx`.

**Corrections to my own RED tests before GREEN.** Three expectations written for the RED run were
wrong, and they were fixed before the GREEN run (the RED log keeps them as they were): the rail-words
assertion for a lane that held only a LOST result (it has no DENY words); a row-text assertion that
expected the uppercase `LOST` where the chip reads `lost`; and the "failure after data" test, which
used `fail()`. That event is a dropped stream. The core reports it as `reconnecting` with no error, so
the test now uses a gap whose recovery snapshot fails.

**Receipts (Windows `gate.ps1`).**

| Gate | Command | Exit | Result |
|---|---|---|---|
| RED | `gate.ps1 -Task test`, before the fixes | 1 | 14 files failed, 31 tests failed, 756 passed (787 run; `verification.test.ts` failed on import). Log outside the worktree, `tbwc-y/red1-test.log` |
| GREEN | `gate.ps1 -Task verify` | 0 | tsc: no errors; 54 files, 802 passed, 0 failed, 0 skipped; sinks self-test OK, 0 findings in 86 files; deps self-test OK, check OK |
| Go | `gate.ps1 -Task go` | 0 | `go vet -mod=readonly` and `go test -mod=readonly -count=1` ok for both packages; `gofmt -l` empty |
| build | `gate.ps1 -Task build` | 0 | one fresh `dist/` copied back. SHA-256: `index.html` 82f6558c…, `.vite/manifest.json` a9abffdb…, `assets/index-C9-CX8a8.js` b243413f…, `assets/index-OkJ8tcKX.css` eadcc378…. These replace `index-K5jt8CUl.js` and `index-DaIzfvv8.css`. |
| Go, after the build | `gate.ps1 -Task go` | 0 | re-run because the embedded `dist/` changed; same result as above |

Source tree: `frontend/src`, 146 files, the sha256 of the sorted per-file sha256 list starts
`bb7eb8a7c5f5ab6b` (computed after the last source edit, before the build). The baseline before this pass was 752 frontend tests in 52 files, 0 skipped. The 50 more are new or
changed tests.

**Not done, or deliberately deferred.**
- **Core judged from the item.** Closed in the claim gate section below (2026-10-14): the record model takes the projection.
- **Stream path versus snapshot.** Closed in the claim gate section below (2026-10-14): a streamed item is judged in its bound projection, as Go's `Store.AppendItem` judges it.
- **Footer.** The footer shows no verification-dependent count, so the gate has nothing to decide
  there. The footer changed only in its unavailable-before-load reading.
- **Posture.** A verified lossy success is counted under `reported`, with a hint that says so. A
  separate entry would change the posture's shape, which is a choice for the owner.
- **Repro gate not run.** `npm run repro` compares the committed `dist/` with a rebuild. The new
  `dist/` is not committed by this pass, so the check has not been run on it. *(Superseded
  by `57e6406`, which commits the rebuilt `dist/`; the Linux rebuild equals it.)*
- **Not verified here.** No browser pass was run for this change. The LOST diamond, hatch and
  double border, the drawer at 1024 px and the StrictMode announcements are checked in happy-dom and
  in the CSS source only. The shape contrast is checked by the token table, not on screen.
  Screen-reader output is not tested.

## Claim gate in the core (2026-10-14)

The two open items of the Phase 3 review are closed. The gate is now in the typed core, and a view cannot ask a record alone.

- **Admission at the boundary.** `core/verification.ts` holds the admission rule, mirrored from the Go validator (`validateItem` and `Store.AppendItem`): an `authoritative` element is never admitted; an `observed` element only from a live source; an element's scope must equal its snapshot's. `core/validate.ts` applies it to served snapshot items and to streamed items. A streamed item is judged in the projection its stream is bound to. The stream reads that projection once, when it opens after a snapshot (`projectionOf`), so a reconnect to another source rebinds it atomically. A refused item is an `AdmissionError` (a `ContractError` with a typed `fault`).
- **Recovery, not silence.** A refused or malformed streamed item is reported to `onError` and handed to `onInvalid`. The stream recovers from a snapshot with an `invalid_item` gap notice, the same notice a malformed item gets. The item is not applied.
- **One gate.** `isVerifiedIn(projection, item)` is the only verified predicate. It is true only in a live structural projection, for an element that is observed and structurally audited. `isItemEvidenceOnly` is the element's own declaration, renamed from the legacy `isVerified` for the differential suite. `verifyIn(projection, item)` is the only producer of `VerifiedEvidence`.
- **The record model takes the projection.** `recordDot`, `statusText`, `recordChips`, `recordDetail`, `recordOf`, `sequenceLanes`, `groupRecords` and `visibleRecords` take it. The app's `judgedItem` is deleted, and no file under `src/app` may name `isItemEvidenceOnly`, `isVerified` or `judgedItem` (`verification.gate.test.ts`). `app/verification.ts` re-exports the core gate.
- **Surfaces.** `TrajectoryView`, `Ledger`, `RecordRow`, `SequenceRail` (`railLanes`) and `RecordInspector` pass the projection they are shown in. No view-level logic was added.
- **Deviations.** D1 to D3, with their evidence tests, are in `phase2-parity.md` §4b.

The property test (`app/verification.property.test.ts`) runs 160 seeded sequences of snapshots and streamed events through the same typed boundary the stream uses. After every step, every model function the views use is asked about every retained record. Outside a live structural projection, none reads a record as verified. The run also counts its own coverage (verified, refused and accepted), so it cannot pass vacuously.

## Default UI switch: React at `/`, legacy at `/legacy/` (2026-10-14)

The React client is now the default UI, as directed. The vanilla workspace stays as
a deprecated page at `/legacy/` until the owner signs off its removal. Nothing legacy
was deleted or edited.

- **Routes.** `GET` or `HEAD /` is `302` to `/app/`. The target is a constant, and the
  query string is ignored. `GET /legacy/` is the vanilla workspace with a deprecation
  notice above `<main>`. Its shell and modules are unchanged and still served from
  `/static/`. Other methods on `/` get `405` with `Allow: GET, HEAD`, and every other
  route stays GET-only. Spellings that are not the exact `/` (`//`, `/%2f`, `/.`,
  `/index.html`, `/static/..`) and `/legacy` are `404`, never a redirect. Host and
  Origin pinning are unchanged.
- **Fallback decision.** If the Vite manifest is missing or corrupt, `/app/` still
  answers `503` with the reason. `/` and `/legacy/` then serve the legacy workspace
  (`200`) with a "Fallback: legacy workspace" notice that names the reason, escaped for
  HTML. `/` never redirects into a `503`: that would put the error on the entry point.
- **Notices.** Both notices reuse the gateway's `notice` classes, so they need no inline
  style and `styles.css` is unchanged. The served legacy page differs from
  `assets/index.html` only by the notice, and a test checks that.
- **Startup banner.** `tbound-webchat` prints `UI: http://host:port/ (React app at
  /app/)` first, then the deprecated `/legacy/` URL. If the app is unavailable, the first
  line says the legacy page is the fallback, and stderr gives the reason.
- **Smoke.** `csp-smoke.mjs` audits `/` (it must land on `/app/`), `/app/` and `/legacy/`.
  It sends 16 header probes, including the redirect's status and exact `Location`,
  `HEAD /`, the 405s, a 404, and foreign Host and Origin 403s. Its `GATEWAY_ROUTES` list
  is checked against the Go route table by `routes_test.go`. The canary, the policy
  source and the exit codes are unchanged.
- **Tests.** New `internal/webchat/routes_test.go` (10 tests). The foreign Host and Origin
  test in `app_test.go` now also covers `/`, `/legacy/` and the fallback. New
  `cmd/tbound-webchat` startup-banner test.

Verification, in WSL (Go 1.27.1, Node 24.15.0, private TMPDIR under `$HOME`):

| Gate | Command | Exit | Result |
|---|---|---|---|
| gofmt | `gofmt -l .` in `extensions/webchat` | 0 | no output |
| vet | `go vet -mod=readonly ./...` | 0 | clean |
| Go | `go test -race -mod=readonly -count=1 -v ./...` | 0 | 50 PASS, 0 SKIP, 0 FAIL (39 before, +11 new) |
| Node | `node --test internal/webchat/jstest/*.test.mjs` | 0 | 258 pass, 0 fail, 0 skipped (unchanged) |
| Boundary | `sh scripts/check-extension-boundary.sh` at the repo root | 0 | OK |
| Core | `cd supervisor && go build ./... && go vet ./...` | 0 | clean |
| Mutation | skip `authorizeRequest` for `/` in a throwaway copy | 1 | `TestAppRefusesForeignHostAndOrigin` and `TestRootRejectsOtherMethodsAndAdvertisesGetAndHead` fail |
| Browser | `node scripts/csp-smoke.mjs --url http://127.0.0.1:18832` (Windows Node, Chrome 155) | 0 | PASS: 16 header probes; `/` lands on `/app/`; `/app/` and `/legacy/` clean; observer live (4 canary violations) |

The browser run used a gateway built in WSL from the final tree (binary SHA-256
prefix `8fffae34084d5ef7`). Source SHA-256 prefixes: `handler.go` `757ab08b184e0a52`,
`app.go` `7c872c8cb17f35d8`, `app_test.go` `5df9a8a15f3faced`, `routes_test.go`
`3e64716e9016fbdc`, `cmd/tbound-webchat/main.go` `932a5e82e0a58d93`,
`cmd/tbound-webchat/main_test.go` `9790f5ae161024e5`, `scripts/csp-smoke.mjs`
`4240751741e565a5`. The legacy `assets/index.html` is unchanged.

These are component and UI checks. They establish no claim: G1=false, containment
not-established, H1 unexecuted.

Not done: the legacy assets, the legacy tests and `/legacy/` are not removed; that
waits for sign-off. The fallback path has Go tests only, with no browser check. `/app`
without a trailing slash is still `404`. Screen-reader behaviour of the notices is not
tested.

## Final browser QA (2026-10-14)

This pass drove the finished React client (`/app/`), the redirect, `/legacy/` and the live
stream in Chrome, against a Linux-hosted demo gateway. `scripts/ui-qa.mjs` is new: opt-in,
Node built-ins only, and it exits non-zero on any failed check. `scripts/csp-smoke.mjs` lost
its favicon allowance: `/favicon.ico` answers 204 (pinned by `favicon_test.go`), so any console
error fails the run. No product code, dist, mockup or Go file changed. Neither did this pass
change the open item "the dead favicon allowance" in the list at the top of this file: this
pass closes it.

Environment (`docs/webchat/qa/p4-ui-qa-receipt.json` holds every hash):

- Gateway: `go build -mod=readonly -o /home/jeli2k/tbwc-gw-q2 ./cmd/tbound-webchat` in WSL2,
  Go 1.27.1, exit 0. Binary SHA-256 `a9dd1067669ea9372a55eaeae9e39c5383f15c678b17cb83260c442f6c1cf7bc`.
  Started from Windows with `Start-Process wsl.exe -ArgumentList '-d','Ubuntu','-e',
  '/home/jeli2k/tbwc-gw-q2','--demo','--addr','127.0.0.1:8861' -WindowStyle Hidden`.
- Dist: `internal/webchat/dist` (4 files), tree SHA-256 `875bcee7e8528ea8a61eb09c14e5befe2d88982e41c03261936d71dc6c4892f3`.
  The served `/app/` HTML and its two assets matched the committed files byte for byte (B).
- Browser: Chrome 155.0.8059.79, `chrome.exe` SHA-256 `e2b66159abacb9d42669364e87252f0eb8a0e3bb443f84763b06d44a98997744`,
  headless, a temporary profile and its own DevTools port. Node v24.15.0 on Windows.
- Repository HEAD `57e6406`. `scripts/ui-qa.mjs` SHA-256 `3c2feff114bc63a0a8ea6e44f91f4b6cc7f92ffdcf51b07a4d9543f9fb179389`;
  `scripts/csp-smoke.mjs` SHA-256 `b0e9125e702bc7542d4fb97501d77827f442d084c291eedbd384a30d1d9b20d0`.
- Command (exit 1; log in `docs/webchat/qa/p4-ui-qa-run.txt`): `node scripts/ui-qa.mjs --url
  http://127.0.0.1:8861 --gateway-sha256 a9dd10…f7bc --gateway-stop '<stop>' --gateway-start '<start>'`.
  The stop command ends the `wsl.exe` client that runs the gateway and `pkill`s the gateway. The
  start command is the `Start-Process` line above.

| Check | Result | Key evidence |
|---|---|---|
| A csp-smoke without the favicon allowance | PASS | exit 0; 0 console errors on `/`, `/app/`, `/legacy/`; observer live (4 canary violations) |
| B redirect, legacy notice, served assets | PASS | HEAD and GET `/` are 302 to `/app/`; `/` ends on `/app/` with the React header; `/legacy/` shows "Deprecated: legacy workspace"; served HTML and assets equal the dist |
| C keyboard walk at 1440 | PASS | chat order: skip link, System, Chat tab, All, 5 tool cards, end. Trajectory: one roving stop for grouping, one for the search, one for the ledger. 0 focus stops without a ring. Arrows, Home and End work on the tabs, theme and filters; Enter presses; ArrowUp, ArrowDown, Home and End move through the ledger; Enter selects; Escape and Close record return focus to the row; the skip link follows the view |
| D responsive | PASS | 1024: the toggle has `aria-expanded`; the closed drawer is `inert` and `aria-hidden`; the open drawer starts at the header's bottom edge; the header controls are hit-testable while it is open; Escape returns focus to the toggle; widening then narrowing leaves it closed. 390: no horizontal overflow, `scrollY` 0 on first show of both views, no visible chip overflow. 13 screenshots (12 for 1440/1024/390 × light/dark × chat/trajectory, plus the open drawer) |
| E theme | **FAIL** | Dark, Light and System persist across reloads: PASS. With `localStorage` throwing, the page renders, defaults to system and applies a choice for the session, with no console error: PASS. **First paint: FAIL**, see defect 1 |
| F live stream | **FAIL** | LOST: a `lost` chip, "LOST, not success", a diamond dot (clip-path polygon) distinct from DENY's square. Lossy and UNKNOWN never carry the success tone, and the fixture has no `ok` tone at all. ALLOW has tone `allow`. "unavailable" is shown. An observed item is refused: "Continuity gap (invalid item)" and a recovery snapshot, count back to 18. A duplicate: 0 snapshot fetches, count unchanged. A gap: one "Continuity gap (sequence gap)" notice, count 18, the gapped record not applied, no success added. Three records while scrolled away: "3 new records · Jump to latest", `scrollY` 260 before and after, one announcement, Jump lands at the bottom with focus on the log. **Lossy wording: FAIL**, see defect 2 |
| G reconnect and offline | PASS | badge: connected, reconnecting, connecting, cycling. Seven retry attempts, waits 609, 1672, 3540, 7978, 5147, 9296 ms (cap 10 s), see observation 3. After the restart, `connected` appears only after the snapshot and the event stream's 200. Offline: 0 gateway requests; the retry starts 2 ms after going back online |
| H forced colours and reduced motion | PASS | `matchMedia` reports both preferences. 6 of 6 ledger state shapes are distinct by computed border style, width, radius and clip-path. Under reduced motion, 0 animations run; the control without the preference shows the drawer's 0.3 s transition |
| I console, CSP, network | PASS | 0 CSP violations. 0 console errors or warnings outside G. Inside G, 8 network failures while the gateway was down or killed: 6 `ERR_CONNECTION_REFUSED`, 1 `ERR_EMPTY_RESPONSE`, 1 `ERR_INCOMPLETE_CHUNKED_ENCODING`. 0 requests off the gateway origin, out of 352 |

Defects (found at `57e6406`; their status is in the fix round below):

1. **The first paint shows the OS scheme when a stored choice differs** (E3). Repro: set
   `localStorage["tbound-webchat-theme"] = "light"` on the gateway origin, emulate
   `prefers-color-scheme: dark`, load `/app/`. The first animation frame's computed
   `documentElement` background is `oklch(0.18 0 0)` (dark). At `DOMContentLoaded` it is
   `oklch(1 0 0)` (light). Stored dark with an OS light scheme flashes the other way. Five of
   five loads flashed in each case. With nothing stored, nothing flashes. Cause: the explicit
   choice is applied by a deferred module (`frontend/index.html:12`, `type="module"`;
   `frontend/src/theme/bootstrap.ts:8`, imported first by `frontend/src/main.tsx:3`). Until it
   runs, the stylesheet's `prefers-color-scheme` query decides the canvas. The comments at
   `bootstrap.ts:2-5` and `main.tsx:1-2` say the attribute is set while the document is still
   being evaluated, and the measurement says the first frame can paint before that. The CSP
   (`script-src 'self'`) forbids an inline script, so a fix needs a render-blocking same-origin
   classic script in `<head>`, or a hash for a tiny inline one. That choice is the owner's.
   Limit: the first frame is sampled in `requestAnimationFrame`, not from a first-paint screenshot. *Superseded: fixed in the fix round below (`theme-boot.js`).*
2. **The lossy wording differs from the QA expectation** (F). The exact phrase "output lossy,
   not counted as confirmed" is produced only by the `default` branch of
   `frontend/src/core/trajectory.ts:158-160`, which a lossy success with the unknown tone reaches.
   The fixture's lossy success takes the `reported` branch (`trajectory.ts:151-152`) and reads
   "succeeded, reported, not independently verified; output lossy". The record is not green, and
   its chat chip reads "lossy" with the unknown tone. Both readings are defensible, so the owner
   should choose one. Low severity. *Superseded: fixed in the fix round below (D4).*

Observations (not defects):

3. **Reconnect timing.** After a dropped stream the client reconnects at once (about 160 ms
   later). Its waits then run one backoff step later than `backoff.ts:37-38` computes for
   attempt 0 (which would give 0.25–0.5 s). The observed waits are 0.5–1 s, 1–2 s, 2–4 s,
   4–8 s, then 5–10 s, all under the cap. The spec's "from about 500 ms, cap 10 s" holds. I did
   not trace the drop path, so the cause of the extra step is unverified. *Superseded: the drop path is traced in the fix round below. The 160 ms reading is retired, and the backoff is unchanged.*
4. **A recovery snapshot replaces the store.** Records pushed by the stream that the gateway's
   snapshot does not contain disappear. Here records 19 to 22 vanished after the refusal. That
   follows from the snapshot-first design; a QA stream should expect it.
5. **ALLOW and pending dots differ only in border width** (2 px and 1 px). Reported is dashed and
   unknown is dotted. The computed shapes are distinct under forced colours, but I did not check
   them at 1x on a Windows high-contrast theme. *Superseded: ALLOW is now a bullseye, distinct by shape (fix round below).*
6. **Stale text, not changed here.** The "Open" list at the top of this file still names the
   favicon allowance, and the "Opt-in browser CSP smoke" section still says "except one 404 for
   `/favicon.ico`" (about lines 304 to 305). This pass makes both wrong. Only this section was edited. *Superseded: both lines are corrected in the fix round below.*

Not verified:

- Screen readers and other assistive technology. ARIA names were checked as DOM strings only.
- Windows high-contrast themes. Forced colours were emulated with CDP `Emulation.setEmulatedMedia`.
- Pointer and touch input. Clicks used `element.click()`, keys went through CDP input, and the
  hit test used `elementFromPoint`.
- Browsers other than Chrome 155, a native Linux browser, and the SSH-forward remote path.
- Real network loss. The outage was a killed WSL process; offline was CDP network emulation.
- Live records. They were injected through a fake `EventSource` built from the wire shapes in
  `handler.go` and `core/validate.ts`. The gateway served only its fixture.
- The favicon change was not mutation-tested, because no gateway serves a 404 favicon.
- Of the 17 screenshots, two were looked at (the open drawer at 1024 and the 390 px chat). The
  rest are hashed in the receipt, not reviewed.
- Go gates (`go test`, `go vet`, `gofmt`) were not run in this pass. `TASK_STATE.md` was not
  updated, because it is outside the permitted edits.

Claim boundary: these are browser checks of presentation and behaviour. They establish no
claim: G1=false, containment not-established, H1 unexecuted.

## Final QA fix round (2026-10-14)

This round addresses the five items that the Final browser QA recorded at `57e6406`, and re-runs that QA. The numbering
below follows this round: items 1 and 2 are the QA's defects 1 and 2; item 3 is the QA's observation 5; item 4 is the
QA's observation 6; item 5 is the QA's observation 3. Items 1 to 4 change code or docs. Item 5 is confirmed and documented,
with no code change. Nothing is
committed. The tree tested is HEAD `caae3af` plus the files under "Files changed", with `frontend/src/app/` at HEAD.

**The shared worktree is not the tested tree.** Another pipeline is editing `frontend/src/app/` in this same worktree.
At the last check, 15 non-test files and 10 new non-test files there differed from HEAD, besides test files. The
worktree's own test run was red in five files: `Rail`, `Shell`, `Shell.views`, `TrajectoryView`, and the token test's
alias-tier check of `app/Shell.module.css`. None of those files is part of this round. So every verification below ran
on an overlay: HEAD (from `git archive`) plus this round's files. The overlay's code files are byte-identical to the
worktree's, and its `dist/` is byte-identical to the worktree's `dist/`. The committed tree must not pick up the other
pipeline's work, and the committed `dist/` corresponds to HEAD's `src/app` plus this round.

### Defects

1. **First paint (E3): fixed.** The explicit choice was applied by a deferred module, so the first frame painted the
   OS scheme. Now `frontend/public/theme-boot.js` is a classic script, first in `<head>` (`index.html`). Vite copies it
   unchanged to `dist/theme-boot.js` and rewrites `src="/theme-boot.js"` to `/app/theme-boot.js`, with no warning. It
   reads the stored choice inside `try`/`catch` and sets `data-theme` as `theme.ts` does. Invalid, system and throwing
   storage keep the system choice. The script is external, so the CSP is unchanged: `script-src 'self'`, no inline
   script, no `unsafe-inline`. `meta color-scheme` stays. The manifest does not name this file, so `app.go` admits it
   by its own rule, `admitBootScript`. The shell must load it as its first `<script>`, a classic tag whose only
   attribute is `src`, before `<body>`, as its only reference, and the file must be in the build. Every other load
   fails the derivation. Its neighbours stay 404. Rejected: a hashed inline script (it would widen the CSP), and a
   Vite-emitted classic bundle (Vite does not emit classic scripts from `index.html`). `bootstrap.ts` and `main.tsx`
   now say that the module is only a fallback.

2. **Lossy wording (F): fixed, deviation D4.** Every lossy success now ends in `output lossy, not counted as
   confirmed`, in every branch. The reported branch reads `succeeded, reported, not independently verified; output
   lossy, not counted as confirmed`. The verified branch reads `succeeded, output lossy, not counted as confirmed`. No
   lossy success takes the ok tone, which was already true. The legacy module still says `...; output lossy`, so the
   difference is recorded as D4 in `phase2-parity.md`. The differential corpus adds the clause to a reported lossy
   success and nothing else, and `differential.test.ts` §F pins the exact strings. Not changed: the chat readings
   (`src/app/views/chat/readings.ts`) and the inspector state chip (`src/app/inspector/recordChip.ts`) say
   `succeeded (output lossy, not confirmed)`. Those files are not owned by this round. If the owner wants the exact
   phrase on every surface, they need the same change.

3. **ALLOW vs PENDING (observation 5): fixed.** ALLOW is a bullseye: its 2 px ring with a centred 2 px dot (`::after`,
   `inset: 0; margin: auto`). Under forced colours the dot keeps a system colour (`forced-color-adjust: none;
   background-color: CanvasText`). PENDING is a thin 1 px hollow ring. The two now differ by the centred mark, not by
   border width alone. The change uses CSS Modules and alias tokens only, with no inline style.

4. **Stale lines: fixed.** The Open list no longer names the favicon allowance. The CSP-smoke section no longer allows
   a 404 for `/favicon.ico`. `phase3-parity.md` line 150 is marked superseded. The old defect list above carries
   "superseded" notes instead of being rewritten, so its receipts stay.

5. **Reconnect backoff: confirmed, not changed.** `backoff.ts:37-39` computes `min(10 s, 500 ms × 2^n)`, scaled by a
   factor in [0.5, 1). Step 0 is 250–500 ms, step 1 is 500–1000 ms, step 2 is 1–2 s, step 3 is 2–4 s, step 4 is 4–8 s,
   and later steps are 5–10 s. A drop schedules step 0 (`stream.ts`, `drop` → `scheduleRetry`). Each failed retry
   schedules the next step (`fail` → `scheduleRetry`). G's first measured gap runs from its first failed request, so
   that gap is step 1. The first measured gap of this run was 516 ms, which is in band. The "about 160 ms" in the
   earlier observation was the time between the `reconnecting` badge and the first request in the receipt's timeline.
   Step 0 cannot be shorter than 250 ms, so that figure is not a backoff wait, and it is retired. G's bands were
   already right. Only the comment that explained them was wrong, and it is corrected.

### Files changed

- Frontend: `index.html`; `public/theme-boot.js` (new); `vite.config.ts`; `src/main.tsx`; `src/theme/bootstrap.ts`;
  `src/theme/theme.ts` (comment); `src/theme/boot.test.ts` (new); `src/theme/shell.test.ts` (new); `src/core/trajectory.ts`;
  `src/core/trajectory.test.ts`; `src/core/differential.test.ts`; `src/ui/StateDot.tsx` (comment);
  `src/ui/StateDot.module.css`; `src/ui/StateDot.test.tsx`; `src/styles/tokens.test.ts`.
- Go: `internal/webchat/app.go`; `internal/webchat/app_test.go`.
- Build: `internal/webchat/dist/` rebuilt. It holds `.vite/manifest.json`, `index.html`, `theme-boot.js` (new), and
  `assets/index-CFJRJdII.js` and `assets/index-DV_e83aP.css`, which replace the two old assets.
- QA: `scripts/ui-qa.mjs` (the G comment is corrected, and H also reads the centred mark's computed box under forced
  colours; no band or expectation was relaxed). `docs/webchat/qa/` holds this run's receipt and log, and regenerated
  screenshots. The first run's FAIL is kept as `p4-ui-qa-receipt-first-run.json` and `p4-ui-qa-run-first-run.txt`.
- Docs: `docs/frontend.md`; `docs/phase2-parity.md` (D4 row); `docs/phase3-parity.md` (line 150, superseded).

### Verification

Red, then green, each observed:

- Lossy wording: the two new `trajectory.test.ts` tests failed (2) before the change and pass after.
- D4: with HEAD's `trajectory.ts`, 4 tests fail across `trajectory.test.ts` and `differential.test.ts` (the corpus, and
  the D4 test). They pass with the change and the D4 annotation.
- Bullseye: 3 `StateDot.test.tsx` tests failed before the CSS change (no centred mark; outline equal apart from the
  mark; forced-colour centre). They pass after. The new token row for the centre passed at once, since no colour
  changed, so it guards rather than drives.
- First paint, logic: `boot.test.ts` fails while `public/theme-boot.js` is absent (the module cannot load). It runs the
  shipped file in `node:vm` against stub storage, and it agrees with `theme.ts` for every stored value.
- First paint, shell: `shell.test.ts` fails on the built `dist/index.html` before the build. It fails the source checks
  (2) when the boot line is removed from `index.html`.
- First paint, admission: `TestAppAdmitsTheThemeScriptOnlyAsTheFirstClassicScript` failed before `app.go` changed and
  passes after. `TestAppThemeScriptAdmissionOpensNoNeighbours` passes before and after, as a guard.
- Mutation check of the admission rule, in a throwaway copy of the module (`mutate.ps1`; the pristine copy passes):
  M1, drop the before-`<body>` check: 2 tests fail. M2, drop the bare-`src` check: 6 tests fail. The first version of
  M2 survived, because every refusal case put a rejected attribute first. Five "src first, then X" cases were added, and
  M2 is now caught. M3, admit the file whenever it is in the build: 1 test fails.

Final gates, on the overlay (exit codes as printed by the gate):

- Windows `gate-overlay.ps1 -Task verify`: exit 0. vitest 62 files and 916 of 916 tests passed, with 0 skipped (HEAD
  had 60 files and 896 tests). `tsc --noEmit` is clean. `check:sinks`: 88 files, 0 findings. `check:deps`: OK.
- Windows `gate-overlay.ps1 -Task go`: exit 0. `go vet` is clean, `go test` is ok for `cmd/tbound-webchat` and
  `internal/webchat`, and gofmt is clean.
- WSL (Ubuntu, Node 24.15.0, Go 1.27.1, `umask 077`, private `TMPDIR`). The overlay was exported with `tar`, leaving
  out `frontend/node_modules`. `npm ci --ignore-scripts`: exit 0. Typecheck 0. vitest 62 files and 916 of 916 tests
  passed. `check:sinks` 0, `check:deps` 0, `npm run build` 0. `go vet -mod=readonly ./...` 0. `gofmt -l .` is empty.
  `go test -race -mod=readonly -count=1 -v ./...`: 56 top-level PASS, 0 SKIP, 0 FAIL (150 at all levels, 0 SKIP, 0 FAIL).
- Builds: the worktree was built once with `gate.ps1 -Task build`, which gives the shipped `dist/`. The overlay was built
  once with `gate-overlay.ps1 -Task build`, to verify the overlay. WSL ran its own `npm run build`.

Reproducibility: `dist/` has five files. Its tree digest, with one `name TAB sha256 LF` line per file in name order, is
`0466e8828508c4a6e9801885a1d516d2c71054dffed0159bc69ea1422a8b5f83`. That digest is the same for the Windows overlay
build, the worktree's `dist/` on Windows, and the WSL Linux build. The QA receipt uses its own form (`name SPACE sha256`,
joined by LF), which gives `0e352aebe1eaafb51914a852069bb34658215ed102802e63bbdb6551de20ca55`.

Gateway: a Linux ELF built in WSL from the export, with `go build -mod=readonly -o ~/tbwc-gw-q3 ./cmd/tbound-webchat`.
Its SHA-256 is `1a783855971e46a12df761d5e05768d7a81a9f6d498097a8d5dc6a5f7aac6bbc`. It ran on `127.0.0.1:8862` with the
demo fixture, started from Windows through `wsl.exe`. Port 8791 was not used.

Source hashes (SHA-256 of the WSL export, which equals the overlay): `frontend/src` tree `f2b55e78…` (one `name TAB sha256`
line per file, as in the dist form above); `internal/webchat/app.go` `08784f49…`; `app_test.go` `0af885d4…`;
`frontend/public/theme-boot.js` `542ca16f…`; `frontend/index.html` `9a9632ae…`. `go build ./...` in WSL: exit 0.

Browser QA: `node scripts/ui-qa.mjs --url http://127.0.0.1:8862 --gateway-sha256 1a7838…7aac6bbc --gateway-stop …
--gateway-start …`, run with Windows Node 24.15.0 and Chrome 155.0.8059.79. Exit 0; `ui-qa: PASS (9 checks, 17 screenshots)`.

| Check | Result | Key evidence |
|---|---|---|
| A csp-smoke without the favicon allowance | PASS | exit 0; 3 pages, 0 console errors |
| B redirect, legacy notice, served assets | PASS | served assets equal `dist/` |
| C keyboard walk at 1440 | PASS | chat 10 stops, trajectory 4; 0 stops without a ring |
| D responsive | PASS | drawer; no overflow at 390 px; 12 screenshots |
| E theme and first paint | PASS | first frames match the final canvas in 4 cases (first run: 5 of 5 loads flashed in each mismatched case) |
| F live stream | PASS | lossy row reads "output lossy, not counted as confirmed" (first run: FAIL) |
| G reconnect and offline | PASS | backoffs 516, 1846, 2664, 5904, 7722, 9781 ms; 0 requests while offline |
| H forced colours and reduced motion | PASS | 6 of 6 ledger shapes distinct, the bullseye's mark counted; PENDING has no mark |
| I console, CSP, network | PASS | 0 CSP violations; 0 requests off the gateway origin, of 406 |

Receipt: `docs/webchat/qa/p4-ui-qa-receipt.json` (outcome PASS). Its `script.sha256` is `222e9993…` for the changed
`ui-qa.mjs`, and its `dist` hashes are the ones above. Log: `docs/webchat/qa/p4-ui-qa-run.txt`. The receipt's `git.head`
is still `caae3af`, which does not describe this uncommitted tree; the file and `dist` hashes do.

Claim boundary: these are browser checks of presentation and behaviour. They establish no claim: G1=false, containment
not-established, H1 unexecuted.

### Not done, or disagreed with

- `npm run repro` cannot pass yet. It compares the build with the `dist/` committed at HEAD, which predates this round.
  Run it after the commit. The cross-platform part, Windows and Linux builds identical, was checked by digest above.
- The shared worktree is red, as listed above. None of those files was touched. The worktree also holds two edits that
  are not this round's: a line in `src/styles/tokens.css` (`--rail-collapsed-width`) and an edit to `docs/web-chat.md`.
  Neither is in the verified tree or the shipped `dist/`. Do not commit them with this round.
- Chat and inspector wording (`readings.ts`, `recordChip.ts`) is not owned here and is not changed. See defect 2.
- The v3 mockup and the design notes still show the pre-bullseye ALLOW dot. Another agent refreshes them.
- `TASK_STATE.md` is not updated, since it is outside the permitted edits.
- The first run's screenshots were overwritten by this run. Only the first run's receipt and log are kept.
- `ToggleGroup.test.tsx` prints "not wrapped in act(...)" warnings. They are pre-existing and were not touched.
- Not verified, as before: screen readers; Windows high-contrast themes (forced colours were emulated with CDP);
  pointer and touch input; browsers other than Chrome 155.
