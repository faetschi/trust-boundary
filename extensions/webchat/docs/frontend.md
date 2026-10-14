# Web chat frontend (React scaffold, Phase 1)

Status: **Phase 1 scaffold. Not the product UI.** The React client is served
under `/app/`, next to the vanilla UI. The vanilla UI is still the default at
`/`. Phase 3 is the first point where the React client can replace it. The
decision is in `adr-0001-optional-extension.md`.

The scaffold renders one placeholder heading and the build version, and it
proves the toolchain: TSX, CSS Modules extracted to a static `.css` file, a
build-time version string, and the no-inline-style rule.

## Layout

- `extensions/webchat/frontend/`: the source (`src/`), the scripts
  (`scripts/`), the pinned `package.json` and `package-lock.json`, `.npmrc`
  (`ignore-scripts=true`), `.nvmrc` (24.15.0) and `DEPENDENCIES.md`.
- `extensions/webchat/internal/webchat/dist/`: the committed build output. The
  Go gateway embeds it with `//go:embed all:dist`, including the `.vite/`
  manifest.
- `extensions/webchat/scripts/csp-smoke.mjs`: the opt-in browser CSP check.

## Commands

Run from `extensions/webchat/frontend/` with Node 24.15.0 (`.nvmrc`):

```sh
npm ci --ignore-scripts      # install exactly the lockfile, no install scripts
npm run verify               # typecheck, tests, build, sink check, dependency check
npm run repro                # two clean builds, compared with each other and with the committed dist/
```

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
  rebuild does not match the committed output. A stale or hand-edited file
  cannot pass unnoticed.

Build on Linux (WSL or CI) is canonical. In the Phase 1 run, a Windows build of
the same sources with the same Node version produced byte-identical files (see
the ADR receipts). `internal/webchat/.gitattributes` pins `dist/**` to LF, so a
clone with `core.autocrlf=true` does not change the bytes.

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

Node: `.nvmrc` 24.15.0. `engines` in `package.json` is
`^22.12.0 || ^24.0.0 || >=26.0.0`, which is what Vite 8 and Vitest 5 require.

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
  therefore depends only on the inputs.

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
6. Every URL in `index.html` must be an admitted route.

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

`app_test.go` pins that string.

## Tests

Go, in `internal/webchat/app_test.go`:

- manifest-derived routes are served with the exact content type, the full
  header set, and the exact file bytes; POST is still 405;
- a synthetic build serves only the manifest-reachable files, and the
  unreachable `assets/orphan.js`, the manifest itself and other files are 404;
- traversal, dot-segment, percent-encoding and double-encoding attempts are 404,
  with a positive control in the same test;
- missing, corrupt, dangling, unsupported and traversing manifests, and a shell
  that references an unlisted file, make `/app/` 503 with the reason while `/`
  and `/api/v1/contract` still return 200;
- the shell has no inline `<script>` body, no `<style>`, no `style=`, no `on*=`
  handler and no `javascript:` URL, and every URL is a same-origin `/app/` route;
- the stylesheet has no `@import` and no external or `data:` `url(`;
- every file in the embedded build is served or is the manifest.

Frontend, in `frontend/src/`: `App.test.tsx` renders the component to a string
with `react-dom/server` and checks the heading, the build version, that the
banner has a class, and that there is no `style=` or `on*=` attribute.

Checks outside the tests: `scripts/check-sinks.mjs` fails on `innerHTML`,
`outerHTML`, `insertAdjacentHTML`, `document.write`, `dangerouslySetInnerHTML`,
`style={`, a `style=` attribute, `<style`, `eval` or `new Function` in `src/`,
or on an inline `<script>` in `index.html`. Test files are excluded because
they assert on those strings. `scripts/check-deps.mjs` fails on a non-exact
pin, a missing `ignore-scripts`, a lock entry that does not resolve from the
registry, an install script (with the one darwin-only `fsevents` exception), or
an installed package that declares one.

## Opt-in browser CSP smoke

`scripts/csp-smoke.mjs` is not part of `go test`, `make test` or any core gate.
It uses Node built-ins only and Node 22 or later for the global WebSocket. It
drives a headless Chrome or Chromium over the DevTools protocol, with a temporary
profile.

```sh
# against a demo gateway already running on a fresh port (not 8791)
make extension-webchat-csp CSP_URL=http://127.0.0.1:8793

# or let the script build the gateway and start it on a free port
make extension-webchat-csp
```

The browser comes from `CHROME_BIN`, then `--chrome`, then the usual Chrome,
Chromium and Edge install paths and `PATH`. It must be a Chromium-family binary.
A WSL checkout has no browser by default: run the smoke from Windows against a
gateway running in WSL, which is reachable at `127.0.0.1` through WSL's localhost
forwarding.

For each of `/app/` and `/`, the script asserts:

- zero CSP violations, from both `securitypolicyviolation` events and
  `security`-source console entries;
- zero console errors and zero uncaught exceptions, except a 404 for
  `/favicon.ico`. The pages have no favicon link, so the browser asks for the
  default icon. The count of allowed favicon 404s is printed; it was 1 for
  `/app/` and 0 for `/` in the recorded run;
- zero elements with a `style` attribute and zero `on*` attributes;
- for `/app/`, a CSS Modules class on the banner, a computed background that
  differs from a default probe element, and a linked stylesheet.

Exit codes: `0` PASS; `1` a check failed; `2` usage error (including port 8791,
which is a stale build); `3` `NOT VERIFIED: no browser`; `4` `NOT VERIFIED`
because the gateway or the DevTools connection was unavailable. A PASS is printed
only when every check passed.

The script was exercised against a negative control: a throwaway server with the
same CSP header that serves a page with an inline `style=`, an `onclick=`, an
inline script the CSP blocks, a same-origin script that calls `console.error`,
and a same-origin script that throws. It exited `1` and reported each category on
both pages.

## Phase plan

- **Phase 1 (this change):** the scaffold, the manifest-derived `/app/` route,
  the tests, the reproducibility gate, and the opt-in CSP smoke. No product UI.
  The vanilla UI is unchanged and remains the default.
- **Phase 2:** port the pure logic to TypeScript: `store`, `stream`, `backoff`,
  `markdown`, `filters`, `trajectory`, `anchor`, `format` and `stop-sequence`.
  The existing 258 Node tests are the parity net, and behaviour may not change.
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
`adr-0001-optional-extension.md`, §(v), the P1 status table. The GitHub Actions
jobs have not run yet.
