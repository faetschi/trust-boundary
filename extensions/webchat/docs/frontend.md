# Web chat frontend (React scaffold, Phase 1)

Status: **Phase 1 (scaffold) and Phase 2 (typed logic port) are committed.
Phase 3 is pending. Not the product UI.** The React client is served
under `/app/`, next to the vanilla UI. The vanilla UI is still the default at
`/`. Phase 3 is the first point where the React client can replace it. The
decision is in `adr-0001-optional-extension.md`.

The scaffold renders one placeholder heading and the build version, and it
proves the toolchain: TSX, CSS Modules extracted to a static `.css` file, a
build-time version string, and the no-inline-style rule.

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
- `__APP_VERSION__` comes from `package.json`, never a timestamp. The output
  depends only on the source bytes, which `frontend/.gitattributes` keeps LF,
  and on the pinned toolchain.

The generated `index.html` has one `<script type="module" src="/app/assets/…">`
and one `<link rel="stylesheet" href="/app/assets/…">`, with no inline script or
style. CSS Modules compile to hashed class names in the static `.css` file.

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
under `/app/` is 404. The vanilla UI, the API and the fixture routes keep
working. `tbound-webchat` prints a `note:` on stderr with the same reason at
startup.

The response headers are the same as for the vanilla UI. The
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
2. **Header probes.** Over plain HTTP, seven responses must carry the policy and
   the expected status: `GET /`, `GET /api/v1/contract`, `GET /app/`, a missing
   `/app/` asset (404), `POST /app/` (405), `GET /` with a foreign `Host` (403),
   and `GET /app/` with a foreign `Origin` (403). The policy is read from the
   `contentSecurityPolicy` constant in `handler.go`.
3. **Browser audit of `/app/` and `/`.** For each page, the Document response's
   `Content-Security-Policy`, as the browser received it, must equal the policy.
   There must be zero CSP violations (from `securitypolicyviolation` events and
   `security`-source console entries), zero console errors and zero uncaught
   exceptions, except one 404 for `/favicon.ico`. The pages have no favicon link,
   so the browser asks for the default icon. The count of allowed favicon 404s is
   printed. There must be zero `style` attributes and zero `on*` attributes.
   `/app/` must render its banner with a CSS Modules class whose computed
   background differs from a default probe element, and a linked stylesheet. `/`
   must have its title, the claim strip, the chat view, and the chat and
   trajectory tabs.

Exit codes: `0` PASS; `1` a check failed; `2` usage error (including port 8791,
which is a stale build); `3` `NOT VERIFIED: no browser`; `4` `NOT VERIFIED`: the
gateway, the Go toolchain or the DevTools connection was unavailable; `5` `FAIL:
observer is blind`. A PASS is printed only when every check passed.

The mutation runs and the live run are recorded with their commands in
`adr-0001-optional-extension.md`, §(v).

## Phase plan

- **Phase 1 (committed in `263b6ca`, review fixes in `2583f5a`):** the scaffold, the manifest-derived `/app/` route,
  the tests, the reproducibility gate, and the opt-in CSP smoke. No product UI.
  The vanilla UI is unchanged and remains the default.
- **Phase 2 (committed in `4700ca5`):** port the pure logic to TypeScript: `store`,
  `stream`, `backoff`, `markdown`, `filters`, `trajectory`, `anchor`, `format` and
  `stop-sequence`. The existing 258 Node tests are the parity net, and behaviour
  may not change. Evidence: `phase2-parity.md` and `phase2-markdown.md`.
  The Go allowlist keeps deriving from the manifest, so no hand-written route is
  added.
- **Phase 3:** replace `render.mjs` and `app.mjs` with React components, with
  native `<dialog>`, Popover and `<details>` for overlays. Parity is shown by the
  browser QA captures and the CSP smoke before `/` switches to the React client.
  The vanilla assets are removed only after that switch.

## Claim boundary

This is an engineering scaffold. It establishes no `G1`, containment or `H1`
result. `G1=false`, containment is `not-established`, and `H1` is unexecuted. The
placeholder shows no policy, effect or publication result. Passing checks here
verify code and UI wiring only.

## Verification receipts

The exact commands, exit codes and hashes for Phase 1 are in
`adr-0001-optional-extension.md`, §(v), the P1 status table. The Phase 2 receipts
are in `phase2-parity.md` §9 and `phase2-markdown.md`. On GitHub, the `webchat`
and `frontend` jobs succeeded at `4700ca5` (run 38580790848). The dispatch-only
`csp-smoke` job has not run there.

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
  verified success needs a `VerifiedEvidence` value that only `verifyItem` creates.

## Phase 3b: the React Chat view (2026-10-14)

The Chat view replaces its placeholder, and the markdown AST has a React mapping. The
Shell and the Trajectory view are not part of this change, and `/` still serves the
vanilla UI.

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
- A success reads `succeeded` only with `VerifiedEvidence` from `verifyItem`, in a live
  structural-audit projection. Otherwise it reads `reported`.
- UNKNOWN, lost, unknown, pending, failed and denied each keep their own label, and none
  of them is in the success tone.
- Lossy output is a chip, and it leaves the state chip as it is.
- An unverified item carries an explicit `unverified` chip.
- The tail count comes only from `store.applied`. Gaps, duplicates and snapshot loads
  add nothing to it.
- The tail pin is kept per store, in a `WeakMap` keyed by the store's actions, so a
  return from the Trajectory view keeps the count.
- Announcements: one debounced announcement of 400 ms, through the shell's announcer.

**Decisions to confirm.**

1. The filters are the four legacy ones: All, Denied, Unknown and Failed. "Reported"
   in the brief is a posture badge, not a filter.
2. Filter counts use `countFilter`, which has the legacy meaning. Denied counts DENY
   decisions and denied results. `governanceCounts` feeds the posture badges.
3. The verified gate needs the projection (live and structural-audit) and the item's
   evidence. This is stricter than the legacy item-only check, and it makes no difference
   to the demo.
4. The gap, window and eviction notices render inside the transcript. The Shell also
   renders the same three notices above the tabs, so two copies appear.
5. A tool card's `data-anchor` is on its wrapper, not on the disclosure element.
6. Turn sections are regions, not headings, so the Shell's heading order is unchanged.

**Open.**

- Duplicate notices, as in decision 4. The fix belongs to `Shell.tsx`, which this
  change does not edit.
- `ui/LiveRegion.test.tsx`, "is the only aria-live or live role in the application
  source", fails. The transcript uses `role="log"` with `aria-live="off"`, as the brief
  requires. Either the scan has to allow that, or the transcript has to stop using the
  log role. The test lives in `src/ui/`, which this change does not edit.
- The Jump control was not exercised in Chrome. The demo has no live arrivals, so its
  count could not be shown. It is covered in happy-dom with stubbed page metrics only.
- This file has no Phase 3a section, although `phase3-parity.md` refers to one.

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
  confirmed word. A lossy success reads `not confirmed` on the unknown tone, and the
  record's name adds `output lossy, not counted as confirmed`. Other states read as the
  core names them. A policy ALLOW is never drawn in the success tone.
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

1. The aside keeps the shell's name, `Governance inspector`, while a record is shown.
   The legacy name was `Record inspector`. The name is set in `Inspector.tsx`, which
   Phase 3c does not own.
2. The group hints are visible text. The toggle primitive has no per-button name, so the
   legacy per-button label is not reproduced.
3. The ledger has one tab stop (a roving stop), not one per row.
4. A row's accessible name includes its payload preview. The legacy name omitted it, so a
   screen reader heard the status but not the payload.
5. The digest's full value is in its `title` only. There is no hidden copy.
6. The tail pin does not survive a view switch. `TabPanel` unmounts the inactive view
   (`ui/Tabs.tsx`), so records applied while Chat is showing are not counted when
   Trajectory reopens. The legacy kept a pin per view. Keeping it needs the pin in the
   store or the shell, and neither is Phase 3c's file.

### Not verified here

- The happy-dom tests do not turn Enter into a click on a button, so they assert the click
  path. The browser walk below did press Enter.
- Scroll anchoring for records that arrive while the reader is away is left to the browser's
  own anchoring. The browser walk did not drive a live stream, so the tail count was
  checked by the unit tests only.
- Screen-reader output is not tested.
- On first load of `/app/` at 390 px the page starts at `scrollY` 825, with the Chat view
  showing. The trajectory view does not cause that: switching to it leaves `scrollY`
  unchanged. The 390 px captures scroll to the top before the capture.

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
guard's regex as a live role. Neither file is Phase 3c's. The test was not changed.

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
