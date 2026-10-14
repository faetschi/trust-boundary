# Frontend dependencies

Every direct dependency is pinned to an exact version in `package.json` and
resolved in `package-lock.json`. Nothing is installed with `^` or `~`. The
policy and the gate that enforces it are in `../docs/frontend.md`.

Checked on 2026-10-14 with Node 24.15.0 on Linux (WSL) and with `npm` 11.12.1.
The registry metadata was read with `npm view`.

## Direct dependencies (9)

| Package | Pinned | Licence | Role | Shipped in `dist/` |
|---|---|---|---|---|
| `react` | 19.3.0 | MIT | UI runtime | yes |
| `react-dom` | 19.3.0 | MIT | DOM renderer | yes |
| `@vitejs/plugin-react` | 6.1.2 | MIT | Vite JSX and Fast Refresh plugin (build only) | no |
| `vite` | 8.3.4 | MIT | build tool and manifest writer (build only) | no |
| `vitest` | 5.0.4 | MIT | unit tests (test only) | no |
| `typescript` | 7.0.2 | Apache-2.0 | type checker, `tsc --noEmit` (check only) | no |
| `@types/react` | 19.3.0 | MIT | types (check only) | no |
| `@types/react-dom` | 19.3.0 | MIT | types (check only) | no |
| `happy-dom` | 21.0.1 | MIT | DOM for the component tests, added in Phase 3a (test only) | no |

Runtime is `react` and `react-dom` only. `react-dom` pulls in `scheduler`
0.28.0 (MIT). Everything else is build, test or type tooling.

## Locked packages (97)

- Runtime (not dev-only): 3 (`react`, `react-dom`, `scheduler`).
- Dev-only: 94.
- Installed on Linux x64: 53. The other 44 are optional, platform-gated native
  packages (`lightningcss-*`, `@rolldown/binding-*`, `@typescript/typescript-*`,
  and `fsevents`). npm does not install them on this platform.

Licence census across all 97 locked packages, from the `license` field that
`package-lock.json` records for each one:

| Licence | Count |
|---|---|
| MIT | 59 |
| Apache-2.0 | 23 |
| MPL-2.0 | 12 |
| ISC | 1 |
| BSD-3-Clause | 1 |
| BSD-2-Clause | 1 |

The MPL-2.0 packages are all `lightningcss` builds, a dev-time CSS tool. The
built bundle contains no `lightningcss` code: a search of the committed
`dist/assets/*.js` finds zero matches.

## Install scripts and audit

- `.npmrc` sets `ignore-scripts=true`. CI and the make target install with
  `npm ci --ignore-scripts`.
- The lockfile has one `hasInstallScript` entry: `node_modules/fsevents` 2.3.3,
  darwin-only and optional, with an install script (`node-gyp rebuild`). It is the
  only install-script exception that `scripts/check-deps.mjs` accepts. The gate
  pins its name, version, sha512 integrity, `os` and scripts, and it decides on
  the lock entry, not on whether fsevents is installed. An installed fsevents
  whose manifest differs from the pinned one fails the gate.
- Every locked package has a sha512 integrity, and a `resolved` URL that is
  exactly `https://registry.npmjs.org/<name>/-/<basename>-<version>.tgz`. An
  alias (a lock name that differs from its path), a link, a git, file or http
  source, and an installed package whose name or version differs from the lock
  all fail the gate. `check-deps.mjs --self-test` proves each of these cases.
- `prepare` scripts in `lightningcss` and `tinyexec` are not install scripts. npm
  does not run `prepare` for registry tarballs, so they are not checked.
- `npm audit` at the time of this record reported **0 vulnerabilities** for
  production dependencies and for the full tree.

## Why these and not others

- No component library and no CSS-in-JS. That was decided in
  `../docs/adr-0001-optional-extension.md`: the CSP is `style-src 'self'`.
- `happy-dom` 21.0.1 is the one DOM library, a dev dependency for the component
  tests (Phase 3a). The keyboard, focus, drawer and announcer behaviour cannot be
  tested with `react-dom/server` strings. It has no install script, and it adds
  nine locked packages: `happy-dom` itself and its eight dependencies, `ws`,
  `entities` (BSD-2-Clause), `whatwg-mimetype`, `buffer-image-size`,
  `undici-types` (through `@types/node`), `@types/node`, `@types/ws` and
  `@types/whatwg-mimetype`. None is in the shipped bundle: Vite bundles only what
  `src/` imports, and the tests are not in `src/`'s production graph. jsdom was
  not chosen because its tree is considerably larger.
- No ESLint. The sink and style checks are small Node scripts
  (`scripts/check-sinks.mjs`). Adding ESLint would bring a large tree for two
  grep-level rules.

## Updating

Change a pin deliberately in `package.json`, run `npm install --ignore-scripts`
with the pinned Node, then run `npm run repro` and then `npm run verify`. Repro
runs first, as in the `extension-webchat-frontend-verify` make target, so it
compares two clean rebuilds with the `dist/` committed at HEAD. Commit the
lockfile, this file and the rebuilt `dist/` together. Re-check `npm audit` and
the licence census.
